package terminal

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

// 与 Server 端会话保留窗口对齐，网络恢复后同一会话可重新附着。
const disconnectedRetention = 5 * time.Minute

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

	mu      sync.Mutex
	conn    *websocket.Conn
	timer   *time.Timer
	removed bool
}

var (
	sessionsMu sync.Mutex
	sessions   = make(map[string]*terminalSession)
)

// StartTerminal keeps a PTY alive briefly after its WebSocket drops. A new
// connection with the same request ID reattaches the existing shell.
func StartTerminal(conn *websocket.Conn, requestID string) bool {
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

	session, firstConnection, err := acquireSession(requestID, conn)
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

func acquireSession(requestID string, conn *websocket.Conn) (*terminalSession, bool, error) {
	sessionsMu.Lock()
	var orphanTerm Terminal
	session := sessions[requestID]
	firstConnection := false
	if session == nil {
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
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
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

func (s *terminalSession) handleInput(conn *websocket.Conn) {
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			s.detach(conn)
			return
		}

		if messageType == websocket.BinaryMessage {
			_, _ = s.term.Write(data)
			continue
		}

		cmd, ok := parseControl(data)
		if !ok {
			_, _ = s.term.Write(data)
			continue
		}
		switch cmd.Type {
		case controlTypeResize:
			if cmd.Cols > 0 && cmd.Rows > 0 {
				_ = s.term.Resize(cmd.Cols, cmd.Rows)
			}
		case "input":
			if cmd.Input != "" {
				_, _ = s.term.Write([]byte(cmd.Input))
			}
		case controlTypeTerminate, "close":
			// 服务端要求结束会话：这是终态，不重连。
			closeSession(s.id, s)
			return
		case controlTypeAbort:
			// 浏览器一侧掉线：PTY 保留，等服务端重连后重新附着。
			s.detach(conn)
			return
		case "heartbeat":
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
			s.mu.Lock()
			conn := s.conn
			s.mu.Unlock()
			notifyTerminate(conn)
			closeSession(s.id, s)
			return
		}

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			continue
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			s.detach(conn)
			continue
		}
	}
}

// detach 保留 PTY，等待服务端用同一个 request_id 重连回来。
// 超过保留窗口仍未重连则清理。
func (s *terminalSession) detach(conn *websocket.Conn) {
	s.mu.Lock()
	if s.removed || s.conn != conn {
		s.mu.Unlock()
		return
	}
	s.conn = nil
	if s.timer == nil {
		s.timer = time.AfterFunc(disconnectedRetention, func() {
			closeSession(s.id, s)
		})
	}
	s.mu.Unlock()
	_ = conn.Close()
}

func closeSession(requestID string, expected *terminalSession) {
	sessionsMu.Lock()
	session := sessions[requestID]
	if session == nil || session != expected {
		sessionsMu.Unlock()
		return
	}
	delete(sessions, requestID)
	sessionsMu.Unlock()

	session.mu.Lock()
	if session.removed {
		session.mu.Unlock()
		return
	}
	session.removed = true
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	conn := session.conn
	session.conn = nil
	term := session.term
	session.mu.Unlock()

	if conn != nil {
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
