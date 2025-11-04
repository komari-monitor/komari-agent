package monitoring

import (
	"runtime"

	"github.com/shirou/gopsutil/v4/mem"
)

type RamInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

func Ram() RamInfo {
	raminfo := RamInfo{}
	v, err := mem.VirtualMemory()
	if err != nil {
		raminfo.Total = 0
		raminfo.Used = 0
		return raminfo
	}
	if flags.MemoryIncludeCache {
		raminfo.Total = v.Total
		raminfo.Used = v.Total - v.Free
		return raminfo
	}
	raminfo.Total = v.Total
	if runtime.GOOS == "windows" {
		raminfo.Used = v.Total - v.Available
	} else {
		raminfo.Used = v.Total - v.Free - v.Buffers - v.Cached
	}

	return raminfo
}
func Swap() RamInfo {
	swapinfo := RamInfo{}
	s, err := mem.SwapMemory()
	if err != nil {
		swapinfo.Total = 0
		swapinfo.Used = 0
	} else {
		swapinfo.Total = s.Total
		swapinfo.Used = s.Used
	}
	return swapinfo
}
