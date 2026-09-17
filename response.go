/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"unsafe"

	"github.com/rs/zerolog"
)

const (
	marshallerIsDirect uint = iota
	marshallerIsJson
)

// Response is a handle to response state owned by a [Context]. Copies share
// the same state. Handles are invalidated — using their mutating methods
// panics — by a later [Context.NewResponse], a later [Context.Hijack], or by
// the write of the response itself. The zero value behaves like a nil pointer:
// it is safe to log; any other use panics.
type Response struct {
	ctx    *Context
	ticket uint
}

// check panics unless r is still the live handle for the response state of
// its Context.
func (r Response) check() {
	if r.ctx == nil || r.ticket != r.ctx.ticket {
		panic("BUG: stale response handle")
	}
}

// Status returns the configured HTTP status.
func (r Response) Status() int { return r.ctx.status }

// Header returns the live response header map. A later
// [Context.NewResponse] call clears it.
func (r Response) Header() http.Header { return r.ctx.writer.Header() }

// Body returns the configured body value, or nil if no body is set.
func (r Response) Body() any { return r.ctx.body }

// Cookie appends a Set-Cookie header for cookie to the response.
func (r Response) Cookie(cookie http.Cookie) {
	r.check()
	r.Header().Add("Set-Cookie", cookie.String())
}

// BytesBody sets a raw byte body without setting Content-Type.
func (r Response) BytesBody(body []byte) {
	r.check()
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// StringBody sets a raw string body without setting Content-Type.
func (r Response) StringBody(body string) {
	r.check()
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// StreamBody sets body as the streaming response body without setting
// Content-Type. body runs after the response status and headers are
// committed:
//
//   - The [StreamWriter] handed to body writes through the response
//     writer and exposes the connection controls the underlying
//     connection supports.
//   - Flush pushes already-written bytes to the client immediately,
//     which suits server-sent-event style responses. Transfer framing
//     (chunked encoding on HTTP/1.1) is managed by net/http.
//   - An error returned by body is logged and the connection is aborted
//     via panic(http.ErrAbortHandler): the status is already on the
//     wire and cannot be changed.
//   - body runs after the Context was cleared and must not use it, or
//     any response handle saved from earlier.
func (r Response) StreamBody(body func(*StreamWriter) error) {
	r.check()
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// PlainTextBody sets body with Content-Type "text/plain; charset=utf-8".
func (r Response) PlainTextBody(body string) {
	r.check()
	r.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// OctetsBody sets body with Content-Type "application/octet-stream".
func (r Response) OctetsBody(body []byte) {
	r.check()
	r.Header().Set("Content-Type", "application/octet-stream")
	r.ctx.body, r.ctx.marshaller = body, marshallerIsDirect
}

// JsonBody stores body for JSON marshaling when the response is written.
// Successful marshaling sets Content-Type to "application/json; charset=utf-8".
// A marshal failure writes 500 Internal Server Error with an empty body.
func (r Response) JsonBody(body any) {
	r.check()
	r.ctx.body, r.ctx.marshaller = body, marshallerIsJson
}

// MarshalZerologObject implements [zerolog.LogObjectMarshaler] for the
// configured status, headers, and body. The zero Response logs as an empty
// object.
func (r Response) MarshalZerologObject(e *zerolog.Event) {
	if r.ctx == nil {
		return
	}
	e.Int("status", r.ctx.status)
	if header := r.ctx.writer.Header(); len(header) > 0 {
		e.Any("header", header)
	}
	if r.ctx.body != nil {
		e.Any("body", r.ctx.body)
	}
}

// writeResponse commits the response currently stored in c to the underlying
// http.ResponseWriter. Router.Handle calls it once after the handler chain
// returns. A hijack recorded with [Context.Hijack] and not canceled by a
// [Context.NewResponse] is committed here instead: the connection is taken
// over and the hijack body runs in place of the response write, so nothing is
// written unless the takeover itself fails. JSON marshal failures and
// unsupported body types become empty 500 responses because they are caught
// before any header is committed. Body write and stream errors occur after
// [http.ResponseWriter.WriteHeader]; the response status is already on the
// wire and cannot be replaced, so the error is logged and the connection is
// then aborted via panic(http.ErrAbortHandler), which server.ServeHTTP and
// net/http treat as a silent connection close.
//
// Before a streaming or hijack body runs, c is cleared: any use of c — or of
// a [Response] handle saved from earlier — is invalid.
func (c *Context) writeResponse(requestCtx context.Context) {
	if c.writer == nil {
		return
	}
	c.ticket++
	logger := zerolog.Ctx(requestCtx)
	switch c.marshaller {
	case marshallerIsJson:
		data, err := json.Marshal(c.body)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to marshal response as JSON")
			clear(c.writer.Header())
			c.writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		c.writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		c.writer.WriteHeader(c.status)
		if count, err := c.writer.Write(data); err != nil {
			logger.Error().Err(err).Int("count", count).Msg("Failed to write response")
			break
		}
		return
	default:
		switch body := c.body.(type) {
		case nil:
			if c.status == 0 {
				logger.Error().Msg("Response is missing")
				clear(c.writer.Header())
				c.writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			c.writer.WriteHeader(c.status)
			return
		case []byte:
			c.writer.WriteHeader(c.status)
			if count, err := c.writer.Write(body); err != nil {
				logger.Error().Err(err).Int("count", count).Msg("Failed to write response body")
				break
			}
			return
		case string:
			c.writer.WriteHeader(c.status)
			if count, err := c.writer.Write(unsafeStringToBytes(body)); err != nil {
				logger.Error().Err(err).Int("count", count).Msg("Failed to write response body")
				break
			}
			return
		case func(*StreamWriter) error:
			c.writer.WriteHeader(c.status)
			streamWriter, ok := c.writer.(*StreamWriter)
			if !ok {
				streamWriter = &StreamWriter{writer: c.writer}
			}
			c.request, c.writer = nil, nil
			if err := body(streamWriter); err != nil {
				logger.Error().Err(err).Msg("Failed to write response body")
				break
			}
			return
		case func(net.Conn, *bufio.ReadWriter) error:
			conn, readWriter, err := http.NewResponseController(c.writer).Hijack()
			if err != nil {
				logger.Error().Err(err).Msg("Failed to hijack connection")
				clear(c.writer.Header())
				c.writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			c.request, c.writer, c.body = nil, nil, nil
			defer func(logger *zerolog.Logger, conn net.Conn) {
				if err := conn.Close(); err != nil {
					logger.Error().Err(err).Msg("Failed while closing hijacked connection")
				}
			}(logger, conn)
			if err := body(conn, readWriter); err != nil {
				logger.Error().Err(err).Msg("Failed to handle hijacked connection")
			}
			return
		default:
			logger.Error().Any("body", body).Msg("Unsupported response body type")
			clear(c.writer.Header())
			c.writer.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	panic(http.ErrAbortHandler)
}

// unsafeStringToBytes returns a zero-copy byte view of value for the immediate
// response write path. The returned slice aliases immutable string storage and
// must never be modified.
func unsafeStringToBytes(value string) []byte {
	return unsafe.Slice(unsafe.StringData(value), len(value))
}
