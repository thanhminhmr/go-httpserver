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
)

// StreamWriter wraps a connection's [http.ResponseWriter] with response
// accounting for the server: informational (1xx) statuses pass through without
// committing a final status, the first final (>=200) status is recorded, a
// Write before any WriteHeader records an implicit 200, and written byte
// counts accumulate. The server installs exactly one StreamWriter per request;
// the response write reuses it for [Response.StreamBody] bodies.
//
// StreamWriter also exposes the connection control features the underlying
// writer supports. [StreamWriter.Flush], [StreamWriter.Hijack],
// [StreamWriter.SetReadDeadline], [StreamWriter.SetWriteDeadline], and
// [StreamWriter.EnableFullDuplex] delegate to the underlying writer; their
// signatures match the probes [http.ResponseController] performs, so response
// controllers created by wrapping code resolve them through this writer.
// Hijack and the deadline setters return [http.ErrNotSupported] when the
// underlying writer lacks the feature. A successful Hijack takes over the
// connection and detaches the underlying writer; after it the server writes
// no HTTP response for the request.
//
// The zero value is invalid.
type StreamWriter struct {
	writer       http.ResponseWriter
	status       int
	bytesWritten int
}

// Header returns the underlying response writer's header map.
func (w *StreamWriter) Header() http.Header { return w.writer.Header() }

// WriteHeader forwards statusCode to the underlying writer. Informational
// (1xx) statuses are forwarded without committing; the first final (>=200)
// status is recorded as the response status.
func (w *StreamWriter) WriteHeader(statusCode int) {
	if statusCode >= 200 && w.status == 0 {
		w.status = statusCode
	}
	w.writer.WriteHeader(statusCode)
}

// Write forwards body data to the underlying writer, recording an implicit
// status of 200 when no final status was written yet and accumulating the
// written byte count.
func (w *StreamWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.writer.Write(body)
	w.bytesWritten += n
	return n, err
}

// Flush tries to push already-written bytes to the client without waiting
// for the response body to finish. It is a no-op when the underlying writer
// does not implement [http.Flusher].
func (w *StreamWriter) Flush() {
	if flusher, ok := w.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack takes over the underlying connection. On success the underlying
// writer is detached: a later use of the StreamWriter is invalid. It returns
// [http.ErrNotSupported] when the underlying writer does not support
// hijacking, or the error reported by the underlying hijacker.
func (w *StreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.writer.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, readWriter, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.writer = nil
	return conn, readWriter, nil
}

// SetReadDeadline sets the read deadline of the underlying connection.
func (w *StreamWriter) SetReadDeadline(deadline time.Time) error {
	if setter, ok := w.writer.(interface{ SetReadDeadline(time.Time) error }); ok {
		return setter.SetReadDeadline(deadline)
	}
	return http.ErrNotSupported
}

// SetWriteDeadline sets the write deadline of the underlying connection.
func (w *StreamWriter) SetWriteDeadline(deadline time.Time) error {
	if setter, ok := w.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return setter.SetWriteDeadline(deadline)
	}
	return http.ErrNotSupported
}

// EnableFullDuplex indicates that the handler will read from the request body
// while writing the response, disabling any automatic half-close behavior of
// the underlying connection.
func (w *StreamWriter) EnableFullDuplex() error {
	if enabler, ok := w.writer.(interface{ EnableFullDuplex() error }); ok {
		return enabler.EnableFullDuplex()
	}
	return http.ErrNotSupported
}
