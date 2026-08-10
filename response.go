/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/thanhminhmr/go-common/common"
	"github.com/thanhminhmr/go-exception"
)

// Context is passed to a [RequestHandler] or [MiddlewareHandler] and carries the
// request-scoped context along with the response state. It implements
// [context.Context] by delegating to the underlying [*http.Request] context.
//
// Build a response with [Context.NewResponse], then use the methods on the
// returned [Response] (such as [Response.JsonBody] or [Response.Cookie]) to
// configure it. The framework writes the response after the handler returns.
type Context struct {
	_ common.NoCopy

	request *http.Request
	writer  http.ResponseWriter

	// response
	status int
	body   any
}

func (c *Context) Deadline() (deadline time.Time, ok bool) { return c.request.Context().Deadline() }
func (c *Context) Done() <-chan struct{}                   { return c.request.Context().Done() }
func (c *Context) Err() error                              { return c.request.Context().Err() }
func (c *Context) Value(key any) any                       { return c.request.Context().Value(key) }
func (c *Context) Response() *Response                     { return &Response{ctx: c} }

// NewResponse starts building a [Response] with the given HTTP status code.
func (c *Context) NewResponse(status int) Response {
	if status < 100 || status > 599 {
		panic("BUG: invalid status")
	}
	c.status = status
	clear(c.writer.Header())
	return Response{ctx: c}
}

// Response represents an HTTP response returned by a [RequestHandler]. Create
// one with [Context.NewResponse], then chain methods such as [Response.JsonBody]
// or [Response.Cookie] to configure it. The framework writes the response to
// the client after the handler returns.
type Response struct{ ctx *Context }

// Status returns the HTTP status code of the response.
func (r Response) Status() int {
	return r.ctx.status
}

// Header returns the response header map. Headers set here are sent with the
// response. Mutations affect the response directly.
func (r Response) Header() http.Header {
	return r.ctx.writer.Header()
}

func (r Response) Body() any {
	return r.ctx.body
}

// Cookie adds a Set-Cookie header to the response.
func (r *Response) Cookie(cookie http.Cookie) {
	r.Header().Add("Set-Cookie", cookie.String())
}

// BytesBody sets the response body to body, written verbatim with no
// Content-Type.
func (r Response) BytesBody(body []byte) {
	r.ctx.body = body
}

// StringBody sets the response body to body, written verbatim with no
// Content-Type.
func (r Response) StringBody(body string) {
	r.ctx.body = body
}

// StreamBody sets the response body to a function that writes content directly
// to the [io.Writer]. The status code is written before body is invoked. No
// Content-Type is set.
func (r Response) StreamBody(body func(io.Writer) error) {
	r.ctx.body = body
}

// PlainTextBody sets the response body to body with Content-Type
// "text/plain; charset=utf-8".
func (r Response) PlainTextBody(body string) {
	r.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.ctx.body = body
}

// OctetsBody sets the response body to body with Content-Type
// "application/octet-stream".
func (r Response) OctetsBody(body []byte) {
	r.Header().Set("Content-Type", "application/octet-stream")
	r.ctx.body = body
}

// JsonBody sets the response body to the JSON encoding of body with
// Content-Type "application/json; charset=utf-8".
func (r Response) JsonBody(body any) {
	r.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.ctx.body = jsonBody{body: body}
}

// MarshalZerologObject implements [zerolog.LogObjectMarshaler] so a Response
// can be embedded in structured log entries.
func (r Response) MarshalZerologObject(e *zerolog.Event) {
	e.Int("status", r.ctx.status)
	if header := r.ctx.writer.Header(); len(header) > 0 {
		e.Any("header", header)
	}
	if r.ctx.body != nil {
		switch body := r.ctx.body.(type) {
		case jsonBody:
			e.Any("body", body.body)
		default:
			e.Any("body", r.ctx.body)
		}
	}
}

type jsonBody = struct{ body any }

func (c *Context) writeResponse() error {
	switch body := c.body.(type) {
	case nil:
		c.writer.WriteHeader(c.status)
		return nil
	case []byte:
		c.writer.WriteHeader(c.status)
		_, err := c.writer.Write(body)
		return err
	case string:
		c.writer.WriteHeader(c.status)
		_, err := c.writer.Write(unsafeStringToBytes(body))
		return err
	case func(io.Writer) error:
		c.writer.WriteHeader(c.status)
		return body(c.writer)
	case jsonBody:
		data, err := json.Marshal(body.body)
		if err == nil {
			c.writer.WriteHeader(c.status)
			_, err = c.writer.Write(data)
		} else {
			c.writer.WriteHeader(http.StatusInternalServerError)
		}
		return err
	}
	c.writer.WriteHeader(http.StatusInternalServerError)
	return exception.String("Response: unsupported body type")
}
