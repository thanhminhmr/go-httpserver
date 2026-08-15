/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ============ json tag tests ============

type jsonSimpleStruct struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type jsonWholeBodyStruct struct {
	Data jsonSimpleStruct `json:""`
}

func TestJsonTag_EmptyTag_BindsWholeBody(t *testing.T) {
	captured, rec := doRequest[jsonWholeBodyStruct](t, captureHandler[jsonWholeBodyStruct],
		http.MethodPost, "/", withJSONBody(jsonSimpleStruct{Name: "alice", Age: 30}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice", captured.request.Data.Name, "Name")
	assert.Equal(t, 30, captured.request.Data.Age, "Age")
}

type jsonFieldStruct struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Active string `json:"active"`
}

func TestJsonTag_NonEmptyTag_ExtractsFields(t *testing.T) {
	captured, rec := doRequest[jsonFieldStruct](t, captureHandler[jsonFieldStruct],
		http.MethodPost, "/", withJSONBody(map[string]any{"name": "bob", "email": "bob@test.com", "active": "yes"}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "bob", captured.request.Name, "Name")
	assert.Equal(t, "bob@test.com", captured.request.Email, "Email")
	assert.Equal(t, "yes", captured.request.Active, "Active")
}

func TestJsonTag_InvalidJSON_400(t *testing.T) {
	_, rec := doRequest[jsonFieldStruct](t, captureHandler[jsonFieldStruct],
		http.MethodPost, "/", withRawBody("application/json", []byte("{invalid json")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestJsonTag_WrongContentType_415(t *testing.T) {
	_, rec := doRequest[jsonFieldStruct](t, captureHandler[jsonFieldStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte(`{"name":"x"}`)))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestJsonTag_ContentLengthZero_Skips(t *testing.T) {
	var captured jsonFieldStruct
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	asTestHTTPHandler(RequestParser(func(ctx *Context, req jsonFieldStruct) {
		captured = req
		ctx.NewResponse(http.StatusOK)
	})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", captured.Name, "Name (no body)")
}

func TestJsonTag_NestedStruct(t *testing.T) {
	type nestedUser struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	type nestedStruct struct {
		User nestedUser `json:"user"`
	}
	captured, rec := doRequest[nestedStruct](t, captureHandler[nestedStruct],
		http.MethodPost, "/", withJSONBody(map[string]any{
			"user": map[string]any{"name": "nested", "email": "n@est.test"},
		}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "nested", captured.request.User.Name, "Name")
	assert.Equal(t, "n@est.test", captured.request.User.Email, "Email")
}

func TestJsonTag_UseNumber_Precision(t *testing.T) {
	type numberStruct struct {
		Value json.Number `json:"value"`
	}
	captured, rec := doRequest[numberStruct](t, captureHandler[numberStruct],
		http.MethodPost, "/", withRawBody("application/json", []byte(`{"value":12345678901234567890}`)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "12345678901234567890", captured.request.Value.String(), "Value")
}

type jsonMultipleEmptyStruct struct {
	A jsonSimpleStruct `json:""`
	B jsonSimpleStruct `json:""`
}

func TestJsonTag_EmptyTag_MultipleTags_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[jsonMultipleEmptyStruct])
	})
}

type jsonNonEmptyAfterEmptyStruct struct {
	All    jsonSimpleStruct `json:""`
	Single string           `json:"name"`
}

func TestJsonTag_NonEmptyAfterEmpty_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[jsonNonEmptyAfterEmptyStruct])
	})
}

// ============ method variation tests ============
//
// POST/PUT/PATCH/DELETE share the same body-parsing code path (request_body.go:
// `case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete`).
// The matrix below covers the four methods for both the form and JSON binders.

func TestJsonTag_BodyMethodMatrix(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   map[string]any
		want   jsonSimpleStruct
	}{
		{"PUT", http.MethodPut, map[string]any{"name": "put-data", "age": 25}, jsonSimpleStruct{Name: "put-data", Age: 25}},
		{"PATCH", http.MethodPatch, map[string]any{"name": "patch-data", "age": 35}, jsonSimpleStruct{Name: "patch-data", Age: 35}},
		{"DELETE", http.MethodDelete, map[string]any{"name": "delete-data", "age": 25}, jsonSimpleStruct{Name: "delete-data", Age: 25}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured, rec := doRequest[jsonSimpleStruct](t, captureHandler[jsonSimpleStruct],
				tc.method, "/", withJSONBody(tc.body))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.want, captured.request)
		})
	}
}

// ============ JSON trailing-content contract ============

// These tests lock in the contract for bindJson: exactly one JSON value may be
// present in the body. Any trailing content—including whitespace—must be
// rejected with a 400 status. Both the whole-body json:"" path and the
// named-field json:"name" path are covered.

func TestJsonTag_TrailingContent(t *testing.T) {
	type namedStruct struct {
		Name string `json:"name"`
	}
	type wholeBodyStruct struct {
		Data jsonSimpleStruct `json:""`
	}

	cases := []struct {
		name      string
		body      []byte
		wantCode  int
		wantName  string
		wantWhole bool
	}{
		// no trailing bytes
		{"named_no_trailing", []byte(`{"name":"a"}`), http.StatusOK, "a", false},
		{"whole_no_trailing", []byte(`{"name":"b","age":1}`), http.StatusOK, "b", true},
		// any trailing byte—including whitespace—must be rejected
		{"named_trailing_space", []byte(`{"name":"a"} `), http.StatusBadRequest, "", false},
		{"named_trailing_newline", []byte(`{"name":"a"}` + "\n"), http.StatusBadRequest, "", false},
		{"whole_trailing_space", []byte(`{"name":"b","age":1} `), http.StatusBadRequest, "", true},
		{"whole_trailing_newline", []byte(`{"name":"b","age":1}` + "\n"), http.StatusBadRequest, "", true},
		// a second JSON value must be rejected
		{"named_second_value", []byte(`{"name":"a"}{"name":"b"}`), http.StatusBadRequest, "", false},
		{"whole_second_value", []byte(`{"name":"b","age":1}{"name":"c","age":2}`), http.StatusBadRequest, "", false},
		// non-whitespace garbage after a valid value must be rejected
		{"named_trailing_garbage", []byte(`{"name":"a"} garbage`), http.StatusBadRequest, "", false},
		{"whole_trailing_garbage", []byte(`{"name":"b","age":1} garbage`), http.StatusBadRequest, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantWhole {
				captured, rec := doRequest[wholeBodyStruct](t, captureHandler[wholeBodyStruct],
					http.MethodPost, "/", withRawBody("application/json", tc.body))
				assert.Equal(t, tc.wantCode, rec.Code)
				if tc.wantCode == http.StatusOK {
					assert.Equal(t, tc.wantName, captured.request.Data.Name, "Data.Name")
				}
			} else {
				captured, rec := doRequest[namedStruct](t, captureHandler[namedStruct],
					http.MethodPost, "/", withRawBody("application/json", tc.body))
				assert.Equal(t, tc.wantCode, rec.Code)
				if tc.wantCode == http.StatusOK {
					assert.Equal(t, tc.wantName, captured.request.Name, "Name")
				}
			}
		})
	}
}
