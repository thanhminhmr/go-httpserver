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
// [Context.writeResponse] reuses it for [Response.StreamBody] bodies. It also
// records when a takeover recorded with [Context.Hijack] is committed at
// write time, which the server layer uses to skip response writing and panic
// recovery output.
//
// StreamWriter also exposes the connection control features the underlying
// writer supports: [StreamWriter.Flush], [StreamWriter.SetReadDeadline],
// [StreamWriter.SetWriteDeadline], and [StreamWriter.EnableFullDuplex] delegate
// to the underlying writer and return [http.ErrNotSupported] when it lacks the
// feature. The deadlines and EnableFullDuplex match the probes
// [http.ResponseController] performs, so response controllers created by
// wrapping code resolve them through this writer; Flush does not match a
// controller probe ([http.Flusher] carries no error) and cannot be resolved
// that way.
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

// Flush try to push already-written bytes to the client without waiting for
// the response body to finish.
func (w *StreamWriter) Flush() {
	if flusher, ok := w.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *StreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijack, ok := w.writer.(http.Hijacker); ok {
		if conn, rw, err := hijack.Hijack(); err == nil {
			w.writer = nil
			return conn, rw, nil
		} else {
			return nil, nil, err
		}
	}
	return nil, nil, http.ErrNotSupported
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
