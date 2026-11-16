//go:build !linux

package monitoring

// procUptime 仅在 Linux 上实现；其它平台沿用 gopsutil 的 host.Uptime()。
func procUptime() (uint64, bool) {
	return 0, false
}
