package monitoring

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/shirou/gopsutil/v4/mem"
)

type RamInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Mode  string
}

type ProcMemInfo struct {
	MemTotal     uint64
	MemFree      uint64
	MemAvailable uint64
	Buffers      uint64
	Cached       uint64
	SwapTotal    uint64
	SwapFree     uint64
	SwapCached   uint64
	Shmem        uint64
	SReclaimable uint64
	Zswap        uint64
	Zswapped     uint64
}

// readProcMeminfo reads /proc/meminfo and returns a filled ProcMemInfo struct
func ReadProcMeminfo() (*ProcMemInfo, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info := &ProcMemInfo{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.TrimSuffix(parts[0], ":")
		valStr := parts[1]
		val, err := strconv.ParseUint(valStr, 10, 64)
		if err != nil {
			continue
		}
		val *= 1024 // Convert kB to bytes

		switch key {
		case "MemTotal":
			info.MemTotal = val
		case "MemFree":
			info.MemFree = val
		case "MemAvailable":
			info.MemAvailable = val
		case "Buffers":
			info.Buffers = val
		case "Cached":
			info.Cached = val
		case "SwapTotal":
			info.SwapTotal = val
		case "SwapFree":
			info.SwapFree = val
		case "SwapCached":
			info.SwapCached = val
		case "Shmem":
			info.Shmem = val
		case "SReclaimable":
			info.SReclaimable = val
		case "Zswap":
			info.Zswap = val
		case "Zswapped":
			info.Zswapped = val
		}
	}
	return info, scanner.Err()
}

func GetMemHtopLike() RamInfo {
	raminfo := RamInfo{Mode: "htoplike"}
	if runtime.GOOS == "linux" {
		info, err := ReadProcMeminfo()
		if err == nil && info.MemTotal > 0 {
			raminfo.Total = info.MemTotal
			// htop logic:
			// usedDiff = free + cached + sreclaimable + buffers
			usedDiff := info.MemFree + info.Cached + info.SReclaimable + info.Buffers

			if info.MemTotal >= usedDiff {
				raminfo.Used = info.MemTotal - usedDiff
			} else {
				raminfo.Used = info.MemTotal - info.MemFree
			}

			// Adjust for Zswap
			if info.Zswap > 0 || info.Zswapped > 0 {
				if raminfo.Used > info.Zswap {
					raminfo.Used -= info.Zswap
				} else {
					raminfo.Used = 0
				}
			}
			return raminfo
		}
	}
	return raminfo
}

func GetMemGopsutil() RamInfo {
	raminfo := RamInfo{Mode: "gopsutil"}
	v, err := mem.VirtualMemory()
	if err == nil {
		raminfo.Total = v.Total
		raminfo.Used = v.Total - v.Available
	}
	return raminfo
}

// 这我还能干嘛，大伙天天说和free显示不一样，我也没办法
func CallFree() RamInfo {
	raminfo := RamInfo{Mode: "callFree"}

	// Only works on Linux/Unix systems
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return raminfo
	}

	// Execute 'free -b' command to get memory in bytes
	cmd := exec.Command("free", "-b")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return raminfo
	}

	// Parse the output
	scanner := bufio.NewScanner(&out)
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		// Skip the header line
		if lineNum == 1 {
			continue
		}

		// Parse the "Mem:" line
		if strings.HasPrefix(line, "Mem:") {
			fields := strings.Fields(line)
			// Format: Mem: total used free shared buff/cache available
			if len(fields) >= 3 {
				total, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					raminfo.Total = total
				}

				used, err := strconv.ParseUint(fields[2], 10, 64)
				if err == nil {
					raminfo.Used = used
				}
			}
			break
		}
	}

	return raminfo
}

func Ram() RamInfo {
	// Use global config
	if pkg_flags.GlobalConfig.MemoryIncludeCache {
		v, err := mem.VirtualMemory()
		if err != nil {
			return RamInfo{}
		}
		return RamInfo{
			Total: v.Total,
			Used:  v.Total - v.Free,
			Mode:  "includeCache",
		}
	}

	if pkg_flags.GlobalConfig.MemoryReportRawUsed {
		return GetMemHtopLike()
	}

	if runtime.GOOS == "linux" {
		h := GetMemHtopLike()
		if h.Total > 0 {
			return h
		}
	}

	// Default fallback
	return GetMemGopsutil()
}

func Swap() RamInfo {
	swapinfo := RamInfo{}

	if runtime.GOOS == "linux" {
		info, err := ReadProcMeminfo()
		if err == nil {
			swapinfo.Total = info.SwapTotal
			// used = total - free - cached
			// Check for underflow
			usedDeductions := info.SwapFree + info.SwapCached
			if info.SwapTotal >= usedDeductions {
				swapinfo.Used = info.SwapTotal - usedDeductions
			} else {
				swapinfo.Used = info.SwapTotal - info.SwapFree
			}
			return swapinfo
		}
	}

	s, err := mem.SwapMemory()
	if err != nil {
		return swapinfo
	}
	swapinfo.Total = s.Total
	swapinfo.Used = s.Used
	return swapinfo
}
