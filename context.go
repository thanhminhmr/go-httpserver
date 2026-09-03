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
	hijacked bool
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
// clears the previous body and all response headers. The response is not written
// until the [Router.Handle] middleware and handler chain returns.
//
// NewResponse panics unless status is between 200 and 599, or when the
// connection was hijacked with [Context.Hijack].
func (c *Context) NewResponse(status int) Response {
	if c.hijacked {
		panic("BUG: response after hijack")
	}
	if status < 200 || status > 599 {
		panic("BUG: invalid status")
	}
	c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
	clear(c.writer.Header())
	return Response{ctx: c}
}

// Hijack takes over the underlying network connection and hands it to body.
// The connection is taken over before any response byte is written — responses
// commit only after the middleware chain returns — and any response state
// assembled so far is discarded: after a successful Hijack,
// [Context.Response] reports false, [Context.Hijacked] reports true, and the
// response write is skipped entirely.
//
// body receives the hijacked connection and a buffered ReadWriter holding any
// unread request bytes. A protocol-switch response (for example the WebSocket
// 101 upgrade line with its headers) must be written manually inside body.
// body owns the connection for its duration; the connection is closed when
// body returns or panics, and the error returned by body is logged and
// returned by Hijack. After the takeover the HTTP request context no longer
// reflects the connection, so body must rely on connection reads or deadlines
// to notice a dropped peer.
//
// Hijack returns an error — without calling body and without touching the
// response state, so the handler can still fall back to [Context.NewResponse]
// — when the underlying writer does not support hijacking
// ([http.ErrNotSupported]) or when the hijack itself fails. Hijack panics
// when called twice on the same Context.
func (c *Context) Hijack(body func(conn net.Conn, readWriter *bufio.ReadWriter) error) error {
	if c.hijacked {
		panic("BUG: connection already hijacked")
	}
	writer := c.writer
	var streamWriter *StreamWriter
	if sw, ok := writer.(*StreamWriter); ok {
		streamWriter = sw
		writer = sw.writer
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return http.ErrNotSupported
	}
	conn, readWriter, err := hijacker.Hijack()
	if err != nil {
		return err
	}
	c.hijacked = true
	if streamWriter != nil {
		streamWriter.hijacked = true
	}
	c.status, c.body, c.marshaller = 0, nil, marshallerIsDirect
	defer conn.Close()
	if err := body(conn, readWriter); err != nil {
		zerolog.Ctx(c.request.Context()).Error().Err(err).Msg("Failed to handle hijacked connection")
		return err
	}
	return nil
}

// Hijacked reports whether [Context.Hijack] took over the connection.
func (c *Context) Hijacked() bool { return c.hijacked }
