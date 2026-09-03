/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResponseWriter is a purpose-built [http.ResponseWriter] for tests that
// need to observe every [http.ResponseWriter.WriteHeader] and
// [http.ResponseWriter.Write] call, including informational (1xx) responses.
// It does NOT depend on [httptest.ResponseRecorder], whose simplified header
// behavior is inadequate for verifying 1xx semantics. It deliberately
// implements none of the connection control features.
type fakeResponseWriter struct {
	header   http.Header
	statuses []int
	writes   [][]byte
}

func newFakeResponseWriter() *fakeResponseWriter {
	return &fakeResponseWriter{header: make(http.Header)}
}

func (f *fakeResponseWriter) Header() http.Header {
	return f.header
}

func (f *fakeResponseWriter) WriteHeader(status int) {
	f.statuses = append(f.statuses, status)
}

func (f *fakeResponseWriter) Write(b []byte) (int, error) {
	f.writes = append(f.writes, b)
	return len(b), nil
}

func TestStreamWriter_InitialState(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	assert.Equal(t, 0, sw.status)
	assert.Equal(t, 0, sw.bytesWritten)
}

func TestStreamWriter_FirstWriteImplicit200(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	n, err := sw.Write([]byte("hello"))

	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, http.StatusOK, sw.status)
	assert.Equal(t, 5, sw.bytesWritten)
}

func TestStreamWriter_MultipleWritesAccumulate(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	n1, _ := sw.Write([]byte("foo"))
	n2, _ := sw.Write([]byte("barbaz"))

	assert.Equal(t, 3, n1)
	assert.Equal(t, 6, n2)
	assert.Equal(t, 9, sw.bytesWritten)
}

func TestStreamWriter_RepeatedFinalStatus_PreservesFirst(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	sw.WriteHeader(http.StatusOK)
	sw.WriteHeader(http.StatusNotFound)

	// First committed 2xx-5xx status wins, matching net/http semantics.
	assert.Equal(t, http.StatusOK, sw.status)
	// Both calls are forwarded to the underlying writer.
	assert.Equal(t, []int{http.StatusOK, http.StatusNotFound}, fake.statuses)
}

func TestStreamWriter_InformationalThenFinal_TracksFinal(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	sw.WriteHeader(http.StatusEarlyHints) // 103
	sw.WriteHeader(http.StatusOK)         // 200

	// 1xx is informational; the subsequent 200 is the first final status.
	assert.Equal(t, http.StatusOK, sw.status)
	assert.Equal(t, []int{http.StatusEarlyHints, http.StatusOK}, fake.statuses)
}

func TestStreamWriter_MultipleInformationalThenBodyWrite_Tracks200(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	sw.WriteHeader(http.StatusContinue)   // 100
	sw.WriteHeader(http.StatusEarlyHints) // 103
	_, _ = sw.Write([]byte("body"))

	// Multiple 1xx responses don't commit; first body write implies 200.
	assert.Equal(t, http.StatusOK, sw.status)
	assert.Equal(t, 4, sw.bytesWritten)
}

func TestStreamWriter_InformationalThenFinalError_TracksError(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	sw.WriteHeader(http.StatusEarlyHints)          // 103
	sw.WriteHeader(http.StatusInternalServerError) // 500

	// 1xx is informational; the subsequent 500 is the first final status.
	assert.Equal(t, http.StatusInternalServerError, sw.status)
}

func TestStreamWriter_InformationalDoesNotCommitFinal(t *testing.T) {
	fake := newFakeResponseWriter()
	sw := &StreamWriter{writer: fake}

	sw.WriteHeader(http.StatusEarlyHints) // 103

	// 1xx is informational, not a final commit; status stays 0.
	// This is critical for panic recovery: server.go checks
	// streamWriter.status == 0 to decide whether to write a 500.
	assert.Equal(t, 0, sw.status)
}

// flushRecorderWriter counts Flush calls on a writer that otherwise does not
// implement any connection control feature.
type flushRecorderWriter struct {
	http.ResponseWriter
	flushes int
}

func (f *flushRecorderWriter) Flush() { f.flushes++ }

// ============================================================================
// Connection control delegation (Flush / deadlines / full duplex)
// ============================================================================

// allFeaturesResponseWriter implements every connection control feature the
// [StreamWriter] probes for, each recording its invocation.
type allFeaturesResponseWriter struct {
	*fakeResponseWriter
	flushes       int
	readDeadline  time.Time
	writeDeadline time.Time
	fullDuplex    bool
}

func (a *allFeaturesResponseWriter) Flush() { a.flushes++ }

// FlushError mirrors the underlying network writer, which exposes both the
// [http.Flusher] contract and the error-returning flush the
// [http.ResponseController] probes for.
func (a *allFeaturesResponseWriter) FlushError() error {
	a.flushes++
	return nil
}

func (a *allFeaturesResponseWriter) SetReadDeadline(deadline time.Time) error {
	a.readDeadline = deadline
	return nil
}

func (a *allFeaturesResponseWriter) SetWriteDeadline(deadline time.Time) error {
	a.writeDeadline = deadline
	return nil
}

func (a *allFeaturesResponseWriter) EnableFullDuplex() error {
	a.fullDuplex = true
	return nil
}

func newAllFeaturesResponseWriter() *allFeaturesResponseWriter {
	return &allFeaturesResponseWriter{
		fakeResponseWriter: newFakeResponseWriter(),
	}
}

func TestStreamWriter_ConnectionControls_Supported(t *testing.T) {
	fake := newAllFeaturesResponseWriter()
	sw := &StreamWriter{writer: fake}

	require.NoError(t, sw.Flush())
	assert.Equal(t, 1, fake.flushes)

	readDeadline := time.Now().Add(time.Second)
	require.NoError(t, sw.SetReadDeadline(readDeadline))
	assert.Equal(t, readDeadline, fake.readDeadline)

	writeDeadline := time.Now().Add(2 * time.Second)
	require.NoError(t, sw.SetWriteDeadline(writeDeadline))
	assert.Equal(t, writeDeadline, fake.writeDeadline)

	require.NoError(t, sw.EnableFullDuplex())
	assert.True(t, fake.fullDuplex)
}

func TestStreamWriter_ConnectionControls_Unsupported(t *testing.T) {
	sw := &StreamWriter{writer: newFakeResponseWriter()}

	assert.ErrorIs(t, sw.Flush(), http.ErrNotSupported)
	assert.ErrorIs(t, sw.SetReadDeadline(time.Now()), http.ErrNotSupported)
	assert.ErrorIs(t, sw.SetWriteDeadline(time.Now()), http.ErrNotSupported)
	assert.ErrorIs(t, sw.EnableFullDuplex(), http.ErrNotSupported)
}

// The deadlines and EnableFullDuplex match the probes
// [http.ResponseController] performs, so controllers created by wrapping code
// resolve those features through the StreamWriter itself. Flush does not match
// a controller probe ([http.Flusher] carries no error) and is therefore not
// asserted here.
func TestStreamWriter_ResponseControllerInterop(t *testing.T) {
	fake := newAllFeaturesResponseWriter()
	controller := http.NewResponseController(&StreamWriter{writer: fake})

	readDeadline := time.Now().Add(time.Second)
	require.NoError(t, controller.SetReadDeadline(readDeadline))
	assert.Equal(t, readDeadline, fake.readDeadline)

	writeDeadline := time.Now().Add(2 * time.Second)
	require.NoError(t, controller.SetWriteDeadline(writeDeadline))
	assert.Equal(t, writeDeadline, fake.writeDeadline)

	require.NoError(t, controller.EnableFullDuplex())
	assert.True(t, fake.fullDuplex)
}
