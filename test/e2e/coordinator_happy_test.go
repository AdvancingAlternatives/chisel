package e2e_test

import (
	"context"
	"net"
	"net/http"
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
		hostname      = "lcm-abc1"
		sessionID     = "ses_test_happy"
		allocatedPort = 23001
	)
	fc.preallocate(hostname, sessionID, allocatedPort)

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	// Note on the placeholder port "50000": the spec/design has the LCM
	// send R:0:127.0.0.1:22 with port 0 meaning "let coordinator decide,"
	// but chisel's settings.DecodeRemote rejects "0" as a port (isPort()
	// requires n>=1) which makes pre-validation in handleWebsocket choke.
	// The β.1 LCM in production sends a real placeholder; the coordinator
	// override rewrites it before the listener binds. availablePort() hands
	// us a currently-free port so CanListen() succeeds regardless of what
	// else is bound on the host (a fixed port like 50000 flaked on Windows
	// CI whenever the runner already had it in use); what matters for this
	// test is that the override changes the listen port to allocatedPort.
	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Headers:          http.Header{"Host": []string{hostname}},
		Remotes:          []string{"R:" + availablePort() + ":127.0.0.1:22"},
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

	// Trigger server shutdown — this is the path that drives Deactivate
	// under the new disconnect-reason gating (client disconnects preserve
	// the session for reconnect; see coordinator_client_disconnect_test.go).
	srv.Close()

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
	if d := fc.deactivateCalls[0]; d.Reason != "server_shutdown" {
		t.Errorf("deactivate.Reason = %q, want %q", d.Reason, "server_shutdown")
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
