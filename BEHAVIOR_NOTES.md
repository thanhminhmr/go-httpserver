# Behavioral notes and review candidates

This document records behavior that is easy to miss when reading the public API, differs from common `net/http`
expectations, or may be worth changing before the package API stabilizes.

It is intentionally separate from the package Godoc. Some items below are probably bugs, while others may be deliberate
tradeoffs that should simply be documented and tested.

## Highest-priority review candidates

### `NewResponse` clears all staged response headers (DECIDED)

Every call to `Context.NewResponse` executes:

```go
clear(c.writer.Header())
```

That clears every header currently staged on the response writer — not only headers previously configured through
`Response`, but also headers set by middleware before `next()`.

This is an explicit API decision. Middleware that needs its headers to survive a downstream `NewResponse` call must set
those headers *after* `next()` returns (on the way out of the chain). The `RequestParser(handler, A, B)` ordering
guarantees that A-after runs after B-after, which runs after the handler returns.

---

### `NewResponse` resets the response marshaller (FIXED)

`Context.NewResponse` now resets `status`, `body`, *and* `marshaller`:

```go
c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
clear(c.writer.Header())
```

Previously the marshaller was not reset, so the result depended on the body type configured before the most recent
`NewResponse` call. That is no longer possible.

---

### Nested parse errors are represented in `Context` (FIXED)

Binding and validation errors are now routed through `Context.NewResponse(status)` rather than being written directly
via `writer.WriteHeader(status)`.

The outermost parser performs the final write after the chain returns, so middleware observing `Context.Response()`
after `next()` sees the actual failure status. There is no longer a window where the wire response is already committed
as 400/408/411/413/415 while the shared `Context.status` is still zero.

---

### `MiddlewareParser` no longer wraps an `http.Handler` (RESOLVED)

The previous design had `MiddlewareParser` return `func(next http.Handler) http.Handler`, which led to a mismatch
between conventional `net/http` handlers writing directly and framework state.

With the new framework-only `Middleware` shape (`func(ctx *Context, next func())`), middleware cannot be ordinary
`net/http` code; everything flows through `Context`. The boundary between `net/http` and the framework is now exactly
at `RequestParser`'s returned `http.HandlerFunc`.

---

### Mixed parser and conventional middleware can lose request context updates (RESOLVED)

This was an artifact of the old shared-context design (`serverCtxKey` stashing and `Context.request` retention).

With `serverCtxKey` removed and `Context.request` set once from the original request, the failure mode this note
described no longer applies to the new `Middleware`-shaped API.

---

### JSON parsing accepts one value without checking for trailing input (FIXED)

`bindJson` now checks for trailing data after the first decode. A body containing one valid JSON value followed by any
non-whitespace trailing content is rejected.

---

### `NewResponse` does not reset the response marshaller (FIXED)

`Context.NewResponse` resets `status`, `body`, *and* `marshaller`:

```go
c.status, c.body, c.marshaller = status, nil, marshallerIsDirect
clear(c.writer.Header())
```

Previously the marshaller was not reset, so the result depended on the body type configured before the most recent
`NewResponse` call.

## Request-binding semantics that may surprise users

### Request bodies are parsed only for POST, PUT, PATCH, and DELETE (INTENDED)

Body tags are ignored for every other method (GET, HEAD, OPTIONS, etc.).

For `POST`, `PUT`, `PATCH`, and `DELETE`, `ContentLength != 0` causes the parser to inspect `Content-Type` even when
the request struct declares no body-binding fields.

Consequences include:

- missing `Content-Type` -> 415 Unsupported Media Type;
- malformed `Content-Type` -> 400 Bad Request;
- no declared binder matching the media type -> 415 Unsupported Media Type.

An endpoint that only cares about query/header/path data can therefore reject a request solely because the client
happened to send an unrelated body.

---

### JSON and form bodies require a known `Content-Length` (INTENDED)

Full-text binding rejects `ContentLength < 0` with 411 Length Required. In normal Go HTTP handling, a chunked request
commonly has an unknown content length, so chunked JSON and form bodies are rejected.

Raw `body` and `multipart` bindings do not make the same length check, so the accepted transfer semantics differ by
binder.

---

### Size and time limits apply only to form and JSON binding (INTENDED)

Form and JSON bodies have a 1 MiB declared-length limit and a 5 second bind timeout.

Raw-body and multipart bindings expose readers directly and have no equivalent framework-level size or read-time limit.
Those endpoints therefore rely on the surrounding HTTP server, proxy, or handler code for body limits and deadlines.

This difference matters for resource-exhaustion behavior.

---

### Raw `body` content types use a whitespace-separated list (FIXED)

A tag such as:

```go
Body io.ReadCloser `body:"text/plain application/xml"`
```

is split with `strings.Fields(value)`, which trims whitespace and splits on runs of whitespace.

That means:

```go
`body:"text/plain application/xml"`
`body:"text/plain  application/xml"`
`body:" text/plain application/xml "`
```

all produce the same two-entry list. This is stricter than the previous semicolon-based syntax and avoids ambiguity
with media-type parameters.

---

### Empty aggregate tags are exclusive for their source (INTENDED)

An empty source tag captures the entire source, for example:

```go
Headers http.Header `header:""`
```

Once an empty tag exists for a source, no other field anywhere in the recursively scanned request type may use the same
source tag. Parser construction panics if both aggregate and named bindings are present.

The exclusivity applies across anonymous embedded structs as well as the top-level struct.

---

### Anonymous pointer embedding is rejected (INTENDED)

Parser tag discovery recursively descends only through anonymous fields whose kind is `struct`. An anonymous
`*SomeStruct` causes parser construction to panic.

This differs from common Go embedding patterns where pointer embedding is valid and useful.

---

### Named nested structs are not recursively scanned for parser tags (INTENDED)

Tag discovery recursively descends through anonymous embedded structs, not arbitrary named struct fields.

For example, an inner `query` tag in this shape is not independently discovered:

```
type Request struct {
	Filters struct {
		Name string `query:"name"`
	}
}
```

Nested JSON can still work when the outer field itself participates in JSON binding, because that path delegates
decoding/binding of the nested value.

---

### A request value can still contain aliases to request-owned data (INTENDED)

`RequestHandler` receives the request struct by value, but several bound field types are references:

- empty `header:""` receives the request's live `http.Header` map;
- empty query/cookie/form tags receive maps;
- `multipart:""` receives a reader over the request body;
- `body:""` receives the request body itself;
- slices and pointer-valued fields remain reference-bearing values.

Passing the struct by value therefore does not make all of its contents independent or safe to retain after the HTTP
request finishes.

In particular, the raw body and multipart reader should be consumed before the handler returns unless ownership/lifetime
is changed explicitly.

## JSON binding is intentionally not one uniform decoding model

### Named `json:"field"` bindings and `json:""` bindings behave differently (INTENDED)

With one or more named JSON tags, the body is first decoded into `map[string]any` using `Decoder.UseNumber`, then passed
through `common.BindStructWithTag`.

With an empty `json:""` tag, the body is decoded directly into that field using `encoding/json`.

These paths can therefore differ in conversion behavior, custom unmarshaling, and accepted shapes.

---

### Named JSON fields use coercive binding (INTENDED)

The test suite intentionally covers behavior such as:

- JSON numbers being preserved as `json.Number` before binding;
- a scalar JSON value being wrapped into a one-element slice target;
- `encoding.TextUnmarshaler`-style conversions;
- additional mapstructure-style conversions.

That is convenient for request binding, but it is less strict than users may expect from ordinary `encoding/json` struct
decoding. A client can sometimes send a different JSON type and still obtain a valid Go target value.

If wire-schema strictness matters, this should be an explicit design choice.

---

### Named JSON binding expects an object-like top level (INTENDED)

When the request type uses named JSON tags, the decoder target is `map[string]any`. A top-level array or scalar does not
behave like direct struct decoding.

Use `json:""` when the endpoint should decode the complete JSON value directly into one field.

---

### Unknown JSON fields are not rejected (INTENDED)

The JSON decoder does not enable `DisallowUnknownFields`. Named-field mode also binds only the fields requested by tags.

This makes the API forward-compatible with extra client fields, but it can also hide misspelled request keys.

## Panic and error behavior is not uniform across bind sources (FIXED)

Form and JSON body binding run in a goroutine wrapped with panic recovery. A panic during those bind operations is
converted to a 500 parser error.

Header, cookie, query, and URL binding run synchronously in the request goroutine. A panic raised by custom conversion
code on those paths can escape `RequestParser`; it is recovered only when the handler is running behind `NewServer`'s
built-in recovery middleware or another recovery layer.

If `RequestParser` is intended to provide the same failure contract for every source, this asymmetry may be worth
removing.

## Response-building behavior

### JSON bodies are marshaled after the handler chain returns (INTENDED)

`JsonBody` stores the Go value and defers `json.Marshal` until the framework writes the response.

If the stored value points to mutable state, changes made after `JsonBody` but before the outermost parser returns can
affect the wire JSON.

This can be useful for after-`next()` middleware, but it is different from APIs that serialize immediately.

---

### JSON marshal failures keep the JSON Content-Type (FIXED)

`JsonBody` sets:

```text
Content-Type: application/json; charset=utf-8
```

before marshaling occurs. If `json.Marshal` later fails, the framework writes 500 Internal Server Error with no
generated error document, but the JSON Content-Type remains staged.

**Possible change:** clear or replace the content type on marshal failure, or emit a valid JSON error body.

---

### Stream errors cannot change the HTTP status (INTENDED)

`StreamBody` commits the configured status before invoking the stream function. If the function returns an error, the
framework can log the error but cannot replace the already-started response status.

Partial body data may already have reached the client.

---

### Raw string/byte/stream bodies do not explicitly set Content-Type (INTENDED)

`StringBody`, `BytesBody`, and `StreamBody` do not set a Content-Type header themselves.

The final wire behavior can depend on the concrete `http.ResponseWriter`. In particular, tests using
`httptest.ResponseRecorder` should not be treated as proof that a production `net/http` server will never infer or add
response metadata.

## Logging and observability

### Request URLs are logged at info level, including query strings (INTENDED)

The built-in request logger records `request.URL.String()`. Query parameters are therefore included in normal info-level
request logs.

Applications that place tokens, email addresses, search terms, or other sensitive values in query strings should account
for this.

---

### Parsed requests and response contents are logged at trace level (INTENDED)

Parser trace logging includes the fully parsed request value. Response trace logging includes the configured status,
headers, and body through `Response.MarshalZerologObject`.

Depending on request/response structs, trace logs can therefore contain:

- cookies or authorization-like values copied into fields;
- personal data;
- `Set-Cookie` response headers;
- complete JSON response objects;
- other application secrets.

Trace logging should be treated as potentially sensitive output rather than harmless diagnostics.

---

### The generated request ID is a correlation value, not a security token (INTENDED)

The built-in logger generates `request_id` with `math/rand/v2`. That is suitable for log correlation but should not be
interpreted as an authentication token, capability, or unpredictable secret.

## Server lifecycle and deployment behavior

### `NewServer` binds on all interfaces (INTENDED)

The configured address is `:<port>`, so the server listens on all available interfaces rather than loopback-only.

That is often correct for containers and services, but it is worth making intentional for local tools and development
binaries.

---

### `NewServer` retains the configuration pointer (INTENDED)

The `ServerConfig` pointer passed to `NewServer` is captured and read later when the lifecycle runner is created.
`ShutdownOnError` is also read from the retained configuration when `ListenAndServe` exits unexpectedly.

Mutating the configuration after registration can therefore change later behavior, and concurrent mutation can introduce
a data race.

Treat the config as immutable after calling `NewServer`.

---

### Only a subset of `http.Server` timeouts are configured (INTENDED)

`NewServer` sets `ReadHeaderTimeout`, `IdleTimeout`, and `MaxHeaderBytes`. It does not set `ReadTimeout` or
`WriteTimeout`.

Form/JSON parsing has its own 5 second timeout, but raw and multipart bodies do not. Whether that is sufficient depends
on the deployment's reverse proxy, transport, and endpoint behavior.

---

### `ShutdownOnError == false` can leave the application running without HTTP (INTENDED)

If `ListenAndServe` returns an unexpected error and `ShutdownOnError` is false, the server runner returns without asking
the application lifecycle to shut down.

That means the process can remain alive while its HTTP server is no longer serving.

This may be useful for multi-service processes, but for a single-purpose HTTP service it can turn a fatal serving
failure into a silent partial outage.

## Suggested order of decisions

If the package is approaching a stable public API, the highest-value decisions are probably:

1. decide whether body parsing should be method-restricted and whether body negotiation should happen when no body tags
   exist;
2. make size/deadline policy consistent across body binders, or document the differences prominently;
3. decide whether JSON marshal failures should emit a valid JSON error body or just a 500;
4. decide whether the `NewResponse` header-clear behavior should preserve middleware-set headers or stay as-is;
5. decide whether parser-source tags should be allowed in named nested structs (currently they are ignored).

The remaining items are mostly documentation and expectation-setting choices rather than correctness bugs.
