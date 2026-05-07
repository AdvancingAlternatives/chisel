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
