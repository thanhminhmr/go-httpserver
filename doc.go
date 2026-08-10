/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// Package httpserver is a declarative, struct-tag driven HTTP server framework
// built on top of [chi]. It binds an incoming HTTP request to a user-supplied
// Request struct by reflecting over its fields and their tags, runs validation,
// then invokes the user's handler.
//
// # Entry points
//
// Two generic constructors turn a typed handler into an [http.Handler]:
//
//   - [RequestParser] returns an [http.HandlerFunc] that parses, validates, and
//     dispatches to the user handler.
//   - [MiddlewareParser] returns a [Middleware] that parses and validates the
//     request, then calls the next handler with the bound Context.
//
// A handler writes its reply through the [Context] it receives. [Context.NewResponse]
// starts a response, and the body/header methods on [Response] configure it; the
// framework writes the response after the handler returns.
//
// # Request binding
//
// Binding is driven by the struct tags listed below, applied from lowest to
// highest priority: header, cookie, query, url, form, json, multipart, body.
// When a field carries an empty tag value (e.g. `header:""`), it captures the
// whole source; only one such field is allowed per source.
//
//	`header:"<Name>"`  Bind a single header. Empty tag requires [http.Header].
//	`cookie:"<Name>"`  Bind a single cookie. Empty tag requires [KeyValues].
//	`query:"<Name>"`   Bind a single query parameter. Empty tag requires [KeyValues].
//	`url:"<Name>"`     Bind a named chi URL segment. Empty tag requires [KeyValue].
//	`form:"<Name>"`    Bind a URL-encoded form field. Empty tag requires [KeyValues].
//	`json:"<Name>"`    Bind a field from a JSON object body.
//	`json:""`          Decode the whole JSON body into this field.
//	`multipart:""`     Expose the body as a [*multipart.Reader].
//	`body:"<Type>"`    Bind the raw body for the listed Content-Types as [io.ReadCloser].
//	`body:""`          Bind the raw body when no other body binder matched.
//	`default:"<v>"`    Applied before parsing for fields with a default.
//
// For POST/PUT/PATCH requests with a body, the binder is selected from the
// Content-Type:
//
//   - application/x-www-form-urlencoded → form binder
//   - application/json                  → json binder
//   - multipart/form-data               → multipart binder
//   - otherwise                         → raw body binder
//
// Text bodies (form and JSON) are read via [bindFullTextBody], which requires a
// Content-Length, caps the body at maxBodyLength (1 MiB), and runs the binder in
// a goroutine with a maxReadBodyDuration (5 s) deadline and cancellation on
// client disconnect.
//
// # Validation
//
// After binding, the request is validated via [common.ValidateStruct]. Tag-based
// validators (e.g. `validate:"required,min=2"`) are honored on any field.
//
// # Errors
//
// If parsing or validation fails, the handler is not invoked and an HTTP error
// response is sent with the appropriate status code and an empty body; the
// failure is logged at error level on the request logger. Parse failures use the
// status returned by the specific binder (e.g. 400 for malformed input, 415 for
// an unsupported or missing Content-Type); validation failures always return 400
// Bad Request.
package httpserver
