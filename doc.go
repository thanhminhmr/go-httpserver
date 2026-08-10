/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// Package httpserver provides typed request parsing and response handling for
// [chi].
//
// [RequestParser] and [MiddlewareParser] bind an HTTP request into a struct,
// apply `default` values, validate the result, and call a typed handler.
// Supported binding tags are `header`, `cookie`, `query`, `url`, `form`, `json`,
// `multipart`, and `body`.
//
// Non-empty `header`, `cookie`, `query`, `url`, `form`, and `json` tags bind the
// named value. An empty tag captures the whole source:
//
//   - `header:""`: [http.Header]
//   - `cookie:""`, `query:""`, `form:""`: [KeyValues]
//   - `url:""`: [KeyValue]
//   - `json:""`: decode the whole JSON body into the field
//   - `multipart:""`: [*multipart.Reader]
//   - `body:""`: [io.ReadCloser] for any otherwise unmatched media type
//
// A whole-source tag cannot be mixed with named tags for the same source. Body
// fields must be [io.ReadCloser]; a non-empty `body` tag is a
// whitespace-separated allowlist of media types, for example `body:"text/plain
// application/xml"`. Defaults are applied first; headers, cookies, query values,
// URL parameters, and the selected body binder are then applied in that order,
// so later sources can overwrite earlier ones.
//
// Body binding is attempted for POST, PUT, PATCH, and DELETE requests unless
// Content-Length is exactly zero. The Content-Type selects form, JSON,
// multipart, or raw-body binding. Form and JSON bodies require a known
// Content-Length, reject declared lengths above 1 MiB, and time out after 5
// seconds while being read.
//
// Parse or validation failures return an empty HTTP error response without
// calling the handler. Handlers build responses with [Context.NewResponse] and
// [Response].
package httpserver
