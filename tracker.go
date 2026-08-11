/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import "net/http"

// responseWriterTracker wraps an [http.ResponseWriter] to record the first
// status code passed to [http.ResponseWriter.WriteHeader] and to count the
// bytes written. All writes are delegated to the underlying writer.
//
// The first call to [http.ResponseWriter.Write] that occurs without a prior
// [http.ResponseWriter.WriteHeader] call is treated as status 200, matching
// the behavior of [net/http].
type responseWriterTracker struct {
	http.ResponseWriter
	status       int
	bytesWritten int
}

func newResponseWriterTracker(w http.ResponseWriter) *responseWriterTracker {
	return &responseWriterTracker{ResponseWriter: w}
}

func (w *responseWriterTracker) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriterTracker) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

// Status returns the recorded status code, or 0 if [WriteHeader] has not been
// called and no body has been written yet.
func (w *responseWriterTracker) Status() int {
	return w.status
}

// BytesWritten returns the total number of body bytes written so far.
func (w *responseWriterTracker) BytesWritten() int {
	return w.bytesWritten
}

// Unwrap exposes the underlying [http.ResponseWriter] so callers can access
// additional interfaces via [http.ResponseController].
func (w *responseWriterTracker) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
