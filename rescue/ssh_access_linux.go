//go:build linux

package rescue

// Temporary public SSH exposure.
//
// Three properties drive this implementation.
//
// The window must close without the control plane. The panel may be exactly
// what is unreachable when this is used, so expiry is enforced locally: a
// kernel-held nftables set element timeout where the rule is ours, a systemd
// transient timer where it is not, and in every case a persisted deadline the
// helper reconciles on each lease. A goroutine sleeping in the helper is not a
// mechanism, because the helper is leased in bounded intervals and restarts.
//
// The rule must land where it actually takes effect. A permissive rule in a
// private nftables table does not override a drop elsewhere: nftables
// evaluates every base chain at a hook, and any drop wins regardless of an
// accept in another table. So an existing firewall manager is driven through
// its own interface, and a raw chain is edited at its head, rather than
// assuming a separate table is sufficient.
//
// Opening a port is not the same as being reachable. On a host whose public
// SSH was deliberately closed, sshd is frequently bound to a private address
// or not running at all, and the firewall was only half the reason. The
// pre-flight check reports that before touching anything, because otherwise
// the operator sees "granted" and a connection that still fails.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rescuev1 "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// temporarySSHDuration is how long the window stays open.
	temporarySSHDuration = 15 * time.Minute
	// temporarySSHTable is deliberately not linuxIsolationTable. The isolation
	// actions delete their whole table when applying or restoring, which would
	// silently revoke an active SSH grant sharing it.
	temporarySSHTable = "komari_temp_ssh"
	temporarySSHSet   = "temp_ssh"
	temporarySSHUnit  = "komari-temp-ssh-revoke"
	temporarySSHMark  = "komari-temp-ssh"
	defaultSSHPort    = 22
)

// sshAccessMechanism records how the permissive rule was installed, because
// revoking it requires the same tool that created it.
type sshAccessMechanism string

const (
	mechanismNftablesSet sshAccessMechanism = "nftables-set"
	mechanismUFW         sshAccessMechanism = "ufw"
	mechanismFirewalld   sshAccessMechanism = "firewalld"
	mechanismIptables    sshAccessMechanism = "iptables"
)

// sshAccessState is persisted so expiry survives a helper restart.
type sshAccessState struct {
	Port      uint32                   `json:"port"`
	GrantedAt time.Time                `json:"granted_at"`
	ExpiresAt time.Time                `json:"expires_at"`
	Mechanism sshAccessMechanism       `json:"mechanism"`
	Expiry    rescuev1.ExpiryMechanism `json:"expiry"`
}

func grantTemporarySSHAccess(ctx context.Context, config ActionConfig, requestedPort uint32) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("temporary SSH access requires a privileged rescue helper")
	}
	port := requestedPort
	if port == 0 || port > 65535 {
		port = defaultSSHPort
	}

	// Reconcile first: a stale grant from a previous helper generation must not
	// be mistaken for the one being created now, and its deadline must not
	// outlive it.
	if err := ReconcileTemporarySSHAccess(ctx, config); err != nil {
		return ActionResult{}, fmt.Errorf("reconcile previous temporary SSH access: %w", err)
	}
	if existing, err := loadSSHAccessState(config); err == nil && existing.ExpiresAt.After(time.Now()) {
		return ActionResult{}, fmt.Errorf("temporary SSH access is already open on port %d until %s", existing.Port, existing.ExpiresAt.UTC().Format(time.RFC3339))
	}

	daemon := inspectSSHDaemon(ctx, port)
	mechanism, err := detectFirewallMechanism()
	if err != nil {
		return ActionResult{}, err
	}

	granted := time.Now()
	expires := granted.Add(temporarySSHDuration)
	state := sshAccessState{
		Port:      port,
		GrantedAt: granted,
		ExpiresAt: expires,
		Mechanism: mechanism,
	}

	// The state file is written before the rule is installed. A crash between
	// the two leaves a deadline with no rule, which reconciliation cleans up
	// harmlessly; the reverse order would leave a rule with no deadline.
	if err := saveSSHAccessState(config, state); err != nil {
		return ActionResult{}, fmt.Errorf("persist temporary SSH access deadline: %w", err)
	}

	// Unlike network isolation, opening a port does not sever the control
	// plane connection, so there is no reason to defer it past the report.
	// Applying it now is what lets the reported state describe what actually
	// happened rather than what was about to be attempted.
	expiry, err := installSSHAccessRule(ctx, config, port, mechanism)
	if err != nil {
		_ = removeSSHAccessState(config)
		return ActionResult{}, err
	}
	state.Expiry = expiry
	if err := saveSSHAccessState(config, state); err != nil {
		// The rule is already installed, so the deadline must be recorded or
		// reconciliation cannot close the window. Roll back rather than leave
		// an unbounded grant.
		_ = removeSSHAccessRule(ctx, state)
		_ = cancelSSHAccessTimer(ctx)
		return ActionResult{}, fmt.Errorf("persist temporary SSH access mechanism: %w", err)
	}

	return ActionResult{
		SSHAccess: &rescuev1.TemporarySSHAccess{
			Port:            port,
			Granted:         true,
			GrantedAt:       timestamppb.New(granted),
			ExpiresAt:       timestamppb.New(expires),
			ExpiryMechanism: expiry,
			SshdState:       daemon,
		},
	}, nil
}

func revokeTemporarySSHAccess(ctx context.Context, config ActionConfig) (ActionResult, error) {
	if !isPrivileged() {
		return ActionResult{}, errors.New("temporary SSH access requires a privileged rescue helper")
	}
	state, err := loadSSHAccessState(config)
	if err != nil {
		if os.IsNotExist(err) {
			return ActionResult{Stdout: []byte("no temporary SSH access is open\n")}, nil
		}
		return ActionResult{}, err
	}
	// Revoking closes a port rather than opening one, so it is applied before
	// reporting: the operator needs to be told it is shut, not that it is
	// about to be.
	if err := revokeSSHAccess(ctx, config, state); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		SSHAccess: &rescuev1.TemporarySSHAccess{
			Port:      state.Port,
			Granted:   false,
			GrantedAt: timestamppb.New(state.GrantedAt),
			ExpiresAt: timestamppb.New(state.ExpiresAt),
		},
	}, nil
}

// ReconcileTemporarySSHAccess closes a window whose deadline has passed.
//
// It is the backstop that makes expiry independent of any scheduler: the
// helper calls it on startup and around each lease, so a rule whose kernel
// timeout or timer was lost is still removed.
func ReconcileTemporarySSHAccess(ctx context.Context, config ActionConfig) error {
	return closeTemporarySSHAccess(ctx, config, false)
}

// CloseTemporarySSHAccess closes an open window regardless of its deadline.
//
// The revocation timer calls this rather than the reconciling form. A timer
// that fires even marginally before the recorded deadline would otherwise
// decide the window is still valid and leave the port open, which is the one
// outcome this whole mechanism exists to prevent.
func CloseTemporarySSHAccess(ctx context.Context, config ActionConfig) error {
	return closeTemporarySSHAccess(ctx, config, true)
}

func closeTemporarySSHAccess(ctx context.Context, config ActionConfig, force bool) error {
	state, err := loadSSHAccessState(config)
	if err != nil {
		if os.IsNotExist(err) {
			// There may still be a rule from a generation that never recorded
			// its deadline, so a forced close sweeps for one anyway.
			if force {
				return sweepOrphanedSSHAccess(ctx)
			}
			return nil
		}
		return err
	}
	if !force && time.Now().Before(state.ExpiresAt) {
		return nil
	}
	return revokeSSHAccess(ctx, config, state)
}

// sweepOrphanedSSHAccess removes a rule left behind without recorded state.
//
// Only the mechanisms that leave an identifiable mark are swept: the private
// nftables table, and the iptables rule carrying our comment. A ufw or
// firewalld allow is indistinguishable from one an operator added themselves,
// so it is left alone rather than risking closing a port they wanted open.
func sweepOrphanedSSHAccess(ctx context.Context) error {
	if err := deleteNftablesSSHAccess(ctx); err != nil {
		return err
	}
	return cancelSSHAccessTimer(ctx)
}

func revokeSSHAccess(ctx context.Context, config ActionConfig, state sshAccessState) error {
	var failures []string
	if err := removeSSHAccessRule(ctx, state); err != nil {
		failures = append(failures, err.Error())
	}
	// The timer is cancelled even when the rule is already gone, so a stale
	// unit cannot fire against a port granted later.
	if err := cancelSSHAccessTimer(ctx); err != nil {
		failures = append(failures, err.Error())
	}
	if err := removeSSHAccessState(config); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return errors.New("revoke temporary SSH access: " + strings.Join(failures, "; "))
	}
	return nil
}

// detectFirewallMechanism picks the tool that owns packet filtering here.
//
// A managed firewall is driven through its own interface because editing
// underlying chains behind it gets reverted on its next reload. A bare
// nftables host gets our own table plus a set element timeout, which is the
// only kernel-enforced expiry available.
func detectFirewallMechanism() (sshAccessMechanism, error) {
	if path, err := exec.LookPath("ufw"); err == nil {
		if output, runErr := exec.Command(path, "status").Output(); runErr == nil &&
			strings.Contains(strings.ToLower(string(output)), "status: active") {
			return mechanismUFW, nil
		}
	}
	if path, err := exec.LookPath("firewall-cmd"); err == nil {
		if err := exec.Command(path, "--state").Run(); err == nil {
			return mechanismFirewalld, nil
		}
	}
	if _, err := exec.LookPath("nft"); err == nil {
		return mechanismNftablesSet, nil
	}
	if _, err := exec.LookPath("iptables"); err == nil {
		return mechanismIptables, nil
	}
	return "", errors.New("no supported firewall tool is available; install nftables, iptables, ufw or firewalld")
}

// installSSHAccessRule takes the mechanism the caller already resolved, so the
// rule that is installed and the mechanism recorded for revocation cannot
// disagree if the host's firewall tooling changes between the two calls.
func installSSHAccessRule(ctx context.Context, config ActionConfig, port uint32, mechanism sshAccessMechanism) (rescuev1.ExpiryMechanism, error) {
	switch mechanism {
	case mechanismNftablesSet:
		if err := installNftablesSSHAccess(ctx, config, port); err != nil {
			return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_UNSPECIFIED, err
		}
		// The element carries its own timeout, so the kernel closes the window
		// even if every userspace component disappears.
		return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_NFTABLES_TIMEOUT, nil
	case mechanismUFW:
		if _, stderr, err := platformCommand(ctx, "ufw", "allow", fmt.Sprintf("%d/tcp", port)); err != nil {
			return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_UNSPECIFIED, fmt.Errorf("ufw allow %d/tcp: %s: %w", port, strings.TrimSpace(string(stderr)), err)
		}
	case mechanismFirewalld:
		// Deliberately not --permanent: a runtime-only rule is dropped by a
		// reload, which fails safe.
		if _, stderr, err := platformCommand(ctx, "firewall-cmd", fmt.Sprintf("--add-port=%d/tcp", port)); err != nil {
			return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_UNSPECIFIED, fmt.Errorf("firewall-cmd --add-port=%d/tcp: %s: %w", port, strings.TrimSpace(string(stderr)), err)
		}
	case mechanismIptables:
		// Inserted at the head of INPUT so it precedes the drop rules that
		// closed the port.
		if _, stderr, err := platformCommand(ctx, "iptables", "-I", "INPUT", "1",
			"-p", "tcp", "--dport", strconv.FormatUint(uint64(port), 10),
			"-m", "comment", "--comment", temporarySSHMark,
			"-j", "ACCEPT"); err != nil {
			return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_UNSPECIFIED, fmt.Errorf("insert iptables rule: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
	}

	// Everything other than the nftables set needs an external timer. Failing
	// to arm one is not fatal: reconciliation still closes the window, so the
	// mechanism is reported honestly instead of pretending it is kernel-held.
	if err := armSSHAccessTimer(ctx, config); err != nil {
		return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_HELPER_RECONCILE, nil
	}
	return rescuev1.ExpiryMechanism_EXPIRY_MECHANISM_SYSTEMD_TIMER, nil
}

func removeSSHAccessRule(ctx context.Context, state sshAccessState) error {
	port := strconv.FormatUint(uint64(state.Port), 10)
	switch state.Mechanism {
	case mechanismNftablesSet:
		return deleteNftablesSSHAccess(ctx)
	case mechanismUFW:
		if _, stderr, err := platformCommand(ctx, "ufw", "delete", "allow", fmt.Sprintf("%s/tcp", port)); err != nil {
			return fmt.Errorf("ufw delete allow %s/tcp: %s: %w", port, strings.TrimSpace(string(stderr)), err)
		}
	case mechanismFirewalld:
		if _, stderr, err := platformCommand(ctx, "firewall-cmd", fmt.Sprintf("--remove-port=%s/tcp", port)); err != nil {
			return fmt.Errorf("firewall-cmd --remove-port=%s/tcp: %s: %w", port, strings.TrimSpace(string(stderr)), err)
		}
	case mechanismIptables:
		// Deleting by full specification removes only our own rule, never an
		// operator's pre-existing allow for the same port.
		if _, stderr, err := platformCommand(ctx, "iptables", "-D", "INPUT",
			"-p", "tcp", "--dport", port,
			"-m", "comment", "--comment", temporarySSHMark,
			"-j", "ACCEPT"); err != nil {
			return fmt.Errorf("delete iptables rule: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
	default:
		return fmt.Errorf("unknown temporary SSH access mechanism %q", state.Mechanism)
	}
	return nil
}

func installNftablesSSHAccess(ctx context.Context, config ActionConfig, port uint32) error {
	// The set is declared with a timeout flag and the rule matches the set, so
	// the port stops being accepted when the element expires without anything
	// having to delete a rule.
	script := fmt.Sprintf(`table inet %[1]s {
	set %[2]s {
		type inet_service
		flags timeout
	}
	chain input {
		type filter hook input priority -150; policy accept;
		tcp dport @%[2]s accept
	}
}
`, temporarySSHTable, temporarySSHSet)

	directory := filepath.Dir(sshAccessStatePath(config))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".komari-temp-ssh-*.conf")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if _, err := temporary.WriteString(script); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, stderr, err := platformCommand(ctx, "nft", "-f", path); err != nil {
		return fmt.Errorf("apply temporary SSH nftables rule: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	element := fmt.Sprintf("{ %d timeout %s }", port, durationForNft(temporarySSHDuration))
	if _, stderr, err := platformCommand(ctx, "nft", "add", "element", "inet", temporarySSHTable, temporarySSHSet, element); err != nil {
		return fmt.Errorf("add temporary SSH port element: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

func deleteNftablesSSHAccess(ctx context.Context) error {
	if _, err := exec.LookPath("nft"); err != nil {
		return nil
	}
	// Listing first avoids depending on localized nft error text when the
	// table is already gone.
	if _, _, err := platformCommand(ctx, "nft", "list", "table", "inet", temporarySSHTable); err != nil {
		return nil
	}
	if _, stderr, err := platformCommand(ctx, "nft", "delete", "table", "inet", temporarySSHTable); err != nil {
		return fmt.Errorf("remove temporary SSH nftables table: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// armSSHAccessTimer schedules revocation through systemd, which is far more
// widely present than atd and survives the helper exiting.
func armSSHAccessTimer(ctx context.Context, config ActionConfig) error {
	binary, err := exec.LookPath("systemd-run")
	if err != nil {
		return err
	}
	if err := cancelSSHAccessTimer(ctx); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	arguments := []string{
		"--unit=" + temporarySSHUnit,
		"--on-active=" + strconv.Itoa(int(temporarySSHDuration.Seconds())),
		"--timer-property=AccuracySec=1s",
		"--description=Revoke Komari temporary SSH access",
		self, "--revoke-temporary-ssh",
	}
	// The timer runs as a bare process with none of the helper's configuration,
	// so a non-default state path has to be passed explicitly or the revocation
	// would look for a file that is not there and silently do nothing.
	if path := strings.TrimSpace(config.IsolationStatePath); path != "" {
		arguments = append(arguments, "--isolation-state-file", path)
	}
	_, stderr, err := platformCommand(ctx, binary, arguments...)
	if err != nil {
		return fmt.Errorf("arm temporary SSH revocation timer: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

func cancelSSHAccessTimer(ctx context.Context) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	for _, unit := range []string{temporarySSHUnit + ".timer", temporarySSHUnit + ".service"} {
		if _, _, err := platformCommand(ctx, "systemctl", "stop", unit); err != nil {
			// A unit that was never created reports failure; that is the
			// expected case and is not an error worth surfacing.
			continue
		}
	}
	return nil
}

// durationForNft renders a duration in the unit syntax nft accepts.
func durationForNft(value time.Duration) string {
	if value%time.Minute == 0 {
		return strconv.Itoa(int(value.Minutes())) + "m"
	}
	return strconv.Itoa(int(value.Seconds())) + "s"
}

// inspectSSHDaemon reports whether a login could actually succeed.
func inspectSSHDaemon(ctx context.Context, port uint32) *rescuev1.SSHDaemonState {
	state := &rescuev1.SSHDaemonState{}
	listening, err := listeningTCPPorts()
	if err == nil {
		state.ObservedListenPorts = listening
		for _, candidate := range listening {
			if candidate == port {
				state.Reachable = true
				break
			}
		}
	} else {
		state.Limitation = "could not read listening sockets: " + err.Error()
	}
	state.Running = sshDaemonRunning(ctx)
	state.ListenAddresses = sshdListenAddresses()

	switch {
	case !state.Running:
		state.Limitation = "sshd does not appear to be running, so opening the firewall cannot allow a login"
	case !state.Reachable && len(state.ObservedListenPorts) > 0:
		state.Limitation = fmt.Sprintf("nothing is listening on port %d; sshd may be bound to another port or to a private address only", port)
	}
	return state
}

func sshDaemonRunning(ctx context.Context) bool {
	if _, err := exec.LookPath("systemctl"); err == nil {
		for _, unit := range []string{"sshd", "ssh"} {
			if _, _, err := platformCommand(ctx, "systemctl", "is-active", "--quiet", unit); err == nil {
				return true
			}
		}
	}
	// Fall back to scanning process names, for hosts without systemd.
	entries, err := os.ReadDir(procRootDirectory())
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procRootDirectory(), entry.Name(), "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "sshd" {
			return true
		}
	}
	return false
}

// sshdListenAddresses reports the configured bindings, including drop-ins,
// because a Tailscale-only host commonly restricts sshd to a private address
// and that is invisible from the firewall alone.
func sshdListenAddresses() []string {
	var addresses []string
	files := []string{"/etc/ssh/sshd_config"}
	if entries, err := filepath.Glob("/etc/ssh/sshd_config.d/*.conf"); err == nil {
		files = append(files, entries...)
	}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "ListenAddress") {
				addresses = append(addresses, fields[1])
			}
		}
	}
	return uniqueStrings(addresses)
}

// listeningTCPPorts reads listening sockets straight from the kernel rather
// than shelling out to ss, keeping the diagnostic cost at a couple of file
// reads.
func listeningTCPPorts() ([]uint32, error) {
	const tcpStateListen = "0A"
	var ports []uint32
	var lastErr error
	found := false
	for _, name := range []string{"tcp", "tcp6"} {
		content, err := os.ReadFile(filepath.Join(procRootDirectory(), "net", name))
		if err != nil {
			lastErr = err
			continue
		}
		found = true
		for index, line := range strings.Split(string(content), "\n") {
			if index == 0 {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != tcpStateListen {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			parsed, err := strconv.ParseUint(portHex, 16, 32)
			if err != nil {
				continue
			}
			ports = append(ports, uint32(parsed))
		}
	}
	if !found {
		return nil, lastErr
	}
	return uniqueUint32(ports), nil
}

func procRootDirectory() string {
	if value := strings.TrimSpace(os.Getenv("HOST_PROC")); value != "" {
		return value
	}
	return "/proc"
}

func uniqueUint32(values []uint32) []uint32 {
	seen := make(map[uint32]bool, len(values))
	result := make([]uint32, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func sshAccessStatePath(config ActionConfig) string {
	return filepath.Join(filepath.Dir(isolationPath(config)), "rescue-temp-ssh.json")
}

func saveSSHAccessState(config ActionConfig, state sshAccessState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := sshAccessStatePath(config)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadSSHAccessState(config ActionConfig) (sshAccessState, error) {
	var state sshAccessState
	data, err := os.ReadFile(sshAccessStatePath(config))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func removeSSHAccessState(config ActionConfig) error {
	if err := os.Remove(sshAccessStatePath(config)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
