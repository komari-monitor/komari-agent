//go:build windows

package monitoring

import "time"

// scanUsed 在 Windows 上未实现：返回 false，配额模式回退为沿用磁盘使用量（不超过 --disk-total）。
func scanUsed(mountpoints []string, deadline time.Time) (uint64, bool) {
	return 0, false
}
