//go:build !linux && !windows

package rescue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	rescuev1 "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1"
)

func isPrivileged() bool { return os.Geteuid() == 0 }

func platformCommand(ctx context.Context, name string, arguments ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func unsupported() (ActionResult, error) {
	return ActionResult{}, errors.New("the privileged rescue helper is supported only on Linux and Windows")
}

func machineDiagnostics(context.Context) (ActionResult, error)    { return unsupported() }
func preparePowerAction(ActionConfig, bool) (ActionResult, error) { return unsupported() }
func prepareAgentRestart(ActionConfig) (ActionResult, error)      { return unsupported() }

func grantTemporarySSHAccess(context.Context, ActionConfig, uint32) (ActionResult, error) {
	return unsupported()
}
func revokeTemporarySSHAccess(context.Context, ActionConfig) (ActionResult, error) {
	return unsupported()
}

// ReconcileTemporarySSHAccess and CloseTemporarySSHAccess have nothing to
// reconcile where the grant itself is unsupported, so they succeed rather than
// failing the helper's lease loop.
func ReconcileTemporarySSHAccess(context.Context, ActionConfig) error { return nil }
func CloseTemporarySSHAccess(context.Context, ActionConfig) error     { return nil }
func prepareInterfaceIsolation(ActionConfig, rescuev1.NetworkIsolationMode) (ActionResult, error) {
	return unsupported()
}
func prepareControlPlaneIsolation(context.Context, ActionConfig) (ActionResult, error) {
	return unsupported()
}
func restoreNetwork(context.Context, ActionConfig) (ActionResult, error) { return unsupported() }
func networkIsolationStatus(ActionConfig) (rescuev1.NetworkIsolationMode, []string) {
	return rescuev1.NetworkIsolationMode_NETWORK_ISOLATION_MODE_UNSPECIFIED, nil
}
