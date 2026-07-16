package e2e_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
	"github.com/jpillora/chisel/server/coordinator"
)

// TestCoordinatorActivateFailureTeardownProxy verifies that when the
// coordinator returns 409 on activate (port mismatch / hostname
// mismatch), chisel-server tears down its just-bound listener so no
// orphan port is exposed.
func TestCoordinatorActivateFailureTeardownProxy(t *testing.T) {
	const allocatedPort = 23042

	var activateCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", func(w http.ResponseWriter, r *http.Request) {
		// Always return a pending session.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(coordSession{
			SessionID:      "ses_conf",
			Port:           allocatedPort,
			State:          "pending",
			TargetHostname: r.URL.Query().Get("target_hostname"),
		})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&activateCalls, 1)
		http.Error(w, "hostname mismatch", http.StatusConflict)
	})
	overrideServer := httptest.NewServer(mux)
	t.Cleanup(overrideServer.Close)

	// Build chisel-server with coordinator pointing at the override.
	srv, err := chserver.NewServerNoCoordValidation(&chserver.Config{
		KeySeed: "test-actfail",
		Reverse: true,
		Auth:    "lcm-fleet:secret",
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
		Headers:          http.Header{"Host": []string{"lcm-conf1"}},
		Remotes:          []string{"R:" + availablePort() + ":127.0.0.1:22"},
		MaxRetryCount:    0,
		MaxRetryInterval: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = c.Start(ctx)
	t.Cleanup(func() { c.Close() })

	time.Sleep(500 * time.Millisecond)

	if atomic.LoadInt32(&activateCalls) == 0 {
		t.Error("activate never called")
	}

	// Critical: port must NOT be listening.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:23042", 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Errorf("port 23042 still listening after activate 409 - proxy not torn down")
	}
}
