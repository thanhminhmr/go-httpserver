# go-httpserver

Internal developer documentation for `github.com/thanhminhmr/go-httpserver`, a declarative struct-tag driven HTTP server
framework built on the standard library [`http.ServeMux`](https://pkg.go.dev/net/http#ServeMux).

## Source files

| File                           | Responsibility                                                            |
|--------------------------------|---------------------------------------------------------------------------|
| [`export.go`](./export.go)     | Re-exported aliases: `Middleware`, `KeyValue`, `KeyValues`.               |
| [`server.go`](./server.go)     | Server config, lifecycle wiring, default request logger.                  |
| [`parser.go`](./parser.go)     | Struct-tag introspection, request binding, validation.                    |
| [`response.go`](./response.go) | `Context` and `Response` builder types.                                   |
| [`helper.go`](./helper.go)     | Internal utilities: zerolog adapters, unsafe string cast, charset reader. |

## export.go

Type aliases re-exported for handler authors.

- `Middleware` — `func(http.Handler) http.Handler`.
- `KeyValue` — `map[string]string`.
- `KeyValues` — `map[string][]string`.

## server.go

- `ServerConfig` — configuration struct with `cfg` tags, `default` tags, and   `validate` constraints. Fields: `Port`,
  `ReadHeaderTimeout`, `IdleTimeout`, `MaxHeaderBytes`, `ShutdownOnError`.
- `NewServer(config *ServerConfig) *http.ServeMux` — constructs the ServeMux, wraps it with the `requestLogger`
  middleware, and registers a runner/cleaner pair with `ctrl.Register` so the server starts and shuts down with the
  application lifecycle.
- `httpServer` — internal struct holding the config, router, and `http.Server`.
- `(*httpServer).runner` — calls `server.ListenAndServe`. Triggers `shutdown` on error when `ShutdownOnError` is set.
- `(*httpServer). cleaner` — calls `server.Shutdown(ctx)` during cleanup.
- `requestLogger` — middleware that attaches a per-request zerolog logger with a random base-36 `request_id`, logs the
  incoming request, and on exit logs status, bytes written, and duration. Panics are recovered and surfaced as 500s.

## parser.go

Core of the framework. Defines the binding contract via struct tags and generates an `http.HandlerFunc` for a typed
handler.

- `RequestHandler[Request]` — `func(ctx *Context, request Request)`.
- `RequestParser[Request](handler RequestHandler[Request]) http.HandlerFunc` — public entry point. Builds the
  `requestTags` cache for `Request`, then returns a closure that allocates a zero `Request`, applies defaults, parses,
  validates, and invokes the handler.
- `MiddlewareHandler[Request]` — `func(ctx *Context, request Request, next func(ctx *Context))`.
- `MiddlewareParser[Request](handler MiddlewareHandler[Request]) Middleware` — like `RequestParser` but produces a
  `Middleware` that parses the request, then calls `next` with the server `Context`. If a `Context` already exists in
  the request (set by an outer `RequestParser` or `MiddlewareParser`), it is reused.
- `requestHandler` — shared orchestrator used by both `RequestParser` and `MiddlewareParser`: gets or creates a server
  `Context` (stored in the request context under `serverCtxKey`), applies defaults, calls `tags.parse`, runs
  `common.ValidateStruct`, then invokes the user handler. All parse/validation failures log at error level and write the
  appropriate status.
- `serverCtxKey` — `reflect.TypeFor[requestTags]()`, used as the context key for the server `Context`.
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

- `Context` — handler input passed by pointer. Wraps `request *http.Request`, `writer http.ResponseWriter`, and response
  state (`status int`, `body any`). Implements `context.Context` via `Deadline/Done/Err/Value` delegating to the request
  context, so a `*Context` can be passed anywhere a `context.Context` is expected.
- `Context.NewResponse(status int) Response` — begins a response: sets `status`, clears the writer header map, and
  returns a `Response` handle.
- `Response` — `struct{ ctx *Context }`. A thin handle whose methods mutate the shared `Context`.
- `Response.Status()` — returns the configured status code.
- `Response.Header()` — returns `ctx.writer.Header()` for direct mutation.
- `Response.Cookie(cookie http.Cookie)` — appends a `Set-Cookie` header.
- `Response.BytesBody([]byte)` — raw bytes, no Content-Type.
- `Response.StringBody(string)` — raw string, no Content-Type.
- `Response.StreamBody(func(io.Writer) error)` — streaming writer, no Content-Type.
- `Response.PlainTextBody(string)` — sets `Content-Type: text/plain; charset=utf-8`.
- `Response.OctetsBody([]byte)` — sets `Content-Type: application/octet-stream`.
- `Response.JsonBody(any)` — sets `Content-Type: application/json; charset=utf-8`.
- `Response.MarshalZerologObject(e)` — implements `zerolog.LogObjectMarshaler` so a response can be embedded in
  structured log entries.
- `Context.writeResponse()` — serializes the response to the wire based on the body's concrete type. Unknown body types
  produce a 500.

## helper.go

- `funcObject(v any) exception.StackFrame` — resolves a function value to its stack frame via `exception.Function`;
  returns `<unknown>` on failure.
- `funcObjects[S ~[]E, E any](values S) exception.StackFrames` — maps `funcObject` over a slice.
- `unsafeStringToBytes(string) []byte` — zero-copy string→bytes via `unsafe.Slice`.
- `charsetReader(reader, params) (io.Reader, error)` — wraps a reader with a charset decoder based on the `charset`
  Content-Type parameter, or auto-detects UTF-8/UTF-16LE/UTF-16BE BOMs.

## License

[Mozilla Public License 2.0](./LICENSE.txt).
