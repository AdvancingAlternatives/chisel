// Package chserver: AA-fork coordinator-integration glue. This file
// holds helpers used by handleWebsocket when chisel-server is configured
// with a coordinator. Kept separate from server_handler.go so the
// upstream merge surface for server_handler.go stays small.
package chserver

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/jpillora/chisel/server/coordinator"
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
