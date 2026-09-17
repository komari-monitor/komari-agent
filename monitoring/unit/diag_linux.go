//go:build linux

package monitoring

// Performance diagnostics collector.
//
// Every value here comes from a kernel virtual file. Nothing shells out, and
// nothing walks /proc/<pid>. That constraint is not stylistic: a provider that
// throttles a guest for sustained CPU makes a diagnostic which itself burns CPU
// useless, because collecting it changes what it measures. The two samples the
// rate fields need are separated by a sleep, so the collector consumes no CPU
// while waiting.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	diagv1 "github.com/r11234567/komari-proto/gen/go/komari/diag/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultSampleInterval is the gap between the two counter samples. It is long
// enough that a one-jiffy rounding error is not visible at typical 100 Hz
// kernels, and short enough that an operator is not left waiting.
const defaultSampleInterval = 500 * time.Millisecond

// cpuTimes holds one /proc/stat CPU line in jiffies.
type cpuTimes struct {
	id        string
	user      uint64
	nice      uint64
	system    uint64
	idle      uint64
	iowait    uint64
	irq       uint64
	softirq   uint64
	steal     uint64
	guest     uint64
	guestNice uint64
}

// total excludes guest and guestNice: the kernel already counts guest time
// inside user, and guest_nice inside nice, so adding them again would inflate
// the denominator and understate every percentage.
func (t cpuTimes) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

// procStatSample is one sample of the counters carried by /proc/stat.
type procStatSample struct {
	aggregate     cpuTimes
	perCPU        []cpuTimes
	softirqs      []uint64
	contextSwitch uint64
	interrupts    uint64
	forks         uint64
	procsRunning  uint64
	procsBlocked  uint64
}

// CollectDiagnostics produces one performance snapshot.
//
// Collectors degrade independently: a kernel without PSI, or without swap,
// yields a warning rather than failing the report, because a partial snapshot
// is what an operator needs when something is already wrong.
func CollectDiagnostics(includeCPU, includeMemory bool) (*diagv1.DiagnosticsReport, error) {
	started := time.Now()
	report := &diagv1.DiagnosticsReport{
		CollectedAt:    timestamppb.New(started),
		SampleInterval: durationpb.New(defaultSampleInterval),
	}

	var firstStat, secondStat *procStatSample
	var firstVM, secondVM map[string]uint64
	var statErr, vmErr error

	if includeCPU {
		firstStat, statErr = readProcStat()
		if statErr != nil {
			report.Warnings = append(report.Warnings, "read /proc/stat: "+statErr.Error())
		}
	}
	if includeMemory {
		firstVM, vmErr = readProcVMStat()
		if vmErr != nil {
			report.Warnings = append(report.Warnings, "read /proc/vmstat: "+vmErr.Error())
		}
	}

	// Both rate sources need a second sample. Sleeping once serves both, so a
	// combined request costs the same wall time as either one alone.
	if (includeCPU && statErr == nil) || (includeMemory && vmErr == nil) {
		time.Sleep(defaultSampleInterval)
	}

	if includeCPU && statErr == nil {
		if secondStat, statErr = readProcStat(); statErr != nil {
			report.Warnings = append(report.Warnings, "re-read /proc/stat: "+statErr.Error())
		}
	}
	if includeMemory && vmErr == nil {
		if secondVM, vmErr = readProcVMStat(); vmErr != nil {
			report.Warnings = append(report.Warnings, "re-read /proc/vmstat: "+vmErr.Error())
		}
	}

	elapsed := time.Since(started)
	// Rates are divided by the interval actually observed, not the nominal one:
	// a throttled or descheduled host can overshoot the sleep considerably, and
	// dividing by the nominal value would overstate every rate.
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		seconds = defaultSampleInterval.Seconds()
	}

	if includeCPU && statErr == nil && secondStat != nil {
		report.Cpu = buildCPUDiagnostics(*firstStat, *secondStat, seconds, report)
	}
	if includeMemory {
		report.Memory = buildMemoryDiagnostics(firstVM, secondVM, seconds, report)
	}

	report.CollectionDuration = durationpb.New(time.Since(started))
	if report.Cpu == nil && report.Memory == nil {
		return report, fmt.Errorf("no diagnostics could be collected")
	}
	return report, nil
}

func buildCPUDiagnostics(first, second procStatSample, seconds float64, report *diagv1.DiagnosticsReport) *diagv1.CpuDiagnostics {
	aggregate := diffCPUTimes(first.aggregate, second.aggregate)
	logical := len(second.perCPU)
	if logical == 0 {
		logical = runtime.NumCPU()
	}

	// The panel a hosting provider exposes is derived from hypervisor-side
	// accounting and reports cores consumed, not a normalized percentage.
	// Converting here is what makes the two numbers comparable at all.
	busy := busyPercent(first.aggregate, second.aggregate)
	result := &diagv1.CpuDiagnostics{
		LogicalCpuCount:          uint32(logical),
		BusyPercent:              busy,
		PanelComparableCores:     busy * float64(logical) / 100,
		States:                   aggregate,
		Softirq:                  diffSoftirqs(first.softirqs, second.softirqs, seconds),
		LoadAverage:              readLoadAverage(report),
		ContextSwitchesPerSecond: perSecond(first.contextSwitch, second.contextSwitch, seconds),
		InterruptsPerSecond:      perSecond(first.interrupts, second.interrupts, seconds),
		ForksPerSecond:           perSecond(first.forks, second.forks, seconds),
		ProcsRunning:             second.procsRunning,
		ProcsBlocked:             second.procsBlocked,
	}

	// Per-CPU detail is redundant on a single-CPU guest and is the common case
	// for the small instances this is aimed at, so it is omitted there.
	if logical > 1 && len(first.perCPU) == len(second.perCPU) {
		for i := range second.perCPU {
			result.PerCpu = append(result.PerCpu, diffCPUTimes(first.perCPU[i], second.perCPU[i]))
		}
	}

	if pressure, err := readPressure("cpu"); err == nil {
		result.Pressure = pressure
	} else {
		report.Warnings = append(report.Warnings, "read cpu pressure: "+err.Error())
	}
	return result
}

// diffCPUTimes converts two jiffy counters into per-state percentages of the
// interval.
func diffCPUTimes(first, second cpuTimes) *diagv1.CpuStateBreakdown {
	total := saturatingDiff(second.total(), first.total())
	breakdown := &diagv1.CpuStateBreakdown{CpuId: second.id}
	if total == 0 {
		return breakdown
	}
	share := func(a, b uint64) float64 {
		return float64(saturatingDiff(a, b)) * 100 / float64(total)
	}
	breakdown.UserPercent = share(second.user, first.user)
	breakdown.NicePercent = share(second.nice, first.nice)
	breakdown.SystemPercent = share(second.system, first.system)
	breakdown.IdlePercent = share(second.idle, first.idle)
	breakdown.IowaitPercent = share(second.iowait, first.iowait)
	breakdown.IrqPercent = share(second.irq, first.irq)
	breakdown.SoftirqPercent = share(second.softirq, first.softirq)
	breakdown.StealPercent = share(second.steal, first.steal)
	breakdown.GuestPercent = share(second.guest, first.guest)
	breakdown.GuestNicePercent = share(second.guestNice, first.guestNice)
	// Kernel time is the sum of the three states a packet-forwarding workload
	// drives, reported together because that is the question being asked.
	breakdown.KernelPercent = breakdown.SystemPercent + breakdown.IrqPercent + breakdown.SoftirqPercent
	return breakdown
}

// busyPercent mirrors the formula the agent's ordinary CPU reporting uses, so
// the diagnostic and the dashboard cannot disagree about the headline number.
// Softirq, irq and steal all count as busy.
func busyPercent(first, second cpuTimes) float64 {
	total := saturatingDiff(second.total(), first.total())
	if total == 0 {
		return 0
	}
	idle := saturatingDiff(second.idle, first.idle) + saturatingDiff(second.iowait, first.iowait)
	busy := saturatingDiff(total, idle)
	value := float64(busy) * 100 / float64(total)
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// diffSoftirqs converts the /proc/stat softirq line into rates.
//
// The kernel emits one counter per softirq vector in a fixed order —
// HI, TIMER, NET_TX, NET_RX, BLOCK, IRQ_POLL, TASKLET, SCHED, HRTIMER, RCU —
// which is why the fields below are read positionally.
func diffSoftirqs(first, second []uint64, seconds float64) *diagv1.SoftirqActivity {
	activity := &diagv1.SoftirqActivity{}
	if len(first) == 0 || len(second) == 0 {
		return activity
	}
	// The /proc/stat softirq line leads with a total, followed by one counter
	// per vector.
	activity.TotalPerSecond = perSecond(first[0], second[0], seconds)
	at := func(index int) uint64 {
		if index+1 >= len(first) || index+1 >= len(second) {
			return 0
		}
		return perSecond(first[index+1], second[index+1], seconds)
	}
	activity.HiPerSecond = at(0)
	activity.TimerPerSecond = at(1)
	activity.NetTxPerSecond = at(2)
	activity.NetRxPerSecond = at(3)
	activity.BlockPerSecond = at(4)
	activity.IrqPollPerSecond = at(5)
	activity.TaskletPerSecond = at(6)
	activity.SchedPerSecond = at(7)
	activity.HrtimerPerSecond = at(8)
	activity.RcuPerSecond = at(9)
	return activity
}

func buildMemoryDiagnostics(firstVM, secondVM map[string]uint64, seconds float64, report *diagv1.DiagnosticsReport) *diagv1.MemoryDiagnostics {
	info, err := ReadProcMeminfo()
	if err != nil {
		report.Warnings = append(report.Warnings, "read /proc/meminfo: "+err.Error())
		return nil
	}

	// Partitioning follows the same htop-derived accounting the agent already
	// uses for its ordinary memory reporting, so the diagnostic does not
	// introduce a second definition of "used".
	usedDiff := info.MemFree + info.Cached + info.SReclaimable + info.Buffers
	used := info.MemTotal - info.MemFree
	if info.MemTotal >= usedDiff {
		used = info.MemTotal - usedDiff
	}
	used += info.Shmem

	swapUsed := saturatingDiff(info.SwapTotal, info.SwapFree+info.SwapCached)

	result := &diagv1.MemoryDiagnostics{
		TotalBytes:            info.MemTotal,
		FreeBytes:             info.MemFree,
		AvailableBytes:        info.MemAvailable,
		BuffersBytes:          info.Buffers,
		CachedBytes:           info.Cached,
		SreclaimableBytes:     info.SReclaimable,
		ShmemBytes:            info.Shmem,
		UsedBytes:             used,
		SwapTotalBytes:        info.SwapTotal,
		SwapFreeBytes:         info.SwapFree,
		SwapCachedBytes:       info.SwapCached,
		SwapUsedBytes:         swapUsed,
		ZswapCompressedBytes:  info.Zswap,
		ZswappedOriginalBytes: info.Zswapped,
	}

	if firstVM != nil && secondVM != nil {
		result.Paging = diffVMStat(firstVM, secondVM, seconds)
	}
	if pressure, err := readPressure("memory"); err == nil {
		result.Pressure = pressure
	} else {
		report.Warnings = append(report.Warnings, "read memory pressure: "+err.Error())
	}
	result.Thrashing = classifyThrashing(result.Paging, result.Pressure)
	return result
}

func diffVMStat(first, second map[string]uint64, seconds float64) *diagv1.PagingActivity {
	rate := func(key string) uint64 {
		return perSecond(first[key], second[key], seconds)
	}
	return &diagv1.PagingActivity{
		// pswpin/pswpout are pages moved to and from a swap device. Unlike a
		// swap occupancy figure, which can sit high and harmless, these are
		// non-zero only while swapping is actually happening.
		SwapInPagesPerSecond:     rate("pswpin"),
		SwapOutPagesPerSecond:    rate("pswpout"),
		PageInPerSecond:          rate("pgpgin"),
		PageOutPerSecond:         rate("pgpgout"),
		MajorFaultsPerSecond:     rate("pgmajfault"),
		MinorFaultsPerSecond:     rate("pgfault"),
		KswapdScannedPerSecond:   rate("pgscan_kswapd"),
		DirectScannedPerSecond:   rate("pgscan_direct"),
		KswapdReclaimedPerSecond: rate("pgsteal_kswapd"),
		DirectReclaimedPerSecond: rate("pgsteal_direct"),
		OomKillsTotal:            second["oom_kill"],
	}
}

// classifyThrashing summarizes paging pressure. The thresholds are advisory and
// the underlying rates travel alongside, so a consumer can disagree.
//
// PSI is preferred when present because it measures time actually lost to
// memory stalls, whereas a page rate only implies a cost. Major faults are the
// fallback signal, and swap traffic alone is deliberately not enough: a host
// steadily paging a little is not thrashing.
func classifyThrashing(paging *diagv1.PagingActivity, pressure *diagv1.PressureStall) diagv1.ThrashingLevel {
	if paging == nil && pressure == nil {
		return diagv1.ThrashingLevel_THRASHING_LEVEL_UNSPECIFIED
	}
	if pressure != nil {
		full := pressure.GetFullAvg10()
		switch {
		case full >= 20:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_SEVERE
		case full >= 5:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_MODERATE
		case full >= 1:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_LIGHT
		}
	}
	if paging != nil {
		swap := paging.SwapInPagesPerSecond + paging.SwapOutPagesPerSecond
		switch {
		case paging.MajorFaultsPerSecond >= 1000 || swap >= 2000:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_SEVERE
		case paging.MajorFaultsPerSecond >= 100 || swap >= 200:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_MODERATE
		case paging.MajorFaultsPerSecond >= 10 || swap >= 20:
			return diagv1.ThrashingLevel_THRASHING_LEVEL_LIGHT
		}
	}
	return diagv1.ThrashingLevel_THRASHING_LEVEL_NONE
}

func readProcStat() (*procStatSample, error) {
	file, err := os.Open(filepath.Join(procRoot(), "stat"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sample := &procStatSample{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			sample.aggregate = parseCPUTimes("", fields[1:])
		case strings.HasPrefix(fields[0], "cpu"):
			sample.perCPU = append(sample.perCPU, parseCPUTimes(fields[0], fields[1:]))
		case fields[0] == "softirq":
			for _, value := range fields[1:] {
				parsed, parseErr := strconv.ParseUint(value, 10, 64)
				if parseErr != nil {
					break
				}
				sample.softirqs = append(sample.softirqs, parsed)
			}
		case fields[0] == "ctxt":
			sample.contextSwitch, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "intr":
			sample.interrupts, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "processes":
			sample.forks, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "procs_running":
			sample.procsRunning, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "procs_blocked":
			sample.procsBlocked, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if sample.aggregate.total() == 0 {
		return nil, fmt.Errorf("no aggregate CPU line in /proc/stat")
	}
	return sample, nil
}

// parseCPUTimes tolerates a short line: the guest and guest_nice counters were
// added in later kernels, and older or unusual kernels may report fewer.
func parseCPUTimes(id string, fields []string) cpuTimes {
	values := make([]uint64, 10)
	for i := 0; i < len(values) && i < len(fields); i++ {
		values[i], _ = strconv.ParseUint(fields[i], 10, 64)
	}
	return cpuTimes{
		id: id, user: values[0], nice: values[1], system: values[2], idle: values[3],
		iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
		guest: values[8], guestNice: values[9],
	}
}

func readProcVMStat() (map[string]uint64, error) {
	file, err := os.Open(filepath.Join(procRoot(), "vmstat"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]uint64, 64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		if parsed, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
			values[fields[0]] = parsed
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// readPressure reads one PSI file. PSI requires a kernel built with
// CONFIG_PSI and booted without psi=0, so absence is normal and reported as a
// warning rather than an error.
func readPressure(resource string) (*diagv1.PressureStall, error) {
	content, err := os.ReadFile(filepath.Join(procRoot(), "pressure", resource))
	if err != nil {
		return nil, err
	}
	stall := &diagv1.PressureStall{}
	found := false
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		avg10, avg60, avg300 := parsePressureAverages(fields[1:])
		switch fields[0] {
		case "some":
			stall.SomeAvg10, stall.SomeAvg60, stall.SomeAvg300 = avg10, avg60, avg300
			found = true
		case "full":
			// CPU pressure reports only "some"; memory and IO report both.
			stall.FullAvg10, stall.FullAvg60, stall.FullAvg300 = &avg10, &avg60, &avg300
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("no pressure averages in %s", resource)
	}
	return stall, nil
}

func parsePressureAverages(fields []string) (avg10, avg60, avg300 float64) {
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		switch key {
		case "avg10":
			avg10 = parsed
		case "avg60":
			avg60 = parsed
		case "avg300":
			avg300 = parsed
		}
	}
	return avg10, avg60, avg300
}

func readLoadAverage(report *diagv1.DiagnosticsReport) []float64 {
	content, err := os.ReadFile(filepath.Join(procRoot(), "loadavg"))
	if err != nil {
		report.Warnings = append(report.Warnings, "read /proc/loadavg: "+err.Error())
		return nil
	}
	fields := strings.Fields(string(content))
	if len(fields) < 3 {
		return nil
	}
	averages := make([]float64, 0, 3)
	for _, field := range fields[:3] {
		parsed, parseErr := strconv.ParseFloat(field, 64)
		if parseErr != nil {
			return nil
		}
		averages = append(averages, parsed)
	}
	return averages
}

// perSecond converts a monotonic counter pair into a rate. A counter that went
// backwards means it was reset, which yields zero rather than a wrapped value.
func perSecond(first, second uint64, seconds float64) uint64 {
	if seconds <= 0 {
		return 0
	}
	return uint64(float64(saturatingDiff(second, first))/seconds + 0.5)
}

func saturatingDiff(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}
