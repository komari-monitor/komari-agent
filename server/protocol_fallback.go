package server

import (
	"errors"
	"fmt"
	"sync"
)

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *httpStatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.Body != "" {
		return fmt.Sprintf("status code: %d,%s", e.StatusCode, e.Body)
	}
	if e.Status != "" {
		return e.Status
	}
	return fmt.Sprintf("status code: %d", e.StatusCode)
}

func isHTTPStatus(err error, statusCode int) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == statusCode
}

func requestedProtocolVersion() int {
	if flags.ProtocolVersion >= 2 {
		return 2
	}
	return 1
}

var runtimeProtocolState struct {
	sync.RWMutex
	connectionProtocol int
}

func setConnectionProtocolVersion(version int) {
	runtimeProtocolState.Lock()
	defer runtimeProtocolState.Unlock()
	runtimeProtocolState.connectionProtocol = version
}

func resetConnectionProtocolVersion() {
	setConnectionProtocolVersion(0)
}

func uploadProtocolVersion() int {
	runtimeProtocolState.RLock()
	defer runtimeProtocolState.RUnlock()
	if runtimeProtocolState.connectionProtocol > 0 {
		return runtimeProtocolState.connectionProtocol
	}
	return requestedProtocolVersion()
}
