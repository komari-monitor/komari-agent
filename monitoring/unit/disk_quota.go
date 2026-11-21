package monitoring

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// diskScanTTL 配额模式下，文件占用扫描结果的缓存时间。
// 扫描比 statfs 贵得多，上报循环（默认 3 秒一次）不应每次都扫。
const diskScanTTL = 5 * time.Minute

// diskScanBudget 单次扫描的时间预算。超过则放弃本次结果并回退为 statfs 的已用量，
// 避免有人把 --disk-total 用在很大的磁盘上时无休止地占用 CPU/IO。
const diskScanBudget = 2 * time.Minute

var diskScanCache struct {
	sync.Mutex
	key        string
	used       uint64
	ok         bool
	updated    time.Time
	refreshing bool
}

var (
	diskTotalWarnOnce sync.Once
	diskScanWarnOnce  sync.Once
)

// ParseDiskSize 解析 "1G"、"512M"、"1.5GiB"、"1073741824" 之类的容量，返回字节数。
// 后缀 K/M/G/T/P 按 1024 进制，可带 i / iB / B；不带后缀视为字节。
func ParseDiskSize(s string) (uint64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("empty size")
	}
	v := strings.ToLower(raw)
	v = strings.TrimSuffix(v, "b")
	v = strings.TrimSuffix(v, "i")
	mult := float64(1)
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 'k':
			mult = 1 << 10
		case 'm':
			mult = 1 << 20
		case 'g':
			mult = 1 << 30
		case 't':
			mult = 1 << 40
		case 'p':
			mult = 1 << 50
		}
		if mult != 1 {
			v = v[:n-1]
		}
	}
	num, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(num) || math.IsInf(num, 0) {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	if num <= 0 {
		return 0, fmt.Errorf("size must be positive: %q", raw)
	}
	bytes := num * mult
	if bytes >= math.MaxUint64 {
		return 0, fmt.Errorf("size too large: %q", raw)
	}
	return uint64(bytes), nil
}

// applyDiskTotal 在配额模式下改写磁盘统计：
// 总量使用 --disk-total 指定的值；已用量不再取 statfs（在 btrfs 配额、部分 LXC/容器存储上，
// statfs 返回的是整个存储池而不是配额），而是扫描被监控挂载点下的文件占用。
// 参数无效或无法扫描时回退为原有行为，不影响上报。
func applyDiskTotal(stat DiskInfo, spec string) DiskInfo {
	total, err := ParseDiskSize(spec)
	if err != nil {
		diskTotalWarnOnce.Do(func() { log.Printf("Ignoring --disk-total: %v", err) })
		return stat
	}
	mounts, err := DiskMountpoints()
	if err != nil || len(mounts) == 0 {
		return stat
	}
	used, ok := scanUsedCached(mounts)
	if !ok {
		// 无法扫描（例如 Windows）：沿用 statfs 的已用量，但不超过指定总量
		used = stat.Used
	}
	if used > total {
		used = total
	}
	return DiskInfo{Total: total, Used: used}
}

// scanUsedCached 返回缓存的扫描结果；扫描总是在后台进行，调用方不会被拖住。
// 还没有结果时（第一次调用、或上一次扫描超时失败）返回 ok=false，由调用方回退；
// 缓存超过 diskScanTTL 后在后台刷新，期间继续返回旧值。
func scanUsedCached(mounts []string) (uint64, bool) {
	key := strings.Join(mounts, "\x00")
	c := &diskScanCache
	c.Lock()
	defer c.Unlock()

	used, ok := uint64(0), false
	if c.key == key && !c.updated.IsZero() {
		used, ok = c.used, c.ok
		if time.Since(c.updated) < diskScanTTL {
			return used, ok
		}
	}
	if !c.refreshing {
		c.refreshing = true
		go func() {
			u, k := scanUsed(mounts, time.Now().Add(diskScanBudget))
			if !k {
				diskScanWarnOnce.Do(func() {
					log.Printf("Disk usage scan did not finish within %s (or nothing could be scanned); falling back to statfs used space. Use --include-mountpoint to restrict the scan to a smaller mount point.", diskScanBudget)
				})
			}
			c.Lock()
			c.key, c.used, c.ok, c.updated, c.refreshing = key, u, k, time.Now(), false
			c.Unlock()
		}()
	}
	return used, ok
}

// WaitForDiskScan 等待配额模式的后台扫描产出第一个结果（用于 list-disk 这类一次性命令），
// 返回是否在 timeout 内得到结果。
func WaitForDiskScan(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		diskScanCache.Lock()
		done := !diskScanCache.updated.IsZero()
		diskScanCache.Unlock()
		if done {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// QuotaOrStatfs 决定一个挂载点最终采用的 (总量, 已用量)：
// 只有当配额上限大于 0 且 **小于** statfs 的总量时才采用配额；
// 配额不小于整个文件系统容量说明它没有实际约束（或数据异常），继续使用 statfs，
// 保证配额检测永远不会让容量比真实容量更大。
func QuotaOrStatfs(statTotal, statUsed, limit, quotaUsed uint64) (total, used uint64) {
	if limit > 0 && limit < statTotal {
		return limit, quotaUsed
	}
	return statTotal, statUsed
}
