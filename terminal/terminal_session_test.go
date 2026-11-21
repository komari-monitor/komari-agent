package terminal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakePTY struct {
	mu       sync.Mutex
	closed   bool
	eofCh    chan struct{}
	writes   [][]byte
	resizes  [][2]int
	closeErr error
}

func newFakePTY() *fakePTY {
	return &fakePTY{eofCh: make(chan struct{})}
}

func (f *fakePTY) triggerEOF() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.eofCh:
	default:
		close(f.eofCh)
	}
}

func (f *fakePTY) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakePTY) Close() error {
	f.mu.Lock()
	f.closed = true
	select {
	case <-f.eofCh:
	default:
		close(f.eofCh)
	}
	f.mu.Unlock()
	return f.closeErr
}

func (f *fakePTY) Read(p []byte) (int, error) {
	<-f.eofCh
	return 0, io.EOF
}

func (f *fakePTY) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	cp := append([]byte(nil), p...)
	f.writes = append(f.writes, cp)
	return len(p), nil
}

func (f *fakePTY) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakePTY) Wait() error {
	<-f.eofCh
	return nil
}

func setAgentTestTiming(t *testing.T, retention, pingInterval, readWait, writeWait time.Duration) {
	t.Helper()
	configMu.Lock()
	prevRet, prevPing, prevRead, prevWrite := disconnectedRetention, terminalPingInterval, terminalReadWait, terminalWriteWait
	disconnectedRetention = retention
	terminalPingInterval = pingInterval
	terminalReadWait = readWait
	terminalWriteWait = writeWait
	configMu.Unlock()
	t.Cleanup(func() {
		configMu.Lock()
		disconnectedRetention = prevRet
		terminalPingInterval = prevPing
		terminalReadWait = prevRead
		terminalWriteWait = prevWrite
		configMu.Unlock()
	})
}

func resetAgentSessions(t *testing.T) {
	t.Helper()
	cleanup := func() {
		sessionsMu.Lock()
		list := make([]*terminalSession, 0, len(sessions))
		for id, s := range sessions {
			delete(sessions, id)
			if s != nil {
				list = append(list, s)
			}
		}
		sessionsMu.Unlock()
		for _, s := range list {
			s.mu.Lock()
			s.removed = true
			s.stopTimerLocked()
			conn := s.conn
			s.conn = nil
			term := s.term
			s.mu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
			if term != nil {
				_ = term.Close()
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
}

func newAgentWSPair(t *testing.T) (clientConn *websocket.Conn, serverConn *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		accepted <- c
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-accepted
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func seedFakeSession(t *testing.T, id string) (*terminalSession, *fakePTY) {
	t.Helper()
	pty := newFakePTY()
	session := &terminalSession{id: id, term: pty}
	sessionsMu.Lock()
	sessions[id] = session
	sessionsMu.Unlock()
	go session.readOutput()
	return session, pty
}

func readAgentControl(t *testing.T, serverConn *websocket.Conn, timeout time.Duration) controlMessage {
	t.Helper()
	_ = serverConn.SetReadDeadline(time.Now().Add(timeout))
	msgType, data, err := serverConn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("message type = %d, want text", msgType)
	}
	var msg controlMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal control message (%q): %v", string(data), err)
	}
	return msg
}

// TestAbortKeepsConnectionAndExpiresWithoutResetOnReconnect verifies that:
// 1. Receiving abort keeps the WS open instead of forcing an immediate disconnect/reconnect churn.
// 2. If a proxy drops the WS while browserDetached, reattaching does NOT cancel or extend the retention timer.
// 3. When the original retention window expires, the agent sends terminate and closes the PTY.
func TestAbortKeepsConnectionAndExpiresWithoutResetOnReconnect(t *testing.T) {
	resetAgentSessions(t)
	setAgentTestTiming(t, 150*time.Millisecond, 5*time.Second, 5*time.Second, time.Second)

	const id = "agent-abort-expiry"
	_, pty := seedFakeSession(t, id)

	client1, server1 := newAgentWSPair(t)
	done1 := make(chan bool, 1)
	go func() {
		done1 <- ReattachTerminal(client1, id)
	}()
	time.Sleep(25 * time.Millisecond)

	// Server notifies browser dropped -> agent must keep WS open and start fixed timer.
	if err := server1.WriteJSON(controlMessage{Type: controlTypeAbort}); err != nil {
		t.Fatalf("write abort failed: %v", err)
	}
	select {
	case retry := <-done1:
		t.Fatalf("agent disconnected immediately on abort (retry=%v); expected connection to stay open", retry)
	case <-time.After(40 * time.Millisecond):
	}

	// Simulate reverse proxy dropping the idle WS at t=40ms.
	_ = server1.Close()
	select {
	case retry := <-done1:
		if !retry {
			t.Fatal("expected retry=true after transient proxy drop before expiry")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ReattachTerminal did not return after proxy closed WS")
	}

	// Reconnect on a second WS — must NOT cancel or extend the timer while browserDetached.
	client2, server2 := newAgentWSPair(t)
	done2 := make(chan bool, 1)
	go func() {
		done2 <- ReattachTerminal(client2, id)
	}()

	// Original 150ms retention expires (~110ms left): agent must send terminate and exit with retry=false.
	msg := readAgentControl(t, server2, 600*time.Millisecond)
	if msg.Type != controlTypeTerminate {
		t.Fatalf("got control type %q on expiry, want %q", msg.Type, controlTypeTerminate)
	}
	select {
	case retry := <-done2:
		if retry {
			t.Fatal("expected retry=false after session expired")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ReattachTerminal did not exit after expiry")
	}
	if HasSession(id) {
		t.Fatal("session still present after expiry")
	}
	closeDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(closeDeadline) && !pty.isClosed() {
		time.Sleep(15 * time.Millisecond)
	}
	if !pty.isClosed() {
		t.Fatal("PTY was not closed on expiry")
	}
}

// TestAbortTimerCancelledOnReady verifies that receiving ready after abort
// cancels the retention timer so the resumed session stays alive.
func TestAbortTimerCancelledOnReady(t *testing.T) {
	resetAgentSessions(t)
	setAgentTestTiming(t, 100*time.Millisecond, 5*time.Second, 5*time.Second, time.Second)

	const id = "agent-abort-resume"
	_, pty := seedFakeSession(t, id)

	client, server := newAgentWSPair(t)
	done := make(chan bool, 1)
	go func() {
		done <- ReattachTerminal(client, id)
	}()
	time.Sleep(20 * time.Millisecond)

	if err := server.WriteJSON(controlMessage{Type: controlTypeAbort}); err != nil {
		t.Fatalf("write abort: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := server.WriteJSON(controlMessage{Type: controlTypeReady}); err != nil {
		t.Fatalf("write ready: %v", err)
	}

	// Wait beyond the 100ms retention window: session must remain alive.
	time.Sleep(140 * time.Millisecond)
	if !HasSession(id) || pty.isClosed() {
		t.Fatal("resumed session was expired despite receiving ready")
	}

	// Cleanly terminate to end the goroutine.
	if err := server.WriteJSON(controlMessage{Type: controlTypeTerminate}); err != nil {
		t.Fatalf("write terminate: %v", err)
	}
	select {
	case retry := <-done:
		if retry {
			t.Fatal("expected retry=false after explicit terminate")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReattachTerminal did not exit after terminate")
	}
}

// TestKeepaliveDetachesSilentConnection verifies that a silently dead server
// connection hits the read deadline and detaches cleanly.
func TestKeepaliveDetachesSilentConnection(t *testing.T) {
	resetAgentSessions(t)
	setAgentTestTiming(t, 5*time.Second, 25*time.Millisecond, 80*time.Millisecond, 50*time.Millisecond)

	const id = "agent-silent-server"
	_, _ = seedFakeSession(t, id)

	client, server := newAgentWSPair(t)
	_ = server // intentionally never reads, so no Pong is sent back

	done := make(chan bool, 1)
	go func() {
		done <- ReattachTerminal(client, id)
	}()

	select {
	case retry := <-done:
		if !retry {
			t.Fatal("expected retry=true after read deadline detach")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("ReattachTerminal did not detach after read deadline")
	}
	if _, ok := SessionDeadline(id); !ok {
		t.Fatal("expected retention deadline to be set after detach")
	}
}

// TestReattachDoesNotRespawnExitedSession ensures that if a shell exited while
// detached, ReattachTerminal sends terminate and returns false instead of
// spawning a fresh zombie PTY.
func TestReattachDoesNotRespawnExitedSession(t *testing.T) {
	resetAgentSessions(t)
	setAgentTestTiming(t, 5*time.Second, 5*time.Second, 5*time.Second, time.Second)

	const id = "agent-exited-while-detached"
	_, pty := seedFakeSession(t, id)

	// Simulate shell exit while no WS is attached.
	pty.triggerEOF()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && HasSession(id) {
		time.Sleep(10 * time.Millisecond)
	}
	if HasSession(id) {
		t.Fatal("session was not cleaned up after PTY EOF")
	}

	client, server := newAgentWSPair(t)
	retry := ReattachTerminal(client, id)
	if retry {
		t.Fatal("ReattachTerminal returned retry=true for an already-exited session")
	}
	if HasSession(id) {
		t.Fatal("ReattachTerminal spawned a brand-new zombie session for an exited ID")
	}
	msg := readAgentControl(t, server, 500*time.Millisecond)
	if msg.Type != controlTypeTerminate {
		t.Fatalf("server got %q, want %q", msg.Type, controlTypeTerminate)
	}
}

// TestCloseSessionTerminatesLocalPTY verifies CloseSession tears down the PTY
// (used when the server rejects reconnect with HTTP 404).
func TestCloseSessionTerminatesLocalPTY(t *testing.T) {
	resetAgentSessions(t)
	const id = "agent-close-helper"
	_, pty := seedFakeSession(t, id)

	if !HasSession(id) {
		t.Fatal("expected seeded session to exist")
	}
	CloseSession(id)
	if HasSession(id) {
		t.Fatal("session still exists after CloseSession")
	}
	if !pty.isClosed() {
		t.Fatal("PTY was not closed by CloseSession")
	}
}
