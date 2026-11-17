package dnsresolver

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDialer 模拟网络：blackhole 中的地址一直挂起直到 ctx 结束（静默丢包），
// refused 中的地址立刻失败，其余地址立刻成功。
type fakeDialer struct {
	mu        sync.Mutex
	blackhole map[string]bool
	refused   map[string]bool
	attempts  []string
	open      []net.Conn
}

func (f *fakeDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)
	f.mu.Lock()
	f.attempts = append(f.attempts, host)
	f.mu.Unlock()
	if f.blackhole[host] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.refused[host] {
		return nil, errors.New("connection refused")
	}
	c1, c2 := net.Pipe()
	f.mu.Lock()
	f.open = append(f.open, c2)
	f.mu.Unlock()
	return c1, nil
}

func (f *fakeDialer) tried() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.attempts...)
}

func resetFamilyMemory(t *testing.T) {
	t.Helper()
	lastGoodFamily.Store("")
	t.Cleanup(func() { lastGoodFamily.Store("") })
}

func remoteHost(c net.Conn) string { return c.RemoteAddr().String() }

func TestDialIPsFallsBackToIPv6WhenIPv4Blackholed(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{blackhole: map[string]bool{"192.0.2.1": true}}
	start := time.Now()
	conn, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1", "2001:db8::1"}, 15*time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	defer conn.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fallback took %v, want well under the 15s timeout", elapsed)
	}
	if got := lastGoodFamily.Load(); got != "6" {
		t.Fatalf("lastGoodFamily = %v, want 6", got)
	}
}

func TestDialIPsDoesNotStartFallbackWhenPrimaryIsFast(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{}
	conn, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1", "2001:db8::1"}, 15*time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	conn.Close()
	time.Sleep(fallbackDelay + 100*time.Millisecond)
	if got := f.tried(); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("attempts = %v, want only the primary address", got)
	}
}

func TestDialIPsStartsFallbackImmediatelyWhenPrimaryRefused(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{refused: map[string]bool{"192.0.2.1": true}}
	start := time.Now()
	conn, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1", "2001:db8::1"}, 15*time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	conn.Close()
	if elapsed := time.Since(start); elapsed >= fallbackDelay {
		t.Fatalf("fallback waited %v after the primary failed, want immediate", elapsed)
	}
}

func TestDialIPsSplitsTimeoutWithinFamily(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{blackhole: map[string]bool{"192.0.2.1": true}}
	conn, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1", "192.0.2.2"}, 4*time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v, want the second IPv4 address to be reached", err)
	}
	conn.Close()
}

func TestDialIPsReportsAllErrors(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{refused: map[string]bool{"192.0.2.1": true, "2001:db8::1": true}}
	_, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1", "2001:db8::1"}, time.Second)
	if err == nil {
		t.Fatal("dialIPs() error = nil, want failure")
	}
	msg := err.Error()
	for _, want := range []string{"failed to dial to any of the resolved IPs", "192.0.2.1", "2001:db8::1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q", msg, want)
		}
	}
}

func TestDialIPsSingleFamilyDoesNotUpdateMemory(t *testing.T) {
	resetFamilyMemory(t)
	lastGoodFamily.Store("6")
	f := &fakeDialer{}
	conn, err := dialIPs(context.Background(), f.dial, "tcp", "443", []string{"192.0.2.1"}, time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	conn.Close()
	if got := lastGoodFamily.Load(); got != "6" {
		t.Fatalf("lastGoodFamily = %v, want unchanged 6", got)
	}
}

func TestDialIPsRespectsNetworkFamily(t *testing.T) {
	resetFamilyMemory(t)
	f := &fakeDialer{}
	conn, err := dialIPs(context.Background(), f.dial, "tcp6", "443", []string{"192.0.2.1", "2001:db8::1"}, time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	conn.Close()
	if got := f.tried(); len(got) != 1 || got[0] != "2001:db8::1" {
		t.Fatalf("attempts = %v, want only the IPv6 address", got)
	}
}

func TestOrderIPsUsesLastGoodFamilyOnlyWithoutExplicitPreference(t *testing.T) {
	resetFamilyMemory(t)
	lastGoodFamily.Store("6")

	ips := []string{"192.0.2.1", "2001:db8::1"}
	orderIPs(ips, "")
	if ips[0] != "2001:db8::1" {
		t.Fatalf("orderIPs(auto) = %v, want IPv6 first after an IPv6 success", ips)
	}

	ips = []string{"2001:db8::1", "192.0.2.1"}
	orderIPs(ips, "4")
	if ips[0] != "192.0.2.1" {
		t.Fatalf("orderIPs(4) = %v, want explicit preference to win", ips)
	}
}

func TestDialIPsAgainstRealListener(t *testing.T) {
	resetFamilyMemory(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	// IPv6 地址不可达（文档前缀），真实 IPv4 监听应在超时前胜出。
	conn, err := dialIPs(context.Background(), newBaseDialer().DialContext, "tcp", port, []string{"2001:db8::1", "127.0.0.1"}, 5*time.Second)
	if err != nil {
		t.Fatalf("dialIPs() error = %v", err)
	}
	defer conn.Close()
	if got := remoteHost(conn); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("connected to %s, want 127.0.0.1", got)
	}
}
