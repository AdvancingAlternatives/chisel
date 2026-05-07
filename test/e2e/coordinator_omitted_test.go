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
