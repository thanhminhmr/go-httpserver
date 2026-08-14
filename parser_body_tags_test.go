/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the body-reading tags: form, json, multipart, and body.
// Also covers HTTP method variations (PUT, PATCH, DELETE, HEAD).

// ============ form tag tests ============

type formSingleStruct struct {
	Name string `form:"name"`
	Age  int    `form:"age"`
}

// TestFormTag_BodyMethodMatrix exercises the form binder across the POST/PUT/
// PATCH methods that share the body-parsing case in parser.go.
func TestFormTag_BodyMethodMatrix(t *testing.T) {
	cases := []struct {
		name   string
		method string
		values url.Values
		want   formSingleStruct
	}{
		{"POST", http.MethodPost, url.Values{"name": {"alice"}, "age": {"30"}}, formSingleStruct{Name: "alice", Age: 30}},
		{"PUT", http.MethodPut, url.Values{"name": {"bob"}, "age": {"25"}}, formSingleStruct{Name: "bob", Age: 25}},
		{"PATCH", http.MethodPatch, url.Values{"name": {"carol"}}, formSingleStruct{Name: "carol"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured, rec := doRequest[formSingleStruct](t, captureHandler[formSingleStruct],
				tc.method, "/", withFormBody(tc.values))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.want.Name, captured.request.Name, "Name")
			if tc.want.Age != 0 {
				assert.Equal(t, tc.want.Age, captured.request.Age, "Age")
			}
		})
	}
}

func TestFormTag_GET_IgnoresBody(t *testing.T) {
	captured, rec := doRequest[formSingleStruct](t, captureHandler[formSingleStruct],
		http.MethodGet, "/", withFormBody(url.Values{"name": {"ignored"}}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", captured.request.Name, "Name (GET ignores body)")
}

func TestFormTag_WrongContentType_415(t *testing.T) {
	_, rec := doRequest[formSingleStruct](t, captureHandler[formSingleStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte("name=alice")))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestFormTag_ContentLengthZero_SkipsBinding(t *testing.T) {
	var captured formSingleStruct
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	asTestHTTPHandler(RequestParser(func(ctx *Context, req formSingleStruct) {
		captured = req
		ctx.NewResponse(http.StatusOK)
	})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", captured.Name, "Name (no body)")
}

type formAllStruct struct {
	Values KeyValues `form:""`
}

func TestFormTag_EmptyTag_BindsAllValues(t *testing.T) {
	captured, rec := doRequest[formAllStruct](t, captureHandler[formAllStruct],
		http.MethodPost, "/", withFormBody(url.Values{"a": {"1"}, "b": {"2"}}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"1"}, captured.request.Values["a"], "Values[a]")
	assert.Equal(t, []string{"2"}, captured.request.Values["b"], "Values[b]")
}

type formAllWrongTypeStruct struct {
	Values string `form:""`
}

func TestFormTag_EmptyTag_WrongType_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[formAllWrongTypeStruct])
	})
}

type formMultipleEmptyStruct struct {
	F1 KeyValues `form:""`
	F2 KeyValues `form:""`
}

func TestFormTag_MultipleEmptyTags_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[formMultipleEmptyStruct])
	})
}

type formNonEmptyAfterEmptyStruct struct {
	All    KeyValues `form:""`
	Single string    `form:"name"`
}

func TestFormTag_NonEmptyAfterEmpty_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[formNonEmptyAfterEmptyStruct])
	})
}

type formMultipleValuesStruct struct {
	IDs []string `form:"id"`
}

func TestFormTag_MultipleValues_SliceField(t *testing.T) {
	captured, rec := doRequest[formMultipleValuesStruct](t, captureHandler[formMultipleValuesStruct],
		http.MethodPost, "/", withFormBody(url.Values{"id": {"1", "2", "3"}}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"1", "2", "3"}, captured.request.IDs, "IDs")
}

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

// ============ multipart tag tests ============

type multipartStruct struct {
	Reader *multipart.Reader `multipart:""`
}

func TestMultipartTag_BasicReader(t *testing.T) {
	captured, rec := doRequest[multipartStruct](t, captureHandler[multipartStruct],
		http.MethodPost, "/", withMultipartBody(t, func(w *multipart.Writer) {
			_ = w.WriteField("field1", "value1")
			_ = w.WriteField("field2", "value2")
		}))
	assert.Equal(t, http.StatusOK, rec.Code)
	if captured.request.Reader == nil {
		t.Fatal("Reader is nil")
	}
	part, err := captured.request.Reader.NextPart()
	if err != nil {
		t.Fatalf("NextPart failed: %v", err)
	}
	assert.Equal(t, "field1", part.FormName(), "first part form name")
	value, _ := io.ReadAll(part)
	assert.Equal(t, "value1", string(value), "first part value")
}

func TestMultipartTag_MissingBoundary_400(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader("garbage"))
	req.Header.Set("Content-Type", "multipart/form-data")
	req.ContentLength = int64(len("garbage"))
	rec := httptest.NewRecorder()
	asTestHTTPHandler(RequestParser(captureHandler[multipartStruct])).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMultipartTag_WrongContentType_415(t *testing.T) {
	_, rec := doRequest[multipartStruct](t, captureHandler[multipartStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte("data")))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

type multipartNonEmptyStruct struct {
	Reader *multipart.Reader `multipart:"value"`
}

func TestMultipartTag_NonEmptyValue_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[multipartNonEmptyStruct])
	})
}

type multipartWrongTypeStruct struct {
	Reader string `multipart:""`
}

func TestMultipartTag_WrongType_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[multipartWrongTypeStruct])
	})
}

type multipartMultipleStruct struct {
	R1 *multipart.Reader `multipart:""`
	R2 *multipart.Reader `multipart:""`
}

func TestMultipartTag_MultipleTags_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[multipartMultipleStruct])
	})
}

func TestMultipartTag_FileUpload(t *testing.T) {
	captured, rec := doRequest[multipartStruct](t, captureHandler[multipartStruct],
		http.MethodPost, "/", withMultipartBody(t, func(w *multipart.Writer) {
			writer, err := w.CreateFormFile("upload", "test.txt")
			if err != nil {
				t.Fatalf("CreateFormFile failed: %v", err)
			}
			_, _ = writer.Write([]byte("file content"))
		}))
	assert.Equal(t, http.StatusOK, rec.Code)
	if captured.request.Reader == nil {
		t.Fatal("Reader is nil")
	}
	part, err := captured.request.Reader.NextPart()
	if err != nil {
		t.Fatalf("NextPart failed: %v", err)
	}
	assert.Equal(t, "test.txt", part.FileName(), "file name")
	content, _ := io.ReadAll(part)
	assert.Equal(t, "file content", string(content), "content")
}

// ============ body tag tests ============

type bodyStruct struct {
	Body io.ReadCloser `body:""`
}

func TestBodyTag_Basic_BindsRawBody(t *testing.T) {
	captured, rec := doRequest[bodyStruct](t, captureHandler[bodyStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte("raw body content")))
	assert.Equal(t, http.StatusOK, rec.Code)
	if captured.request.Body == nil {
		t.Fatal("Body is nil")
	}
	data, _ := io.ReadAll(captured.request.Body)
	assert.Equal(t, "raw body content", string(data), "Body")
}

type bodyWithContentTypesStruct struct {
	Body io.ReadCloser `body:"text/plain application/xml"`
}

func TestBodyTag_WithContentTypes_AcceptsMatching(t *testing.T) {
	captured, rec := doRequest[bodyWithContentTypesStruct](t, captureHandler[bodyWithContentTypesStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte("plain text")))
	assert.Equal(t, http.StatusOK, rec.Code)
	data, _ := io.ReadAll(captured.request.Body)
	assert.Equal(t, "plain text", string(data), "Body")
}

func TestBodyTag_WithContentTypes_RejectsNonMatching_415(t *testing.T) {
	_, rec := doRequest[bodyWithContentTypesStruct](t, captureHandler[bodyWithContentTypesStruct],
		http.MethodPost, "/", withRawBody("application/json", []byte("{}")))
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestBodyTag_MultipleContentTypes_AcceptsAny(t *testing.T) {
	captured, rec := doRequest[bodyWithContentTypesStruct](t, captureHandler[bodyWithContentTypesStruct],
		http.MethodPost, "/", withRawBody("application/xml", []byte("<x/>")))
	assert.Equal(t, http.StatusOK, rec.Code)
	data, _ := io.ReadAll(captured.request.Body)
	assert.Equal(t, "<x/>", string(data), "Body")
}

type bodyConflictFormStruct struct {
	Body io.ReadCloser `body:"application/x-www-form-urlencoded"`
	Form url.Values    `form:"name"`
}

type bodyConflictJsonStruct struct {
	Body io.ReadCloser    `body:"application/json"`
	Data jsonSimpleStruct `json:""`
}

type bodyConflictMultipartStruct struct {
	Body   io.ReadCloser     `body:"multipart/form-data"`
	Reader *multipart.Reader `multipart:""`
}

type bodyMultipleStruct struct {
	B1 io.ReadCloser `body:""`
	B2 io.ReadCloser `body:""`
}

type bodyWrongTypeStruct struct {
	Body string `body:""`
}

func TestBodyTag_ConflictsWithForm_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[bodyConflictFormStruct])
	})
}

func TestBodyTag_ConflictsWithJson_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[bodyConflictJsonStruct])
	})
}

func TestBodyTag_ConflictsWithMultipart_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[bodyConflictMultipartStruct])
	})
}

func TestBodyTag_MultipleTags_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[bodyMultipleStruct])
	})
}

func TestBodyTag_WrongType_Panics(t *testing.T) {
	require.Panics(t, func() {
		_ = RequestParser(captureHandler[bodyWrongTypeStruct])
	})
}

// Test that body tag is used as fallback when no other body tags match
type bodyFallbackStruct struct {
	Body io.ReadCloser `body:""`
}

func TestBodyTag_Fallback_WhenNoOtherBodyTagMatches(t *testing.T) {
	captured, rec := doRequest[bodyFallbackStruct](t, captureHandler[bodyFallbackStruct],
		http.MethodPost, "/", withRawBody("application/octet-stream", []byte("binary data")))
	assert.Equal(t, http.StatusOK, rec.Code)
	data, _ := io.ReadAll(captured.request.Body)
	assert.Equal(t, "binary data", string(data), "Body")
}

func TestBodyTag_BodyReadable(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 1024)
	captured, rec := doRequest[bodyStruct](t, captureHandler[bodyStruct],
		http.MethodPost, "/", withRawBody("text/plain", body))
	assert.Equal(t, http.StatusOK, rec.Code)
	data, _ := io.ReadAll(captured.request.Body)
	assert.Len(t, data, 1024, "Body length")
}

// ============ method variation tests ============
//
// POST/PUT/PATCH/DELETE share the same body-parsing code path (parser.go:
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

// TestBodyTag_BodyMethodMatrix exercises the raw-body binder across the PUT/
// PATCH/DELETE methods that share the body-parsing case in parser.go.
func TestBodyTag_BodyMethodMatrix(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   string
	}{
		{"PUT", http.MethodPut, "put body"},
		{"PATCH", http.MethodPatch, "patch body"},
		{"DELETE", http.MethodDelete, "delete body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured, rec := doRequest[bodyStruct](t, captureHandler[bodyStruct],
				tc.method, "/", withRawBody("text/plain", []byte(tc.body)))
			require.Equal(t, http.StatusOK, rec.Code)
			data, _ := io.ReadAll(captured.request.Body)
			assert.Equal(t, tc.body, string(data), "Body")
		})
	}
}

// ============ body tag content-length zero ============

func TestBodyTag_ContentLengthZero_SkipsBinding(t *testing.T) {
	captured, rec := doRequest[bodyStruct](t, captureHandler[bodyStruct],
		http.MethodPost, "/", withRawBody("text/plain", []byte{}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, captured.request.Body, "Body should be nil when ContentLength is 0")
}
