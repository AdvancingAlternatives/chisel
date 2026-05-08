package coordinator

import "testing"

func TestPathConstants(t *testing.T) {
	if PathLookup != "/sessions/lookup" {
		t.Errorf("PathLookup = %q, want /sessions/lookup", PathLookup)
	}
	if PathActivate != "/sessions/%s/activate" {
		t.Errorf("PathActivate = %q, want /sessions/%%s/activate", PathActivate)
	}
	if PathDeactivate != "/sessions/%s/deactivate" {
		t.Errorf("PathDeactivate = %q, want /sessions/%%s/deactivate", PathDeactivate)
	}
}
