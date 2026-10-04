//go:build linux

package monitoring

import (
	"os"
	"strconv"
	"strings"
)

var procUptimePath = "/proc/uptime"

// procUptime 读取 /proc/uptime 的第一个字段（秒）。
// 普通机器上它与 sysinfo(2) 的结果一致；在 LXC 容器里它是容器自己的运行时间。
// 监控宿主机（设置了 HOST_PROC）时保持原行为，读不到或格式不对时返回 false，由调用方回退。
func procUptime() (uint64, bool) {
	if flags.HostProc != "" {
		return 0, false
	}
	b, err := os.ReadFile(procUptimePath)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return uint64(secs), true
}
