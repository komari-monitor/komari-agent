//go:build linux
// +build linux

package monitoring

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func GpuName() string {
	if name := getFromLspci(); name != "None" {
		return name
	}

	if name := getFromSysfsDRM(); name != "None" {
		return name
	}
	return "None"
}

func getFromLspci() string {
	out, err := exec.Command("lspci").Output()
	if err != nil {
		return "None"
	}
	excludePatterns := []string{
		"^1111",                             // 1111 (rev 02)
		`(?i)^cirrus logic (cl[-\s]?)?gd 5`, // CL-GD 系列 1990 年代中期的产物, 现常用于虚拟机
		"(?i)virtio",
		"(?i)vmware",
		`(?i)qxl`, // SPICE 虚拟显卡
		`(?i)hyper-v`,
	}

	lines := strings.Split(string(out), "\n")

	priorityVendors := []string{"nvidia", "amd", "radeon", "intel", "arc", "snap", "qualcomm", "snapdragon"}

	isExcluded := func(name string) bool {
		for _, pattern := range excludePatterns {
			if matched, _ := regexp.MatchString(pattern, name); matched {
				return true
			}
		}
		return false
	}

	extractName := func(line string) string {
		// 取最后一个冒号之后的内容
		idx := strings.LastIndex(line, ":")
		if idx == -1 || idx == len(line)-1 {
			return ""
		}
		name := strings.TrimSpace(line[idx+1:])

		// 去除末尾的 (rev xx)
		if parenIdx := strings.LastIndex(name, "("); parenIdx != -1 {
			name = strings.TrimSpace(name[:parenIdx])
		}
		return name
	}

	// 寻找 priorityVendors
	for _, line := range lines {
		lower := strings.ToLower(line)

		// 必须确认是显示设备，防止匹配到 Intel 网卡或 Qualcomm 蓝牙
		if !strings.Contains(lower, "vga") && !strings.Contains(lower, "3d") && !strings.Contains(lower, "display") {
			continue
		}

		for _, vendor := range priorityVendors {
			if strings.Contains(lower, vendor) {
				name := extractName(line)
				if name != "" && !isExcluded(name) {
					// 找到独显立刻返回
					return name
				}
			}
		}
	}

	// 任意非黑名单的 VGA 设备
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "vga") || strings.Contains(lower, "3d") || strings.Contains(lower, "display") {
			name := extractName(line)
			if name != "" && !isExcluded(name) {
				return name
			}
		}
	}

	return "None"

}

func getFromSysfsDRM() string {
	matches, _ := filepath.Glob("/sys/class/drm/card*")

	excludedDrivers := map[string]bool{
		"virtio-pci":  true,
		"virtio_gpu":  true,
		"bochs-drm":   true,
		"qxl":         true,
		"vmwgfx":      true,
		"cirrus":      true,
		"vboxvideo":   true,
		"hyperv_fb":   true,
		"simpledrm":   true,
		"simplefb":    true,
		"cirrus-qemu": true,
	}

	for _, path := range matches {
		// 驱动名称
		driverLink, err := os.Readlink(filepath.Join(path, "device", "driver"))
		if err != nil {
			continue
		}
		driverName := filepath.Base(driverLink)

		if excludedDrivers[driverName] {
			continue
		}

		// 设备树 compatible 提取具体型号
		// /sys/class/drm/card0/device/of_node/compatible
		// "qcom,adreno-750.1\0qcom,adreno"
		exactModel := ""
		compatibleBytes, err := os.ReadFile(filepath.Join(path, "device", "of_node", "compatible"))
		if err == nil {
			exactModel = parseSocModel(driverName, compatibleBytes)
		}

		// 有具体型号则直接返回
		if exactModel != "" {
			return exactModel
		}

		// 通用的驱动名称映射
		switch driverName {
		case "vc4", "vc4-drm":
			return "Broadcom VideoCore IV/VI (Raspberry Pi)"
		case "v3d", "v3d-drm":
			return "Broadcom V3D (Raspberry Pi 4/5)"
		case "msm", "msm_drm":
			return "Qualcomm Adreno (Unknown Model)"
		case "panfrost":
			return "ARM Mali (Panfrost)"
		case "lima":
			return "ARM Mali (Lima)"
		case "sun4i-drm", "sunxi-drm":
			return "Allwinner Display Engine"
		case "tegra":
			return "NVIDIA Tegra"
		case "ast": // LXC 容器映射物理显卡
			return "ASPEED Technology, Inc. ASPEED Graphics Family"
		case "i915", "i915-drm":
			return "Intel Integrated Graphics"
		case "mgag200":
			return "Matrox G200 Series"
		}

		if driverName != "" {
			return "Direct Render Manager " + driverName
		}
	}

	// 开发板 Model
	modelData, err := os.ReadFile("/sys/firmware/devicetree/base/model")
	if err == nil {
		model := string(modelData)
		if strings.Contains(model, "Raspberry Pi") {
			return "Broadcom VideoCore (Integrated)"
		}
	}

	return "None"
}

// parseSocModel 解析设备树 compatible 字符串，提取人性化名称
func parseSocModel(driver string, rawBytes []byte) string {
	// compatible 文件包含多个以 \0 分隔的字符串
	content := string(bytes.ReplaceAll(rawBytes, []byte{0}, []byte(" ")))
	lower := strings.ToLower(content)

	// 高通 Adreno (Qualcomm)
	if driver == "msm" || strings.Contains(lower, "adreno") {
		// "adreno-750", "adreno-660"
		re := regexp.MustCompile(`adreno[-_](\d+)`)
		matches := re.FindStringSubmatch(lower)
		if len(matches) > 1 {
			return "Qualcomm Adreno " + matches[1]
		}
		return "Qualcomm Adreno"
	}

	// ARM Mali (Rockchip/MediaTek/AmLogic)
	if driver == "panfrost" || driver == "lima" || strings.Contains(lower, "mali") {
		// "mali-g610", "mali-t860"
		re := regexp.MustCompile(`mali[-_]([a-z]\d+)`)
		matches := re.FindStringSubmatch(lower)
		if len(matches) > 1 {
			return "ARM Mali " + strings.ToUpper(matches[1]) // Mali G610
		}
		return "ARM Mali" // 泛指
	}

	// 树莓派 VideoCore
	if driver == "vc4" || driver == "vc4-drm" || driver == "v3d" {
		if strings.Contains(lower, "bcm2712") {
			return "Broadcom VideoCore VII (Pi 5)"
		}
		if strings.Contains(lower, "bcm2711") {
			return "Broadcom VideoCore VI (Pi 4)"
		}
		if strings.Contains(lower, "bcm2837") || strings.Contains(lower, "bcm2835") {
			return "Broadcom VideoCore IV"
		}
	}

	// Allwinner (全志)
	// "allwinner,sun50i-h6-display-engine"
	if strings.Contains(lower, "allwinner") || strings.Contains(lower, "sun50i") || strings.Contains(lower, "sun8i") {
		re := regexp.MustCompile(`sun\d+i-([a-z0-9]+)`)
		matches := re.FindStringSubmatch(lower)
		if len(matches) > 1 {
			model := strings.ToUpper(matches[1])
			return "Allwinner " + model
		}
		return "Allwinner Display Engine"
	}

	// NVIDIA Tegra
	if driver == "tegra" {
		if strings.Contains(lower, "tegra194") {
			return "NVIDIA Tegra Xavier"
		}
		if strings.Contains(lower, "tegra234") {
			return "NVIDIA Orin"
		}
		if strings.Contains(lower, "tegra210") {
			return "NVIDIA Tegra X1"
		}
	}

	return ""
}
