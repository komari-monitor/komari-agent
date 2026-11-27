package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

// TestEstablishTerminalConnectionStopsImmediatelyOn404 ensures the agent does
// not keep retrying every 15s for 5 minutes after the server rejects a session
// with HTTP 404 (Session not found).
func TestEstablishTerminalConnectionStopsImmediatelyOn404(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "Session not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		establishTerminalConnection("test-token", "expired-session-404", srv.URL)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("establishTerminalConnection kept retrying after HTTP 404")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1", got)
	}
}

// TestProcessV2EventRejectsEmptyTerminalRequestID ensures blank request_id
// values are rejected before reaching the dialer: they would bypass the
// terminalDials dedup map and allow unbounded dial goroutines.
func TestProcessV2EventRejectsEmptyTerminalRequestID(t *testing.T) {
	// 去重 map 是包级全局且跨 count=N 重复执行持续存在：事件 ID 唯一化，
	// 保证每次重复运行都真正走到终端分支而不是被 dedup 短路。
	for i, requestID := range []string{"", "   ", "\t\n"} {
		params := map[string]interface{}{"request_id": requestID}
		eventID := fmt.Sprintf("evt-blank-%d-%d", i, time.Now().UnixNano())
		if processV2Event(nil, v2.MethodAgentTerminal, params, eventID, nil) {
			t.Fatalf("empty/blank request_id %q was accepted", requestID)
		}
	}
}

// TestEstablishTerminalConnectionRejectsEmptyID ensures the dial loop never
// starts for an empty request_id even if called directly.
func TestEstablishTerminalConnectionRejectsEmptyID(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		establishTerminalConnection("test-token", "", srv.URL)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("establishTerminalConnection with empty id did not return immediately")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("server hits = %d, want 0 for empty request_id", got)
	}
}

// TestEstablishTerminalConnectionDisableWebSshNoRetryStorm ensures that with
// remote control disabled the dialer performs at most one attempt and never
// enters the 5-minute 15s retry loop even on transient (non-404) failures.
func TestEstablishTerminalConnectionDisableWebSshNoRetryStorm(t *testing.T) {
	flags.DisableWebSsh = true
	t.Cleanup(func() { flags.DisableWebSsh = false })

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 503 is transient: the non-disable path would retry every 15s.
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		establishTerminalConnection("test-token", "disable-no-retry", srv.URL)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("establishTerminalConnection entered retry loop with DisableWebSsh=true")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want exactly 1 (no retry storm)", got)
	}
}
