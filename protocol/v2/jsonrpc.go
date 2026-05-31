package v2

import (
	"encoding/json"
	"time"

	v1 "github.com/komari-monitor/komari-agent/protocol/v1"
)

const (
	Version               = "2.0"
	MethodAgentReport     = "agent.report"
	MethodAgentBasicInfo  = "agent.basicInfo"
	MethodAgentPingResult = "agent.pingResult"
	MethodAgentTaskResult = "agent.taskResult"
	MethodAgentExec       = "agent.exec"
	MethodAgentPing       = "agent.ping"
	MethodAgentMessage    = "agent.message"
	MethodAgentEvent      = "agent.event"
	MethodAgentTerminal   = "agent.terminal.request"
)

type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

func NewNotification(method string, params interface{}) []byte {
	payload, _ := json.Marshal(Request{JSONRPC: Version, Method: method, Params: params})
	return payload
}

func BuildReportPayload(report v1.ReportPayload) []byte {
	var raw interface{}
	_ = json.Unmarshal(report, &raw)
	return NewNotification(MethodAgentReport, map[string]interface{}{"report": raw})
}

func BuildBasicInfoPayload(info map[string]interface{}) []byte {
	return NewNotification(MethodAgentBasicInfo, map[string]interface{}{"info": info})
}

func BuildPingResultPayload(taskID uint, pingType string, value int, finishedAt time.Time) interface{} {
	return Request{
		JSONRPC: Version,
		Method:  MethodAgentPingResult,
		Params: map[string]interface{}{
			"task_id":     taskID,
			"ping_type":   pingType,
			"value":       value,
			"finished_at": finishedAt.Format(time.RFC3339Nano),
		},
	}
}

func BindParams(raw interface{}, target interface{}) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
