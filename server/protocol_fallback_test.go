package server

import (
	"errors"
	"testing"
)

func preserveProtocolFallbackState(t *testing.T) {
	t.Helper()

	protocolVersion := flags.ProtocolVersion
	runtimeProtocolState.Lock()
	connectionProtocol := runtimeProtocolState.connectionProtocol
	v2ProtocolFailures := runtimeProtocolState.v2ProtocolFailures
	runtimeProtocolState.Unlock()

	t.Cleanup(func() {
		flags.ProtocolVersion = protocolVersion
		runtimeProtocolState.Lock()
		runtimeProtocolState.connectionProtocol = connectionProtocol
		runtimeProtocolState.v2ProtocolFailures = v2ProtocolFailures
		runtimeProtocolState.Unlock()
	})
	flags.ProtocolVersion = 2
	resetConnectionProtocolVersion()
}

func TestParseV2ResponseTreatsHTMLAsProtocolFailure(t *testing.T) {
	preserveProtocolFallbackState(t)

	_, err := parseV2Response([]byte("<!DOCTYPE html><html></html>"))
	if err == nil {
		t.Fatal("expected invalid v2 response error")
	}
	if !isV2ProtocolFailure(err) {
		t.Fatalf("expected protocol failure, got %T: %v", err, err)
	}
}

func TestV2ProtocolFailureFallsBackAfterThreeAttempts(t *testing.T) {
	preserveProtocolFallbackState(t)

	err := &httpStatusError{StatusCode: 404, Status: "404 Not Found"}
	for attempt := 1; attempt < v2ProtocolFallbackThreshold; attempt++ {
		if shouldFallbackToV1(2, err) {
			t.Fatalf("unexpected fallback on attempt %d", attempt)
		}
	}
	if !shouldFallbackToV1(2, err) {
		t.Fatalf("expected fallback on attempt %d", v2ProtocolFallbackThreshold)
	}
}

func TestNetworkErrorsDoNotCountTowardV2Fallback(t *testing.T) {
	preserveProtocolFallbackState(t)

	err := errors.New("dial tcp: lookup example.com: no such host")
	for attempt := 1; attempt <= v2ProtocolFallbackThreshold+1; attempt++ {
		if shouldFallbackToV1(2, err) {
			t.Fatalf("network error counted toward fallback on attempt %d", attempt)
		}
	}
	runtimeProtocolState.RLock()
	failures := runtimeProtocolState.v2ProtocolFailures
	runtimeProtocolState.RUnlock()
	if failures != 0 {
		t.Fatalf("expected no protocol failures, got %d", failures)
	}
}

func TestV2SuccessResetsProtocolFailureCount(t *testing.T) {
	preserveProtocolFallbackState(t)

	err := &httpStatusError{StatusCode: 404, Status: "404 Not Found"}
	for attempt := 1; attempt < v2ProtocolFallbackThreshold; attempt++ {
		if shouldFallbackToV1(2, err) {
			t.Fatalf("unexpected fallback on attempt %d", attempt)
		}
	}
	resetV2ProtocolFailures(2)
	if shouldFallbackToV1(2, err) {
		t.Fatal("success did not reset v2 protocol failure count")
	}
}
