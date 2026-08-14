/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/thanhminhmr/go-common/common"
	"github.com/thanhminhmr/go-exception"
)

// RequestHandler handles a parsed and validated request. The handler builds its
// response through ctx, usually with [Context.NewResponse].
type RequestHandler[Request any] = func(ctx *Context, request Request)

// RequestParser converts a typed [RequestHandler] into an [http.HandlerFunc].
//
// Request must be a non-pointer struct. Its defaults and binding-tag layout are
// checked when RequestParser is called; invalid definitions panic. Each request
// gets a fresh Request value which is defaulted, bound, validated, then passed
// to handler.
//
// Parse and validation failures return an empty HTTP error response without
// calling handler. If no parser handler configures a response, the outermost
// parser returns 500 Internal Server Error. Handler panics are not recovered;
// [NewServer] installs recovery middleware for that purpose.
func RequestParser[Request any](handler RequestHandler[Request]) Handler {
	tags := createTags(reflect.TypeFor[Request]())
	return func(ctx *Context) {
		var parsed Request
		requestHandler(ctx, &tags, &parsed, func(ctx *Context) { handler(ctx, parsed) })
	}
}

// MiddlewareHandler handles a parsed request around the next handler. Call next
// to continue the chain; parser middleware in the same chain shares ctx.
type MiddlewareHandler[Request any] = func(ctx *Context, request Request, next func())

// MiddlewareParser converts a typed [MiddlewareHandler] into [Middleware].
// Request parsing and validation follow the same rules as [RequestParser].
//
// Parser middleware shares one [Context] with downstream parser handlers. The
// request body is not buffered or rewound, so a body consumed by one parser
// cannot be parsed again downstream. Direct writes by ordinary net/http handlers
// bypass the shared response state.
//
// A parse or validation failure stops the chain and writes the error response
// immediately. Otherwise, the outermost parser writes the configured response
// after the chain returns.
func MiddlewareParser[Request any](handler MiddlewareHandler[Request]) Middleware {
	tags := createTags(reflect.TypeFor[Request]())
	return func(ctx *Context, next func()) {
		var parsed Request
		requestHandler(ctx, &tags, &parsed, func(ctx *Context) { handler(ctx, parsed, next) })
	}
}

func requestHandler(ctx *Context, tags *requestTags, parsed any, next Handler) {
	logger := zerolog.Ctx(ctx)
	// apply default value for request
	if err := common.ApplyDefaults(parsed); err != nil {
		logger.Error().Err(err).Msg("Failed to apply request defaults")
		ctx.NewResponse(http.StatusInternalServerError)
		return
	}
	// parse request
	if status, err := tags.parse(ctx.request, reflect.ValueOf(parsed).Elem()); err != nil {
		logger.Error().Err(err).Msg("Failed to parse request")
		ctx.NewResponse(status)
		return
	}
	// validate request
	if err := common.ValidateStruct(parsed); err != nil {
		logger.Error().Err(err).Msg("Failed to validate request")
		ctx.NewResponse(http.StatusBadRequest)
		return
	}
	logger.Trace().Any("parsed", parsed).Msg("Request parsed, calling handler...")
	// call next handler
	next(ctx)
	// log handler response
	logger.Trace().Object("response", ctx.Response()).Msg("Handler returned")
}

//region requestTags

type requestTags struct {
	flags               uint
	headerFieldIndex    []int
	cookieFieldIndex    []int
	queryFieldIndex     []int
	urlFieldIndex       []int
	formFieldIndex      []int
	jsonFieldIndex      []int
	multipartFieldIndex []int
	bodyFieldIndex      []int
	bodyContentTypes    []string
}

const (
	tagHeader uint = 1 << iota
	tagCookie
	tagQuery
	tagUrl
	tagForm
	tagJson
	tagMultipart
	tagBody
)

const (
	contentTypeIsForm      = "application/x-www-form-urlencoded"
	contentTypeIsJson      = "application/json"
	contentTypeIsMultipart = "multipart/form-data"
)

var globalTags = map[reflect.Type]requestTags{}
var globalTagsMutex sync.Mutex

func createTags(requestType reflect.Type) requestTags {
	if requestType.Kind() != reflect.Struct {
		panic("BUG: parsed request must be a struct")
	}
	// lock the global tags cache
	globalTagsMutex.Lock()
	defer globalTagsMutex.Unlock()
	// check if request tags already exists
	tags, exists := globalTags[requestType]
	if exists {
		return tags
	}
	// check if defaults are valid
	if err := common.ApplyDefaults(reflect.New(requestType).Interface()); err != nil {
		panic(err)
	}
	// check the tags recursively
	tags.checkRecursively(requestType, nil)
	if len(tags.bodyContentTypes) > 0 {
		if tags.flags&tagForm != 0 && slices.Contains(tags.bodyContentTypes, contentTypeIsForm) {
			panic("BUG: `form` tag field is not allowed when `body` tag contains " + contentTypeIsForm)
		} else if tags.flags&tagJson != 0 && slices.Contains(tags.bodyContentTypes, contentTypeIsJson) {
			panic("BUG: `json` tag field is not allowed when `body` tag contains " + contentTypeIsJson)
		} else if tags.flags&tagMultipart != 0 && slices.Contains(tags.bodyContentTypes, contentTypeIsMultipart) {
			panic("BUG: `multipart` tag field is not allowed when `body` tag contains " + contentTypeIsMultipart)
		}
	}
	globalTags[requestType] = tags
	return tags
}

var typeForHeader = reflect.TypeFor[http.Header]()
var typeForKeyValues = reflect.TypeFor[KeyValues]()
var typeForKeyValue = reflect.TypeFor[KeyValue]()
var typeForMultipartReader = reflect.TypeFor[*multipart.Reader]()
var typeForReadCloser = reflect.TypeFor[io.ReadCloser]()

func (tags *requestTags) checkRecursively(requestType reflect.Type, fieldIndex []int) {
	for index := range requestType.NumField() {
		field := requestType.Field(index)
		// skip if field is not exported
		if field.PkgPath != "" {
			continue
		}
		// process anonymous struct
		if field.Anonymous {
			if field.Type.Kind() != reflect.Struct {
				panic("BUG: anonymous field must be a struct")
			}
			tags.checkRecursively(field.Type, append(fieldIndex, field.Index...))
			continue
		}
		// process header tag
		if value, exists := field.Tag.Lookup("header"); exists {
			if value != "" {
				if tags.headerFieldIndex != nil {
					panic("BUG: multiple `header` tag fields are not allowed when empty `header` tag is present")
				}
			} else {
				if tags.flags&tagHeader != 0 {
					panic("BUG: multiple `header` tag fields are not allowed when empty `header` tag is present")
				}
				if field.Type != typeForHeader {
					panic("BUG: empty `header` tag field must be a `http.Header`")
				}
				tags.headerFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagHeader
		}
		// process cookie tag
		if value, exists := field.Tag.Lookup("cookie"); exists {
			if value != "" {
				if tags.cookieFieldIndex != nil {
					panic("BUG: multiple `cookie` tag fields are not allowed when empty `cookie` tag is present")
				}
			} else {
				if tags.flags&tagCookie != 0 {
					panic("BUG: multiple `cookie` tag fields are not allowed when empty `cookie` tag is present")
				}
				if field.Type != typeForKeyValues {
					panic("BUG: empty `cookie` tag field must be a `httpserver.KeyValues`")
				}
				tags.cookieFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagCookie
		}
		// process query tag
		if value, exists := field.Tag.Lookup("query"); exists {
			if value != "" {
				if tags.queryFieldIndex != nil {
					panic("BUG: multiple `query` tag fields are not allowed when empty `query` tag is present")
				}
			} else {
				if tags.flags&tagQuery != 0 {
					panic("BUG: multiple `query` tag fields are not allowed when empty `query` tag is present")
				}
				if field.Type != typeForKeyValues {
					panic("BUG: empty `query` tag field must be a `httpserver.KeyValues`")
				}
				tags.queryFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagQuery
		}
		// process url tag
		if value, exists := field.Tag.Lookup("url"); exists {
			if value != "" {
				if tags.urlFieldIndex != nil {
					panic("BUG: multiple `url` tag fields are not allowed when empty `url` tag is present")
				}
			} else {
				if tags.flags&tagUrl != 0 {
					panic("BUG: multiple `url` tag fields are not allowed when empty `url` tag is present")
				}
				if field.Type != typeForKeyValue {
					panic("BUG: empty `url` tag field must be a `httpserver.KeyValue`")
				}
				tags.urlFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagUrl
		}
		// process form tag
		if value, exists := field.Tag.Lookup("form"); exists {
			if value != "" {
				if tags.formFieldIndex != nil {
					panic("BUG: multiple `form` tag fields are not allowed when empty `form` tag is present")
				}
			} else {
				if tags.flags&tagForm != 0 {
					panic("BUG: multiple `form` tag fields are not allowed when empty `form` tag is present")
				}
				if field.Type != typeForKeyValues {
					panic("BUG: empty `form` tag field must be a `httpserver.KeyValues`")
				}
				tags.formFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagForm
		}
		// process json tag
		if value, exists := field.Tag.Lookup("json"); exists {
			if value != "" {
				if tags.jsonFieldIndex != nil {
					panic("BUG: multiple `json` tag fields are not allowed when empty `json` tag is present")
				}
			} else {
				if tags.flags&tagJson != 0 {
					panic("BUG: multiple `json` tag fields are not allowed when empty `json` tag is present")
				}
				tags.jsonFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			}
			tags.flags = tags.flags | tagJson
		}
		// process multipart tag
		if value, exists := field.Tag.Lookup("multipart"); exists {
			if value != "" {
				panic("BUG: `multipart` tag value must be empty")
			}
			if tags.flags&tagMultipart != 0 {
				panic("BUG: multiple `multipart` tag fields are not allowed")
			}
			if field.Type != typeForMultipartReader {
				panic("BUG: `multipart` tag field must be a `*multipart.Reader`")
			}
			tags.flags = tags.flags | tagMultipart
			tags.multipartFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
		}
		// process `body` tag
		if value, exists := field.Tag.Lookup("body"); exists {
			if tags.flags&tagBody != 0 {
				panic("BUG: multiple `body` tag fields are not allowed")
			}
			if field.Type != typeForReadCloser {
				panic("BUG: `body` tag field must be a `io.ReadCloser`")
			}
			tags.flags = tags.flags | tagBody
			tags.bodyFieldIndex = append(append([]int(nil), fieldIndex...), field.Index...)
			if value != "" {
				tags.bodyContentTypes = strings.Fields(value)
			}
		}
	}
}

//endregion requestTags

//region parseRequest

const (
	maxBodyLength       = 1 << 20 // 1 MiB
	maxReadBodyDuration = 5 * time.Second
)

func (tags *requestTags) parse(request *http.Request, parsed reflect.Value) (status int, parseErr error) {
	// parse and bind request header
	if tags.flags&tagHeader != 0 {
		if status, parseErr = tags.bindHeader(request, parsed); parseErr != nil {
			return
		}
	}
	// parse and bind cookies
	if tags.flags&tagCookie != 0 {
		if status, parseErr = tags.bindCookie(request, parsed); parseErr != nil {
			return
		}
	}
	// parse and bind url query values
	if tags.flags&tagQuery != 0 {
		if status, parseErr = tags.bindQuery(request, parsed); parseErr != nil {
			return
		}
	}
	// parse and bind url parameters
	if tags.flags&tagUrl != 0 {
		if status, parseErr = tags.bindUrl(request, parsed); parseErr != nil {
			return
		}
	}
	// parse and bind body
	switch request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		// check for empty request
		if request.ContentLength == 0 {
			return
		}
		// get content type
		contentTypeHeader := request.Header.Get("Content-Type")
		if contentTypeHeader == "" {
			return http.StatusUnsupportedMediaType, exception.String("HttpServer: Content-Type is missing")
		}
		// parse media type
		contentType, contentTypeParameters, err := mime.ParseMediaType(contentTypeHeader)
		if err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Content-Type is invalid").AddCause(err)
		}
		// parse and bind request body as form
		if tags.flags&tagForm != 0 && contentType == contentTypeIsForm {
			return bindFullTextBody(request, contentTypeParameters, parsed, tags.bindForm)
		}
		// parse and bind request body as JSON
		if tags.flags&tagJson != 0 && contentType == contentTypeIsJson {
			return bindFullTextBody(request, contentTypeParameters, parsed, tags.bindJson)
		}
		// parse and bind request body as multipart form
		if tags.flags&tagMultipart != 0 && contentType == contentTypeIsMultipart {
			return tags.bindMultipart(request, parsed, contentTypeParameters)
		}
		// bind request body raw
		if tags.flags&tagBody != 0 && (len(tags.bodyContentTypes) == 0 || slices.Contains(tags.bodyContentTypes, contentType)) {
			tags.bindBody(request, parsed)
			return
		}
		// nothing matched
		return http.StatusUnsupportedMediaType, exception.String("HttpServer: Content-Type is unsupported")
	}
	return
}

func bindFullTextBody(request *http.Request, contentTypeParameters map[string]string, parsed reflect.Value,
	binder func(reader io.Reader, parsed reflect.Value) (int, error)) (int, error) {
	if request.ContentLength < 0 {
		return http.StatusLengthRequired, exception.String("HttpServer: Content-Length is required but missing")
	} else if request.ContentLength > maxBodyLength {
		return http.StatusRequestEntityTooLarge, exception.String("HttpServer: Content-Length is too large")
	} else if reader, err := charsetReader(request.Body, contentTypeParameters); err != nil {
		return http.StatusUnsupportedMediaType,
			exception.String("HttpServer: cannot determine body encoding").AddCause(err)
	} else {
		type resultValue struct {
			status    int
			err       error
			recovered exception.Exception
		}
		done := make(chan resultValue, 1)
		// run the binder
		go func(binder func(reader io.Reader, parsed reflect.Value) (int, error), done chan<- resultValue) {
			defer exception.Recover(func(recovered exception.Exception) { done <- resultValue{recovered: recovered} })
			status, err := binder(reader, parsed)
			done <- resultValue{status: status, err: err}
		}(binder, done)
		// set time limit for binder
		select {
		case result := <-done:
			// re-panic recovered value
			if result.recovered != nil {
				panic(result.recovered)
			}
			return result.status, result.err
		case <-request.Context().Done():
			return http.StatusRequestTimeout,
				exception.String("HttpServer: Client diconnected").AddSuppressed(request.Body.Close())
		case <-time.After(maxReadBodyDuration):
			return http.StatusRequestTimeout,
				exception.String("HttpServer: Bind body timed out").AddSuppressed(request.Body.Close())
		}
	}
}

func (tags *requestTags) bindHeader(request *http.Request, parsed reflect.Value) (int, error) {
	// parse and bind request header
	if len(request.Header) > 0 {
		if tags.headerFieldIndex != nil {
			parsed.FieldByIndex(tags.headerFieldIndex).Set(reflect.ValueOf(request.Header))
		} else if err := common.BindStructWithTag("header", request.Header, parsed.Addr().Interface()); err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Bind request header failed").AddCause(err)
		}
	}
	return 0, nil
}

func (tags *requestTags) bindCookie(request *http.Request, parsed reflect.Value) (int, error) {
	// check if any cookies
	if cookies := request.Cookies(); len(cookies) > 0 {
		// convert cookies into key-values
		keyValues := make(KeyValues, len(cookies))
		for _, cookie := range cookies {
			keyValues[cookie.Name] = append(keyValues[cookie.Name], cookie.Value)
		}
		// parse and bind cookies
		if tags.cookieFieldIndex != nil {
			parsed.FieldByIndex(tags.cookieFieldIndex).Set(reflect.ValueOf(keyValues))
		} else if err := common.BindStructWithTag("cookie", keyValues, parsed.Addr().Interface()); err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Bind cookies failed").AddCause(err)
		}
	}
	return 0, nil
}

func (tags *requestTags) bindQuery(request *http.Request, parsed reflect.Value) (int, error) {
	// parse and bind url query values
	if values := request.URL.Query(); len(values) > 0 {
		if tags.queryFieldIndex != nil {
			parsed.FieldByIndex(tags.queryFieldIndex).Set(reflect.ValueOf(values))
		} else if err := common.BindStructWithTag("query", values, parsed.Addr().Interface()); err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Bind query values failed").AddCause(err)
		}
	}
	return 0, nil
}

func (tags *requestTags) bindUrl(request *http.Request, parsed reflect.Value) (int, error) {
	// skip when there is no matched route pattern
	if request.Pattern == "" {
		return 0, nil
	}
	// collect named URL parameters from the matched route pattern
	keyValue := getPathValues(request)
	// no URL parameters collected
	if len(keyValue) == 0 {
		return 0, nil
	}
	// parse and bind url parameters
	if tags.urlFieldIndex != nil {
		parsed.FieldByIndex(tags.urlFieldIndex).Set(reflect.ValueOf(keyValue))
	} else if err := common.BindStructWithTag("url", keyValue, parsed.Addr().Interface()); err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Bind url params failed").AddCause(err)
	}
	return 0, nil
}

func (tags *requestTags) bindForm(reader io.Reader, parsed reflect.Value) (int, error) {
	// read the whole body at once
	body, err := io.ReadAll(reader)
	if err != nil {
		return http.StatusInternalServerError, exception.String("HttpServer: Read request body failed").AddCause(err)
	}
	// parse form body
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Parse form body failed").AddCause(err)
	}
	// bind form body
	if tags.formFieldIndex != nil {
		parsed.FieldByIndex(tags.formFieldIndex).Set(reflect.ValueOf(values))
	} else if err := common.BindStructWithTag("form", values, parsed.Addr().Interface()); err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Bind form params failed").AddCause(err)
	}
	return 0, nil
}

func (tags *requestTags) bindJson(reader io.Reader, parsed reflect.Value) (int, error) {
	// check if decode the whole body to the JSON field
	var target any
	var values map[string]any
	if tags.jsonFieldIndex != nil {
		target = parsed.FieldByIndex(tags.jsonFieldIndex).Addr().Interface()
	} else {
		target = &values
	}
	// shared json decoder path
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return http.StatusBadRequest, exception.String("HttpServer: Decode json body failed").AddCause(err)
	}
	// check for trailing data
	if count, err := io.CopyN(io.Discard, reader, 1); count > 0 || err != nil && err != io.EOF {
		return http.StatusBadRequest, exception.String("HttpServer: Decode json body failed").AddCause(err)
	}
	// bind json body
	if tags.jsonFieldIndex == nil {
		if err := common.BindStructWithTag("json", values, parsed.Addr().Interface()); err != nil {
			return http.StatusBadRequest, exception.String("HttpServer: Bind json values failed").AddCause(err)
		}
	}
	return 0, nil
}

func (tags *requestTags) bindMultipart(
	request *http.Request, parsed reflect.Value, parameters map[string]string,
) (int, error) {
	// get multipart boundary
	boundary, ok := parameters["boundary"]
	if !ok {
		return http.StatusBadRequest,
			exception.String("HttpServer: Boundary is missing in Content-Type of a " + contentTypeIsMultipart)
	}
	parsed.FieldByIndex(tags.multipartFieldIndex).Set(reflect.ValueOf(multipart.NewReader(request.Body, boundary)))
	return 0, nil
}

func (tags *requestTags) bindBody(request *http.Request, parsed reflect.Value) {
	parsed.FieldByIndex(tags.bodyFieldIndex).Set(reflect.ValueOf(request.Body))
}

//endregion parseRequest
