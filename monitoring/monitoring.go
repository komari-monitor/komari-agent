package monitoring

import (
	"encoding/json"
	"fmt"
	"log"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	unit "github.com/komari-monitor/komari-agent/monitoring/unit"
)

var flags = pkg_flags.GlobalConfig

type report struct {
	CPU         cpuReport         `json:"cpu"`
	Ram         usageReport       `json:"ram"`
	Swap        usageReport       `json:"swap"`
	Load        loadReport        `json:"load"`
	Disk        usageReport       `json:"disk"`
	Network     networkReport     `json:"network"`
	Connections connectionsReport `json:"connections"`
	GPU         interface{}       `json:"gpu,omitempty"`
	Uptime      uint64            `json:"uptime"`
	Process     int               `json:"process"`
	Message     string            `json:"message"`
}

type cpuReport struct {
	Usage float64 `json:"usage"`
}

type usageReport struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type loadReport struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type networkReport struct {
	Up        uint64 `json:"up"`
	Down      uint64 `json:"down"`
	TotalUp   uint64 `json:"totalUp"`
	TotalDown uint64 `json:"totalDown"`
}

type connectionsReport struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}

type gpuModelsReport struct {
	Models []string `json:"models"`
}

type gpuReport struct {
	Count        int               `json:"count"`
	AverageUsage float64           `json:"average_usage"`
	DetailedInfo []gpuDeviceReport `json:"detailed_info"`
}

type gpuDeviceReport struct {
	Name        string  `json:"name"`
	MemoryTotal uint64  `json:"memory_total"`
	MemoryUsed  uint64  `json:"memory_used"`
	Utilization float64 `json:"utilization"`
	Temperature uint64  `json:"temperature"`
}

func GenerateReport() []byte {
	message := ""
	data := report{}

	cpu := unit.Cpu()
	cpuUsage := cpu.CPUUsage
	if cpuUsage <= 0.001 {
		cpuUsage = 0.001
	}
	data.CPU = cpuReport{Usage: cpuUsage}

	ram := unit.Ram()
	data.Ram = usageReport{Total: ram.Total, Used: ram.Used}

	swap := unit.Swap()
	data.Swap = usageReport{Total: swap.Total, Used: swap.Used}
	load := unit.Load()
	data.Load = loadReport{Load1: load.Load1, Load5: load.Load5, Load15: load.Load15}

	disk := unit.Disk()
	data.Disk = usageReport{Total: disk.Total, Used: disk.Used}

	totalUp, totalDown, networkUp, networkDown, err := unit.NetworkSpeed()
	if err != nil {
		message += fmt.Sprintf("failed to get network speed: %v\n", err)
	}
	data.Network = networkReport{Up: networkUp, Down: networkDown, TotalUp: totalUp, TotalDown: totalDown}

	tcpCount, udpCount, err := unit.ConnectionsCount()
	if err != nil {
		message += fmt.Sprintf("failed to get connections: %v\n", err)
	}
	data.Connections = connectionsReport{TCP: tcpCount, UDP: udpCount}

	uptime, err := unit.Uptime()
	if err != nil {
		message += fmt.Sprintf("failed to get uptime: %v\n", err)
	}
	data.Uptime = uptime

	data.Process = unit.ProcessCount()

	// GPU监控 - 根据标志决定详细程度
	if flags.EnableGPU {
		// 详细GPU监控模式
		gpuInfo, err := unit.GetDetailedGPUInfo()
		if err != nil {
			message += fmt.Sprintf("failed to get detailed GPU info: %v\n", err)
			// 降级到基础GPU信息
			gpuNames, nameErr := unit.GetDetailedGPUHost()
			if nameErr == nil && len(gpuNames) > 0 {
				data.GPU = gpuModelsReport{Models: gpuNames}
			}
		} else if len(gpuInfo) > 0 {
			// 成功获取详细信息
			gpuData := make([]gpuDeviceReport, len(gpuInfo))
			totalGPUUsage := 0.0

			for i, info := range gpuInfo {
				gpuData[i] = gpuDeviceReport{
					Name:        info.Name,
					MemoryTotal: info.MemoryTotal,
					MemoryUsed:  info.MemoryUsed,
					Utilization: info.Utilization,
					Temperature: info.Temperature,
				}
				totalGPUUsage += info.Utilization
			}

			avgGPUUsage := totalGPUUsage / float64(len(gpuInfo))
			data.GPU = gpuReport{Count: len(gpuInfo), AverageUsage: avgGPUUsage, DetailedInfo: gpuData}
		}
	}
	// 基础模式下，GPU信息已在basicInfo中处理

	data.Message = message

	s, err := json.Marshal(data)
	if err != nil {
		log.Println("Failed to marshal data:", err)
	}
	return s
}
