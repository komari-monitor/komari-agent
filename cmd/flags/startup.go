package flags_pkg

import (
	"encoding/json"
	"sync"
)

var startupConfig struct {
	sync.RWMutex
	values map[string]interface{}
}

// CaptureStartupConfig runs after all configuration sources and discovery have
// been applied. False values or empty values are not omitted; sensitive
// credentials (token / auto_discovery_key) are redacted before capture so the
// startup configuration query can never leak them back to the panel.
func CaptureStartupConfig(extra map[string]interface{}) error {
	raw, err := json.Marshal(GlobalConfig)
	if err != nil {
		return err
	}
	var values map[string]interface{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	// 凭据脱敏：token 是节点身份凭据、auto_discovery_key 是集群级注册密钥，
	// 回传给面板/前端会扩大泄漏半径，一律替换为占位符；month_rotate 等
	// 非敏感运行参数完整保留。
	for _, key := range []string{"token", "auto_discovery_key"} {
		if v, ok := values[key]; ok {
			if s, isStr := v.(string); isStr && s != "" {
				values[key] = "[REDACTED]"
			}
		}
	}
	for key, value := range extra {
		values[key] = value
	}
	startupConfig.Lock()
	startupConfig.values = values
	startupConfig.Unlock()
	return nil
}

func StartupConfig() map[string]interface{} {
	startupConfig.RLock()
	defer startupConfig.RUnlock()
	if startupConfig.values == nil {
		return nil
	}
	values := make(map[string]interface{}, len(startupConfig.values))
	for key, value := range startupConfig.values {
		values[key] = value
	}
	return values
}
