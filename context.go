/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bufio"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/thanhminhmr/go-common/common"
)

// Context is the per-request state passed to [Handler] and [Middleware]. It
// implements [context.Context] by delegating to the underlying HTTP request and
// owns the response state assembled by the handler chain.
//
// Context values are created by [Router.Handle]. The zero value is invalid, and
// a Context must not be copied after first use.
type Context struct {
	_ common.NoCopy

	request *http.Request
	writer  http.ResponseWriter

	// response
	status     int
	body       any
	marshaller uint

	// connection
	hijacked   bool
	hijackBody func(conn net.Conn, readWriter *bufio.ReadWriter) error
}

// Deadline delegates to the HTTP request context.
func (c *Context) Deadline() (deadline time.Time, ok bool) { return c.request.Context().Deadline() }

// Done delegates to the HTTP request context.
func (c *Context) Done() <-chan struct{} { return c.request.Context().Done() }

// Err delegates to the HTTP request context.
func (c *Context) Err() error { return c.request.Context().Err() }

// Value delegates to the HTTP request context.
func (c *Context) Value(key any) any { return c.request.Context().Value(key) }

// Response returns a handle to the current response and reports whether one
// exists. Its status is zero until [Context.NewResponse] is called; until then
// Response returns the zero Response and false.
func (c *Context) Response() (Response, bool) {
	if c.status == 0 {
		return Response{}, false
	}
	return Response{ctx: c}, true
}

// NewResponse starts a new response with status and returns its handle. It
// clears the previous body and all response headers, and cancels a pending
// [Context.Hijack]. The response is not written until the [Router.Handle]
// middleware and handler chain returns.
//
// NewResponse panics unless status is between 200 and 599, or when the
// connection was taken over by a [Context.Hijack] body.
func (c *Context) NewResponse(status int) Response {
	if c.hijacked {
		panic("BUG: response after hijack")
	}
	if status < 200 || status > 599 {
		panic("BUG: invalid status")
	}
	c.hijackBody = nil
	c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
	clear(c.writer.Header())
	return Response{ctx: c}
}

// Hijack records body as the takeover handler for the underlying network
// connection. The takeover itself is committed after the complete middleware
// and handler chain returns, at the same point the response is written: a
// pending hijack discards any response state assembled so far — after Hijack,
// [Context.Response] reports false and [Context.Hijacked] reports true — and
// the later write runs body instead of writing a response. Middleware that
// runs after the handler can inspect the pending takeover with
// [Context.Hijacked] and cancel it by calling [Context.NewResponse]; calling
// Hijack again before the write replaces body.
//
// body receives the hijacked connection and a buffered ReadWriter holding any
// unread request bytes. A protocol-switch response (for example the WebSocket
// 101 upgrade line with its headers) must be written manually inside body.
// body owns the connection for its duration; the connection is closed when
// body returns or panics, and the error returned by body is logged at write
// time. After the takeover the HTTP request context no longer reflects the
// connection, so body must rely on connection reads or deadlines to notice a
// dropped peer. Inside body, [Context.NewResponse] and [Context.Hijack] panic.
//
// Hijack returns an error — without recording anything and without touching
// the response state, so the handler can still fall back to
// [Context.NewResponse] — when the underlying writer does not support
// hijacking ([http.ErrNotSupported]). A hijack failure at write time is
// logged and written as an empty 500 response.
func (c *Context) Hijack(body func(conn net.Conn, readWriter *bufio.ReadWriter) error) error {
	if c.hijacked {
		panic("BUG: connection already hijacked")
	}
	writer := c.writer
	if sw, ok := writer.(*StreamWriter); ok {
		writer = sw.writer
	}
	if _, ok := writer.(http.Hijacker); !ok {
		return http.ErrNotSupported
	}
	c.hijackBody = body
	c.status, c.body, c.marshaller = 0, nil, marshallerIsDirect
	return nil
}

// commitHijack takes over the connection and runs body. writeResponse calls
// it for the hijack body recorded by [Context.Hijack] instead of writing the
// response. A takeover failure is logged and written as an empty 500
// response; after a successful takeover the server skips all response output.
func (c *Context) commitHijack(body func(conn net.Conn, readWriter *bufio.ReadWriter) error) {
	logger := zerolog.Ctx(c.request.Context())
	writer := c.writer
	var streamWriter *StreamWriter
	if sw, ok := writer.(*StreamWriter); ok {
		streamWriter = sw
		writer = sw.writer
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		logger.Error().Msg("Failed to hijack connection")
		clear(c.writer.Header())
		c.writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, readWriter, err := hijacker.Hijack()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to hijack connection")
		clear(c.writer.Header())
		c.writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	c.hijacked = true
	if streamWriter != nil {
		streamWriter.hijacked = true
	}
	defer conn.Close()
	if err := body(conn, readWriter); err != nil {
		logger.Error().Err(err).Msg("Failed to handle hijacked connection")
	}
}

// Hijacked reports whether the connection is being taken over with
// [Context.Hijack]: a pending takeover recorded by a handler — which
// middleware may still cancel with [Context.NewResponse] — or the committed
// takeover running its body at write time.
func (c *Context) Hijacked() bool { return c.hijackBody != nil || c.hijacked }
