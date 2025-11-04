package terminal

import (
	"encoding/json"
	"fmt"

	"github.com/gorilla/websocket"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

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

// StartTerminal 启动终端并处理 WebSocket 通信
func StartTerminal(conn *websocket.Conn) {
	if flags.DisableWebSsh {
		conn.WriteMessage(websocket.TextMessage, []byte("\n\nWeb SSH is disabled. Enable it by running without the --disable-web-ssh flag."))
		conn.Close()
		return
	}
	impl, err := newTerminalImpl()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v\r\n", err)))
		return
	}

	errChan := make(chan error, 1)
	defer impl.term.Close()
	// 从 WebSocket 读取消息并写入终端
	go handleWebSocketInput(conn, impl.term, errChan)

	// 从终端读取输出并写入 WebSocket
	go handleTerminalOutput(conn, impl.term, errChan)

	// 错误处理和清理
	go func() {
		err := <-errChan
		if err != nil && conn != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v\r\n", err)))
			conn.Close()
		}
		impl.term.Close()
	}()
	// 等待终端进程结束
	if err := impl.term.Wait(); err != nil {
		select {
		case errChan <- err:
			// 错误已发送
		default:
			// 错误通道已满或已关闭
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Terminal exited with error: %v\r\n", err)))
		}
	}
}

// handleWebSocketInput 处理 WebSocket 输入
func handleWebSocketInput(conn *websocket.Conn, term Terminal, errChan chan<- error) {
	for {
		t, p, err := conn.ReadMessage()
		if err != nil {
			errChan <- err
			return
		}
		if t == websocket.TextMessage {
			var cmd struct {
				Type  string `json:"type"`
				Cols  int    `json:"cols,omitempty"`
				Rows  int    `json:"rows,omitempty"`
				Input string `json:"input,omitempty"`
			}
			if err := json.Unmarshal(p, &cmd); err == nil {
				switch cmd.Type {
				case "resize":
					if cmd.Cols > 0 && cmd.Rows > 0 {
						term.Resize(cmd.Cols, cmd.Rows)
					}
				case "input":
					if cmd.Input != "" {
						term.Write([]byte(cmd.Input))
					}
				}
			} else {
				term.Write(p)
			}
		}
		if t == websocket.BinaryMessage {
			term.Write(p)
		}
	}
}

// handleTerminalOutput 处理终端输出
func handleTerminalOutput(conn *websocket.Conn, term Terminal, errChan chan<- error) {
	buf := make([]byte, 4096)
	for {
		n, err := term.Read(buf)
		if err != nil {
			errChan <- err
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			errChan <- err
			return
		}
	}
}
