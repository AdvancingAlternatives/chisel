package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the HTTP client for AdvancingAlternatives's bastion coordinator.
// Holds a configured *http.Client (mTLS + per-call timeout) and the base
// URL. All methods take a context.Context for cancellation propagation.
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// New builds a Client from cfg, validating URL + loading mTLS material.
// Errors out before any HTTP flight if cfg is invalid.
func New(cfg *Config) (*Client, error) {
	u, err := cfg.ParseURL()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := cfg.LoadMTLS()
	if err != nil {
		return nil, err
	}

	timeout := cfg.TimeoutOrDefault()
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}

	return &Client{
		baseURL: strings.TrimRight(u.String(), "/"),
		http:    httpClient,
		timeout: timeout,
	}, nil
}

// Lookup queries the coordinator for a pending session matching hostname.
//
// On HTTP 200 returns the parsed session.
// On HTTP 404 returns (nil, err) with err wrapping ErrNotFound — the
// expected "no pending session" path.
// On HTTP 401/403 returns (nil, err) wrapping ErrAuth (Layer-2 cert bug).
// On HTTP 5xx or transport failure returns (nil, err) wrapping ErrTransient.
func (c *Client) Lookup(ctx context.Context, hostname string) (*Session, error) {
	q := url.Values{}
	q.Set("target_hostname", hostname)
	reqURL := c.baseURL + PathLookup + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Lookup: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var s Session
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return nil, fmt.Errorf("Lookup: decode: %w", err)
		}
		return &s, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Lookup: %w", ErrNotFound)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %s", ErrAuth, string(body))
	case resp.StatusCode >= 500:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: %w: %d %s", ErrTransient, resp.StatusCode, string(body))
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Lookup: unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

// Activate posts the bound port + remote address for a session,
// transitioning it pending->active on the coordinator side.
//
// Returns nil on 200.
// Returns err wrapping ErrNotFound on 404 (session gone — race condition).
// Returns err wrapping ErrConflict on 409 (port mismatch / hostname
// mismatch / wrong state — caller should tear down the proxy).
// Returns err wrapping ErrAuth on 401/403 (cert bug).
// Returns err wrapping ErrTransient on 5xx / transport failure.
func (c *Client) Activate(ctx context.Context, sessionID, hostname string, port int, remoteAddr string) error {
	body, err := json.Marshal(ActivateRequest{
		TargetHostname:   hostname,
		ActualPortBound:  port,
		ClientRemoteAddr: remoteAddr,
	})
	if err != nil {
		return fmt.Errorf("Activate: marshal: %w", err)
	}

	reqURL := c.baseURL + fmt.Sprintf(PathActivate, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Activate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Activate: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// Drain body so the connection can be reused by the next call.
		io.Copy(io.Discard, resp.Body)
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("Activate: %w", ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %s", ErrConflict, string(respBody))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %s", ErrAuth, string(respBody))
	case resp.StatusCode >= 500:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: %w: %d %s", ErrTransient, resp.StatusCode, string(respBody))
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Activate: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}

// Deactivate posts the close reason for a session. Best-effort from the
// caller's perspective: errors are logged but generally don't propagate
// (the coordinator's TTL fallback handles cleanup if this fails).
//
// Returns nil on 200 or 204.
// Returns err wrapping ErrNotFound on 404 (session gone — coordinator already cleaned up).
// Returns err wrapping ErrConflict on 409 (already closed — stale retry).
// Returns err wrapping ErrAuth on 401/403, ErrTransient on 5xx / transport.
func (c *Client) Deactivate(ctx context.Context, sessionID, hostname string, port int, reason string) error {
	body, err := json.Marshal(DeactivateRequest{
		TargetHostname:  hostname,
		ActualPortBound: port,
		Reason:          reason,
	})
	if err != nil {
		return fmt.Errorf("Deactivate: marshal: %w", err)
	}

	reqURL := c.baseURL + fmt.Sprintf(PathDeactivate, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Deactivate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Deactivate: %w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent:
		io.Copy(io.Discard, resp.Body)
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("Deactivate: %w", ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %s", ErrConflict, string(respBody))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %s", ErrAuth, string(respBody))
	case resp.StatusCode >= 500:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: %w: %d %s", ErrTransient, resp.StatusCode, string(respBody))
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Deactivate: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}

// NewClientForTesting builds a Client with a plain http.Client (no mTLS).
// Used by integration tests that point at httptest.Server, which doesn't
// serve TLS. NEVER use this in production code paths.
func NewClientForTesting(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
		timeout: 5 * time.Second,
	}
}
