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
