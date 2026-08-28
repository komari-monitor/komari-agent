package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

const (
	maxEditableFileSize  = int64(4 * 1024 * 1024)
	fileChunkSize        = int64(2 * 1024 * 1024)
	maxTransferChunkSize = int64(128 * 1024 * 1024)
	searchResultLimit    = 500
	downloadSessionTTL   = 15 * time.Minute
)

type fileInfo struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	IsDir      bool      `json:"is_dir"`
	IsSymlink  bool      `json:"is_symlink"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	ModeOctal  string    `json:"mode_octal"`
	UID        int       `json:"uid"`
	GID        int       `json:"gid"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	ModifiedAt time.Time `json:"modified_at"`
	Target     string    `json:"target,omitempty"`
}

type uploadChunkState struct {
	Size         int64
	ExpectedSize int64
	ChunkSize    int64
	TargetPath   string
	Parts        map[int64]struct{}
	PartCount    int64
	TempPath     string
	CreatedAt    time.Time
}

type downloadSessionState struct {
	Path      string
	Offset    int64
	Size      int64
	ChunkSize int64
	CreatedAt time.Time
}

var (
	uploadChunksMu     sync.Mutex
	uploadChunks       = make(map[string]uploadChunkState)
	downloadSessionsMu sync.Mutex
	downloadSessions   = make(map[string]downloadSessionState)
)

type searchMatch struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Text  string `json:"text,omitempty"`
	IsDir bool   `json:"is_dir"`
}

type fileReadResult struct {
	Data        string    `json:"data"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
	ContentType string    `json:"content_type"`
	Binary      bool      `json:"binary"`
}

func handleFileOperation(operation v2.FileOperation) {
	result := runFileOperation(operation)
	postFileResult(result)
}

func runFileOperation(operation v2.FileOperation) v2.FileResult {
	result := v2.FileResult{UUID: operation.UUID, RequestID: operation.RequestID}
	payload, err := executeFileOperation(operation)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.OK = true
	result.Result = payload
	return result
}

func executeFileOperation(operation v2.FileOperation) (json.RawMessage, error) {
	if pkg_flags.GlobalConfig.DisableWebSsh {
		return nil, errors.New("web control is disabled")
	}
	switch operation.Op {
	case "list":
		return listFiles(argString(operation.Args, "path"))
	case "list_roots":
		return listFilesystemRoots()
	case "stat":
		return statFile(argString(operation.Args, "path"))
	case "read":
		return readFile(argString(operation.Args, "path"))
	case "write":
		return writeFile(argString(operation.Args, "path"), operation.Data)
	case "mkdir":
		return mkdir(argString(operation.Args, "path"), argString(operation.Args, "mode"))
	case "delete":
		return deletePath(argString(operation.Args, "path"))
	case "move":
		return movePath(argString(operation.Args, "source"), argString(operation.Args, "destination"))
	case "copy":
		return copyPath(argString(operation.Args, "source"), argString(operation.Args, "destination"))
	case "chmod":
		return chmodPath(argString(operation.Args, "path"), argString(operation.Args, "mode"))
	case "chown":
		return chownPath(operation.Args)
	case "search":
		return searchFiles(operation.Args)
	case "read_chunk":
		return readFileChunk(operation.Args)
	case "download_init":
		return initFileDownload(operation.Args)
	case "download_chunk":
		return readFileDownloadChunk(operation.Args)
	case "download_finish", "download_cancel":
		return finishFileDownload(operation.Args)
	case "upload_chunk":
		return writeFileChunk(operation.Args, operation.Data)
	case "upload_cancel":
		return cancelFileUpload(operation.Args)
	default:
		return nil, fmt.Errorf("unsupported file operation: %s", operation.Op)
	}
}

func listFiles(root string) (json.RawMessage, error) {
	if runtime.GOOS == "windows" && strings.TrimSpace(root) == "/" {
		return listVirtualRootEntries()
	}
	root = resolveFilePath(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := describeFile(filepath.Join(root, entry.Name()), entry.Type()&fs.ModeSymlink != 0)
		if err != nil {
			continue
		}
		files = append(files, info)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return json.Marshal(files)
}

func statFile(path string) (json.RawMessage, error) {
	path = resolveFilePath(path)
	info, err := describeFile(path, isSymlink(path))
	if err != nil {
		return nil, err
	}
	return json.Marshal(info)
}

func readFile(path string) (json.RawMessage, error) {
	path = resolveFilePath(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("cannot read a directory")
	}
	if info.Size() > maxEditableFileSize {
		return nil, fmt.Errorf("file exceeds the %d byte edit limit", maxEditableFileSize)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fileReadResult{
		Data:        base64.StdEncoding.EncodeToString(content),
		Size:        info.Size(),
		ModifiedAt:  info.ModTime().UTC(),
		ContentType: http.DetectContentType(content),
		Binary:      isBinaryContent(content),
	})
}

// isBinaryContent deliberately inspects the beginning of a file rather than
// rejecting every file that contains a NUL byte. A number of useful formats
// start with a readable header and only become binary later; the workbench can
// still present those files as text when their prefix is unambiguously readable.
func isBinaryContent(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	sample := content
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		sample = sample[3:]
	}
	if nul := bytes.IndexByte(sample, 0); nul >= 0 {
		sample = sample[:nul]
	}
	return !looksLikeText(sample)
}

func looksLikeText(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if !utf8.Valid(sample) {
		return looksLikeLegacyText(sample)
	}
	runes := []rune(string(sample))
	if len(runes) == 0 {
		return true
	}
	readable := 0
	for _, value := range runes {
		switch value {
		case '\n', '\r', '\t', '\f':
			readable++
		default:
			if unicode.IsControl(value) || !unicode.IsPrint(value) {
				return false
			}
			readable++
		}
	}
	return readable == len(runes)
}

func looksLikeLegacyText(sample []byte) bool {
	for _, charset := range []encoding.Encoding{
		simplifiedchinese.GB18030,
		traditionalchinese.Big5,
	} {
		decoded, err := charset.NewDecoder().Bytes(sample)
		if err != nil || !utf8.Valid(decoded) {
			continue
		}
		if looksLikeUTF8Text(decoded) {
			return true
		}
	}
	return false
}

func looksLikeUTF8Text(sample []byte) bool {
	if len(sample) == 0 || !utf8.Valid(sample) {
		return false
	}
	runes := []rune(string(sample))
	if len(runes) == 0 {
		return true
	}
	for _, value := range runes {
		switch value {
		case '\n', '\r', '\t', '\f':
			continue
		default:
			if unicode.IsControl(value) || !unicode.IsPrint(value) {
				return false
			}
		}
	}
	return true
}

func writeFile(path, encoded string) (json.RawMessage, error) {
	path = resolveFilePath(path)
	content, err := decodeData(encoded)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxEditableFileSize {
		return nil, fmt.Errorf("file exceeds the %d byte edit limit", maxEditableFileSize)
	}
	if err := atomicWrite(path, content); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"saved": true, "size": len(content)})
}

func mkdir(path, modeValue string) (json.RawMessage, error) {
	path = resolveFilePath(path)
	mode := fs.FileMode(0o755)
	if strings.TrimSpace(modeValue) != "" {
		parsed, err := parseMode(modeValue)
		if err != nil {
			return nil, err
		}
		mode = fs.FileMode(parsed)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"created": true})
}

func deletePath(path string) (json.RawMessage, error) {
	cleaned := resolveFilePath(path)
	volume := filepath.VolumeName(cleaned)
	volumeRoot := ""
	if volume != "" {
		volumeRoot = filepath.Clean(volume + string(filepath.Separator))
	}
	if cleaned == string(filepath.Separator) || cleaned == volumeRoot {
		return nil, errors.New("refusing to delete a filesystem root")
	}
	if _, err := os.Lstat(cleaned); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(cleaned); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"deleted": true})
}

func movePath(source, destination string) (json.RawMessage, error) {
	source = resolveFilePath(source)
	destination = resolveFilePath(destination)
	if samePath(source, destination) {
		return json.Marshal(map[string]any{"moved": true})
	}
	if pathContains(source, destination) {
		return nil, errors.New("cannot move a path into itself")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(source, destination); err != nil {
		if !isCrossDeviceRename(err) {
			return nil, err
		}
		// Windows and Unix both reject rename across volumes. Fall back to a
		// copy followed by removal so moving between drive letters works too.
		info, statErr := os.Lstat(source)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if copyErr := copySymlink(source, destination); copyErr != nil {
				return nil, copyErr
			}
		} else if info.IsDir() {
			if copyErr := copyDirectory(source, destination); copyErr != nil {
				return nil, copyErr
			}
		} else if copyErr := copyRegularFile(source, destination, info); copyErr != nil {
			return nil, copyErr
		}
		if removeErr := os.RemoveAll(source); removeErr != nil {
			return nil, removeErr
		}
	}
	return json.Marshal(map[string]any{"moved": true})
}

func isCrossDeviceRename(err error) bool {
	return errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.Errno(17))
}

func copyPath(source, destination string) (json.RawMessage, error) {
	source = resolveFilePath(source)
	destination = resolveFilePath(destination)
	if samePath(source, destination) {
		return nil, errors.New("source and destination are the same")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		if pathContains(source, destination) {
			return nil, errors.New("cannot copy a directory into itself")
		}
		if err := copyDirectory(source, destination); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"copied": true})
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := copySymlink(source, destination); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"copied": true})
	}
	if err := copyRegularFile(source, destination, info); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"copied": true})
}

func copyRegularFile(source, destination string, info os.FileInfo) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err = output.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	if err = os.Chmod(destination, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

func copySymlink(source, destination string) error {
	target, err := os.Readlink(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if existing, err := os.Lstat(destination); err == nil {
		if existing.IsDir() && existing.Mode()&os.ModeSymlink == 0 {
			return errors.New("cannot replace destination directory with a symlink")
		}
		if err := os.Remove(destination); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, destination)
}

func copyDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return copySymlink(path, target)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			if err := os.Chmod(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		}
		return copyRegularFile(path, target, info)
	})
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(relative)
}
func chmodPath(path, modeValue string) (json.RawMessage, error) {
	path = resolveFilePath(path)
	mode, err := parseMode(modeValue)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, fs.FileMode(mode)); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"mode": fmt.Sprintf("%04o", mode)})
}

func chownPath(args map[string]interface{}) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	uid := -1
	gid := -1
	if _, exists := args["uid"]; exists {
		uid = int(argInt64(args, "uid"))
	}
	if owner := argString(args, "owner"); owner != "" {
		resolved, err := resolveUnixAccount(owner, true)
		if err != nil {
			return nil, err
		}
		uid = resolved
	}
	if _, exists := args["gid"]; exists {
		gid = int(argInt64(args, "gid"))
	}
	if group := argString(args, "group"); group != "" {
		resolved, err := resolveUnixAccount(group, false)
		if err != nil {
			return nil, err
		}
		gid = resolved
	}
	if err := changeOwnership(path, uid, gid); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"uid": uid, "gid": gid})
}

func searchFiles(args map[string]interface{}) (json.RawMessage, error) {
	query := argString(args, "query")
	includeContent := argBool(args, "content")
	if query == "" {
		return nil, errors.New("query is required")
	}

	var roots []string
	if runtime.GOOS == "windows" && strings.TrimSpace(argString(args, "path")) == "/" {
		roots = virtualRootSearchPaths()
	} else {
		roots = []string{resolveFilePath(argString(args, "path"))}
	}
	lowerQuery := strings.ToLower(query)

	matches := make([]searchMatch, 0)
	searchRootFiles := func(root string) error {
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if len(matches) >= searchResultLimit {
				return fs.SkipAll
			}
			if walkErr != nil {
				if path == root {
					return walkErr
				}
				if entry != nil && entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			nameMatch := strings.Contains(strings.ToLower(entry.Name()), lowerQuery)
			isSymlink := entry.Type()&fs.ModeSymlink != 0
			if nameMatch && !includeContent {
				matches = append(matches, searchMatch{
					Path:  virtualizeFilePath(path),
					IsDir: entry.IsDir(),
				})
			} else if includeContent && !entry.IsDir() && !isSymlink {
				if match, ok := matchFileContent(path, query, lowerQuery); ok && match.Line > 0 {
					matches = append(matches, match)
					return nil
				}
			}
			return nil
		})
	}
	for _, root := range roots {
		if err := searchRootFiles(root); err != nil && !errors.Is(err, fs.SkipAll) {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"matches": matches, "limited": len(matches) >= searchResultLimit})
}

func uploadChunkCount(size, chunkSize int64) int64 {
	if size <= 0 || chunkSize <= 0 {
		return 0
	}
	return (size + chunkSize - 1) / chunkSize
}

func uploadPartPathFor(targetPath, uploadID string) string {
	target := filepath.ToSlash(targetPath)
	name := filepath.Base(target)
	if name == "" || name == "." || name == "/" {
		name = "upload"
	}
	return filepath.Join(filepath.Dir(target), "."+name+".komari-upload-"+uploadID+".part")
}

func syncUploadDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

func removeUploadFileLocked(uploadID string) error {
	state, ok := uploadChunks[uploadID]
	if !ok {
		return nil
	}
	if state.TempPath != "" {
		if err := os.Remove(state.TempPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	delete(uploadChunks, uploadID)
	return nil
}

func writeUploadChunkLocked(uploadID, targetPath string, offset int64, content []byte, truncate bool) error {
	state := uploadChunks[uploadID]
	partPath := state.TempPath
	if partPath == "" {
		partPath = uploadPartPathFor(targetPath, uploadID)
	} else if filepath.Clean(partPath) != filepath.Clean(uploadPartPathFor(targetPath, uploadID)) {
		partPath = uploadPartPathFor(targetPath, uploadID)
	}
	openFlags := os.O_WRONLY | os.O_CREATE
	if truncate {
		openFlags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, openFlags, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	state.Size = max(state.Size, offset+int64(len(content)))
	if state.TargetPath == "" {
		state.TargetPath = targetPath
	}
	if state.Parts == nil {
		state.Parts = make(map[int64]struct{})
	}
	chunkSize := state.ChunkSize
	if chunkSize <= 0 {
		chunkSize = fileChunkSize
	}
	state.Parts[offset/chunkSize] = struct{}{}
	state.TempPath = partPath
	state.CreatedAt = time.Now()
	uploadChunks[uploadID] = state
	return nil
}

func cancelFileUpload(args map[string]interface{}) (json.RawMessage, error) {
	uploadID := strings.TrimSpace(argString(args, "upload_id"))
	if uploadID == "" {
		return nil, errors.New("upload_id is required")
	}
	targetPath := strings.TrimSpace(argString(args, "path"))
	if targetPath != "" {
		targetPath = resolveFilePath(targetPath)
	}

	uploadChunksMu.Lock()
	state, exists := uploadChunks[uploadID]
	if exists {
		if targetPath != "" && state.TargetPath != "" && !samePath(targetPath, state.TargetPath) {
			uploadChunksMu.Unlock()
			return nil, errors.New("upload target path does not match session")
		}
		if err := removeUploadFileLocked(uploadID); err != nil {
			uploadChunksMu.Unlock()
			return nil, err
		}
		uploadChunksMu.Unlock()
		return json.Marshal(map[string]any{"cancelled": true})
	}
	uploadChunksMu.Unlock()

	// A process restart can discard the in-memory state while leaving the part
	// file behind. Derive the deterministic path and remove only that file.
	if targetPath != "" {
		partPath := uploadPartPathFor(targetPath, uploadID)
		if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"cancelled": true})
}
func initFileDownload(args map[string]interface{}) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	offset := argInt64(args, "offset")
	size := argInt64(args, "size")
	chunkSize := argInt64(args, "chunk_size")
	if offset < 0 || size < 0 {
		return nil, errors.New("download offset and size must not be negative")
	}
	if chunkSize == 0 {
		chunkSize = fileChunkSize
	}
	if chunkSize <= 0 || chunkSize > maxTransferChunkSize {
		return nil, fmt.Errorf("chunk_size must be between 1 and %d bytes", maxTransferChunkSize)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("cannot download a directory")
	}
	if offset > info.Size() || size > info.Size()-offset {
		return nil, errors.New("download range exceeds file size")
	}

	id := newFileTransferID()
	now := time.Now()
	downloadSessionsMu.Lock()
	for sessionID, session := range downloadSessions {
		if now.Sub(session.CreatedAt) > downloadSessionTTL {
			delete(downloadSessions, sessionID)
		}
	}
	downloadSessions[id] = downloadSessionState{
		Path:      path,
		Offset:    offset,
		Size:      size,
		ChunkSize: chunkSize,
		CreatedAt: now,
	}
	downloadSessionsMu.Unlock()

	return json.Marshal(map[string]any{
		"download_id": id,
		"offset":      offset,
		"size":        size,
		"chunk_size":  chunkSize,
		"chunk_count": uploadChunkCount(size, chunkSize),
		"name":        info.Name(),
		"modified_at": info.ModTime().UTC(),
	})
}

func readFileDownloadChunk(args map[string]interface{}) (json.RawMessage, error) {
	id := strings.TrimSpace(argString(args, "download_id"))
	index := argInt64(args, "chunk_index")
	if id == "" {
		return nil, errors.New("download_id is required")
	}
	if index < 0 {
		return nil, errors.New("download chunk index must not be negative")
	}
	downloadSessionsMu.Lock()
	session, ok := downloadSessions[id]
	downloadSessionsMu.Unlock()
	if !ok {
		return nil, errors.New("unknown download session")
	}
	offset := argInt64(args, "offset")
	length := argInt64(args, "length")
	if length == 0 {
		chunkCount := uploadChunkCount(session.Size, session.ChunkSize)
		if index < 0 || index >= chunkCount {
			return nil, errors.New("invalid download chunk index")
		}
		offset = session.Offset + index*session.ChunkSize
		length = min(session.ChunkSize, session.Size-index*session.ChunkSize)
	}
	if offset < session.Offset || length <= 0 || length > maxTransferChunkSize || offset > session.Offset+session.Size || length > session.Offset+session.Size-offset {
		return nil, errors.New("download range is invalid")
	}
	content, read, err := readFileBytes(session.Path, offset, length)
	if err != nil {
		return nil, err
	}
	if read != length {
		return nil, errors.New("download file changed while reading")
	}
	return json.Marshal(map[string]any{
		"data":        base64.StdEncoding.EncodeToString(content),
		"offset":      offset,
		"read":        read,
		"chunk_index": index,
		"eof":         offset+read >= session.Offset+session.Size,
	})
}

func finishFileDownload(args map[string]interface{}) (json.RawMessage, error) {
	id := strings.TrimSpace(argString(args, "download_id"))
	if id == "" {
		return nil, errors.New("download_id is required")
	}
	downloadSessionsMu.Lock()
	delete(downloadSessions, id)
	downloadSessionsMu.Unlock()
	return json.Marshal(map[string]any{"finished": true})
}

func newFileTransferID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		return hex.EncodeToString(id[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func readFileChunk(args map[string]interface{}) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	offset := argInt64(args, "offset")
	length := argInt64(args, "length")
	if offset < 0 {
		return nil, errors.New("read offset must not be negative")
	}
	if length <= 0 {
		return nil, errors.New("read length must be positive")
	}
	content, read, err := readFileBytes(path, offset, length)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"data":   base64.StdEncoding.EncodeToString(content[:read]),
		"offset": offset,
		"read":   read,
		"eof":    read < length,
	})
}

func readFileBytes(path string, offset, length int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	content := make([]byte, length)
	read, err := io.ReadFull(file, content)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return content[:read], int64(read), nil
	}
	if err != nil {
		return nil, 0, err
	}
	return content, int64(read), nil
}

func writeFileChunk(args map[string]interface{}, encoded string) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	targetPath := path
	offset := argInt64(args, "offset")
	first := argBool(args, "first")
	final := argBool(args, "final")
	uploadID := strings.TrimSpace(argString(args, "upload_id"))
	chunkIndex := argInt64(args, "chunk_index")
	chunkCount := argInt64(args, "chunk_count")
	totalSize := argInt64(args, "total_size")
	chunkSize := argInt64(args, "chunk_size")
	if chunkSize == 0 {
		chunkSize = fileChunkSize
	}
	content, err := decodeData(encoded)
	if err != nil {
		return nil, err
	}
	if uploadID == "" {
		return nil, errors.New("upload_id is required")
	}
	if path == string(filepath.Separator) || path == "." {
		return nil, errors.New("upload path must be a file")
	}
	if offset < 0 {
		return nil, errors.New("upload offset must not be negative")
	}
	if chunkCount <= 0 || totalSize <= 0 {
		return nil, errors.New("chunk_count and total_size are required")
	}
	if chunkSize <= 0 || chunkSize > maxTransferChunkSize {
		return nil, fmt.Errorf("chunk_size must be between 1 and %d bytes", maxTransferChunkSize)
	}
	expectedChunkCount := (totalSize + chunkSize - 1) / chunkSize
	if chunkCount != expectedChunkCount {
		return nil, fmt.Errorf("chunk_count %d does not match total_size %d", chunkCount, totalSize)
	}
	if chunkIndex < 0 || chunkIndex > chunkCount {
		return nil, fmt.Errorf("invalid upload chunk index %d", chunkIndex)
	}
	finalOnly := final && len(content) == 0
	if finalOnly {
		if chunkIndex != chunkCount || offset != totalSize {
			return nil, errors.New("invalid final upload marker")
		}
	} else {
		if chunkIndex >= chunkCount || offset != chunkIndex*chunkSize {
			return nil, fmt.Errorf("chunk %d has an invalid offset", chunkIndex)
		}
		expectedSize := min(chunkSize, totalSize-offset)
		if expectedSize <= 0 || int64(len(content)) != expectedSize {
			return nil, fmt.Errorf("chunk %d has size %d, want %d", chunkIndex, len(content), expectedSize)
		}
		if int64(len(content)) > maxTransferChunkSize {
			return nil, fmt.Errorf("chunk exceeds %d bytes", maxTransferChunkSize)
		}
	}
	uploadChunksMu.Lock()
	defer uploadChunksMu.Unlock()
	state, exists := uploadChunks[uploadID]
	if first {
		if chunkIndex != 0 || offset != 0 || finalOnly {
			return nil, errors.New("invalid first upload chunk")
		}
		if exists {
			if err := removeUploadFileLocked(uploadID); err != nil {
				return nil, err
			}
		}
		state = uploadChunkState{
			ExpectedSize: totalSize,
			ChunkSize:    chunkSize,
			TargetPath:   targetPath,
			PartCount:    chunkCount,
			Parts:        make(map[int64]struct{}),
		}
		uploadChunks[uploadID] = state
		exists = true
	} else if !exists {
		return nil, errors.New("unknown upload session")
	}
	if state.TargetPath != "" && filepath.Clean(state.TargetPath) != filepath.Clean(targetPath) {
		return nil, errors.New("upload target path does not match session")
	}
	if state.ExpectedSize != 0 && state.ExpectedSize != totalSize {
		return nil, errors.New("upload total size does not match session")
	}
	if state.ChunkSize != 0 && state.ChunkSize != chunkSize {
		return nil, errors.New("upload chunk size does not match session")
	}
	if state.PartCount != 0 && state.PartCount != chunkCount {
		return nil, errors.New("upload chunk count does not match session")
	}
	state.ExpectedSize = totalSize
	state.ChunkSize = chunkSize
	state.PartCount = chunkCount
	if state.Parts == nil {
		state.Parts = make(map[int64]struct{})
	}
	if !finalOnly {
		if err := writeUploadChunkLocked(uploadID, targetPath, offset, content, first); err != nil {
			return nil, err
		}
		state = uploadChunks[uploadID]
	}

	if final {
		if !exists {
			return nil, errors.New("unknown upload session")
		}
		if len(state.Parts) != int(state.PartCount) {
			return nil, fmt.Errorf("upload incomplete: received %d of %d chunks", len(state.Parts), state.PartCount)
		}
		if state.Size != state.ExpectedSize {
			return nil, fmt.Errorf("upload size is %d, want %d", state.Size, state.ExpectedSize)
		}
		agentPartPath := state.TempPath
		if agentPartPath == "" {
			return nil, errors.New("upload part file is missing")
		}
		partPath := agentPartPath
		targetDir := filepath.Dir(path)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(partPath, path); err != nil {
			return nil, err
		}
		if err := syncUploadDirectory(targetDir); err != nil {
			return nil, err
		}
		delete(uploadChunks, uploadID)
	}
	return json.Marshal(map[string]any{
		"received": len(content),
		"final":    final,
		"offset":   offset + int64(len(content)),
	})
}

func describeFile(path string, symlink bool) (fileInfo, error) {
	var info os.FileInfo
	var err error
	if symlink {
		info, err = os.Lstat(path)
	} else {
		info, err = os.Stat(path)
	}
	if err != nil {
		return fileInfo{}, err
	}
	uid, gid, owner, group := fileOwnership(info)
	item := fileInfo{
		Name:       filepath.Base(path),
		Path:       virtualizeFilePath(path),
		IsDir:      info.IsDir(),
		IsSymlink:  symlink,
		Size:       info.Size(),
		Mode:       info.Mode().String(),
		ModeOctal:  fmt.Sprintf("%04o", info.Mode().Perm()),
		UID:        uid,
		GID:        gid,
		Owner:      owner,
		Group:      group,
		ModifiedAt: info.ModTime().UTC(),
	}
	if symlink {
		if target, linkErr := os.Readlink(path); linkErr == nil {
			item.Target = target
			if targetInfo, statErr := os.Stat(path); statErr == nil {
				item.IsDir = targetInfo.IsDir()
				item.Size = targetInfo.Size()
				item.Mode = targetInfo.Mode().String()
				item.ModeOctal = fmt.Sprintf("%04o", targetInfo.Mode().Perm())
				uid, gid, owner, group := fileOwnership(targetInfo)
				item.UID = uid
				item.GID = gid
				item.Owner = owner
				item.Group = group
				item.ModifiedAt = targetInfo.ModTime().UTC()
			}
		}
	}
	return item, nil
}

func matchFileContent(path, query, lowerQuery string) (searchMatch, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 10*1024*1024 {
		return searchMatch{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return searchMatch{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), lowerQuery) {
			text := line
			if len(text) > 300 {
				text = text[:300]
			}
			return searchMatch{
				Path: virtualizeFilePath(path),
				Line: lineNumber,
				Text: text,
			}, true
		}
	}
	return searchMatch{}, false
}

func atomicWrite(path string, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".komari-edit-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err = temporary.Write(content); err != nil {
		return err
	}
	if err = temporary.Chmod(0o644); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		if err = os.Chmod(temporaryName, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if err = replaceFile(temporaryName, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func postFileResult(result v2.FileResult) {
	endpoint := strings.TrimSuffix(flags.Endpoint, "/") +
		"/api/clients/v2/rpc?token=" + flags.Token
	body := v2.NewRequest(nil, v2.MethodAgentFileResult, result)
	client := dnsresolver.GetHTTPClientWithPreference(60*time.Second, flags.PreferIPVersion)
	const maxAttempts = 4
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytesReader(body))
		if err != nil {
			log.Printf("failed to create file result request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if err == nil && response != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			return
		}
		// A 4xx response is a definitive protocol/application rejection; retrying
		// it only delays the caller and cannot restore the pending request.
		if response != nil && response.StatusCode >= 400 && response.StatusCode < 500 {
			log.Printf("file result endpoint returned %s", response.Status)
			return
		}
		if attempt < maxAttempts {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}
		if err != nil {
			log.Printf("failed to return file result: %v", err)
		} else if response != nil {
			log.Printf("file result endpoint returned %s", response.Status)
		}
	}
}

func resolveFilePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = "/"
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if trimmed == "~" {
				return resolveOSPath(home)
			}
			return resolveOSPath(filepath.Join(home, trimmed[2:]))
		}
	}
	resolved := filepath.Clean(trimmed)
	return resolveOSPath(resolved)
}

func virtualizeFilePath(path string) string {
	native := filepath.ToSlash(path)
	if runtime.GOOS != "windows" {
		return native
	}

	volume := filepath.VolumeName(native)
	if volume == "" {
		return native
	}
	drive := strings.TrimSuffix(volume, ":")
	rest := strings.TrimPrefix(native[len(volume):], "/")
	if rest == "" {
		return "/" + strings.ToUpper(drive)
	}
	return "/" + strings.ToUpper(drive) + "/" + rest
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func argString(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

func argBool(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func argInt64(args map[string]interface{}, key string) int64 {
	if args == nil {
		return 0
	}
	switch number := args[key].(type) {
	case float64:
		return int64(number)
	case float32:
		return int64(number)
	case int:
		return int64(number)
	case int8:
		return int64(number)
	case int16:
		return int64(number)
	case int32:
		return int64(number)
	case int64:
		return number
	case uint:
		return int64(number)
	case uint8:
		return int64(number)
	case uint16:
		return int64(number)
	case uint32:
		return int64(number)
	case uint64:
		return int64(number)
	case json.Number:
		parsed, _ := number.Int64()
		return parsed
	default:
		return 0
	}
}

func parseMode(value string) (uint32, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("mode is required")
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, "0o"), 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid octal mode: %s", value)
	}
	return uint32(parsed), nil
}

func decodeData(encoded string) ([]byte, error) {
	if encoded == "" {
		return []byte{}, nil
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func bytesReader(value []byte) io.Reader {
	return bytes.NewReader(value)
}
