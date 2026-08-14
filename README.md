# go-httpserver

`github.com/thanhminhmr/go-httpserver` is a declarative struct-tag driven HTTP server framework built on the standard
library [`http.ServeMux`](https://pkg.go.dev/net/http#ServeMux).

## At a glance

1. Call [`NewServer`](./server.go) with a logger and a `ServerConfig`. It returns a [`Router`](./export.go) wired to
   the [ctrl] lifecycle.
2. Use `Router.Group(middlewares...)` to derive a child `Router` that inherits the parent's middleware chain.
3. Use `Router.Handle(pattern, handler)` to register a [`Handler = func(ctx *Context)`](./export.go).
4. Wrap a typed handler with [`RequestParser[Request](handler RequestHandler[Request]) Handler`](./parser.go) to get
   binding, defaults, and validation from struct tags.
5. Wrap a typed middleware with
   [`MiddlewareParser[Request](handler MiddlewareHandler[Request]) Middleware`](./parser.go) to parse the request before
   the downstream handler runs.
6. Inside a handler, call `ctx.NewResponse(status)` to start a response and use the returned `Response`'s `*Body(...)`
   methods to configure the body. The `Router` writes the response after the handler/middleware chain runs.

## Source files

| File                                       | Responsibility                                                                               |
|--------------------------------------------|----------------------------------------------------------------------------------------------|
| [`export.go`](./export.go)                 | Re-exported aliases: `Handler`, `Middleware`, `Router`, `KeyValue`, `KeyValues`.             |
| [`server.go`](./server.go)                 | `ServerConfig`, `NewServer`, `httpServer` (lifecycle, logging, panic recovery).              |
| [`parser.go`](./parser.go)                 | `RequestParser` / `MiddlewareParser`, struct-tag introspection, request binding, validation. |
| [`response.go`](./response.go)             | `Context` and `Response` builder types.                                                      |
| [`tracker.go`](./tracker.go)               | `responseWriterTracker` wrapping `http.ResponseWriter` to record status and bytes.           |
| [`request_unsafe.go`](./request_unsafe.go) | Unsafe accessors into `net/http` internals for URL wildcard extraction.                      |
| [`helper.go`](./helper.go)                 | Internal utilities: zerolog adapters, unsafe string cast, charset reader.                    |

## export.go

Types and the `Router` re-exported for handler authors.

- `Handler` — `func(ctx *Context)`. The handler signature registered via `Router.Handle`.
- `Middleware` — `func(ctx *Context, next func())`. Wraps a `Handler` in a chain. Call `next` to continue the chain;
  code after the `next` call runs on the way out, mirroring `defer`.
- `Router` — `struct { serveMux *http.ServeMux; logger *zerolog.Logger; middlewares []Middleware }`. Holds a `ServeMux`,
  a logger, and an ordered middleware chain.
  - `Router.Group(middlewares ...Middleware) Router` — returns a child `Router` whose chain is the parent's chain
    followed by the supplied middlewares.
  - `Router.Handle(pattern string, handler Handler)` — registers `handler` under `pattern`. At registration time it logs
    the pattern, middlewares, and handler. At request time it builds a `Context`, runs the middleware chain, defaults to
    status 500 when no response was configured, and writes the response.
- `KeyValue` — `map[string]string`.
- `KeyValues` — `map[string][]string`.

## server.go

Server configuration, lifecycle wiring, and per-request logging and panic recovery.

- `ServerConfig` — configuration struct with `cfg` tags, `default` tags, and `validate` constraints. Fields: `Port`,
  `ReadHeaderTimeout`, `IdleTimeout`, `MaxHeaderBytes`, `ShutdownOnError`. Timeout values are in seconds. `NewServer`
  does not apply or validate the configuration tags.
- `NewServer(logger *zerolog.Logger, config *ServerConfig) Router` — creates the `http.ServeMux`, registers an
  `http.Server` with the [ctrl] lifecycle, and returns a `Router` wired to `logger`. The config must already be
  defaulted and validated, and should not be modified after this call.
- `httpServer` — internal struct holding the config, `*http.ServeMux`, and `http.Server`. It is its own `http.Handler`
  so per-request logging and panic recovery can wrap every dispatch.
  - `(*httpServer).runner(ctx, shutdown)` — calls `server.ListenAndServe`. On error other than `http.ErrServerClosed`,
    logs it and, when `ShutdownOnError` is set, cancels the lifecycle.
  - `(*httpServer).cleaner(ctx)` — calls `server.Shutdown(ctx)` during cleanup.
  - `(*httpServer).ServeHTTP(writer, request)` — derives a child logger carrying a random base-36 `request_id`, logs the
    incoming request, then defers a response log (status, bytes, duration) and a panic recovery via `exception.Recover`.
    The writer is wrapped with `responseWriterTracker` so the deferred log can read the status and bytes written by the
    inner `serveMux`.

## parser.go

Core of the framework. Defines the binding contract via struct tags and produces a `Handler` from a typed handler.

- `RequestHandler[Request]` — `func(ctx *Context, request Request)`.
- `RequestParser[Request](handler RequestHandler[Request]) Handler` — public entry point. Builds the `requestTags` cache
  for `Request`, then returns a closure that allocates a zero `Request`, applies defaults, parses, validates, and
  invokes the handler with the shared `Context`.
- `MiddlewareHandler[Request]` — `func(ctx *Context, request Request, next func())`.
- `MiddlewareParser[Request](handler MiddlewareHandler[Request]) Middleware` — like `RequestParser` but produces a
  `Middleware` that parses the request, then calls `next`. Parser middleware shares one `Context` with downstream parser
  handlers.
- `requestHandler(ctx, tags, parsed, next)` — shared orchestrator used by both `RequestParser` and `MiddlewareParser`:
  applies defaults via `common.ApplyDefaults`, calls `tags.parse`, runs `common.ValidateStruct`, then invokes `next`.
  All parse/validation failures log at error level and write the appropriate status to the shared `Context`.
- `requestTags` — cached descriptor for a `Request` struct. Holds bit flags (`tagHeader`, `tagCookie`, `tagQuery`,
  `tagUrl`, `tagForm`, `tagJson`, `tagMultipart`, `tagBody`) and field-index paths discovered during reflection.
- `globalTags` / `globalTagsMutex` — process-wide cache mapping `reflect.Type` to its `requestTags`, populated lazily by
  `createTags`.
- `createTags(requestType reflect.Type) requestTags` — cache lookup, defaults validation, and recursive tag scan via
  `checkRecursively`.
- `(*requestTags).checkRecursively` — walks the struct (including embedded anonymous structs), enforces the "at most one
  empty-tag field" rule per binding source, and records the field index path.
- `(*requestTags).parse(request, parsed) (status, err)` — orchestrates binding in order: header, cookie, query, url. For
  `POST`/`PUT`/`PATCH` with a body, selects the binder based on `Content-Type`: form
  (`application/x-www-form-urlencoded`), JSON (`application/json`), multipart (`multipart/form-data`), or raw `body`.
- `(*requestTags).bindHeader` — populates either the `http.Header` field (empty `header` tag) or calls
  `common.BindStructWithTag("header", ...)`.
- `(*requestTags).bindCookie` — collapses `request.Cookies()` into a `KeyValues` map and binds.
- `(*requestTags).bindQuery` — binds `request.URL.Query()` via the empty `query:""` field.
- `(*requestTags).bindUrl` — reads `request.PathValue` for each wildcard in `request.Pattern`, builds a `KeyValue`, and
  binds via the `url:""` field.
- `(*requestTags).bindForm` — reads the body, parses it as a URL-encoded form, and binds.
- `(*requestTags).bindJson` — decodes the JSON body either into the field at `jsonFieldIndex` or into a generic
  `map[string]any` for tag-based binding.
- `(*requestTags).bindMultipart` — extracts the boundary from the `Content-Type` parameters and constructs a
  `multipart.Reader` for the `multipart:""` field.
- `(*requestTags).bindBody` — assigns the raw `request.Body` to the `body:""` field.
- `bindFullTextBody` — shared path for form/JSON binding. Requires `Content-Length`, caps at `maxBodyLength` (1 MiB),
  runs the binder in a goroutine with a `maxReadBodyDuration` (5 s) deadline and cancellation on client disconnect.
- Constants `contentTypeIsForm`, `contentTypeIsJson`, `contentTypeIsMultipart` define the recognized `Content-Type`
  values for body binding.
- `maxBodyLength` and `maxReadBodyDuration` bound text body reads.

### Tag summary (for `Request` structs)

| Tag                    | Empty-tag field type | Purpose                                           |
|------------------------|----------------------|---------------------------------------------------|
| `header:"<Name>"`      | n/a                  | Bind a single header.                             |
| `header:""`            | `http.Header`        | Bind the whole header map by reference.           |
| `cookie:"<Name>"`      | n/a                  | Bind a single cookie.                             |
| `cookie:""`            | `KeyValues`          | Bind all cookies.                                 |
| `query:"<Name>"`       | n/a                  | Bind a single query parameter.                    |
| `query:""`             | `KeyValues`          | Bind all query parameters.                        |
| `url:"<Name>"`         | n/a                  | Bind a named ServeMux path wildcard.              |
| `url:""`               | `KeyValue`           | Bind all URL parameters.                          |
| `form:"<Name>"`        | n/a                  | Bind a single URL-encoded form field.             |
| `form:""`              | `KeyValues`          | Bind the whole form.                              |
| `json:"<Name>"`        | n/a                  | Bind a field from a JSON object body.             |
| `json:""`              | any                  | Decode the whole JSON body into this field.       |
| `multipart:""`         | `*multipart.Reader`  | Expose the body as a multipart reader.            |
| `body:"<Type>;<Type>"` | `io.ReadCloser`      | Bind the raw body for the listed Content-Types.   |
| `body:""`              | `io.ReadCloser`      | Bind the raw body when no other body binder ran.  |
| `default:"<value>"`    | any                  | Applied before parsing for fields with a default. |

## response.go

`Context` and `Response` builder types shared between the parser and the handler.

- `Context` — handler input passed by pointer. Wraps `request *http.Request`, `writer http.ResponseWriter`, and response
  state (`status int`, `body any`, `marshaller uint`). Implements `context.Context` via `Deadline/Done/Err/Value`
  delegating to the request context, so a `*Context` can be passed anywhere a `context.Context` is expected.
- `Context.NewResponse(status int) Response` — begins a response: sets `status`, clears the writer header map, and
  returns a `Response` handle.
- `Context.Response() Response` — returns a handle to the current response without changing its state. Its status is
  zero until `Context.NewResponse` is called.
- `Response` — `struct{ ctx *Context }`. A thin handle whose methods mutate the shared `Context`.
- `Response.Status()` — returns the configured status code.
- `Response.Body() any` — returns the configured body value (nil if no body set).
- `Response.Header()` — returns `ctx.writer.Header()` for direct mutation.
- `Response.Cookie(cookie http.Cookie)` — appends a `Set-Cookie` header.
- `Response.BytesBody([]byte)` — raw bytes, no Content-Type.
- `Response.StringBody(string)` — raw string, no Content-Type.
- `Response.StreamBody(func(io.Writer) error)` — streaming writer, no Content-Type.
- `Response.PlainTextBody(string)` — sets `Content-Type: text/plain; charset=utf-8`.
- `Response.OctetsBody([]byte)` — sets `Content-Type: application/octet-stream`.
- `Response.JsonBody(any)` — stores body for JSON marshaling when the response is written. Successful marshaling sets
  `Content-Type` to `application/json; charset=utf-8`. A marshal failure writes 500 Internal Server Error.
- `Context.writeResponse()` — serializes the response to the wire based on the body's concrete type. Unknown body types
  produce a 500.

## tracker.go

`responseWriterTracker` wraps an `http.ResponseWriter` to record the first status code passed to `WriteHeader` and to
count the bytes written. All writes are delegated to the underlying writer.

- `responseWriterTracker` — `struct { http.ResponseWriter; status int; bytesWritten int }`.
- `newResponseWriterTracker(w http.ResponseWriter)` — constructs a `responseWriterTracker`.
- `(*responseWriterTracker).WriteHeader(status int)` — records the first status passed and delegates to the underlying
  writer.
- `(*responseWriterTracker).Write(b []byte) (int, error)` — sets status to `http.StatusOK` if no status was set, then
  delegates and counts bytes written.
- `(*responseWriterTracker).Status() int` — returns the recorded status code, or 0 if `WriteHeader` has not been called
  and no body has been written yet.
- `(*responseWriterTracker).BytesWritten() int` — returns the total number of body bytes written so far.
- `(*responseWriterTracker).Unwrap() http.ResponseWriter` — exposes the underlying `http.ResponseWriter` so callers can
  access additional interfaces via `http.ResponseController`.

## request_unsafe.go

Unsafe accessors into `net/http` internals. These types and functions reach into `http.Request` fields that are not
exposed by the standard library, so they depend on the exact memory layout of `net/http` types.

- `httpSegment` — `struct { s string; wild bool; multi bool }`. A segment of an `http.ServeMux` pattern: `s` is the
  literal/wildcard name, `wild` marks a wildcard segment, `multi` marks a multi-segment wildcard (`{name...}`).
- `httpPattern` — `struct { str string; method string; host string; segments []httpSegment; loc string }`. A pattern
  registered with `http.ServeMux`, decomposed into segments for wildcard lookup.
- `httpRequest` — `struct { _ [33]uintptr; pat *httpPattern; matches []string }`. The unsafe overlay onto
  `*http.Request`: `pat` is the pattern that matched, and `matches` holds the values for the matching wildcards in
  `pat`. The `_ [33]uintptr` field pads past the `http.Request` header fields to reach the pattern/matches fields. This
  layout depends on the exact `net/http` version and is fragile across standard-library updates.
- `getPathValues(r *http.Request) KeyValue` — returns the named wildcard values captured by the matching `http.ServeMux`
  pattern, keyed by wildcard name. Returns nil if the request has no pattern (e.g., not matched by `ServeMux`).

## helper.go

Internal utilities used by `export.go` and `parser.go`.

- `funcObject(v any) exception.StackFrame` — resolves a function value to its stack frame via `exception.Function`;
  returns `<unknown>` on failure.
- `funcObjects[S ~[]E, E any](values S) exception.StackFrames` — maps `funcObject` over a slice.
- `unsafeStringToBytes(string) []byte` — zero-copy string→bytes via `unsafe.Slice`.
- `charsetReader(reader, params) (io.Reader, error)` — wraps a reader with a charset decoder based on the `charset`
  Content-Type parameter, or auto-detects UTF-8/UTF-16LE/UTF-16BE BOMs.

## License

[Mozilla Public License 2.0](./LICENSE.txt).
