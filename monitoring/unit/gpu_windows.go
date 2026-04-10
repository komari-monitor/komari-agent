//go:build windows
// +build windows

package monitoring

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const cimQueryTimeout = 3 * time.Second

type win32VideoController struct {
	Name                   string `json:"Name"`
	PNPDeviceID            string `json:"PNPDeviceID"`
	Status                 string `json:"Status"`
	ConfigManagerErrorCode int    `json:"ConfigManagerErrorCode"`
}

func GpuName() string {
	controllers, err := queryVideoControllers()
	if err != nil {
		return "Unknown"
	}
	result := formatVideoControllerNames(controllers)
	if result != "" {
		return result
	}
	return "None"
}

func formatVideoControllerNames(controllers []win32VideoController) string {
	seenDeviceIDs := make(map[string]struct{})
	nameCounts := make(map[string]int)
	nameOrder := make([]string, 0)

	for _, controller := range controllers {
		if controller.ConfigManagerErrorCode != 0 {
			continue
		}

		status := strings.ToUpper(strings.TrimSpace(controller.Status))
		if status != "" && status != "OK" {
			continue
		}

		deviceDesc := strings.TrimSpace(controller.Name)
		if deviceDesc == "" {
			continue
		}

		if isVirtualWindowsGPU(deviceDesc, controller.PNPDeviceID) {
			continue
		}

		deviceID := normalizeDeviceID(controller.PNPDeviceID)
		if deviceID != "" {
			if _, exists := seenDeviceIDs[deviceID]; exists {
				continue
			}
			seenDeviceIDs[deviceID] = struct{}{}
		}

		if _, exists := nameCounts[deviceDesc]; !exists {
			nameOrder = append(nameOrder, deviceDesc)
		}
		nameCounts[deviceDesc]++
	}

	if len(nameOrder) > 0 {
		parts := make([]string, 0, len(nameOrder))
		for _, name := range nameOrder {
			count := nameCounts[name]
			if count > 1 {
				parts = append(parts, name+" × "+strconv.Itoa(count))
				continue
			}
			parts = append(parts, name)
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func queryVideoControllers() ([]win32VideoController, error) {
	script := `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $ErrorActionPreference = 'Stop'; Get-CimInstance Win32_VideoController | Select-Object Name,PNPDeviceID,Status,ConfigManagerErrorCode | ConvertTo-Json -Compress`
	ctx, cancel := context.WithTimeout(context.Background(), cimQueryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script).Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return []win32VideoController{}, nil
		}
		return nil, err
	}

	return decodeVideoControllersJSON(strings.TrimSpace(string(out)))
}

func decodeVideoControllersJSON(trimmed string) ([]win32VideoController, error) {
	if trimmed == "" || trimmed == "null" {
		return []win32VideoController{}, nil
	}

	var list []win32VideoController
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
		return list, nil
	}

	var single win32VideoController
	if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
		return nil, err
	}
	return []win32VideoController{single}, nil
}

func normalizeDeviceID(deviceID string) string {
	return strings.ToUpper(strings.TrimSpace(deviceID))
}

func isVirtualWindowsGPU(name string, pnpDeviceID string) bool {
	upperPNP := normalizeDeviceID(pnpDeviceID)
	if strings.HasPrefix(upperPNP, "SWD\\") || strings.HasPrefix(upperPNP, "ROOT\\") {
		return true
	}

	lowerName := strings.ToLower(strings.TrimSpace(name))
	virtualNamePatterns := []string{
		"microsoft remote display adapter",
		"microsoft basic render driver",
		"remote display",
		"virtual",
		"virtio",
		"vmware",
		"virtualbox",
		"qxl",
		"parallels",
		"hyper-v",
	}
	for _, pattern := range virtualNamePatterns {
		if strings.Contains(lowerName, pattern) {
			return true
		}
	}

	return false
}
