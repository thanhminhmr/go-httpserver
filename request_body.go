/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"bufio"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/thanhminhmr/go-common/common"
	"github.com/thanhminhmr/go-exception"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	contentTypeIsForm      = "application/x-www-form-urlencoded"
	contentTypeIsJson      = "application/json"
	contentTypeIsMultipart = "multipart/form-data"
)

const (
	maxBodyLength       = 1 << 20 // 1 MiB
	maxReadBodyDuration = 5 * time.Second
)

var unicodeUTF16LE = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
var unicodeUTF16BE = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)

// charsetReader returns a reader decoded for form or JSON binding. An explicit
// Content-Type charset is authoritative. Without one, UTF-8, UTF-16LE, and
// UTF-16BE byte-order marks are recognized; otherwise the buffered input is
// returned unchanged. Unknown explicit charsets return an error.
func charsetReader(reader io.Reader, contentTypeParams map[string]string) (io.Reader, error) {
	if contentCharset, exists := contentTypeParams["charset"]; exists {
		if contentEncoding, _ := charset.Lookup(contentCharset); contentEncoding != nil {
			return transform.NewReader(reader, contentEncoding.NewDecoder()), nil
		}
		return nil, exception.String("Charset: Invalid content charset").SetExtra("charset", contentCharset)
	}
	buffered := bufio.NewReader(reader)
	peek, _ := buffered.Peek(3)
	switch {
	case len(peek) >= 3:
		if peek[0] == 0xEF && peek[1] == 0xBB && peek[2] == 0xBF {
			_, _ = buffered.Discard(3)
			return buffered, nil
		}
		fallthrough
	case len(peek) >= 2:
		if peek[0] == 0xFF && peek[1] == 0xFE {
			_, _ = buffered.Discard(2)
			return transform.NewReader(buffered, unicodeUTF16LE.NewDecoder()), nil
		}
		if peek[0] == 0xFE && peek[1] == 0xFF {
			_, _ = buffered.Discard(2)
			return transform.NewReader(buffered, unicodeUTF16BE.NewDecoder()), nil
		}
	}
	return buffered, nil
}

// bindFullTextBody applies the production Content-Length, size, charset,
// cancellation, and timeout rules used by form and JSON body binding.
func bindFullTextBody(request *http.Request, contentTypeParameters map[string]string, parsed reflect.Value,
	binder func(reader io.Reader, parsed reflect.Value) (int, error)) (int, error) {
	return bindFullTextBodyWithTimeout(request, contentTypeParameters, parsed, binder, maxReadBodyDuration)
}

// bindFullTextBodyWithTimeout is the testable core of [bindFullTextBody]. It
// accepts an explicit timeout so tests can exercise timeout and cancellation
// paths without waiting for [maxReadBodyDuration]. Binder panics are propagated
// back to the caller goroutine; timeout or request cancellation closes the
// request body to unblock the binder.
func bindFullTextBodyWithTimeout(request *http.Request, contentTypeParameters map[string]string, parsed reflect.Value,
	binder func(reader io.Reader, parsed reflect.Value) (int, error), timeout time.Duration) (int, error) {
	if request.ContentLength < 0 {
		return http.StatusLengthRequired, exception.String("HttpServer: Content-Length is required but missing")
	} else if request.ContentLength > maxBodyLength {
		return http.StatusRequestEntityTooLarge, exception.String("HttpServer: Content-Length is too large")
	}
	type resultValue struct {
		status    int
		err       error
		recovered exception.Exception
	}
	done := make(chan resultValue, 1)
	go func() {
		defer exception.Recover(func(recovered exception.Exception) { done <- resultValue{recovered: recovered} })
		if reader, err := charsetReader(request.Body, contentTypeParameters); err != nil {
			done <- resultValue{
				status: http.StatusUnsupportedMediaType,
				err:    exception.String("HttpServer: cannot determine body encoding").AddCause(err),
			}
		} else {
			status, err := binder(reader, parsed)
			done <- resultValue{status: status, err: err}
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		if result.recovered != nil {
			panic(result.recovered)
		}
		return result.status, result.err
	case <-request.Context().Done():
		return http.StatusRequestTimeout,
			exception.String("HttpServer: Client diconnected").AddSuppressed(request.Body.Close())
	case <-timer.C:
		return http.StatusRequestTimeout,
			exception.String("HttpServer: Bind body timed out").AddSuppressed(request.Body.Close())
	}
}

// bindForm consumes the decoded body reader as URL-encoded form data and binds
// either the complete KeyValues map or named form values.
func (tags *requestTags) bindForm(reader io.Reader, parsed reflect.Value) (int, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return http.StatusInternalServerError, exception.String("HttpServer: Read request body failed").AddCause(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Parse form body failed").AddCause(err)
	}
	if tags.formFieldIndex != nil {
		parsed.FieldByIndex(tags.formFieldIndex).Set(reflect.ValueOf(values))
	} else if err := common.BindStructWithTag("form", values, parsed.Addr().Interface()); err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Bind form params failed").AddCause(err)
	}
	return 0, nil
}

// bindJson decodes exactly one JSON value. An empty json tag decodes directly
// into its target field; otherwise the body must decode to an object whose
// values are bound to named json-tagged fields. json.Decoder.UseNumber is used
// to avoid losing integer precision before named-field conversion.
func (tags *requestTags) bindJson(reader io.Reader, parsed reflect.Value) (int, error) {
	var target any
	var values map[string]any
	if tags.jsonFieldIndex != nil {
		target = parsed.FieldByIndex(tags.jsonFieldIndex).Addr().Interface()
	} else {
		target = &values
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Decode json body failed").AddCause(err)
	}
	nextReader := io.MultiReader(decoder.Buffered(), reader)
	if count, err := io.CopyN(io.Discard, nextReader, 1); count > 0 || err != nil && err != io.EOF {
		return http.StatusBadRequest, exception.String("HttpServer: Decode json body failed").AddCause(err)
	}
	if tags.jsonFieldIndex == nil {
		if err := common.BindStructWithTag("json", values, parsed.Addr().Interface()); err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Bind json values failed").AddCause(err)
		}
	}
	return 0, nil
}

// bindMultipart constructs a multipart.Reader over the live request body using
// the boundary from Content-Type. It does not buffer or consume the multipart
// body itself.
func (tags *requestTags) bindMultipart(
	request *http.Request, parsed reflect.Value, parameters map[string]string,
) (int, error) {
	boundary, ok := parameters["boundary"]
	if !ok {
		return http.StatusBadRequest,
			exception.String("HttpServer: Boundary is missing in Content-Type of a " + contentTypeIsMultipart)
	}
	parsed.FieldByIndex(tags.multipartFieldIndex).Set(reflect.ValueOf(multipart.NewReader(request.Body, boundary)))
	return 0, nil
}

// bindBody assigns the live request body to the raw body field. The body is not
// buffered or rewound.
func (tags *requestTags) bindBody(request *http.Request, parsed reflect.Value) {
	parsed.FieldByIndex(tags.bodyFieldIndex).Set(reflect.ValueOf(request.Body))
}
