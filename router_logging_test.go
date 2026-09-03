/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ============ funcObject / funcObjects ============

func TestFuncObject_KnownHandler(t *testing.T) {
	frame := funcObject(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	assert.NotEqual(t, "<unknown>", frame.Function)
	assert.NotEmpty(t, frame.File)
	assert.Greater(t, frame.Line, 0)
}

func TestFuncObject_UnknownValue(t *testing.T) {
	frame := funcObject(42)
	assert.Equal(t, "<unknown>", frame.Function)
}

func TestFuncObject_NilValue(t *testing.T) {
	frame := funcObject(nil)
	assert.Equal(t, "<unknown>", frame.Function)
}

func TestFuncObjects_Empty(t *testing.T) {
	frames := funcObjects([]http.Handler{})
	assert.Empty(t, frames)
}

func TestFuncObjects_NonEmpty(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})
	frames := funcObjects([]http.Handler{h, h})
	assert.Len(t, frames, 2)
	assert.NotEqual(t, "<unknown>", frames[0].Function)
	assert.NotEqual(t, "<unknown>", frames[1].Function)
}

// TestFuncObjects_GenericInstantiation covers the [funcObjects] generic when
// instantiated with [Middleware] (router.go uses funcObjects(r.middlewares)
// inside Handle). The []http.Handler instantiation above exercises the same
// body; this asserts the Middleware specialization resolves and orders results
// identically.
func TestFuncObjects_GenericInstantiation(t *testing.T) {
	m := func(ctx *Context, next func()) { next() }
	frames := funcObjects([]Middleware{m, m})
	assert.Len(t, frames, 2)
	assert.NotEqual(t, "<unknown>", frames[0].Function)
	assert.NotEqual(t, "<unknown>", frames[1].Function)
}

// ============ Router.Handle: nil-logger branch ============

// TestRouter_Handle_NilLogger_RegistersAndDispatches covers the false side of
// `if r.logger != nil` in [Router.Handle] (router.go:78). Every other test
// routes through [newTestRouter] (which installs a nop logger); here the Router
// is built with a nil logger so route registration skips the "Registering route"
// log, and dispatch still works end-to-end.
func TestRouter_Handle_NilLogger_RegistersAndDispatches(t *testing.T) {
	r := Router{serveMux: http.NewServeMux()} // logger intentionally nil
	assert.NotPanics(t, func() {
		r.Handle("GET /", func(ctx *Context) {
			ctx.NewResponse(http.StatusOK).StringBody("ok")
		})
	})
	rec := doRouterRequest(r, http.MethodGet, "/")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}
