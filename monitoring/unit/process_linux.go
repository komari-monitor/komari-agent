//go:build !windows
// +build !windows

package monitoring

import (
	"os"
	"strconv"
)

// ProcessCount returns the number of running processes
func ProcessCount() (count int) {
	return processCountLinux()
}

// processCountLinux counts processes by reading /proc directory
func processCountLinux() (count int) {
	procDir := "/proc"

	if flags.HostProc != "" {
		if info, err := os.Stat(flags.HostProc); err == nil && info.IsDir() {
			procDir = flags.HostProc
		}
	}

	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		if _, err := strconv.ParseInt(entry.Name(), 10, 64); err == nil {
			//if _, err := filepath.ParseInt(entry.Name(), 10, 64); err == nil {
			count++
		}
	}

	return count
}
