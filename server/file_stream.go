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
	fileStreamHTTPTimeout   = 30 * time.Minute
	fileStreamBufferSize    = 512 * 1024
	maxFileStreamOperations = 8
)

var (
	fileStreamBufferPool = sync.Pool{New: func() any { return make([]byte, fileStreamBufferSize) }}
	uploadWriteLocksMu   sync.Mutex
	uploadWriteLocks     = make(map[string]*sync.Mutex)
	uploadStreamsMu      sync.Mutex
	uploadStreams        = make(map[string]map[string]context.CancelFunc)
	uploadActiveMu       sync.Mutex
	uploadActive         = make(map[string]*activeUploadStreams)
	fileStreamSlots      = make(chan struct{}, maxFileStreamOperations)
)

type activeUploadStreams struct {
	count int
	idle  chan struct{}
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
	return dnsresolver.GetHTTPClientWithPreference(fileStreamHTTPTimeout, pkg_flags.GlobalConfig.PreferIPVersion)
}

func sendDownloadStream(args map[string]interface{}) (json.RawMessage, error) {
	path := resolveFilePath(argString(args, "path"))
	offset := argInt64(args, "offset")
	length := argInt64(args, "length")
	if offset < 0 || length <= 0 || length > maxTransferChunkSize {
		return nil, errors.New("invalid download stream range")
	}
	transferURL, err := buildFileTransferURL(args)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("cannot download a directory")
	}
	if offset > info.Size() || length > info.Size()-offset {
		return nil, errors.New("download range exceeds file size")
	}
	if expectedSize := argInt64(args, "file_size"); expectedSize > 0 && info.Size() != expectedSize {
		return nil, errors.New("download file changed while opening stream")
	}
	if modified := strings.TrimSpace(argString(args, "modified_at")); modified != "" {
		if expectedTime, parseErr := time.Parse(time.RFC3339Nano, modified); parseErr == nil && !info.ModTime().UTC().Equal(expectedTime.UTC()) {
			return nil, errors.New("download file changed while opening stream")
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	streamContext, cancel := context.WithTimeout(context.Background(), fileStreamHTTPTimeout)
	defer cancel()
	if err := acquireFileStreamSlot(streamContext); err != nil {
		return nil, err
	}
	defer releaseFileStreamSlot()
	request, err := http.NewRequestWithContext(streamContext, http.MethodPost, transferURL, io.NewSectionReader(file, offset, length))
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if len(message) == 0 {
			return nil, fmt.Errorf("file transfer endpoint returned %s", response.Status)
		}
		return nil, fmt.Errorf("file transfer endpoint returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return json.Marshal(map[string]any{"sent": length})
}

func receiveUploadStream(args map[string]interface{}) (json.RawMessage, error) {
	spec, err := parseUploadStreamSpec(args)
	if err != nil {
		return nil, err
	}
	transferURL, err := buildFileTransferURL(args)
	if err != nil {
		return nil, err
	}
	streamContext, cancel := context.WithCancel(context.Background())
	streamKey := strings.TrimSpace(argString(args, "transfer_id"))
	endActive := beginUploadStream(spec.UploadID)
	defer endActive()
	registerUploadStream(spec.UploadID, streamKey, cancel)
	defer unregisterUploadStream(spec.UploadID, streamKey)
	defer cancel()
	if err := acquireFileStreamSlot(streamContext); err != nil {
		return nil, err
	}
	defer releaseFileStreamSlot()
	request, err := http.NewRequestWithContext(streamContext, http.MethodPost, transferURL, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if len(message) == 0 {
			return nil, fmt.Errorf("file transfer endpoint returned %s", response.Status)
		}
		return nil, fmt.Errorf("file transfer endpoint returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if response.ContentLength >= 0 && response.ContentLength != spec.Expected {
		return nil, fmt.Errorf("upload stream content length %d, want %d", response.ContentLength, spec.Expected)
	}
	return writeUploadStreamChunk(spec, response.Body)
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

func registerUploadStream(uploadID, streamID string, cancel context.CancelFunc) {
	if uploadID == "" || streamID == "" || cancel == nil {
		return
	}
	uploadStreamsMu.Lock()
	streams := uploadStreams[uploadID]
	if streams == nil {
		streams = make(map[string]context.CancelFunc)
		uploadStreams[uploadID] = streams
	}
	streams[streamID] = cancel
	uploadStreamsMu.Unlock()
}

func unregisterUploadStream(uploadID, streamID string) {
	if uploadID == "" || streamID == "" {
		return
	}
	uploadStreamsMu.Lock()
	if streams := uploadStreams[uploadID]; streams != nil {
		delete(streams, streamID)
		if len(streams) == 0 {
			delete(uploadStreams, uploadID)
		}
	}
	uploadStreamsMu.Unlock()
}

func beginUploadStream(uploadID string) func() {
	if uploadID == "" {
		return func() {}
	}
	uploadActiveMu.Lock()
	state := uploadActive[uploadID]
	if state == nil {
		state = &activeUploadStreams{idle: make(chan struct{})}
		close(state.idle)
		uploadActive[uploadID] = state
	}
	state.count++
	if state.count == 1 {
		state.idle = make(chan struct{})
	}
	uploadActiveMu.Unlock()
	return func() {
		uploadActiveMu.Lock()
		state := uploadActive[uploadID]
		if state != nil && state.count > 0 {
			state.count--
			if state.count == 0 {
				close(state.idle)
			}
		}
		uploadActiveMu.Unlock()
	}
}

func waitUploadStreams(uploadID string) {
	if uploadID == "" {
		return
	}
	for {
		uploadActiveMu.Lock()
		state := uploadActive[uploadID]
		if state == nil || state.count == 0 {
			if state != nil {
				delete(uploadActive, uploadID)
			}
			uploadActiveMu.Unlock()
			return
		}
		idle := state.idle
		uploadActiveMu.Unlock()
		<-idle
	}
}

func cancelUploadStreams(uploadID string) {
	if uploadID == "" {
		return
	}
	uploadStreamsMu.Lock()
	streams := uploadStreams[uploadID]
	cancellations := make([]context.CancelFunc, 0, len(streams))
	for _, cancel := range streams {
		cancellations = append(cancellations, cancel)
	}
	uploadStreamsMu.Unlock()
	for _, cancel := range cancellations {
		cancel()
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
		spec.ChunkSize = fileChunkSize
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
	lock := uploadWriteLock(spec.UploadID)
	lock.Lock()
	uploadChunksMu.Lock()
	state, exists := uploadChunks[spec.UploadID]
	if spec.First {
		if exists {
			if err := removeUploadFileLocked(spec.UploadID); err != nil {
				uploadChunksMu.Unlock()
				lock.Unlock()
				return nil, err
			}
		}
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
			lock.Unlock()
			return nil, openErr
		}
		closeErr := file.Close()
		if closeErr != nil {
			uploadChunksMu.Unlock()
			lock.Unlock()
			return nil, closeErr
		}
		state.TempPath = partPath
		uploadChunks[spec.UploadID] = state
		exists = true
	} else if !exists {
		uploadChunksMu.Unlock()
		lock.Unlock()
		return nil, errors.New("unknown upload session")
	}
	if state.TargetPath != "" && filepath.Clean(state.TargetPath) != filepath.Clean(spec.Path) {
		uploadChunksMu.Unlock()
		lock.Unlock()
		return nil, errors.New("upload target path does not match session")
	}
	if state.ExpectedSize != 0 && state.ExpectedSize != spec.TotalSize {
		uploadChunksMu.Unlock()
		lock.Unlock()
		return nil, errors.New("upload total size does not match session")
	}
	if state.ChunkSize != 0 && state.ChunkSize != spec.ChunkSize {
		uploadChunksMu.Unlock()
		lock.Unlock()
		return nil, errors.New("upload chunk size does not match session")
	}
	partPath := state.TempPath
	if partPath == "" {
		partPath = uploadPartPathFor(spec.Path, spec.UploadID)
	}
	uploadChunksMu.Unlock()
	lock.Unlock()

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
