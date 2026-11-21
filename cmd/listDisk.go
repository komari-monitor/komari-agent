package cmd

import (
	"fmt"
	"log"
	"text/tabwriter"
	"time"

	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/spf13/cobra"
)

var ListDiskCmd = &cobra.Command{
	Use:   "list-disk",
	Short: "List all physical disks",
	Long:  `List all physical disks`,
	Run: func(cmd *cobra.Command, args []string) {
		dl, err := disk.Partitions(true)
		if err != nil {
			log.Println("Failed to get disk partitions:", err)
			return
		}
		log.Println("All Disk Partitions:")
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "Mountpoint\tFstype")
		for _, part := range dl {
			fmt.Fprintf(w, "%s\t%s\n", part.Mountpoint, part.Fstype)
		}
		_ = w.Flush()
		diskList, err := monitoring.DiskList()
		if err != nil {
			log.Println("Failed to get disk list:", err)
			return
		}
		log.Println("Monitoring Mountpoints:", diskList)
		if mounts, merr := monitoring.DiskMountpoints(); merr == nil {
			for _, mp := range mounts {
				if limit, used, ok := monitoring.BtrfsQuotaFor(mp); ok {
					log.Printf("btrfs qgroup quota for %s: limit=%s used=%s (limit=%d used=%d bytes)", mp, humanBytes(limit), humanBytes(used), limit, used)
				}
			}
		}
		// 配额模式的扫描在后台进行：先触发一次，再等它出结果，这样这里显示的就是最终会上报的值
		if flags.DiskTotal != "" {
			_ = monitoring.Disk()
			monitoring.WaitForDiskScan(3 * time.Minute)
		}
		reported := monitoring.Disk()
		log.Printf("Reported disk: total=%s used=%s (total=%d used=%d bytes)", humanBytes(reported.Total), humanBytes(reported.Used), reported.Total, reported.Used)
	},
}

func init() {
	RootCmd.AddCommand(ListDiskCmd)
}

// humanBytes 以 1024 进制输出便于阅读的容量。
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
