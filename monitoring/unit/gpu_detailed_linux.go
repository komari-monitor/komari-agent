//go:build linux

package monitoring

import (
	"errors"
)

const (
	vendorAMD = iota + 1
	vendorNVIDIA
)

var vendorType = getDetailedVendor()

// DetailedGPUInfo 详细GPU信息结构体
type DetailedGPUInfo struct {
	Name        string  `json:"name"`         // GPU型号
	MemoryTotal uint64  `json:"memory_total"` // 总显存 (字节)
	MemoryUsed  uint64  `json:"memory_used"`  // 已用显存 (字节)
	Utilization float64 `json:"utilization"`  // GPU使用率 (0-100)
	Temperature uint64  `json:"temperature"`  // 温度 (摄氏度)
}

func getDetailedVendor() uint8 {
	_, err := getNvidiaDetailedStat()
	if err != nil {
		return vendorAMD
	} else {
		return vendorNVIDIA
	}
}

func getNvidiaDetailedStat() ([]float64, error) {
	smi := &NvidiaSMI{
		BinPath: "/usr/bin/nvidia-smi",
	}
	err1 := smi.Start()
	if err1 != nil {
		return nil, err1
	}
	data, err2 := smi.GatherUsage()
	if err2 != nil {
		return nil, err2
	}
	return data, nil
}

func getAMDDetailedStat() ([]float64, error) {
	if data, err := getAMDROCmDetailedStat(); err == nil && len(data) > 0 {
		return data, nil
	}
	return getAMDSysfsDetailedStat()
}

func getNvidiaDetailedHost() ([]string, error) {
	smi := &NvidiaSMI{
		BinPath: "/usr/bin/nvidia-smi",
	}
	err := smi.Start()
	if err != nil {
		return nil, err
	}
	data, err := smi.GatherModel()
	if err != nil {
		return nil, err
	}
	return data, nil
}

func getAMDDetailedHost() ([]string, error) {
	if data, err := getAMDROCmDetailedHost(); err == nil && len(data) > 0 {
		return data, nil
	}
	return getAMDSysfsDetailedHost()
}

// GetDetailedGPUHost 获取GPU型号信息
func GetDetailedGPUHost() ([]string, error) {
	var gi []string
	var err error

	switch vendorType {
	case vendorAMD:
		gi, err = getAMDDetailedHost()
	case vendorNVIDIA:
		gi, err = getNvidiaDetailedHost()
	default:
		return nil, errors.New("invalid vendor")
	}

	if err != nil {
		return nil, err
	}

	return gi, nil
}

// GetDetailedGPUState 获取GPU使用率
func GetDetailedGPUState() ([]float64, error) {
	var gs []float64
	var err error

	switch vendorType {
	case vendorAMD:
		gs, err = getAMDDetailedStat()
	case vendorNVIDIA:
		gs, err = getNvidiaDetailedStat()
	default:
		return nil, errors.New("invalid vendor")
	}

	if err != nil {
		return nil, err
	}

	return gs, nil
}

// GetDetailedGPUInfo 获取详细GPU信息
func GetDetailedGPUInfo() ([]DetailedGPUInfo, error) {
	var gpuInfos []DetailedGPUInfo
	var err error

	switch vendorType {
	case vendorAMD:
		gpuInfos, err = getAMDDetailedInfo()
	case vendorNVIDIA:
		gpuInfos, err = getNvidiaDetailedInfo()
	default:
		return nil, errors.New("invalid vendor")
	}

	if err != nil {
		return nil, err
	}

	return gpuInfos, nil
}

func getNvidiaDetailedInfo() ([]DetailedGPUInfo, error) {
	smi := &NvidiaSMI{
		BinPath: "/usr/bin/nvidia-smi",
	}
	err := smi.Start()
	if err != nil {
		return nil, err
	}

	data, err := smi.GatherDetailedInfo()
	if err != nil {
		return nil, err
	}

	var gpuInfos []DetailedGPUInfo
	for _, nvidiaInfo := range data {
		gpuInfo := DetailedGPUInfo{
			Name:        nvidiaInfo.Name,
			MemoryTotal: nvidiaInfo.MemoryTotal,
			MemoryUsed:  nvidiaInfo.MemoryUsed,
			Utilization: nvidiaInfo.Utilization,
			Temperature: nvidiaInfo.Temperature,
		}
		gpuInfos = append(gpuInfos, gpuInfo)
	}

	return gpuInfos, nil
}

func getAMDDetailedInfo() ([]DetailedGPUInfo, error) {
	if gpuInfos, err := getAMDROCmDetailedInfo(); err == nil && len(gpuInfos) > 0 {
		return gpuInfos, nil
	}
	return getAMDSysfsDetailedInfo()
}

func getAMDROCmDetailedStat() ([]float64, error) {
	rsmi := &ROCmSMI{
		BinPath: "/opt/rocm/bin/rocm-smi",
	}
	if err := rsmi.Start(); err != nil {
		return nil, err
	}
	return rsmi.GatherUsage()
}

func getAMDROCmDetailedHost() ([]string, error) {
	rsmi := &ROCmSMI{
		BinPath: "/opt/rocm/bin/rocm-smi",
	}
	if err := rsmi.Start(); err != nil {
		return nil, err
	}
	return rsmi.GatherModel()
}

func getAMDROCmDetailedInfo() ([]DetailedGPUInfo, error) {
	rsmi := &ROCmSMI{
		BinPath: "/opt/rocm/bin/rocm-smi",
	}
	if err := rsmi.Start(); err != nil {
		return nil, err
	}

	data, err := rsmi.GatherDetailedInfo()
	if err != nil {
		return nil, err
	}

	var gpuInfos []DetailedGPUInfo
	for _, amdInfo := range data {
		gpuInfos = append(gpuInfos, DetailedGPUInfo{
			Name:        amdInfo.Name,
			MemoryTotal: amdInfo.MemoryTotal,
			MemoryUsed:  amdInfo.MemoryUsed,
			Utilization: amdInfo.Utilization,
			Temperature: amdInfo.Temperature,
		})
	}
	return gpuInfos, nil
}
