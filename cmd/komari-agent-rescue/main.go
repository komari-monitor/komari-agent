package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/komari-monitor/komari-agent/rescue"
	"github.com/komari-monitor/komari-agent/update"
)

func main() {
	config, err := configFromFileArgument(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	var configFile string
	var revokeTemporarySSH bool
	flag.StringVar(&configFile, "config", "", "Restricted JSON helper configuration path")
	flag.BoolVar(&revokeTemporarySSH, "revoke-temporary-ssh", false, "Close any open temporary SSH access window and exit")
	flag.StringVar(&config.Endpoint, "endpoint", env("KOMARI_RESCUE_ENDPOINT", config.Endpoint), "Komari Connect endpoint")
	flag.StringVar(&config.Token, "token", env("KOMARI_RESCUE_TOKEN", config.Token), "Agent API token")
	flag.StringVar(&config.CFAccessClientID, "cf-access-client-id", env("KOMARI_RESCUE_CF_ACCESS_CLIENT_ID", config.CFAccessClientID), "Cloudflare Access service-token Client ID")
	flag.StringVar(&config.CFAccessClientSecret, "cf-access-client-secret", env("KOMARI_RESCUE_CF_ACCESS_CLIENT_SECRET", config.CFAccessClientSecret), "Cloudflare Access service-token Client Secret")
	flag.StringVar(&config.AgentID, "agent-id", env("KOMARI_RESCUE_AGENT_ID", config.AgentID), "Expected Agent ID (optional; authentication remains authoritative)")
	flag.StringVar(&config.InstanceIDPath, "instance-id-file", env("KOMARI_RESCUE_INSTANCE_ID_FILE", config.InstanceIDPath), "Persistent helper instance ID path")
	flag.BoolVar(&config.IgnoreUnsafeCert, "ignore-unsafe-cert", config.IgnoreUnsafeCert || envBool("KOMARI_RESCUE_IGNORE_UNSAFE_CERT"), "Ignore unsafe certificate errors")
	flag.StringVar(&config.Action.AgentPath, "agent-path", env("KOMARI_RESCUE_AGENT_PATH", config.Action.AgentPath), "Installed Agent binary path")
	flag.StringVar(&config.Action.ConfigPath, "agent-config", env("KOMARI_RESCUE_AGENT_CONFIG", config.Action.ConfigPath), "Installed Agent JSON config path")
	flag.StringVar(&config.Action.RuntimeStatePath, "runtime-state-file", env("KOMARI_RESCUE_RUNTIME_STATE_FILE", config.Action.RuntimeStatePath), "Agent runtime snapshot path")
	flag.StringVar(&config.Action.ServiceName, "agent-service-name", env("KOMARI_RESCUE_AGENT_SERVICE_NAME", fallback(config.Action.ServiceName, "komari-agent")), "Installed Agent service name")
	flag.StringVar(&config.Action.RuntimeIdentity, "agent-runtime-identity", env("KOMARI_RESCUE_AGENT_RUNTIME_IDENTITY", config.Action.RuntimeIdentity), "Installed Agent runtime identity")
	flag.StringVar(&config.Action.RuntimeUser, "agent-runtime-user", env("KOMARI_RESCUE_AGENT_RUNTIME_USER", config.Action.RuntimeUser), "Installed Agent runtime user")
	flag.StringVar(&config.Action.ControlPlaneURL, "control-plane-url", env("KOMARI_RESCUE_CONTROL_PLANE_URL", fallback(config.Action.ControlPlaneURL, config.Endpoint)), "Control plane URL retained during network isolation")
	flag.StringVar(&config.Action.IsolationStatePath, "isolation-state-file", env("KOMARI_RESCUE_ISOLATION_STATE_FILE", config.Action.IsolationStatePath), "Komari-managed network isolation state path")
	flag.Parse()
	config.Version = update.CurrentVersion
	// The revocation timer runs this binary as a bare process with no
	// credentials, so it must not require an endpoint or token: closing a
	// firewall window is purely local and has to work when the control plane
	// is exactly what is unreachable.
	if revokeTemporarySSH {
		if err := rescue.CloseTemporarySSHAccess(context.Background(), config.Action); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(config); err != nil {
		log.Fatal(err)
	}
}

func configFromFileArgument(arguments []string) (rescue.Config, error) {
	path := ""
	for index := range arguments {
		if arguments[index] == "--config" && index+1 < len(arguments) {
			path = arguments[index+1]
			break
		}
	}
	if path == "" {
		return rescue.Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rescue.Config{}, fmt.Errorf("read helper config: %w", err)
	}
	var config rescue.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return rescue.Config{}, fmt.Errorf("decode helper config: %w", err)
	}
	return config, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	switch os.Getenv(name) {
	case "1", "true", "TRUE", "yes", "YES":
		return true
	default:
		return false
	}
}

func fallback(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}

func run(config rescue.Config) error {
	if config.Endpoint == "" || config.Token == "" {
		return fmt.Errorf("--endpoint and --token are required")
	}
	helper, err := rescue.New(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return helper.Run(ctx)
}
