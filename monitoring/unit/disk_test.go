package monitoring

import (
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestIsPhysicalDisk(t *testing.T) {
	tests := []struct {
		name string
		part disk.PartitionStat
		want bool
	}{
		{
			name: "Android sdcard FUSE storage",
			part: disk.PartitionStat{
				Device:     "/dev/fuse",
				Mountpoint: "/sdcard",
				Fstype:     "fuse",
			},
			want: true,
		},
		{
			name: "other FUSE mount",
			part: disk.PartitionStat{
				Device:     "/dev/fuse",
				Mountpoint: "/mnt/fuse",
				Fstype:     "fuse",
			},
			want: false,
		},
		{
			name: "SSHFS mount",
			part: disk.PartitionStat{
				Device:     "user@example.com:/path",
				Mountpoint: "/mnt/sshfs",
				Fstype:     "fuse.sshfs",
			},
			want: false,
		},
		{
			name: "NTFS-3G mount",
			part: disk.PartitionStat{
				Device:     "/dev/sda1",
				Mountpoint: "/mnt/ntfs",
				Fstype:     "fuseblk",
			},
			want: true,
		},
		{
			name: "Docker overlay mount",
			part: disk.PartitionStat{
				Device:     "overlay",
				Mountpoint: "/var/lib/docker/overlay2/test/merged",
				Fstype:     "overlay",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPhysicalDisk(tt.part); got != tt.want {
				t.Errorf("isPhysicalDisk() = %v, want %v", got, tt.want)
			}
		})
	}
}
