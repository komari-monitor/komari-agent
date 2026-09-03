package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/komari-monitor/komari-agent/dnsresolver"
)

const (
	fileStreamHTTPTimeout    = 30 * time.Minute
	fileStreamBufferSize     = 512 * 1024
	maxFileStreamOperations  = 8
	uploadCancelTombstoneTTL = 15 * time.Minute
)

var (
	fileStreamBufferPool = sync.Pool{New: func() any { return make([]byte, fileStreamBufferSize) }}
	uploadWriteLocksMu   sync.Mutex
	uploadWriteLocks     = make(map[string]*sync.Mutex)
	uploadLifecycleMu    sync.Mutex
	uploadLifecycles     = make(map[string]*uploadStreamLifecycle)
	fileStreamSlots      = make(chan struct{}, maxFileStreamOperations)
)

type uploadStreamLifecycle struct {
	active     int
	idle       chan struct{}
	canceled   bool
	canceledAt time.Time
	streams    map[string]context.CancelFunc
}

type uploadStreamSpec struct {
	Path       string
	UploadID   string
	Offset     int64
	ChunkIndex int64
	ChunkCount int64
	TotalSize  int64
	ChunkSize  int64
	Expected   int64
	First      bool
}

func buildFileTransferURL(args map[string]interface{}) (string, error) {
	id := strings.TrimSpace(argString(args, "transfer_id"))
	token := strings.TrimSpace(argString(args, "transfer_token"))
	if id == "" || token == "" {
		return "", errors.New("transfer_id and transfer_token are required")
	}
	base, err := url.Parse(strings.TrimSpace(pkg_flags.GlobalConfig.Endpoint))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid agent endpoint")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/clients/transfer/" + url.PathEscape(id)
	base.RawPath = ""
	query := base.Query()
	query.Set("token", pkg_flags.GlobalConfig.Token)
	query.Set("transfer_token", token)
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func fileStreamHTTPClient() *http.Client {
	return dnsresolver.GetHTTPClientWithoutHTTP2(fileStreamHTTPTimeout, pkg_flags.GlobalConfig.PreferIPVersion)
}

func sendDownloadStream(args map[string]interface{}) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	offset := argInt64(args, "offset")
	length := argInt64(args, "length")
	if offset < 0 || length <= 0 || length > maxTransferChunkSize {
		return nil, fmt.Errorf("download_stream: invalid range offset=%d length=%d", offset, length)
	}
	transferURL, err := buildFileTransferURL(args)
	if err != nil {
		return nil, fmt.Errorf("download_stream: build transfer URL: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("download_stream: stat %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, errors.New("download_stream: cannot download a directory")
	}
	if offset > info.Size() || length > info.Size()-offset {
		return nil, fmt.Errorf("download_stream: range offset=%d length=%d exceeds file size=%d", offset, length, info.Size())
	}
	if expectedSize := argInt64(args, "file_size"); expectedSize > 0 && info.Size() != expectedSize {
		return nil, errors.New("download_stream: file changed while opening stream")
	}
	if modified := strings.TrimSpace(argString(args, "modified_at")); modified != "" {
		if expectedTime, parseErr := time.Parse(time.RFC3339Nano, modified); parseErr == nil && !info.ModTime().UTC().Equal(expectedTime.UTC()) {
			return nil, errors.New("download_stream: file changed while opening stream")
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("download_stream: open %q: %w", path, err)
	}
	defer file.Close()
	streamContext, cancel := context.WithTimeout(context.Background(), fileStreamHTTPTimeout)
	defer cancel()
	if err := acquireFileStreamSlot(streamContext); err != nil {
		return nil, fmt.Errorf("download_stream: acquire stream slot: %w", err)
	}
	defer releaseFileStreamSlot()
	request, err := http.NewRequestWithContext(streamContext, http.MethodPost, transferURL, io.NewSectionReader(file, offset, length))
	if err != nil {
		return nil, fmt.Errorf("download_stream: create HTTP request: %w", err)
	}
	request.ContentLength = length
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("X-Komari-Transfer-ID", strings.TrimSpace(argString(args, "transfer_id")))
	request.Header.Set("X-Komari-Transfer-Token", strings.TrimSpace(argString(args, "transfer_token")))
	request.Header.Set("X-Komari-Transfer-Offset", fmt.Sprintf("%d", offset))
	request.Header.Set("X-Komari-Transfer-Length", fmt.Sprintf("%d", length))
	if fileSize := argInt64(args, "file_size"); fileSize > 0 {
		request.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, fileSize))
	}

	response, err := fileStreamHTTPClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("download_stream: HTTP request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if len(message) == 0 {
			return nil, fmt.Errorf("download_stream: file transfer endpoint returned %s", response.Status)
		}
		return nil, fmt.Errorf("download_stream: file transfer endpoint returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return json.Marshal(map[string]any{"sent": length})
}

func receiveUploadStream(args map[string]interface{}) (json.RawMessage, error) {
	spec, err := parseUploadStreamSpec(args)
	if err != nil {
		return nil, fmt.Errorf("upload_stream: invalid metadata: %w", err)
	}
	transferURL, err := buildFileTransferURL(args)
	if err != nil {
		return nil, fmt.Errorf("upload_stream: build transfer URL: %w", err)
	}
	streamContext, cancel := context.WithCancel(context.Background())
	streamKey := strings.TrimSpace(argString(args, "transfer_id"))
	endActive, lifecycleErr := beginUploadStream(spec.UploadID, streamKey, cancel)
	if lifecycleErr != nil {
		cancel()
		return nil, lifecycleErr
	}
	defer endActive()
	defer cancel()
	if err := acquireFileStreamSlot(streamContext); err != nil {
		return nil, fmt.Errorf("upload_stream: acquire stream slot: %w", err)
	}
	defer releaseFileStreamSlot()
	request, err := http.NewRequestWithContext(streamContext, http.MethodPost, transferURL, nil)
	if err != nil {
		return nil, fmt.Errorf("upload_stream: HTTP request: %w", err)
	}
	request.ContentLength = 0
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("X-Komari-Transfer-ID", strings.TrimSpace(argString(args, "transfer_id")))
	request.Header.Set("X-Komari-Transfer-Token", strings.TrimSpace(argString(args, "transfer_token")))
	request.Header.Set("X-Komari-Transfer-Offset", fmt.Sprintf("%d", spec.Offset))
	request.Header.Set("X-Komari-Transfer-Length", fmt.Sprintf("%d", spec.Expected))
	response, err := fileStreamHTTPClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("upload_stream: HTTP request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if len(message) == 0 {
			return nil, fmt.Errorf("upload_stream: file transfer endpoint returned %s", response.Status)
		}
		return nil, fmt.Errorf("upload_stream: file transfer endpoint returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if response.ContentLength >= 0 && response.ContentLength != spec.Expected {
		return nil, fmt.Errorf("upload_stream: response content length %d, want %d", response.ContentLength, spec.Expected)
	}
	result, err := writeUploadStreamChunk(spec, response.Body)
	if err != nil {
		return nil, fmt.Errorf("upload_stream: write chunk offset=%d length=%d: %w", spec.Offset, spec.Expected, err)
	}
	return result, nil
}

func acquireFileStreamSlot(ctx context.Context) error {
	select {
	case fileStreamSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseFileStreamSlot() {
	<-fileStreamSlots
}

// beginUploadStream registers the active request and its cancellation function
// under one lock. This closes the race where upload_cancel arrives between
// starting an operation and registering its HTTP request.
func beginUploadStream(uploadID, streamID string, cancel context.CancelFunc) (func(), error) {
	if uploadID == "" {
		return func() {}, nil
	}
	uploadLifecycleMu.Lock()
	pruneUploadLifecyclesLocked(time.Now())
	state := uploadLifecycles[uploadID]
	if state == nil {
		state = &uploadStreamLifecycle{
			idle:    make(chan struct{}),
			streams: make(map[string]context.CancelFunc),
		}
		close(state.idle)
		uploadLifecycles[uploadID] = state
	}
	if state.canceled {
		uploadLifecycleMu.Unlock()
		return nil, errors.New("upload session was cancelled")
	}
	state.active++
	if state.active == 1 {
		state.idle = make(chan struct{})
	}
	if streamID != "" && cancel != nil {
		state.streams[streamID] = cancel
	}
	uploadLifecycleMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			uploadLifecycleMu.Lock()
			state := uploadLifecycles[uploadID]
			if state != nil {
				if streamID != "" {
					delete(state.streams, streamID)
				}
				if state.active > 0 {
					state.active--
					if state.active == 0 {
						close(state.idle)
						if !state.canceled {
							delete(uploadLifecycles, uploadID)
						}
					}
				}
			}
			uploadLifecycleMu.Unlock()
		})
	}, nil
}

func waitUploadStreams(uploadID string) {
	if uploadID == "" {
		return
	}
	for {
		uploadLifecycleMu.Lock()
		state := uploadLifecycles[uploadID]
		if state == nil || state.active == 0 {
			if state != nil && !state.canceled {
				delete(uploadLifecycles, uploadID)
			}
			uploadLifecycleMu.Unlock()
			return
		}
		idle := state.idle
		uploadLifecycleMu.Unlock()
		<-idle
	}
}

func cancelUploadStreams(uploadID string) {
	if uploadID == "" {
		return
	}
	uploadLifecycleMu.Lock()
	pruneUploadLifecyclesLocked(time.Now())
	state := uploadLifecycles[uploadID]
	if state == nil {
		state = &uploadStreamLifecycle{
			idle:    make(chan struct{}),
			streams: make(map[string]context.CancelFunc),
		}
		close(state.idle)
		uploadLifecycles[uploadID] = state
	}
	state.canceled = true
	state.canceledAt = time.Now()
	cancellations := make([]context.CancelFunc, 0, len(state.streams))
	for _, cancel := range state.streams {
		cancellations = append(cancellations, cancel)
	}
	uploadLifecycleMu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
}

func pruneUploadLifecyclesLocked(now time.Time) {
	for uploadID, state := range uploadLifecycles {
		if state == nil || !state.canceled || state.active != 0 {
			continue
		}
		if state.canceledAt.IsZero() || now.Sub(state.canceledAt) >= uploadCancelTombstoneTTL {
			delete(uploadLifecycles, uploadID)
		}
	}
}

func parseUploadStreamSpec(args map[string]interface{}) (uploadStreamSpec, error) {
	spec := uploadStreamSpec{
		Path:       resolveFilePath(argString(args, "path")),
		UploadID:   strings.TrimSpace(argString(args, "upload_id")),
		Offset:     argInt64(args, "offset"),
		ChunkIndex: argInt64(args, "chunk_index"),
		ChunkCount: argInt64(args, "chunk_count"),
		TotalSize:  argInt64(args, "total_size"),
		ChunkSize:  argInt64(args, "chunk_size"),
		First:      argBool(args, "first"),
	}
	if spec.ChunkSize == 0 {
		spec.ChunkSize = defaultTransferChunkSize
	}
	if spec.UploadID == "" {
		return uploadStreamSpec{}, errors.New("upload_id is required")
	}
	if spec.Path == string(filepath.Separator) || spec.Path == "." {
		return uploadStreamSpec{}, errors.New("upload path must be a file")
	}
	if spec.Offset < 0 || spec.ChunkCount <= 0 || spec.TotalSize <= 0 {
		return uploadStreamSpec{}, errors.New("invalid upload stream metadata")
	}
	if spec.ChunkSize <= 0 || spec.ChunkSize > maxTransferChunkSize {
		return uploadStreamSpec{}, fmt.Errorf("chunk_size must be between 1 and %d bytes", maxTransferChunkSize)
	}
	if spec.ChunkCount != (spec.TotalSize+spec.ChunkSize-1)/spec.ChunkSize {
		return uploadStreamSpec{}, errors.New("chunk_count does not match total_size")
	}
	if spec.ChunkIndex < 0 || spec.ChunkIndex >= spec.ChunkCount || spec.Offset != spec.ChunkIndex*spec.ChunkSize {
		return uploadStreamSpec{}, errors.New("invalid upload chunk offset")
	}
	spec.Expected = min(spec.ChunkSize, spec.TotalSize-spec.Offset)
	if spec.Expected <= 0 {
		return uploadStreamSpec{}, errors.New("invalid upload chunk size")
	}
	return spec, nil
}

func uploadWriteLock(uploadID string) *sync.Mutex {
	uploadWriteLocksMu.Lock()
	defer uploadWriteLocksMu.Unlock()
	lock := uploadWriteLocks[uploadID]
	if lock == nil {
		lock = &sync.Mutex{}
		uploadWriteLocks[uploadID] = lock
	}
	return lock
}

func forgetUploadWriteLock(uploadID string) {
	if uploadID == "" {
		return
	}
	uploadWriteLocksMu.Lock()
	delete(uploadWriteLocks, uploadID)
	uploadWriteLocksMu.Unlock()
}

func writeUploadStreamChunk(spec uploadStreamSpec, source io.Reader) (json.RawMessage, error) {
	uploadChunksMu.Lock()
	state, exists := uploadChunks[spec.UploadID]
	if spec.First {
		if !exists {
			state = uploadChunkState{
				ExpectedSize: spec.TotalSize,
				ChunkSize:    spec.ChunkSize,
				TargetPath:   spec.Path,
				PartCount:    spec.ChunkCount,
				Parts:        make(map[int64]struct{}),
			}
			partPath := uploadPartPathFor(spec.Path, spec.UploadID)
			file, openErr := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if openErr != nil {
				uploadChunksMu.Unlock()
				return nil, openErr
			}
			if closeErr := file.Close(); closeErr != nil {
				uploadChunksMu.Unlock()
				return nil, closeErr
			}
			state.TempPath = partPath
			uploadChunks[spec.UploadID] = state
			exists = true
		}
	} else if !exists {
		uploadChunksMu.Unlock()
		return nil, errors.New("unknown upload session")
	}
	if state.TargetPath != "" && filepath.Clean(state.TargetPath) != filepath.Clean(spec.Path) {
		uploadChunksMu.Unlock()
		return nil, errors.New("upload target path does not match session")
	}
	if state.ExpectedSize != 0 && state.ExpectedSize != spec.TotalSize {
		uploadChunksMu.Unlock()
		return nil, errors.New("upload total size does not match session")
	}
	if state.ChunkSize != 0 && state.ChunkSize != spec.ChunkSize {
		uploadChunksMu.Unlock()
		return nil, errors.New("upload chunk size does not match session")
	}
	if state.PartCount != 0 && state.PartCount != spec.ChunkCount {
		uploadChunksMu.Unlock()
		return nil, errors.New("upload chunk count does not match session")
	}
	partPath := state.TempPath
	if partPath == "" {
		partPath = uploadPartPathFor(spec.Path, spec.UploadID)
	}
	uploadChunksMu.Unlock()

	file, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	buffer := fileStreamBufferPool.Get().([]byte)
	writer := &offsetFileWriter{file: file, offset: spec.Offset}
	written, copyErr := io.CopyBuffer(writer, io.LimitReader(source, spec.Expected), buffer)
	fileStreamBufferPool.Put(buffer)
	if copyErr != nil {
		_ = file.Close()
		return nil, copyErr
	}
	if written != spec.Expected {
		_ = file.Close()
		return nil, fmt.Errorf("upload stream ended after %d of %d bytes", written, spec.Expected)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}

	uploadChunksMu.Lock()
	state, exists = uploadChunks[spec.UploadID]
	if !exists {
		uploadChunksMu.Unlock()
		return nil, errors.New("upload session was cancelled while receiving the stream")
	}
	state.ExpectedSize = spec.TotalSize
	state.ChunkSize = spec.ChunkSize
	state.PartCount = spec.ChunkCount
	state.Size = max(state.Size, spec.Offset+written)
	if state.TargetPath == "" {
		state.TargetPath = spec.Path
	}
	if state.Parts == nil {
		state.Parts = make(map[int64]struct{})
	}
	state.Parts[spec.ChunkIndex] = struct{}{}
	state.TempPath = partPath
	state.CreatedAt = time.Now()
	uploadChunks[spec.UploadID] = state
	uploadChunksMu.Unlock()

	return json.Marshal(map[string]any{
		"received": written,
		"offset":   spec.Offset + written,
	})
}

type offsetFileWriter struct {
	file   *os.File
	offset int64
}

func (writer *offsetFileWriter) Write(data []byte) (int, error) {
	written, err := writer.file.WriteAt(data, writer.offset)
	writer.offset += int64(written)
	return written, err
}
