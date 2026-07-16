package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
	"github.com/jpillora/chisel/server/coordinator"
)

// TestCoordinatorSIGTERMParallelDrain verifies that when chisel-server's
// Close() is called with N active sessions, all N deactivate calls fire
// and complete within a wall-clock window much smaller than (N * timeout).
// Catches a regression where shutdown serializes deactivate calls
// instead of relying on per-goroutine OnRemoteUnbound closures.
func TestCoordinatorSIGTERMParallelDrain(t *testing.T) {
	const N = 10
	const allocatedPortBase = 23100

	var activateCount, deactivateCount int32

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", func(w http.ResponseWriter, r *http.Request) {
		hostname := r.URL.Query().Get("target_hostname")
		// Map "lcm-drain<N>" -> port 23100+N, sessionID "ses_drain_<N>".
		var idx int
		fmt.Sscanf(hostname, "lcm-drain%d", &idx)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(coordSession{
			SessionID:      fmt.Sprintf("ses_drain_%d", idx),
			Port:           allocatedPortBase + idx,
			State:          "pending",
			TargetHostname: hostname,
		})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		// /sessions/{id}/activate -> 200; /sessions/{id}/deactivate -> 200 with 1s sleep.
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case len(r.URL.Path) > len("/deactivate") && r.URL.Path[len(r.URL.Path)-len("/deactivate"):] == "/deactivate":
			atomic.AddInt32(&deactivateCount, 1)
			time.Sleep(1 * time.Second) // simulate slow coordinator
			w.WriteHeader(http.StatusOK)
		case len(r.URL.Path) > len("/activate") && r.URL.Path[len(r.URL.Path)-len("/activate"):] == "/activate":
			atomic.AddInt32(&activateCount, 1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
	overrideServer := httptest.NewServer(mux)
	t.Cleanup(overrideServer.Close)

	srv, err := chserver.NewServerNoCoordValidation(&chserver.Config{
		KeySeed: "test-sigterm",
		Reverse: true,
		Auth:    "lcm-fleet:secret",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	chserver.SetCoordClient(srv, coordinator.NewClientForTesting(overrideServer.URL))
	listenPort := startServer(t, srv)

	// Bring up N concurrent client sessions.
	clients := make([]*chclient.Client, 0, N)
	for i := 0; i < N; i++ {
		c, err := chclient.NewClient(&chclient.Config{
			Server:  "http://127.0.0.1:" + listenPort,
			Auth:    "lcm-fleet:secret",
			Headers: http.Header{"Host": []string{fmt.Sprintf("lcm-drain%d", i)}},
			// Use a fresh OS-assigned free port per client (availablePort)
			// because chisel's CanListen() check does a real bind/close
			// before the coordinator override; a fixed port like 50000+i
			// flaked on Windows CI when the runner already had it in use.
			// The placeholder is rewritten to allocatedPortBase+idx by
			// overrideReverseRemotePort.
			Remotes:       []string{"R:" + availablePort() + ":127.0.0.1:22"},
			MaxRetryCount: 0,
		})
		if err != nil {
			t.Fatalf("NewClient %d: %v", i, err)
		}
		ctx := context.Background()
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	// Wait until all N activates have landed.
	if !waitFor(5*time.Second, func() bool {
		return atomic.LoadInt32(&activateCount) >= N
	}) {
		t.Fatalf("not all activates fired: got %d, want %d", atomic.LoadInt32(&activateCount), N)
	}

	// SIGTERM equivalent: call Close + wait for all deactivates.
	start := time.Now()
	srv.Close()

	// All N deactivates should complete in roughly 1s wall-clock (parallel),
	// not N * 1s (serial). Allow 3s headroom for goroutine scheduling.
	if !waitFor(3*time.Second, func() bool {
		return atomic.LoadInt32(&deactivateCount) >= N
	}) {
		t.Errorf("only %d/%d deactivates fired within 3s after Close()", atomic.LoadInt32(&deactivateCount), N)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("drain took %v, want < 3s (parallel deactivates expected, not serial)", elapsed)
	}
}
