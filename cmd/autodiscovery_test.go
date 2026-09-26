package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withAutoDiscoveryFile 临时设置状态文件路径，测试结束后恢复全局配置
func withAutoDiscoveryFile(t *testing.T, path string) {
	t.Helper()
	old := flags.AutoDiscoveryFile
	flags.AutoDiscoveryFile = path
	t.Cleanup(func() { flags.AutoDiscoveryFile = old })
}

// 场景1：未指定路径时，与历史版本保持一致（可执行文件所在目录下的 auto-discovery.json）
func TestGetAutoDiscoveryFilePathDefault(t *testing.T) {
	withAutoDiscoveryFile(t, "")

	got := getAutoDiscoveryFilePath()
	if filepath.Base(got) != "auto-discovery.json" {
		t.Fatalf("default file name changed, got: %s", got)
	}
	if execPath, err := os.Executable(); err == nil {
		want := filepath.Join(filepath.Dir(execPath), "auto-discovery.json")
		if got != want {
			t.Fatalf("default path = %s, want %s", got, want)
		}
	}
}

// 场景2：指定自定义路径后，可以从该路径读取已有状态
func TestLoadAutoDiscoveryConfigFromCustomPath(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "data", "auto-discovery.json")
	if err := os.MkdirAll(filepath.Dir(custom), 0755); err != nil {
		t.Fatal(err)
	}
	existing := AutoDiscoveryConfig{UUID: "uuid-existing", Token: "token-existing"}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, data, 0644); err != nil {
		t.Fatal(err)
	}
	withAutoDiscoveryFile(t, custom)

	config, err := loadAutoDiscoveryConfig()
	if err != nil {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v", err)
	}
	if config == nil {
		t.Fatal("loadAutoDiscoveryConfig() = nil, want config")
	}
	if config.UUID != existing.UUID || config.Token != existing.Token {
		t.Fatalf("loaded config = %+v, want %+v", *config, existing)
	}
}

// 场景3：指定自定义路径且文件（含父目录）不存在时，保存可以正常创建并写入
func TestSaveAutoDiscoveryConfigCreatesFile(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "data", "auto-discovery.json") // 父目录不存在
	withAutoDiscoveryFile(t, custom)

	want := AutoDiscoveryConfig{UUID: "uuid-new", Token: "token-new"}
	if err := saveAutoDiscoveryConfig(&want); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	data, err := os.ReadFile(custom)
	if err != nil {
		t.Fatalf("state file was not created at %s: %v", custom, err)
	}
	var got AutoDiscoveryConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to parse saved state: %v", err)
	}
	if got.UUID != want.UUID || got.Token != want.Token {
		t.Fatalf("saved state = %+v, want %+v", got, want)
	}
}

// 场景4：模拟重启 —— 同一自定义路径先保存后加载，能够恢复已有状态
func TestAutoDiscoveryConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "data", "auto-discovery.json")
	withAutoDiscoveryFile(t, custom)

	want := AutoDiscoveryConfig{UUID: "uuid-restart", Token: "token-restart"}
	if err := saveAutoDiscoveryConfig(&want); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	// 模拟进程重启：重新加载同一持久化路径
	config, err := loadAutoDiscoveryConfig()
	if err != nil {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v", err)
	}
	if config == nil {
		t.Fatal("loadAutoDiscoveryConfig() = nil after restart, want restored config")
	}
	if config.UUID != want.UUID || config.Token != want.Token {
		t.Fatalf("restored config = %+v, want %+v", *config, want)
	}
}

// 场景5（补充）：默认路径下保存不改变历史行为
func TestSaveAutoDiscoveryConfigDefaultPath(t *testing.T) {
	withAutoDiscoveryFile(t, "")

	// 默认路径位于可执行文件所在目录，测试进程对其可写
	want := AutoDiscoveryConfig{UUID: "uuid-default", Token: "token-default"}
	if err := saveAutoDiscoveryConfig(&want); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}
	config, err := loadAutoDiscoveryConfig()
	if err != nil {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v", err)
	}
	if config == nil || config.UUID != want.UUID || config.Token != want.Token {
		t.Fatalf("restored config = %+v, want %+v", config, want)
	}
}
