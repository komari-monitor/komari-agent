package monitoring

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/shirou/gopsutil/v4/cpu"
)

var flags = pkg_flags.GlobalConfig

type CpuInfo struct {
	CPUName         string  `json:"cpu_name"`
	CPUArchitecture string  `json:"cpu_architecture"`
	CPUCores        int     `json:"cpu_cores"`
	CPUUsage        float64 `json:"cpu_usage"`
}

func Cpu() CpuInfo {
	cpuinfo := CpuInfo{
		CPUName:         "Unknown",
		CPUArchitecture: runtime.GOARCH,
		CPUCores:        1,
		CPUUsage:        0.0,
	}

	// 优先使用 lscpu 获取 CPU 信息
	name, err := readCPUNameFromLscpu()
	if err == nil && name != "" {
		cpuinfo.CPUName = strings.TrimSpace(name)
	} else {
		// 如果 lscpu 无法获取 CPU 名称，尝试使用 gopsutil
		info, err := cpu.Info()
		if err == nil && len(info) > 0 {
			cpuinfo.CPUName = strings.TrimSpace(info[0].ModelName)
			if cpuinfo.CPUName == "" {
				if info[0].VendorID != "" || info[0].Family != "" {
					cpuinfo.CPUName = strings.TrimSpace(info[0].VendorID + " " + info[0].Family)
				}
			}
		}
	}

	if cpuinfo.CPUName == "Unknown" {
		name, err := readCPUNameFromProc()
		if err == nil && name != "" {
			cpuinfo.CPUName = strings.TrimSpace(name)
		}
	}

	cores, err := cpu.Counts(true)
	if err == nil {
		cpuinfo.CPUCores = cores
	}

	percentages, err := cpu.Percent(1*time.Second, false)
	if err == nil && len(percentages) > 0 {
		cpuinfo.CPUUsage = percentages[0]
	}

	return cpuinfo
}

// readCPUNameFromLscpu 从 lscpu 命令读取 CPU 名称
func readCPUNameFromLscpu() (string, error) {
	cmd := exec.Command("lscpu")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Model name:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}

	return "", scanner.Err()
}

// readCPUNameFromProc 从 /proc/cpuinfo 读取 CPU 名称
func readCPUNameFromProc() (string, error) {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Model\t") || strings.HasPrefix(line, "Hardware\t") || strings.HasPrefix(line, "Processor\t") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}

	return "", scanner.Err()
}
