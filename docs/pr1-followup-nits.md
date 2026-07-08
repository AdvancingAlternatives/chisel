# PR #1 follow-up nits — to file as GitHub issues once `gh` auth is restored

These are the four (C1-C4) follow-up items from Nathan's PR #1 review.
Should-fix-before-merge B1 was already resolved in commit `7a48751`.

---

## C1 — `classifyDisconnect`: tighten EOF detection via `errors.Is(io.EOF)`

**File:** `share/tunnel/disconnect.go`

`classifyDisconnect` currently uses `strings.HasSuffix(cause.Error(), "EOF")` to detect EOF cancel causes. Tighten to `errors.Is(cause, io.EOF)` for wrapped-error robustness — the current implementation misses cases like `fmt.Errorf("disconnect: %w (peer=foo)", io.EOF)` which produces `"disconnect: EOF (peer=foo)"` (no longer a suffix match).

Not blocking; tests cover the suffix happy path. One-line fix.

Label: enhancement

---

## C2 — `coordLog` fakes WARN with INFO+prefix; add real WARN level to `cio.Logger`

**File:** `server/server.go` (and `share/cio/logger.go`)

`coordLog` routes `"warn"` level to `s.Infof("WARN "+format, ...)` because chisel's `cio.Logger` doesn't expose `Warnf`. Cosmetic but undermines log-level filtering at the operations layer.

Cleanest fix: extend `share/cio/logger.go` with a real `Warnf` method, then update `coordLog` to use it. Touches upstream-reachable code (`share/cio/`) — needs the AA-fork comment markers per the Phase 1 convention.

Label: enhancement

---

## C3 — Design doc example `R:0:127.0.0.1:22` won't parse — update to `R:50000:...`

**File:** `docs/superpowers/specs/2026-05-07-chisel-fork-coordinator-callbacks-design.md` (around line 386 in the data-flow happy-path diagram)

The example shows `R:0:127.0.0.1:22` as the LCM-supplied reverse spec. Chisel's `settings.DecodeRemote.isPort` rejects port 0 (`n > 0` required), so this would actually fail `CanListen` validation server-side. lcm-agent PR #3 already updates the production script to use `R:50000:localhost:22`; the design-doc example should mirror that.

Pure doc fix. No code change.

Label: documentation

---

## C4 — Coordinator client `Deactivate`: take `tunnel.DisconnectReason` instead of string

**Files:** `server/coordinator/client.go` (signature) + `server/coord_handler.go` (call site)

`Client.Deactivate` takes `reason string`. Tighter to take `tunnel.DisconnectReason` directly so the type system enforces only the three valid values (`DisconnectClient` / `DisconnectConnectionLost` / `DisconnectServerShutdown`).

The current call site in `coordinatorUnbindHook` already does `string(reason)` to convert, so the cast can move into the client.

Touch is small (one signature change + the cast site). No behavior change; cleaner API.

Label: enhancement
