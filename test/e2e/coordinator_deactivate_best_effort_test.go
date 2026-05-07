package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
	"github.com/jpillora/chisel/server/coordinator"
)

// TestCoordinatorDeactivateBestEffort verifies that a 5xx deactivate
// response doesn't propagate as an error to the chisel-server's exit
// path. handleWebsocket returns cleanly; the coordinator's TTL fallback
// is the safety net for cleanup.
func TestCoordinatorDeactivateBestEffort(t *testing.T) {
	const allocatedPort = 23061

	var activateCalls, deactivateCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(coordSession{
			SessionID:      "ses_be",
			Port:           allocatedPort,
			State:          "pending",
			TargetHostname: r.URL.Query().Get("target_hostname"),
		})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		// /sessions/ses_be/activate -> 200; /sessions/ses_be/deactivate -> 500
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case r.URL.Path == "/sessions/ses_be/activate":
			atomic.AddInt32(&activateCalls, 1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/sessions/ses_be/deactivate":
			atomic.AddInt32(&deactivateCalls, 1)
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	overrideServer := httptest.NewServer(mux)
	t.Cleanup(overrideServer.Close)

	srv, err := chserver.NewServerNoCoordValidation(&chserver.Config{
		KeySeed:     "test-be",
		Reverse:     true,
		Auth:        "lcm-fleet:secret",
		Coordinator: &coordinator.Config{},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	chserver.SetCoordClient(srv, coordinator.NewClientForTesting(overrideServer.URL))
	listenPort := startServer(t, srv)
	defer srv.Close()

	c, _ := chclient.NewClient(&chclient.Config{
		Server:           "http://127.0.0.1:" + listenPort,
		Auth:             "lcm-fleet:secret",
		Headers:          http.Header{"Host": []string{"lcm-be1"}},
		Remotes:          []string{"R:50000:127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Wait for tunnel up (activate fired).
	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&activateCalls) > 0
	}) {
		t.Fatal("activate never fired")
	}

	// Trigger disconnect.
	cancel()
	c.Close()

	// Deactivate must fire (gets 500) but NOT crash the handler.
	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&deactivateCalls) > 0
	}) {
		t.Fatal("deactivate never called")
	}

	// Test passes if we reach here without panic / hung server. The
	// chisel-server log will contain the deactivate failure at WARN -
	// not asserted directly because cio.Logger doesn't expose buffer.
}
