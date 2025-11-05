package utils

import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// ConvertIDNToASCII 将包含国际化域名(IDN)的 URL 转换为 ASCII 兼容编码(ACE)格式
// 例如: "https://中文域名.com" -> "https://xn--fiq228c.com"
func ConvertIDNToASCII(urlStr string) (string, error) {
	// 解析 URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return urlStr, err
	}

	hostname := parsedURL.Hostname()

	// 检查是否为 IP 地址(IPv4 或 IPv6),如果是则不需要转换
	if net.ParseIP(hostname) != nil {
		return parsedURL.String(), nil
	}

	// 转换主机名为 Punycode
	asciiHost, err := idna.ToASCII(hostname)
	if err != nil {
		return urlStr, err
	}

	// 如果有端口,需要保留
	if parsedURL.Port() != "" {
		parsedURL.Host = asciiHost + ":" + parsedURL.Port()
	} else {
		parsedURL.Host = asciiHost
	}

	return parsedURL.String(), nil
}

// ConvertHostToASCII 将主机名(可能包含端口)转换为 ASCII 兼容编码格式
// 例如: "中文域名.com:8080" -> "xn--fiq228c.com:8080"
func ConvertHostToASCII(host string) (string, error) {
	// 分离主机名和端口
	var hostname, port string

	// 处理 IPv6 地址格式 [::1]:port
	if strings.HasPrefix(host, "[") {
		if idx := strings.LastIndex(host, "]"); idx != -1 {
			hostname = host[1:idx] // 去掉方括号
			if len(host) > idx+1 {
				port = host[idx+1:] // 包含冒号
			}
		} else {
			hostname = host
		}
	} else if idx := strings.LastIndex(host, ":"); idx != -1 {
		// 检查是否为 IPv6 地址(包含多个冒号)
		if strings.Count(host, ":") > 1 {
			// 可能是不带方括号的 IPv6 地址
			hostname = host
		} else {
			// IPv4:port 格式
			hostname = host[:idx]
			port = host[idx:]
		}
	} else {
		hostname = host
	}

	// 检查是否为 IP 地址,如果是则不需要转换
	if net.ParseIP(hostname) != nil {
		return host, nil
	}

	// 转换为 ASCII
	asciiHost, err := idna.ToASCII(hostname)
	if err != nil {
		return host, err
	}

	// 重新组装,如果原始输入有方括号则保留
	if strings.HasPrefix(host, "[") && port != "" {
		return "[" + asciiHost + "]" + port, nil
	}
	return asciiHost + port, nil
}
