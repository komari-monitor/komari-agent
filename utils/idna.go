package utils

import (
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

	// 转换主机名为 Punycode
	asciiHost, err := idna.ToASCII(parsedURL.Hostname())
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
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostname = host[:idx]
		port = host[idx:]
	} else {
		hostname = host
	}

	// 转换为 ASCII
	asciiHost, err := idna.ToASCII(hostname)
	if err != nil {
		return host, err
	}

	return asciiHost + port, nil
}
