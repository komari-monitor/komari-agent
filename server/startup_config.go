package server

import (
	"context"
	"log"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

func handleStartupConfig(params v2.StartupConfigParams) {
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
