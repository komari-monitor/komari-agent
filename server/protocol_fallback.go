package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

type v2ProtocolError struct {
	Err error
}

const v2ProtocolFallbackThreshold = 3

func (e *v2ProtocolError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *v2ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
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

func newV2ProtocolError(err error) error {
	if err == nil {
		return nil
	}
	return &v2ProtocolError{Err: err}
}

func isV2ProtocolFailure(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return true
	}
	var protocolErr *v2ProtocolError
	return errors.As(err, &protocolErr)
}

func noteV2AttemptResult(protocolVersion int, err error) (int, bool) {
	if protocolVersion < 2 || requestedProtocolVersion() < 2 {
		return 0, false
	}
	runtimeProtocolState.Lock()
	defer runtimeProtocolState.Unlock()
	if err == nil {
		runtimeProtocolState.v2ProtocolFailures = 0
		return 0, false
	}
	if !isV2ProtocolFailure(err) {
		return runtimeProtocolState.v2ProtocolFailures, false
	}
	runtimeProtocolState.v2ProtocolFailures++
	return runtimeProtocolState.v2ProtocolFailures, runtimeProtocolState.v2ProtocolFailures >= v2ProtocolFallbackThreshold
}

func resetV2ProtocolFailures(protocolVersion int) {
	_, _ = noteV2AttemptResult(protocolVersion, nil)
}

func shouldFallbackToV1(protocolVersion int, err error) bool {
	failures, fallback := noteV2AttemptResult(protocolVersion, err)
	if !fallback {
		return false
	}
	return failures >= v2ProtocolFallbackThreshold
}

func parseV2Response(body []byte) (*v2.Response, error) {
	var rpcResp v2.Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, newV2ProtocolError(fmt.Errorf("invalid v2 JSON-RPC response: %w, body: %s", err, bodySnippet(body)))
	}
	if rpcResp.JSONRPC != v2.Version {
		return nil, newV2ProtocolError(fmt.Errorf("invalid v2 JSON-RPC version %q, body: %s", rpcResp.JSONRPC, bodySnippet(body)))
	}
	if rpcResp.Error != nil {
		return &rpcResp, newV2ProtocolError(fmt.Errorf("v2 rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message))
	}
	return &rpcResp, nil
}

func bodySnippet(body []byte) string {
	const max = 120
	if len(body) > max {
		body = body[:max]
	}
	return fmt.Sprintf("%q", string(body))
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
	v2ProtocolFailures int
}

func setConnectionProtocolVersion(version int) {
	runtimeProtocolState.Lock()
	defer runtimeProtocolState.Unlock()
	runtimeProtocolState.connectionProtocol = version
	if version >= 2 {
		runtimeProtocolState.v2ProtocolFailures = 0
	}
}

func resetConnectionProtocolVersion() {
	runtimeProtocolState.Lock()
	defer runtimeProtocolState.Unlock()
	runtimeProtocolState.connectionProtocol = 0
	runtimeProtocolState.v2ProtocolFailures = 0
}

func uploadProtocolVersion() int {
	runtimeProtocolState.RLock()
	defer runtimeProtocolState.RUnlock()
	if runtimeProtocolState.connectionProtocol > 0 {
		return runtimeProtocolState.connectionProtocol
	}
	return requestedProtocolVersion()
}
