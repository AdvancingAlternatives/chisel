# Chisel-fork: coordinator-callback design

**Repo:** `AdvancingAlternatives/chisel` (fork of `jpillora/chisel`)
**Date:** 2026-05-07
**Status:** Approved by Nathan, ready for implementation
**Related docs:**
- `bastion/docs/chisel-fork-contract.md` — the contract this fork honors
- `bastion/docs/chisel-fork-exploratory-findings.md` — pre-design code skim of upstream chisel

## Why this fork exists

Bastion's β.1 architecture (`AdvancingAlternatives/bastion@v0.4.0-rc.0`) requires chisel-server to consult an external coordinator at every reverse-tunnel connect:

- The LCM fleet uses a single shared `lcm-fleet:<secret>` chisel auth credential — necessary but not sufficient. Every tunnel also needs a pre-allocated coordinator-side session for the connecting hostname.
- Upstream chisel doesn't expose the lifecycle hooks needed (between auth-acceptance and port-bind, between port-unbind and goroutine exit).
- Vendoring upstream and wrapping wouldn't avoid modification — those hooks aren't in the public API.

This fork adds the hooks.

## Top-level decisions (settled in brainstorming)

| # | Decision | Rationale |
|---|---|---|
| 1 | Hook injection via `OnRemoteBound` / `OnRemoteUnbound` callbacks on `tunnel.Config` | Smallest invasive surface; share/tunnel stays generic. Upstream-PR-friendly framing: "complete proxy lifecycle observability." |
| 2 | Coordinator unreachable → fail closed (reject WS) | Threat model says fleet credential alone insufficient; caching softens this. LCM retry handles transient outages transparently. |
| 3 | `tunnel.DisconnectReason` Go type + 3 named consts | Catches typo drift across version bumps; reads as real chisel API. |
| 4 | Single `--coordinator-url` flag (not 3 per-endpoint URLs) | Path conventions are owned by us at both ends; one flag = one place to misconfigure. |
| 5 | Module path stays `github.com/jpillora/chisel`; new code in `server/coordinator/` | Smallest fork-vs-upstream diff. `share/` is conventionally cross-client/server; coordinator client is server-only. |

## Architecture

When `--coordinator-url` is set, chisel-server's reverse-tunnel handshake gets three new steps wrapping the existing flow:

```
LCM connect  → auth (existing) → DecodeConfig (existing)
             → [NEW] coordinator.Lookup(req.Host)
             → tunnel.New (existing) with OnRemoteBound/OnRemoteUnbound (NEW)
             → tunnel.BindRemotes (existing)
                  → proxy.listen() (existing)
                  → [NEW] OnRemoteBound: coordinator.Activate
                  → proxy.Run accept loop (existing)
             → tunnel teardown (existing)
                  → [NEW] OnRemoteUnbound: coordinator.Deactivate
```

When `--coordinator-url` is unset, the fork is bit-for-bit upstream behavior. Bastion-dev redeploys from the fork are no-ops; the gateway-agent flow keeps working unchanged.

**Modification surfaces:**
- `share/tunnel/tunnel.go` — adds two callbacks + a type. Generic; the eventual upstream PR target.
- `share/tunnel/tunnel_in_proxy.go` — wires the callbacks into `Proxy.listen` and `Proxy.Run`.
- `server/coordinator/` — new package. Fork-only. HTTP client + path constants + types.
- `server/server.go` — `Config` gets a `Coordinator *coordinator.Config` field; `NewServer` builds the client; URL validated at parse time.
- `server/server_handler.go` — `handleWebsocket` adds the lookup → port-override → activate flow when coordinator is configured.
- `main.go` — new flags wiring through to the config struct.

Total ~300-400 LOC + tests. Every fork-introduced change tagged `// AA-fork:` for upstream-merge tractability.

## Components

### `share/tunnel/` — upstream-PR target

**`DisconnectReason` type (new file or in `tunnel.go`)**

```go
// DisconnectReason carries the cause of a proxy unbind. Best-effort:
// chisel-server distinguishes only the three layers below; finer-grained
// causes (idle, max_duration) are LCM-state knowledge and get attributed
// at the consumer (e.g., coordinator) by log correlation.
type DisconnectReason string

const (
    DisconnectClient         DisconnectReason = "client_disconnect"
    DisconnectConnectionLost DisconnectReason = "connection_lost"
    DisconnectServerShutdown DisconnectReason = "server_shutdown"
)
```

**`tunnel.Config` additions**

```go
type Config struct {
    *cio.Logger
    Inbound   bool
    Outbound  bool
    Socks     bool
    KeepAlive time.Duration
    ACL       func(addr string) bool

    // AA-fork: lifecycle hooks for externally-managed sessions.
    //
    // OnRemoteBound fires after a Proxy's net.Listener is bound and
    // before its accept loop starts. Returning a non-nil error tears
    // down THIS proxy only (not the whole tunnel) and propagates the
    // error back to BindRemotes' caller. Use case: external session
    // manager wants to validate the binding before exposing the port.
    OnRemoteBound func(ctx context.Context, remote *settings.Remote, listener net.Listener) error

    // OnRemoteUnbound fires when a Proxy's accept loop exits, regardless
    // of cause. Best-effort: callers should treat reason as advisory,
    // not authoritative — the underlying ssh.Conn close cause isn't
    // always knowable at this layer. Use case: external session manager
    // wants to mark the session closed.
    OnRemoteUnbound func(remote *settings.Remote, reason DisconnectReason)
}
```

**`BindRemotes` modifications** (`tunnel.go`)

After `proxies[i] = p` in the build loop, before the run-goroutine spawn, call `OnRemoteBound` if set. Returning err from the callback removes that single proxy from the slice and propagates up. Existing partial-bind cleanup handles the "torn-down proxies that already bound" case.

**`Proxy.Run` modifications** (`tunnel_in_proxy.go`)

After `Run` exits, call `OnRemoteUnbound` if set. The reason is derived **at the callback site** by inspecting the proxy's context via `context.Cause(ctx)` — no setter, no field on Tunnel, no race possible. Classification:

```go
func classifyDisconnect(ctx context.Context) DisconnectReason {
    cause := context.Cause(ctx)
    switch {
    case errors.Is(cause, ErrServerShutdown):
        return DisconnectServerShutdown
    case cause == nil || errors.Is(cause, context.Canceled) || isEOF(cause):
        return DisconnectClient
    default:
        return DisconnectConnectionLost
    }
}
```

This works because both close paths flow into the proxy's ctx as cancellation causes:
- **Server shutdown:** `Server.Close()` calls `shutdownCancel(ErrServerShutdown)` on the server's root cancel-cause-context, which propagates to every in-flight handler's derived context. Cause = `ErrServerShutdown`.
- **SSH conn close:** chisel's existing `errgroup.WithContext` wraps `BindSSH`'s return error as the group's cancel cause (errgroup uses `context.WithCancelCause` since `golang.org/x/sync v0.6+`). `BindSSH` returns `c.Wait()`'s err — `nil` for clean close, EOF for normal end-of-stream, other error for abnormal close.
- **No setter, no race:** `context.Cause` reads the same atomic value the cancel function writes. The classifier runs at proxy.Run exit time, by which point the cause is already set by whichever cancel-path fired.

**New shared sentinel** in `share/tunnel/tunnel.go`:

```go
// AA-fork: cancel cause used by chisel-server's Close() so OnRemoteUnbound
// callbacks can distinguish server-initiated shutdown from client-initiated
// disconnect via context.Cause(ctx).
var ErrServerShutdown = errors.New("chisel: server shutdown")
```

**Helpers added to `share/settings/remote.go`**:

```go
// RemotePortInt returns RemotePort parsed as int, or 0 on parse failure.
// Convenience for callers that need the bound port as a number rather
// than the wire-format string.
func (r *Remote) RemotePortInt() int
```

The OnRemoteBound/OnRemoteUnbound callbacks carry the bound port to coordinator.Activate/Deactivate, which expects int.

### `server/coordinator/` — new package

```
server/coordinator/
  client.go          // Client struct + Lookup, Activate, Deactivate methods
  paths.go           // PathLookup, PathActivate, PathDeactivate constants
  types.go           // Session, ActivateRequest, DeactivateRequest
  config.go          // Config struct + ParseURL helper + LoadMTLS helper
  errors.go          // sentinel errors: ErrNotFound, ErrConflict, ErrAuth, ErrTransient
  client_test.go     // unit tests with httptest.NewServer
```

**`paths.go`** — single source of truth, mirrors `bastion/coordinator/api.go`'s routes:

```go
const (
    PathLookup     = "/sessions/lookup"
    PathActivate   = "/sessions/%s/activate"   // %s = sessionID
    PathDeactivate = "/sessions/%s/deactivate" // %s = sessionID
)
```

**`config.go`**:

```go
type Config struct {
    URL          string        // base URL, e.g. https://coordinator:8443
    MTLSCertFile string        // path to chisel-server.pem
    MTLSKeyFile  string        // path to chisel-server-key.pem
    MTLSCAFile   string        // path to ca.pem (verifies coordinator's server cert)
    Timeout      time.Duration // per-call timeout, default 5*time.Second
}

// ParseURL validates URL is parseable, scheme=https, host non-empty.
// Called at flag-parse time so misconfigs fail bastion-chisel startup
// rather than first-LCM-connection.
func (c *Config) ParseURL() (*url.URL, error)

// LoadMTLS reads cert/key/ca files and returns a *tls.Config wired for
// client mTLS to the coordinator.
func (c *Config) LoadMTLS() (*tls.Config, error)
```

**`client.go`**:

```go
type Client struct {
    baseURL string
    http    *http.Client  // configured with mTLS + Timeout
    timeout time.Duration
}

func New(cfg *Config) (*Client, error)

// Lookup queries coordinator for a pending session matching hostname.
// Returns (nil, ErrNotFound) on 404, (nil, wrapped err) on transport/5xx/auth.
func (c *Client) Lookup(ctx context.Context, hostname string) (*Session, error)

// Activate posts the bound port + remote address for a session,
// transitioning it pending→active on coordinator side.
// Returns nil on 200, ErrConflict on 409, ErrNotFound on 404,
// wrapped err otherwise.
func (c *Client) Activate(ctx context.Context, sessionID, hostname string, port int, remoteAddr string) error

// Deactivate posts the close reason. Best-effort; caller logs on err
// rather than retrying inline.
func (c *Client) Deactivate(ctx context.Context, sessionID, hostname string, port int, reason tunnel.DisconnectReason) error
```

**`errors.go`** — sentinel errors so callers can distinguish failure modes without string-matching:

```go
var (
    ErrNotFound  = errors.New("coordinator: not found")  // HTTP 404
    ErrConflict  = errors.New("coordinator: conflict")   // HTTP 409
    ErrAuth      = errors.New("coordinator: auth failure") // HTTP 401/403 (chisel-server cert bug)
    ErrTransient = errors.New("coordinator: transient")  // HTTP 5xx or transport-level
)

// Errors returned from Client methods wrap one of these so callers can
// errors.Is() against them.
```

### `server/` — modifications

**`Config.Coordinator`** (in `server.go`):

```go
type Config struct {
    // ...existing fields
    Coordinator *coordinator.Config  // nil = legacy mode (upstream behavior)
}
```

**`Server` struct**:

```go
type Server struct {
    // ...existing fields
    coordClient    *coordinator.Client      // nil iff config.Coordinator == nil
    shutdownCtx    context.Context          // cancelled with cause ErrServerShutdown by Close()
    shutdownCancel context.CancelCauseFunc  // tied to shutdownCtx
}
```

In `NewServer`:
```go
ctx, cancel := context.WithCancelCause(context.Background())
server.shutdownCtx = ctx
server.shutdownCancel = cancel
```

In `Server.Close()`:
```go
s.shutdownCancel(tunnel.ErrServerShutdown)
return s.httpServer.Close()
```

In `handleWebsocket`, the per-request context is derived as a child of `s.shutdownCtx` so server shutdown propagates as a cause:
```go
reqCtx, reqCancel := context.WithCancelCause(req.Context())
go func() {
    select {
    case <-s.shutdownCtx.Done():
        reqCancel(context.Cause(s.shutdownCtx))
    case <-reqCtx.Done():
    }
}()
defer reqCancel(nil)

eg, egCtx := errgroup.WithContext(reqCtx)
// ... BindSSH and BindRemotes use egCtx ...
```

The errgroup's `egCtx` inherits cancellation cause from `reqCtx`. When `BindSSH` returns its `c.Wait()` error, errgroup cancels with that error as cause; the proxy.Run goroutine's ctx (a child of egCtx) sees the same cause via `context.Cause`. Server shutdown overrides via the shutdown→reqCtx propagation goroutine.

No `atomic.Bool`, no setter method, no timing race.

**`NewServer`** validates URL at parse time + builds the client:

```go
if c.Coordinator != nil {
    if _, err := c.Coordinator.ParseURL(); err != nil {
        return nil, fmt.Errorf("coordinator URL: %w", err)
    }
    cli, err := coordinator.New(c.Coordinator)
    if err != nil {
        return nil, fmt.Errorf("coordinator client: %w", err)
    }
    server.coordClient = cli
}
```

**`handleWebsocket`** (in `server_handler.go`) — additions roughly here:

```go
// ... existing auth + DecodeConfig ...

// AA-fork: coordinator lookup before tunnel binding.
var (
    sessionID  string
    hostname   = req.Host
    remoteAddr = req.RemoteAddr
)
if s.coordClient != nil {
    sess, err := s.coordClient.Lookup(req.Context(), hostname)
    if err != nil {
        rejectChisel(configReq, l, err) // SSH-level reject + log at appropriate level
        return
    }
    sessionID = sess.SessionID
    if err := overrideReverseRemotePort(c.Remotes, sess.Port); err != nil {
        l.Errorf("override port: %v", err)
        return
    }
}

// Build tunnel config with hooks closing over sessionID + s.coordClient.
tunnelConfig := tunnel.Config{ /* existing */ }
if s.coordClient != nil {
    tunnelConfig.OnRemoteBound = func(ctx context.Context, r *settings.Remote, ln net.Listener) error {
        return s.coordClient.Activate(ctx, sessionID, hostname, r.RemotePortInt(), remoteAddr)
    }
    tunnelConfig.OnRemoteUnbound = func(r *settings.Remote, reason tunnel.DisconnectReason) {
        ctx, cancel := context.WithTimeout(context.Background(), s.config.Coordinator.Timeout)
        defer cancel()
        if err := s.coordClient.Deactivate(ctx, sessionID, hostname, r.RemotePortInt(), reason); err != nil {
            l.Errorf("deactivate failed sessionID=%s hostname=%s port=%d reason=%s err=%v",
                sessionID, hostname, r.RemotePortInt(), reason, err)
        }
    }
}

// ... existing tunnel.New + BindSSH + BindRemotes inside errgroup ...
// (no SetDisconnectReason call needed — proxy.Run's exit reads
// context.Cause(ctx) to classify the reason locally)
```

**Helper: `rejectChisel(req *ssh.Request, l *cio.Logger, err error)`** — small unexported function in `server_handler.go` that:
1. Inspects `err` against the coordinator sentinel errors (`coordinator.ErrAuth`, `ErrTransient`, `ErrConflict`, `ErrNotFound`)
2. Sends an SSH-level rejection via `req.Reply(false, []byte(message))` — the chisel client surfaces the message in its journal so an operator triaging "why isn't this LCM connecting" sees the cause directly
3. Logs at the appropriate level (INFO/WARN/ERROR per the table below)

The SSH connection closes naturally when handleWebsocket returns after this helper. There is no HTTP status to set — the WebSocket upgrade already happened in `handleClientHandler` (status 101) before chisel-server enters the SSH handshake. The "reject" mechanism is the chisel handshake's `r.Reply(false, ...)` path, identical to how upstream chisel rejects on "config invalid" or "ACL access denied."

Maps coordinator errors → SSH reject message strings + log levels:

| Coord error | SSH reject message | Log level |
|---|---|---|
| `ErrNotFound` (lookup) | `no pending session for hostname <X>` | INFO (operator hasn't clicked yet) |
| `ErrAuth` (any call) | `coordinator unreachable` | ERROR (chisel-server cert bug — page ops) |
| `ErrTransient` (any call) | `coordinator unreachable, retry in flight` | WARN (LCM retries) |
| `ErrConflict` (activate) | `state divergence on activate, see coordinator logs` | WARN (teardown + retry converges) |

The coordinator's actual cause stays in chisel-server's logs (with sessionID, hostname, raw err) — the LCM-facing message stays generic so a misbehaving chisel-server cert can't leak Layer-2 detail to LCM clients.

### `main.go` — new flags

```
--coordinator-url            base URL (e.g. https://coord.bastion:8443)
--coordinator-mtls-cert      path to chisel-server.pem
--coordinator-mtls-key       path to chisel-server-key.pem
--coordinator-mtls-ca        path to ca.pem
--coordinator-timeout        per-call timeout (default 5s)
```

When `--coordinator-url` is empty, all coordinator-prefixed flags are ignored and chisel runs in upstream-compatible mode. When set, the cert/key/ca flags are required (validated at startup).

## Data flow (happy path)

```
LCM client
   │ chisel client --hostname=lcm-a57d --auth=$AUTH https://bastion:8443 R:0:127.0.0.1:22
   ▼
chisel-server.handleWebsocket
   │
   ├─ ssh.NewServerConn (auth lcm-fleet:secret OK)
   ├─ DecodeConfig → c.Remotes = [{Reverse=true, RemotePort=0, LocalHost=127.0.0.1, LocalPort=22}]
   │
   ├─ [AA-fork] coordinator.Client.Lookup(ctx, "lcm-a57d")
   │     GET https://coordinator:8443/sessions/lookup?target_hostname=lcm-a57d
   │     ← 200 {session_id="ses_abc", port=22099, state="pending", target_hostname="lcm-a57d"}
   │
   ├─ [AA-fork] override c.Remotes[0].RemotePort = "22099"
   │
   ├─ tunnel.New with OnRemoteBound + OnRemoteUnbound capturing
   │     (sessionID="ses_abc", hostname="lcm-a57d", remoteAddr=req.RemoteAddr, coordClient)
   │
   ├─ BindRemotes (errgroup goroutine)
   │  └─ NewProxy
   │     └─ proxy.listen() binds 0.0.0.0:22099
   │  └─ [AA-fork] OnRemoteBound(ctx, remote, listener) fires
   │     └─ coordinator.Client.Activate(ctx, "ses_abc", "lcm-a57d", 22099, "10.5.6.7:55432")
   │        POST https://coordinator:8443/sessions/ses_abc/activate
   │        body: {target_hostname: "lcm-a57d", actual_port_bound: 22099, client_remote_addr: "10.5.6.7:55432"}
   │        ← 200 {state: "active", ...}
   │     └─ returns nil → BindRemotes proceeds to spawn proxy.Run
   │
   ▼ tunnel running, operator can SSH through bastion:2222 → port-forward 22099 → LCM:22
   │
   │ ... session lifetime ...
   │
   ├─ ssh.Conn closes (any cause)
   │  └─ proxy.Run returns
   │  └─ [AA-fork] OnRemoteUnbound(remote, reason) fires
   │     └─ coordinator.Client.Deactivate(ctx, "ses_abc", "lcm-a57d", 22099, reason)
   │        POST https://coordinator:8443/sessions/ses_abc/deactivate
   │        body: {reason: "client_disconnect"}
   │        ← 204 No Content
   │
   └─ handleWebsocket exits
```

## Error handling

### Per-call mapping

The coordinator lookup happens AFTER WebSocket upgrade + AFTER chisel SSH-layer auth + AFTER DecodeConfig. There's no HTTP status code to return at that stage — the WS upgrade is already 101 Switching Protocols and the connection has moved to chisel's SSH-over-WebSocket protocol. Failures are surfaced as **SSH-level rejection** via the existing `r.Reply(false, []byte(msg))` path (same mechanism chisel uses today for "config invalid" / "access denied" errors), followed by closing the SSH connection. The chisel client's `--max-retry-count=-1` retry loop kicks in on the next dial.

Inside chisel-server:

| Call | 200 | 404 | 409 | 401/403 | 5xx | Transport |
|---|---|---|---|---|---|---|
| Lookup | proceed | SSH reject "no pending session", INFO log | n/a | SSH reject "coordinator unreachable", ERROR log (cert bug) | SSH reject "coordinator unreachable", WARN log | SSH reject "coordinator unreachable", WARN log |
| Activate | proceed | tear down proxy + SSH reject "session gone", WARN | tear down + SSH reject "state divergence", WARN | tear down + SSH reject "coordinator unreachable", ERROR | tear down + SSH reject "coordinator unreachable", WARN | tear down + SSH reject, WARN |
| Deactivate | done | log + done (stale state) | log + done (already closed) | log + done at ERROR (cert bug) | log + done at WARN (TTL fallback handles) | log + done at WARN |

**SSH reject message strings are operator-facing.** The chisel client surfaces them in its journal so an operator triaging "why isn't this LCM connecting" can see the cause without cross-referencing two log streams. Examples:
- `chisel: server rejected: no pending session for hostname lcm-a57d` — operator hasn't clicked "Open SSH" in AS2 yet
- `chisel: server rejected: coordinator unreachable, retry in flight` — transient
- `chisel: server rejected: state divergence on activate, see coordinator logs` — needs investigation

**Why coordinator-side 401/403 doesn't propagate to the LCM as auth failure:**
LCM's `--auth` is checked at `ssh.NewServerConn`, BEFORE any coordinator call. By the time we're in lookup/activate, LCM auth is known good. Any coordinator-side 401/403 is a Layer-2 issue (chisel-server's mTLS cert to coordinator is misconfigured) — surfacing this as auth-failure to the LCM would mislead it. Generic "coordinator unreachable" is honest: from the LCM's perspective, the coordinator effectively *is* unreachable, even though the underlying cause is a cert bug. Chisel-server logs the actual cause at ERROR for ops alerting.

**Why activate 409 tears down rather than logs-and-proceeds:**
Coordinator is the source of truth for session state. If chisel proceeds despite a 409, two things go wrong:
1. bastion-sshd's Match rule (which consults coordinator) sees the session as inactive → operator SSH gets rejected anyway.
2. Chisel and coordinator state diverge until eventual TTL reaping.

Tearing down + 503 + LCM retry → next lookup sees the fresh state → convergence within seconds.

### SIGTERM drain

`http.Server.Shutdown()` cancels each in-flight request's context. Each `handleWebsocket` goroutine's `eg.Wait()` returns; `OnRemoteUnbound` fires from each goroutine in parallel. Total wall-clock bounded by max-single-call ≈ `coordinator.Timeout` = 5s.

`bastion/docker-compose.yml` will set `stop_grace_period: 30s` for the bastion-chisel service (deploy-side change, not chisel-fork code). Integration test asserts N=10 active sessions all deactivate within 8s wall-clock.

The cancel-cause-context machinery in the Server struct (`shutdownCtx` cancelled with `tunnel.ErrServerShutdown` via `Close()`) propagates through to the proxy.Run goroutine's context. The `OnRemoteUnbound` callback's classifier reads `context.Cause(ctx)` and maps `ErrServerShutdown` to `DisconnectServerShutdown`, distinguishing it from regular client-side disconnects.

## Testing

### Unit tests (`server/coordinator/client_test.go`)

Per-method:
- `TestLookup_200_HappyPath` — assert URL, query params, response parsing
- `TestLookup_404_ReturnsErrNotFound`
- `TestLookup_503_ReturnsErrTransient`
- `TestLookup_401_ReturnsErrAuth`
- `TestLookup_TransportFailure_ReturnsErrTransient` (server torn down mid-call)
- `TestActivate_200_HappyPath`
- `TestActivate_409_ReturnsErrConflict`
- `TestActivate_404_ReturnsErrNotFound`
- `TestActivate_5xx_ReturnsErrTransient`
- `TestActivate_AuthFailure_ReturnsErrAuth`
- `TestDeactivate_200_HappyPath`
- `TestDeactivate_404_ReturnsErrNotFound` (logged, not propagated by server caller)
- Plus: TLS misconfig (wrong CA, wrong cert) → error before any HTTP flight

### Integration tests (`test/e2e/coordinator_test.go`)

Each test spins up a fake coordinator (`httptest.NewServer` with handlers that record calls), a real chisel-server with `Coordinator` configured pointing at it, and a real chisel-client.

- `TestCoordinatorHappyPath` — full flow: lookup → bind → activate → run → deactivate. Verify chisel listens on the coordinator-allocated port (not a random one). Verify activate body contains `target_hostname`, `actual_port_bound`, `client_remote_addr`.
- `TestCoordinatorRejectsUnknownHostname` — coordinator returns 404 lookup. Chisel SSH-rejects the connection ("no pending session"). No activate fires, no listener bound. INFO log on chisel side.
- `TestCoordinatorRejectsAuthFailure` — coordinator returns 401 lookup. Chisel SSH-rejects with generic "coordinator unreachable" (does NOT echo "auth failure" to LCM). chisel-server ERROR log contains the actual cause (`coordinator: auth failure`).
- `TestCoordinatorActivateFailureTeardownProxy` — coordinator returns 409 activate. Chisel tears down listener, SSH-rejects to client, no orphan port. Deactivate not called (session never reached active state on chisel side).
- `TestCoordinatorOmittedRunsLegacy` — `Config.Coordinator == nil` → upstream behavior unchanged. Client-requested port (legacy `R:23999:127.0.0.1:22`) is bound directly. No coordinator interaction.
- `TestCoordinatorDeactivateBestEffort` — coordinator returns 5xx on deactivate. Chisel logs WARN, exits handleWebsocket cleanly. (TTL fallback on coordinator side handles cleanup.)
- `TestCoordinatorReconnectIdempotent` — second activate for the same session_id (LCM tunnel drops + reconnects within session window). Coordinator must return 200, not 409. **This test fails until the coordinator-side idempotent-activate fix lands** — which is the correct gating for β.1 readiness.
- `TestCoordinatorSIGTERMParallelDrain` — start 10 active sessions, SIGTERM the chisel-server, assert all 10 deactivate calls land within 8s wall-clock. Catches accidental serialization regressions.

### Existing chisel e2e tests

The full upstream `test/e2e/` suite (`auth_test.go`, `proxy_test.go`, `socks_test.go`, `tls_test.go`, etc.) continues to pass with no changes — proves the legacy code path is unaffected when `Config.Coordinator` is nil.

## Out of scope

- **SSH host-key fingerprint pinning per-LCM** (the "option 1" hardening from the bastion β.1 doc). Belongs in a follow-up that also extends the coordinator's session schema.
- **Coordinator-restart reconciliation** (the `reconcile()` stub on coordinator side). Requires a chisel-fork API endpoint exposing active sessions, which this design doesn't add. Filed for the next phase.
- **CRL or cert-rotation runbook for `chisel-server.pem`**. Tracked in `bastion/docs/review-followups.md`.
- **Deactivate reason attribution beyond the 3 named consts**. "idle" and "max_duration" come from LCM-side state — coordinator joins logs to attribute them.
- **Caching coordinator responses** to soften transient outages. Threat model wants every connection re-validated.

## Open coordinator follow-up

Before β.1 ships, the bastion coordinator's `Activate` method needs to be made idempotent on reconnect:

> Current behavior (in `bastion/coordinator/sessions.go`): `if s.State != SessionStatePending { return error }` — returns 409 on second activate for the same session.
>
> Required behavior: accept second activate for an active session under the same `(session_id, target_hostname, port)` triple as a no-op success (200). Actual mismatch on any of those three remains 409.

Without this, a transient LCM tunnel drop + reconnect within the session window forces a full session-restart cycle by the operator. This is a coordinator-side change, not a chisel-fork change, but it's a hard prerequisite for `TestCoordinatorReconnectIdempotent` to pass.

Tracked as a separate task; the chisel-fork PR will not merge until the coordinator fix lands.

## Implementation phasing

1. **Phase 1: `share/tunnel/` hooks** — add `DisconnectReason` type, callbacks on `Config`, wiring in `BindRemotes` + `Proxy.Run`. Self-contained; no coordinator dependency. Ship-ready as an upstream PR independent of the coordinator code.
2. **Phase 2: `server/coordinator/` package** — Client, types, paths, errors. Unit tests with `httptest.NewServer`. Self-contained library code.
3. **Phase 3: `server/` integration** — `Config.Coordinator`, `NewServer` wiring (including `shutdownCtx`/`shutdownCancel`), `handleWebsocket` lookup→bind→activate flow, `rejectChisel` SSH-level reject mapping. Depends on Phases 1+2.
4. **Phase 4: `main.go` flags** — CLI plumbing. Trivial.
5. **Phase 5: integration tests** — `test/e2e/coordinator_test.go` covering the eight scenarios above.
6. **Phase 6: docs** — README note about the fork (one-sentence module-path explanation), commit-message hygiene marking AA-fork additions, contract doc cross-reference.

Each phase is one or two commits. Single PR at the end against the fork's `main`.

## Tagging + release

After integration tests pass:
- Tag `v1.10.1-aa.1` on the fork (semver-prerelease suffix marks it as our derivative of upstream v1.10.1).
- bastion's `chisel/Dockerfile` updated to download from `https://github.com/AdvancingAlternatives/chisel/releases/download/v1.10.1-aa.1/...`.
- bastion repo gets a `v0.4.0-rc.1` tag once the chisel-fork release is wired in and end-to-end smoke passes against the hallway LCMs.
- Promote bastion to `v0.4.0` after smoke test passes.

## Future upstream PR

The `share/tunnel/` changes (Phase 1) are a candidate for an upstream PR. Framing: "complete proxy lifecycle observability via OnRemoteBound/OnRemoteUnbound callbacks." If accepted, we move back to vendoring `jpillora/chisel` directly and dropping the `server/coordinator/` integration into our own server-side wrapper; the fork's `server/` modifications stay AA-only.

The `server/coordinator/` integration is fork-only and never targets upstream — it's bastion-specific glue.
