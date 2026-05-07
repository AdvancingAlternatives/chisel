// AA-fork: this file was added by AdvancingAlternatives's chisel fork
// to expose proxy-lifecycle observability via the OnRemoteBound /
// OnRemoteUnbound callbacks on tunnel.Config (see tunnel.go).

package tunnel

import (
	"context"
	"errors"
	"strings"
)

// DisconnectReason describes why a remote was unbound from the tunnel.
// The server distinguishes three coarse categories; consumers needing
// finer attribution (idle timeout, max-duration, etc.) are expected to
// derive that from their own state.
type DisconnectReason string

const (
	// DisconnectClient indicates the remote disconnected cleanly —
	// either via a graceful WebSocket close or normal end-of-stream
	// (EOF). The typical case for a client process exiting cleanly.
	DisconnectClient DisconnectReason = "client_disconnect"

	// DisconnectConnectionLost indicates the remote disconnected
	// abnormally — TCP reset, network partition, keepalive failure,
	// or any non-EOF error returned by the SSH connection.
	DisconnectConnectionLost DisconnectReason = "connection_lost"

	// DisconnectServerShutdown indicates the chisel server itself
	// initiated the disconnect — typically because Server.Close()
	// was called. Distinguishable via context.Cause(ctx) returning
	// ErrServerShutdown.
	DisconnectServerShutdown DisconnectReason = "server_shutdown"
)

// ErrServerShutdown is the cancel cause used by Server.Close() so
// that consumers reading context.Cause(ctx) on a disconnected proxy's
// context can distinguish server-initiated shutdown from any other
// disconnect cause.
var ErrServerShutdown = errors.New("chisel: server shutdown")

// classifyDisconnect maps a proxy's running-context cancel-cause to a
// DisconnectReason. Called from Proxy.Run's exit path so the callback site
// has authoritative reason data without a setter race.
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
