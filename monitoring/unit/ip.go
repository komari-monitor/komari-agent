package monitoring

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/komari-monitor/komari-agent/dnsresolver"
)

var (
	// 创建适用于IPv4和IPv6的HTTP客户端
	ipv4HTTPClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := dnsresolver.GetNetDialer(15 * time.Second)
				return dialer.DialContext(ctx, "tcp4", addr) // 锁v4防止出现问题
			},
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		Timeout: 15 * time.Second,
	}
	ipv6HTTPClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := dnsresolver.GetNetDialer(15 * time.Second)
				return dialer.DialContext(ctx, "tcp6", addr) // 锁v6防止出现问题
			},
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		Timeout: 15 * time.Second,
	}
	userAgent = "curl/8.0.1"
)

func GetIPv4Address() (string, error) {

	webAPIs := []string{
		"https://www.visa.cn/cdn-cgi/trace",
		"https://www.qualcomm.cn/cdn-cgi/trace",
		"https://www.toutiao.com/stream/widget/local_weather/data/",
		"https://edge-ip.html.zone/geo",
		"https://vercel-ip.html.zone/geo",
		"http://ipv4.ip.sb",
		"https://api.ipify.org?format=json",
	}

	for _, api := range webAPIs {
		// get ipv4
		req, err := http.NewRequest("GET", api, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := ipv4HTTPClient.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close() // 获取后立即关闭防止堵塞
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
		ipv4 := re.FindString(string(body))
		if ipv4 != "" {
			log.Printf("Get IPV4 Success: %s", ipv4)
			return ipv4, nil
		}
	}
	return "", nil
}

func GetIPv6Address() (string, error) {

	webAPIs := []string{
		"https://v6.ip.zxinc.org/info.php?type=json",
		"https://api6.ipify.org?format=json",
		"https://ipv6.icanhazip.com",
		"https://api-ipv6.ip.sb/geoip",
	}

	for _, api := range webAPIs {
		// get ipv6
		req, err := http.NewRequest("GET", api, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := ipv6HTTPClient.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close() // 获取后立即关闭防止堵塞
		if err != nil {
			continue
		}

		// 使用正则表达式从响应体中提取IPv6地址
		re := regexp.MustCompile(`(([0-9A-Fa-f]{1,4}:){7})([0-9A-Fa-f]{1,4})|(([0-9A-Fa-f]{1,4}:){1,6}:)(([0-9A-Fa-f]{1,4}:){0,4})([0-9A-Fa-f]{0,4})`)
		ipv6 := re.FindString(string(body))
		if ipv6 != "" {
			log.Printf("Get IPV6 Success:  %s", ipv6)
			return ipv6, nil
		}
	}
	return "", nil
}

func GetIPAddress() (ipv4, ipv6 string, err error) {

	if flags.GetIpAddrFromNic {
		allowNics, err := InterfaceList()
		if err != nil {
			log.Printf("Get Interface List Error: %v", err)
		} else {
			ipv4, ipv6 = getIPFromInterfaces(allowNics)
			if ipv4 != "" || ipv6 != "" {
				log.Printf("Get IP from NIC - IPv4: %s, IPv6: %s", ipv4, ipv6)
				return ipv4, ipv6, nil
			}
		}
	}

	if flags.CustomIpv4 != "" {
		ipv4 = flags.CustomIpv4
	} else {
		ipv4, err = GetIPv4Address()
		if err != nil {
			log.Printf("Get IPV4 Error: %v", err)
			ipv4 = ""
		}
	}
	if flags.CustomIpv6 != "" {
		ipv6 = flags.CustomIpv6
	} else {
		ipv6, err = GetIPv6Address()
		if err != nil {
			log.Printf("Get IPV6 Error: %v", err)
			ipv6 = ""
		}
	}

	return ipv4, ipv6, nil
}

// getIPFromInterfaces 从指定的网卡接口获取 IPv4 和 IPv6 地址
func getIPFromInterfaces(nicNames []string) (ipv4, ipv6 string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		log.Printf("Failed to get network interfaces: %v", err)
		return "", ""
	}
	for _, iface := range interfaces {
		// 检查接口是否在允许列表中
		if !func(slice []string, item string) bool {
			for _, s := range slice {
				if s == item {
					return true
				}
			}
			return false
		}(nicNames, iface.Name) {
			continue
		}

		// 跳过未启动的接口
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil || ip.IsLoopback() {
				continue
			}

			// 获取 IPv4 地址
			if ipv4 == "" && ip.To4() != nil {
				ipv4 = ip.String()
			}

			// 获取 IPv6 地址（排除链路本地地址）
			if ipv6 == "" && ip.To4() == nil && !ip.IsLinkLocalUnicast() {
				ipv6 = ip.String()
			}

			// 如果已经找到 IPv4 和 IPv6,提前返回
			if ipv4 != "" && ipv6 != "" {
				return ipv4, ipv6
			}
		}
	}

	return ipv4, ipv6
}
