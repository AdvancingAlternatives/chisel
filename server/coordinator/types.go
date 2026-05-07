package coordinator

// Session is the subset of the coordinator's session record that this
// client cares about. The coordinator's full Session struct has additional
// fields (created_at, expires_at, credential, etc.) that aren't needed at
// the chisel-server layer. Tagged with json names matching coordinator's
// API responses.
type Session struct {
	SessionID      string `json:"session_id"`
	Port           int    `json:"port"`
	State          string `json:"state"` // "pending", "active", or "closed"
	TargetHostname string `json:"target_hostname"`
	AuthorizedUser string `json:"authorized_user,omitempty"`
}

// ActivateRequest is the body posted to POST /sessions/{id}/activate.
// Fields match the contract in bastion/docs/chisel-fork-contract.md.
type ActivateRequest struct {
	// TargetHostname is the chisel client's --hostname value (carried in
	// the WebSocket Host header). Coordinator verifies it matches the
	// recorded hostname for the session — defense-in-depth against a
	// compromised chisel-server cert flipping arbitrary sessions active
	// under the wrong identity.
	TargetHostname string `json:"target_hostname"`

	// ActualPortBound is the OS-assigned port the chisel-server bound for
	// the reverse tunnel. Should match the coordinator's pre-allocated
	// port; mismatch returns 409.
	ActualPortBound int `json:"actual_port_bound"`

	// ClientRemoteAddr is chisel-server's view of the LCM client's IP+port
	// (from req.RemoteAddr). Logged on the coordinator side for the audit
	// trail described in bastion's SECURITY.md §3.
	ClientRemoteAddr string `json:"client_remote_addr"`
}

// DeactivateRequest is the body posted to POST /sessions/{id}/deactivate.
type DeactivateRequest struct {
	// TargetHostname is repeated for cheap deactivate-for-wrong-session
	// detection on the coordinator side.
	TargetHostname string `json:"target_hostname"`

	// ActualPortBound is the port that was bound when this session was
	// active — same purpose as TargetHostname (correlation/sanity check).
	ActualPortBound int `json:"actual_port_bound"`

	// Reason is the disconnect cause. One of
	// "client_disconnect" / "connection_lost" / "server_shutdown" — see
	// share/tunnel/disconnect.go.
	Reason string `json:"reason"`
}
