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
// verify that Hijack closes the connection exactly once.
type closeRecordingConn struct {
	net.Conn
	closed int
}

func (c *closeRecordingConn) Close() error {
	c.closed++
	return c.Conn.Close()
}

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

// ============ Context.Hijack: success path ============

func TestContext_Hijack_Success(t *testing.T) {
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
	err := ctx.Hijack(func(conn net.Conn, rw *bufio.ReadWriter) error {
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
	})
	require.NoError(t, err)

	result := <-pipeCh
	assert.Equal(t, "body-marker", string(result.data), "client received body write")
	assert.Equal(t, "unread request bytes", readUnread, "body received unread request bytes")
	assert.Equal(t, 1, w.conn.closed, "connection closed exactly once")
	assert.True(t, w.hijacked)
	assert.True(t, ctx.Hijacked())
	_, hasResponse := ctx.Response()
	assert.False(t, hasResponse, "pending response discarded")
	assert.Empty(t, w.statuses, "no status written by the framework")
	assert.Empty(t, w.writes, "no body written by the framework")
}

func TestContext_Hijack_ThroughStreamWriter(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	streamWriter := &StreamWriter{writer: w}
	ctx := &Context{request: req, writer: streamWriter}
	err := ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil })
	require.NoError(t, err)
	assert.True(t, w.hijacked)
	assert.True(t, streamWriter.hijacked)
	assert.True(t, ctx.Hijacked())
}

func TestContext_Hijack_WriteResponseSkipped(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	ctx.writeResponse(context.Background())
	assert.Empty(t, w.statuses, "hijacked response write skipped (no 500 fallback)")
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

func TestContext_Hijack_UnderlyingError_StateUntouched(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	hijackErr := errors.New("hijack refused")
	w.hijackErr = hijackErr
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	ctx.NewResponse(http.StatusOK).StringBody("existing")
	bodyCalled := false
	err := ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error {
		bodyCalled = true
		return nil
	})
	assert.ErrorIs(t, err, hijackErr)
	assert.False(t, bodyCalled)
	assert.False(t, ctx.Hijacked())
	_, hasResponse := ctx.Response()
	assert.True(t, hasResponse, "response state survives a failed hijack")
	assert.Equal(t, http.StatusOK, ctx.status)
}

func TestContext_Hijack_BodyError_LoggedReturnedAndClosed(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf).Level(zerolog.InfoLevel)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	ctx := &Context{request: req, writer: w}
	bodyErr := errors.New("body failed")
	err := ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return bodyErr })
	assert.ErrorIs(t, err, bodyErr)
	assert.Equal(t, 1, w.conn.closed, "connection closed")
	assert.Contains(t, logBuf.String(), "Failed to handle hijacked connection")
}

func TestContext_Hijack_BodyPanic_ClosesAndPropagates(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.PanicsWithValue(t, "boom", func() {
		_ = ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { panic("boom") })
	})
	assert.Equal(t, 1, w.conn.closed, "connection closed during panic unwind")
	assert.True(t, ctx.Hijacked())
}

// ============ Context.Hijack: misuse panics ============

func TestContext_Hijack_Twice_Panics(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	require.Panics(t, func() {
		_ = ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil })
	})
}

func TestContext_NewResponse_AfterHijack_Panics(t *testing.T) {
	w, clientConn := newHijackableWriter(t)
	defer clientConn.Close()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	ctx := &Context{request: req, writer: w}
	require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
	require.Panics(t, func() { ctx.NewResponse(http.StatusOK) })
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
	var hijackErr error
	router.Handle("GET /", func(ctx *Context) {
		hijackErr = ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil })
	})
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	router.serveMux.ServeHTTP(w, req)
	require.NoError(t, hijackErr)
	assert.True(t, afterHijacked, "middleware observes the hijack after next")
	assert.False(t, hasResponse, "middleware sees no response to replace")
	assert.Empty(t, w.statuses)
	assert.Empty(t, w.writes)
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
		require.NoError(t, ctx.Hijack(func(net.Conn, *bufio.ReadWriter) error { return nil }))
		panic("boom after hijack")
	})
	server := &httpServer{serveMux: router.serveMux}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { server.ServeHTTP(w, req) })
	assert.Empty(t, w.statuses, "no 500 written for a hijacked connection")
	logs := logBuf.String()
	assert.Contains(t, logs, "Recovered from panic")
	assert.Contains(t, logs, "Connection hijacked")
}
