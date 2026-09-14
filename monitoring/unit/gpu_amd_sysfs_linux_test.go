//go:build linux

package monitoring

import "testing"

func TestAMDSysfsDetailedInfo(t *testing.T) {
	infos, err := getAMDSysfsDetailedInfo()
	if err != nil {
		t.Skipf("no amdgpu sysfs metrics on this host: %v", err)
	}
	if len(infos) == 0 {
		t.Skip("no amdgpu sysfs metrics on this host")
	}
	for i, info := range infos {
		t.Logf("gpu[%d] name=%s util=%.1f mem=%d/%d temp=%d", i, info.Name, info.Utilization, info.MemoryUsed, info.MemoryTotal, info.Temperature)
		if info.Name == "" {
			t.Errorf("gpu[%d] missing name", i)
		}
		if info.MemoryTotal == 0 {
			t.Errorf("gpu[%d] missing vram total", i)
		}
	}
}
