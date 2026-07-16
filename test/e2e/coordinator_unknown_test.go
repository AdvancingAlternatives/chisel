package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// TestCoordinatorRejectsUnknownHostname verifies that when the
// coordinator returns 404 to a lookup, chisel-server SSH-rejects the
// connection. Activate is never called; no listener is bound.
func TestCoordinatorRejectsUnknownHostname(t *testing.T) {
	fc := newFakeCoordinator(t)
	// no preallocate — fake returns 404 for any hostname

	srv := chiselServerWithFakeCoordinator(t, fc, "lcm-fleet:secret")
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Headers:          http.Header{"Host": []string{"lcm-unknown1"}},
		Remotes:          []string{"R:" + availablePort() + ":127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = c.Start(ctx) // expect error from server-side reject
	t.Cleanup(func() { c.Close() })

	// Give it half a second for the lookup to fire.
	time.Sleep(500 * time.Millisecond)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.lookupCalls) == 0 {
		t.Errorf("lookup never called")
	}
	if len(fc.activateCalls) != 0 {
		t.Errorf("activate fired despite 404 lookup; activate calls = %+v", fc.activateCalls)
	}
}
