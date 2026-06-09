//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTaskCommandCreatesPowerShellScriptWindows(t *testing.T) {
	cmd, cleanup, err := buildTaskCommand("Write-Output 'hello'")
	if err != nil {
		t.Fatalf("buildTaskCommand returned error: %v", err)
	}
	t.Cleanup(cleanup)

	if !isPowerShellExecutable(filepath.Base(cmd.Path)) {
		t.Fatalf("expected powershell command, got %q", cmd.Path)
	}
	if len(cmd.Args) != 6 {
		t.Fatalf("expected powershell -File args, got %#v", cmd.Args)
	}
	if cmd.Args[1] != "-NoProfile" || cmd.Args[2] != "-ExecutionPolicy" || cmd.Args[3] != "Bypass" || cmd.Args[4] != "-File" {
		t.Fatalf("unexpected powershell args: %#v", cmd.Args)
	}

	scriptPath := cmd.Args[5]
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("expected temp script to exist: %v", err)
	}
	if !strings.Contains(string(content), "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8") {
		t.Fatalf("expected UTF-8 output prelude in temp script, got %q", string(content))
	}
	if !strings.Contains(string(content), "Write-Output 'hello'") {
		t.Fatalf("expected command body in temp script, got %q", string(content))
	}

	cleanup()
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup to remove temp script, stat error: %v", err)
	}
}

func isPowerShellExecutable(name string) bool {
	return strings.EqualFold(name, "powershell") ||
		strings.EqualFold(name, "powershell.exe") ||
		strings.EqualFold(name, "pwsh") ||
		strings.EqualFold(name, "pwsh.exe")
}
