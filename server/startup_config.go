package server

import (
	"context"
	"log"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

// 在途上报并发上限：异常或被控面板连续下发 startup_config 查询时，
// 防止无界放大出站 HTTP POST 协程数。超出上限的查询直接丢弃。
const startupConfigMaxInflight = 2

var startupConfigInflight = make(chan struct{}, startupConfigMaxInflight)

func handleStartupConfig(params v2.StartupConfigParams) {
	select {
	case startupConfigInflight <- struct{}{}:
		defer func() { <-startupConfigInflight }()
	default:
		log.Print("dropping agent startup configuration query: too many in flight")
		return
	}
	result := v2.StartupConfigResult{
		RequestID: params.RequestID,
		Config:    pkg_flags.StartupConfig(),
	}
	if result.Config == nil {
		result.Error = "startup configuration is not available"
	}
	// Use the existing authenticated POST transport for both WS and pull
	// requests. Never log the payload or transport errors containing tokens.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	payload := v2.NewRequest(params.RequestID, v2.MethodAgentStartupConfigResult, result)
	if _, err := postV2RequestContext(ctx, payload); err != nil {
		log.Print("failed to return agent startup configuration")
	}
}
