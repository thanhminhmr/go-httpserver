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
	"net/http"
	"net/http/httptest"
	"strings"
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
