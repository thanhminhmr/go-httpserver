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
// implements [context.Context] by delegating to the underlying HTTP request and
// owns the response state assembled by the handler chain.
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
// exists. Its status is zero until [Context.NewResponse] is called; until then
// Response returns the zero Response and false. A pending [Context.Hijack]
// means no response exists either.
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

// Hijack records body as the takeover handler for the underlying network
// connection. The takeover itself is committed after the complete middleware
// and handler chain returns, at the same point a response is written: a
// pending hijack discards any response state assembled so far — after Hijack,
// [Context.Response] reports false and [Context.Hijacked] reports true — and
// at write time the connection is handed to body instead of writing a
// response. Middleware that runs after the handler can inspect the pending
// takeover with [Context.Hijacked] and cancel it by calling
// [Context.NewResponse]; calling Hijack again before the write replaces body.
// Hijack invalidates [Response] handles returned earlier; using them panics.
//
// body receives the hijacked connection and a buffered ReadWriter holding any
// unread request bytes. A protocol-switch response (for example the WebSocket
// 101 upgrade line with its headers) must be written manually inside body.
// body owns the connection for its duration; the connection is closed when
// body returns or panics, and the error returned by body is logged at write
// time. After the takeover the HTTP request context no longer reflects the
// connection, so body must rely on connection reads or deadlines to notice a
// dropped peer. body runs after the Context was cleared, so it must not use
// the Context or any response handle saved from earlier.
//
// Hijack assumes the underlying connection is hijackable: the server serves
// plain HTTP/1.1 only — no TLS and no HTTP/2 — so that always holds. A
// takeover that still fails at write time is logged and written as an empty
// 500 response.
func (c *Context) Hijack(body func(net.Conn, *bufio.ReadWriter) error) {
	if body == nil {
		panic("BUG: nil hijack body")
	}
	c.status, c.body, c.marshaller = 0, body, marshallerIsDirect
	c.ticket++
}

// Hijacked reports whether the connection is being taken over with
// [Context.Hijack]: a pending takeover recorded by a handler, which middleware
// may still cancel with [Context.NewResponse]. Once the takeover is committed
// the Context is cleared, so Hijacked no longer reports it.
func (c *Context) Hijacked() bool { return c.status == 0 && c.body != nil }
