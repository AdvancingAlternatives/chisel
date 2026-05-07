package coordinator

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	all := []error{ErrNotFound, ErrConflict, ErrAuth, ErrTransient}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("%v errors.Is %v unexpectedly", a, b)
			}
		}
	}
}

func TestSentinelErrorsWrapForCallers(t *testing.T) {
	wrapped := fmt.Errorf("call: %w", ErrConflict)
	if !errors.Is(wrapped, ErrConflict) {
		t.Error("errors.Is fails on wrapped sentinel")
	}
	if errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is matches unrelated sentinel")
	}
}
