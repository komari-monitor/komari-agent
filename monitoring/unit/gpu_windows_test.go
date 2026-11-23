//go:build windows

package monitoring

import "testing"

func TestGpuNameFromAdaptersSkipsIndirectDisplay(t *testing.T) {
	// 实测：1 张 RTX 5880 + 网易 UU 远程、向日葵两个虚拟显示器，
	// DXGI 枚举出 3 个同名、不同 LUID 的 NVIDIA 适配器。
	gpus := []GPUInfo{
		{Name: "NVIDIA RTX 5880 Ada Generation", VendorId: 0x10DE, LUIDHighPart: 2, LUIDLowPart: 0x04824666, IndirectDisplay: true},
		{Name: "NVIDIA RTX 5880 Ada Generation", VendorId: 0x10DE, LUIDLowPart: 0x00020564, IndirectDisplay: true},
		{Name: "NVIDIA RTX 5880 Ada Generation", VendorId: 0x10DE, LUIDLowPart: 0x00014BF4},
		{Name: "Microsoft Basic Render Driver", VendorId: 0x1414, LUIDLowPart: 0x00015FDF, Flags: 2},
	}
	got := gpuNameFromAdapters(gpus)
	want := "NVIDIA RTX 5880 Ada Generation"
	if got != want {
		t.Fatalf("gpuNameFromAdapters() = %q, want %q", got, want)
	}
}
