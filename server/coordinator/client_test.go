package coordinator

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClientReturnsConfiguredClient(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: cert,
		MTLSKeyFile:  key,
		MTLSCAFile:   ca,
		Timeout:      3 * time.Second,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.baseURL != "https://coord:8443" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	cfg := &Config{URL: "http://insecure"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted http URL")
	}
}

func TestNewClientRejectsMissingMTLSFiles(t *testing.T) {
	cfg := &Config{URL: "https://coord:8443"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted missing mTLS files")
	}
}

// httpRoundTripperWithTLS is a small helper: returns the *http.Client
// constructed by New so we can inspect its Transport's TLSClientConfig.
func transportTLS(c *Client) *tls.Config {
	if c.http == nil {
		return nil
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		return nil
	}
	return tr.TLSClientConfig
}

func TestNewClientUsesTLS13Minimum(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL: "https://coord:8443", MTLSCertFile: cert, MTLSKeyFile: key, MTLSCAFile: ca,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tlsCfg := transportTLS(c)
	if tlsCfg == nil {
		t.Fatal("Transport.TLSClientConfig is nil")
	}
	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS13 (%d)", tlsCfg.MinVersion, tls.VersionTLS13)
	}
}

// fakeCoordinatorServer returns an httptest.Server with the given handler.
// Wires the coordinator.Client to it, replacing the http.Transport's TLS
// config so the test doesn't need real certs (the server is plain HTTP).
func fakeCoordinatorServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := &Client{
		baseURL: srv.URL,
		http:    srv.Client(),
		timeout: 2 * time.Second,
	}
	return srv, c
}

func TestLookup200ReturnsSession(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/lookup" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("target_hostname") != "lcm-a57d" {
			t.Errorf("target_hostname query = %q", r.URL.Query().Get("target_hostname"))
		}
		json.NewEncoder(w).Encode(Session{
			SessionID:      "ses_xyz",
			Port:           22099,
			State:          "pending",
			TargetHostname: "lcm-a57d",
		})
	})

	s, err := c.Lookup(context.Background(), "lcm-a57d")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if s.SessionID != "ses_xyz" || s.Port != 22099 {
		t.Errorf("got %+v", s)
	}
}

func TestLookup404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no pending session", http.StatusNotFound)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLookup401ReturnsErrAuth(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestLookup500ReturnsErrTransient(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestLookupTransportFailureReturnsErrTransient(t *testing.T) {
	srv, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // server is now down; next call gets connection-refused
	_, err := c.Lookup(context.Background(), "lcm-x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestActivate200Success(t *testing.T) {
	var got ActivateRequest
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/sessions/ses_abc/activate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	})

	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "10.5.6.7:55432")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got.TargetHostname != "lcm-a57d" || got.ActualPortBound != 22099 || got.ClientRemoteAddr != "10.5.6.7:55432" {
		t.Errorf("activate body = %+v", got)
	}
}

func TestActivate409ReturnsErrConflict(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "hostname mismatch", http.StatusConflict)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestActivate404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session gone", http.StatusNotFound)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestActivate503ReturnsErrTransient(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	err := c.Activate(context.Background(), "ses_abc", "lcm-a57d", 22099, "x")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestDeactivate200Success(t *testing.T) {
	var got DeactivateRequest
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/ses_abc/deactivate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	})

	err := c.Deactivate(context.Background(), "ses_abc", "lcm-a57d", 22099, "client_disconnect")
	if err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if got.Reason != "client_disconnect" {
		t.Errorf("reason = %q", got.Reason)
	}
	if got.TargetHostname != "lcm-a57d" || got.ActualPortBound != 22099 {
		t.Errorf("deactivate body = %+v", got)
	}
}

func TestDeactivate204Success(t *testing.T) {
	// Coordinator may also use 204 No Content.
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-a57d", 22099, "client_disconnect")
	if err != nil {
		t.Errorf("Deactivate(204): %v", err)
	}
}

func TestDeactivate409ReturnsErrConflict(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "already closed", http.StatusConflict)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-x", 22099, "client_disconnect")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestDeactivate404ReturnsErrNotFound(t *testing.T) {
	_, c := fakeCoordinatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session gone", http.StatusNotFound)
	})
	err := c.Deactivate(context.Background(), "ses_abc", "lcm-x", 22099, "client_disconnect")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
