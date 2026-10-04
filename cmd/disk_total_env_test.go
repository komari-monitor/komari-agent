package cmd

import (
	"encoding/json"
	"testing"
)

// 新配置项必须能通过环境变量和 JSON 配置文件加载（与其它参数一致）。
func TestDiskTotalLoadedFromEnvAndJSON(t *testing.T) {
	old := flags.DiskTotal
	defer func() { flags.DiskTotal = old }()

	flags.DiskTotal = ""
	t.Setenv("AGENT_DISK_TOTAL", "512M")
	loadFromEnv()
	if flags.DiskTotal != "512M" {
		t.Errorf("env AGENT_DISK_TOTAL: got %q, want %q", flags.DiskTotal, "512M")
	}

	flags.DiskTotal = ""
	if err := json.Unmarshal([]byte(`{"disk_total":"2G"}`), flags); err != nil {
		t.Fatal(err)
	}
	if flags.DiskTotal != "2G" {
		t.Errorf("json disk_total: got %q, want %q", flags.DiskTotal, "2G")
	}
}
