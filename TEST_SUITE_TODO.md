# go-httpserver Test Suite Upgrade TODO

## Scope and review status

This plan is based on the updated `go-httpserver(3).tgz` tree.

Intentional design choices that must **not** be reopened by this work:

- Keep the unsafe `net/http` ServeMux path extraction. Preserve its layout-canary and functional path-binding tests.
- Keep full request/response trace logging. Sensitive trace output is within the intended log security boundary.

Previously reported issues that are already fixed are not treated as current bugs. In particular, `Router.Group` now copies its middleware slice, `NewResponse` rejects 1xx statuses, JSON marshal failure clears response headers, and the repository now contains `go.sum` and `LICENSE.txt`. Where useful, this plan adds regression tests for those fixes.

The current suite is parser-heavy (239 tests) but has no direct tests of `Router.Handle`, `Router.Group`, or `MiddlewareParser`. The main goal is to make the tests reflect the actual framework execution path instead of mostly testing a parallel test-only adapter.

> Execution note: this review environment has Go 1.23.2 while the module requires Go 1.25.0, and required modules/toolchains are not locally available. The project suite could not be executed here. The JSON decoder behavior described below was reproduced independently with the standard library.

## Confirmed remaining problems

1. **JSON trailing-data fix currently rejects valid trailing whitespace.** `bindJson` checks `decoder.Buffered()` plus the underlying reader for any remaining byte. Valid JSON followed by `\n`, spaces, or tabs is therefore rejected. This is especially important because the shared `withJSONBody` test helper uses `json.Encoder.Encode`, which appends a newline.
2. **The real router/middleware composition layer is effectively untested.** Existing parser helpers recreate the response lifecycle instead of calling `Router.Handle`; no test exercises `Router.Group` or `MiddlewareParser`.
3. **`responseWriterTracker` records the last `WriteHeader` call, not the actual final HTTP status.** Repeated final `WriteHeader` calls can make the tracked status differ from the underlying response, and 1xx informational responses are not modeled correctly.
4. **`httpServer.ServeHTTP` has no behavioral tests.** Panic recovery, tracker integration, request logger injection, and response logging are uncovered.
5. **Timeout/goroutine tests are slow and timing-sensitive.** They contain real 5-second waits, arbitrary sleeps, redundant manual body closes, and one test that only tests `io.Pipe` rather than package behavior.
6. **Concurrency tests provide weak guarantees.** One only checks status codes; the other constructs a new ServeMux/parser per goroutine and is still named after Chi. Neither strongly proves request isolation through the actual shared router.
7. **Response tests are duplicated and miss important state/error contracts.** Several cases exist in both `response_test.go` and `parser_response_test.go`, while status boundaries, response reset semantics, context delegation, and write-error propagation are missing.
8. **Several tests only test dependencies/standard-library behavior.** These add maintenance cost without protecting this package.
9. **Benchmarks are not correctness-guarded.** In particular, `BenchmarkComplex_Request` builds a flat JSON body even though the request expects an `address` object, so it may benchmark an error path without noticing.
10. **Test names/comments and nearby GoDocs still contain stale pre-ServeMux/pre-middleware assumptions.** These make the intended architecture harder to understand and can mislead future test changes.

---

## TODO 1 — Restore a trustworthy JSON baseline

- [ ] Add focused JSON trailing-content tests in `parser_body_tags_test.go` before changing production code.
  - Accept a JSON value with no trailing bytes.
  - Accept trailing JSON whitespace: space, `\n`, `\r`, and `\t`.
  - Reject a second JSON value, e.g. `{"a":1}{"b":2}`.
  - Reject non-whitespace garbage after a valid JSON value.
  - Cover both the whole-body `json:""` path and the named-field JSON binding path at least once.
- [ ] Replace the current “any remaining byte is invalid” logic in `bindJson` with a decoder-level EOF check.
  - Decode the intended value once.
  - Attempt a second decode into a throwaway value.
  - Accept only `io.EOF` from the second decode.
  - Do not manually reject trailing whitespace.
- [ ] Keep `decoder.UseNumber()` behavior unchanged.
- [ ] Do not change `withJSONBody`; its newline is useful because it exercises valid encoder output.

**Verify before continuing**

```bash
go test ./... -run 'TestJsonTag|TestPriority_Json|TestValidation.*Json'
go test ./...
```

Expected result: all existing JSON tests plus the new whitespace/extra-value tests pass.

## TODO 2 — Separate parser-unit helpers from real framework integration helpers

- [ ] Keep a small direct `Handler -> http.Handler` adapter only for narrow parser/response unit tests, but stop describing it as equivalent coverage for `Router.Handle`.
- [ ] Rename `asHTTPHandler` to make its test-only scope explicit, for example `serveParserHandler` or `asTestHTTPHandler`.
- [ ] Add a `newTestRouter` helper that constructs:
  - a fresh `http.ServeMux`;
  - a disabled/nop `zerolog.Logger`;
  - a real `Router` using those values.
- [ ] Add a `doRouterRequest` helper that registers through `Router.Handle` and dispatches through the actual ServeMux.
- [ ] Keep `doRequest` for focused binding tests; do **not** migrate all 164 call sites just to increase integration usage.
- [ ] Use the router helper for all new router, middleware, and end-to-end response tests.

**Verify before continuing**

```bash
go test ./... -run 'TestRouterTestHelperSmoke'
```

The smoke test must prove that the helper reaches a handler registered with the real `Router.Handle`.

## TODO 3 — Add direct `Router` and `Router.Group` contract tests

Create `router_test.go` and test the actual dispatcher.

- [ ] Handler response is written with the configured status/body.
- [ ] A handler that configures no response produces 500 through `Router.Handle` itself.
- [ ] Middleware order is exactly:
  - outer before;
  - inner before;
  - handler;
  - inner after;
  - outer after.
- [ ] Middleware can short-circuit by not calling `next`; downstream middleware/handler must not run.
- [ ] Middleware after `next` can intentionally replace the downstream response with `NewResponse`.
- [ ] `Group` inherits parent middleware in parent-first order.
- [ ] Nested groups preserve order across all levels.
- [ ] Creating a child group does not mutate the parent or a sibling group.
- [ ] Add a regression test for the already-fixed caller-slice aliasing bug:
  - create `middlewares := []Middleware{m1}`;
  - call `group := root.Group(middlewares...)`;
  - mutate `middlewares[0]` afterward;
  - verify `group` still uses `m1`.
- [ ] Verify two routes registered from the same router/group remain independent.

**Verify before continuing**

```bash
go test ./... -run '^TestRouter'
go test ./... -shuffle=on -count=20 -run '^TestRouter'
```

## TODO 4 — Add `MiddlewareParser` integration coverage

Create `middleware_parser_test.go`. These tests must use the real router helper from TODO 2.

- [x] Successful parser middleware binds request data and calls downstream exactly once.
- [x] Defaults are applied before the middleware handler runs.
- [x] Validation failure sets 400 and does not call downstream.
- [x] Parse/bind failure sets the expected status and does not call downstream.
- [x] Parsed middleware and a downstream `RequestParser` share the same `*Context` and response state.
- [x] A middleware can inspect the downstream response after `next` and then modify/replace it.
- [x] A middleware can configure a response and short-circuit without calling `next`.
- [x] A chain that finishes without any response still becomes 500 via `Router.Handle`.
- [x] Lock in the documented body behavior:
  - header/query-only parser middleware followed by a body parser succeeds;
  - two parser layers that both consume the body are not silently rewound/replayed.
- [x] Add one nested `Router.Group(MiddlewareParser(...))` case so group composition and typed middleware are tested together.

**Verify before continuing**

```bash
go test ./... -run 'TestMiddleware|TestRouter'
go test ./... -race -run 'TestMiddleware|TestRouter'
```

## TODO 5 — Define and test `responseWriterTracker` semantics, then fix it

Create `tracker_test.go` before modifying `tracker.go`.

- [x] Initial state: status 0, bytes 0, `Unwrap()` returns the exact underlying writer.
- [x] First body `Write` with no final status records implicit 200 and the returned byte count.
- [x] Multiple body writes accumulate the actual `n` values returned by the underlying writer.
- [x] Repeated final statuses preserve the **first committed 2xx-5xx status**, matching the underlying HTTP response rather than the last attempted `WriteHeader`.
- [x] Informational responses do not become the final tracked status:
  - `103 -> 200` tracks 200;
  - multiple 1xx responses followed by a body write track 200;
  - 1xx followed by a final error tracks that final error.
- [x] Use a purpose-built fake writer for 1xx tests instead of relying on `httptest.ResponseRecorder`'s simplified header behavior.
- [x] Refactor `tracker.go` so it distinguishes informational writes from the first final status.
- [x] Keep panic-recovery needs in mind: the tracker must still be able to tell `httpServer` whether a final response has been committed.

**Verify before continuing**

```bash
go test ./... -run '^TestResponseWriterTracker'
go test ./... -race -run '^TestResponseWriterTracker'
```

## TODO 6 — Test `httpServer.ServeHTTP` as an integration boundary

Expand `server_test.go`; move unrelated helper tests out later in TODO 10.

- [x] Construct `httpServer` directly with a fresh ServeMux; do not involve the global `ctrl` lifecycle for these request-path tests.
- [x] Normal handler response passes through with correct status and body.
- [x] Panic before a final response is committed is recovered and becomes 500.
- [x] Panic after a final response has already been committed does not attempt to replace the wire statuses.
- [x] Informational response followed by panic still permits recovery to emit a final 500; this is the integration regression for TODO 5.
- [x] Request context values from the original request remain available inside the handler.
- [x] A zerolog logger is installed into the request context seen by the inner handler.
- [x] Capture zerolog output and assert stable fields rather than exact serialized lines:
  - request log contains method/host/path/request_id;
  - response log contains the actual final status and byte count.
  - do not assert random request IDs or exact durations.
- [x] Add one actual `Router.Handle` request through `httpServer.ServeHTTP` so the full stack is covered: server -> ServeMux -> Router middleware -> parser -> response writer.

**Verify before continuing**

```bash
go test ./... -run '^TestHTTPServer|^TestRouter'
go test ./... -race -run '^TestHTTPServer|^TestRouter'
```

## TODO 7 — Consolidate and strengthen `Context` / `Response` tests

Make `response_test.go` the owner of direct `Context`/`Response` unit tests. Remove duplicate cases from `parser_response_test.go` after equivalent coverage exists.

- [ ] Add `NewResponse` boundary tests:
  - 199 panics;
  - 200 succeeds;
  - 599 succeeds;
  - 600 panics.
- [ ] Add a regression test that a new response clears the previous headers, body, and marshaller state.
- [ ] Test `Response.Body()` directly.
- [ ] Test `Context`'s `Deadline`, `Done`, `Err`, and `Value` delegation using a real request context with a value and cancellation/deadline.
- [ ] Strengthen JSON marshal-failure regression coverage:
  - status is 500;
  - old headers are cleared;
  - body is empty.
- [ ] Strengthen unsupported-body regression coverage with the same header/body assertions.
- [ ] Add a small failing `ResponseWriter` to verify propagation of body-write errors for raw bytes/string/JSON success paths.
- [ ] Keep the existing stream-body error test.
- [ ] Keep only one or two router-level response smoke tests; do not test every body builder both directly and through `RequestParser`.
- [ ] Delete `TestStatusText`; it tests only `net/http.StatusText`.

**Verify before continuing**

```bash
go test ./... -run 'TestResponse|TestContext'
```

## TODO 8 — Replace slow/flaky body-timeout tests with deterministic synchronization

Refactor for testability without changing the public API.

- [ ] Extract the timeout-dependent portion of `bindFullTextBody` into an internal helper that accepts a timeout duration; production `bindFullTextBody` passes `maxReadBodyDuration`.
  - Prefer an explicit duration parameter over a mutable package-global timeout variable.
- [ ] Implement a test-only blocking `io.ReadCloser` controlled by channels:
  - signal when `Read` has started;
  - block until `Close` is called;
  - record that `Close` happened;
  - unblock the read immediately on close.
- [ ] Rewrite timeout coverage using a very short explicit test timeout; do not sleep for the production five seconds.
- [ ] Rewrite context-cancellation coverage to wait for the reader-start signal, then call `cancel()` immediately; remove arbitrary 50 ms sleeps.
- [ ] Assert both timeout and cancellation paths close the body and the binder goroutine exits.
- [ ] Keep binder-panic propagation coverage, but synchronize it without sleeps.
- [ ] Remove `TestGoroutine_BodyClose_CancelsInFlightRead`; it only tests `io.Pipe`.
- [ ] Remove the redundant manual body close after context cancellation—the production path already closes the body.
- [ ] Remove `slowReader` and all production-duration waits/skips from the suite.

**Verify before continuing**

```bash
go test ./... -run 'Test.*BindFullTextBody|TestError_BodyTimeout|TestGoroutine' -count=50
go test ./... -short
```

The focused tests should complete quickly and should not rely on wall-clock sleeps for ordering.

## TODO 9 — Rewrite concurrency tests to prove isolation and race safety

Replace the current `parser_concurrency_test.go` cases.

- [ ] Create one shared real `Router`/`RequestParser` before starting goroutines.
- [ ] Register a handler that returns the parsed request ID/name in its response.
- [ ] Start many requests together using a start barrier channel.
- [ ] Give every request a unique value and assert that each response contains exactly its own value; status-only assertions are insufficient.
- [ ] Include middleware in at least one shared-router concurrency test so the dispatcher is race-tested too.
- [ ] If retaining a tag-cache registration stress test, make it a separate test with a fresh request type and concurrently construct `RequestParser` values for that same type.
- [ ] Do not call helpers that may use `t.Fatal`/`FailNow` from worker goroutines. Return worker errors/results over channels and assert from the main test goroutine.
- [ ] Rename all remaining `ChiRouter` / `ChiRouteParam` tests and comments to ServeMux/Router terminology.

**Verify before continuing**

```bash
go test ./... -race -run 'TestConcurrency|TestRouter|TestMiddleware' -count=20
```

## TODO 10 — Remove tests that do not protect this package and reorganize files by ownership

- [ ] Delete `TestUTF16EncodeDecode`; the real charset parser tests already exercise the encoding/decoding integration.
- [ ] Delete pure ServeMux behavior tests that only prove standard-library semantics (`exact method/path`, raw 404, raw 405, raw HEAD-on-GET) unless they are rewritten to pass through package code.
- [ ] Keep the ServeMux wildcard/path-binding tests because they validate `getPathValues` and the intentional unsafe integration.
- [ ] Keep `TestHTTPRequestUnsafeLayout` as a deliberate Go-version canary for the unsafe design.
- [ ] Move `funcObject` / `funcObjects` tests from `server_test.go` to `helper_test.go`.
- [ ] Keep `server_test.go` focused on `httpServer` lifecycle/request behavior.
- [ ] Merge/remove `parser_response_test.go` once TODO 7 has established canonical response coverage.
- [ ] Update stale test file comments that still refer to Chi or to behavior no longer present.

**Verify before continuing**

```bash
go test ./...
```

After cleanup, every remaining test should be able to answer: “Which behavior owned by `go-httpserver` would regress if this test failed?”

## TODO 11 — Make the parser suite easier to scan without reducing behavioral coverage

Do this only after the missing integration coverage is in place.

- [ ] Preserve the existing binding matrix, but convert obvious repetitive variants to table-driven subtests where that makes intent clearer.
  - Good candidates: POST/PUT/PATCH/DELETE body-method matrices and repeated primitive invalid-value cases.
  - Do not combine unrelated binding contracts merely to reduce line count.
- [ ] Organize parser tests consistently by phase:
  - registration/tag-layout validation;
  - successful binding;
  - request-time errors;
  - validation;
  - integration edge cases.
- [ ] Use `require` for prerequisites that make subsequent assertions invalid; use `assert` for independent result checks.
- [ ] Prefer helpers that return concrete captured values/results instead of hidden global/shared state.
- [ ] Ensure helper names explicitly say whether they use the real Router or the parser-only adapter.
- [ ] Remove duplicated comments that restate the test name without adding contract information.

**Verify before continuing**

```bash
go test ./... -shuffle=on -count=20
```

No test should depend on declaration order or another test populating `globalTags` first.

## TODO 12 — Correct and guard benchmarks

- [ ] Fix `BenchmarkComplex_Request` so the JSON body matches the request shape, e.g. `{"address":{"street":...,"city":...}}`.
- [ ] For every benchmark, execute one untimed preflight request and assert the expected status/result before `b.ResetTimer()`.
- [ ] Rename benchmarks so it is clear whether they measure parser-only test harness overhead or real Router end-to-end handling.
- [ ] Keep allocation reporting with `b.ReportAllocs()`.
- [ ] Add a small Router+middleware benchmark only after the corresponding correctness tests exist.
- [ ] Do not use benchmarks as correctness tests; the preflight assertion only prevents accidentally benchmarking an error path.

**Verify before continuing**

```bash
go test ./... -run '^$' -bench . -benchmem
```

## TODO 13 — Add final quality gates and small fuzz coverage

- [ ] Run the normal suite as the primary gate:

```bash
go test ./...
```

- [ ] Run the race detector as a required gate for this package because it contains shared caches, middleware chains, and goroutine-based body parsing:

```bash
go test -race ./...
```

- [ ] Run shuffled/repeated tests after the timing cleanup:

```bash
go test ./... -shuffle=on -count=20
```

- [ ] Run static checks:

```bash
go vet ./...
```

- [ ] Add small fuzz targets for parser inputs with bounded body sizes:
  - JSON body: arbitrary bytes must return a controlled status or valid parse result, never panic/hang because of malformed input.
  - form body: arbitrary URL-encoded-ish bytes must not panic/hang.
  - Content-Type/charset parameter parsing: arbitrary header values must fail cleanly.
- [ ] Keep fuzz targets focused on package-owned parsing behavior; do not fuzz the standard library in isolation.
- [ ] Generate a coverage profile for review, but do not introduce an arbitrary global percentage gate. Instead confirm that `export.go`, `tracker.go`, and the request path in `server.go` now have meaningful coverage.

## TODO 14 — Synchronize nearby documentation with the tested contracts

Do this last so docs describe the behavior the new tests have locked in.

- [ ] `parser.go`: `RequestParser` returns `Handler`, not `http.HandlerFunc`.
- [ ] `parser.go`: remove/update the claim that `MiddlewareParser` writes parse failures “immediately”; it updates shared response state and stops the chain, while `Router` writes afterward.
- [ ] `response.go`: `Context` is created by `Router.Handle`, not by the parser wrappers.
- [ ] `response.go`: update the `NewResponse` GoDoc from “100-599” to “200-599”.
- [ ] `README.md`: include DELETE in body-parsed HTTP methods.
- [ ] `README.md`: document the actual whitespace-separated `body` tag content types instead of semicolon-separated examples.
- [ ] `README.md`: describe URL wildcard extraction as the intentional unsafe ServeMux-internal implementation, not `Request.PathValue` iteration.
- [ ] `tracker.go` and `README.md`: document the final tracker semantics established in TODO 5.
- [ ] Remove remaining Chi-era terminology from tests/comments.

**Final verification**

```bash
go test ./...
go test -race ./...
go test ./... -shuffle=on -count=20
go vet ./...
```

## Definition of done

- [ ] JSON accepts valid trailing whitespace and rejects any second value/garbage.
- [ ] `Router.Handle`, `Router.Group`, middleware ordering/short-circuiting, and `MiddlewareParser` have direct integration tests.
- [ ] Tracker tests match the actual HTTP final-status contract, including informational responses.
- [ ] `httpServer.ServeHTTP` panic recovery and logging/tracker integration are tested.
- [ ] No test waits five seconds or uses arbitrary sleeps as its synchronization mechanism.
- [ ] Concurrency tests verify per-request data isolation on one shared router and pass under `-race`.
- [ ] Response behavior has one canonical unit-test location with boundary/reset/error coverage.
- [ ] Pure dependency/stdlib tests and stale Chi-era tests/comments are gone.
- [ ] Benchmarks preflight their success path.
- [ ] The full suite passes normal, race, shuffled/repeated, and vet gates on Go 1.25.x.
