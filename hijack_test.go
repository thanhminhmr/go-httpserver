/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============ helpers ============

// closeRecordingConn wraps a net.Conn and counts Close calls so tests can
// verify that the committed hijack closes the connection exactly once.
type closeRecordingConn struct {
	net.Conn
	closed int
}

func (c *closeRecordingConn) Close() error {
	c.closed++
	return c.Conn.Close()
}

// closeFailingConn wraps a net.Conn whose Close always fails, for the
// close-error logging path of the committed hijack.
type closeFailingConn struct{ net.Conn }

func (c *closeFailingConn) Close() error { return errors.New("close failed") }

// hijackableWriter is an http.ResponseWriter whose connection can be
// hijacked. Reads on the hijacked ReadWriter surface unreadRequest; writes
// flow to the client end of the pipe.
type hijackableWriter struct {
	header     http.Header
	statuses   []int
	writes     [][]byte
	conn       *closeRecordingConn
	readWriter *bufio.ReadWriter
	hijacked   bool
	hijackErr  error
}

func newHijackableWriter(t *testing.T) (*hijackableWriter, net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	w := &hijackableWriter{
		header: http.Header{},
		conn:   &closeRecordingConn{Conn: serverConn},
		readWriter: bufio.NewReadWriter(
			bufio.NewReader(strings.NewReader("unread request bytes")),
			bufio.NewWriter(serverConn),
		),
	}
	return w, clientConn
}

func (w *hijackableWriter) Header() http.Header { return w.header }

func (w *hijackableWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
}

func (w *hijackableWriter) Write(body []byte) (int, error) {
	w.writes = append(w.writes, body)
	return len(body), nil
}

func (w *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijackErr != nil {
		return nil, nil, w.hijackErr
	}
	w.hijacked = true
	return w.conn, w.readWriter, nil
}

// ============ Context.Hijack: pending takeover ============

func TestContext_Hijack_RecordsPendingTakeover(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	ctx.NewResponse(http.StatusOK).StringBody("pending response")

	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	assert.False(t, w.hijacked, "connection not taken over until write time")
	assert.True(t, ctx.Hijacked(), "takeover pending")
	_, hasResponse := ctx.Response()
	assert.False(t, hasResponse, "pending response discarded")
	assert.Equal(t, 0, ctx.status)
	assert.Empty(t, w.statuses, "nothing written by recording the takeover")
	assert.Empty(t, w.writes)
}

// Hijack(nil) records no takeover: a nil body is a bug loud enough to panic.
func TestContext_Hijack_NilBody_Panics(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: httptest.NewRecorder()}
	assert.PanicsWithValue(t, "BUG: nil hijack body", func() {
		_ = ctx.Hijack(nil)
	})
	assert.False(t, ctx.Hijacked(), "nothing recorded")
}

func TestContext_Hijack_Twice_Overwrites(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		t.Error("first hijack body must not run")
		return nil
	}))
	type pipeResult struct {
		data []byte
		err  error
	}
	pipeCh := make(chan pipeResult, 1)
	go func() {
		data, err := io.ReadAll(clientConn)
		pipeCh <- pipeResult{data: data, err: err}
	}()
	require.NoError(t, ctx.Hijack(func(conn net.Conn, rw *bufio.ReadWriter) error {
		_, err := rw.WriteString("second")
		if err != nil {
			return err
		}
		return rw.Flush()
	}))
	ctx.writeResponse(context.Background())
	assert.True(t, w.hijacked, "second body committed")
	result := <-pipeCh
	assert.Equal(t, "second", string(result.data), "only the second body ran")
	assert.Equal(t, 1, w.conn.closed)
}

// ============ Context.Hijack: commit at write time ============

func TestContext_Hijack_Commit(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	ctx.NewResponse(http.StatusOK).StringBody("pending response")

	type pipeResult struct {
		data []byte
		err  error
	}
	pipeCh := make(chan pipeResult, 1)
	go func() {
		data, err := io.ReadAll(clientConn)
		pipeCh <- pipeResult{data: data, err: err}
	}()

	var readUnread string
	require.NoError(t, ctx.Hijack(func(conn net.Conn, rw *bufio.ReadWriter) error {
		assert.Same(t, w.conn, conn)
		data, err := io.ReadAll(rw.Reader)
		if err != nil {
			return err
		}
		readUnread = string(data)
		if _, err = rw.WriteString("body-marker"); err != nil {
			return err
		}
		return rw.Flush()
	}))
	assert.False(t, w.hijacked, "takeover deferred until write time")

	ctx.writeResponse(context.Background())
	assert.True(t, w.hijacked, "takeover committed by writeResponse")

	result := <-pipeCh
	assert.Equal(t, "body-marker", string(result.data), "client received body write")
	assert.Equal(t, "unread request bytes", readUnread, "body received unread request bytes")
	assert.Equal(t, 1, w.conn.closed, "connection closed exactly once")
	assert.Empty(t, w.statuses, "no status written by the framework")
	assert.Empty(t, w.writes, "no body written by the framework")
}

func TestContext_Hijack_ThroughStreamWriter(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	streamWriter := &StreamWriter{writer: w}
	ctx := &Context{request: req, writer: streamWriter}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	assert.NotNil(t, streamWriter.writer, "through StreamWriter: still deferred")
	ctx.writeResponse(context.Background())
	assert.True(t, w.hijacked)
	assert.Nil(t, streamWriter.writer, "writer detached by the committed hijack")
	assert.Nil(t, ctx.request, "Context cleared after the takeover")
	assert.Nil(t, ctx.writer)
}

func TestContext_Hijack_WriteResponseSkipped(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	ctx.writeResponse(context.Background())
	assert.Empty(t, w.statuses, "hijack committed instead of a response")
	ctx.writeResponse(context.Background())
	assert.Empty(t, w.statuses, "second writeResponse is a no-op (no 500 fallback)")
	assert.Empty(t, w.writes)
}

// ============ Context.Hijack: failure paths ============

func TestContext_Hijack_NonHijackableWriter_Error(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	ctx := &Context{request: req, writer: rec}
	assert.False(t, ctx.Hijacked())
	bodyCalled := false
	err := ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		bodyCalled = true
		return nil
	})
	assert.ErrorIs(t, err, http.ErrNotSupported)
	assert.False(t, bodyCalled)
	assert.False(t, ctx.Hijacked())
	response := ctx.NewResponse(http.StatusOK)
	response.StringBody("fallback")
	ctx.writeResponse(context.Background())
	assert.Equal(t, http.StatusOK, rec.Code, "handler can fall back to a response")
}

func TestContext_Hijack_CommitFailure_LogsAndWrites500(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	w.hijackErr = errors.New("hijack refused")
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	ctx := &Context{request: req, writer: &StreamWriter{writer: w}}
	ctx.NewResponse(http.StatusTeapot).Header().Set("X-Stale", "yes")
	bodyCalled := false
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		bodyCalled = true
		return nil
	}))
	ctx.writeResponse(req.Context())
	assert.False(t, bodyCalled, "hijack body must not run")
	assert.Equal(t, []int{http.StatusInternalServerError}, w.statuses, "empty 500 response")
	assert.Empty(t, w.header, "stale headers cleared")
	assert.Contains(t, logBuf.String(), "Failed to hijack connection")
}

func TestContext_Hijack_CommitCapabilityVanished_LogsAndWrites500(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	rec := httptest.NewRecorder()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	ctx := &Context{request: req, writer: w}
	bodyCalled := false
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		bodyCalled = true
		return nil
	}))
	ctx.writer = &StreamWriter{writer: rec}
	ctx.writeResponse(req.Context())
	assert.False(t, bodyCalled, "hijack body must not run")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "empty 500 response")
	assert.Contains(t, logBuf.String(), "Failed to hijack connection")
}

func TestContext_Hijack_BodyError_LoggedAndClosed(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return errors.New("body failed") }))
	ctx.writeResponse(req.Context())
	assert.Equal(t, 1, w.conn.closed, "connection closed")
	assert.Contains(t, logBuf.String(), "Failed to handle hijacked connection")
}

// A close error of the hijacked connection is logged after the body ran.
func TestContext_Hijack_CloseError_Logged(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	w.conn = &closeRecordingConn{Conn: &closeFailingConn{Conn: w.conn.Conn}}
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	ctx.writeResponse(req.Context())
	assert.Contains(t, logBuf.String(), "Failed while closing hijacked connection")
}

func TestContext_Hijack_BodyPanic_ClosesAndPropagates(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { panic("boom") }))
	require.PanicsWithValue(t, "boom", func() { ctx.writeResponse(context.Background()) })
	assert.Equal(t, 1, w.conn.closed, "connection closed during panic unwind")
	assert.Nil(t, ctx.request, "Context cleared before the body ran")
}

// ============ cancel by response ============

func TestContext_NewResponse_CancelsPendingHijack(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	bodyCalled := false
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		bodyCalled = true
		return nil
	}))
	ctx.NewResponse(http.StatusOK).StringBody("fallback")
	assert.False(t, ctx.Hijacked(), "pending takeover canceled")
	ctx.writeResponse(context.Background())
	assert.False(t, w.hijacked, "connection never taken over")
	assert.False(t, bodyCalled, "hijack body never ran")
	assert.Equal(t, []int{http.StatusOK}, w.statuses)
	assert.Equal(t, [][]byte{[]byte("fallback")}, w.writes)
	assert.Equal(t, 0, w.conn.closed)
}

func TestResponse_StaleHandle_AfterHijack_Panics(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	response := ctx.NewResponse(http.StatusOK)
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	require.True(t, ctx.Hijacked())
	// Hijack invalidated the handle: using it panics, and the takeover stays
	// pending. A handler that changed its mind must call NewResponse instead.
	assert.PanicsWithValue(t, "BUG: stale response handle", func() { response.StringBody("stale") })
	assert.True(t, ctx.Hijacked(), "takeover still pending after the panicking setter")
	ctx.writeResponse(context.Background())
	assert.True(t, w.hijacked, "takeover committed by writeResponse")
	assert.Empty(t, w.statuses, "no HTTP response written")
	assert.Equal(t, 1, w.conn.closed)
}

// ============ middleware interaction ============

func TestMiddleware_ObservesHijack_AfterNext(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var afterHijacked, hasResponse bool
	router := newTestRouter().Group(func(ctx *Context, next func()) {
		next()
		afterHijacked = ctx.Hijacked()
		_, hasResponse = ctx.Response()
	})
	router.Handle("GET /", func(ctx *Context) {
		require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	})
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	router.serveMux.ServeHTTP(w, req)
	assert.True(t, afterHijacked, "middleware observes the pending takeover after next")
	assert.False(t, hasResponse, "middleware sees no response to replace")
	assert.True(t, w.hijacked, "takeover committed after the chain returned")
	assert.Empty(t, w.statuses)
	assert.Empty(t, w.writes)
}

func TestMiddleware_VetoesHijack_ReplacesResponse(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	bodyCalled := false
	router := newTestRouter().Group(func(ctx *Context, next func()) {
		next()
		if ctx.Hijacked() {
			ctx.NewResponse(http.StatusForbidden).StringBody("upgrade not allowed")
		}
	})
	router.Handle("GET /", func(ctx *Context) {
		require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
			bodyCalled = true
			return nil
		}))
	})
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	router.serveMux.ServeHTTP(w, req)
	assert.False(t, w.hijacked, "connection never taken over")
	assert.False(t, bodyCalled, "hijack body never ran")
	assert.Equal(t, []int{http.StatusForbidden}, w.statuses)
	assert.Equal(t, [][]byte{[]byte("upgrade not allowed")}, w.writes)
	assert.Equal(t, 0, w.conn.closed)
}

// ============ server integration ============

func TestHTTPServer_Hijack_LogsHijackedResponse(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	var hijackErr error
	router := Router{serveMux: http.NewServeMux()}
	router.Handle("GET /", func(ctx *Context) {
		hijackErr = ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil })
	})
	server := &httpServer{serveMux: router.serveMux}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	server.ServeHTTP(w, req)
	require.NoError(t, hijackErr)
	assert.True(t, w.hijacked)
	assert.Empty(t, w.statuses, "no HTTP response written for a hijacked connection")
	assert.Empty(t, w.writes)
	logs := logBuf.String()
	assert.Contains(t, logs, "Connection hijacked")
	assert.NotContains(t, logs, `"message":"Response"`)
}

func TestHTTPServer_PanicAfterHijack_AbortsConnection(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	router := Router{serveMux: http.NewServeMux()}
	router.Handle("GET /", func(ctx *Context) {
		require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { panic("boom after hijack") }))
	})
	server := &httpServer{serveMux: router.serveMux}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { server.ServeHTTP(w, req) })
	assert.True(t, w.hijacked, "takeover committed before the panic")
	assert.Empty(t, w.statuses, "no 500 written for a hijacked connection")
	logs := logBuf.String()
	assert.Contains(t, logs, "Recovered from panic")
	assert.Contains(t, logs, "Connection hijacked")
}

func TestHTTPServer_PanicDuringMiddleware_HijackNeverCommits(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	bodyCalled := false
	router := Router{serveMux: http.NewServeMux()}.Group(func(ctx *Context, next func()) {
		next()
		panic("boom in middleware")
	})
	router.Handle("GET /", func(ctx *Context) {
		require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
			bodyCalled = true
			return nil
		}))
	})
	server := &httpServer{serveMux: router.serveMux}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	server.ServeHTTP(w, req)
	assert.False(t, w.hijacked, "takeover never committed")
	assert.False(t, bodyCalled, "hijack body never ran")
	assert.Equal(t, []int{http.StatusInternalServerError}, w.statuses, "panic becomes 500")
	assert.Equal(t, 0, w.conn.closed, "connection untouched")
	logs := logBuf.String()
	assert.Contains(t, logs, `"message":"Response"`)
	assert.NotContains(t, logs, "Connection hijacked")
}
