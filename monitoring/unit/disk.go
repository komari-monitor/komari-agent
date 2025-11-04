package monitoring

import (
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

type DiskInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

func Disk() DiskInfo {
	diskinfo := DiskInfo{}
	usage, err := disk.Partitions(true)
	if err != nil {
		diskinfo.Total = 0
		diskinfo.Used = 0
	} else {
		// 如果指定了自定义挂载点，只统计指定的挂载点
		if flags.IncludeMountpoints != "" {
			includeMounts := strings.Split(flags.IncludeMountpoints, ";")
			for _, mountpoint := range includeMounts {
				mountpoint = strings.TrimSpace(mountpoint)
				if mountpoint != "" {
					u, err := disk.Usage(mountpoint)
					if err != nil {
						continue
					} else {
						diskinfo.Total += u.Total
						diskinfo.Used += u.Used
					}
				}
			}
		} else {
			// 使用默认逻辑，排除临时文件系统和网络驱动器
			for _, part := range usage {
				if isPhysicalDisk(part) {
					u, err := disk.Usage(part.Mountpoint)
					if err != nil {
						continue
					} else {
						diskinfo.Total += u.Total
						diskinfo.Used += u.Used
					}
				}
			}
		}
	}
	return diskinfo
}

// isPhysicalDisk 判断分区是否为物理磁盘
func isPhysicalDisk(part disk.PartitionStat) bool {
	// 对于LXC等基于loop的根文件系统，始终包含根挂载点
	if part.Mountpoint == "/" {
		return true
	}
	mountpoint := strings.ToLower(part.Mountpoint)
	// 排除挂载点
	var mountpointsToExclude = []string{
		"/tmp",
		"/var/tmp",
		"/dev/shm",
		"/run",
		"/run/lock",
		"/run/user",
		"/var/lib/containers",
		"/var/lib/docker",
		"/proc",
		"/dev/pts",
		"/sys",
		"/sys/fs/cgroup",
		"/dev/mqueue",
		"/etc/resolv.conf",
		"/etc/host", // /etc/hosts,/etc/hostname
		"/dev/hugepages",
	}
	for _, mp := range mountpointsToExclude {
		if mountpoint == mp || strings.HasPrefix(mountpoint, mp) {
			return false
		}
	}

	fstype := strings.ToLower(part.Fstype)
	var fstypeToExclude = []string{
		"tmpfs",
		"devtmpfs",
		"nfs",
		"cifs",
		"smb",
		"vboxsf",
		"9p",
		"fuse",
		"overlay",
		"proc",
		"devpts",
		"sysfs",
		"cgroup",
		"mqueue",
		"hugetlbfs",
	}
	for _, fs := range fstypeToExclude {
		if fstype == fs || strings.HasPrefix(fstype, fs) {
			return false
		}
	}
	// Windows 网络驱动器通常是映射盘符，但不容易通过fstype判断
	// 可以通过opts判断，Windows网络驱动通常有相关选项
	optsStr := strings.ToLower(strings.Join(part.Opts, ","))
	if strings.Contains(optsStr, "remote") || strings.Contains(optsStr, "network") {
		return false
	}

	// 虚拟内存
	if strings.HasPrefix(part.Device, "/dev/loop") {
		return false
	}

	return true
}

func DiskList() ([]string, error) {
	diskList := []string{}
	if flags.IncludeMountpoints != "" {
		includeMounts := strings.Split(flags.IncludeMountpoints, ";")
		for _, mountpoint := range includeMounts {
			mountpoint = strings.TrimSpace(mountpoint)
			if mountpoint != "" {
				diskList = append(diskList, mountpoint)
			}
		}
	} else {
		usage, err := disk.Partitions(true)
		if err != nil {
			return nil, err
		}
		for _, part := range usage {
			if isPhysicalDisk(part) {
				diskList = append(diskList, fmt.Sprintf("%s (%s)", part.Mountpoint, part.Fstype))
			}
		}
	}
	return diskList, nil
}
