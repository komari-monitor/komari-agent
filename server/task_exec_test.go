package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunTaskCommandMultilineUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell script execution test")
	}

	result, exitCode := runTaskCommand("printf '%s\\n' first\nprintf '%s\\n' second\n")

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", exitCode, result)
	}
	if result != "first\nsecond\n" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestRunTaskCommandEscapingAndWildcardsUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell script execution test")
	}

	tempDir := t.TempDir()
	command := strings.Join([]string{
		"cd " + shellSingleQuote(tempDir),
		"touch alpha.txt beta.log gamma.txt",
		"printf '%s\\n' \"quoted value with spaces\"",
		"printf '%s\\n' '*.txt'",
		"printf '%s ' *.txt",
		"printf '\\n'",
	}, "\n")

	result, exitCode := runTaskCommand(command)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", exitCode, result)
	}
	lines := strings.Split(strings.TrimSuffix(result, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 output lines, got %d in %q", len(lines), result)
	}
	if lines[0] != "quoted value with spaces" {
		t.Fatalf("quoted argument was not preserved: %q", lines[0])
	}
	if lines[1] != "*.txt" {
		t.Fatalf("quoted wildcard should remain literal, got %q", lines[1])
	}
	expanded := strings.Fields(lines[2])
	if len(expanded) != 2 || expanded[0] != "alpha.txt" || expanded[1] != "gamma.txt" {
		t.Fatalf("wildcard expansion mismatch: %q", lines[2])
	}
}

func TestBuildTaskCommandUsesShellStdinUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell script execution test")
	}

	cmd, cleanup, err := buildTaskCommand("printf done")
	if err != nil {
		t.Fatalf("buildTaskCommand returned error: %v", err)
	}
	defer cleanup()

	if filepath.Base(cmd.Path) != "sh" {
		t.Fatalf("expected sh command, got %q", cmd.Path)
	}
	if strings.Join(cmd.Args, " ") != "sh -s" {
		t.Fatalf("expected sh -s args, got %#v", cmd.Args)
	}
	if cmd.Stdin == nil {
		t.Fatal("expected command script to be provided on stdin")
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
