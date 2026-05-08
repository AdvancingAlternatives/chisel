// Package coordinator provides the HTTP client for AdvancingAlternatives's
// bastion coordinator API. The API contract is documented in the bastion
// repo (docs/chisel-fork-contract.md). This package is fork-only and never
// targets upstream.
package coordinator

// Path templates for the coordinator API. The activate / deactivate paths
// take a session_id substituted via fmt.Sprintf with %s. These constants
// are the single source of truth for URL conventions; they live in code
// rather than as configurable flags because the convention is owned by us
// at both ends (bastion's coordinator + this fork) — flexibility to serve
// the API under different paths is hypothetical.
const (
	PathLookup     = "/sessions/lookup"
	PathActivate   = "/sessions/%s/activate"
	PathDeactivate = "/sessions/%s/deactivate"
)
