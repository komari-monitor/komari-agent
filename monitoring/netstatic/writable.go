package netstatic

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

// SaveFilePath is relative to the working directory, so a service that runs as an unprivileged user from a
// read-only directory (for example "/" under systemd's ProtectSystem=strict) cannot persist its counters.
// That used to fail silently: the monthly totals simply restarted from zero after every restart.

var (
	saveWarnMu sync.Mutex
	saveFailed bool
)

// checkWritable reports whether the statistics file can be created next to SaveFilePath.
// It writes and removes a probe file and never touches the real data file.
func checkWritable() error {
	dir := filepath.Dir(SaveFilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := SaveFilePath + ".probe"
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(probe)
}

// warnNotWritable logs once at startup when the statistics file cannot be written.
func warnNotWritable() {
	if err := checkWritable(); err != nil {
		log.Printf("WARNING: traffic statistics cannot be saved to %q: %v. Monthly traffic will be lost when the agent restarts; "+
			"run the agent from a writable working directory.", SaveFilePath, err)
	}
}

// reportSave logs a failed periodic save once (not every interval) and logs again after a recovery.
// It returns true when it wrote a log line.
func reportSave(err error) bool {
	saveWarnMu.Lock()
	defer saveWarnMu.Unlock()
	if err == nil {
		saveFailed = false
		return false
	}
	if saveFailed {
		return false
	}
	saveFailed = true
	log.Printf("WARNING: failed to save traffic statistics: %v", err)
	return true
}
