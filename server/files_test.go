package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

func TestFileOperationsRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "hello.txt")

	mustRunFileOperation(t, v2.FileOperation{
		Op:   "mkdir",
		Args: map[string]interface{}{"path": filepath.Dir(path), "mode": "0755"},
	})
	mustRunFileOperation(t, v2.FileOperation{
		Op:   "create",
		Args: map[string]interface{}{"path": path},
	})

	statted := mustRunFileOperation(t, v2.FileOperation{Op: "stat", Args: map[string]interface{}{"path": path}})
	var info fileInfo
	if err := json.Unmarshal(statted, &info); err != nil {
		t.Fatalf("decode stat result: %v", err)
	}
	if info.Path != virtualizeFilePath(path) || info.IsDir || info.Size != 0 {
		t.Fatalf("unexpected created file metadata: %+v", info)
	}

	listed := mustRunFileOperation(t, v2.FileOperation{Op: "list", Args: map[string]interface{}{"path": filepath.Dir(path)}})
	var items []fileInfo
	if err := json.Unmarshal(listed, &items); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	if len(items) != 1 || items[0].Name != "hello.txt" {
		t.Fatalf("unexpected list result: %+v", items)
	}
}

func TestCreateFileReplacesContentsAndPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := createFile(path); err != nil {
		t.Fatalf("createFile: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("created file contains %d bytes", len(content))
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestCopyDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "note.txt"), []byte("copied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := copyPath(source, destination); err != nil {
		t.Fatalf("copy directory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "nested", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "copied" {
		t.Fatalf("copied content = %q", content)
	}
	if _, err := copyPath(source, filepath.Join(source, "child")); err == nil {
		t.Fatal("copy into source directory was accepted")
	}
}

func TestListFilesResolvesSymlinkTargetKind(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-dir")
	targetFile := filepath.Join(root, "target-file.txt")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(root, "dir-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(targetFile, filepath.Join(root, "file-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	listed := mustRunFileOperation(t, v2.FileOperation{Op: "list", Args: map[string]interface{}{"path": root}})
	var items []fileInfo
	if err := json.Unmarshal(listed, &items); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	kinds := make(map[string]bool, 2)
	targets := make(map[string]string, 2)
	for _, item := range items {
		if item.Name == "dir-link" || item.Name == "file-link" {
			kinds[item.Name] = item.IsDir
			targets[item.Name] = item.Target
		}
	}
	if !kinds["dir-link"] {
		t.Fatal("directory symlink was not reported as a directory")
	}
	if kinds["file-link"] {
		t.Fatal("file symlink was reported as a directory")
	}
	if targets["dir-link"] != targetDir || targets["file-link"] != targetFile {
		t.Fatalf("unexpected symlink targets: %+v", targets)
	}

	statted := mustRunFileOperation(t, v2.FileOperation{Op: "stat", Args: map[string]interface{}{"path": filepath.Join(root, "dir-link")}})
	var statItem fileInfo
	if err := json.Unmarshal(statted, &statItem); err != nil {
		t.Fatalf("decode stat result: %v", err)
	}
	if !statItem.IsSymlink || !statItem.IsDir {
		t.Fatalf("stat result lost symlink metadata: %+v", statItem)
	}
}

func TestResolveFilePathTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home directory: %v", err)
	}
	if got := resolveFilePath("~"); got != home {
		t.Fatalf("resolveFilePath(~) = %q, want %q", got, home)
	}
	if got, want := resolveFilePath("~/nested"), filepath.Join(home, "nested"); got != want {
		t.Fatalf("resolveFilePath(~/nested) = %q, want %q", got, want)
	}
}

func TestFileOperationsRejectedWhenWebControlDisabled(t *testing.T) {
	original := pkg_flags.GlobalConfig.DisableWebSsh
	pkg_flags.GlobalConfig.DisableWebSsh = true
	defer func() {
		pkg_flags.GlobalConfig.DisableWebSsh = original
	}()

	if _, err := executeFileOperation(v2.FileOperation{
		Op:   "list",
		Args: map[string]interface{}{"path": "/"},
	}); err == nil {
		t.Fatal("expected file operation to be rejected when web control is disabled")
	}
}

func TestLegacyDataOperationsAreRemoved(t *testing.T) {
	for _, operation := range []string{"read", "write", "read_chunk", "download_init", "download_chunk", "download_finish", "upload_chunk"} {
		if _, err := executeFileOperation(v2.FileOperation{Op: operation}); err == nil {
			t.Fatalf("legacy operation %q is still supported", operation)
		}
	}
}

func mustRunFileOperation(t *testing.T, operation v2.FileOperation) json.RawMessage {
	t.Helper()
	result := runFileOperation(operation)
	if !result.OK {
		t.Fatalf("operation %s failed: %s", operation.Op, result.Error)
	}
	return result.Result
}

func TestDeletePathRejectsFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(filepath.Clean(os.TempDir())) + string(filepath.Separator)
	if _, err := deletePath(root); err == nil {
		t.Fatalf("deletePath(%q) did not reject filesystem root", root)
	}
}
