package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func TestFileOperationsRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "hello.txt")

	mustRunFileOperation(t, v2.FileOperation{
		Op:   "mkdir",
		Args: map[string]interface{}{"path": filepath.Dir(path), "mode": "0755"},
	})
	mustRunFileOperation(t, v2.FileOperation{
		Op:   "write",
		Args: map[string]interface{}{"path": path},
		Data: base64.StdEncoding.EncodeToString([]byte("hello komari")),
	})

	read := mustRunFileOperation(t, v2.FileOperation{Op: "read", Args: map[string]interface{}{"path": path}})
	var content fileReadResult
	if err := json.Unmarshal(read, &content); err != nil {
		t.Fatalf("decode read result: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(content.Data)
	if err != nil {
		t.Fatalf("decode file data: %v", err)
	}
	if got, want := string(decoded), "hello komari"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if content.Binary {
		t.Fatal("text file was marked as binary")
	}

	mustRunFileOperation(t, v2.FileOperation{
		Op:   "write",
		Args: map[string]interface{}{"path": path},
		Data: base64.StdEncoding.EncodeToString([]byte("updated")),
	})
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if got, want := string(updated), "updated"; got != want {
		t.Fatalf("updated content = %q, want %q", got, want)
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

func TestFileReadTreatsReadableBinaryHeaderAsText(t *testing.T) {
	readableHeader := append([]byte("custom-format: version 1\nname=demo\n"), 0, 0, 0, 0)
	if isBinaryContent(readableHeader) {
		t.Fatal("readable file header was classified as binary")
	}
	readablePrefix := append(bytes.Repeat([]byte{'a'}, 512), 0xff)
	if isBinaryContent(readablePrefix) {
		t.Fatal("readable prefix was classified as binary")
	}

	if !isBinaryContent([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatal("binary header was classified as text")
	}
}

func TestFileReadTreatsCommonLegacyTextAsText(t *testing.T) {
	for _, test := range []struct {
		name    string
		charset encoding.Encoding
		value   string
	}{
		{name: "gb18030", charset: simplifiedchinese.GB18030, value: "中文配置文件\n第二行"},
		{name: "big5", charset: traditionalchinese.Big5, value: "繁體中文設定檔\n第二行"},
	} {
		encoded, err := test.charset.NewEncoder().Bytes([]byte(test.value))
		if err != nil {
			t.Fatalf("encode %s sample: %v", test.name, err)
		}
		if isBinaryContent(encoded) {
			t.Fatalf("%s text was classified as binary", test.name)
		}
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

func TestFileChunkUploadAndDownload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.bin")
	content := []byte("first-second")
	totalSize := int64(len(content))
	chunkCount := uploadChunkCount(totalSize, fileChunkSize)
	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        path,
			"offset":      float64(0),
			"chunk_index": float64(0),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "test-upload",
			"first":       true,
		},
		Data: base64.StdEncoding.EncodeToString(content),
	})
	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        path,
			"offset":      float64(totalSize),
			"chunk_index": float64(chunkCount),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "test-upload",
			"final":       true,
		},
	})

	chunk := mustRunFileOperation(t, v2.FileOperation{
		Op: "read_chunk",
		Args: map[string]interface{}{
			"path":   path,
			"offset": float64(0),
			"length": float64(fileChunkSize),
		},
	})
	var result struct {
		Data string `json:"data"`
		Read int    `json:"read"`
		EOF  bool   `json:"eof"`
	}
	if err := json.Unmarshal(chunk, &result); err != nil {
		t.Fatalf("decode chunk result: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatalf("decode chunk data: %v", err)
	}
	if got, want := string(decoded), string(content); got != want {
		t.Fatalf("chunk = %q, want %q", got, want)
	}
	if !result.EOF || result.Read != len(decoded) {
		t.Fatalf("unexpected chunk metadata: %+v", result)
	}
}

func TestFileChunkUploadResume(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "resume-target.txt")
	first := bytes.Repeat([]byte{'a'}, int(fileChunkSize))
	second := []byte("tail")
	totalSize := int64(len(first) + len(second))
	chunkCount := uploadChunkCount(totalSize, fileChunkSize)

	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        targetPath,
			"offset":      float64(0),
			"chunk_index": float64(0),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "resume-upload",
			"first":       true,
		},
		Data: base64.StdEncoding.EncodeToString(first),
	})

	// Retrying the first chunk must restart only that upload and remain safe.
	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        targetPath,
			"offset":      float64(0),
			"chunk_index": float64(0),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "resume-upload",
			"first":       true,
		},
		Data: base64.StdEncoding.EncodeToString(first),
	})

	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        targetPath,
			"offset":      float64(fileChunkSize),
			"chunk_index": float64(1),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "resume-upload",
		},
		Data: base64.StdEncoding.EncodeToString(second),
	})
	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        targetPath,
			"offset":      float64(totalSize),
			"chunk_index": float64(chunkCount),
			"chunk_count": float64(chunkCount),
			"total_size":  float64(totalSize),
			"upload_id":   "resume-upload",
			"final":       true,
		},
	})

	result, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read resumed file: %v", err)
	}
	wantContent := append(append([]byte(nil), first...), second...)
	if !bytes.Equal(result, wantContent) {
		t.Fatalf("unexpected resumed content size: got %d, want %d", len(result), len(wantContent))
	}
}

func TestFileChunkUploadCancelRemovesPart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.bin")
	uploadID := "cancel-upload"
	chunk := []byte("partial")
	mustRunFileOperation(t, v2.FileOperation{
		Op: "upload_chunk",
		Args: map[string]interface{}{
			"path":        path,
			"offset":      float64(0),
			"chunk_index": float64(0),
			"chunk_count": float64(1),
			"total_size":  float64(len(chunk)),
			"upload_id":   uploadID,
			"first":       true,
		},
		Data: base64.StdEncoding.EncodeToString(chunk),
	})

	mustRunFileOperation(t, v2.FileOperation{
		Op:   "upload_cancel",
		Args: map[string]interface{}{"upload_id": uploadID, "path": path},
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("target file exists after cancel: %v", err)
	}
	if _, err := os.Stat(uploadPartPathFor(path, uploadID)); !os.IsNotExist(err) {
		t.Fatalf("part file exists after cancel: %v", err)
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
