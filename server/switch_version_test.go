package server

import (
	"errors"
	"testing"

	"github.com/komari-monitor/komari-agent/update"
)

func TestRunSwitchVersion(t *testing.T) {
	tests := []struct {
		name             string
		err              error
		wantRestartCalls int
	}{
		{name: "no update needed"},
		{name: "restart after install", err: update.ErrRestartRequired, wantRestartCalls: 1},
		{name: "update failed", err: errors.New("failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restarts := 0
			called := false
			runSwitchVersion("1.2.*", func(version string) error {
				called = true
				if version != "1.2.*" {
					t.Fatalf("version = %q, want 1.2.*", version)
				}
				return tt.err
			}, func() {
				restarts++
			})
			if !called {
				t.Fatal("version checker was not called")
			}
			if restarts != tt.wantRestartCalls {
				t.Fatalf("restart calls = %d, want %d", restarts, tt.wantRestartCalls)
			}
		})
	}
}
