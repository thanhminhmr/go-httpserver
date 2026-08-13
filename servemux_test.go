/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ServeMux routing regression tests. These verify the routing semantics now
// owned by [http.ServeMux]: exact method+path match, path/wildcard binding,
// 404 for unmatched paths, 405 for wrong methods, and HEAD-on-GET behavior.

func TestServeMux_ExactMethodAndPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestServeMux_PathParameter_BindsID(t *testing.T) {
	type Req struct {
		ID string `url:"id"`
	}
	captured, rec := doServeMuxRequest[Req](t,
		http.MethodGet, "/items/{id}", "/items/42",
		captureHandler[Req])
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "42", captured.request.ID)
}

func TestServeMux_RemainderParameter_BindsPath(t *testing.T) {
	type Req struct {
		Path string `url:"path"`
	}
	mux := http.NewServeMux()
	mux.Handle("GET /files/{path...}", asHTTPHandler(RequestParser(func(ctx *Context, req Req) {
		ctx.NewResponse(http.StatusOK).JsonBody(req.Path)
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/files/a/b/c", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"a/b/c"`, rec.Body.String())
}

func TestServeMux_EmptyUrlTag_CollectsAllNamedParameters(t *testing.T) {
	type Req struct {
		Params KeyValue `url:""`
	}
	captured, rec := doServeMuxRequest[Req](t,
		http.MethodGet, "/users/{userId}/posts/{postId}", "/users/u123/posts/p456",
		captureHandler[Req])
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, KeyValue{"userId": "u123", "postId": "p456"}, captured.request.Params)
}

func TestServeMux_UnmatchedPath_Returns404(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /here", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeMux_WrongMethod_Returns405(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("POST /create", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/create", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestServeMux_GETRouteAcceptsHEAD(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /items", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/items", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
