package v2

import (
	"encoding/json"
	"time"
)

// 报文构造器：使用共享的类型化参数结构，避免手工拼 map 造成
// 字段名与 server 端契约漂移（编译期保证 key 与 json tag 一致）。

func NewNotification(method string, params interface{}) []byte {
	payload, _ := json.Marshal(Request{JSONRPC: Version, Method: method, Params: params})
	return payload
}

func NewRequest(id interface{}, method string, params interface{}) []byte {
	payload, _ := json.Marshal(Request{JSONRPC: Version, Method: method, Params: params, ID: id})
	return payload
}

// BuildReportPayload wraps the raw v1 report payload (opaque passthrough)
// into an agent.report notification.
func BuildReportPayload(report []byte) []byte {
	return NewNotification(MethodAgentReport, ReportParams{Report: json.RawMessage(report)})
}

func BuildReportRequest(id interface{}, report []byte, ackEventIDs []string) []byte {
	return NewRequest(id, MethodAgentReport, ReportParams{Report: json.RawMessage(report), AckEventIDs: ackEventIDs})
}

func BuildBasicInfoPayload(info map[string]interface{}) []byte {
	return NewNotification(MethodAgentBasicInfo, BasicInfoParams{Info: info})
}

func BuildPingResultPayload(taskID uint, pingType string, value int, finishedAt time.Time) interface{} {
	return Request{
		JSONRPC: Version,
		Method:  MethodAgentPingResult,
		Params: PingResultParams{
			TaskID:     taskID,
			PingType:   pingType,
			Value:      value,
			FinishedAt: finishedAt,
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

func BindResult(raw interface{}, target interface{}) error {
	return BindParams(raw, target)
}
