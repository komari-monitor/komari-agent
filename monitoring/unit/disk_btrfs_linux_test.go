//go:build linux

package monitoring

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeBtrfs 在临时目录里搭出 mountinfo 与 /sys/fs/btrfs 的样子，并让包内变量指向它们。
type fakeBtrfs struct {
	t     *testing.T
	root  string
	lines []string
}

func newFakeBtrfs(t *testing.T) *fakeBtrfs {
	t.Helper()
	f := &fakeBtrfs{t: t, root: t.TempDir()}
	oldMI, oldSys, oldFlag := btrfsMountinfoPath, btrfsSysfsRoot, flags.DisableBtrfsQuota
	btrfsMountinfoPath = filepath.Join(f.root, "mountinfo")
	btrfsSysfsRoot = filepath.Join(f.root, "sys-fs-btrfs")
	flags.DisableBtrfsQuota = false
	t.Cleanup(func() { btrfsMountinfoPath, btrfsSysfsRoot, flags.DisableBtrfsQuota = oldMI, oldSys, oldFlag })
	return f
}

func (f *fakeBtrfs) mount(line string) { f.lines = append(f.lines, line) }

func (f *fakeBtrfs) flushMountinfo() {
	f.t.Helper()
	content := ""
	for _, l := range f.lines {
		content += l + "\n"
	}
	if err := os.WriteFile(btrfsMountinfoPath, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeBtrfs) qgroup(fsid, id string, files map[string]string) {
	f.t.Helper()
	dir := filepath.Join(btrfsSysfsRoot, fsid, "qgroups", "0_"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for name, val := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val+"\n"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// 仿照真实非特权 LXC 容器（btrfs 子卷 + qgroup 配额）的 mountinfo 格式；所有编号、设备名、容量均为示例值。
const exampleContainerRoot = `100 50 0:60 /containers/EXAMPLE/rootfs / rw,relatime,idmapped shared:11 master:12 - btrfs /dev/loop0 rw,discard=async,space_cache=v2,user_subvol_rm_allowed,subvolid=1234,subvol=/containers/EXAMPLE`

func TestBtrfsQuotaContainerLayout(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.mount(`101 100 0:61 / /dev rw,nosuid - tmpfs tmpfs rw,size=492k`)
	f.flushMountinfo()
	fsid := "00000000-0000-4000-8000-000000000001"
	f.qgroup(fsid, "1234", map[string]string{
		"referenced": "70000000", "exclusive": "40000000",
		"max_referenced": "1000000000", "max_exclusive": "0", "limit_flags": "1",
	})
	f.qgroup(fsid, "300", map[string]string{"referenced": "1", "max_referenced": "0", "limit_flags": "0"})

	limit, used, ok := BtrfsQuotaFor("/")
	if !ok {
		t.Fatal("expected a quota to be detected")
	}
	if limit != 1000000000 || used != 70000000 {
		t.Errorf("limit/used = %d/%d, want 1000000000/70000000", limit, used)
	}
}

func TestBtrfsQuotaNoLimit(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.flushMountinfo()
	f.qgroup("fs1", "1234", map[string]string{"referenced": "10", "max_referenced": "0", "limit_flags": "0"})
	if _, _, ok := BtrfsQuotaFor("/"); ok {
		t.Error("limit_flags=0 means no limit; must fall back")
	}
}

func TestBtrfsQuotaExclusiveOnly(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.flushMountinfo()
	f.qgroup("fs1", "1234", map[string]string{
		"referenced": "900", "exclusive": "300", "max_referenced": "0", "max_exclusive": "2000", "limit_flags": "2",
	})
	limit, used, ok := BtrfsQuotaFor("/")
	if !ok || limit != 2000 || used != 300 {
		t.Errorf("got %d/%d ok=%v, want 2000/300 true", limit, used, ok)
	}
}

func TestBtrfsQuotaHexLimitFlags(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.flushMountinfo()
	f.qgroup("fs1", "1234", map[string]string{"referenced": "5", "max_referenced": "100", "limit_flags": "0x1"})
	if limit, used, ok := BtrfsQuotaFor("/"); !ok || limit != 100 || used != 5 {
		t.Errorf("got %d/%d ok=%v", limit, used, ok)
	}
}

func TestBtrfsQuotaAmbiguousSubvolID(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.flushMountinfo()
	vals := map[string]string{"referenced": "5", "max_referenced": "100", "limit_flags": "1"}
	f.qgroup("fs-a", "1234", vals)
	f.qgroup("fs-b", "1234", vals)
	if _, _, ok := BtrfsQuotaFor("/"); ok {
		t.Error("two filesystems with the same subvolid is ambiguous; must not guess")
	}
}

func TestBtrfsQuotaNotBtrfsOrMissing(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(`30 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,subvolid=1234`)
	f.flushMountinfo()
	f.qgroup("fs1", "1234", map[string]string{"referenced": "5", "max_referenced": "100", "limit_flags": "1"})
	if _, _, ok := BtrfsQuotaFor("/"); ok {
		t.Error("ext4 mount must not use btrfs quota")
	}
	if _, _, ok := BtrfsQuotaFor("/nonexistent"); ok {
		t.Error("unknown mountpoint must return false")
	}
}

func TestBtrfsQuotaDisabledByFlag(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(exampleContainerRoot)
	f.flushMountinfo()
	f.qgroup("fs1", "1234", map[string]string{"referenced": "5", "max_referenced": "100", "limit_flags": "1"})
	flags.DisableBtrfsQuota = true
	if _, _, ok := BtrfsQuotaFor("/"); ok {
		t.Error("--disable-btrfs-quota must turn detection off")
	}
}

func TestBtrfsQuotaMountpointWithEscapedSpace(t *testing.T) {
	f := newFakeBtrfs(t)
	f.mount(`40 1 0:70 / /mnt/my\040disk rw,relatime - btrfs /dev/sdb rw,subvolid=300,subvol=/data`)
	f.flushMountinfo()
	f.qgroup("fs1", "300", map[string]string{"referenced": "7", "max_referenced": "70", "limit_flags": "1"})
	if limit, used, ok := BtrfsQuotaFor("/mnt/my disk"); !ok || limit != 70 || used != 7 {
		t.Errorf("got %d/%d ok=%v", limit, used, ok)
	}
}

func TestUnescapeMountinfo(t *testing.T) {
	cases := map[string]string{`/a\040b`: "/a b", `/plain`: "/plain", `/x\134y`: `/x\y`, `/bad\04`: `/bad\04`}
	for in, want := range cases {
		if got := unescapeMountinfo(in); got != want {
			t.Errorf("unescapeMountinfo(%q) = %q, want %q", in, got, want)
		}
	}
}
