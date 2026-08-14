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

// Context carries the request context and the response being built by parser
// handlers. It implements [context.Context] by delegating to the HTTP request.
//
// Context values are created by [RequestParser] and [MiddlewareParser]. The zero
// value is invalid, and a Context must not be copied.
type Context struct {
	_ common.NoCopy

	request *http.Request
	writer  http.ResponseWriter

	// response
	status     int
	body       any
	marshaller uint
}

const (
	marshallerIsDirect uint = iota
	marshallerIsJson
)

// Deadline delegates to the HTTP request context.
func (c *Context) Deadline() (deadline time.Time, ok bool) { return c.request.Context().Deadline() }

// Done delegates to the HTTP request context.
func (c *Context) Done() <-chan struct{} { return c.request.Context().Done() }

// Err delegates to the HTTP request context.
func (c *Context) Err() error { return c.request.Context().Err() }

// Value delegates to the HTTP request context.
func (c *Context) Value(key any) any { return c.request.Context().Value(key) }

// Response returns a handle to the current response without changing it.
// Its status is zero until [Context.NewResponse] is called.
func (c *Context) Response() Response { return Response{ctx: c} }

// NewResponse starts a new response with status and returns its handle.
// It clears the previous body and all response headers. The response is written
// only after the outermost parser returns.
//
// NewResponse panics unless status is between 200 and 599.
func (c *Context) NewResponse(status int) Response {
	if status < 200 || status > 599 {
		panic("BUG: invalid status")
	}
	c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
	clear(c.writer.Header())
	return Response{ctx: c}
}

// Response is a handle to response state owned by a [Context]. Copies share the
// same state. The zero value is invalid.
type Response struct{ ctx *Context }

// Status returns the configured HTTP status, or zero before [Context.NewResponse].
func (r Response) Status() int { return r.ctx.status }

// Header returns the live response header map.
// A later [Context.NewResponse] call clears it.
func (r Response) Header() http.Header { return r.ctx.writer.Header() }

// Body returns the configured body value, or nil if no body is set.
func (r Response) Body() any { return r.ctx.body }

// Cookie appends a Set-Cookie header for cookie to the response.
func (r Response) Cookie(cookie http.Cookie) {
	r.Header().Add("Set-Cookie", cookie.String())
}

// BytesBody sets a raw byte body without setting Content-Type.
func (r Response) BytesBody(body []byte) {
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// StringBody sets a raw string body without setting Content-Type.
func (r Response) StringBody(body string) {
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// StreamBody sets a body writer without setting Content-Type. The status is
// committed before body runs, so an error from body can be logged but cannot
// change the HTTP response status.
func (r Response) StreamBody(body func(io.Writer) error) {
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// PlainTextBody sets body with Content-Type "text/plain; charset=utf-8".
func (r Response) PlainTextBody(body string) {
	r.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// OctetsBody sets body with Content-Type "application/octet-stream".
func (r Response) OctetsBody(body []byte) {
	r.Header().Set("Content-Type", "application/octet-stream")
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// JsonBody stores body for JSON marshaling when the response is written.
// Successful marshaling sets Content-Type to "application/json; charset=utf-8".
// A marshal failure writes 500 Internal Server Error with an empty body.
func (r Response) JsonBody(body any) {
	r.ctx.body, r.ctx.marshaller = body, marshallerIsJson
}

// MarshalZerologObject implements [zerolog.LogObjectMarshaler] for the
// configured status, headers, and body.
func (r Response) MarshalZerologObject(e *zerolog.Event) {
	e.Int("status", r.ctx.status)
	if header := r.ctx.writer.Header(); len(header) > 0 {
		e.Any("header", header)
	}
	if r.ctx.body != nil {
		e.Any("body", r.ctx.body)
	}
}

func (c *Context) writeResponse() error {
	switch c.marshaller {
	case marshallerIsJson:
		data, err := json.Marshal(c.body)
		if err == nil {
			c.writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			c.writer.WriteHeader(c.status)
			_, err = c.writer.Write(data)
		} else {
			clear(c.writer.Header())
			c.writer.WriteHeader(http.StatusInternalServerError)
		}
		return err
	default:
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
		}
		clear(c.writer.Header())
		c.writer.WriteHeader(http.StatusInternalServerError)
		return exception.String("Response: unsupported body type")
	}
}
