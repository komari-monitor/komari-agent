package server

import (
	"errors"
	"log"

	"github.com/komari-monitor/komari-agent/update"
)

func switchAgentVersion(version string, onRestartRequired func()) {
	if flags.DisableWebSsh {
		log.Print("Ignoring v2 switch version request because remote control is disabled")
		return
	}
	runSwitchVersion(version, update.CheckAndUpdateForVersionLine, onRestartRequired)
}

func runSwitchVersion(version string, check func(string) error, onRestartRequired func()) {
	err := check(version)
	if errors.Is(err, update.ErrRestartRequired) {
		log.Printf("Agent version switch to %q installed; restarting", version)
		if onRestartRequired != nil {
			onRestartRequired()
		}
		return
	}
	if err != nil {
		log.Printf("Failed to switch agent version to %q: %v", version, err)
		return
	}
	log.Printf("Agent version switch to %q completed without restart", version)
}
