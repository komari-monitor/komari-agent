package monitoring

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseDiskSize(t *testing.T) {
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{"1G", 1 << 30, false},
		{"1g", 1 << 30, false},
		{"1GiB", 1 << 30, false},
		{"1GB", 1 << 30, false},
		{"512M", 512 << 20, false},
		{"1.5G", 3 << 29, false},
		{"2T", 2 << 40, false},
		{"100K", 100 << 10, false},
		{"1073741824", 1 << 30, false},
		{" 1 G ", 1 << 30, false},
		{"", 0, true},
		{"abc", 0, true},
		{"0", 0, true},
		{"-1G", 0, true},
		{"1X", 0, true},
		{"99999999P", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseDiskSize(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseDiskSize(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseDiskSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanUsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scanUsed is not implemented on windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 64<<10)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "b.bin"), 128<<10)

	used, ok := scanUsed([]string{dir}, time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("scanUsed reported no mountpoint scanned")
	}
	// 至少要覆盖文件内容（分配块数 >= 文件大小），且不应离谱地大
	if used < (64<<10)+(128<<10) {
		t.Errorf("used = %d, want >= %d", used, (64<<10)+(128<<10))
	}
	if used > 4<<20 {
		t.Errorf("used = %d, unexpectedly large", used)
	}

	// 硬链接只算一次
	if err := os.Link(filepath.Join(dir, "a.bin"), filepath.Join(dir, "a.link")); err != nil {
		t.Skipf("hard links not supported here: %v", err)
	}
	usedAfter, _ := scanUsed([]string{dir}, time.Now().Add(time.Minute))
	// 多一个目录项最多多出一个目录块的量，远小于再算一遍 64KiB
	if usedAfter > used+(32<<10) {
		t.Errorf("hard link counted twice: before=%d after=%d", used, usedAfter)
	}

	// 符号链接不被跟随（指向一个很大的目录也不会被计入）
	if err := os.Symlink("/", filepath.Join(dir, "rootlink")); err == nil {
		usedSym, _ := scanUsed([]string{dir}, time.Now().Add(time.Minute))
		if usedSym > usedAfter+(32<<10) {
			t.Errorf("symlink was followed: before=%d after=%d", usedAfter, usedSym)
		}
	}
}

func TestScanUsedDeadlineExceeded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scanUsed is not implemented on windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 4<<10)
	if _, ok := scanUsed([]string{dir}, time.Now().Add(-time.Second)); ok {
		t.Error("expected ok=false when the deadline has already passed")
	}
}

func TestScanUsedMissingMountpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scanUsed is not implemented on windows")
	}
	if _, ok := scanUsed([]string{filepath.Join(t.TempDir(), "does-not-exist")}, time.Now().Add(time.Minute)); ok {
		t.Error("expected ok=false when no mountpoint can be scanned")
	}
}

func TestApplyDiskTotalInvalidKeepsOriginal(t *testing.T) {
	orig := DiskInfo{Total: 149 << 30, Used: 100 << 30}
	got := applyDiskTotal(orig, "not-a-size")
	if got != orig {
		t.Errorf("invalid --disk-total must not change stats: got %+v want %+v", got, orig)
	}
}

func TestApplyDiskTotalUsesQuota(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scanUsed is not implemented on windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.bin"), 256<<10)

	oldInclude := flags.IncludeMountpoints
	flags.IncludeMountpoints = dir
	diskScanCache.Lock()
	diskScanCache.key, diskScanCache.updated = "", time.Time{}
	diskScanCache.Unlock()
	defer func() { flags.IncludeMountpoints = oldInclude }()

	orig := DiskInfo{Total: 149 << 30, Used: 120 << 30}

	// 第一次调用：扫描在后台进行，先回退为 statfs 的已用量（并被总量封顶），不会阻塞
	first := applyDiskTotal(orig, "1G")
	if first.Total != 1<<30 {
		t.Errorf("Total = %d, want %d", first.Total, uint64(1<<30))
	}
	if first.Used != 1<<30 {
		t.Errorf("first Used = %d, want fallback capped at total %d", first.Used, uint64(1<<30))
	}

	// 等后台扫描完成后，已用量应是扫描值，而不是 120G 的 statfs 值
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got := applyDiskTotal(orig, "1G")
		if got.Used >= 256<<10 && got.Used < 4<<20 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("scanned used space never replaced the statfs fallback")
}

func TestQuotaOrStatfs(t *testing.T) {
	const tb = uint64(1) << 40
	tests := []struct {
		name                    string
		total, used, limit, qus uint64
		wantTotal, wantUsed     uint64
	}{
		{"quota smaller than pool is used", 149 << 30, 9 << 30, 1_024_000_000, 75_722_752, 1_024_000_000, 75_722_752},
		{"quota equal to capacity is ignored", tb, 10, tb, 5, tb, 10},
		{"quota larger than capacity is ignored", tb, 10, 2 * tb, 5, tb, 10},
		{"zero quota is ignored", tb, 10, 0, 5, tb, 10},
	}
	for _, tt := range tests {
		gotT, gotU := QuotaOrStatfs(tt.total, tt.used, tt.limit, tt.qus)
		if gotT != tt.wantTotal || gotU != tt.wantUsed {
			t.Errorf("%s: got %d/%d, want %d/%d", tt.name, gotT, gotU, tt.wantTotal, tt.wantUsed)
		}
	}
}
