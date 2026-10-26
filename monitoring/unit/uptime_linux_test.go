//go:build linux

package monitoring

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProcUptime(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "uptime")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procUptimePath
	procUptimePath = p
	t.Cleanup(func() { procUptimePath = old })
}

func TestProcUptime(t *testing.T) {
	oldHost := flags.HostProc
	defer func() { flags.HostProc = oldHost }()
	flags.HostProc = ""

	writeProcUptime(t, "12345.67 12345.67\n")
	if got, ok := procUptime(); !ok || got != 12345 {
		t.Errorf("procUptime() = %d, %v; want 12345, true", got, ok)
	}

	for _, bad := range []string{"", "abc def\n", "-5 1\n"} {
		writeProcUptime(t, bad)
		if _, ok := procUptime(); ok {
			t.Errorf("procUptime() accepted malformed content %q", bad)
		}
	}

	// 监控宿主机时保持原行为（不读本地 /proc/uptime）
	writeProcUptime(t, "100.00 100.00\n")
	flags.HostProc = "/host/proc"
	if _, ok := procUptime(); ok {
		t.Error("procUptime() must defer to the original behaviour when HOST_PROC is set")
	}

	// 文件不存在
	procUptimePath = filepath.Join(t.TempDir(), "missing")
	flags.HostProc = ""
	if _, ok := procUptime(); ok {
		t.Error("procUptime() must report false when the file cannot be read")
	}
}
