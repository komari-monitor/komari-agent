package dnsresolver

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig
var (
	DNSServers = []string{
		"[2606:4700:4700::1111]:53", // Cloudflare IPv6
		"[2606:4700:4700::1001]:53", // Cloudflare IPv6 备用
		"[2001:4860:4860::8888]:53", // Google IPv6
		"[2001:4860:4860::8844]:53", // Google IPv6 备用

		"114.114.114.114:53", // 114DNS，中国大陆
		"1.1.1.1:53",         // Cloudflare IPv4
		"8.8.8.8:53",         // Google IPv4
		"8.8.4.4:53",         // Google IPv4 备用
		"223.5.5.5:53",       // 阿里DNS，中国大陆
		"119.29.29.29:53",    // DNSPod，中国大陆
	}

	// CustomDNSServer 自定义DNS服务器，可以通过命令行参数设置
	CustomDNSServer string

	preferV4Once sync.Once
	hasIPv4      bool
)

// SetCustomDNSServer 设置自定义DNS服务器
func SetCustomDNSServer(dnsServer string) {
	if dnsServer == "" {
		return
	}
	CustomDNSServer = normalizeDNSServer(dnsServer)
}

// normalizeDNSServer 将输入的 DNS 服务器字符串规范化为 host:port 形式：
// - IPv6 地址自动加方括号并补全端口 :53（若未提供）
// - IPv4/域名未提供端口时补全 :53
func normalizeDNSServer(s string) string {
	s = strings.TrimSpace(s)
	// 已是 [ipv6]:port 或 host:port 形式
	if (strings.HasPrefix(s, "[") && strings.Contains(s, "]:")) || (strings.Count(s, ":") == 1 && !strings.Contains(s, "]")) {
		return s
	}
	// 纯 IPv6（未加端口/括号）
	if strings.Count(s, ":") >= 2 && !strings.Contains(s, "]") {
		return "[" + s + "]:53"
	}
	// 其它情况：若未包含端口则补 53
	if !strings.Contains(s, ":") {
		return s + ":53"
	}
	return s
}

// getCurrentDNSServer 获取当前要使用的DNS服务器
func getCurrentDNSServer() string {
	if CustomDNSServer != "" {
		return CustomDNSServer
	}
	// 如果没有设置自定义DNS，返回空字符串，表示应使用系统默认解析器
	return ""
}

// GetCustomResolver 返回一个解析器：
// - 若设置了自定义 DNS：使用该服务器（并在失败时尝试内置列表作为兜底）。
// - 若未设置自定义 DNS：返回系统默认解析器（不使用内置列表）。
func GetCustomResolver() *net.Resolver {
	// 未设置自定义 DNS，直接使用系统默认解析器
	if getCurrentDNSServer() == "" {
		return net.DefaultResolver
	}

	// 设置了自定义 DNS，则构造使用自定义 DNS 的解析器
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second}

			// 优先使用自定义 DNS 服务器
			dnsServer := getCurrentDNSServer()
			if dnsServer != "" {
				if conn, err := d.DialContext(ctx, "udp", dnsServer); err == nil {
					return conn, nil
				}
			}
			log.Printf("Custom DNS server %s is unreachable, trying fallback servers", dnsServer)
			// 如果自定义DNS不可用，则尝试内置列表作为兜底
			for _, server := range DNSServers {
				if server == dnsServer {
					continue
				}
				if conn, err := d.DialContext(ctx, "udp", server); err == nil {
					return conn, nil
				}
			}

			return nil, fmt.Errorf("no available DNS server")
		},
	}
}

// GetHTTPClient 返回一个使用自定义DNS解析器的HTTP客户端
// buildTransport 构建带有自定义解析/拨号策略的 HTTP 传输层，可注入 TLS 配置
func buildTransport(timeout time.Duration, tlsConfig *tls.Config) *http.Transport {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	customResolver := GetCustomResolver()
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := customResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			// 根据本机是否具备 IPv4 动态排序
			preferIPv4 := preferIPv4First()
			sort.SliceStable(ips, func(i, j int) bool {
				ip1 := net.ParseIP(ips[i])
				ip2 := net.ParseIP(ips[j])
				if ip1 == nil || ip2 == nil {
					return false
				}
				if preferIPv4 {
					return ip1.To4() != nil && ip2.To4() == nil
				}
				// IPv6 优先
				return ip1.To4() == nil && ip2.To4() != nil
			})
			for _, ip := range ips {
				dialer := &net.Dialer{
					Timeout:   timeout,
					KeepAlive: 30 * time.Second,
					DualStack: true,
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("failed to dial to any of the resolved IPs")
		},
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
	}
}

func GetHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: buildTransport(timeout, &tls.Config{
			InsecureSkipVerify: flags.IgnoreUnsafeCert,
		}),
		Timeout: timeout,
	}
}

// GetNetDialer 返回一个使用自定义DNS解析器的网络拨号器
func GetNetDialer(timeout time.Duration) *net.Dialer {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Resolver:  GetCustomResolver(),
	}
}

// GetDialContext 返回一个自定义 DialContext：
// - 使用自定义解析器解析主机名
// - 优先尝试 IPv4，再尝试 IPv6
// - 逐个 IP 进行连接尝试，直到成功或全部失败
func GetDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	resolver := GetCustomResolver()

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		// 为解析设置一个带超时的子 context，避免整体拨号过快超时
		lookupCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		ips, err := resolver.LookupHost(lookupCtx, host)
		if err != nil {
			return nil, err
		}

		// 根据本机是否具备 IPv4 动态排序
		preferIPv4 := preferIPv4First()
		sort.SliceStable(ips, func(i, j int) bool {
			ip1 := net.ParseIP(ips[i])
			ip2 := net.ParseIP(ips[j])
			if ip1 == nil || ip2 == nil {
				return false
			}
			if preferIPv4 {
				return ip1.To4() != nil && ip2.To4() == nil
			}
			// IPv6 优先
			return ip1.To4() == nil && ip2.To4() != nil
		})

		// 逐个 IP 尝试连接
		for _, ip := range ips {
			d := &net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return c, nil
			}
		}
		return nil, fmt.Errorf("failed to dial to any of the resolved IPs")
	}
}

// preferIPv4First 检测本机是否存在可用的 IPv4 地址，若没有则在连接尝试中优先 IPv6
func preferIPv4First() bool {
	preferV4Once.Do(func() {
		ifaces, _ := net.Interfaces()
		for _, iface := range ifaces {
			if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.IsLoopback() {
					continue
				}
				if ip.To4() != nil {
					hasIPv4 = true
					return
				}
			}
		}
		hasIPv4 = false
	})
	return hasIPv4
}
