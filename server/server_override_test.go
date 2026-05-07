package chserver

import (
	"testing"

	"github.com/jpillora/chisel/share/settings"
)

func TestOverrideReverseRemotePort(t *testing.T) {
	cases := []struct {
		name    string
		remotes []*settings.Remote
		port    int
		wantErr bool
		wantPort string
	}{
		{
			name: "single reverse",
			remotes: []*settings.Remote{
				{Reverse: true, LocalPort: "0"},
			},
			port:     22099,
			wantErr:  false,
			wantPort: "22099",
		},
		{
			name: "no reverse remote",
			remotes: []*settings.Remote{
				{Reverse: false, LocalPort: "0"},
			},
			port:    22099,
			wantErr: true,
		},
		{
			name:    "empty remotes",
			remotes: []*settings.Remote{},
			port:    22099,
			wantErr: true,
		},
		{
			name: "multiple reverses (β.1 is 1-remote per session, this is undefined behavior; pick the first)",
			remotes: []*settings.Remote{
				{Reverse: true, LocalPort: "0"},
				{Reverse: true, LocalPort: "0"},
			},
			port:     22099,
			wantErr:  false,
			wantPort: "22099",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := overrideReverseRemotePort(tc.remotes, tc.port)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if tc.remotes[0].LocalPort != tc.wantPort {
					t.Errorf("LocalPort = %q, want %q", tc.remotes[0].LocalPort, tc.wantPort)
				}
			}
		})
	}
}
