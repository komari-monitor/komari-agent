//go:build !linux

package monitoring

import (
	"errors"

	diagv1 "github.com/r11234567/komari-proto/gen/go/komari/diag/v1"
)

// CollectDiagnostics is Linux-only. The whole report is built from /proc
// counters that have no portable equivalent: per-state CPU jiffies, the
// softirq vector table, vmstat paging counters and PSI. Synthesizing a
// partially-populated report on another platform would misrepresent missing
// data as measured zeroes, so this refuses instead.
func CollectDiagnostics(includeCPU, includeMemory bool) (*diagv1.DiagnosticsReport, error) {
	return nil, errors.New("performance diagnostics are supported only on Linux")
}
