package monitoring

import (
	"encoding/json"
	"fmt"
	"log"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
)

var flags = pkg_flags.GlobalConfig

func GenerateReport() []byte {
	message := ""
	data := map[string]interface{}{}

	cpu := monitoring.Cpu()
	cpuUsage := cpu.CPUUsage
	if cpuUsage <= 0.001 {
		cpuUsage = 0.001
	}
	data["cpu"] = map[string]interface{}{
		"usage": cpuUsage,
	}

	ram := monitoring.Ram()
	data["ram"] = map[string]interface{}{
		"total": ram.Total,
		"used":  ram.Used,
	}

	swap := monitoring.Swap()
	data["swap"] = map[string]interface{}{
		"total": swap.Total,
		"used":  swap.Used,
	}
	load := monitoring.Load()
	data["load"] = map[string]interface{}{
		"load1":  load.Load1,
		"load5":  load.Load5,
		"load15": load.Load15,
	}

	disk := monitoring.Disk()
	data["disk"] = map[string]interface{}{
		"total": disk.Total,
		"used":  disk.Used,
	}

	totalUp, totalDown, networkUp, networkDown, err := monitoring.NetworkSpeed()
	if err != nil {
		message += fmt.Sprintf("failed to get network speed: %v\n", err)
	}
	data["network"] = map[string]interface{}{
		"up":        networkUp,
		"down":      networkDown,
		"totalUp":   totalUp,
		"totalDown": totalDown,
	}

	tcpCount, udpCount, err := monitoring.ConnectionsCount()
	if err != nil {
		message += fmt.Sprintf("failed to get connections: %v\n", err)
	}
	data["connections"] = map[string]interface{}{
		"tcp": tcpCount,
		"udp": udpCount,
	}

	uptime, err := monitoring.Uptime()
	if err != nil {
		message += fmt.Sprintf("failed to get uptime: %v\n", err)
	}
	data["uptime"] = uptime

	processcount := monitoring.ProcessCount()
	data["process"] = processcount

	// GPU监控 - 根据标志决定详细程度
	if flags.EnableGPU {
		// 详细GPU监控模式
		gpuInfo, err := monitoring.GetDetailedGPUInfo()
		if err != nil {
			message += fmt.Sprintf("failed to get detailed GPU info: %v\n", err)
			// 降级到基础GPU信息
			gpuNames, nameErr := monitoring.GetDetailedGPUHost()
			if nameErr == nil && len(gpuNames) > 0 {
				data["gpu"] = map[string]interface{}{
					"models": gpuNames,
				}
			}
		} else {
			// 成功获取详细信息
			gpuData := make([]map[string]interface{}, len(gpuInfo))
			totalGPUUsage := 0.0

			for i, info := range gpuInfo {
				gpuData[i] = map[string]interface{}{
					"name":         info.Name,
					"memory_total": info.MemoryTotal,
					"memory_used":  info.MemoryUsed,
					"utilization":  info.Utilization,
					"temperature":  info.Temperature,
				}
				totalGPUUsage += info.Utilization
			}

			avgGPUUsage := totalGPUUsage / float64(len(gpuInfo))

			data["gpu"] = map[string]interface{}{
				"count":         len(gpuInfo),
				"average_usage": avgGPUUsage,
				"detailed_info": gpuData,
			}
		}
	}
	// 基础模式下，GPU信息已在basicInfo中处理

	data["message"] = message

	s, err := json.Marshal(data)
	if err != nil {
		log.Println("Failed to marshal data:", err)
	}
	return s
}
