package monitoring

import (
	"github.com/shirou/gopsutil/v4/host"
)

func Uptime() (uint64, error) {
	// Linux 上优先读 /proc/uptime：gopsutil 的 host.Uptime() 用 sysinfo(2) 系统调用，
	// 在 LXC 容器里返回的是宿主机的运行时间，而 /proc/uptime（被 LXCFS 等虚拟化后）是容器自己的。
	if up, ok := procUptime(); ok {
		return up, nil
	}
	return host.Uptime()
}
