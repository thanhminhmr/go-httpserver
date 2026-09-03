/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Context.NewResponse: status boundaries and state reset
// ============================================================================

// NewResponse must panic for statuses outside [200, 599] and accept everything
// in that range. A response whose NewResponse was never called has Status()==0.
func TestContext_NewResponse_StatusBoundaries(t *testing.T) {
	t.Run("below_200_panics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		assert.Panics(t, func() {
			(&Context{writer: rec}).NewResponse(199)
		})
	})
	t.Run("200_to_599_succeed", func(t *testing.T) {
		for _, status := range []int{200, 201, 204, 301, 404, 500, 599} {
			rec := httptest.NewRecorder()
			assert.NotPanics(t, func() {
				r := (&Context{writer: rec}).NewResponse(status)
				assert.Equal(t, status, r.Status())
			}, "status %d", status)
		}
	})
	t.Run("above_599_panics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		assert.Panics(t, func() {
			(&Context{writer: rec}).NewResponse(600)
		})
	})
}

// A second NewResponse must clear headers/body/marshaller set by an earlier
// NewResponse so handlers cannot accidentally inherit stale state.
func TestContext_NewResponse_ClearsPreviousResponseState(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}

	prev := ctx.NewResponse(http.StatusOK)
	prev.Header().Set("X-Old", "value")
	prev.JsonBody(map[string]string{"k": "v"}) // sets marshaller=JSON
	require.Equal(t, "value", rec.Header().Get("X-Old"))
	require.NotNil(t, ctx.body)
	require.Equal(t, marshallerIsJson, ctx.marshaller)

	ctx.NewResponse(http.StatusNoContent)
	assert.Equal(t, http.StatusNoContent, ctx.status)
	assert.Nil(t, ctx.body)
	assert.Equal(t, marshallerIsDirect, ctx.marshaller)
	assert.Empty(t, rec.Header(), "headers must be cleared by NewResponse")
}

// ============================================================================
// Context.Response: existence reporting
// ============================================================================

// Response must return the zero Response and false before NewResponse is
// called, and a live handle and true afterward.
func TestContext_Response_ReportsExistence(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}

	resp, ok := ctx.Response()
	assert.False(t, ok, "no response exists before NewResponse")
	assert.Equal(t, Response{}, resp, "zero Response must be returned when none exists")

	ctx.NewResponse(http.StatusTeapot)
	resp, ok = ctx.Response()
	assert.True(t, ok, "response exists after NewResponse")
	assert.Same(t, ctx, resp.ctx)
	assert.Equal(t, http.StatusTeapot, resp.Status())
}

// The zero Response behaves like a nil pointer: every method call panics.
func TestPanic_ZeroResponseMethods_Panic(t *testing.T) {
	assert.Panics(t, func() { _ = Response{}.Status() }, "Status")
	assert.Panics(t, func() { _ = Response{}.Header() }, "Header")
	assert.Panics(t, func() { _ = Response{}.Body() }, "Body")
	assert.Panics(t, func() { Response{}.Cookie(http.Cookie{Name: "n"}) }, "Cookie")
	assert.Panics(t, func() { Response{}.BytesBody(nil) }, "BytesBody")
	assert.Panics(t, func() { Response{}.StringBody("") }, "StringBody")
	assert.Panics(t, func() { Response{}.StreamBody(nil) }, "StreamBody")
	assert.Panics(t, func() { Response{}.PlainTextBody("") }, "PlainTextBody")
	assert.Panics(t, func() { Response{}.OctetsBody(nil) }, "OctetsBody")
	assert.Panics(t, func() { Response{}.JsonBody(nil) }, "JsonBody")
}

// The zero Response is loggable: it serializes as an empty object instead of
// panicking.
func TestResponse_ZeroValue_LogsAsEmptyObject(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	require.NotPanics(t, func() {
		logger.Info().Object("response", Response{}).Msg("serialized")
	})

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(logBuf.Bytes(), &decoded))
	respObj, ok := decoded["response"].(map[string]any)
	require.True(t, ok, "zero Response should serialize as an empty object")
	assert.Empty(t, respObj, "zero Response should not emit any fields")
}

// ============================================================================
// Response handle: Status / Header / Body / Cookie
// ============================================================================

func TestResponse_StatusGetter(t *testing.T) {
	rec := httptest.NewRecorder()
	r := (&Context{writer: rec}).NewResponse(http.StatusTeapot)
	assert.Equal(t, http.StatusTeapot, r.Status())
}

func TestResponse_HeaderGetter(t *testing.T) {
	rec := httptest.NewRecorder()
	r := (&Context{writer: rec}).NewResponse(http.StatusOK)
	r.Header().Set("X-Test", "value")
	assert.Equal(t, "value", r.Header().Get("X-Test"))
}

// Body() returns the configured body value, or nil on a fresh response.
func TestResponse_Body_ReturnsConfiguredBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	r := ctx.NewResponse(http.StatusOK)

	require.Nil(t, r.Body(), "fresh response body must be nil")

	r.StringBody("hello")
	assert.Equal(t, "hello", r.Body())

	data := []byte{1, 2, 3}
	r.BytesBody(data)
	assert.Equal(t, data, r.Body())
}

func TestResponse_CookieSetter(t *testing.T) {
	rec := httptest.NewRecorder()
	r := (&Context{writer: rec}).NewResponse(http.StatusOK)
	r.Cookie(http.Cookie{Name: "session", Value: "abc"})
	assert.Len(t, rec.Header().Values("Set-Cookie"), 1)
}

// Cookie must append rather than replace: multiple Cookie calls produce
// multiple Set-Cookie headers, preserving all cookies.
func TestResponse_Cookie_AppendsCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	r := (&Context{writer: rec}).NewResponse(http.StatusOK)
	r.Cookie(http.Cookie{Name: "a", Value: "1"})
	r.Cookie(http.Cookie{Name: "b", Value: "2"})
	assert.Equal(t, []string{"a=1", "b=2"}, rec.Header().Values("Set-Cookie"))
}

// ============================================================================
// Context context.Context delegation (Deadline / Done / Err / Value)
// ============================================================================

func TestContext_Deadline_DelegatesToRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	ctx := &Context{request: req, writer: rec}
	dl, ok := ctx.Deadline()
	assert.False(t, ok)
	assert.True(t, dl.IsZero())
}

func TestContext_DoneAndErr_DelegateToRequest(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(rootCtx)
	rec := httptest.NewRecorder()
	ctx := &Context{request: req, writer: rec}

	done := ctx.Done()
	require.NotNil(t, done)
	require.Nil(t, ctx.Err(), "Err before cancel should be nil")

	cancel()
	<-done
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestContext_Value_DelegatesToRequest(t *testing.T) {
	type ctxKey struct{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "stored"))
	rec := httptest.NewRecorder()
	ctx := &Context{request: req, writer: rec}
	assert.Equal(t, "stored", ctx.Value(ctxKey{}))
}

// ============================================================================
// Context.writeResponse body-type matrix
// ============================================================================

func TestContext_writeResponse_NilBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec, status: http.StatusNoContent}
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestContext_writeResponse_BytesBody(t *testing.T) {
	rec := httptest.NewRecorder()
	data := []byte("raw bytes")
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).BytesBody(data)
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, data, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestContext_writeResponse_StringBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StringBody("a string")
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "a string", rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

// The recorder is not a *StreamWriter, so writeResponse wraps it on the fly
// (the bypass path used when the Router runs without the server's writer).
func TestContext_writeResponse_StreamBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StreamBody(func(w *StreamWriter) error {
		_, err := w.Write([]byte("streamed"))
		return err
	})
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "streamed", rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

// A stream-body error occurs after [http.ResponseWriter.WriteHeader]; the
// status is already on the wire and cannot be recovered. The error is logged,
// then writeResponse panics with [http.ErrAbortHandler] so the wrapping
// server/net/http silently closes the connection. The committed status stays
// observable on the recorder.
func TestContext_writeResponse_StreamBody_Error(t *testing.T) {
	rec := httptest.NewRecorder()
	streamErr := errors.New("stream write failed")
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StreamBody(func(*StreamWriter) error {
		return streamErr
	})
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		ctx.writeResponse(context.Background())
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// StreamBody with an installed StreamWriter (the server path): Write flows
// through the response accounting while Flush reaches the underlying
// connection flush. Each Flush call must land exactly once on the underlying
// flusher, between the surrounding Writes.
func TestContext_writeResponse_StreamBody_Flushes(t *testing.T) {
	fake := newFakeResponseWriter()
	underlying := &flushRecorderWriter{ResponseWriter: fake}
	streamWriter := &StreamWriter{writer: underlying}
	ctx := &Context{writer: streamWriter}
	ctx.NewResponse(http.StatusOK).StreamBody(func(w *StreamWriter) error {
		if _, err := w.Write([]byte("first ")); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
		_, err := w.Write([]byte("second"))
		return err
	})
	require.NotPanics(t, func() {
		ctx.writeResponse(context.Background())
	})
	assert.Equal(t, http.StatusOK, streamWriter.status)
	assert.Equal(t, len("first second"), streamWriter.bytesWritten)
	assert.Equal(t, [][]byte{[]byte("first "), []byte("second")}, fake.writes)
	assert.Equal(t, 1, underlying.flushes)
}

// StreamWriter.Flush fails with [http.ErrNotSupported] when the underlying
// connection cannot flush. The callback decides whether a flush failure is
// fatal: returning it makes writeResponse abort the connection with
// [http.ErrAbortHandler], preserving the already-committed status.
func TestContext_writeResponse_StreamBody_FlushUnsupported_Aborts(t *testing.T) {
	streamWriter := &StreamWriter{writer: newFakeResponseWriter()}
	ctx := &Context{writer: streamWriter}
	var flushErr error
	ctx.NewResponse(http.StatusOK).StreamBody(func(w *StreamWriter) error {
		flushErr = w.Flush()
		return flushErr
	})
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		ctx.writeResponse(context.Background())
	})
	require.Error(t, flushErr)
	assert.ErrorIs(t, flushErr, http.ErrNotSupported)
	assert.Equal(t, http.StatusOK, streamWriter.status)
}

func TestContext_writeResponse_PlainTextBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).PlainTextBody("hello plain")
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello plain", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestContext_writeResponse_OctetsBody(t *testing.T) {
	rec := httptest.NewRecorder()
	data := []byte{0x00, 0x01, 0x02, 0xFF}
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).OctetsBody(data)
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, data, rec.Body.Bytes())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
}

func TestContext_writeResponse_JsonBody(t *testing.T) {
	rec := httptest.NewRecorder()
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	p := payload{Name: "alice", Age: 30}
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusCreated).JsonBody(p)
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	var result payload
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, p, result)
}

// JsonBody(nil) must marshal to the JSON null literal with the JSON content
// type, not be treated as "no body".
func TestContext_writeResponse_JsonBody_Nil(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).JsonBody(nil)
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "null", rec.Body.String())
}

// ============================================================================
// Context.writeResponse error paths
// ============================================================================

// JSON marshal failure must clear stale headers, write 500 and leave the body
// empty. The header set just before writeResponse (after the NewResponse clear)
// must be gone after the failure path runs.
func TestContext_writeResponse_JsonMarshalError(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).JsonBody(make(chan int))
	ctx.writer.Header().Set("X-Added", "value")
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Added"))
	assert.Empty(t, rec.Body.Bytes(), "marshal failure body must be empty")
}

// Unsupported body type must clear headers and write 500.
func TestContext_writeResponse_UnknownBodyType(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec, status: http.StatusOK, body: 12345}
	ctx.writer.Header().Set("X-Added", "value")
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Added"))
}

// Body-write errors occur after [http.ResponseWriter.WriteHeader]; the status
// is already committed and cannot be replaced. writeResponse logs the error and
// then panics with [http.ErrAbortHandler] so the wrapping server/net/http
// silently closes the connection.
func TestContext_writeResponse_BodyWriteError_AbortsConnection(t *testing.T) {
	fw := &failingResponseWriter{writeErr: errors.New("write failed")}
	ctx := &Context{writer: fw}
	ctx.NewResponse(http.StatusOK).BytesBody([]byte("data"))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		ctx.writeResponse(context.Background())
	})
}

// TestContext_writeResponse_JsonBody_WriteError covers the JSON-marshaller
// Write-error arm (response.go:113). The marshal succeeds, WriteHeader commits
// 200, then the body Write fails: writeResponse logs and re-panics with
// [http.ErrAbortHandler]. Mirrors the BytesBody test above for the JSON path.
func TestContext_writeResponse_JsonBody_WriteError(t *testing.T) {
	fw := &failingResponseWriter{writeErr: errors.New("write failed")}
	ctx := &Context{writer: fw}
	ctx.NewResponse(http.StatusOK).JsonBody(map[string]string{"k": "v"})
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		ctx.writeResponse(context.Background())
	})
}

// TestContext_writeResponse_StringBody_WriteError covers the string-body
// Write-error arm (response.go:138). Same abort contract as the JSON/bytes
// paths; isolates the string branch which shares one panic with them.
func TestContext_writeResponse_StringBody_WriteError(t *testing.T) {
	fw := &failingResponseWriter{writeErr: errors.New("write failed")}
	ctx := &Context{writer: fw}
	ctx.NewResponse(http.StatusOK).StringBody("a string")
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		ctx.writeResponse(context.Background())
	})
}

// TestResponse_MarshalZerologObject_IncludesHeaders covers the header branch in
// [Response.MarshalZerologObject]: when the response carries at least one
// header, the serialized object must include a `header` field, alongside the
// always-emitted `status`.
func TestResponse_MarshalZerologObject_IncludesHeaders(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	response := ctx.NewResponse(http.StatusTeapot)
	response.Header().Set("X-Test", "value")

	logger.Info().Object("response", response).Msg("serialized")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(logBuf.Bytes(), &decoded))

	respObj, ok := decoded["response"].(map[string]any)
	require.True(t, ok, "response object should be present")
	assert.EqualValues(t, http.StatusTeapot, respObj["status"])

	headerObj, ok := respObj["header"].(map[string]any)
	require.True(t, ok, "header field should be serialized")
	assert.Equal(t, []any{"value"}, headerObj["X-Test"])
}

// TestResponse_MarshalZerologObject_IncludesBody covers the body branch in
// [Response.MarshalZerologObject] (`if r.ctx.body != nil { e.Any("body", …) }`).
// IncludesHeaders above intentionally leaves body nil; here a body is set so the
// branch executes and `body` appears alongside `status` in the serialized form.
func TestResponse_MarshalZerologObject_IncludesBody(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	response := ctx.NewResponse(http.StatusTeapot)
	response.Header().Set("X-Test", "value")
	response.JsonBody(map[string]string{"k": "v"})

	logger.Info().Object("response", response).Msg("serialized")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(logBuf.Bytes(), &decoded))

	respObj, ok := decoded["response"].(map[string]any)
	require.True(t, ok, "response object should be present")
	assert.EqualValues(t, http.StatusTeapot, respObj["status"])

	bodyObj, ok := respObj["body"].(map[string]any)
	require.True(t, ok, "body field should be serialized when body is set")
	assert.Equal(t, "v", bodyObj["k"])
}

// ============================================================================
// helpers
// ============================================================================

// failingResponseWriter is a minimal http.ResponseWriter whose Write returns a
// configured error. Header/WriteHeader operate normally so we can isolate the
// body-write error path.
type failingResponseWriter struct {
	header   http.Header
	status   int
	writeErr error
}

func (f *failingResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func (f *failingResponseWriter) Write([]byte) (int, error) { return 0, f.writeErr }

func (f *failingResponseWriter) WriteHeader(statusCode int) { f.status = statusCode }
