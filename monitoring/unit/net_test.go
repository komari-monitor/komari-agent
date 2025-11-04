package monitoring

import (
	"testing"
)

func TestConnectionsCount(t *testing.T) {
	tcpCount, udpCount, err := ConnectionsCount()
	if err != nil {
		t.Fatalf("ConnectionsCount failed: %v", err)
	}

	if tcpCount < 0 {
		t.Errorf("Expected non-negative TCP count, got %d", tcpCount)
	}

	if udpCount < 0 {
		t.Errorf("Expected non-negative UDP count, got %d", udpCount)
	}

	t.Logf("TCP connections: %d, UDP connections: %d", tcpCount, udpCount)
}

func TestParseNics(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]struct{}
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:  "single nic",
			input: "eth0",
			expected: map[string]struct{}{
				"eth0": {},
			},
		},
		{
			name:  "multiple nics",
			input: "eth0,wlan0,enp0s3",
			expected: map[string]struct{}{
				"eth0":   {},
				"wlan0":  {},
				"enp0s3": {},
			},
		},
		{
			name:  "nics with spaces",
			input: " eth0 , wlan0 , enp0s3 ",
			expected: map[string]struct{}{
				"eth0":   {},
				"wlan0":  {},
				"enp0s3": {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNics(tt.input)

			if tt.expected == nil && result != nil {
				t.Errorf("Expected nil, got %v", result)
				return
			}

			if tt.expected != nil && result == nil {
				t.Errorf("Expected %v, got nil", tt.expected)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d items, got %d", len(tt.expected), len(result))
				return
			}

			for key := range tt.expected {
				if _, exists := result[key]; !exists {
					t.Errorf("Expected key %s not found in result", key)
				}
			}
		})
	}
}

func TestShouldInclude(t *testing.T) {
	tests := []struct {
		name        string
		nicName     string
		includeNics map[string]struct{}
		excludeNics map[string]struct{}
		expected    bool
	}{
		{
			name:        "loopback interface should be excluded",
			nicName:     "lo",
			includeNics: nil,
			excludeNics: nil,
			expected:    false,
		},
		{
			name:        "docker interface should be excluded",
			nicName:     "docker0",
			includeNics: nil,
			excludeNics: nil,
			expected:    false,
		},
		{
			name:        "normal interface with no filters",
			nicName:     "eth0",
			includeNics: nil,
			excludeNics: nil,
			expected:    true,
		},
		{
			name:    "interface in include list",
			nicName: "eth0",
			includeNics: map[string]struct{}{
				"eth0": {},
			},
			excludeNics: nil,
			expected:    true,
		},
		{
			name:    "interface not in include list",
			nicName: "wlan0",
			includeNics: map[string]struct{}{
				"eth0": {},
			},
			excludeNics: nil,
			expected:    false,
		},
		{
			name:        "interface in exclude list",
			nicName:     "eth0",
			includeNics: nil,
			excludeNics: map[string]struct{}{
				"eth0": {},
			},
			expected: false,
		},
		{
			name:        "interface not in exclude list",
			nicName:     "wlan0",
			includeNics: nil,
			excludeNics: map[string]struct{}{
				"eth0": {},
			},
			expected: true,
		},
		{
			name:    "loopback in include list should still be excluded",
			nicName: "lo",
			includeNics: map[string]struct{}{
				"lo": {},
			},
			excludeNics: nil,
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldInclude(tt.nicName, tt.includeNics, tt.excludeNics)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestNetworkSpeedFallback(t *testing.T) {
	// 测试回退方法
	includeNics := map[string]struct{}{}
	excludeNics := map[string]struct{}{}

	totalUp, totalDown, upSpeed, downSpeed, err := getNetworkSpeedFallback(includeNics, excludeNics)
	if err != nil {
		t.Fatalf("getNetworkSpeedFallback failed: %v", err)
	}

	t.Logf("TotalUp: %d, TotalDown: %d, UpSpeed: %d/s, DownSpeed: %d/s",
		totalUp, totalDown, upSpeed, downSpeed)
}

func TestNetworkSpeedWithoutMonthRotate(t *testing.T) {

	flags.MonthRotate = 1

	// 设置测试值
	flags.IncludeNics = ""
	flags.ExcludeNics = ""

	totalUp, totalDown, upSpeed, downSpeed, err := NetworkSpeed()
	if err != nil {
		t.Fatalf("NetworkSpeed failed: %v", err)
	}

	t.Logf("Without MonthRotate - TotalUp: %d, TotalDown: %d, UpSpeed: %d/s, DownSpeed: %d/s",
		totalUp, totalDown, upSpeed, downSpeed)
}

func TestNetworkSpeedWithMonthRotate(t *testing.T) {
	// 保存原始值
	originalMonthRotate := flags.MonthRotate
	originalIncludeNics := flags.IncludeNics
	originalExcludeNics := flags.ExcludeNics

	// 恢复原始值
	defer func() {
		flags.MonthRotate = originalMonthRotate
		flags.IncludeNics = originalIncludeNics
		flags.ExcludeNics = originalExcludeNics
	}()

	// 设置测试值 - 启用月重置
	flags.MonthRotate = 1
	flags.IncludeNics = ""
	flags.ExcludeNics = ""

	totalUp, totalDown, upSpeed, downSpeed, err := NetworkSpeed()

	// 如果vnstat不可用，可能会回退到原来的方法，这是正常的
	if err != nil {
		t.Fatalf("NetworkSpeed failed: %v", err)
	}

	t.Logf("With MonthRotate - TotalUp: %d, TotalDown: %d, UpSpeed: %d/s, DownSpeed: %d/s",
		totalUp, totalDown, upSpeed, downSpeed)
}

func TestNetworkSpeedWithNicFilters(t *testing.T) {
	// 保存原始值
	originalMonthRotate := flags.MonthRotate
	originalIncludeNics := flags.IncludeNics
	originalExcludeNics := flags.ExcludeNics

	// 恢复原始值
	defer func() {
		flags.MonthRotate = originalMonthRotate
		flags.IncludeNics = originalIncludeNics
		flags.ExcludeNics = originalExcludeNics
	}()

	// 测试排除回环接口
	flags.MonthRotate = 0
	flags.IncludeNics = ""
	flags.ExcludeNics = "lo,docker0"

	totalUp, totalDown, upSpeed, downSpeed, err := NetworkSpeed()
	if err != nil {
		t.Fatalf("NetworkSpeed with excludeNics failed: %v", err)
	}

	t.Logf("With excludeNics - TotalUp: %d, TotalDown: %d, UpSpeed: %d/s, DownSpeed: %d/s",
		totalUp, totalDown, upSpeed, downSpeed)
}
