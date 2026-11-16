package terminal

import (
	"encoding/json"

	"github.com/gorilla/websocket"
)

// 终端 WebSocket 的控制消息协议（被控端侧）。
//
// 一条 WebSocket 上同时跑两类数据，靠帧类型区分：
//   - 二进制帧 = 终端内容（PTY 输入输出），双向原样转发
//   - 文本帧   = 控制消息，即下面这些 type
//
// 被控端只关心服务端发来的 terminate / abort / resize，以及自己发出的
// terminate。其余消息一律忽略——这是前向兼容的全部要求，新增类型无需
// 任何一个客户端改代码。
//
// abort 和 terminate 不带参数：协议只有两方，收到 abort 自己就是仍然在线的
// 那方，无需说明是谁断了；terminate 只表达"到此为止"。
const (
	controlTypeResize    = "resize"
	controlTypeTerminate = "terminate"
	controlTypeAbort     = "abort"
)

// controlFrame 是控制消息的统一结构。
type controlFrame struct {
	Type string `json:"type"`
}

// controlMessage 是入站控制消息的宽松视图：未知字段忽略，未知 type 忽略。
type controlMessage struct {
	Type  string `json:"type"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Input string `json:"input,omitempty"`
}

// writeControl 发出一条控制消息。写失败由读循环收尾，这里不重复处理。
func writeControl(conn *websocket.Conn, frame controlFrame) {
	if conn == nil {
		return
	}
	_ = conn.WriteJSON(frame)
}

func notifyTerminate(conn *websocket.Conn) {
	writeControl(conn, controlFrame{Type: controlTypeTerminate})
}

// parseControl 解析一帧文本消息。非 JSON 或没有 type 的帧返回 ok=false，
// 调用方据此把它当作 PTY 输入。未知 type 仍是 ok=true，由调用方忽略。
func parseControl(data []byte) (controlMessage, bool) {
	var msg controlMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return controlMessage{}, false
	}
	if msg.Type == "" {
		return controlMessage{}, false
	}
	return msg, true
}
