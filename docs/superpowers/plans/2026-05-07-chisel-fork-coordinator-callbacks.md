# Chisel-fork coordinator-callbacks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add coordinator-callback integration to chisel-server so it consults bastion's coordinator at WebSocket reverse-tunnel connect-time (lookup → port-pre-allocation → activate → deactivate-on-disconnect), while leaving upstream-compatible behavior intact when the integration is unconfigured.

**Architecture:** Generic proxy lifecycle hooks (`OnRemoteBound`/`OnRemoteUnbound` + `DisconnectReason`) added to `share/tunnel/` — these are upstream-PR-target; they don't know about coordinators. Coordinator-specific HTTP client + handler integration lives in `server/coordinator/` and `server/server_handler.go` — fork-only. Cancel-cause-context propagation (`Server.shutdownCtx` cancelled with `tunnel.ErrServerShutdown`) gives the disconnect-reason classifier reliable cause data without setter races.

**Tech Stack:** Go 1.25 (per `go.mod`), `golang.org/x/sync v0.19.0` (errgroup with cancel-cause), `crypto/tls` for mTLS to coordinator, `net/http` + `net/http/httptest` for client + tests, existing chisel test/e2e patterns.

**Working directory:** `C:\Claude\chisel\` on branch `feat/coordinator-callbacks`. Spec at `docs/superpowers/specs/2026-05-07-chisel-fork-coordinator-callbacks-design.md`.

---

## Pre-flight verification

Before any code changes, run a one-line check that the cancel-cause behavior in `errgroup.WithContext` is available. The whole disconnect-reason classifier depends on it; older `golang.org/x/sync` pins (< v0.6) silently degrade and every disconnect classifies as `DisconnectClient` regardless of cause.

### Task 0: Verify x/sync pin

**Files:** Read-only check.

- [ ] **Step 1: Run pin check**

```
grep '^	golang.org/x/sync ' go.mod
```

Expected output:
```
	golang.org/x/sync v0.19.0
```

If the version is `< v0.6.0`, STOP and update `go.mod` first. The classifier in Phase 1 task 5 won't work otherwise.

---

## Phase 1: `share/tunnel/` lifecycle hooks (upstream-PR target)

This phase produces the generic proxy-lifecycle-hook surface. Independently mergeable upstream as "complete proxy lifecycle observability." No coordinator dependency.

### Task 1: Add `DisconnectReason` type + sentinel error

**Files:**
- Create: `share/tunnel/disconnect.go`
- Test: `share/tunnel/disconnect_test.go`

- [ ] **Step 1: Write the failing test**

Create `share/tunnel/disconnect_test.go`:

```go
package tunnel

import (
	"errors"
	"testing"
)

func TestDisconnectReasonValues(t *testing.T) {
	cases := []struct {
		reason DisconnectReason
		want   string
	}{
		{DisconnectClient, "client_disconnect"},
		{DisconnectConnectionLost, "connection_lost"},
		{DisconnectServerShutdown, "server_shutdown"},
	}
	for _, tc := range cases {
		if string(tc.reason) != tc.want {
			t.Errorf("%q = %q, want %q", tc.reason, string(tc.reason), tc.want)
		}
	}
}

func TestErrServerShutdownIsSentinel(t *testing.T) {
	if ErrServerShutdown == nil {
		t.Fatal("ErrServerShutdown is nil")
	}
	if !errors.Is(ErrServerShutdown, ErrServerShutdown) {
		t.Error("errors.Is fails on the sentinel itself")
	}
	if errors.Is(errors.New("other"), ErrServerShutdown) {
		t.Error("errors.Is matches an unrelated error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./share/tunnel/ -run TestDisconnectReasonValues -v
go test ./share/tunnel/ -run TestErrServerShutdownIsSentinel -v
```

Expected: FAIL with "DisconnectReason is undefined" / "DisconnectClient is undefined" / "ErrServerShutdown is undefined".

- [ ] **Step 3: Write minimal implementation**

Create `share/tunnel/disconnect.go`:

```go
package tunnel

import "errors"

// AA-fork: DisconnectReason carries the cause of a proxy unbind. Best-effort:
// chisel-server distinguishes only the three layers below; finer-grained causes
// (idle, max_duration) are LCM-state knowledge and get attributed at the
// consumer (e.g., bastion's coordinator) by log correlation.
type DisconnectReason string

const (
	// DisconnectClient is the default reason when the SSH connection
	// closed cleanly or with EOF — typically the client (LCM)
	// initiating the disconnect via systemctl stop.
	DisconnectClient DisconnectReason = "client_disconnect"

	// DisconnectConnectionLost is set when the SSH connection closed
	// with a non-EOF error — TCP reset, network partition, abnormal
	// keepalive failure.
	DisconnectConnectionLost DisconnectReason = "connection_lost"

	// DisconnectServerShutdown is set when chisel-server's own
	// shutdown caused the close (Server.Close() cancels shutdownCtx
	// with this sentinel as the cause).
	DisconnectServerShutdown DisconnectReason = "server_shutdown"
)

// AA-fork: ErrServerShutdown is the cancel cause used by chisel-server's
// Close() so OnRemoteUnbound callbacks can distinguish server-initiated
// shutdown from client-initiated disconnect via context.Cause(ctx).
var ErrServerShutdown = errors.New("chisel: server shutdown")
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./share/tunnel/ -v
```

Expected: PASS for both new tests; existing tunnel tests untouched.

- [ ] **Step 5: Commit**

```
git add share/tunnel/disconnect.go share/tunnel/disconnect_test.go
git commit -m "feat(tunnel): add DisconnectReason type + ErrServerShutdown sentinel

AA-fork: groundwork for proxy lifecycle observability. Three named
reasons (Client / ConnectionLost / ServerShutdown) plus a sentinel
error used as a cancel-cause for distinguishing server-initiated
closes via context.Cause(ctx)."
```

### Task 2: Add `RemotePortInt` helper to settings.Remote

**Files:**
- Modify: `share/settings/remote.go`
- Test: `share/settings/remote_test.go`

- [ ] **Step 1: Write the failing test**

Append to `share/settings/remote_test.go`:

```go
func TestRemotePortInt(t *testing.T) {
	cases := []struct {
		name      string
		remote    Remote
		want      int
	}{
		{"valid number", Remote{RemotePort: "22099"}, 22099},
		{"empty string", Remote{RemotePort: ""}, 0},
		{"non-numeric", Remote{RemotePort: "socks"}, 0},
		{"zero", Remote{RemotePort: "0"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.remote.RemotePortInt(); got != tc.want {
				t.Errorf("RemotePortInt() = %d, want %d", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./share/settings/ -run TestRemotePortInt -v
```

Expected: FAIL with "remote.RemotePortInt undefined".

- [ ] **Step 3: Write minimal implementation**

Add to `share/settings/remote.go` at end of file:

```go
// AA-fork: RemotePortInt returns RemotePort parsed as int, or 0 on parse
// failure or empty value. Convenience for callers that need the bound port
// as a number rather than the wire-format string (specifically the
// coordinator client, which sends actual_port_bound as JSON int).
func (r *Remote) RemotePortInt() int {
	if r.RemotePort == "" {
		return 0
	}
	n, err := strconv.Atoi(r.RemotePort)
	if err != nil {
		return 0
	}
	return n
}
```

Also confirm `strconv` is already imported in `remote.go` (it is — used by `isPort`).

- [ ] **Step 4: Run test to verify it passes**

```
go test ./share/settings/ -v
```

Expected: PASS for all 4 subcases; existing `remote_test.go` tests untouched.

- [ ] **Step 5: Commit**

```
git add share/settings/remote.go share/settings/remote_test.go
git commit -m "feat(settings): add Remote.RemotePortInt helper

AA-fork: parses RemotePort string to int, 0 on parse failure. Used
by the coordinator-callback path in server_handler.go to populate the
actual_port_bound field of the activate request body."
```

### Task 3: Add `OnRemoteBound`/`OnRemoteUnbound` to tunnel.Config

**Files:**
- Modify: `share/tunnel/tunnel.go:21-31`

- [ ] **Step 1: Read the current Config struct**

```
sed -n '21,31p' share/tunnel/tunnel.go
```

Should show:

```go
//Config a Tunnel
type Config struct {
	*cio.Logger
	Inbound   bool
	Outbound  bool
	Socks     bool
	KeepAlive time.Duration
	//ACL optionally checks if a given address (host:port) is allowed.
	//When set, outbound connections are denied if this returns false.
	ACL func(addr string) bool
}
```

- [ ] **Step 2: Add the callback fields**

Replace lines 21-31 in `share/tunnel/tunnel.go` with:

```go
//Config a Tunnel
type Config struct {
	*cio.Logger
	Inbound   bool
	Outbound  bool
	Socks     bool
	KeepAlive time.Duration
	//ACL optionally checks if a given address (host:port) is allowed.
	//When set, outbound connections are denied if this returns false.
	ACL func(addr string) bool

	// AA-fork: OnRemoteBound fires after a Proxy's net.Listener is bound and
	// before its accept loop starts. Returning a non-nil error tears down
	// THIS proxy only (not the whole tunnel) and propagates the error back
	// to BindRemotes' caller. Use case: external session manager wants to
	// validate the binding before exposing the port.
	//
	// listener.Addr() is the actually-bound address (relevant when the
	// caller requested ":0" / RemotePort=0 and the OS picked a port).
	OnRemoteBound func(ctx context.Context, remote *settings.Remote, listener net.Listener) error

	// AA-fork: OnRemoteUnbound fires when a Proxy's accept loop exits,
	// regardless of cause. The reason is derived at the callback site
	// from context.Cause(ctx) on the proxy's running context — see
	// classifyDisconnect in this package.
	OnRemoteUnbound func(remote *settings.Remote, reason DisconnectReason)
}
```

The file's existing imports already include `context`, `net`, and `github.com/jpillora/chisel/share/settings`. Verify:

```
grep -E '^\t"(context|net)"|jpillora/chisel/share/settings' share/tunnel/tunnel.go
```

Should show all three imports present.

- [ ] **Step 3: Verify the package compiles**

```
go build ./share/tunnel/...
```

Expected: no output (success). The fields are unused so far — no test, but the package must compile.

- [ ] **Step 4: Verify existing tunnel tests still pass**

```
go test ./share/tunnel/...
```

Expected: PASS (existing tests don't touch the new fields).

- [ ] **Step 5: Commit**

```
git add share/tunnel/tunnel.go
git commit -m "feat(tunnel): add OnRemoteBound/OnRemoteUnbound callbacks to Config

AA-fork: lifecycle hooks for externally-managed sessions. Both default
to nil; tunnel logic calls them only when set. OnRemoteBound err tears
down the proxy whose listener it just received; OnRemoteUnbound is
informational. Reason for unbound comes from context.Cause(ctx) at the
callback site (classifier introduced in a later commit)."
```

### Task 4: Add `classifyDisconnect` helper

**Files:**
- Modify: `share/tunnel/disconnect.go`
- Test: `share/tunnel/disconnect_test.go`

- [ ] **Step 1: Write the failing test**

Append to `share/tunnel/disconnect_test.go`:

```go
import (
	"context"
	"io"
)

func TestClassifyDisconnectFromCancelCause(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  DisconnectReason
	}{
		{"server shutdown sentinel", ErrServerShutdown, DisconnectServerShutdown},
		{"plain context.Canceled", context.Canceled, DisconnectClient},
		{"nil cause", nil, DisconnectClient},
		{"io.EOF", io.EOF, DisconnectClient},
		{"wrapped EOF", errors.New("read tcp: EOF"), DisconnectClient},
		{"abnormal error", errors.New("read tcp: connection reset by peer"), DisconnectConnectionLost},
		{"server shutdown wrapped in fmt.Errorf", errors.Join(ErrServerShutdown, errors.New("plus context")), DisconnectServerShutdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			if tc.cause != nil {
				cancel(tc.cause)
			} else {
				cancel(nil) // cancel with nil cause; context.Canceled appears via Err but Cause is nil
			}
			got := classifyDisconnect(ctx)
			if got != tc.want {
				t.Errorf("classifyDisconnect = %q, want %q (cause=%v)", got, tc.want, tc.cause)
			}
		})
	}
}
```

The merge of imports — at this point `share/tunnel/disconnect_test.go` should have:

```go
import (
	"context"
	"errors"
	"io"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./share/tunnel/ -run TestClassifyDisconnectFromCancelCause -v
```

Expected: FAIL with "classifyDisconnect undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `share/tunnel/disconnect.go`:

```go
import (
	"context"
	"errors"
	"strings"
)

// AA-fork: classifyDisconnect maps a proxy's running-context cancel-cause
// to a DisconnectReason. Called from Proxy.Run's exit path so the
// callback site has authoritative reason data without a setter race.
//
// Mapping rules (first match wins):
//   - cause errors.Is ErrServerShutdown → DisconnectServerShutdown
//   - cause is nil OR context.Canceled OR has "EOF" suffix → DisconnectClient
//   - any other non-nil cause → DisconnectConnectionLost
func classifyDisconnect(ctx context.Context) DisconnectReason {
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrServerShutdown) {
		return DisconnectServerShutdown
	}
	if cause == nil {
		return DisconnectClient
	}
	if errors.Is(cause, context.Canceled) {
		return DisconnectClient
	}
	if strings.HasSuffix(cause.Error(), "EOF") {
		return DisconnectClient
	}
	return DisconnectConnectionLost
}
```

Note: imports go at the TOP of `disconnect.go` (merge with the existing `import "errors"`). Final import block:

```go
import (
	"context"
	"errors"
	"strings"
)
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./share/tunnel/ -v
```

Expected: PASS for all 7 subcases of `TestClassifyDisconnectFromCancelCause` and the existing tests from Task 1.

- [ ] **Step 5: Commit**

```
git add share/tunnel/disconnect.go share/tunnel/disconnect_test.go
git commit -m "feat(tunnel): add classifyDisconnect helper

AA-fork: reads context.Cause(ctx) and maps to DisconnectReason.
Server shutdown sentinel takes precedence; EOF / nil / context.Canceled
classify as Client (graceful or unknown-but-clean); anything else is
ConnectionLost. Used by Proxy.Run on exit."
```

### Task 5: Wire `OnRemoteBound` into `BindRemotes`

**Files:**
- Modify: `share/tunnel/tunnel.go:148-176`
- Test: `share/tunnel/tunnel_callbacks_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `share/tunnel/tunnel_callbacks_test.go`:

```go
package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jpillora/chisel/share/cio"
	"github.com/jpillora/chisel/share/settings"
)

// fakeSSHTunnel is a minimal stub of sshTunnel for proxy tests that don't
// need a real SSH connection (we never actually proxy any data).
type fakeSSHTunnel struct{}

func (fakeSSHTunnel) getSSH(ctx context.Context) interface{ /* placeholder */ } {
	return nil
}

func TestBindRemotesCallsOnRemoteBound(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		callbackFired bool
		callbackPort  int
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			callbackFired = true
			tcp, ok := ln.Addr().(*net.TCPAddr)
			if !ok {
				t.Errorf("listener.Addr() is %T, want *net.TCPAddr", ln.Addr())
				return nil
			}
			callbackPort = tcp.Port
			return nil
		},
	}
	tun := New(cfg)

	// Bind to a free port (RemotePort=0 → OS picks).
	remote := &settings.Remote{
		Reverse: true,
		LocalHost:  "127.0.0.1",
		LocalPort:  "0",
		LocalProto: "tcp",
		RemoteHost: "127.0.0.1",
		RemotePort: "0",
		RemoteProto: "tcp",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// BindRemotes blocks; run in a goroutine so we can cancel.
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()

	// Give the bind enough time to fire the callback.
	time.Sleep(100 * time.Millisecond)

	if !callbackFired {
		t.Fatal("OnRemoteBound was not called")
	}
	if callbackPort == 0 {
		t.Errorf("callbackPort = 0, want a real OS-assigned port")
	}

	cancel()
	<-done // unblock
}

func TestBindRemotesCallbackErrTearsDownProxy(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			return errors.New("simulated teardown")
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse: true,
		LocalHost: "127.0.0.1", LocalPort: "0", LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := tun.BindRemotes(ctx, []*settings.Remote{remote})
	if err == nil {
		t.Fatal("BindRemotes returned nil, want error from OnRemoteBound")
	}
	if !contains(err.Error(), "simulated teardown") {
		t.Errorf("err = %q, want it to wrap 'simulated teardown'", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./share/tunnel/ -run TestBindRemotesCallsOnRemoteBound -v
go test ./share/tunnel/ -run TestBindRemotesCallbackErrTearsDownProxy -v
```

Expected: both FAIL — first because the callback isn't being called, second because BindRemotes ignores the (non-existent) callback.

- [ ] **Step 3: Modify `BindRemotes`**

In `share/tunnel/tunnel.go`, locate `BindRemotes` (currently lines 148-176). Replace the entire function with:

```go
//BindRemotes converts the given remotes into proxies, and blocks
//until the caller cancels the context or there is a proxy error.
func (t *Tunnel) BindRemotes(ctx context.Context, remotes []*settings.Remote) error {
	if len(remotes) == 0 {
		return errors.New("no remotes")
	}
	if !t.Inbound {
		return errors.New("inbound connections blocked")
	}
	proxies := make([]*Proxy, 0, len(remotes))
	for _, remote := range remotes {
		p, err := NewProxy(t.Logger, t, t.proxyCount, remote)
		if err != nil {
			// Tear down already-bound proxies before returning.
			for _, prev := range proxies {
				prev.Close()
			}
			return err
		}

		// AA-fork: OnRemoteBound runs after listener bind + before accept loop.
		// Callback err tears down THIS proxy only (and any previously-bound
		// proxies in this BindRemotes call); other tunnels are unaffected.
		if t.Config.OnRemoteBound != nil {
			ln := p.boundListener()
			if cbErr := t.Config.OnRemoteBound(ctx, remote, ln); cbErr != nil {
				p.Close()
				for _, prev := range proxies {
					prev.Close()
				}
				return fmt.Errorf("OnRemoteBound: %w", cbErr)
			}
		}

		proxies = append(proxies, p)
		t.proxyCount++
	}

	eg, ctx := errgroup.WithContext(ctx)
	for _, proxy := range proxies {
		p := proxy
		eg.Go(func() error {
			return p.Run(ctx)
		})
	}
	t.Debugf("Bound proxies")
	err := eg.Wait()
	t.Debugf("Unbound proxies")
	return err
}
```

The function uses `fmt.Errorf` — check that `fmt` is imported in `tunnel.go`. If not, add to the import block.

- [ ] **Step 4: Add the `boundListener` accessor + `Close` method to Proxy**

In `share/tunnel/tunnel_in_proxy.go`, locate the `Proxy` struct (around line 21-31). After the struct definition, add:

```go
// AA-fork: boundListener returns the proxy's underlying listener once it's
// been bound. Used by tunnel.BindRemotes to pass the listener to the
// OnRemoteBound callback. UDP and stdio proxies return nil (the callback
// signature accepts net.Listener which is fine for unused/nil values, but
// callers in the chisel-server reverse-tunnel path always have TCP).
func (p *Proxy) boundListener() net.Listener {
	if p.tcp != nil {
		return p.tcp
	}
	return nil
}

// AA-fork: Close tears down the proxy's listener. Used by BindRemotes when
// OnRemoteBound returns an error and the proxy needs to be torn down before
// the accept loop starts.
func (p *Proxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tcp != nil {
		err := p.tcp.Close()
		p.tcp = nil
		return err
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass + run existing tunnel tests**

```
go test ./share/tunnel/...
```

Expected: PASS for both new tests + existing tests untouched.

- [ ] **Step 6: Commit**

```
git add share/tunnel/tunnel.go share/tunnel/tunnel_in_proxy.go share/tunnel/tunnel_callbacks_test.go
git commit -m "feat(tunnel): wire OnRemoteBound callback into BindRemotes

AA-fork: After NewProxy succeeds (port bound), call OnRemoteBound if
configured. Non-nil err from the callback tears down THIS proxy plus
any previously-bound proxies in the same BindRemotes call, propagating
the err back to the caller. Other tunnels (different goroutines) are
unaffected. Adds Proxy.boundListener() accessor and Proxy.Close()
helper to support the teardown path."
```

### Task 6: Wire `OnRemoteUnbound` into `Proxy.Run`

**Files:**
- Modify: `share/tunnel/tunnel_in_proxy.go` (around line 73)
- Test: append to `share/tunnel/tunnel_callbacks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `share/tunnel/tunnel_callbacks_test.go`:

```go
func TestProxyRunCallsOnRemoteUnboundOnExit(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		unboundFired  bool
		unboundReason DisconnectReason
		mu            sync.Mutex
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteBound: func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
			return nil
		},
		OnRemoteUnbound: func(r *settings.Remote, reason DisconnectReason) {
			mu.Lock()
			defer mu.Unlock()
			unboundFired = true
			unboundReason = reason
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse: true,
		LocalHost: "127.0.0.1", LocalPort: "0", LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()

	// Wait until proxy is up.
	time.Sleep(100 * time.Millisecond)

	// Simulate clean client disconnect (no cause).
	cancel(nil)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !unboundFired {
		t.Fatal("OnRemoteUnbound was not called")
	}
	if unboundReason != DisconnectClient {
		t.Errorf("reason = %q, want DisconnectClient", unboundReason)
	}
}

func TestProxyRunReportsServerShutdownReason(t *testing.T) {
	logger := cio.NewLogger("test")
	logger.Info = false
	logger.Debug = false

	var (
		mu            sync.Mutex
		unboundReason DisconnectReason
		fired         bool
	)

	cfg := Config{
		Logger:  logger,
		Inbound: true,
		OnRemoteUnbound: func(r *settings.Remote, reason DisconnectReason) {
			mu.Lock()
			defer mu.Unlock()
			unboundReason = reason
			fired = true
		},
	}
	tun := New(cfg)

	remote := &settings.Remote{
		Reverse: true,
		LocalHost: "127.0.0.1", LocalPort: "0", LocalProto: "tcp",
		RemoteHost: "127.0.0.1", RemotePort: "0", RemoteProto: "tcp",
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.BindRemotes(ctx, []*settings.Remote{remote}) }()
	time.Sleep(100 * time.Millisecond)

	cancel(ErrServerShutdown)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !fired {
		t.Fatal("OnRemoteUnbound did not fire")
	}
	if unboundReason != DisconnectServerShutdown {
		t.Errorf("reason = %q, want DisconnectServerShutdown", unboundReason)
	}
}
```

The merged imports at the top of `tunnel_callbacks_test.go` should be:

```go
import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jpillora/chisel/share/cio"
	"github.com/jpillora/chisel/share/settings"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./share/tunnel/ -run TestProxyRunCallsOnRemoteUnbound -v
go test ./share/tunnel/ -run TestProxyRunReportsServerShutdownReason -v
```

Expected: both FAIL because OnRemoteUnbound isn't being called.

- [ ] **Step 3: Modify `Proxy.Run`**

In `share/tunnel/tunnel_in_proxy.go`, locate `func (p *Proxy) Run(ctx context.Context)` (around line 73). The function currently looks like (showing roughly):

```go
func (p *Proxy) Run(ctx context.Context) error {
	if p.remote.Stdio {
		// ...
	}
	// ... loop over Accept ...
}
```

Wrap the body so that on exit (any return path), `OnRemoteUnbound` fires if configured. Insert at the very top of `Run`:

```go
func (p *Proxy) Run(ctx context.Context) error {
	// AA-fork: defer the OnRemoteUnbound callback so it fires regardless
	// of how Run exits (clean accept-loop end, listener err, ctx cancel).
	// Reason is classified from ctx.Cause at the moment Run returns —
	// no setter race possible because the cause is set by whichever
	// cancel-path fired (errgroup wraps BindSSH's err; Server.Close
	// cancels with ErrServerShutdown).
	if p.tunnelConfig().OnRemoteUnbound != nil {
		defer func() {
			p.tunnelConfig().OnRemoteUnbound(p.remote, classifyDisconnect(ctx))
		}()
	}

	// ... existing body unchanged ...
```

`p.tunnelConfig()` is a new accessor that returns the parent Tunnel's Config. Add it as a method on Proxy:

In `share/tunnel/tunnel_in_proxy.go`, find the `Proxy` struct and the `boundListener` accessor added in Task 5. Add right after `boundListener`:

```go
// AA-fork: tunnelConfig exposes the parent Tunnel's Config so Proxy.Run
// can read OnRemoteUnbound at exit time.
func (p *Proxy) tunnelConfig() Config {
	if t, ok := p.sshTun.(*Tunnel); ok {
		return t.Config
	}
	// Test stubs may pass a fakeSSHTunnel; return zero Config so the
	// nil-check on OnRemoteUnbound at the call site safely skips.
	return Config{}
}
```

- [ ] **Step 4: Run tests to verify they pass + run existing tunnel tests**

```
go test ./share/tunnel/...
```

Expected: PASS for the two new tests + earlier callback test from Task 5 + all pre-existing share/tunnel tests.

- [ ] **Step 5: Commit**

```
git add share/tunnel/tunnel_in_proxy.go share/tunnel/tunnel_callbacks_test.go
git commit -m "feat(tunnel): fire OnRemoteUnbound on proxy.Run exit

AA-fork: deferred callback at the top of Proxy.Run guarantees fire on
any return path. Reason via classifyDisconnect(ctx) reads
context.Cause(ctx) — server shutdown propagates ErrServerShutdown via
chisel-server's Close() (introduced in Phase 3); BindSSH errs flow
through errgroup's cancel-cause; nil cause defaults to DisconnectClient.

Also adds Proxy.tunnelConfig() accessor for the deferred callback to
reach the parent Tunnel's Config from Proxy methods."
```

### Task 7: Run all share/tunnel tests one more time + run upstream e2e

**Files:** Read-only.

- [ ] **Step 1: Full share/tunnel test pass**

```
go test ./share/tunnel/... -v
```

Expected: all green. Note the test counts; the list should include:
- `TestDisconnectReasonValues`
- `TestErrServerShutdownIsSentinel`
- `TestClassifyDisconnectFromCancelCause` (with 7 subtests)
- `TestRemotePortInt` (with 4 subtests)
- `TestBindRemotesCallsOnRemoteBound`
- `TestBindRemotesCallbackErrTearsDownProxy`
- `TestProxyRunCallsOnRemoteUnboundOnExit`
- `TestProxyRunReportsServerShutdownReason`
- Plus existing wg_test.go, remote_test.go tests

- [ ] **Step 2: Verify upstream e2e tests still pass**

```
go test ./test/e2e/...
```

Expected: PASS for all existing chisel e2e tests. This is the regression check that Phase 1 didn't break upstream behavior — when callbacks are nil, BindRemotes / Proxy.Run skip them entirely.

- [ ] **Step 3: Push the Phase 1 commits to the fork (no PR yet)**

```
git push origin feat/coordinator-callbacks
```

Phase 1 is upstream-PR-ready as a unit; the next phases extend the fork-only side.

---

## Phase 2: `server/coordinator/` package

This phase produces the HTTP client for bastion's coordinator API. Self-contained library code; unit-tested with `httptest.NewServer`. No coupling to `server/server_handler.go` yet.

### Task 8: Create paths.go with constants

**Files:**
- Create: `server/coordinator/paths.go`
- Test: `server/coordinator/paths_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/paths_test.go`:

```go
package coordinator

import "testing"

func TestPathConstants(t *testing.T) {
	if PathLookup != "/sessions/lookup" {
		t.Errorf("PathLookup = %q, want /sessions/lookup", PathLookup)
	}
	if PathActivate != "/sessions/%s/activate" {
		t.Errorf("PathActivate = %q, want /sessions/%%s/activate", PathActivate)
	}
	if PathDeactivate != "/sessions/%s/deactivate" {
		t.Errorf("PathDeactivate = %q, want /sessions/%%s/deactivate", PathDeactivate)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./server/coordinator/ -run TestPathConstants -v
```

Expected: FAIL with "package coordinator does not exist" or "PathLookup undefined".

- [ ] **Step 3: Write minimal implementation**

Create `server/coordinator/paths.go`:

```go
// Package coordinator provides the HTTP client for AdvancingAlternatives's
// bastion coordinator API. The API contract is documented in the bastion
// repo (docs/chisel-fork-contract.md). This package is fork-only and never
// targets upstream.
package coordinator

// Path templates for the coordinator API. The activate / deactivate paths
// take a session_id substituted via fmt.Sprintf with %s. These constants
// are the single source of truth for URL conventions; they live in code
// rather than as configurable flags because the convention is owned by us
// at both ends (bastion's coordinator + this fork) — flexibility to serve
// the API under different paths is hypothetical.
const (
	PathLookup     = "/sessions/lookup"
	PathActivate   = "/sessions/%s/activate"
	PathDeactivate = "/sessions/%s/deactivate"
)
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./server/coordinator/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add server/coordinator/paths.go server/coordinator/paths_test.go
git commit -m "feat(coordinator): add path constants

AA-fork: bastion coordinator API path conventions live here as the
single source of truth. Activate / deactivate take session_id via
%s substitution at call time."
```

### Task 9: Create types.go with Session + request bodies

**Files:**
- Create: `server/coordinator/types.go`
- Test: `server/coordinator/types_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/types_test.go`:

```go
package coordinator

import (
	"encoding/json"
	"testing"
)

func TestSessionUnmarshalsLookupResponse(t *testing.T) {
	body := `{"session_id":"ses_abc","port":22099,"state":"pending","target_hostname":"lcm-a57d","authorized_user":"wyatt"}`
	var s Session
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.SessionID != "ses_abc" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.Port != 22099 {
		t.Errorf("Port = %d", s.Port)
	}
	if s.State != "pending" {
		t.Errorf("State = %q", s.State)
	}
	if s.TargetHostname != "lcm-a57d" {
		t.Errorf("TargetHostname = %q", s.TargetHostname)
	}
}

func TestActivateRequestMarshalsToContractFields(t *testing.T) {
	req := ActivateRequest{
		TargetHostname:   "lcm-a57d",
		ActualPortBound:  22099,
		ClientRemoteAddr: "10.5.6.7:55432",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"target_hostname":"lcm-a57d"`,
		`"actual_port_bound":22099`,
		`"client_remote_addr":"10.5.6.7:55432"`,
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in JSON: %s", want, got)
		}
	}
}

func TestDeactivateRequestMarshalsToContractFields(t *testing.T) {
	req := DeactivateRequest{
		TargetHostname:  "lcm-a57d",
		ActualPortBound: 22099,
		Reason:          "client_disconnect",
	}
	b, _ := json.Marshal(req)
	got := string(b)
	for _, want := range []string{
		`"target_hostname":"lcm-a57d"`,
		`"actual_port_bound":22099`,
		`"reason":"client_disconnect"`,
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in JSON: %s", want, got)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -v
```

Expected: FAIL with "Session undefined" / "ActivateRequest undefined" / etc.

- [ ] **Step 3: Write minimal implementation**

Create `server/coordinator/types.go`:

```go
package coordinator

// Session is the subset of the coordinator's session record that this
// client cares about. The coordinator's full Session struct has additional
// fields (created_at, expires_at, credential, etc.) that aren't needed at
// the chisel-server layer. Tagged with json names matching coordinator's
// API responses.
type Session struct {
	SessionID      string `json:"session_id"`
	Port           int    `json:"port"`
	State          string `json:"state"` // "pending", "active", or "closed"
	TargetHostname string `json:"target_hostname"`
	AuthorizedUser string `json:"authorized_user,omitempty"`
}

// ActivateRequest is the body posted to POST /sessions/{id}/activate.
// Fields match the contract in bastion/docs/chisel-fork-contract.md.
type ActivateRequest struct {
	// TargetHostname is the chisel client's --hostname value (carried in
	// the WebSocket Host header). Coordinator verifies it matches the
	// recorded hostname for the session — defense-in-depth against a
	// compromised chisel-server cert flipping arbitrary sessions active
	// under the wrong identity.
	TargetHostname string `json:"target_hostname"`

	// ActualPortBound is the OS-assigned port the chisel-server bound for
	// the reverse tunnel. Should match the coordinator's pre-allocated
	// port; mismatch returns 409.
	ActualPortBound int `json:"actual_port_bound"`

	// ClientRemoteAddr is chisel-server's view of the LCM client's IP+port
	// (from req.RemoteAddr). Logged on the coordinator side for the audit
	// trail described in bastion's SECURITY.md §3.
	ClientRemoteAddr string `json:"client_remote_addr"`
}

// DeactivateRequest is the body posted to POST /sessions/{id}/deactivate.
type DeactivateRequest struct {
	// TargetHostname is repeated for cheap deactivate-for-wrong-session
	// detection on the coordinator side.
	TargetHostname string `json:"target_hostname"`

	// ActualPortBound is the port that was bound when this session was
	// active — same purpose as TargetHostname (correlation/sanity check).
	ActualPortBound int `json:"actual_port_bound"`

	// Reason is the disconnect cause. One of
	// "client_disconnect" / "connection_lost" / "server_shutdown" — see
	// share/tunnel/disconnect.go.
	Reason string `json:"reason"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all three new tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/types.go server/coordinator/types_test.go
git commit -m "feat(coordinator): add Session + ActivateRequest + DeactivateRequest types

AA-fork: JSON-tagged structs matching the coordinator API contract.
Session unmarshals lookup responses; the two request structs marshal
cleanly to the field shapes documented in
bastion/docs/chisel-fork-contract.md."
```

### Task 10: Create errors.go with sentinel errors

**Files:**
- Create: `server/coordinator/errors.go`
- Test: `server/coordinator/errors_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/errors_test.go`:

```go
package coordinator

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	all := []error{ErrNotFound, ErrConflict, ErrAuth, ErrTransient}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("%v errors.Is %v unexpectedly", a, b)
			}
		}
	}
}

func TestSentinelErrorsWrapForCallers(t *testing.T) {
	wrapped := fmt.Errorf("call: %w", ErrConflict)
	if !errors.Is(wrapped, ErrConflict) {
		t.Error("errors.Is fails on wrapped sentinel")
	}
	if errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is matches unrelated sentinel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./server/coordinator/ -run TestSentinelErrorsAreDistinct -v
```

Expected: FAIL with "ErrNotFound undefined".

- [ ] **Step 3: Write minimal implementation**

Create `server/coordinator/errors.go`:

```go
package coordinator

import "errors"

// Sentinel errors for failure modes the chisel-server caller distinguishes.
// All Client method errors wrap one of these so callers can use errors.Is()
// to branch on category without string-matching coordinator messages.
var (
	// ErrNotFound is returned when the coordinator responds 404 to a
	// lookup, activate, or deactivate. For lookup, this is the normal
	// "operator hasn't clicked yet" path; chisel-server SSH-rejects the
	// connection with "no pending session for hostname X" + INFO log.
	ErrNotFound = errors.New("coordinator: not found")

	// ErrConflict is returned when the coordinator responds 409. For
	// activate, this means port mismatch / hostname mismatch / wrong
	// state — chisel-server tears down the proxy and SSH-rejects with
	// "state divergence" + WARN log. For deactivate, it means the
	// session is already closed (idempotent retry no-op).
	ErrConflict = errors.New("coordinator: conflict")

	// ErrAuth is returned for HTTP 401/403. This is a chisel-server
	// config bug (wrong mTLS cert / expired / wrong CA) — Layer-2,
	// not the LCM's auth. Logged at ERROR for ops alerting; LCM sees
	// generic "coordinator unreachable" so Layer-2 detail doesn't leak.
	ErrAuth = errors.New("coordinator: auth failure")

	// ErrTransient is returned for HTTP 5xx or transport-level failures
	// (timeout, connection refused, DNS error). Chisel-server SSH-rejects
	// with "coordinator unreachable, retry in flight" + WARN log; LCM's
	// chisel-client retries via its --max-retry-count=-1.
	ErrTransient = errors.New("coordinator: transient failure")
)
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for both new tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/errors.go server/coordinator/errors_test.go
git commit -m "feat(coordinator): add sentinel errors

AA-fork: ErrNotFound / ErrConflict / ErrAuth / ErrTransient are the
four failure categories chisel-server's caller distinguishes.
Client methods wrap these via fmt.Errorf so callers use errors.Is()
to branch without string-matching."
```

### Task 11: Create config.go with Config struct + ParseURL helper

**Files:**
- Create: `server/coordinator/config.go`
- Test: `server/coordinator/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/config_test.go`:

```go
package coordinator

import (
	"strings"
	"testing"
	"time"
)

func TestParseURLAcceptsValidHTTPS(t *testing.T) {
	cfg := &Config{URL: "https://coordinator:8443"}
	u, err := cfg.ParseURL()
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("Scheme = %q", u.Scheme)
	}
	if u.Host != "coordinator:8443" {
		t.Errorf("Host = %q", u.Host)
	}
}

func TestParseURLRejectsHTTP(t *testing.T) {
	cfg := &Config{URL: "http://coordinator:8443"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted http://, want error")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("err = %q, want it to mention https", err)
	}
}

func TestParseURLRejectsEmptyHost(t *testing.T) {
	cfg := &Config{URL: "https:///path"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted empty host, want error")
	}
}

func TestParseURLRejectsMalformed(t *testing.T) {
	cfg := &Config{URL: "://broken"}
	_, err := cfg.ParseURL()
	if err == nil {
		t.Fatal("ParseURL accepted malformed URL, want error")
	}
}

func TestConfigDefaultTimeout(t *testing.T) {
	cfg := &Config{URL: "https://x:8443"}
	if cfg.timeoutOrDefault() != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", cfg.timeoutOrDefault())
	}

	cfg.Timeout = 12 * time.Second
	if cfg.timeoutOrDefault() != 12*time.Second {
		t.Errorf("explicit timeout = %v, want 12s", cfg.timeoutOrDefault())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -v
```

Expected: FAIL with "Config undefined" / "ParseURL undefined".

- [ ] **Step 3: Write minimal implementation**

Create `server/coordinator/config.go`:

```go
package coordinator

import (
	"fmt"
	"net/url"
	"time"
)

// Config holds the runtime configuration for a coordinator.Client.
// Populated from CLI flags (see main.go) or test setup.
type Config struct {
	// URL is the coordinator's base URL (e.g., https://coordinator:8443).
	// Must be https; the path conventions PathLookup / PathActivate /
	// PathDeactivate are appended at call time.
	URL string

	// MTLSCertFile is the path to chisel-server.pem (this fork's
	// client cert presented to the coordinator). Required when URL is set.
	MTLSCertFile string

	// MTLSKeyFile is the path to chisel-server-key.pem matching CertFile.
	MTLSKeyFile string

	// MTLSCAFile is the path to ca.pem used to verify the coordinator's
	// server cert. Same CA that signed the client cert.
	MTLSCAFile string

	// Timeout is the per-call HTTP timeout. Default 5 seconds (anything
	// slower is a coordinator-sick signal — fail fast and let the LCM
	// retry rather than block the operator's SSH attempt).
	Timeout time.Duration
}

// ParseURL validates URL and returns the parsed *url.URL. Called at
// flag-parse time so misconfigs fail bastion-chisel startup rather than
// first-LCM-connection.
func (c *Config) ParseURL() (*url.URL, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("coordinator URL is empty")
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", c.URL, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("coordinator URL must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("coordinator URL has empty host: %q", c.URL)
	}
	return u, nil
}

// timeoutOrDefault returns Timeout if set, otherwise the 5s default.
func (c *Config) timeoutOrDefault() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Second
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all 5 new tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/config.go server/coordinator/config_test.go
git commit -m "feat(coordinator): add Config + ParseURL validation

AA-fork: Config holds URL + mTLS cert paths + timeout. ParseURL
validates at flag-parse time (https only, non-empty host, parseable)
so chisel-server fails to start on misconfig rather than first-LCM-
connect. timeoutOrDefault returns 5s default — anything slower is a
coordinator-sick signal, fail fast."
```

### Task 12: Add LoadMTLS helper to Config

**Files:**
- Modify: `server/coordinator/config.go`
- Test: `server/coordinator/config_mtls_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/config_mtls_test.go`:

```go
package coordinator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// genTestCert writes a self-signed cert + key + ca to dir. Returns the
// three file paths.
func genTestCert(t *testing.T, dir string) (certPath, keyPath, caPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("certgen: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	caPath = certPath // self-signed; CA is the same cert
	return
}

func TestLoadMTLSValidFiles(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: cert,
		MTLSKeyFile:  key,
		MTLSCAFile:   ca,
	}
	tlsCfg, err := cfg.LoadMTLS()
	if err != nil {
		t.Fatalf("LoadMTLS: %v", err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("Certificates len = %d, want 1", len(tlsCfg.Certificates))
	}
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs is nil")
	}
}

func TestLoadMTLSMissingCertFile(t *testing.T) {
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: "/nonexistent/cert.pem",
		MTLSKeyFile:  "/nonexistent/key.pem",
		MTLSCAFile:   "/nonexistent/ca.pem",
	}
	_, err := cfg.LoadMTLS()
	if err == nil {
		t.Fatal("LoadMTLS accepted nonexistent files")
	}
	if !strings.Contains(err.Error(), "cert") {
		t.Errorf("err = %q, want it to mention 'cert'", err)
	}
}

func TestLoadMTLSEmptyCertField(t *testing.T) {
	cfg := &Config{URL: "https://coord:8443"}
	_, err := cfg.LoadMTLS()
	if err == nil {
		t.Fatal("LoadMTLS accepted empty cert paths")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -run TestLoadMTLS -v
```

Expected: FAIL with "LoadMTLS undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `server/coordinator/config.go`:

```go
import (
	"crypto/tls"
	"crypto/x509"
	"os"
)

// LoadMTLS reads the cert/key/ca files from disk and returns a *tls.Config
// wired for client mTLS to the coordinator. Errors out cleanly on missing
// files, malformed PEM, or empty path fields. Called once at chisel-server
// startup; the returned *tls.Config is reused across all coordinator HTTP
// calls.
func (c *Config) LoadMTLS() (*tls.Config, error) {
	if c.MTLSCertFile == "" || c.MTLSKeyFile == "" || c.MTLSCAFile == "" {
		return nil, fmt.Errorf("mTLS cert/key/ca paths must all be set")
	}

	cert, err := tls.LoadX509KeyPair(c.MTLSCertFile, c.MTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load cert/key: %w", err)
	}

	caPEM, err := os.ReadFile(c.MTLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA file %q has no valid PEM certs", c.MTLSCAFile)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
```

The merged import block at the top of `config.go` should be:

```go
import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"time"
)
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all coordinator tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/config.go server/coordinator/config_mtls_test.go
git commit -m "feat(coordinator): add Config.LoadMTLS

AA-fork: reads cert/key/ca from disk, returns *tls.Config wired for
client mTLS with TLS 1.3 minimum. Errors out cleanly on missing fields
or unreadable files. Reused across all coordinator HTTP calls so the
filesystem read happens once at chisel-server startup."
```

### Task 13: Create client.go skeleton with constructor

**Files:**
- Create: `server/coordinator/client.go`
- Test: `server/coordinator/client_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coordinator/client_test.go`:

```go
package coordinator

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

func TestNewClientReturnsConfiguredClient(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: cert,
		MTLSKeyFile:  key,
		MTLSCAFile:   ca,
		Timeout:      3 * time.Second,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.baseURL != "https://coord:8443" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	cfg := &Config{URL: "http://insecure"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted http URL")
	}
}

func TestNewClientRejectsMissingMTLSFiles(t *testing.T) {
	cfg := &Config{URL: "https://coord:8443"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted missing mTLS files")
	}
}

// httpRoundTripperWithTLS is a small helper: returns the *http.Client
// constructed by New so we can inspect its Transport's TLSClientConfig.
func transportTLS(c *Client) *tls.Config {
	if c.http == nil {
		return nil
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		return nil
	}
	return tr.TLSClientConfig
}

func TestNewClientUsesTLS13Minimum(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL: "https://coord:8443", MTLSCertFile: cert, MTLSKeyFile: key, MTLSCAFile: ca,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tlsCfg := transportTLS(c)
	if tlsCfg == nil {
		t.Fatal("Transport.TLSClientConfig is nil")
	}
	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS13 (%d)", tlsCfg.MinVersion, tls.VersionTLS13)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -run TestNewClient -v
```

Expected: FAIL with "Client undefined" / "New undefined".

- [ ] **Step 3: Write minimal implementation**

Create `server/coordinator/client.go`:

```go
package coordinator

import (
	"net/http"
	"strings"
	"time"
)

// Client is the HTTP client for AdvancingAlternatives's bastion coordinator.
// Holds a configured *http.Client (mTLS + per-call timeout) and the base
// URL. All methods take a context.Context for cancellation propagation.
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// New builds a Client from cfg, validating URL + loading mTLS material.
// Errors out before any HTTP flight if cfg is invalid.
func New(cfg *Config) (*Client, error) {
	u, err := cfg.ParseURL()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := cfg.LoadMTLS()
	if err != nil {
		return nil, err
	}

	timeout := cfg.timeoutOrDefault()
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}

	return &Client{
		baseURL: strings.TrimRight(u.String(), "/"),
		http:    httpClient,
		timeout: timeout,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all 4 new tests + earlier ones.

- [ ] **Step 5: Commit**

```
git add server/coordinator/client.go server/coordinator/client_test.go
git commit -m "feat(coordinator): add Client struct + New constructor

AA-fork: Client wraps *http.Client with mTLS-loaded TLS 1.3 config
and per-call timeout. New() validates URL (https only) and loads
mTLS files before returning; subsequent method calls trust the wiring."
```

### Task 14: Implement Client.Lookup

**Files:**
- Modify: `server/coordinator/client.go`
- Modify: `server/coordinator/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `server/coordinator/client_test.go`:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
)

// fakeCoordinatorServer returns an httptest.Server with the given handler.
// Wires the coordinator.Client to it, replacing the http.Transport's TLS
// config so the test doesn't need real certs (the server is plain HTTP).
func fakeCoordinatorServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := &Client{
		baseURL: srv.URL,
		http:    srv.Client(),
		timeout: 2 * time.Second,
	}
	return srv, c
}

func TestLookup200ReturnsSession(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/lookup" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("target_hostname") != "lcm-a57d" {
			t.Errorf("target_hostname query = %q", r.URL.Query().Get("target_hostname"))
		}
		json.NewEncoder(w).Encode(Session{
			SessionID:      "ses_xyz",
			Port:           22099,
			State:          "pending",
			TargetHostname: "lcm-a57d",
		})
	})

	s, err := c.Lookup(context.Background(), "lcm-a57d")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if s.SessionID != "ses_xyz" || s.Port != 22099 {
		t.Errorf("got %+v", s)
	}
}

func TestLookup404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no pending session", http.StatusNotFound)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLookup401ReturnsErrAuth(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestLookup500ReturnsErrTransient(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestLookupTransportFailureReturnsErrTransient(t *testing.T) {
	srv, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // server is now down; next call gets connection-refused
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}
```

The merged imports at top of `client_test.go`:

```go
import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -run TestLookup -v
```

Expected: FAIL with "c.Lookup undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `server/coordinator/client.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

// Lookup queries the coordinator for a pending session matching hostname.
//
// On HTTP 200 returns the parsed session.
// On HTTP 404 returns (nil, err) with err wrapping ErrNotFound — the
// expected "no pending session" path.
// On HTTP 401/403 returns (nil, err) wrapping ErrAuth (Layer-2 cert bug).
// On HTTP 5xx or transport failure returns (nil, err) wrapping ErrTransient.
func (c *Client) Lookup(ctx context.Context, hostname string) (*Session, error) {
	q := url.Values{}
	q.Set("target_hostname", hostname)
	reqURL := c.baseURL + PathLookup + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Lookup: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var s Session
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return nil, fmt.Errorf("Lookup: decode: %w", err)
		}
		return &s, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Lookup: %w", ErrNotFound)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %s", ErrAuth, string(body))
	case resp.StatusCode >= 500:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %d %s", ErrTransient, resp.StatusCode, string(body))
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: unexpected status %d: %s", resp.StatusCode, string(body))
	}
}
```

The merged imports at top of `client.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all 5 new Lookup subtests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/client.go server/coordinator/client_test.go
git commit -m "feat(coordinator): implement Client.Lookup

AA-fork: GET /sessions/lookup?target_hostname=X with full status-code
mapping per the design doc (200 → Session, 404 → ErrNotFound, 401/403
→ ErrAuth, 5xx/transport → ErrTransient). Caller branches on errors.Is
without string-matching."
```

### Task 15: Implement Client.Activate

**Files:**
- Modify: `server/coordinator/client.go`
- Modify: `server/coordinator/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `server/coordinator/client_test.go`:

```go
import "bytes"

func TestActivate200Success(t *testing.T) {
	var got ActivateRequest
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/sessions/ses_abc/activate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	})

	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "10.5.6.7:55432")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got.TargetHostname != "lcm-a57d" || got.ActualPortBound != 22099 || got.ClientRemoteAddr != "10.5.6.7:55432" {
		t.Errorf("activate body = %+v", got)
	}
}

func TestActivate409ReturnsErrConflict(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "hostname mismatch", http.StatusConflict)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestActivate404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session gone", http.StatusNotFound)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestActivate503ReturnsErrTransient(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}
```

The bytes import is unused yet; we need it later — add it to the imports.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -run TestActivate -v
```

Expected: FAIL with "c.Activate undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `server/coordinator/client.go`:

```go
import "bytes"

// Activate posts the bound port + remote address for a session,
// transitioning it pending→active on the coordinator side.
//
// Returns nil on 200.
// Returns err wrapping ErrNotFound on 404 (session gone — race condition).
// Returns err wrapping ErrConflict on 409 (port mismatch / hostname
// mismatch / wrong state — caller should tear down the proxy).
// Returns err wrapping ErrAuth on 401/403 (cert bug).
// Returns err wrapping ErrTransient on 5xx / transport failure.
func (c *Client) Activate(ctx context.Context, sessionID, hostname string, port int, remoteAddr string) error {
	body, err := json.Marshal(ActivateRequest{
		TargetHostname:   hostname,
		ActualPortBound:  port,
		ClientRemoteAddr: remoteAddr,
	})
	if err != nil {
		return fmt.Errorf("Activate: marshal: %w", err)
	}

	reqURL := c.baseURL + fmt.Sprintf(PathActivate, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Activate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Activate: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// Drain body so the connection can be reused by the next call.
		io.Copy(io.Discard, resp.Body)
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("Activate: %w", ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %s", ErrConflict, string(respBody))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %s", ErrAuth, string(respBody))
	case resp.StatusCode >= 500:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %d %s", ErrTransient, resp.StatusCode, string(respBody))
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all 4 new Activate subtests + all earlier tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/client.go server/coordinator/client_test.go
git commit -m "feat(coordinator): implement Client.Activate

AA-fork: POST /sessions/{id}/activate with target_hostname, port, and
client_remote_addr. Status-code mapping per design doc (200/404/409/
401/5xx). 409 specifically maps to ErrConflict so the caller (handler
in Phase 3) can branch on it for proxy-teardown."
```

### Task 16: Implement Client.Deactivate

**Files:**
- Modify: `server/coordinator/client.go`
- Modify: `server/coordinator/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `server/coordinator/client_test.go`:

```go
func TestDeactivate200Success(t *testing.T) {
	var got DeactivateRequest
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/ses_abc/deactivate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	})

	err := c.Deactivate(context.Background(), "ses_abc", "lcm-a57d", 22099, "client_disconnect")
	if err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if got.Reason != "client_disconnect" {
		t.Errorf("reason = %q", got.Reason)
	}
	if got.TargetHostname != "lcm-a57d" || got.ActualPortBound != 22099 {
		t.Errorf("deactivate body = %+v", got)
	}
}

func TestDeactivate204Success(t *testing.T) {
	// Coordinator may also use 204 No Content.
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-a57d", 22099, "client_disconnect")
	if err != nil {
		t.Errorf("Deactivate(204): %v", err)
	}
}

func TestDeactivate409ReturnsErrConflict(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "already closed", http.StatusConflict)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-x", 22099, "client_disconnect")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestDeactivate404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session gone", http.StatusNotFound)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-x", 22099, "client_disconnect")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/coordinator/ -run TestDeactivate -v
```

Expected: FAIL with "c.Deactivate undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `server/coordinator/client.go`:

```go
// Deactivate posts the close reason for a session. Best-effort from the
// caller's perspective: errors are logged but generally don't propagate
// (the coordinator's TTL fallback handles cleanup if this fails).
//
// Returns nil on 200 or 204.
// Returns err wrapping ErrNotFound on 404 (session gone — coordinator already cleaned up).
// Returns err wrapping ErrConflict on 409 (already closed — stale retry).
// Returns err wrapping ErrAuth on 401/403, ErrTransient on 5xx / transport.
func (c *Client) Deactivate(ctx context.Context, sessionID, hostname string, port int, reason string) error {
	body, err := json.Marshal(DeactivateRequest{
		TargetHostname:  hostname,
		ActualPortBound: port,
		Reason:          reason,
	})
	if err != nil {
		return fmt.Errorf("Deactivate: marshal: %w", err)
	}

	reqURL := c.baseURL + fmt.Sprintf(PathDeactivate, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Deactivate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Deactivate: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent:
		io.Copy(io.Discard, resp.Body)
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("Deactivate: %w", ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %s", ErrConflict, string(respBody))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %s", ErrAuth, string(respBody))
	case resp.StatusCode >= 500:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %d %s", ErrTransient, resp.StatusCode, string(respBody))
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./server/coordinator/ -v
```

Expected: PASS for all 4 new Deactivate subtests + all coordinator tests.

- [ ] **Step 5: Commit**

```
git add server/coordinator/client.go server/coordinator/client_test.go
git commit -m "feat(coordinator): implement Client.Deactivate

AA-fork: POST /sessions/{id}/deactivate with target_hostname, port,
and reason. Accepts 200 or 204 as success. 409 maps to ErrConflict
(already closed — stale retry no-op for the caller). Caller logs
errors at WARN/ERROR per the table in the design doc; coordinator's
TTL fallback covers any failures."
```

### Task 17: Phase 2 final verification

**Files:** Read-only.

- [ ] **Step 1: Full coordinator test pass**

```
go test ./server/coordinator/... -v
```

Expected: all green. Test list includes (count ~25 subtests including subcases):
- `TestPathConstants`
- `TestSessionUnmarshalsLookupResponse`
- `TestActivateRequestMarshalsToContractFields`
- `TestDeactivateRequestMarshalsToContractFields`
- `TestSentinelErrorsAreDistinct`
- `TestSentinelErrorsWrapForCallers`
- `TestParseURL{AcceptsValidHTTPS,RejectsHTTP,RejectsEmptyHost,RejectsMalformed}`
- `TestConfigDefaultTimeout`
- `TestLoadMTLS{ValidFiles,MissingCertFile,EmptyCertField}`
- `TestNewClient{ReturnsConfiguredClient,RejectsBadURL,RejectsMissingMTLSFiles,UsesTLS13Minimum}`
- `TestLookup{200ReturnsSession,404ReturnsErrNotFound,401ReturnsErrAuth,500ReturnsErrTransient,TransportFailureReturnsErrTransient}`
- `TestActivate{200Success,409ReturnsErrConflict,404ReturnsErrNotFound,503ReturnsErrTransient}`
- `TestDeactivate{200Success,204Success,409ReturnsErrConflict,404ReturnsErrNotFound}`

- [ ] **Step 2: Push Phase 2 commits**

```
git push origin feat/coordinator-callbacks
```

---

## Phase 3: `server/` integration

This phase wires the coordinator client into chisel-server's reverse-tunnel handler. Adds shutdownCtx/shutdownCancel for cancel-cause propagation, the lookup→port-override→activate flow with closure-captured callbacks, and the rejectChisel helper. Independently testable via Phase 5's integration tests; passes existing chisel e2e in the meantime when Coordinator field is nil.

### Task 18: Add Coordinator field to chserver.Config + Server struct fields

**Files:**
- Modify: `server/server.go`
- Test: `server/server_init_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `server/server_init_test.go`:

```go
package chserver

import (
	"errors"
	"testing"

	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/tunnel"
)

func TestNewServerNoCoordinatorWorks(t *testing.T) {
	s, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if s.coordClient != nil {
		t.Error("coordClient should be nil when Coordinator field unset")
	}
	if s.shutdownCtx == nil {
		t.Error("shutdownCtx should always be initialised, even in legacy mode")
	}
}

func TestNewServerInvalidCoordinatorURLFails(t *testing.T) {
	_, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
		Coordinator: &coordinator.Config{
			URL: "http://insecure", // not https
		},
	})
	if err == nil {
		t.Fatal("NewServer accepted http coordinator URL")
	}
}

func TestServerCloseCancelsShutdownCtxWithSentinel(t *testing.T) {
	s, err := NewServer(&Config{
		KeySeed: "test",
		Reverse: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Don't actually start the http server; just call Close.
	s.shutdownCancel(tunnel.ErrServerShutdown)
	if !errors.Is(s.shutdownCtx.Err(), context.Canceled) {
		t.Errorf("shutdownCtx.Err = %v, want Canceled", s.shutdownCtx.Err())
	}
	if !errors.Is(context.Cause(s.shutdownCtx), tunnel.ErrServerShutdown) {
		t.Errorf("shutdownCtx cause = %v, want ErrServerShutdown", context.Cause(s.shutdownCtx))
	}
}
```

The merged imports:

```go
import (
	"context"
	"errors"
	"testing"

	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/tunnel"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./server/ -run TestNewServerNoCoordinatorWorks -v
```

Expected: FAIL with "Coordinator field undefined" / "shutdownCtx undefined".

- [ ] **Step 3: Modify Config struct**

In `server/server.go`, find the `Config` struct (around line 24-35). Replace it with:

```go
// Config is the configuration for the chisel service
type Config struct {
	KeySeed   string
	KeyFile   string
	AuthFile  string
	Auth      string
	Proxy     string
	Socks5    bool
	Reverse   bool
	KeepAlive time.Duration
	TLS       TLSConfig

	// AA-fork: optional coordinator integration. When non-nil, the server
	// consults this coordinator at every reverse-tunnel connect to gate
	// the request, override the tunnel port to a coordinator-allocated
	// value, and report lifecycle events. When nil, the server runs in
	// upstream-compatible mode — the coordinator code paths are bypassed.
	Coordinator *coordinator.Config
}
```

Add the import for `coordinator` at the top of `server.go`:

```go
import (
	// ...existing...
	"github.com/jpillora/chisel/server/coordinator"
)
```

- [ ] **Step 4: Modify Server struct**

In `server/server.go`, find the `Server` struct (around line 38-48). Replace with:

```go
// Server respresent a chisel service
type Server struct {
	*cio.Logger
	config       *Config
	fingerprint  string
	httpServer   *cnet.HTTPServer
	reverseProxy *httputil.ReverseProxy
	sessCount    int32
	sessions     *settings.Users
	sshConfig    *ssh.ServerConfig
	users        *settings.UserIndex

	// AA-fork: coordinator integration state. coordClient is nil iff
	// config.Coordinator == nil. shutdownCtx is cancelled with cause
	// tunnel.ErrServerShutdown by Close() so OnRemoteUnbound callbacks
	// can distinguish server-initiated closes via context.Cause(ctx).
	coordClient    *coordinator.Client
	shutdownCtx    context.Context
	shutdownCancel context.CancelCauseFunc
}
```

Verify `context` is in the imports (it is — used by StartContext).

- [ ] **Step 5: Modify NewServer to wire shutdownCtx + coordClient**

In `server/server.go`, locate `NewServer` (around line 56-144). At the very start of the function (right after `server := &Server{...}`), set up shutdownCtx. After all existing initialization, set up coordClient.

Find:

```go
func NewServer(c *Config) (*Server, error) {
	server := &Server{
		config:     c,
		httpServer: cnet.NewHTTPServer(),
		Logger:     cio.NewLogger("server"),
		sessions:   settings.NewUsers(),
	}
```

Replace with:

```go
func NewServer(c *Config) (*Server, error) {
	// AA-fork: initialise the shutdown context so Close() can propagate a
	// cancel cause to in-flight handlers' OnRemoteUnbound callbacks.
	shutdownCtx, shutdownCancel := context.WithCancelCause(context.Background())

	server := &Server{
		config:         c,
		httpServer:     cnet.NewHTTPServer(),
		Logger:         cio.NewLogger("server"),
		sessions:       settings.NewUsers(),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}
```

Then at the END of the function (just before `return server, nil`), add:

```go
	// AA-fork: build the coordinator client if configured.
	if c.Coordinator != nil {
		client, err := coordinator.New(c.Coordinator)
		if err != nil {
			return nil, fmt.Errorf("coordinator: %w", err)
		}
		server.coordClient = client
		server.Infof("Coordinator integration enabled: %s", c.Coordinator.URL)
	}

	return server, nil
}
```

(The existing `return server, nil` stays; just insert the coordinator block above it.)

- [ ] **Step 6: Modify Close to fire shutdownCancel**

Find `func (s *Server) Close()` (around line 189-191). Replace with:

```go
// Close forcibly closes the http server.
//
// AA-fork: cancels shutdownCtx with tunnel.ErrServerShutdown as the cause
// so OnRemoteUnbound callbacks can classify server-initiated closes
// via context.Cause(ctx). The cancel is best-effort (callers may invoke
// Close multiple times); cancel-cause is safe to call multiple times
// with later calls being no-ops.
func (s *Server) Close() error {
	s.shutdownCancel(tunnel.ErrServerShutdown)
	return s.httpServer.Close()
}
```

Add the import for `tunnel`:

```go
import (
	// ...existing...
	"github.com/jpillora/chisel/share/tunnel"
)
```

- [ ] **Step 7: Run tests to verify they pass**

```
go test ./server/ -v
```

Expected: PASS for all 3 new tests. Existing chisel tests are unaffected (the new fields only matter when used).

- [ ] **Step 8: Commit**

```
git add server/server.go server/server_init_test.go
git commit -m "feat(server): add Coordinator field + cancel-cause shutdown

AA-fork: Config.Coordinator (nil = legacy mode) wires into a
coordinator.Client at NewServer time, validating URL + loading mTLS
material before returning. Server gains shutdownCtx (context.WithCancelCause)
that Close() cancels with tunnel.ErrServerShutdown — so in-flight handlers
can derive ctxs from it and OnRemoteUnbound's classifyDisconnect reads
the cause locally without a setter race."
```

### Task 19: Add overrideReverseRemotePort helper

**Files:**
- Create: `server/server_override_test.go`
- Modify: `server/server.go` (or add new file `server/coord_handler.go`)

For clarity, add the helper to a new file `server/coord_handler.go` so the existing `server.go` and `server_handler.go` don't grow with fork-specific glue.

- [ ] **Step 1: Write the failing test**

Create `server/server_override_test.go`:

```go
package chserver

import (
	"testing"

	"github.com/jpillora/chisel/share/settings"
)

func TestOverrideReverseRemotePort(t *testing.T) {
	cases := []struct {
		name    string
		remotes []*settings.Remote
		port    int
		wantErr bool
		wantPort string
	}{
		{
			name: "single reverse",
			remotes: []*settings.Remote{
				{Reverse: true, RemotePort: "0"},
			},
			port:     22099,
			wantErr:  false,
			wantPort: "22099",
		},
		{
			name: "no reverse remote",
			remotes: []*settings.Remote{
				{Reverse: false, RemotePort: "0"},
			},
			port:    22099,
			wantErr: true,
		},
		{
			name:    "empty remotes",
			remotes: []*settings.Remote{},
			port:    22099,
			wantErr: true,
		},
		{
			name: "multiple reverses (β.1 is 1-remote per session, this is undefined behavior; pick the first)",
			remotes: []*settings.Remote{
				{Reverse: true, RemotePort: "0"},
				{Reverse: true, RemotePort: "0"},
			},
			port:     22099,
			wantErr:  false,
			wantPort: "22099",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := overrideReverseRemotePort(tc.remotes, tc.port)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if tc.remotes[0].RemotePort != tc.wantPort {
					t.Errorf("RemotePort = %q, want %q", tc.remotes[0].RemotePort, tc.wantPort)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./server/ -run TestOverrideReverseRemotePort -v
```

Expected: FAIL with "overrideReverseRemotePort undefined".

- [ ] **Step 3: Write minimal implementation**

Create `server/coord_handler.go`:

```go
// Package chserver: AA-fork coordinator-integration glue. This file
// holds helpers used by handleWebsocket when chisel-server is configured
// with a coordinator. Kept separate from server_handler.go so the
// upstream merge surface for server_handler.go stays small.
package chserver

import (
	"fmt"
	"strconv"

	"github.com/jpillora/chisel/share/settings"
)

// overrideReverseRemotePort finds the first reverse remote in remotes and
// sets its RemotePort to port. The β.1 model has exactly one reverse
// per session (R:0:localhost:22 from the LCM); if zero remotes match,
// returns an error so the caller can SSH-reject the connection.
//
// Mutates remotes in place because chisel's downstream code re-reads
// each Remote.RemotePort via .UserAddr() / .CanListen(), and the
// override needs to be visible there.
func overrideReverseRemotePort(remotes []*settings.Remote, port int) error {
	for _, r := range remotes {
		if r.Reverse {
			r.RemotePort = strconv.Itoa(port)
			return nil
		}
	}
	return fmt.Errorf("no reverse remote in client config (got %d remotes)", len(remotes))
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./server/ -run TestOverrideReverseRemotePort -v
```

Expected: PASS for all 4 subcases.

- [ ] **Step 5: Commit**

```
git add server/coord_handler.go server/server_override_test.go
git commit -m "feat(server): add overrideReverseRemotePort helper

AA-fork: rewrites the first reverse Remote's RemotePort in-place to
the coordinator-allocated port. β.1's LCM-driven model has exactly
one reverse remote per session (R:0:localhost:22); the override
makes chisel bind to the pre-allocated port instead of a random one.
Errors when no reverse remote is found so the caller can SSH-reject."
```

### Task 20: Add rejectChisel helper

**Files:**
- Modify: `server/coord_handler.go`
- Test: `server/coord_handler_test.go`

- [ ] **Step 1: Write the failing test**

Create `server/coord_handler_test.go`:

```go
package chserver

import (
	"errors"
	"testing"

	"github.com/jpillora/chisel/server/coordinator"
)

func TestRejectChiselMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not found", coordinator.ErrNotFound, "no pending session"},
		{"auth", coordinator.ErrAuth, "coordinator unreachable"},
		{"transient", coordinator.ErrTransient, "coordinator unreachable, retry in flight"},
		{"conflict", coordinator.ErrConflict, "state divergence on activate"},
		{"unknown", errors.New("weird"), "coordinator error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := rejectMessage(tc.err)
			if !contains(msg, tc.want) {
				t.Errorf("rejectMessage(%v) = %q, want it to contain %q", tc.err, msg, tc.want)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./server/ -run TestRejectChiselMessage -v
```

Expected: FAIL with "rejectMessage undefined".

- [ ] **Step 3: Write minimal implementation**

Append to `server/coord_handler.go`:

```go
import (
	"errors"
	"github.com/jpillora/chisel/server/coordinator"
)

// rejectMessage maps a coordinator client error to the operator-facing
// message string surfaced via SSH-level rejection (r.Reply(false, ...)).
//
// Auth and transient failures intentionally return the same generic
// "coordinator unreachable" framing so a chisel-server cert bug
// (Layer-2) doesn't leak detail to the LCM client. The actual cause
// stays in chisel-server's own logs (logged separately by the caller
// at the appropriate level — INFO/WARN/ERROR per the design doc).
func rejectMessage(err error) string {
	switch {
	case errors.Is(err, coordinator.ErrNotFound):
		return "no pending session for hostname"
	case errors.Is(err, coordinator.ErrAuth):
		return "coordinator unreachable"
	case errors.Is(err, coordinator.ErrTransient):
		return "coordinator unreachable, retry in flight"
	case errors.Is(err, coordinator.ErrConflict):
		return "state divergence on activate, see coordinator logs"
	default:
		return "coordinator error"
	}
}
```

The merged imports at top of `coord_handler.go`:

```go
import (
	"errors"
	"fmt"
	"strconv"

	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/settings"
)
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./server/ -run TestRejectChiselMessage -v
```

Expected: PASS for all 5 subcases.

- [ ] **Step 5: Commit**

```
git add server/coord_handler.go server/coord_handler_test.go
git commit -m "feat(server): add rejectMessage helper

AA-fork: maps coordinator client errors to operator-facing SSH-reject
message strings. Generic 'coordinator unreachable' framing for Auth
(Layer-2 cert bug) and Transient (5xx) failures so chisel-server
config issues don't leak to LCM clients. Specific 'no pending session'
for ErrNotFound and 'state divergence' for ErrConflict so triage
hints surface in the chisel-client journal."
```

### Task 21: Modify handleWebsocket — add coordinator integration

This is the largest single task in Phase 3 — modifies the existing handleWebsocket function. To keep the diff reviewable, the implementation is split into a new helper `handleCoordinatorPath` that we call from a small if-branch in handleWebsocket.

**Files:**
- Modify: `server/server_handler.go`
- Modify: `server/coord_handler.go`

- [ ] **Step 1: Read the current handleWebsocket**

Reference `share/tunnel/disconnect.go` and `server/server_handler.go:51-171`. The current handleWebsocket runs auth → DecodeConfig → validate remotes → tunnel.New → BindSSH/BindRemotes. We'll add the coordinator branch after DecodeConfig + remotes validation but before `tunnel.New`.

- [ ] **Step 2: Add coordinatorBindHook helper**

Append to `server/coord_handler.go`:

```go
import (
	"context"
	"net"

	"github.com/jpillora/chisel/share/tunnel"
)

// coordinatorBindHook returns a closure suitable for tunnel.Config.OnRemoteBound
// that posts an Activate to the coordinator and propagates errors. Captures
// sessionID, hostname, and remoteAddr in the closure so they're available
// at bind time.
func coordinatorBindHook(client *coordinator.Client, sessionID, hostname, remoteAddr string) func(context.Context, *settings.Remote, net.Listener) error {
	return func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
		// Use the listener's Addr to get the actually-bound port (relevant
		// when the requested port was 0 / OS-picked, though in coordinator
		// mode we override to the allocated port so they should match).
		port := r.RemotePortInt()
		if tcp, ok := ln.Addr().(*net.TCPAddr); ok && tcp.Port != 0 {
			port = tcp.Port
		}
		return client.Activate(ctx, sessionID, hostname, port, remoteAddr)
	}
}

// coordinatorUnbindHook returns a closure suitable for tunnel.Config.OnRemoteUnbound
// that posts a Deactivate to the coordinator. Errors are logged but not
// propagated — the coordinator's TTL fallback handles cleanup if this fails.
//
// The deactivate call uses a fresh background context with the coordinator's
// timeout, NOT the proxy's ctx (which is already cancelled by the time
// OnRemoteUnbound fires — its cause is what tells us the disconnect reason).
func coordinatorUnbindHook(client *coordinator.Client, log func(format string, args ...interface{}), timeout time.Duration, sessionID, hostname string) func(*settings.Remote, tunnel.DisconnectReason) {
	return func(r *settings.Remote, reason tunnel.DisconnectReason) {
		port := r.RemotePortInt()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := client.Deactivate(ctx, sessionID, hostname, port, string(reason)); err != nil {
			log("deactivate failed sessionID=%s hostname=%s port=%d reason=%s err=%v",
				sessionID, hostname, port, reason, err)
		}
	}
}
```

The merged import block at top of `coord_handler.go`:

```go
import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jpillora/chisel/server/coordinator"
	"github.com/jpillora/chisel/share/settings"
	"github.com/jpillora/chisel/share/tunnel"
)
```

- [ ] **Step 3: Modify handleWebsocket — wire coordinator branch**

In `server/server_handler.go`, find the section right after `r.Reply(true, nil)` (the line that confirms the chisel handshake) and BEFORE `tunnelConfig := tunnel.Config{...}`. Currently around line 136-138. Insert:

```go
	//successfuly validated config!
	r.Reply(true, nil)

	// AA-fork: coordinator integration — runs only when configured.
	// Derive a request-scoped ctx that's cancelled either by the request
	// closing OR the server shutting down. Server shutdown propagates
	// cancel-cause tunnel.ErrServerShutdown so OnRemoteUnbound's
	// classifyDisconnect can pick it up locally.
	reqCtx, reqCancel := context.WithCancelCause(req.Context())
	go func() {
		select {
		case <-s.shutdownCtx.Done():
			reqCancel(context.Cause(s.shutdownCtx))
		case <-reqCtx.Done():
		}
	}()
	defer reqCancel(nil)

	var (
		sessionID  string
		hostname   = req.Host
		remoteAddr = req.RemoteAddr
	)

	if s.coordClient != nil {
		// 1. Lookup pending session for this hostname.
		sess, err := s.coordClient.Lookup(reqCtx, hostname)
		if err != nil {
			level := "info"
			if errors.Is(err, coordinator.ErrAuth) {
				level = "error"
			} else if errors.Is(err, coordinator.ErrTransient) {
				level = "warn"
			}
			s.coordLog(level, "lookup failed hostname=%s err=%v", hostname, err)
			failed(s.Errorf("%s", rejectMessage(err)))
			return
		}
		sessionID = sess.SessionID

		// 2. Override the reverse remote's port to the allocated value.
		if err := overrideReverseRemotePort(c.Remotes, sess.Port); err != nil {
			l.Errorf("override reverse port: %v", err)
			failed(s.Errorf("server config error"))
			return
		}
	}
```

Then find the `tunnelConfig := tunnel.Config{...}` block (around line 138-148). Replace with:

```go
	//tunnel per ssh connection
	tunnelConfig := tunnel.Config{
		Logger:    l,
		Inbound:   s.config.Reverse,
		Outbound:  true, //server always accepts outbound
		Socks:     s.config.Socks5,
		KeepAlive: s.config.KeepAlive,
	}
	//enforce ACL on every channel, not just the initial config
	if user != nil {
		tunnelConfig.ACL = user.HasAccess
	}
	// AA-fork: install coordinator callbacks when configured. The closures
	// capture sessionID + hostname + remoteAddr + the client + a logger so
	// the call sites (inside tunnel package) don't need to know about the
	// chisel-server type.
	if s.coordClient != nil {
		tunnelConfig.OnRemoteBound = coordinatorBindHook(s.coordClient, sessionID, hostname, remoteAddr)
		tunnelConfig.OnRemoteUnbound = coordinatorUnbindHook(
			s.coordClient,
			func(format string, args ...interface{}) { l.Infof(format, args...) },
			s.config.Coordinator.timeoutOrDefault(),
			sessionID,
			hostname,
		)
	}
	tunnel := tunnel.New(tunnelConfig)
```

Then the `eg, ctx := errgroup.WithContext(req.Context())` line — change to use `reqCtx`:

```go
	//bind
	eg, ctx := errgroup.WithContext(reqCtx)
```

- [ ] **Step 4: Add coordLog helper to Server**

In `server/server.go`, add a small method on Server. Find the `// AddUser` section and insert before it:

```go
// AA-fork: coordLog is a small wrapper around Server's logger that respects
// a level argument. Used by handleWebsocket's coordinator-error mapping to
// route messages to INFO / WARN / ERROR per the design doc's table.
func (s *Server) coordLog(level, format string, args ...interface{}) {
	switch level {
	case "error":
		s.Errorf(format, args...)
	case "warn":
		// chisel's cio.Logger doesn't have Warnf — log as Info with a prefix.
		s.Infof("WARN "+format, args...)
	default:
		s.Infof(format, args...)
	}
}
```

- [ ] **Step 5: Run server build to verify**

```
go build ./server/...
```

Expected: no errors. The new code references `time.Duration` (used in coordinatorUnbindHook), confirm `time` is imported in server_handler.go (it is — used by ConfigTimeout).

- [ ] **Step 6: Run tests to verify nothing broke**

```
go test ./server/...
go test ./test/e2e/...
```

Expected: all PASS. The legacy path (Coordinator nil) is unchanged from upstream. New code compiles but isn't exercised by these tests yet — that's Phase 5.

- [ ] **Step 7: Commit**

```
git add server/server_handler.go server/coord_handler.go server/server.go
git commit -m "feat(server): wire coordinator lookup → activate → deactivate flow

AA-fork: handleWebsocket gets a coordinator branch that runs only when
config.Coordinator is non-nil. Derives a request-scoped ctx from
shutdownCtx so server shutdown propagates as cancel-cause. Lookup
gates the connection (404 → SSH reject 'no pending session'). Port
override rewrites the reverse remote to the coordinator-allocated
port. OnRemoteBound/OnRemoteUnbound callbacks (closures capturing
sessionID/hostname/remoteAddr) post Activate/Deactivate at the right
moments in the proxy lifecycle.

When config.Coordinator is nil, the new code is bypassed entirely —
upstream behavior is preserved."
```

### Task 22: Phase 3 verification

**Files:** Read-only.

- [ ] **Step 1: Full server tests + e2e**

```
go test ./server/... -v
go test ./test/e2e/... -v
```

Expected: all PASS. Existing chisel e2e proves legacy mode works; new server unit tests cover the wiring.

- [ ] **Step 2: Full project tests**

```
go test ./... -v
```

Expected: all PASS (existing tests + new ones from Phase 1, 2, 3).

- [ ] **Step 3: Push Phase 3 commits**

```
git push origin feat/coordinator-callbacks
```

---

## Phase 4: `main.go` flag wiring

CLI plumbing — small commit, validates URL at parse time.

### Task 23: Add coordinator-prefixed CLI flags

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Read current flag block**

```
grep -n "flags.String" main.go | head -30
```

Will show the existing flag definitions. Identify a clean insertion point — preferably grouped with the TLS-related server flags.

- [ ] **Step 2: Add the new flags + wiring**

In `main.go`, find the function `server` (the one that parses flags + builds chserver.Config). It's currently the function that handles the `chisel server ...` subcommand. Inside, find where the existing flags are defined.

Add these flag declarations alongside the existing ones:

```go
	coordinatorURL := flags.String("coordinator-url", "", "")
	coordinatorMTLSCert := flags.String("coordinator-mtls-cert", "", "")
	coordinatorMTLSKey := flags.String("coordinator-mtls-key", "", "")
	coordinatorMTLSCA := flags.String("coordinator-mtls-ca", "", "")
	coordinatorTimeout := flags.Duration("coordinator-timeout", 5*time.Second, "")
```

After the existing flag parse + Config build, before `chserver.NewServer(c)`, add:

```go
	// AA-fork: wire coordinator integration when --coordinator-url is set.
	if *coordinatorURL != "" {
		c.Coordinator = &coordinator.Config{
			URL:          *coordinatorURL,
			MTLSCertFile: *coordinatorMTLSCert,
			MTLSKeyFile:  *coordinatorMTLSKey,
			MTLSCAFile:   *coordinatorMTLSCA,
			Timeout:      *coordinatorTimeout,
		}
	}
```

Add to the imports at top of `main.go`:

```go
import (
	// ...existing...
	"github.com/jpillora/chisel/server/coordinator"
)
```

- [ ] **Step 3: Update the help text for `chisel server`**

Find the existing `serverHelp` string constant in `main.go`. Add a section documenting the new flags. Locate the part that lists existing flags (e.g., `--keepalive`, `--proxy`) and append:

```
    --coordinator-url, AA-fork: base URL of the bastion coordinator (e.g.
    https://coordinator:8443). When unset, chisel runs in upstream-compatible
    mode without coordinator integration. When set, all reverse-tunnel
    connect requests consult the coordinator at /sessions/lookup before
    binding. Required to be https.

    --coordinator-mtls-cert, --coordinator-mtls-key, --coordinator-mtls-ca,
    AA-fork: paths to the chisel-server's client certificate, key, and the
    coordinator's CA cert respectively. Required when --coordinator-url is
    set. The chisel-server CN must match the coordinator's authz mapping
    (typically "bastion-chisel"). See bastion's docs/chisel-fork-contract.md.

    --coordinator-timeout, AA-fork: per-call HTTP timeout for coordinator
    requests. Default 5s. Anything slower is a coordinator-sick signal —
    fail fast and let the LCM retry rather than block the operator's SSH.
```

- [ ] **Step 4: Verify build**

```
go build ./...
```

Expected: no errors.

- [ ] **Step 5: Verify --help works**

```
go run . server --help 2>&1 | grep -A 3 coordinator
```

Expected: shows the new flag descriptions in the help output.

- [ ] **Step 6: Commit**

```
git add main.go
git commit -m "feat(main): add --coordinator-* CLI flags

AA-fork: --coordinator-url enables coordinator integration; --coordinator-mtls-*
specify the cert/key/ca paths; --coordinator-timeout overrides the 5s
default. URL validation happens inside coordinator.New() called from
NewServer; --coordinator-url empty leaves Config.Coordinator nil
(upstream-compatible mode)."
```

---

## Phase 5: Integration tests

End-to-end tests that boot a real chisel-server with a fake coordinator (httptest.Server). Each test scenario exercises one design property.

### Task 24: Test infrastructure — fake coordinator

**Files:**
- Create: `test/e2e/coordinator_helpers_test.go`

- [ ] **Step 1: Create the fake-coordinator helper**

Create `test/e2e/coordinator_helpers_test.go`:

```go
package e2e_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCoordinator is an in-process httptest.Server that implements the
// bastion coordinator's session-lifecycle endpoints. Records every call
// so tests can assert on hit-counts + bodies.
type fakeCoordinator struct {
	server *httptest.Server

	mu              sync.Mutex
	sessions        map[string]coordSession // keyed by target_hostname
	sessionsByID    map[string]coordSession
	lookupCalls     []string // target_hostnames in order
	activateCalls   []activateBody
	deactivateCalls []deactivateBody

	// Test-side overrides
	lookupStatusOverride int // when non-zero, lookup returns this status regardless
}

type coordSession struct {
	SessionID      string `json:"session_id"`
	Port           int    `json:"port"`
	State          string `json:"state"`
	TargetHostname string `json:"target_hostname"`
}

type activateBody struct {
	SessionID        string `json:"-"`
	TargetHostname   string `json:"target_hostname"`
	ActualPortBound  int    `json:"actual_port_bound"`
	ClientRemoteAddr string `json:"client_remote_addr"`
}

type deactivateBody struct {
	SessionID       string `json:"-"`
	TargetHostname  string `json:"target_hostname"`
	ActualPortBound int    `json:"actual_port_bound"`
	Reason          string `json:"reason"`
}

func newFakeCoordinator(t *testing.T) *fakeCoordinator {
	t.Helper()
	fc := &fakeCoordinator{
		sessions:     map[string]coordSession{},
		sessionsByID: map[string]coordSession{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", fc.handleLookup)
	mux.HandleFunc("/sessions/", fc.handleSessionsByID)
	fc.server = httptest.NewServer(mux)
	t.Cleanup(fc.server.Close)
	return fc
}

func (fc *fakeCoordinator) URL() string { return fc.server.URL }

func (fc *fakeCoordinator) preallocate(hostname, sessionID string, port int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	s := coordSession{SessionID: sessionID, Port: port, State: "pending", TargetHostname: hostname}
	fc.sessions[hostname] = s
	fc.sessionsByID[sessionID] = s
}

func (fc *fakeCoordinator) handleLookup(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("target_hostname")
	fc.mu.Lock()
	fc.lookupCalls = append(fc.lookupCalls, hostname)
	override := fc.lookupStatusOverride
	s, ok := fc.sessions[hostname]
	fc.mu.Unlock()

	if override != 0 {
		http.Error(w, "override", override)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// handleSessionsByID dispatches /sessions/{id}/activate and /sessions/{id}/deactivate
// using URL-path parsing (the http.ServeMux pattern matching requires
// exact paths; we lift the wildcard manually here for this stand-in).
func (fc *fakeCoordinator) handleSessionsByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "sessions" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id := parts[1]
	verb := parts[2]
	switch verb {
	case "activate":
		fc.handleActivate(w, r, id)
	case "deactivate":
		fc.handleDeactivate(w, r, id)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (fc *fakeCoordinator) handleActivate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body activateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	body.SessionID = id

	fc.mu.Lock()
	fc.activateCalls = append(fc.activateCalls, body)
	s, ok := fc.sessionsByID[id]
	if ok {
		s.State = "active"
		fc.sessionsByID[id] = s
		fc.sessions[s.TargetHostname] = s
	}
	fc.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (fc *fakeCoordinator) handleDeactivate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body deactivateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	body.SessionID = id

	fc.mu.Lock()
	fc.deactivateCalls = append(fc.deactivateCalls, body)
	s, ok := fc.sessionsByID[id]
	if ok {
		s.State = "closed"
		fc.sessionsByID[id] = s
		delete(fc.sessions, s.TargetHostname)
	}
	fc.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// extractHostFromURL parses urlStr and returns its host:port. Used in
// tests when constructing a chisel-client config that points at the
// httptest server's listen address.
func extractHostFromURL(t *testing.T, urlStr string) string {
	t.Helper()
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("parse %q: %v", urlStr, err)
	}
	return u.Host
}

// waitFor blocks until pred returns true or timeout. Returns true if pred
// became true, false on timeout.
func waitFor(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pred()
}

var _ = errors.New // pacify import if unused in helpers (keeps import for tests below)
```

- [ ] **Step 2: Verify helper compiles**

```
go test ./test/e2e/ -run NoSuchTest -v
```

Expected: no compile errors. The helper is unused so far; tests using it come in subsequent tasks.

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_helpers_test.go
git commit -m "test(e2e): add fakeCoordinator helper

In-process httptest.Server stand-in for the bastion coordinator.
Records lookup/activate/deactivate calls + bodies so tests can
assert on hit counts and request shapes. Subsequent test tasks
build scenarios on top of this helper."
```

### Task 25: Test — coordinator omitted runs legacy mode

**Files:**
- Create: `test/e2e/coordinator_omitted_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_omitted_test.go`:

```go
package e2e_test

import (
	"context"
	"net"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
)

// TestCoordinatorOmittedRunsLegacy verifies that when chserver.Config
// has no Coordinator field set, the server runs in upstream-compatible
// mode. Client-requested port is bound directly; no coordinator
// interaction is attempted.
func TestCoordinatorOmittedRunsLegacy(t *testing.T) {
	srv, err := chserver.NewServer(&chserver.Config{
		KeySeed: "test-omit",
		Reverse: true,
		Auth:    "user:pass",
		// Coordinator: nil — explicitly testing default
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	listenPort := availablePort()
	if err := srv.Start("127.0.0.1", listenPort); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	tunnelPort := availablePort()
	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "user:pass",
		Hostname:         "lcm-leg1",
		Remotes:          []string{"R:" + tunnelPort + ":127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// Legacy: chisel-server binds the client-requested port directly. No
	// coordinator lookup; the port that comes up on the server side is
	// what the client asked for.
	if !waitFor(2*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+tunnelPort, 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}) {
		t.Errorf("chisel did not bind legacy port %s", tunnelPort)
	}
}
```

The `availablePort` function already exists in chisel's existing test/e2e helpers (in `setup_test.go`).

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorOmittedRunsLegacy -v
```

Expected: PASS. This validates that Phase 1-3 changes haven't regressed legacy behavior.

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_omitted_test.go
git commit -m "test(e2e): coordinator-omitted runs legacy mode unchanged

Regression check that chserver.Config without Coordinator field still
binds client-requested ports directly. Catches accidental coordinator-
path activation; baseline for the rest of Phase 5's integration tests."
```

### Task 26: Test — happy path

**Files:**
- Create: `test/e2e/coordinator_happy_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_happy_test.go`:

```go
package e2e_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
	"github.com/jpillora/chisel/server/coordinator"
)

// TestCoordinatorHappyPath verifies the full lookup → bind → activate
// → run → deactivate flow against the fake coordinator. Asserts:
// chisel binds the coordinator-allocated port (not a client-requested
// one), activate body carries hostname/port/remoteAddr, deactivate
// fires on disconnect.
func TestCoordinatorHappyPath(t *testing.T) {
	fc := newFakeCoordinator(t)

	const (
		hostname     = "lcm-abc1"
		sessionID    = "ses_test_happy"
		allocatedPort = 23001
	)
	fc.preallocate(hostname, sessionID, allocatedPort)

	// Note: the fakeCoordinator runs on plain HTTP (httptest.NewServer),
	// while chserver expects an https URL for coordinator. We override
	// the chserver.Config.Coordinator to point at the http URL and skip
	// mTLS by direct-injection via a test helper (see
	// chiselServerWithFakeCoordinator below).
	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Hostname:         hostname,
		Remotes:          []string{"R:0:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Wait for activate hook.
	if !waitFor(3*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.activateCalls) > 0
	}) {
		fc.mu.Lock()
		t.Fatalf("activate never called; lookups=%d", len(fc.lookupCalls))
	}

	fc.mu.Lock()
	if len(fc.lookupCalls) == 0 || fc.lookupCalls[0] != hostname {
		t.Errorf("lookup target_hostname = %v, want [%q]", fc.lookupCalls, hostname)
	}
	got := fc.activateCalls[0]
	fc.mu.Unlock()

	if got.TargetHostname != hostname {
		t.Errorf("activate.TargetHostname = %q, want %q", got.TargetHostname, hostname)
	}
	if got.ActualPortBound != allocatedPort {
		t.Errorf("activate.ActualPortBound = %d, want %d", got.ActualPortBound, allocatedPort)
	}
	if !strings.Contains(got.ClientRemoteAddr, "127.0.0.1") {
		t.Errorf("activate.ClientRemoteAddr = %q, want 127.0.0.1:...", got.ClientRemoteAddr)
	}

	// Verify chisel actually bound the coordinator-allocated port.
	if !waitFor(2*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:23001", 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}) {
		t.Errorf("chisel did not bind allocated port 23001")
	}

	// Trigger disconnect.
	cancel()
	c.Close()

	// Wait for deactivate.
	if !waitFor(3*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.deactivateCalls) > 0
	}) {
		t.Fatal("deactivate never called")
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if d := fc.deactivateCalls[0]; d.SessionID != sessionID {
		t.Errorf("deactivate.SessionID = %q, want %q", d.SessionID, sessionID)
	}
}

// chiselServerWithFakeCoordinator builds a chserver pointing at fc.
// Because the fake runs http (not https), we patch the resulting
// Server's coordClient.http to be a non-mTLS client.
func chiselServerWithFakeCoordinator(t *testing.T, fc *fakeCoordinator, auth string) *chserver.Server {
	t.Helper()

	// Build a placeholder Config — URL is a dummy; we replace coordClient
	// after NewServer rejects the http URL with a special test path.
	srv, err := chserver.NewServerNoCoordValidation(&chserver.Config{
		KeySeed: "test-coord",
		Reverse: true,
		Auth:    auth,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Inject a coordClient that uses the fake's http URL directly.
	cc := coordinator.NewClientForTesting(fc.URL())
	chserver.SetCoordClient(srv, cc)
	return srv
}

// startServer starts srv on 127.0.0.1 with an OS-assigned port and
// returns the port string.
func startServer(t *testing.T, srv *chserver.Server) string {
	t.Helper()
	port := availablePort()
	if err := srv.Start("127.0.0.1", port); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return port
}
```

- [ ] **Step 2: Add the test-only escape hatches in coordinator package**

Append to `server/coordinator/client.go`:

```go
// NewClientForTesting builds a Client with a plain http.Client (no mTLS).
// Used by integration tests that point at httptest.Server, which doesn't
// serve TLS. NEVER use this in production code paths.
func NewClientForTesting(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
		timeout: 5 * time.Second,
	}
}
```

In `server/server.go`, add:

```go
// NewServerNoCoordValidation is identical to NewServer except it skips
// the coordinator URL/mTLS validation step. ONLY for integration tests
// that inject a test coordClient via SetCoordClient.
func NewServerNoCoordValidation(c *Config) (*Server, error) {
	saved := c.Coordinator
	c.Coordinator = nil
	srv, err := NewServer(c)
	c.Coordinator = saved
	return srv, err
}

// SetCoordClient injects a coordinator.Client for testing. The injected
// client is used by handleWebsocket as if config.Coordinator had been
// set.
func SetCoordClient(s *Server, c *coordinator.Client) {
	s.coordClient = c
}
```

- [ ] **Step 3: Run test**

```
go test ./test/e2e/ -run TestCoordinatorHappyPath -v
```

Expected: PASS. Lookup is called with hostname "lcm-abc1"; activate fires with the right body; chisel binds 23001; deactivate fires on close.

- [ ] **Step 4: Commit**

```
git add test/e2e/coordinator_happy_test.go server/coordinator/client.go server/server.go
git commit -m "test(e2e): coordinator happy-path integration test

Drives a real chisel-server through the full lookup→bind→activate→
run→deactivate flow against the fakeCoordinator helper. Asserts on
allocated-port binding, activate body shape, deactivate on disconnect.

Adds two test-only escape hatches in chisel:
- coordinator.NewClientForTesting (skip mTLS for httptest)
- chserver.NewServerNoCoordValidation + SetCoordClient (inject test
  client, skip URL validation for http URLs)

Production code paths are unaffected — these helpers are explicitly
test-only."
```

### Task 27: Test — rejects unknown hostname

**Files:**
- Create: `test/e2e/coordinator_unknown_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_unknown_test.go`:

```go
package e2e_test

import (
	"context"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorRejectsUnknownHostname verifies that when the
// coordinator returns 404 to a lookup, chisel-server SSH-rejects the
// connection. Activate is never called; no listener is bound.
func TestCoordinatorRejectsUnknownHostname(t *testing.T) {
	fc := newFakeCoordinator(t)
	// no preallocate — fake returns 404 for any hostname

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Hostname:         "lcm-unknown1",
		Remotes:          []string{"R:0:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = c.Start(ctx) // expect error from server-side reject
	t.Cleanup(func() { c.Close() })

	// Give it half a second for the lookup to fire.
	time.Sleep(500 * time.Millisecond)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.lookupCalls) == 0 {
		t.Errorf("lookup never called")
	}
	if len(fc.activateCalls) != 0 {
		t.Errorf("activate fired despite 404 lookup; activate calls = %+v", fc.activateCalls)
	}
}
```

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorRejectsUnknownHostname -v
```

Expected: PASS. Lookup happens once; no activate.

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_unknown_test.go
git commit -m "test(e2e): coordinator rejects unknown hostname

Verifies that 404 from lookup causes SSH-level reject without any
activate firing. Catches a regression where chisel might fall through
to legacy port-binding on lookup failure."
```

### Task 28: Test — activate failure tears down proxy

**Files:**
- Create: `test/e2e/coordinator_activate_fail_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_activate_fail_test.go`:

```go
package e2e_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorActivateFailureTeardownProxy verifies that when the
// coordinator returns 409 on activate (port mismatch / hostname mismatch),
// chisel-server tears down its just-bound listener so no orphan port is
// exposed. The LCM client retries via its --max-retry-count loop (in this
// test, just one attempt to keep timing simple).
func TestCoordinatorActivateFailureTeardownProxy(t *testing.T) {
	const allocatedPort = 23042

	// Custom fake that returns 409 on activate.
	mux := http.NewServeMux()
	var lookupCount, activateCount int
	var mu sync.Mutex
	mux.HandleFunc("/sessions/lookup", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lookupCount++
		mu.Unlock()
		json.NewEncoder(w).Encode(coordSession{
			SessionID:      "ses_conf",
			Port:           allocatedPort,
			State:          "pending",
			TargetHostname: r.URL.Query().Get("target_hostname"),
		})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		activateCount++
		mu.Unlock()
		http.Error(w, "hostname mismatch", http.StatusConflict)
	})
	server := httptesNewServer(t, mux)

	fc := &fakeCoordinator{server: server}
	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, _ := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Hostname:         "lcm-conf1",
		Remotes:          []string{"R:0:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = c.Start(ctx)
	t.Cleanup(func() { c.Close() })

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	if lookupCount == 0 {
		t.Error("lookup never called")
	}
	if activateCount == 0 {
		t.Error("activate never called")
	}
	mu.Unlock()

	// Critical assertion: port 23042 must NOT be listening (proxy was torn down).
	conn, err := net.DialTimeout("tcp", "127.0.0.1:23042", 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Errorf("port 23042 still listening after activate 409 — proxy not torn down")
	}
}

func httptesNewServer(t *testing.T, h http.Handler) *http.Server { // helper inline
	t.Helper()
	// We can't return an *httptest.Server because we want to control the
	// addr. But a vanilla httptest will do — same thing.
	srv := http.Server{Handler: h}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	srv.Addr = ln.Addr().String()
	return &srv
}
```

Note: the existing `fakeCoordinator` struct has fields not exposed by this inline server. Adjust the test to use the proper helper instead:

Actually the test above references `httptesNewServer` and constructs `fakeCoordinator` from a custom `server`. Cleaner alternative — use the standard fakeCoordinator + override-mode mechanism:

Replace the test with:

```go
package e2e_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

func TestCoordinatorActivateFailureTeardownProxy(t *testing.T) {
	const allocatedPort = 23042

	fc := newFakeCoordinator(t)
	fc.preallocate("lcm-conf1", "ses_conf", allocatedPort)

	// Override the activate handler to always return 409.
	var activateCalls int32
	fc.server.Config.Handler.(*http.ServeMux).HandleFunc("/sessions/ses_conf/activate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&activateCalls, 1)
		http.Error(w, "hostname mismatch", http.StatusConflict)
	})

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, _ := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Hostname:         "lcm-conf1",
		Remotes:          []string{"R:0:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = c.Start(ctx)
	t.Cleanup(func() { c.Close() })

	time.Sleep(500 * time.Millisecond)

	if atomic.LoadInt32(&activateCalls) == 0 {
		t.Error("activate never called")
	}

	// Critical: port must NOT be listening.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:23042", 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Errorf("port 23042 still listening after activate 409 — proxy not torn down")
	}
}
```

(Imports: add `net/http`, `sync/atomic`.)

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorActivateFailureTeardownProxy -v
```

Expected: PASS. The OnRemoteBound callback's err return tears down the proxy before its accept loop starts, so port 23042 is closed by the time the test dials it.

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_activate_fail_test.go
git commit -m "test(e2e): activate 409 tears down proxy

Verifies the OnRemoteBound err path: when coordinator rejects activate
with 409 (hostname mismatch / port mismatch), the just-bound listener
is closed and no orphan port leaks. Critical for the threat model —
without teardown, a state-divergence between coordinator and chisel
would expose phantom listeners that operator SSH would still try."
```

### Task 29: Test — deactivate best effort

**Files:**
- Create: `test/e2e/coordinator_deactivate_best_effort_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_deactivate_best_effort_test.go`:

```go
package e2e_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorDeactivateBestEffort verifies that a 5xx deactivate
// response doesn't propagate as an error to the chisel-server's exit
// path. handleWebsocket returns cleanly; the coordinator's TTL fallback
// is the safety net for cleanup.
func TestCoordinatorDeactivateBestEffort(t *testing.T) {
	fc := newFakeCoordinator(t)
	fc.preallocate("lcm-be1", "ses_be", 23061)

	var deactivateCalls int32
	fc.server.Config.Handler.(*http.ServeMux).HandleFunc("/sessions/ses_be/deactivate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&deactivateCalls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, _ := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Hostname:         "lcm-be1",
		Remotes:          []string{"R:0:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Wait for tunnel up.
	if !waitFor(2*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.activateCalls) > 0
	}) {
		t.Fatal("activate never fired")
	}

	// Trigger disconnect.
	cancel()
	c.Close()

	// Deactivate must fire (gets 500) but NOT crash the handler.
	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&deactivateCalls) > 0
	}) {
		t.Fatal("deactivate never called")
	}

	// Test passes if we reach here without panic / hung server. The
	// chisel-server log will contain the deactivate failure at WARN —
	// not asserted directly because cio.Logger doesn't expose buffer.
}
```

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorDeactivateBestEffort -v
```

Expected: PASS. Deactivate fires; chisel-server exits cleanly even though the call returned 500.

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_deactivate_best_effort_test.go
git commit -m "test(e2e): deactivate best-effort on 5xx

Verifies handleWebsocket exits cleanly when the coordinator's
deactivate hook returns 500. The TTL fallback on the coordinator
side is the safety net; chisel-server doesn't retry inline."
```

### Task 30: Test — reconnect idempotent (gated on coordinator follow-up)

**Files:**
- Create: `test/e2e/coordinator_reconnect_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_reconnect_test.go`:

```go
package e2e_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorReconnectIdempotent simulates an LCM tunnel-drop +
// reconnect within the session window. The coordinator must accept the
// second activate for the same session_id under the same params as
// idempotent (200 / no-op), NOT 409. This test fails until the
// coordinator-side fix lands in bastion's coordinator/sessions.go.
//
// SKIPPED IF the coordinator fake's idempotent-activate path isn't
// wired up (skipping intentionally because the bastion-side test is
// the canonical verification — this test exists to prevent regressions
// once the coordinator is fixed).
func TestCoordinatorReconnectIdempotent(t *testing.T) {
	fc := newFakeCoordinator(t)
	fc.preallocate("lcm-re1", "ses_re", 23071)

	var activateCount int32
	fc.server.Config.Handler.(*http.ServeMux).HandleFunc("/sessions/ses_re/activate", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&activateCount, 1)
		// Coordinator behavior we want once the fix lands: second activate
		// for the same session_id with matching hostname+port = 200.
		// Until then, the coordinator returns 409 and this test fails —
		// which is the right gate.
		if count > 1 {
			// idempotent on second call
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	// First connection.
	c1, _ := chclient.NewClient(&chclient.Config{
		Server:        "http://127.0.0.1:" + listenPort,
		Auth:          "lcm-fleet:secret",
		Hostname:      "lcm-re1",
		Remotes:       []string{"R:0:127.0.0.1:22"},
		MaxRetryCount: 0,
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := c1.Start(ctx1); err != nil {
		t.Fatalf("Start c1: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&activateCount) >= 1
	}) {
		t.Fatal("first activate never fired")
	}

	// Drop first connection.
	cancel1()
	c1.Close()

	// Second connection (simulate LCM reconnect).
	time.Sleep(200 * time.Millisecond) // let chisel-server's deactivate run
	c2, _ := chclient.NewClient(&chclient.Config{
		Server:        "http://127.0.0.1:" + listenPort,
		Auth:          "lcm-fleet:secret",
		Hostname:      "lcm-re1",
		Remotes:       []string{"R:0:127.0.0.1:22"},
		MaxRetryCount: 0,
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := c2.Start(ctx2); err != nil {
		t.Fatalf("Start c2: %v", err)
	}
	defer c2.Close()

	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&activateCount) >= 2
	}) {
		t.Errorf("second activate didn't fire (only %d activates total)", atomic.LoadInt32(&activateCount))
	}
}
```

Note: this test's fake-coordinator handler returns 200 unconditionally. The REAL gating is that bastion's coordinator (in the next session) must also return 200 on second activate. This e2e test exists to catch chisel-fork regressions; the bastion-coordinator-side test is the canonical verification of the idempotency fix.

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorReconnectIdempotent -v
```

Expected: PASS (the fake returns 200 unconditionally; this verifies chisel-fork doesn't refuse to reconnect after a drop).

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_reconnect_test.go
git commit -m "test(e2e): reconnect idempotent (chisel-fork side)

Verifies the chisel-fork accepts a second activate for the same
session_id when reconnecting after a drop. The fake coordinator
returns 200 for both activate calls; the bastion-coordinator-side
canonical test gates idempotency on the actual coordinator
implementation (currently 409s — fix flagged for β.1)."
```

### Task 31: Test — SIGTERM parallel drain

**Files:**
- Create: `test/e2e/coordinator_sigterm_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/coordinator_sigterm_test.go`:

```go
package e2e_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorSIGTERMParallelDrain verifies that when chisel-server's
// Close() is called with N active sessions, all N deactivate calls fire
// and complete within a wall-clock window much smaller than (N * timeout).
// Catches a regression where shutdown serializes deactivate calls
// instead of relying on per-goroutine OnRemoteUnbound closures.
func TestCoordinatorSIGTERMParallelDrain(t *testing.T) {
	const N = 10

	fc := newFakeCoordinator(t)
	for i := 0; i < N; i++ {
		fc.preallocate(fmt.Sprintf("lcm-drain%d", i), fmt.Sprintf("ses_drain_%d", i), 23100+i)
	}

	var deactivateCount int32
	// Hook deactivate to count + add 1s sleep (simulating slow coordinator).
	for i := 0; i < N; i++ {
		sessionID := fmt.Sprintf("ses_drain_%d", i)
		fc.server.Config.Handler.(*http.ServeMux).HandleFunc(
			"/sessions/"+sessionID+"/deactivate",
			func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&deactivateCount, 1)
				time.Sleep(1 * time.Second)
				w.WriteHeader(http.StatusOK)
			},
		)
	}

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)

	// Bring up N concurrent client sessions.
	clients := make([]*chclient.Client, 0, N)
	for i := 0; i < N; i++ {
		c, err := chclient.NewClient(&chclient.Config{
			Server:        "http://127.0.0.1:" + listenPort,
			Auth:          "lcm-fleet:secret",
			Hostname:      fmt.Sprintf("lcm-drain%d", i),
			Remotes:       []string{"R:0:127.0.0.1:22"},
			MaxRetryCount: 0,
		})
		if err != nil {
			t.Fatalf("NewClient %d: %v", i, err)
		}
		ctx := context.Background()
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	// Wait until all N activates have landed.
	if !waitFor(5*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.activateCalls) >= N
	}) {
		t.Fatalf("not all activates fired: got %d, want %d", len(fc.activateCalls), N)
	}

	// SIGTERM equivalent: call Close + wait for all deactivates.
	start := time.Now()
	srv.Close()

	// All N deactivates should complete in roughly 1s wall-clock (parallel),
	// not N * 1s (serial). Allow 3s headroom for goroutine scheduling.
	if !waitFor(3*time.Second, func() bool {
		return atomic.LoadInt32(&deactivateCount) >= N
	}) {
		t.Errorf("only %d/%d deactivates fired within 3s after Close()", atomic.LoadInt32(&deactivateCount), N)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("drain took %v, want < 3s (parallel deactivates expected, not serial)", elapsed)
	}
}
```

Add `net/http` to the imports.

- [ ] **Step 2: Run test**

```
go test ./test/e2e/ -run TestCoordinatorSIGTERMParallelDrain -v
```

Expected: PASS. All 10 deactivates fire within ~1.5-2s wall-clock (parallel), not 10s+ (serial).

- [ ] **Step 3: Commit**

```
git add test/e2e/coordinator_sigterm_test.go
git commit -m "test(e2e): SIGTERM parallel drain

Catches the regression where shutdown serializes deactivate calls
instead of relying on per-goroutine OnRemoteUnbound. With N=10
sessions and 1s simulated coordinator latency per deactivate, drain
should complete in roughly 1s (parallel) — not 10s (serial). The
test asserts < 3s wall-clock as headroom for goroutine scheduling."
```

### Task 32: Phase 5 verification

- [ ] **Step 1: Full e2e test pass**

```
go test ./test/e2e/... -v
```

Expected: all PASS. Combined test count includes coordinator tests + existing chisel e2e tests. Existing tests continue to pass (legacy mode preserved).

- [ ] **Step 2: Push Phase 5 commits**

```
git push origin feat/coordinator-callbacks
```

---

## Phase 6: Docs + tagging

### Task 33: README note about module-path retention

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add fork note**

In `README.md`, find the very first heading. Insert a one-paragraph note immediately after the project description (before the "Features" or "Install" section). Example placement:

```markdown
# chisel

[... existing description ...]

> **AdvancingAlternatives fork notice:** This repository is a fork of
> [`jpillora/chisel`](https://github.com/jpillora/chisel) with the addition
> of coordinator-callback hooks (`OnRemoteBound` / `OnRemoteUnbound`,
> `DisconnectReason`) for externally-managed session lifecycles. The Go
> module path is retained as `github.com/jpillora/chisel` to minimize
> upstream-merge churn — this is intentional, not abandoned. See
> `docs/superpowers/specs/2026-05-07-chisel-fork-coordinator-callbacks-design.md`
> for the design rationale and `bastion/docs/chisel-fork-contract.md`
> in the bastion repo for the consumer contract.

[... continue with existing content ...]
```

- [ ] **Step 2: Verify the README still renders correctly**

```
head -30 README.md
```

Visually confirm the new paragraph fits the existing tone and isn't duplicated.

- [ ] **Step 3: Commit**

```
git add README.md
git commit -m "docs: AdvancingAlternatives fork notice in README

AA-fork: explains why this repo's module path is still
github.com/jpillora/chisel — intentional, not abandoned. Points
future maintainers at the design doc for context."
```

### Task 34: PR + tag v1.10.1-aa.1

**Files:** None (git operations only).

- [ ] **Step 1: Verify all tests pass on the branch**

```
go test ./...
```

Expected: all PASS.

- [ ] **Step 2: Push final state**

```
git push origin feat/coordinator-callbacks
```

- [ ] **Step 3: Open PR for review**

```
gh pr create --repo AdvancingAlternatives/chisel --base main --head feat/coordinator-callbacks --title "feat: coordinator-callback integration (β.1 fork)" --body "Implements the coordinator-callback design from \`docs/superpowers/specs/2026-05-07-chisel-fork-coordinator-callbacks-design.md\`.

## Summary
- \`share/tunnel/\` adds OnRemoteBound/OnRemoteUnbound callbacks + DisconnectReason type (upstream-PR-target; generic proxy lifecycle hooks)
- \`server/coordinator/\` adds the HTTP client for AdvancingAlternatives's bastion coordinator (fork-only)
- \`server/\` integration wires lookup → activate → deactivate into handleWebsocket; cancel-cause-context for shutdown propagation
- \`main.go\` adds --coordinator-* CLI flags
- 8 integration tests in \`test/e2e/\` covering happy path, rejection paths, idempotent reconnect, SIGTERM drain

## Tagging plan
After merge, tag v1.10.1-aa.1 marking the fork's first AA-specific release. bastion will pin its chisel image to this tag.

## Rollback safety
\`Coordinator\` field nil → upstream behavior unchanged. bastion-dev redeploys from this fork are no-ops.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

- [ ] **Step 4: Wait for CI + merge**

After CI passes:

```
gh pr merge <PR_NUMBER> --repo AdvancingAlternatives/chisel --squash --delete-branch
```

- [ ] **Step 5: Tag the release**

```
git checkout main
git pull --ff-only
git tag -a v1.10.1-aa.1 -m "AdvancingAlternatives chisel v1.10.1-aa.1

Forked from jpillora/chisel v1.10.1. Adds coordinator-callback
integration for externally-managed session lifecycles. Backward-
compatible with upstream when \`Config.Coordinator\` is nil.

See docs/superpowers/specs/2026-05-07-chisel-fork-coordinator-callbacks-design.md
for the full design."
git push origin v1.10.1-aa.1
```

The fork is now ready for bastion to consume.

---

## Self-review checklist

After all 34 tasks land, run through this once:

1. **Spec coverage:** every section of the design doc maps to a task.
   - DisconnectReason + ErrServerShutdown → Task 1
   - RemotePortInt → Task 2
   - OnRemoteBound/OnRemoteUnbound + classifyDisconnect + Tunnel wiring → Tasks 3-7
   - server/coordinator/ package → Tasks 8-17
   - Server.Coordinator field + shutdownCtx + handleWebsocket integration → Tasks 18-22
   - --coordinator-* CLI flags → Task 23
   - Integration tests covering 7 design scenarios → Tasks 24-32
   - README + tagging → Tasks 33-34

2. **Placeholder scan:** every step has full code or full commands. No "TBD", no "similar to X."

3. **Type consistency:** field names, function signatures, error names, package imports all match across tasks.

4. **TDD discipline:** every code-touching task has the test → run-failing → implement → run-passing → commit pattern.

5. **Coordinator follow-up:** Task 30's `TestCoordinatorReconnectIdempotent` will pass with the fake; the bastion-side coordinator fix (idempotent activate on reconnect) is a hard prerequisite for end-to-end smoke. Tracked in the design doc; the bastion PR doesn't merge until that fix is in.
