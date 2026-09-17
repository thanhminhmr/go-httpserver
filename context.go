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

	"github.com/thanhminhmr/go-common/common"
)

// Context is the per-request state passed to [Handler] and [Middleware]. It
// implements [context.Context] by delegating to the underlying HTTP request's
// context and owns the response state assembled by the handler chain.
//
// Context values are created by [Router.Handle]. The zero value is invalid, and
// a Context must not be copied after first use. Its lifetime ends when the
// response is written: before a streaming or hijack body runs, the Context is
// cleared, and calling anything on it — or on a [Response] handle saved from
// earlier — is a bug with undefined behavior.
type Context struct {
	_ common.NoCopy

	request *http.Request
	writer  http.ResponseWriter

	// response
	status     int
	body       any
	marshaller uint
	ticket     uint
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
// exists. It returns the zero Response and false before [Context.NewResponse]
// is called and while a [Context.Hijack] is pending.
func (c *Context) Response() (Response, bool) {
	if c.status == 0 {
		return Response{}, false
	}
	return Response{ctx: c, ticket: c.ticket}, true
}

// NewResponse starts a new response with status and returns its handle. It
// clears the previous body and all response headers, cancels a pending
// [Context.Hijack], and invalidates [Response] handles returned earlier;
// using them panics. The response is not written until the [Router.Handle]
// middleware and handler chain returns.
//
// NewResponse panics unless status is between 200 and 599, or when the Context
// was already cleared for a streaming or hijack body.
func (c *Context) NewResponse(status int) Response {
	if status < 200 || status > 599 {
		panic("BUG: invalid status")
	}
	c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
	clear(c.writer.Header())
	c.ticket++
	return Response{ctx: c, ticket: c.ticket}
}

// Hijack records body as the takeover handler for the underlying
// connection. The takeover is committed after the middleware and handler
// chain returns, in place of the response:
//
//   - Any response state is discarded; [Context.Response] reports false and
//     [Context.Hijacked] reports true until the takeover is committed.
//   - Later middleware may cancel it with [Context.NewResponse]; calling
//     Hijack again replaces body.
//   - Hijack invalidates earlier [Response] handles; using them panics.
//
// body receives the hijacked connection and a buffered ReadWriter holding
// any unread request bytes; a protocol-switch response (for example the
// WebSocket 101 upgrade) must be written manually inside body.
//
//   - The connection is closed when body returns or panics; the error
//     returned by body is logged.
//   - After the takeover the request context no longer reflects the
//     connection: body must rely on connection reads or deadlines.
//   - body runs after the Context was cleared and must not use it, or any
//     response handle saved from earlier.
//
// The server serves plain HTTP/1.1, so connections are always hijackable;
// a takeover that still fails at write time is logged and answered with
// an empty 500 response.
func (c *Context) Hijack(body func(net.Conn, *bufio.ReadWriter) error) {
	if body == nil {
		panic("BUG: nil hijack body")
	}
	c.status, c.body, c.marshaller = 0, body, marshallerIsDirect
	c.ticket++
}

// Hijacked reports whether a [Context.Hijack] takeover is pending — recorded
// but not yet committed. Once committed, the Context is cleared and Hijacked
// no longer reports it.
func (c *Context) Hijacked() bool { return c.status == 0 && c.body != nil }
