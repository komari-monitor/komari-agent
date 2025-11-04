package monitoring

import (
	"fmt"
	"strings"
	"time"

	"github.com/komari-monitor/komari-agent/monitoring/netstatic"
	"github.com/komari-monitor/komari-agent/utils"
	"github.com/shirou/gopsutil/v4/net"
)

func ConnectionsCount() (tcpCount, udpCount int, err error) {
	tcps, err := net.Connections("tcp")
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get TCP connections: %w", err)
	}
	udps, err := net.Connections("udp")
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get UDP connections: %w", err)
	}

	return len(tcps), len(udps), nil
}

var (
	// 预定义常见的回环和虚拟接口名称
	loopbackNames = map[string]struct{}{
		"br":      {},
		"cni":     {},
		"docker":  {},
		"podman":  {},
		"flannel": {},
		"lo":      {},
		"veth":    {}, // Docker
		"virbr":   {}, // KVM
		"vmbr":    {}, // Proxmox
	}
)

// VnstatInterface represents a network interface in vnstat output
type VnstatInterface struct {
	Name    string        `json:"name"`
	Alias   string        `json:"alias"`
	Created VnstatDate    `json:"created"`
	Updated VnstatUpdated `json:"updated"`
	Traffic VnstatTraffic `json:"traffic"`
}

// VnstatDate represents date information
type VnstatDate struct {
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
}

// VnstatUpdated represents updated information
type VnstatUpdated struct {
	Date      VnstatDateInfo `json:"date"`
	Time      VnstatTimeInfo `json:"time"`
	Timestamp int64          `json:"timestamp"`
}

// VnstatDateInfo represents date components
type VnstatDateInfo struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// VnstatTimeInfo represents time components
type VnstatTimeInfo struct {
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

// VnstatTraffic represents traffic data from vnstat
type VnstatTraffic struct {
	Total      VnstatTotal        `json:"total"`
	FiveMinute []VnstatTimeEntry  `json:"fiveminute"`
	Hour       []VnstatTimeEntry  `json:"hour"`
	Day        []VnstatTimeEntry  `json:"day"`
	Month      []VnstatMonthEntry `json:"month"`
	Year       []VnstatYearEntry  `json:"year"`
	Top        []VnstatTimeEntry  `json:"top"`
}

// VnstatTotal represents total traffic data
type VnstatTotal struct {
	Rx uint64 `json:"rx"`
	Tx uint64 `json:"tx"`
}

// VnstatTimeEntry represents a time-based traffic entry
type VnstatTimeEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Time      VnstatTimeInfo `json:"time,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatMonthEntry represents a monthly traffic entry
type VnstatMonthEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatYearEntry represents a yearly traffic entry
type VnstatYearEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatOutput represents the complete vnstat JSON output
type VnstatOutput struct {
	VnstatVersion string            `json:"vnstatversion"`
	JsonVersion   string            `json:"jsonversion"`
	Interfaces    []VnstatInterface `json:"interfaces"`
}

func NetworkSpeed() (totalUp, totalDown, upSpeed, downSpeed uint64, err error) {
	includeNics := parseNics(flags.IncludeNics)
	excludeNics := parseNics(flags.ExcludeNics)

	// 如果设置了月重置（非0），统计totalUp、totalDown
	if flags.MonthRotate != 0 {
		netstatic.StartOrContinue() // 确保netstatic在运行
		now := uint64(time.Now().Unix())
		resetDay := uint64(utils.GetLastResetDate(flags.MonthRotate, time.Now()).Unix())
		nicStatics, err := netstatic.GetTotalTrafficBetween(resetDay, now)
		if err != nil {
			// 如果netstatic失败，回退到原来的方法，并返回额外的错误信息
			fallbackUp, fallbackDown, fallbackUpSpeed, fallbackDownSpeed, fallbackErr := getNetworkSpeedFallback(includeNics, excludeNics)
			if fallbackErr != nil {
				return fallbackUp, fallbackDown, fallbackUpSpeed, fallbackDownSpeed, fmt.Errorf("failed to call GetTotalTrafficBetween: %v; fallback error: %w", err, fallbackErr)
			}
			return fallbackUp, fallbackDown, fallbackUpSpeed, fallbackDownSpeed, fmt.Errorf("failed to call GetTotalTrafficBetween: %w", err)
		}

		for interfaceName, stats := range nicStatics {
			if shouldInclude(interfaceName, includeNics, excludeNics) {
				totalUp += stats.Tx
				totalDown += stats.Rx
			}
		}

		// 对于实时速度，仍然使用gopsutil方法
		_, _, upSpeed, downSpeed, err = getNetworkSpeedFallback(includeNics, excludeNics)
		if err != nil {
			return totalUp, totalDown, 0, 0, err
		}

		return totalUp, totalDown, upSpeed, downSpeed, nil
	}

	// 如果没有设置月重置，使用原来的方法
	return getNetworkSpeedFallback(includeNics, excludeNics)
}

func getNetworkSpeedFallback(includeNics, excludeNics map[string]struct{}) (totalUp, totalDown, upSpeed, downSpeed uint64, err error) {
	// 获取第一次网络IO计数器
	ioCounters1, err := net.IOCounters(true)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(ioCounters1) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("no network interfaces found")
	}

	// 统计第一次所有非回环接口的流量
	var totalUp1, totalDown1 uint64
	for _, interfaceStats := range ioCounters1 {
		if shouldInclude(interfaceStats.Name, includeNics, excludeNics) {
			totalUp1 += interfaceStats.BytesSent
			totalDown1 += interfaceStats.BytesRecv
		}
	}

	// 等待1秒
	time.Sleep(time.Second)

	// 获取第二次网络IO计数器
	ioCounters2, err := net.IOCounters(true)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(ioCounters2) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("no network interfaces found")
	}

	// 统计第二次所有非回环接口的流量
	var totalUp2, totalDown2 uint64
	for _, interfaceStats := range ioCounters2 {
		if shouldInclude(interfaceStats.Name, includeNics, excludeNics) {
			totalUp2 += interfaceStats.BytesSent
			totalDown2 += interfaceStats.BytesRecv
		}
	}

	// 计算速度 (每秒的速率)
	upSpeed = totalUp2 - totalUp1
	downSpeed = totalDown2 - totalDown1

	return totalUp2, totalDown2, upSpeed, downSpeed, nil
}

func parseNics(nics string) map[string]struct{} {
	if nics == "" {
		return nil
	}
	nicSet := make(map[string]struct{})
	for _, nic := range strings.Split(nics, ",") {
		nicSet[strings.TrimSpace(nic)] = struct{}{}
	}
	return nicSet
}

func shouldInclude(nicName string, includeNics, excludeNics map[string]struct{}) bool {
	// 默认排除回环接口
	for loopbackName := range loopbackNames {
		if strings.HasPrefix(nicName, loopbackName) {
			return false
		}
	}

	// 如果定义了白名单，则只包括白名单中的接口
	if len(includeNics) > 0 {
		_, ok := includeNics[nicName]
		return ok
	}

	// 如果定义了黑名单，则排除黑名单中的接口
	if len(excludeNics) > 0 {
		if _, ok := excludeNics[nicName]; ok {
			return false
		}
	}

	return true
}

func InterfaceList() ([]string, error) {
	includeNics := parseNics(flags.IncludeNics)
	excludeNics := parseNics(flags.ExcludeNics)
	interfaces := []string{}

	ioCounters, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}
	for _, interfaceStats := range ioCounters {
		if shouldInclude(interfaceStats.Name, includeNics, excludeNics) {
			interfaces = append(interfaces, interfaceStats.Name)
		}
	}
	return interfaces, nil
}
