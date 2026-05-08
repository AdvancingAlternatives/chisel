// Package chserver: AA-fork coordinator-integration glue. This file
// holds helpers used by handleWebsocket when chisel-server is configured
// with a coordinator. Kept separate from server_handler.go so the
// upstream merge surface for server_handler.go stays small.
package chserver

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

// overrideReverseRemotePort finds the first reverse remote in remotes and
// rewrites the listen-side port (LocalPort) to the coordinator-allocated
// value. The β.1 model has exactly one reverse per session
// (R:0:localhost:22 from the LCM); if zero remotes match, returns an error
// so the caller can SSH-reject the connection.
//
// In chisel's R:LISTEN_PORT:DEST_HOST:DEST_PORT encoding, the listen-side
// port maps to Remote.LocalPort (and proxy.listen() binds LocalHost:LocalPort
// for reverse remotes). RemotePort is the destination port on the LCM side
// and must be left intact so SSH traffic still reaches port 22 there.
//
// Mutates remotes in place because chisel's downstream code re-reads
// each Remote via .UserAddr() / .CanListen() / .Local() / .Remote(), and the
// override needs to be visible there.
func overrideReverseRemotePort(remotes []*settings.Remote, port int) error {
	for _, r := range remotes {
		if r.Reverse {
			r.LocalPort = strconv.Itoa(port)
			return nil
		}
	}
	return fmt.Errorf("no reverse remote in client config (got %d remotes)", len(remotes))
}

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
