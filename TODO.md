# TODO — Test `NewServer` With a Minimal Fake Lifecycle

## 10. Add a narrow lifecycle-registration seam

**Goal:** allow `NewServer` itself to execute in tests without starting the global `ctrl` state machine.

Do not emulate the whole controller. Only intercept the `Starter` that `NewServer` registers.

- [ ] In `server.go`, add one package-private indirection:

```go
var registerServer = ctrl.Register
```

- [ ] Change `NewServer` from:

```go
ctrl.Register(func(ctx, _ context.Context) (ctrl.Runner, ctrl.Cleaner) {
    ...
})
```

to:

```go
registerServer(func(ctx, _ context.Context) (ctrl.Runner, ctrl.Cleaner) {
    ...
})
```

- [ ] Make no other production change.
- [ ] Keep `registerServer` package-private.
- [ ] Do not introduce an interface or fake controller implementation.
- [ ] Do not change the exported `NewServer` API.

This preserves normal production behavior exactly:

```text
NewServer
    -> registerServer
    -> ctrl.Register
```

The test can temporarily replace only `registerServer`.

**Verify:**

```sh
go test .
```

All existing tests must remain unchanged and pass.

---

## 11. Add one `NewServer` lifecycle integration test

**File:** `server_test.go` or a dedicated `server_integration_test.go`

**Test name:**

```text
TestNewServer_Lifecycle
```

This test should emulate only the lifecycle behavior relevant to `NewServer`:

1. capture the registered starter;
2. finish configuring the router;
3. invoke the starter;
4. start the returned runner;
5. wait until the HTTP server is reachable;
6. make one real HTTP request;
7. invoke the cleaner;
8. verify the runner exits.

### 11.1 Reserve a test port

- [ ] Open a temporary listener:

```go
listener, err := net.Listen("tcp4", "127.0.0.1:0")
```

- [ ] Read the selected port from `listener.Addr()`.
- [ ] Close the temporary listener.
- [ ] Convert the port to `uint16`.

Keep this helper local or add something small such as:

```go
func freeTCPPort(t *testing.T) uint16
```

Do not hard-code a port.

---

### 11.2 Replace `registerServer`

- [ ] Save the original function:

```go
originalRegister := registerServer
```

- [ ] Restore it with `t.Cleanup`:

```go
t.Cleanup(func() {
    registerServer = originalRegister
})
```

- [ ] Capture the starter instead of running it immediately:

```go
var starter ctrl.Starter

registerServer = func(value ctrl.Starter) {
    starter = value
}
```

Do **not** start the server inside this callback.

That distinction is important: the real controller registers services during initialization and starts runners afterward. The fake should preserve the same ordering.

- [ ] Do not call `t.Parallel()` in this test because it temporarily replaces package-global state.

---

### 11.3 Call the real `NewServer`

- [ ] Create a logger backed by a `bytes.Buffer`.

- [ ] Create a complete test configuration:

```go
config := &ServerConfig{
    Port:              port,
    ReadHeaderTimeout: 5,
    IdleTimeout:       60,
    MaxHeaderBytes:    4096,
    ShutdownOnError:   true,
}
```

- [ ] Call:

```go
router := NewServer(&logger, config)
```

- [ ] Assert:

```go
require.NotNil(t, starter)
```

This proves that `NewServer` registered exactly one lifecycle starter through the fake registration boundary.

---

### 11.4 Configure a route before starting the server

- [ ] Register a simple route through the returned `Router`:

```go
router.Handle("GET /", func(ctx *Context) {
    ctx.NewResponse(http.StatusOK).StringBody("ok")
})
```

Use a normal successful response.

Do not deliberately panic here. Panic recovery already has focused tests; this integration test should verify `NewServer` wiring and lifecycle behavior.

---

### 11.5 Manually execute the captured starter

- [ ] Create a lifecycle context containing the test logger:

```go
lifecycleCtx, lifecycleCancel := context.WithCancel(
    logger.WithContext(context.Background()),
)
defer lifecycleCancel()
```

- [ ] Call the captured starter:

```go
runner, cleaner := starter(lifecycleCtx, lifecycleCtx)
```

- [ ] Assert both are non-nil:

```go
require.NotNil(t, runner)
require.NotNil(t, cleaner)
```

At this point the fake lifecycle has performed the equivalent of the controller's startup phase.

---

### 11.6 Start the runner

- [ ] Start the runner in a goroutine:

```go
runnerDone := make(chan struct{})

go func() {
    defer close(runnerDone)
    runner(lifecycleCtx, lifecycleCancel)
}()
```

The runner should now be blocked in the real:

```go
http.Server.ListenAndServe()
```

---

### 11.7 Wait until the server actually starts

Do not use:

```go
time.Sleep(...)
```

Startup time is inherently asynchronous; poll for the condition that matters.

- [ ] Construct:

```go
url := fmt.Sprintf("http://127.0.0.1:%d/", port)
```

- [ ] Use an HTTP client with a short per-request timeout.

- [ ] Retry until either:
  - the server responds; or
  - an overall test deadline expires.

For example, use `require.Eventually` with a roughly one-second overall limit and a short polling interval.

The polling callback should:

1. issue `GET /`;
2. return `false` on connection errors;
3. close the body when a response is received;
4. record the successful response;
5. return `true`.

Do not treat initial `connection refused` errors as test failures; they simply mean `ListenAndServe` has not bound the socket yet.

---

### 11.8 Verify the real request

- [ ] Assert the successful response has:

```text
status = 200
body   = "ok"
```

This verifies the complete path:

```text
NewServer
  -> ServeMux
  -> httpServer
  -> ListenAndServe
  -> Router.Handle
  -> handler
  -> response
```

It also proves that the port configured through `ServerConfig` was actually used.

---

### 11.9 Emulate controller shutdown

After the request succeeds, trigger cleanup directly.

- [ ] Create a bounded cleanup context:

```go
cleanupCtx, cleanupCancel := context.WithTimeout(
    logger.WithContext(context.Background()),
    time.Second,
)
defer cleanupCancel()
```

- [ ] Call:

```go
cleaner(cleanupCtx)
```

This invokes the real:

```go
http.Server.Shutdown(...)
```

and should cause `ListenAndServe` to return `http.ErrServerClosed`.

- [ ] Cancel the lifecycle context afterward:

```go
lifecycleCancel()
```

This roughly reproduces the relevant controller shutdown sequence without involving controller globals, signals, or controller configuration.

---

### 11.10 Verify the runner actually stopped

- [ ] Wait for `runnerDone` with a bounded timeout:

```go
select {
case <-runnerDone:
    // success
case <-time.After(time.Second):
    t.Fatal("HTTP server runner did not stop")
}
```

Never allow the test to leave a running HTTP server goroutine behind.

- [ ] Add defensive cleanup so that an earlier assertion failure still attempts to stop the server.

Prefer registering cleanup immediately after obtaining `cleaner`:

```go
t.Cleanup(func() {
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    cleaner(ctx)
})
```

Ensure calling the cleaner twice is harmless before using this pattern; otherwise guard it with `sync.Once`.

---

### 11.11 Verify lifecycle logging

- [ ] Inspect the captured zerolog output.

- [ ] Assert it contains the important lifecycle events:

```text
Start serving
Shutting down...
Shutdown complete
```

- [ ] Assert it does **not** contain:

```text
Server closed with error
```

A normal `Shutdown` causes `ListenAndServe` to return `http.ErrServerClosed`, which `runner` is expected to ignore.

Do not assert the entire serialized log output or exact ordering of unrelated request-log fields.

---

### 11.12 Make the test repeatable

- [ ] Run:

```sh
go test -run TestNewServer_Lifecycle -count=10 .
```

- [ ] Run with race detection:

```sh
go test -race -run TestNewServer_Lifecycle .
```

- [ ] Then run the entire package repeatedly:

```sh
go test -count=10 .
```

The test must:
- restore `registerServer`;
- close all HTTP response bodies;
- stop the real server;
- leave no goroutines running;
- leave no listener open;
- not depend on `ctrl` global state.

---

## 12. Final coverage verification

The integration test now executes `NewServer` directly inside the ordinary test binary, so no subprocess coverage collection or `GOCOVERDIR` handling is required.

- [ ] Generate the normal profile:

```sh
go test \
    -cover \
    -covermode=atomic \
    -coverprofile=cover.txt \
    .
```

- [ ] Inspect function coverage:

```sh
go tool cover -func=cover.txt
```

- [ ] Inspect any remaining zero-count blocks:

```sh
awk '$3 == 0 { print }' cover.txt
```

- [ ] Confirm:
  - `NewServer` is covered;
  - its server-construction callback is covered;
  - `runner` is covered;
  - `cleaner` is covered;
  - tasks 1–9 remain covered;
  - the removed `helper.go` branch is absent.

- [ ] Run the final reliability checks:

```sh
go test .
go test -race .
go test -count=10 .
```

- [ ] If `cover.txt` still contains uncovered statements, inspect them individually.

Do not add tests solely to force 100% coverage unless the remaining branch represents behavior worth specifying.

## Expected result

There should now be no need for:

- `ctrl.Control`;
- `ctrl.ShutdownOnSignal`;
- a child process;
- an integration-only executable;
- build tags;
- signal handling;
- `GOCOVERDIR`;
- coverage-profile merging.

The test owns only the tiny part of the lifecycle needed by `NewServer`:

```text
capture starter
      ↓
NewServer returns
      ↓
configure router
      ↓
invoke starter
      ↓
run server
      ↓
make request
      ↓
invoke cleaner
      ↓
runner exits
```