// Package v2 re-exports the shared JSON-RPC 2.0 wire contract.
//
// The wire types live in the komari-protocol module (guarded there by freeze
// tests); this shell keeps existing import paths stable. Payload builders
// live in builders.go.
package v2

import protocolv2 "github.com/komari-monitor/komari-protocol/protocol/v2"

const (
	Version               = protocolv2.Version
	MethodAgentReport     = protocolv2.MethodAgentReport
	MethodAgentBasicInfo  = protocolv2.MethodAgentBasicInfo
	MethodAgentPingResult = protocolv2.MethodAgentPingResult
	MethodAgentTaskResult = protocolv2.MethodAgentTaskResult
	MethodAgentExec       = protocolv2.MethodAgentExec
	MethodAgentPing       = protocolv2.MethodAgentPing
	MethodAgentMessage    = protocolv2.MethodAgentMessage
	MethodAgentEvent      = protocolv2.MethodAgentEvent
	MethodAgentTerminal   = protocolv2.MethodAgentTerminal
	MethodAgentPull       = protocolv2.MethodAgentPull
)

type (
	Request               = protocolv2.Request
	Response              = protocolv2.Response
	RPCError              = protocolv2.RPCError
	Event                 = protocolv2.Event
	EventResult           = protocolv2.EventResult
	ReportParams          = protocolv2.ReportParams
	BasicInfoParams       = protocolv2.BasicInfoParams
	PingResultParams      = protocolv2.PingResultParams
	PullParams            = protocolv2.PullParams
	ExecParams            = protocolv2.ExecParams
	PingParams            = protocolv2.PingParams
	MessageParams         = protocolv2.MessageParams
	EventParams           = protocolv2.EventParams
	TerminalRequestParams = protocolv2.TerminalRequestParams
)
