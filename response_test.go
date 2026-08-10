/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ============ Context.writeResponse (HTTP wire marshaling) ============

func TestResponse_Write_NilBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec, status: http.StatusNoContent}
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestResponse_Write_BytesBody(t *testing.T) {
	rec := httptest.NewRecorder()
	data := []byte("raw bytes")
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).BytesBody(data)
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, data, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestResponse_Write_StringBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StringBody("a string")
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "a string", rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestResponse_Write_StreamBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StreamBody(func(w io.Writer) error {
		_, err := w.Write([]byte("streamed"))
		return err
	})
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "streamed", rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestResponse_Write_StreamBody_Error(t *testing.T) {
	rec := httptest.NewRecorder()
	streamErr := errors.New("stream write failed")
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).StreamBody(func(w io.Writer) error {
		return streamErr
	})
	err := ctx.writeResponse()
	assert.ErrorIs(t, err, streamErr)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestResponse_Write_PlainTextBody(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).PlainTextBody("hello plain")
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello plain", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestResponse_Write_OctetsBody(t *testing.T) {
	rec := httptest.NewRecorder()
	data := []byte{0x00, 0x01, 0x02, 0xFF}
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).OctetsBody(data)
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, data, rec.Body.Bytes())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
}

func TestResponse_Write_JsonBody(t *testing.T) {
	rec := httptest.NewRecorder()
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	p := payload{Name: "alice", Age: 30}
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusCreated).JsonBody(p)
	err := ctx.writeResponse()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	var result payload
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, p, result)
}

func TestResponse_Write_JsonBody_MarshalError(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec}
	ctx.NewResponse(http.StatusOK).JsonBody(make(chan int))
	err := ctx.writeResponse()
	assert.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestResponse_Write_UnknownBodyType(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := &Context{writer: rec, status: http.StatusOK, body: 12345}
	err := ctx.writeResponse()
	assert.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
