package cmd

import (
	"log"

	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/spf13/cobra"
)

var CheckMemCmd = &cobra.Command{
	Use:   "check-mem",
	Short: "Check memory usage",
	Long:  `Check memory usage`,
	Run: func(cmd *cobra.Command, args []string) {
		log.Println("--- Memory Check ---")

		// Print raw /proc/meminfo if on Linux
		if info, err := monitoring.ReadProcMeminfo(); err == nil {
			log.Println("--- /proc/meminfo ---")
			log.Printf("MemTotal:     %d MiB", info.MemTotal/1024/1024)
			log.Printf("MemFree:      %d MiB", info.MemFree/1024/1024)
			log.Printf("MemAvailable: %d MiB", info.MemAvailable/1024/1024)
			log.Printf("Buffers:      %d MiB", info.Buffers/1024/1024)
			log.Printf("Cached:       %d MiB", info.Cached/1024/1024)
			log.Printf("SwapTotal:    %d MiB", info.SwapTotal/1024/1024)
			log.Printf("SwapFree:     %d MiB", info.SwapFree/1024/1024)
			log.Printf("SwapCached:   %d MiB", info.SwapCached/1024/1024)
			log.Printf("Shmem:        %d MiB", info.Shmem/1024/1024)
			log.Printf("SReclaimable: %d MiB", info.SReclaimable/1024/1024)
			log.Printf("Zswap:        %d MiB", info.Zswap/1024/1024)
			log.Printf("Zswapped:     %d MiB", info.Zswapped/1024/1024)
			log.Println("---------------------")
		}

		printRamInfo := func(info monitoring.RamInfo) {
			log.Printf("[%s] Total: %d bytes (%d MiB), Used: %d bytes (%d MiB)",
				info.Mode,
				info.Total, info.Total/(1024*1024),
				info.Used, info.Used/(1024*1024),
			)
		}

		printRamInfo(monitoring.GetMemHtopLike())
		printRamInfo(monitoring.GetMemGopsutil())
		printRamInfo(monitoring.CallFree())

		log.Println("--- Current Configured ---")
		printRamInfo(monitoring.Ram())
	},
}

func init() {
	RootCmd.AddCommand(CheckMemCmd)
}
