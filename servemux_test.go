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

// ServeMux wildcard/path-binding regression tests. These validate the
// integration between the package's [getPathValues] helper and the
// [http.ServeMux] route-parameter feature.

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
	mux.Handle("GET /files/{path...}", asTestHTTPHandler(RequestParser(func(ctx *Context, req Req) {
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
