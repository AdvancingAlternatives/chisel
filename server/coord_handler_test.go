package chserver

import (
	"errors"
	"testing"

	"github.com/jpillora/chisel/server/coordinator"
)

func TestRejectChiselMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not found", coordinator.ErrNotFound, "no pending session"},
		{"auth", coordinator.ErrAuth, "coordinator unreachable"},
		{"transient", coordinator.ErrTransient, "coordinator unreachable, retry in flight"},
		{"conflict", coordinator.ErrConflict, "state divergence on activate"},
		{"unknown", errors.New("weird"), "coordinator error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := rejectMessage(tc.err)
			if !contains(msg, tc.want) {
				t.Errorf("rejectMessage(%v) = %q, want it to contain %q", tc.err, msg, tc.want)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
