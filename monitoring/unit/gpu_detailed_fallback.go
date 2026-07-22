//go:build !linux && !windows

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

// GetDetailedGPUHost 获取GPU型号信息 - 回退实现
func GetDetailedGPUHost() ([]string, error) {
	return nil, errors.New("detailed GPU monitoring not supported on this platform")
}

// GetDetailedGPUState 获取GPU使用率 - 回退实现
func GetDetailedGPUState() ([]float64, error) {
	return nil, errors.New("detailed GPU monitoring not supported on this platform")
}

// GetDetailedGPUInfo 获取详细GPU信息 - 回退实现
func GetDetailedGPUInfo() ([]DetailedGPUInfo, error) {
	return nil, errors.New("detailed GPU monitoring not supported on this platform")
}
