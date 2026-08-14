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
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============ httpServer.ServeHTTP integration tests ============

type testContextKey struct{ name string }

func newTestHTTPServer() *httpServer {
	serveMux := http.NewServeMux()
	return &httpServer{config: &ServerConfig{}, serveMux: serveMux}
}

func TestHTTPServer_NormalHandlerResponse(t *testing.T) {
	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())
}

func TestHTTPServer_PanicBeforeResponse_Becomes500(t *testing.T) {
	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		panic("before any response")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHTTPServer_PanicAfterResponse_StatusPreserved(t *testing.T) {
	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("committed"))
		panic("after commit")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "committed", rec.Body.String())
}

func TestHTTPServer_InformationalThenPanic_500(t *testing.T) {
	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints) // 103 - informational
		panic("after informational")
	})

	fake := newFakeResponseWriter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	server.ServeHTTP(fake, req)

	// 103 is informational (not a final commit), so recovery still writes 500.
	assert.Equal(t, []int{http.StatusEarlyHints, http.StatusInternalServerError}, fake.statuses)
}

func TestHTTPServer_RequestContextValuesAvailable(t *testing.T) {
	key := testContextKey{name: "user-id"}
	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		val := r.Context().Value(key)
		_, _ = w.Write([]byte(val.(string)))
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), key, "u123"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Equal(t, "u123", rec.Body.String())
}

func TestHTTPServer_LoggerInstalledInContext(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		l := zerolog.Ctx(r.Context())
		l.Info().Msg("handler-visible")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Contains(t, logBuf.String(), "handler-visible")
}

func TestHTTPServer_LogFields_RequestAndResponse(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	server := newTestHTTPServer()
	server.serveMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("tea"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	require.Len(t, lines, 2, "expected request + response log lines")

	var reqLog map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &reqLog))
	assert.Equal(t, "GET", reqLog["method"])
	assert.Equal(t, "example.com", reqLog["host"])
	assert.Equal(t, "/", reqLog["path"])
	assert.NotEmpty(t, reqLog["request_id"])
	assert.Equal(t, "Request", reqLog["message"])

	var respLog map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &respLog))
	assert.EqualValues(t, http.StatusTeapot, respLog["status"])
	assert.EqualValues(t, 3, respLog["bytes"])
	// Don't assert on duration (non-deterministic) or request_id (random).
}

func TestHTTPServer_RouterHandleFullStack(t *testing.T) {
	serveMux := http.NewServeMux()
	server := &httpServer{config: &ServerConfig{}, serveMux: serveMux}

	nopLog := zerolog.Nop()
	router := Router{serveMux: serveMux, logger: &nopLog}
	router.Handle("/api", func(ctx *Context) {
		ctx.NewResponse(http.StatusOK).StringBody("from router")
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "from router", rec.Body.String())
}

// ============================================================================
// httpServer.runner serving-error policy
// ============================================================================

// TestHTTPServer_Runner_ListenError_ShutdownPolicy verifies that an unexpected
// ListenAndServe failure is logged, and that the shutdown callback is invoked
// exactly once when ShutdownOnError is true, and never when it is false.
func TestHTTPServer_Runner_ListenError_ShutdownPolicy(t *testing.T) {
	cases := []struct {
		name            string
		shutdownOnError bool
		expectShutdown  int32
	}{
		{"true", true, 1},
		{"false", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := zerolog.New(&logBuf)

			ctx := logger.WithContext(context.Background())
			var shutdownCalled atomic.Int32
			shutdown := func() { shutdownCalled.Add(1) }

			server := &httpServer{
				config: &ServerConfig{ShutdownOnError: tc.shutdownOnError},
				server: http.Server{Addr: "127.0.0.1:not-a-port"},
			}

			assert.NotPanics(t, func() { server.runner(ctx, shutdown) })

			assert.Equal(t, tc.expectShutdown, shutdownCalled.Load())
			assert.Contains(t, logBuf.String(), "Server closed with error")
		})
	}
}

// TestHTTPServer_Runner_ServerClosed_IsIgnored verifies that the normal
// ErrServerClosed case is silently ignored by [httpServer.runner]: no error is
// logged and the shutdown callback is not invoked.
func TestHTTPServer_Runner_ServerClosed_IsIgnored(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(context.Background())

	var shutdownCalled atomic.Int32
	shutdown := func() { shutdownCalled.Add(1) }

	server := &httpServer{
		config: &ServerConfig{ShutdownOnError: true},
		server: http.Server{Addr: "127.0.0.1:not-a-port"},
	}

	// Shutdown the server first so ListenAndServe returns http.ErrServerClosed.
	require.NoError(t, server.server.Shutdown(context.Background()))

	assert.NotPanics(t, func() { server.runner(ctx, shutdown) })

	assert.Equal(t, int32(0), shutdownCalled.Load())
	assert.NotContains(t, logBuf.String(), "Server closed with error")
}

// ============================================================================
// httpServer.cleaner
// ============================================================================

// TestHTTPServer_Cleaner_Success verifies that the success path emits the
// expected log sequence ("Shutting down...", "Shutdown complete") and does not
// log "Error while shutting down".
func TestHTTPServer_Cleaner_Success(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(context.Background())

	server := &httpServer{
		config: &ServerConfig{},
		server: http.Server{Addr: "127.0.0.1:0"},
	}

	server.cleaner(ctx)

	assert.Contains(t, logBuf.String(), "Shutting down...")
	assert.Contains(t, logBuf.String(), "Shutdown complete")
	assert.NotContains(t, logBuf.String(), "Error while shutting down")
}

// TestHTTPServer_Cleaner_ShutdownError_LogsAndCompletes verifies that when
// server.Shutdown(ctx) returns an error (due to a canceled context with an
// active request still in flight), cleaner logs the error and the final
// "Shutdown complete" message.
func TestHTTPServer_Cleaner_ShutdownError_LogsAndCompletes(t *testing.T) {
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})

	server := &httpServer{
		config: &ServerConfig{},
		server: http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				w.WriteHeader(http.StatusOK)
			}),
		},
	}

	// Start serving in a goroutine.
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.server.Serve(listener)
	}()

	// Issue one HTTP request in another goroutine.
	reqDone := make(chan struct{})
	var resp *http.Response
	go func() {
		defer close(reqDone)
		resp, err = http.Get("http://" + listener.Addr().String())
	}()

	// Wait for the handler to start. Do not use a fixed sleep.
	<-started

	// Failure paths must also release the blocked handler.
	t.Cleanup(func() {
		close(release)
		<-reqDone
		<-serveDone
		if resp != nil {
			_ = resp.Body.Close()
		}
	})

	// Cancel the context before calling cleaner so Shutdown returns the context
	// error while the active connection is still in flight.
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	cancel()

	server.cleaner(ctx)

	assert.Contains(t, logBuf.String(), "Error while shutting down")
	assert.Contains(t, logBuf.String(), "Shutdown complete")
}
