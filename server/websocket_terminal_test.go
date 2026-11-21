package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
