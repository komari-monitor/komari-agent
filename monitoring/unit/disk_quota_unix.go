//go:build !windows

package monitoring

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var errScanDeadline = errors.New("disk scan deadline exceeded")

type inodeKey struct {
	dev uint64
	ino uint64
}

// scanUsed 统计各挂载点下文件实际占用的磁盘空间（字节），口径接近 `du -x`：
//   - 按分配的块数（st_blocks*512）计，稀疏文件不会被夸大；
//   - 同一 inode 的硬链接只算一次；
//   - 不跨文件系统（遇到设备号不同的子目录直接跳过），避免把 /proc、其它挂载算进来；
//   - 不跟随符号链接。
//
// 超过 deadline 视为失败（返回 false），由调用方回退；没有任何挂载点能扫描也返回 false。
func scanUsed(mountpoints []string, deadline time.Time) (uint64, bool) {
	var total uint64
	scanned := false
	seen := make(map[inodeKey]struct{})

	for _, root := range mountpoints {
		fi, err := os.Lstat(root)
		if err != nil {
			continue
		}
		rootStat, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		rootDev := uint64(rootStat.Dev) //nolint:unconvert // Dev 的类型因平台而异
		scanned = true

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if time.Now().After(deadline) {
				return errScanDeadline
			}
			if err != nil {
				// 读不了的目录（权限等）跳过，继续统计其它部分
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil
			}
			if uint64(st.Dev) != rootDev { //nolint:unconvert
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if st.Nlink > 1 && !d.IsDir() {
				key := inodeKey{dev: uint64(st.Dev), ino: uint64(st.Ino)} //nolint:unconvert
				if _, dup := seen[key]; dup {
					return nil
				}
				seen[key] = struct{}{}
			}
			total += uint64(st.Blocks) * 512 //nolint:unconvert
			return nil
		})
		if errors.Is(walkErr, errScanDeadline) {
			return 0, false
		}
	}
	return total, scanned
}
