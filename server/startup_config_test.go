package server

import (
	"strings"
	"testing"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

// TestCaptureStartupConfigRedactsCredentials verifies the startup
// configuration snapshot never carries plaintext token / auto_discovery_key
// while preserving non-sensitive operational fields like month_rotate.
func TestCaptureStartupConfigRedactsCredentials(t *testing.T) {
	prevToken, prevKey, prevRotate := pkg_flags.GlobalConfig.Token, pkg_flags.GlobalConfig.AutoDiscoveryKey, pkg_flags.GlobalConfig.MonthRotate
	pkg_flags.GlobalConfig.Token = "secret-token-xyz"
	pkg_flags.GlobalConfig.AutoDiscoveryKey = "cluster-secret-key"
	pkg_flags.GlobalConfig.MonthRotate = 15
	t.Cleanup(func() {
		pkg_flags.GlobalConfig.Token, pkg_flags.GlobalConfig.AutoDiscoveryKey, pkg_flags.GlobalConfig.MonthRotate = prevToken, prevKey, prevRotate
	})

	if err := pkg_flags.CaptureStartupConfig(nil); err != nil {
		t.Fatalf("CaptureStartupConfig failed: %v", err)
	}
	config := pkg_flags.StartupConfig()
	if config == nil {
		t.Fatal("StartupConfig returned nil after capture")
	}
	for _, key := range []string{"token", "auto_discovery_key"} {
		v, ok := config[key]
		if !ok {
			t.Fatalf("key %q missing from startup config", key)
		}
		s, isStr := v.(string)
		if !isStr || s != "[REDACTED]" {
			t.Fatalf("key %q = %v, want [REDACTED] (plaintext credential leak)", key, v)
		}
	}
	if rotate, ok := config["month_rotate"]; !ok || rotate != float64(15) {
		t.Fatalf("month_rotate = %v (ok=%v), want 15 preserved", rotate, ok)
	}

	// Empty credentials stay empty (no placeholder noise).
	pkg_flags.GlobalConfig.Token = ""
	pkg_flags.GlobalConfig.AutoDiscoveryKey = ""
	if err := pkg_flags.CaptureStartupConfig(nil); err != nil {
		t.Fatalf("CaptureStartupConfig with empty creds failed: %v", err)
	}
	config = pkg_flags.StartupConfig()
	if s, _ := config["token"].(string); s != "" {
		t.Fatalf("empty token became %q, want empty", s)
	}
	if s, _ := config["auto_discovery_key"].(string); s != "" {
		t.Fatalf("empty auto_discovery_key became %q, want empty", s)
	}
	// Serialized form must never embed either plaintext secret anywhere.
	pkg_flags.GlobalConfig.Token = "secret-token-xyz"
	pkg_flags.GlobalConfig.AutoDiscoveryKey = "cluster-secret-key"
	if err := pkg_flags.CaptureStartupConfig(nil); err != nil {
		t.Fatalf("recapture failed: %v", err)
	}
	config = pkg_flags.StartupConfig()
	for _, secret := range []string{"secret-token-xyz", "cluster-secret-key"} {
		for key, value := range config {
			if s, isStr := value.(string); isStr && strings.Contains(s, secret) {
				t.Fatalf("plaintext secret %q leaked into startup config key %q", secret, key)
			}
		}
	}
}
