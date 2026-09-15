//go:build linux

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMOTDWarningIsAppendedAndRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motd")
	const original = "Welcome to the server.\n\nUseful links here.\n"
	if err := os.WriteFile(path, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}

	cleanup, err := installMOTDWarning(path,
		newSecurityWarning("https://user:secret@panel.example.com:8443/path?token=secret", "root"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, original) {
		t.Fatalf("warning is not at the bottom after the original MOTD: %q", content)
	}
	if !strings.HasSuffix(content, warningUninstallURL+"\n") {
		t.Fatalf("unexpected warning ending: %q", content)
	}
	for _, want := range []string{
		motdWarningStart,
		"\x1b[33mpanel.example.com:8443\x1b[0m",
		"\x1b[31mexecute commands\x1b[0m",
		"\x1b[31mmodify files\x1b[0m",
		"\x1b[33mroot\x1b[0m",
		warningAdvice,
		warningCompromise,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
	if strings.Contains(content, "secret") {
		t.Fatal("MOTD exposed credentials")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("MOTD permissions changed: %v", err)
	}

	cleanup()
	cleanup()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("MOTD was not restored:\nwant %q\n got %q", original, data)
	}
}

func TestMOTDWarningIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motd")
	const original = "Welcome.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	firstCleanup, err := installMOTDWarning(path, newSecurityWarning("https://one.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	secondCleanup, err := installMOTDWarning(path, newSecurityWarning("https://two.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstCleanup)
	t.Cleanup(secondCleanup)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), motdWarningStart) != 1 || strings.Contains(string(data), "one.example.com") {
		t.Fatalf("warning was duplicated or not updated: %q", data)
	}
	if !strings.HasPrefix(string(data), original) || !strings.Contains(string(data), "two.example.com") {
		t.Fatalf("unexpected MOTD: %q", data)
	}
}

func TestMOTDWarningPreservesAdministratorChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motd")
	if err := os.WriteFile(path, []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup, err := installMOTDWarning(path, newSecurityWarning("https://panel.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("administrator change\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\nadministrator change\n" {
		t.Fatalf("administrator changes were not preserved: %q", data)
	}
}

func TestMOTDWarningCreatesAndRemovesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motd")
	cleanup, err := installMOTDWarning(path, newSecurityWarning("https://panel.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), motdWarningStart) {
		t.Fatalf("managed MOTD was not created: %q, %v", data, err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary MOTD was not removed: %v", err)
	}
}

func TestMOTDWarningIsRemovedWhenRemoteControlIsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motd")
	const original = "Welcome to the server.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup, err := installMOTDWarning(path, newSecurityWarning("https://panel.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	if err := removeInstalledMOTDWarning(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), motdWarningStart) {
		t.Fatalf("MOTD warning remained after remote control was disabled: %q", data)
	}
	if !strings.Contains(string(data), original) {
		t.Fatalf("original MOTD content was not preserved: %q", data)
	}
}

func TestMOTDWarningRejectsMalformedManagedBlock(t *testing.T) {
	for name, content := range map[string]string{
		"incomplete": motdWarningStart + "\npartial\n",
		"duplicate":  motdWarningStart + "\n" + motdWarningEnd + motdWarningStart + "\n" + motdWarningEnd,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "motd")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := installMOTDWarning(path, newSecurityWarning("https://panel.example.com", "root")); err == nil {
				t.Fatal("accepted a malformed managed block")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content {
				t.Fatal("modified a malformed managed block")
			}
		})
	}
}

func TestMOTDWarningFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "motd.target")
	link := filepath.Join(dir, "motd")
	if err := os.WriteFile(target, []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cleanup, err := installMOTDWarning(link, newSecurityWarning("https://panel.example.com", "root"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("MOTD symlink was replaced")
	}
	cleanup()
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "target\n" {
		t.Fatalf("symlink target was not restored: %q, %v", data, err)
	}
}

func TestLegacyUpdateMOTDHookCleanup(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed")
	if err := os.WriteFile(managed, []byte(legacyUpdateMOTDMarker+"\nprintf warning\n"), 0755); err != nil {
		t.Fatal(err)
	}
	removeLegacyUpdateMOTDWarning(managed)
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("legacy managed hook was not removed: %v", err)
	}

	unmanaged := filepath.Join(dir, "unmanaged")
	const content = "#!/bin/sh\nprintf administrator\n"
	if err := os.WriteFile(unmanaged, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	removeLegacyUpdateMOTDWarning(unmanaged)
	data, err := os.ReadFile(unmanaged)
	if err != nil || string(data) != content {
		t.Fatal("unmanaged legacy hook was changed")
	}
}
