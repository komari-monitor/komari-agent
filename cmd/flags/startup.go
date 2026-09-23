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
// been applied. No fields, credentials, false values or empty values are omitted.
func CaptureStartupConfig(extra map[string]interface{}) error {
	raw, err := json.Marshal(GlobalConfig)
	if err != nil {
		return err
	}
	var values map[string]interface{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
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
