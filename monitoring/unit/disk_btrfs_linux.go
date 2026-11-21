//go:build linux

package monitoring

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// btrfs qgroup 配额探测。
//
// 背景：在 btrfs 上，容器/租户的磁盘配额通常设在子卷的 qgroup 上，statfs 看不到，
// 返回的是整个存储池的容量。较新的内核把每个 qgroup 以普通文件的形式放进 sysfs：
//
//	/sys/fs/btrfs/<fsid>/qgroups/<level>_<id>/{referenced,exclusive,max_referenced,max_exclusive,limit_flags}
//
// 而容器自己的子卷号可以从 /proc/self/mountinfo 的 subvolid= 读到，因此不需要任何特权。

var (
	btrfsMountinfoPath = "/proc/self/mountinfo"
	btrfsSysfsRoot     = "/sys/fs/btrfs"
)

// qgroup limit_flags 的位（内核 BTRFS_QGROUP_LIMIT_*）
const (
	qgroupLimitMaxRfer = 1 << 0
	qgroupLimitMaxExcl = 1 << 1
)

// BtrfsQuotaFor 返回 mountpoint 所在 btrfs 子卷的配额上限与已用量（字节）。
// 仅当该挂载点是 btrfs、且其子卷的 qgroup 设置了上限时 ok 才为 true；
// 任何一步不确定（非 btrfs、sysfs 不可读、没有设上限、存在歧义）都返回 false，由调用方回退。
func BtrfsQuotaFor(mountpoint string) (limit, used uint64, ok bool) {
	if flags.DisableBtrfsQuota {
		return 0, 0, false
	}
	subvolID, found := btrfsSubvolID(mountpoint)
	if !found {
		return 0, 0, false
	}
	dir, found := btrfsQgroupDir(subvolID)
	if !found {
		return 0, 0, false
	}
	lf, found := btrfsReadUint(filepath.Join(dir, "limit_flags"))
	if !found {
		return 0, 0, false
	}
	switch {
	case lf&qgroupLimitMaxRfer != 0:
		limit, found = btrfsReadUint(filepath.Join(dir, "max_referenced"))
		if !found {
			return 0, 0, false
		}
		used, found = btrfsReadUint(filepath.Join(dir, "referenced"))
	case lf&qgroupLimitMaxExcl != 0:
		limit, found = btrfsReadUint(filepath.Join(dir, "max_exclusive"))
		if !found {
			return 0, 0, false
		}
		used, found = btrfsReadUint(filepath.Join(dir, "exclusive"))
	default:
		return 0, 0, false
	}
	if !found || limit == 0 {
		return 0, 0, false
	}
	return limit, used, true
}

// btrfsSubvolID 在 mountinfo 里找到挂载点为 mountpoint 的 btrfs 挂载，返回其 subvolid。
// 同一挂载点被多次挂载时取最后一个（最上层的）。
func btrfsSubvolID(mountpoint string) (string, bool) {
	f, err := os.Open(btrfsMountinfoPath)
	if err != nil {
		return "", false
	}
	defer f.Close()

	want := filepath.Clean(mountpoint)
	id, found := "", false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		// 格式：... <mountpoint> <opts> [optional...] - <fstype> <source> <super options>
		line := sc.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		pre := strings.Fields(line[:sep])
		post := strings.Fields(line[sep+3:])
		if len(pre) < 5 || len(post) < 3 || post[0] != "btrfs" {
			continue
		}
		if filepath.Clean(unescapeMountinfo(pre[4])) != want {
			continue
		}
		for _, opt := range strings.Split(post[2], ",") {
			if v, ok := strings.CutPrefix(opt, "subvolid="); ok && v != "" {
				id, found = v, true
			}
		}
	}
	return id, found
}

// btrfsQgroupDir 返回唯一匹配的 qgroup 目录；没有或多于一个（不同文件系统的子卷号相同）时返回 false。
func btrfsQgroupDir(subvolID string) (string, bool) {
	if _, err := strconv.ParseUint(subvolID, 10, 64); err != nil {
		return "", false
	}
	matches, err := filepath.Glob(filepath.Join(btrfsSysfsRoot, "*", "qgroups", "0_"+subvolID))
	if err != nil || len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

// btrfsReadUint 读取 sysfs 里的十进制或 0x 前缀的整数。
func btrfsReadUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// unescapeMountinfo 还原 mountinfo 里的八进制转义（\040 空格、\011 制表符、\012 换行、\134 反斜杠）。
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
