package server

import (
	"testing"
	"time"
)

var testTargets = []struct {
	target string
}{
	{"v6-sh-cm.oojj.de"},
	{"2409:8c1e:8f80:2:6a::"},
	{"[2409:8c1e:8f80:2:6a::]:80"},
	{"v4-sh-cm.oojj.de"},
	{"117.185.125.154"},
	{"117.185.125.154:80"},
	{"https://github.com/komari-monitor/komari"},
}

func TestICMPPing(t *testing.T) {
	timeout := 3 * time.Second
	for _, tt := range testTargets {
		t.Run(tt.target, func(t *testing.T) {
			latency, err := icmpPing(tt.target, timeout)
			if latency < -1 {
				t.Errorf("ICMP ping %s: invalid latency %d", tt.target, latency)
			}
			if err != nil {
				t.Errorf("ICMP ping %s error: %v", tt.target, err)
			}
		})
	}
}

func TestTCPPing(t *testing.T) {
	timeout := 3 * time.Second
	for _, tt := range testTargets {
		t.Run(tt.target, func(t *testing.T) {
			latency, err := tcpPing(tt.target, timeout)
			if latency < -1 {
				t.Errorf("TCP ping %s: invalid latency %d", tt.target, latency)
			}
			if err != nil {
				t.Errorf("TCP ping %s error: %v", tt.target, err)
			}
		})
	}
}

func TestHTTPPing(t *testing.T) {
	timeout := 3 * time.Second
	for _, tt := range testTargets {
		t.Run(tt.target, func(t *testing.T) {
			latency, certsExpireTime, err := httpPing(tt.target, timeout)
			if latency < -1 {
				t.Errorf("HTTP ping %s: invalid latency %d", tt.target, latency)
			}
			if certsExpireTime < 0 {
				t.Errorf("HTTP ping %s: certs expire %d", tt.target, certsExpireTime)
			}
			if err != nil {
				t.Errorf("HTTP ping %s error: %v", tt.target, err)
			}
		})
	}
}
