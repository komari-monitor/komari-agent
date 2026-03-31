package monitoring

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	cpuid "github.com/klauspost/cpuid/v2"
)

func Virtualized() string {
	// Windows: use CPUID to detect hypervisor presence and vendor.
	if runtime.GOOS == "windows" {
		return detectByCPUID()
	}

	// Linux/others: prefer systemd-detect-virt if available; fallback to CPUID.
	if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		virt := strings.TrimSpace(string(out))
		if virt != "" {
			return virt
		}
	}

	// Non-systemd environments (e.g., Alpine containers): try container heuristics.
	if ct := detectContainer(); ct != "" {
		return ct
	}

	// Fallback (any OS): CPUID hypervisor bit and vendor mapping.
	return detectByCPUID()
}

// detectByCPUID uses cpuid to check if running under a hypervisor and maps vendor to a common name.
func detectByCPUID() string {
	if !cpuid.CPU.VM() {
		// Align with systemd-detect-virt for bare metal.
		return "none"
	}
	vendor := strings.ToLower(cpuid.CPU.HypervisorVendorString)

	vendorMap := map[string][]string{
		"kvm":       {"kvm"},
		"microsoft": {"microsoft", "hyper-v", "msvm", "mshyperv"},
		"vmware":    {"vmware"},
		"xen":       {"xen"},
		"bhyve":     {"bhyve"},
		"qemu":      {"qemu"},
		"parallels": {"parallels"},
		"oracle":    {"oracle", "virtualbox", "vbox"},
		"acrn":      {"acrn"},
	}

	for name, keys := range vendorMap {
		for _, key := range keys {
			if vendor == key || strings.Contains(vendor, key) {
				return name
			}
		}
	}
	if vendor != "" {
		return vendor
	}
	return "virtualized"
}

// detectContainer attempts to detect common Linux container environments when systemd isn't available.
// Returns a systemd-detect-virt-like string such as "docker", "podman", "lxc", "container" or empty if not detected.
func detectContainer() string {
	// Definite file markers first.
	if fileExists("/.dockerenv") {
		return "docker"
	}
	if fileExists("/run/.containerenv") { // podman / CRI-O
		if s := parseCgroupForContainer(); s != "" {
			return s
		}
		return "container"
	}

	// cgroup based detection (safer & more specific than broad substring checks)
	if s := parseCgroupForContainer(); s != "" {
		return s
	}
	if fileExists("/dev/.lxc-boot-id") {
		return "lxc"
	}
	if fileExists("/.komari-agent-container") {
		return "container"
	}
	// (Removed mountinfo heuristics which caused host false positives when Docker/Kube tools are installed.)
	return ""
}

func fileExists(p string) bool {
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return true
	}
	return false
}

func parseCgroupForContainer() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	lower := strings.ToLower(string(data))

	// Precompile (once) regex patterns for common container runtimes.
	// Patterns target leaf elements referencing container IDs instead of any occurrence of runtime name to reduce false positives.
	var (
		dockerIDPattern    = regexp.MustCompile(`(?m)/(?:docker|cri-containerd)[/-]([0-9a-f]{12,64})(?:\.scope)?$`)
		dockerScopePattern = regexp.MustCompile(`(?m)/docker-[0-9a-f]{12,64}\.scope$`)
		kubePattern        = regexp.MustCompile(`(?m)/kubepods[/.].*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}).*`) // pod UID
		podmanPattern      = regexp.MustCompile(`(?m)/(?:libpod|podman)[-_]([0-9a-f]{12,64})(?:\.scope)?$`)
		lxcPattern         = regexp.MustCompile(`(?m)/lxc/[^/]+$`)
		crioPattern        = regexp.MustCompile(`(?m)/crio-[0-9a-f]{12,64}\.scope$`)
	)

	// Order: specific runtime before generic container.
	if dockerIDPattern.FindStringIndex(lower) != nil || dockerScopePattern.FindStringIndex(lower) != nil {
		return "docker"
	}
	if podmanPattern.FindStringIndex(lower) != nil {
		return "podman"
	}
	if crioPattern.FindStringIndex(lower) != nil {
		return "container" // CRI-O generic
	}
	if kubePattern.FindStringIndex(lower) != nil {
		return "kubernetes"
	}
	if lxcPattern.FindStringIndex(lower) != nil {
		return "lxc"
	}

	return ""
}
