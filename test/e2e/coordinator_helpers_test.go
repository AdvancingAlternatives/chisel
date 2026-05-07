package e2e_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCoordinator is an in-process httptest.Server that implements the
// bastion coordinator's session-lifecycle endpoints. Records every call
// so tests can assert on hit-counts + bodies.
type fakeCoordinator struct {
	server *httptest.Server

	mu              sync.Mutex
	sessions        map[string]coordSession // keyed by target_hostname
	sessionsByID    map[string]coordSession
	lookupCalls     []string // target_hostnames in order
	activateCalls   []activateBody
	deactivateCalls []deactivateBody

	// Test-side overrides
	lookupStatusOverride int // when non-zero, lookup returns this status regardless
}

type coordSession struct {
	SessionID      string `json:"session_id"`
	Port           int    `json:"port"`
	State          string `json:"state"`
	TargetHostname string `json:"target_hostname"`
}

type activateBody struct {
	SessionID        string `json:"-"`
	TargetHostname   string `json:"target_hostname"`
	ActualPortBound  int    `json:"actual_port_bound"`
	ClientRemoteAddr string `json:"client_remote_addr"`
}

type deactivateBody struct {
	SessionID       string `json:"-"`
	TargetHostname  string `json:"target_hostname"`
	ActualPortBound int    `json:"actual_port_bound"`
	Reason          string `json:"reason"`
}

func newFakeCoordinator(t *testing.T) *fakeCoordinator {
	t.Helper()
	fc := &fakeCoordinator{
		sessions:     map[string]coordSession{},
		sessionsByID: map[string]coordSession{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/lookup", fc.handleLookup)
	mux.HandleFunc("/sessions/", fc.handleSessionsByID)
	fc.server = httptest.NewServer(mux)
	t.Cleanup(fc.server.Close)
	return fc
}

func (fc *fakeCoordinator) URL() string { return fc.server.URL }

func (fc *fakeCoordinator) preallocate(hostname, sessionID string, port int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	s := coordSession{SessionID: sessionID, Port: port, State: "pending", TargetHostname: hostname}
	fc.sessions[hostname] = s
	fc.sessionsByID[sessionID] = s
}

func (fc *fakeCoordinator) handleLookup(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("target_hostname")
	fc.mu.Lock()
	fc.lookupCalls = append(fc.lookupCalls, hostname)
	override := fc.lookupStatusOverride
	s, ok := fc.sessions[hostname]
	fc.mu.Unlock()

	if override != 0 {
		http.Error(w, "override", override)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// handleSessionsByID dispatches /sessions/{id}/activate and /sessions/{id}/deactivate
// using URL-path parsing (the http.ServeMux pattern matching requires
// exact paths; we lift the wildcard manually here for this stand-in).
func (fc *fakeCoordinator) handleSessionsByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "sessions" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id := parts[1]
	verb := parts[2]
	switch verb {
	case "activate":
		fc.handleActivate(w, r, id)
	case "deactivate":
		fc.handleDeactivate(w, r, id)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (fc *fakeCoordinator) handleActivate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body activateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	body.SessionID = id

	fc.mu.Lock()
	fc.activateCalls = append(fc.activateCalls, body)
	s, ok := fc.sessionsByID[id]
	if ok {
		s.State = "active"
		fc.sessionsByID[id] = s
		fc.sessions[s.TargetHostname] = s
	}
	fc.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (fc *fakeCoordinator) handleDeactivate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body deactivateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	body.SessionID = id

	fc.mu.Lock()
	fc.deactivateCalls = append(fc.deactivateCalls, body)
	s, ok := fc.sessionsByID[id]
	if ok {
		s.State = "closed"
		fc.sessionsByID[id] = s
		delete(fc.sessions, s.TargetHostname)
	}
	fc.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// extractHostFromURL parses urlStr and returns its host:port. Used in
// tests when constructing a chisel-client config that points at the
// httptest server's listen address.
func extractHostFromURL(t *testing.T, urlStr string) string {
	t.Helper()
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("parse %q: %v", urlStr, err)
	}
	return u.Host
}

// waitFor blocks until pred returns true or timeout. Returns true if pred
// became true, false on timeout.
func waitFor(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pred()
}

var _ = errors.New // pacify import if unused in helpers (keeps import for tests below)
