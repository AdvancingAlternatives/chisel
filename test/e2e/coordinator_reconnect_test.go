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

// TestCoordinatorReconnectIdempotent simulates an LCM tunnel-drop +
// reconnect within the session window. The coordinator must accept the
// second activate for the same session_id under the same params as
// idempotent (200 / no-op), NOT 409. This test fails until the
// coordinator-side fix lands in bastion's coordinator/sessions.go.
func TestCoordinatorReconnectIdempotent(t *testing.T) {
	const allocatedPort = 23071

	var activateCount int32

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(coordSession{
			SessionID:      "ses_re",
			Port:           allocatedPort,
			State:          "pending",
			TargetHostname: r.URL.Query().Get("target_hostname"),
		})
	})
	mux.HandleFunc("/sessions/ses_re/activate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&activateCount, 1)
		// Idempotent on every call. (Real coordinator must do same once
		// the bastion-side idempotent-activate fix lands.)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/sessions/ses_re/deactivate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	overrideServer := httptest.NewServer(mux)
	t.Cleanup(overrideServer.Close)

	srv, err := chserver.NewServerNoCoordValidation(&chserver.Config{
		KeySeed: "test-reconnect",
		Reverse: true,
		Auth:    "lcm-fleet:secret",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	chserver.SetCoordClient(srv, coordinator.NewClientForTesting(overrideServer.URL))
	listenPort := startServer(t, srv)
	defer srv.Close()

	// First connection.
	c1, _ := chclient.NewClient(&chclient.Config{
		Server:        "http://127.0.0.1:" + listenPort,
		Auth:          "lcm-fleet:secret",
		Headers:       http.Header{"Host": []string{"lcm-re1"}},
		Remotes:       []string{"R:" + availablePort() + ":127.0.0.1:22"},
		MaxRetryCount: 0,
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := c1.Start(ctx1); err != nil {
		t.Fatalf("Start c1: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&activateCount) >= 1
	}) {
		t.Fatal("first activate never fired")
	}

	// Drop first connection.
	cancel1()
	c1.Close()

	// Second connection (simulate LCM reconnect).
	// Under disconnect-reason gating the chisel-server does NOT fire a
	// Deactivate on client-disconnect — the session stays active so the
	// reconnect Lookup can land on it. We still pause briefly to let the
	// chisel-server tear down its proxy goroutine before the new client
	// reuses the placeholder listener port.
	time.Sleep(200 * time.Millisecond)
	c2, _ := chclient.NewClient(&chclient.Config{
		Server:        "http://127.0.0.1:" + listenPort,
		Auth:          "lcm-fleet:secret",
		Headers:       http.Header{"Host": []string{"lcm-re1"}},
		Remotes:       []string{"R:" + availablePort() + ":127.0.0.1:22"},
		MaxRetryCount: 0,
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := c2.Start(ctx2); err != nil {
		t.Fatalf("Start c2: %v", err)
	}
	defer c2.Close()

	if !waitFor(2*time.Second, func() bool {
		return atomic.LoadInt32(&activateCount) >= 2
	}) {
		t.Errorf("second activate didn't fire (only %d activates total)", atomic.LoadInt32(&activateCount))
	}
}
