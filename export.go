/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"net/http"

	"github.com/rs/zerolog"
)

// KeyValue holds all URL parameters for an empty `url:""` tag.
type KeyValue = map[string]string

// KeyValues holds all values for an empty `cookie:""`, `query:""`, or
// `form:""` tag.
type KeyValues = map[string][]string

type Handler = func(ctx *Context)

// Middleware wraps a [Context] handler in a chain. Call next to continue the
// chain; code after the next call runs on the way out, mirroring defer.
type Middleware = func(ctx *Context, next func())

type Router struct {
	serveMux    *http.ServeMux
	logger      *zerolog.Logger
	middlewares []Middleware
}

func (r Router) Group(middlewares ...Middleware) Router {
	return Router{
		serveMux:    r.serveMux,
		logger:      r.logger,
		middlewares: append(append([]Middleware(nil), r.middlewares...), middlewares...),
	}
}

func (r Router) Handle(pattern string, handler Handler) {
	// create middleware dispatcher
	var dispatcher func(*Context, int)
	dispatcher = func(ctx *Context, i int) {
		if i < len(r.middlewares) {
			r.middlewares[i](ctx, func() { dispatcher(ctx, i+1) })
		} else {
			handler(ctx)
		}
	}
	// log the route
	r.logger.Info().Str("pattern", pattern).Array("middlewares", funcObjects(r.middlewares)).
		Object("handler", funcObject(handler)).Msg("Registering route")
	// register the handler
	r.serveMux.HandleFunc(pattern, func(writer http.ResponseWriter, request *http.Request) {
		ctx := &Context{request: request, writer: writer}
		dispatcher(ctx, 0)
		if ctx.status == 0 {
			ctx.NewResponse(http.StatusInternalServerError)
		}
		if err := ctx.writeResponse(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("Failed to write response")
		}
	})
}
