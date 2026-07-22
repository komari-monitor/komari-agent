//go:build windows

package monitoring

import (
	"errors"
)

// DetailedGPUInfo 详细GPU信息结构体
type DetailedGPUInfo struct {
	Name        string  `json:"name"`         // GPU型号
	MemoryTotal uint64  `json:"memory_total"` // 总显存 (字节)
	MemoryUsed  uint64  `json:"memory_used"`  // 已用显存 (字节)
	Utilization float64 `json:"utilization"`  // GPU使用率 (0-100)
	Temperature uint64  `json:"temperature"`  // 温度 (摄氏度)
}

// GetDetailedGPUHost 获取GPU型号信息 (Windows: 仅支持 NVIDIA)
func GetDetailedGPUHost() ([]string, error) {
	smi := &NvidiaSMI{}
	if err := smi.Start(); err != nil {
		return nil, err
	}
	return smi.GatherModel()
}

// GetDetailedGPUState 获取GPU使用率 (Windows: 仅支持 NVIDIA)
func GetDetailedGPUState() ([]float64, error) {
	smi := &NvidiaSMI{}
	if err := smi.Start(); err != nil {
		return nil, err
	}
	return smi.GatherUsage()
}

// GetDetailedGPUInfo 获取详细GPU信息 (Windows: 仅支持 NVIDIA)
func GetDetailedGPUInfo() ([]DetailedGPUInfo, error) {
	smi := &NvidiaSMI{}
	if err := smi.Start(); err != nil {
		return nil, err
	}

	data, err := smi.GatherDetailedInfo()
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, errors.New("no NVIDIA GPU detected")
	}

	gpuInfos := make([]DetailedGPUInfo, len(data))
	for i, nvidiaInfo := range data {
		gpuInfos[i] = DetailedGPUInfo{
			Name:        nvidiaInfo.Name,
			MemoryTotal: nvidiaInfo.MemoryTotal,
			MemoryUsed:  nvidiaInfo.MemoryUsed,
			Utilization: nvidiaInfo.Utilization,
			Temperature: nvidiaInfo.Temperature,
		}
	}

	return gpuInfos, nil
}
