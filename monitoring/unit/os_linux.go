//go:build !windows
// +build !windows

package monitoring

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

func OSName() string {
	// Check if it's an Android system
	if androidVersion := detectAndroid(); androidVersion != "" {
		return androidVersion
	}

	// Check if it's a Proxmox VE
	if pveVersion := detectProxmoxVE(); pveVersion != "" {
		return pveVersion
	}

	// Check if it's a Synology
	if synologyName := detectSynology(); synologyName != "" {
		return synologyName
	}

	// Check if it's a fnOS
	if fnOSName := detectFnOS(); fnOSName != "" {
		return fnOSName
	}

	file, err := os.Open("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(line[len("PRETTY_NAME="):], `"`)
		}
	}

	if err := scanner.Err(); err != nil {
		return "Linux"
	}

	return "Linux"
}

func detectFnOS() string {
	if info, err := os.Stat("/usr/trim/BUILD_VERSION"); err == nil && !info.IsDir() {
		if data, err := os.ReadFile("/usr/trim/BUILD_VERSION"); err == nil {
			// format like "1.1.11"
			if version := strings.TrimSpace(string(data)); version != "" {
				return "fnOS " + version
			}
		}
	}
	if info, err := os.Stat("/usr/trim"); err == nil && info.IsDir() {
		return "fnOS"
	}
	return ""
}

func detectSynology() string {
	synologyFiles := []string{
		"/etc/synoinfo.conf",
		"/etc.defaults/synoinfo.conf",
	}

	for _, file := range synologyFiles {
		if info, err := os.Stat(file); err == nil && !info.IsDir() {
			if synologyInfo := readSynologyInfo(file); synologyInfo != "" {
				return synologyInfo
			}
		}
	}

	if info, err := os.Stat("/usr/syno"); err == nil && info.IsDir() {
		return "Synology DSM"
	}

	return ""
}

func readSynologyInfo(filename string) string {
	file, err := os.Open(filename)
	if err != nil {
		return ""
	}
	defer file.Close()

	var unique, udcCheckState string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "unique=") {
			unique = strings.Trim(strings.TrimPrefix(line, "unique="), `"`)
		} else if strings.HasPrefix(line, "udc_check_state=") {
			udcCheckState = strings.Trim(strings.TrimPrefix(line, "udc_check_state="), `"`)
		}
	}

	if unique != "" && strings.Contains(unique, "synology_") {
		parts := strings.Split(unique, "_")
		if len(parts) >= 3 {
			model := strings.ToUpper(parts[len(parts)-1])

			result := "Synology " + model

			if udcCheckState != "" {
				result += " DSM " + udcCheckState
			} else {
				result += " DSM"
			}

			return result
		}
	}

	return ""
}

func detectProxmoxVE() string {
	if _, err := exec.LookPath("pveversion"); err != nil {
		return ""
	}

	out, err := exec.Command("pveversion").Output()
	if err != nil {
		return ""
	}

	output := strings.TrimSpace(string(out))
	lines := strings.Split(output, "\n")

	var version string
	var codename string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "pve-manager/") {
			parts := strings.Split(line, "/")
			if len(parts) >= 2 {
				versionPart := parts[1]
				if idx := strings.Index(versionPart, "~"); idx != -1 {
					versionPart = versionPart[:idx]
				}
				version = versionPart
			}
		}

	}

	if version != "" {
		if file, err := os.Open("/etc/os-release"); err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "VERSION_CODENAME=") {
					codename = strings.Trim(line[len("VERSION_CODENAME="):], `"`)
					break
				}
			}
		}
	}

	if version != "" {
		if codename != "" {
			return "Proxmox VE " + version + " (" + codename + ")"
		}
		return "Proxmox VE " + version
	}

	return "Proxmox VE"
}

// detectAndroid detects if the system is Android and returns detailed information
func detectAndroid() string {
	// 1. Try to get Android version info using the getprop command
	cmdGetprop := exec.Command("getprop", "ro.build.version.release")
	version, err := cmdGetprop.Output()
	if err == nil && len(version) > 0 {
		versionStr := strings.TrimSpace(string(version))

		// Get device model
		cmdGetModel := exec.Command("getprop", "ro.product.model")
		model, err2 := cmdGetModel.Output()
		modelStr := strings.TrimSpace(string(model))

		// Get manufacturer information
		cmdGetBrand := exec.Command("getprop", "ro.product.brand")
		brand, err3 := cmdGetBrand.Output()
		brandStr := strings.TrimSpace(string(brand))

		result := "Android " + versionStr

		// Add brand and model information (if available)
		if err2 == nil && modelStr != "" {
			if err3 == nil && brandStr != "" && brandStr != modelStr {
				result += " (" + brandStr + " " + modelStr + ")"
			} else {
				result += " (" + modelStr + ")"
			}
		}

		return result
	}

	// 2. Try to check Android system build.prop file
	if _, err := os.Stat("/system/build.prop"); err == nil {
		return readAndroidBuildProp()
	}

	// 3. Check for typical Android directory structure
	if isAndroidSystem() {
		return "Android"
	}

	return ""
}

// readAndroidBuildProp reads Android version information from build.prop file
func readAndroidBuildProp() string {
	file, err := os.Open("/system/build.prop")
	if err != nil {
		return "Android"
	}
	defer file.Close()

	var version, model, brand string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "ro.build.version.release=") {
			version = strings.TrimPrefix(line, "ro.build.version.release=")
		} else if strings.HasPrefix(line, "ro.product.model=") {
			model = strings.TrimPrefix(line, "ro.product.model=")
		} else if strings.HasPrefix(line, "ro.product.brand=") {
			brand = strings.TrimPrefix(line, "ro.product.brand=")
		}

		// If all information has been collected, we can exit early
		if version != "" && model != "" && brand != "" {
			break
		}
	}

	if version != "" {
		result := "Android " + version

		if model != "" {
			if brand != "" && brand != model {
				result += " (" + brand + " " + model + ")"
			} else {
				result += " (" + model + ")"
			}
		}

		return result
	}

	return "Android"
}

// isAndroidSystem determines if the system is Android by checking typical directory structure
func isAndroidSystem() bool {
	androidDirs := []string{
		"/system/app",
		"/system/priv-app",
		"/data/app",
		"/sdcard",
	}

	dirCount := 0
	for _, dir := range androidDirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirCount++
		}
	}

	// Consider it an Android system only if at least 2 directories exist
	return dirCount >= 2
}

// KernelVersion returns the kernel version on Linux systems
func KernelVersion() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "Unknown"
	}

	return strings.TrimSpace(string(out))
}
