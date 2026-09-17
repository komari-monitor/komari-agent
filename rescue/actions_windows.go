//go:build windows

package rescue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	rescuev1 "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1"
	"golang.org/x/sys/windows"
)

const windowsIsolationGroup = "Komari Rescue Isolation"

type isolationState struct {
	Mode          rescuev1.NetworkIsolationMode `json:"mode"`
	Interfaces    []string                      `json:"interfaces,omitempty"`
	Profiles      []firewallProfile             `json:"profiles,omitempty"`
	DisabledRules []string                      `json:"disabled_rules,omitempty"`
	AppliedAt     time.Time                     `json:"applied_at"`
}

type firewallProfile struct {
	Name                  string `json:"Name"`
	DefaultInboundAction  string `json:"DefaultInboundAction"`
	DefaultOutboundAction string `json:"DefaultOutboundAction"`
}

func isPrivileged() bool {
	token := windows.Token(0)
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	member, err := token.IsMember(administrators)
	return err == nil && member
}

func platformCommand(ctx context.Context, name string, arguments ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}

func machineDiagnostics(ctx context.Context) (ActionResult, error) {
	script := `$ErrorActionPreference='Continue'; Get-ComputerInfo | Select-Object OsName,OsVersion,WindowsVersion,CsName,CsTotalPhysicalMemory; Get-CimInstance Win32_OperatingSystem | Select-Object LastBootUpTime,FreePhysicalMemory; Get-Volume | Select-Object DriveLetter,FileSystemLabel,Size,SizeRemaining; Get-NetAdapter | Select-Object Name,Status,LinkSpeed,MacAddress; Get-NetIPConfiguration; Get-Service | Where-Object Status -eq 'Stopped' | Select-Object -First 30 Name,DisplayName,StartType`
	return runBounded(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

func preparePowerAction(_ ActionConfig, reboot bool) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("machine power control requires an Administrator rescue helper")
	}
	argument, message := "/s", "shutdown"
	if reboot {
		argument, message = "/r", "reboot"
	}
	return ActionResult{
		Stdout: []byte("machine_" + message + "=scheduled\n"),
		AfterReport: func(context.Context) error {
			time.Sleep(2 * time.Second)
			return exec.Command("shutdown.exe", argument, "/t", "0", "/f").Start()
		},
	}, nil
}

func prepareAgentRestart(config ActionConfig) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("restarting the Agent requires an Administrator rescue helper")
	}
	service := strings.TrimSpace(config.ServiceName)
	if service == "" || strings.ContainsAny(service, "/\\\x00\n\r'") {
		return ActionResult{}, errors.New("invalid Agent service name")
	}
	return ActionResult{
		Stdout: []byte("online_config_rollback=accepted\nagent_restart=scheduled\n"),
		AfterReport: func(context.Context) error {
			time.Sleep(2 * time.Second)
			return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Restart-Service -Name '"+service+"' -Force").Start()
		},
	}, nil
}

func prepareInterfaceIsolation(config ActionConfig, mode rescuev1.NetworkIsolationMode) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("network isolation requires an Administrator rescue helper")
	}
	interfaces, err := interfacesForMode(mode)
	if err != nil {
		return ActionResult{}, err
	}
	quoted := make([]string, len(interfaces))
	for index, name := range interfaces {
		quoted[index] = powerShellQuote(name)
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule; $aliases=@(%s); foreach($alias in $aliases) { New-NetFirewallRule -DisplayName ('Komari block inbound '+$alias) -Group '%s' -Direction Inbound -Action Block -InterfaceAlias $alias; New-NetFirewallRule -DisplayName ('Komari block outbound '+$alias) -Group '%s' -Direction Outbound -Action Block -InterfaceAlias $alias }`, windowsIsolationGroup, strings.Join(quoted, ","), windowsIsolationGroup, windowsIsolationGroup)
	return ActionResult{
		Stdout: []byte(fmt.Sprintf("network_isolation=scheduled\ninterfaces=%s\n", strings.Join(interfaces, ","))),
		AfterReport: func(ctx context.Context) error {
			time.Sleep(2 * time.Second)
			state := isolationState{Mode: mode, Interfaces: interfaces, AppliedAt: time.Now().UTC()}
			if err := saveIsolationState(config, state); err != nil {
				return fmt.Errorf("save network isolation state: %w", err)
			}
			if _, stderr, err := platformCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
				_ = restoreWindowsIsolation(context.Background(), state)
				_ = os.Remove(isolationPath(config))
				return fmt.Errorf("apply Komari interface isolation: %s: %w", strings.TrimSpace(string(stderr)), err)
			}
			return nil
		},
	}, nil
}

func prepareControlPlaneIsolation(ctx context.Context, config ActionConfig) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("network isolation requires an Administrator rescue helper")
	}
	addresses, port, err := resolveControlPlane(ctx, config.ControlPlaneURL)
	if err != nil {
		return ActionResult{}, err
	}
	snapshotJSON, stderr, err := platformCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$profiles=@(Get-NetFirewallProfile | ForEach-Object { [pscustomobject]@{Name=$_.Name;DefaultInboundAction=$_.DefaultInboundAction.ToString();DefaultOutboundAction=$_.DefaultOutboundAction.ToString()} }); $rules=@(Get-NetFirewallRule -Enabled True | Where-Object Group -ne 'Komari Rescue Isolation' | Select-Object -ExpandProperty Name); [pscustomobject]@{profiles=$profiles;disabled_rules=$rules} | ConvertTo-Json -Depth 4 -Compress`)
	if err != nil {
		return ActionResult{}, fmt.Errorf("read Windows Firewall profiles: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var snapshot isolationState
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return ActionResult{}, fmt.Errorf("decode Windows Firewall state: %w", err)
	}
	snapshot.Mode = rescuev1.NetworkIsolationMode_NETWORK_ISOLATION_MODE_CONTROL_PLANE_ONLY
	snapshot.AppliedAt = time.Now().UTC()
	if err := validateFirewallSnapshot(snapshot); err != nil {
		return ActionResult{}, err
	}
	remote := make([]string, len(addresses))
	for index, address := range addresses {
		remote[index] = address.String()
	}
	disabledRules := powerShellArray(snapshot.DisabledRules)
	remoteAddresses := "@(" + powerShellArray(remote) + ")"
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule; @(%s) | ForEach-Object { Get-NetFirewallRule -Name $_ -ErrorAction SilentlyContinue | Disable-NetFirewallRule }; Set-NetFirewallProfile -Profile Domain,Private,Public -DefaultInboundAction Block -DefaultOutboundAction Block; New-NetFirewallRule -DisplayName 'Komari control plane TCP' -Group '%s' -Direction Outbound -Action Allow -Protocol TCP -RemotePort %d -RemoteAddress %s; New-NetFirewallRule -DisplayName 'Komari control plane UDP' -Group '%s' -Direction Outbound -Action Allow -Protocol UDP -RemotePort %d -RemoteAddress %s`, windowsIsolationGroup, disabledRules, windowsIsolationGroup, port, remoteAddresses, windowsIsolationGroup, port, remoteAddresses)
	return ActionResult{
		Stdout: []byte("network_isolation=control-plane-only-scheduled\n"),
		AfterReport: func(ctx context.Context) error {
			time.Sleep(2 * time.Second)
			if err := saveIsolationState(config, snapshot); err != nil {
				return fmt.Errorf("save Windows Firewall state: %w", err)
			}
			if _, stderr, err := platformCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
				_ = restoreWindowsIsolation(context.Background(), snapshot)
				_ = os.Remove(isolationPath(config))
				return fmt.Errorf("apply control-plane isolation: %s: %w", strings.TrimSpace(string(stderr)), err)
			}
			return nil
		},
	}, nil
}

func restoreNetwork(ctx context.Context, config ActionConfig) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("restoring network access requires an Administrator rescue helper")
	}
	state, _ := loadIsolationState(config)
	if err := restoreWindowsIsolation(ctx, state); err != nil {
		return ActionResult{}, err
	}
	_ = os.Remove(isolationPath(config))
	return ActionResult{Stdout: []byte("network_isolation=removed\n")}, nil
}

func networkIsolationStatus(config ActionConfig) (rescuev1.NetworkIsolationMode, []string) {
	state, err := loadIsolationState(config)
	if err != nil {
		return rescuev1.NetworkIsolationMode_NETWORK_ISOLATION_MODE_NONE, nil
	}
	return state.Mode, append([]string(nil), state.Interfaces...)
}

func interfacesForMode(mode rescuev1.NetworkIsolationMode) ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []string
	for _, item := range interfaces {
		name := strings.ToLower(item.Name)
		if mode == rescuev1.NetworkIsolationMode_NETWORK_ISOLATION_MODE_TAILSCALE_INTERFACES && strings.HasPrefix(name, "tailscale") {
			result = append(result, item.Name)
		}
	}
	if mode == rescuev1.NetworkIsolationMode_NETWORK_ISOLATION_MODE_PUBLIC_INTERFACES {
		output, stderr, err := platformCommand(context.Background(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Get-NetRoute | Where-Object { $_.DestinationPrefix -in @('0.0.0.0/0','::/0') } | Sort-Object RouteMetric | Select-Object -ExpandProperty InterfaceIndex -Unique`)
		if err != nil {
			return nil, fmt.Errorf("discover public interfaces: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
		for _, line := range strings.Fields(string(output)) {
			index, err := strconv.Atoi(line)
			if err == nil {
				if item, lookupErr := net.InterfaceByIndex(index); lookupErr == nil {
					result = append(result, item.Name)
				}
			}
		}
	}
	result = uniqueStrings(result)
	if len(result) == 0 {
		return nil, errors.New("no matching network interfaces were found")
	}
	return result, nil
}

func resolveControlPlane(ctx context.Context, endpoint string) ([]net.IP, uint16, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, 0, errors.New("a valid HTTP(S) control plane URL is required")
	}
	port := uint16(443)
	if parsed.Scheme == "http" {
		port = 80
	}
	if parsed.Port() != "" {
		value, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil || value == 0 {
			return nil, 0, errors.New("control plane port is invalid")
		}
		port = uint16(value)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, 0, fmt.Errorf("resolve control plane: %w", err)
	}
	return addresses, port, nil
}

func isolationPath(config ActionConfig) string {
	if value := strings.TrimSpace(config.IsolationStatePath); value != "" {
		return value
	}
	return filepath.Join(os.Getenv("ProgramData"), "Komari", "Rescue", "network-isolation.json")
}

func saveIsolationState(config ActionConfig, state isolationState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := isolationPath(config)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadIsolationState(config ActionConfig) (isolationState, error) {
	data, err := os.ReadFile(isolationPath(config))
	if err != nil {
		return isolationState{}, err
	}
	var state isolationState
	err = json.Unmarshal(data, &state)
	return state, err
}

func restoreWindowsIsolation(ctx context.Context, state isolationState) error {
	if err := validateFirewallSnapshot(state); err != nil {
		return err
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule`, windowsIsolationGroup)
	for _, profile := range state.Profiles {
		script += fmt.Sprintf("; Set-NetFirewallProfile -Profile %s -DefaultInboundAction %s -DefaultOutboundAction %s", profile.Name, profile.DefaultInboundAction, profile.DefaultOutboundAction)
	}
	if len(state.DisabledRules) > 0 {
		script += fmt.Sprintf("; @(%s) | ForEach-Object { Get-NetFirewallRule -Name $_ -ErrorAction SilentlyContinue | Enable-NetFirewallRule }", powerShellArray(state.DisabledRules))
	}
	if _, stderr, err := platformCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
		return fmt.Errorf("remove Komari network isolation: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

func validateFirewallSnapshot(state isolationState) error {
	validProfile := map[string]bool{"Domain": true, "Private": true, "Public": true}
	validAction := map[string]bool{"Allow": true, "Block": true, "NotConfigured": true}
	for _, profile := range state.Profiles {
		if !validProfile[profile.Name] || !validAction[profile.DefaultInboundAction] || !validAction[profile.DefaultOutboundAction] {
			return errors.New("stored Windows Firewall profile state is invalid")
		}
	}
	for _, name := range state.DisabledRules {
		if strings.ContainsAny(name, "\x00\r\n") {
			return errors.New("stored Windows Firewall rule name is invalid")
		}
	}
	return nil
}

func powerShellArray(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = powerShellQuote(value)
	}
	return strings.Join(quoted, ",")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func powerShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// Temporary SSH exposure is not implemented on Windows. The Linux path relies
// on a kernel-enforced nftables element timeout, or a systemd transient timer,
// to close the window without the control plane; an equivalent here would need
// a scheduled task whose failure modes differ enough to warrant its own
// design rather than a partial port.
func grantTemporarySSHAccess(context.Context, ActionConfig, uint32) (ActionResult, error) {
	return ActionResult{}, errors.New("temporary SSH access is not supported on Windows")
}

func revokeTemporarySSHAccess(context.Context, ActionConfig) (ActionResult, error) {
	return ActionResult{}, errors.New("temporary SSH access is not supported on Windows")
}

// ReconcileTemporarySSHAccess and CloseTemporarySSHAccess have nothing to
// reconcile where the grant itself is unsupported, so they succeed rather than
// failing the helper's lease loop.
func ReconcileTemporarySSHAccess(context.Context, ActionConfig) error { return nil }
func CloseTemporarySSHAccess(context.Context, ActionConfig) error     { return nil }
