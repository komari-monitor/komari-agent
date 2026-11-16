package netstatic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withSavePath(t *testing.T, p string) {
	t.Helper()
	old := SaveFilePath
	SaveFilePath = p
	saveFailed = false
	t.Cleanup(func() { SaveFilePath = old; saveFailed = false })
}

func TestCheckWritableOK(t *testing.T) {
	dir := t.TempDir()
	withSavePath(t, filepath.Join(dir, "sub", "net_static.json"))
	if err := checkWritable(); err != nil {
		t.Fatalf("expected writable, got %v", err)
	}
	// must not leave the probe or create the real data file behind
	entries, _ := os.ReadDir(filepath.Join(dir, "sub"))
	if len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

func TestCheckWritableFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the parent "directory" is a regular file, so this fails for root as well
	withSavePath(t, filepath.Join(blocker, "net_static.json"))
	if err := checkWritable(); err == nil {
		t.Fatal("expected an error when the directory cannot be created")
	}
}

func TestReportSaveLogsOnceAndRearms(t *testing.T) {
	withSavePath(t, "x")
	boom := errors.New("boom")
	if !reportSave(boom) {
		t.Fatal("first failure must be logged")
	}
	if reportSave(boom) || reportSave(boom) {
		t.Fatal("repeated failures must not be logged again")
	}
	if reportSave(nil) {
		t.Fatal("success must not log")
	}
	if !reportSave(boom) {
		t.Fatal("a failure after a recovery must be logged again")
	}
}
