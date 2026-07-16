// AA-fork: e2e tests for the disconnect-reason gating in
// coordinatorUnbindHook. Verifies that client-side disconnects preserve
// the coordinator session (no Deactivate) while server-shutdowns drive
// Deactivate with reason=server_shutdown.
//
// See AdvancingAlternatives/chisel#2 for the design rationale.

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestClientDisconnectKeepsSessionActive verifies that a clean WebSocket
// close from the chisel-client does NOT trigger a coordinator.Deactivate.
// The session is intentionally left active so the LCM's reconnect path
// (Lookup + idempotent-Activate) can rehydrate the same session_id after
// a routine `systemctl restart lcm-bastion` on the gateway.
func TestClientDisconnectKeepsSessionActive(t *testing.T) {
	fc := newFakeCoordinator(t)

	const (
		hostname      = "lcm-keep1"
		sessionID     = "ses_keep_active"
		allocatedPort = 23201
	)
	fc.preallocate(hostname, sessionID, allocatedPort)

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

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

	// Wait for activate so we know the tunnel is fully up.
	if !waitFor(3*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.activateCalls) > 0
	}) {
		t.Fatal("activate never fired")
	}

	// Trigger a clean client-side disconnect (cancel ctx + Close).
	cancel()
	c.Close()

	// Give the chisel-server ample time to run OnRemoteUnbound. With the
	// disconnect-reason gating the unbind hook MUST NOT call Deactivate.
	// We can't easily "wait for a thing not to happen," so we wait a
	// fixed window long enough to cover the unbind hook + coordinator
	// HTTP roundtrip (unbind closure has a 5s default timeout but the
	// HTTP call to the in-process fake is fast).
	time.Sleep(500 * time.Millisecond)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if got := len(fc.deactivateCalls); got != 0 {
		t.Errorf("deactivate fired %d times on client-disconnect; want 0 (session must stay active for reconnect). calls: %+v", got, fc.deactivateCalls)
	}
}

// TestServerShutdownDeactivates verifies that a chisel-server shutdown
// (Server.Close()) DOES drive a coordinator.Deactivate, and that the
// reason posted to the coordinator is "server_shutdown" (the wire form
// of tunnel.DisconnectServerShutdown). This is the one branch where the
// reverse listener genuinely cannot accept a reconnect, so the session
// must be closed-out on the coordinator.
func TestServerShutdownDeactivates(t *testing.T) {
	fc := newFakeCoordinator(t)

	const (
		hostname      = "lcm-shutdn1"
		sessionID     = "ses_shutdown"
		allocatedPort = 23202
	)
	fc.preallocate(hostname, sessionID, allocatedPort)

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)

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

	// Wait for activate so we know the tunnel is fully up.
	if !waitFor(3*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.activateCalls) > 0
	}) {
		t.Fatal("activate never fired")
	}

	// SIGTERM equivalent: chisel-server.Close() cancels shutdownCtx with
	// tunnel.ErrServerShutdown as the cause, which propagates through the
	// proxy.Run context. The unbind hook's classifier should map this to
	// DisconnectServerShutdown and call Deactivate.
	srv.Close()

	// Wait for deactivate to fire.
	if !waitFor(3*time.Second, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.deactivateCalls) > 0
	}) {
		fc.mu.Lock()
		t.Fatalf("deactivate never called on server shutdown; activates=%d, deactivates=%d", len(fc.activateCalls), len(fc.deactivateCalls))
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	got := fc.deactivateCalls[0]
	if got.SessionID != sessionID {
		t.Errorf("deactivate.SessionID = %q, want %q", got.SessionID, sessionID)
	}
	if got.Reason != "server_shutdown" {
		t.Errorf("deactivate.Reason = %q, want %q (tunnel.DisconnectServerShutdown wire form)", got.Reason, "server_shutdown")
	}
}
