package rescue

import (
	"context"
	"strings"
	"testing"

	rescuev1 "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1"
)

func TestExecuteActionRejectsRemoteArguments(t *testing.T) {
	_, err := ExecuteAction(context.Background(), ActionConfig{}, rescuev1.RescueAction_RESCUE_ACTION_DIAGNOSTICS, []string{"whoami"}, 0)
	if err == nil || !strings.Contains(err.Error(), "do not accept remote arguments") {
		t.Fatalf("ExecuteAction() error = %v, want remote argument rejection", err)
	}
}

// The temporary SSH actions take a typed port, so arguments must stay rejected
// for them too: accepting a port through arguments would reopen the very hole
// the blanket rejection exists to close.
func TestExecuteActionRejectsArgumentsForTemporarySSHAccess(t *testing.T) {
	_, err := ExecuteAction(context.Background(), ActionConfig{}, rescuev1.RescueAction_RESCUE_ACTION_TEMPORARY_SSH_ACCESS, []string{"22"}, 22)
	if err == nil || !strings.Contains(err.Error(), "do not accept remote arguments") {
		t.Fatalf("ExecuteAction() error = %v, want remote argument rejection", err)
	}
}

func TestExecuteActionRejectsUnspecifiedAction(t *testing.T) {
	_, err := ExecuteAction(context.Background(), ActionConfig{}, rescuev1.RescueAction_RESCUE_ACTION_UNSPECIFIED, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "unsupported rescue action") {
		t.Fatalf("ExecuteAction() error = %v, want unsupported action rejection", err)
	}
}

func TestExecuteActionRejectsDeprecatedActions(t *testing.T) {
	for _, action := range []rescuev1.RescueAction{
		rescuev1.RescueAction_RESCUE_ACTION_VERIFY_INSTALLATION,
		rescuev1.RescueAction_RESCUE_ACTION_RESTORE_LAST_CONFIG,
		rescuev1.RescueAction_RESCUE_ACTION_ROLLBACK_RUNTIME_SNAPSHOT,
		rescuev1.RescueAction_RESCUE_ACTION_REPAIR_FIREWALL,
		rescuev1.RescueAction_RESCUE_ACTION_RESTART_AGENT,
	} {
		if _, err := ExecuteAction(context.Background(), ActionConfig{}, action, nil, 0); err == nil || !strings.Contains(err.Error(), "unsupported rescue action") {
			t.Errorf("ExecuteAction(%s) error = %v, want unsupported action rejection", action, err)
		}
	}
}

func TestNormalizeEndpointRejectsNonHTTPTransport(t *testing.T) {
	if _, err := normalizeEndpoint("file:///tmp/socket"); err == nil {
		t.Fatal("normalizeEndpoint() accepted a non-HTTP transport")
	}
}

func TestFinalOutputIsBoundedAndMarksStderr(t *testing.T) {
	result := finalOutput(ActionResult{Stdout: []byte("ok"), Stderr: []byte("warning")}, 7)
	if len(result) > maximumEventOutput || !strings.Contains(string(result), "[stderr]\nwarning") {
		t.Fatalf("finalOutput() = %q", result)
	}
}
