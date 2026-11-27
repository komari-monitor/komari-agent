package terminal

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

var (
	configMu sync.RWMutex
	// 与 Server 端会话保留窗口对齐，网络恢复后同一会话可重新附着。
	disconnectedRetention = 5 * time.Minute
	terminalPingInterval  = 30 * time.Second
	terminalReadWait      = 75 * time.Second
	terminalWriteWait     = 10 * time.Second

	errSessionGone = errors.New("terminal session no longer exists")
)

func getTerminalTiming() (retention, pingInterval, readWait, writeWait time.Duration) {
	configMu.RLock()
	defer configMu.RUnlock()
	return disconnectedRetention, terminalPingInterval, terminalReadWait, terminalWriteWait
}

// Terminal 接口定义平台特定的终端操作
type Terminal interface {
	Close() error
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() error
}

// terminalImpl 封装终端和平台特定逻辑
type terminalImpl struct {
	shell      string
	workingDir string
	term       Terminal
}

type terminalSession struct {
	id   string
	term Terminal

	mu              sync.Mutex
	writeMu         sync.Mutex
	conn            *websocket.Conn
	timer           *time.Timer
	timerGen        uint64
	timerDeadline   time.Time
	browserDetached bool
	removed         bool
}

var (
	sessionsMu sync.Mutex
	sessions   = make(map[string]*terminalSession)
)

// StartTerminal keeps a PTY alive briefly after its WebSocket drops. A new
// connection with the same request ID reattaches the existing shell.
func StartTerminal(conn *websocket.Conn, requestID string) bool {
	return startTerminal(conn, requestID, true)
}

// ReattachTerminal reattaches an existing PTY session without spawning a new
// shell if the local session already exited or expired.
func ReattachTerminal(conn *websocket.Conn, requestID string) bool {
	return startTerminal(conn, requestID, false)
}

func startTerminal(conn *websocket.Conn, requestID string, allowCreate bool) bool {
	if flags.DisableWebSsh {
		notifyTerminate(conn)
		_ = conn.Close()
		return false
	}
	if requestID == "" {
		notifyTerminate(conn)
		_ = conn.Close()
		return false
	}

	session, firstConnection, err := acquireSession(requestID, conn, allowCreate)
	if err != nil {
		notifyTerminate(conn)
		_ = conn.Close()
		return false
	}
	if firstConnection {
		go session.readOutput()
	}

	session.handleInput(conn)
	_ = conn.Close()
	session.mu.Lock()
	retry := !session.removed
	session.mu.Unlock()
	return retry
}

// HasSession reports whether a local PTY session with requestID is still alive.
func HasSession(requestID string) bool {
	if requestID == "" {
		return false
	}
	sessionsMu.Lock()
	session := sessions[requestID]
	sessionsMu.Unlock()
	if session == nil {
		return false
	}
	session.mu.Lock()
	alive := !session.removed
	session.mu.Unlock()
	return alive
}

// SessionDeadline returns the current retention deadline of a suspended local
// session, if a retention timer is active.
func SessionDeadline(requestID string) (time.Time, bool) {
	if requestID == "" {
		return time.Time{}, false
	}
	sessionsMu.Lock()
	session := sessions[requestID]
	sessionsMu.Unlock()
	if session == nil {
		return time.Time{}, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.removed || session.timerDeadline.IsZero() {
		return time.Time{}, false
	}
	return session.timerDeadline, true
}

// CloseSession terminates a local PTY session by requestID (e.g. when the
// server rejects reconnect with 404).
func CloseSession(requestID string) {
	if requestID == "" {
		return
	}
	sessionsMu.Lock()
	session := sessions[requestID]
	sessionsMu.Unlock()
	if session != nil {
		closeSession(requestID, session, false)
	}
}

func acquireSession(requestID string, conn *websocket.Conn, allowCreate bool) (*terminalSession, bool, error) {
	sessionsMu.Lock()
	var orphanTerm Terminal
	session := sessions[requestID]
	firstConnection := false
	if session == nil {
		if !allowCreate {
			sessionsMu.Unlock()
			return nil, false, errSessionGone
		}
		sessionsMu.Unlock()

		impl, err := newTerminalImpl()
		if err != nil {
			return nil, false, err
		}
		session = &terminalSession{id: requestID, term: impl.term}

		sessionsMu.Lock()
		if sessions[requestID] != nil {
			session = sessions[requestID]
			orphanTerm = impl.term
		} else {
			sessions[requestID] = session
			firstConnection = true
		}
	}

	session.mu.Lock()
	if session.removed {
		session.mu.Unlock()
		sessionsMu.Unlock()
		if orphanTerm != nil {
			go gracefulClose(orphanTerm)
		}
		return nil, false, errSessionGone
	}
	// 如果已知浏览器处于断开挂起态，重连服务端后不取消本地保留倒计时，
	// 直到收到浏览器恢复信号（ready / resize / heartbeat / 输入）再取消。
	if !session.browserDetached {
		session.stopTimerLocked()
	}
	oldConn := session.conn
	session.conn = conn
	session.mu.Unlock()
	sessionsMu.Unlock()

	if orphanTerm != nil {
		go gracefulClose(orphanTerm)
	}

	if oldConn != nil && oldConn != conn {
		_ = oldConn.Close()
	}
	return session, firstConnection, nil
}

func startConnKeepalive(conn *websocket.Conn) (stop func(), refreshReadDeadline func()) {
	if conn == nil {
		return func() {}, func() {}
	}
	_, pingInterval, readWait, writeWait := getTerminalTiming()
	refresh := func() {
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
	}
	refresh()
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readWait))
	})
	conn.SetPingHandler(func(data string) error {
		if err := conn.SetReadDeadline(time.Now().Add(readWait)); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeWait))
	})

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
					// Ping 写超时说明底层写已被背压卡死：主动关闭连接，
					// 中断阻塞中的数据帧写并触发读循环退出，避免会话半死不活。
					_ = conn.Close()
					return
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}, refresh
}

func (s *terminalSession) handleInput(conn *websocket.Conn) {
	stopKeepalive, refreshReadDeadline := startConnKeepalive(conn)
	defer stopKeepalive()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			s.detach(conn)
			return
		}
		refreshReadDeadline()

		if messageType == websocket.BinaryMessage {
			s.markBrowserActive()
			_, _ = s.term.Write(data)
			continue
		}

		cmd, ok := parseControl(data)
		if !ok {
			s.markBrowserActive()
			_, _ = s.term.Write(data)
			continue
		}
		switch cmd.Type {
		case controlTypeReady:
			s.markBrowserActive()
		case controlTypeResize:
			s.markBrowserActive()
			if cmd.Cols > 0 && cmd.Rows > 0 {
				_ = s.term.Resize(cmd.Cols, cmd.Rows)
			}
		case "input":
			s.markBrowserActive()
			if cmd.Input != "" {
				_, _ = s.term.Write([]byte(cmd.Input))
			}
		case controlTypeTerminate, "close":
			// 服务端要求结束会话：这是终态，不重连。
			closeSession(s.id, s, false)
			return
		case controlTypeAbort:
			// 浏览器一侧掉线：保持当前 WS 连接在线等待恢复或服务端 terminate，
			// 同时在本地按固定保留窗口兜底计时（兼容旧版服务端），避免先断再重连引发续期循环。
			s.markBrowserDetached()
		case controlTypeHeartbeat:
			s.markBrowserActive()
			if cmd.ID != "" {
				_ = s.writeJSON(conn, controlMessage{Type: controlTypeHeartbeat, ID: cmd.ID})
			}
		}
		// 未知控制消息按前向兼容忽略，绝不写进 PTY。
	}
}

func (s *terminalSession) readOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := s.term.Read(buf)
		if err != nil {
			// PTY 结束（用户 exit、进程被杀等）：这是正常终止，明确告诉
			// 服务端不要等重连，而不是留下一根静默断掉的连接。
			closeSession(s.id, s, true)
			return
		}

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			continue
		}
		if err := s.writeMessage(conn, websocket.BinaryMessage, buf[:n]); err != nil {
			s.detach(conn)
			continue
		}
	}
}

func (s *terminalSession) writeMessage(conn *websocket.Conn, messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// 数据帧写会覆写底层 net.Conn 写截止：必须在持有 writeMu 期间显式设置
	// 有限写超时，否则半开连接/TCP 零窗口背压会让 writeMu 被无限期占用，
	// 阻塞到期清理与终止通知，最终泄漏 PTY 与协程。
	_, _, _, writeWait := getTerminalTiming()
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(messageType, data)
}

func (s *terminalSession) writeJSON(conn *websocket.Conn, v interface{}) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _, _, writeWait := getTerminalTiming()
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(v)
}

func (s *terminalSession) startTimerLocked(retention time.Duration) {
	if s.timer != nil || s.removed {
		return
	}
	s.timerGen++
	gen := s.timerGen
	s.timerDeadline = time.Now().Add(retention)
	s.timer = time.AfterFunc(retention, func() {
		closeSessionWithGen(s.id, s, true, &gen)
	})
}

func (s *terminalSession) stopTimerLocked() {
	s.timerGen++
	s.timerDeadline = time.Time{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *terminalSession) markBrowserDetached() {
	retention, _, _, _ := getTerminalTiming()
	s.mu.Lock()
	if !s.removed {
		s.browserDetached = true
		s.startTimerLocked(retention)
	}
	s.mu.Unlock()
}

func (s *terminalSession) markBrowserActive() {
	s.mu.Lock()
	if !s.removed {
		s.browserDetached = false
		s.stopTimerLocked()
	}
	s.mu.Unlock()
}

// detach 保留 PTY，等待服务端用同一个 request_id 重连回来。
// 超过保留窗口仍未重连则清理；若已在挂起倒计时中则不重置截止时间。
func (s *terminalSession) detach(conn *websocket.Conn) {
	retention, _, _, _ := getTerminalTiming()
	s.mu.Lock()
	if s.removed || s.conn != conn {
		s.mu.Unlock()
		return
	}
	s.conn = nil
	s.startTimerLocked(retention)
	s.mu.Unlock()
	_ = conn.Close()
}

func closeSession(requestID string, expected *terminalSession, notifyPeer bool) {
	closeSessionWithGen(requestID, expected, notifyPeer, nil)
}

// closeSessionWithGen 在 sessionsMu -> session.mu 一致锁序的同一临界区内
// 核验会话身份、存活状态及（可选）定时器世代，核验通过后立即提交
// removed 并删除 map 项，随后才释放锁执行网络/PTY 关闭 I/O。
// 定时器到期回调必须传入触发时的世代：浏览器在到期边界恢复会话后
// timerGen 已递增，旧世代回调绝不允许误杀已恢复的会话。
func closeSessionWithGen(requestID string, expected *terminalSession, notifyPeer bool, expectedGen *uint64) {
	if expected == nil {
		return
	}
	sessionsMu.Lock()
	session := sessions[requestID]
	if session != expected {
		sessionsMu.Unlock()
		return
	}
	session.mu.Lock()
	if session.removed ||
		(expectedGen != nil && (session.timerGen != *expectedGen || session.timer == nil)) {
		session.mu.Unlock()
		sessionsMu.Unlock()
		return
	}
	session.removed = true
	session.stopTimerLocked()
	conn := session.conn
	session.conn = nil
	term := session.term
	delete(sessions, requestID)
	session.mu.Unlock()
	sessionsMu.Unlock()

	if conn != nil {
		if notifyPeer {
			// terminate 通知失败/超时不允许阻塞 conn.Close 与 PTY 释放：
			// writeJSON 持有 writeMu 且有有限写超时兜底。
			_ = session.writeJSON(conn, controlMessage{Type: controlTypeTerminate})
		}
		_ = conn.Close()
	}
	_ = gracefulClose(term)
}

func gracefulClose(term Terminal) error {
	gracefulShutdown(term)
	return term.Close()
}

// gracefulShutdown 尝试优雅地关闭终端
func gracefulShutdown(term Terminal) {
	for i := 0; i < 3; i++ {
		if _, err := term.Write([]byte{3}); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)
	_, _ = term.Write([]byte{4})
	time.Sleep(100 * time.Millisecond)
	_, _ = term.Write([]byte("exit\n"))
	time.Sleep(100 * time.Millisecond)
}
