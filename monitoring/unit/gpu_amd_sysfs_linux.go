//go:build linux

package monitoring

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func getAMDSysfsDetailedInfo() ([]DetailedGPUInfo, error) {
	cards, err := listAMDSysfsCards()
	if err != nil {
		return nil, err
	}

	fallbackName := GpuName()
	infos := make([]DetailedGPUInfo, 0, len(cards))
	for _, card := range cards {
		name := card.name
		if name == "" || name == "None" {
			name = fallbackName
		}
		if name == "" || name == "None" {
			name = "AMD GPU"
		}
		infos = append(infos, DetailedGPUInfo{
			Name:        name,
			MemoryTotal: card.memoryTotal,
			MemoryUsed:  card.memoryUsed,
			Utilization: card.utilization,
			Temperature: card.temperature,
		})
	}
	return infos, nil
}

func getAMDSysfsDetailedHost() ([]string, error) {
	infos, err := getAMDSysfsDetailedInfo()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name != "" {
			names = append(names, info.Name)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no AMD GPU found in sysfs")
	}
	return names, nil
}

func getAMDSysfsDetailedStat() ([]float64, error) {
	infos, err := getAMDSysfsDetailedInfo()
	if err != nil {
		return nil, err
	}
	usage := make([]float64, 0, len(infos))
	for _, info := range infos {
		usage = append(usage, info.Utilization)
	}
	return usage, nil
}

type amdSysfsCard struct {
	name        string
	memoryTotal uint64
	memoryUsed  uint64
	utilization float64
	temperature uint64
}

func listAMDSysfsCards() ([]amdSysfsCard, error) {
	matches, err := filepath.Glob("/sys/class/drm/card*")
	if err != nil {
		return nil, err
	}

	var cards []amdSysfsCard
	for _, path := range matches {
		if !isDRMCardPath(path) {
			continue
		}

		deviceDir := filepath.Join(path, "device")
		driverLink, err := os.Readlink(filepath.Join(deviceDir, "driver"))
		if err != nil {
			continue
		}
		if filepath.Base(driverLink) != "amdgpu" {
			continue
		}

		busy, err := readSysfsFloat(filepath.Join(deviceDir, "gpu_busy_percent"))
		if err != nil {
			continue
		}

		card := amdSysfsCard{
			name:        readAMDSysfsName(deviceDir),
			utilization: busy,
			temperature: readAMDSysfsTemperature(deviceDir),
		}
		if total, err := readSysfsUint(filepath.Join(deviceDir, "mem_info_vram_total")); err == nil {
			card.memoryTotal = total
		}
		if used, err := readSysfsUint(filepath.Join(deviceDir, "mem_info_vram_used")); err == nil {
			card.memoryUsed = used
		}
		cards = append(cards, card)
	}

	if len(cards) == 0 {
		return nil, errors.New("no amdgpu sysfs metrics found")
	}
	return cards, nil
}

func readAMDSysfsName(deviceDir string) string {
	for _, name := range []string{"product_name", "product_description"} {
		data, err := os.ReadFile(filepath.Join(deviceDir, name))
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(data))
		if value != "" {
			return value
		}
	}
	return ""
}

func readAMDSysfsTemperature(deviceDir string) uint64 {
	hwmons, err := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*"))
	if err != nil {
		return 0
	}

	var fallback uint64
	for _, hwmon := range hwmons {
		labels, _ := filepath.Glob(filepath.Join(hwmon, "temp*_label"))
		for _, labelPath := range labels {
			labelBytes, err := os.ReadFile(labelPath)
			if err != nil {
				continue
			}
			label := strings.ToLower(strings.TrimSpace(string(labelBytes)))
			inputPath := strings.TrimSuffix(labelPath, "_label") + "_input"
			milli, err := readSysfsUint(inputPath)
			if err != nil {
				continue
			}
			celsius := milli / 1000
			if strings.Contains(label, "junction") || strings.Contains(label, "edge") {
				return celsius
			}
			if fallback == 0 {
				fallback = celsius
			}
		}

		if fallback == 0 {
			inputs, _ := filepath.Glob(filepath.Join(hwmon, "temp*_input"))
			for _, inputPath := range inputs {
				milli, err := readSysfsUint(inputPath)
				if err != nil {
					continue
				}
				fallback = milli / 1000
				break
			}
		}
	}
	return fallback
}

func readSysfsUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func readSysfsFloat(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
}
