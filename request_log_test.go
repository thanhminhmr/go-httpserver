/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for Trace-level logging emitted by [requestHandler].

// TestRequestHandler_TraceLogs_Success covers the two Trace-level log lines in
// [requestHandler] (request.go:96 "Request parsed, calling handler..." and :98
// "Handler returned"). All other tests install [zerolog.Nop] (the test router)
// or use Info-level buffers, so Trace calls never execute. Here a Trace-enabled
// logger is attached to the request context; both messages must appear, in
// order, in the output buffer.
func TestRequestHandler_TraceLogs_Success(t *testing.T) {
	type Req struct{}

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.TraceLevel)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(logger.WithContext(req.Context()))
	rec := httptest.NewRecorder()
	ctx := &Context{request: req, writer: rec}

	var parsed Req
	nextCalled := false
	requestHandler(ctx, &requestTags{}, &parsed, func(ctx *Context) {
		nextCalled = true
		ctx.NewResponse(http.StatusOK)
	})

	require.True(t, nextCalled, "next must be called on a successful parse")
	output := buf.String()
	assert.Contains(t, output, "Request parsed, calling handler...")
	assert.Contains(t, output, "Handler returned")
	// Wires: the "parsed" trace must precede the "Handler returned" trace.
	assert.Less(t,
		strings.Index(output, "Request parsed, calling handler..."),
		strings.Index(output, "Handler returned"),
		"parse trace must precede return trace",
	)
}
