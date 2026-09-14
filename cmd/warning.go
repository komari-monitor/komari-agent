package cmd

import (
	"fmt"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"unicode"

	"github.com/komari-monitor/komari-agent/utils"
)

const (
	warningTitle        = "[Komari] Remote control is enabled on this device"
	warningAdvice       = "If you did not set this up, your device may have been accessed without authorization."
	warningCompromise   = "Stop Komari Agent immediately and check your device for signs of compromise."
	warningUninstallURL = "https://komari-document.pages.dev/en/faq/uninstall"
)

type securityWarning struct {
	PanelHost string
	RunAsUser string
}

func newSecurityWarning(endpoint, runAsUser string) securityWarning {
	return securityWarning{
		PanelHost: warningHost(endpoint),
		RunAsUser: warningSingleLine(runAsUser),
	}
}

func (w securityWarning) message() string {
	return fmt.Sprintf("%s can execute commands and read or modify files on this device as %s.\n\n%s\n%s\n\nUninstall Komari Agent: %s",
		w.PanelHost, w.RunAsUser, warningAdvice, warningCompromise, warningUninstallURL)
}

func warningHost(endpoint string) string {
	const unknownHost = "the configured Komari server"
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, "://") && !strings.HasPrefix(endpoint, "//") {
		endpoint = "//" + endpoint
	}
	endpoint, err := utils.ConvertIDNToASCII(endpoint)
	if err != nil {
		return unknownHost
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return unknownHost
	}
	host := warningSingleLine(u.Host)
	if host == "" {
		return unknownHost
	}
	return host
}

func warningSingleLine(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, value))
}

func warningCurrentUser() string {
	if uid := os.Geteuid(); uid >= 0 {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			return u.Username
		}
		return "UID " + strconv.Itoa(uid)
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "the agent's account"
}
