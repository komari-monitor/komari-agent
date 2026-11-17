package dnsresolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

const (
	// fallbackDelay 是首选地址族开始拨号后，启动另一地址族的等待时间（RFC 8305 建议 250ms 左右）。
	fallbackDelay = 300 * time.Millisecond
	// minAttemptTimeout 是单个地址的最短拨号时间，避免地址较多时每个地址分到的时间过短。
	minAttemptTimeout = 2 * time.Second
	// maxConnectTimeout 限制建立 TCP 连接的总时长；超长的请求超时（如文件流）不应拖长拨号。
	maxConnectTimeout = 30 * time.Second
)

// lastGoodFamily 记录最近一次拨号成功的地址族（"4" 或 "6"），
// 未显式指定 --prefer-ip-version 时下次优先使用，避免每次重连都先撞已被阻断的地址族。
var lastGoodFamily atomic.Value

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func ipFamily(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.To4() != nil {
		return "4"
	}
	return "6"
}

// orderIPs 按显式偏好、上次成功的地址族、本机是否有 IPv4 的顺序决定拨号顺序。
func orderIPs(ips []string, preferIPVersion string) {
	preferIPVersion = normalizeIPVersionPreference(preferIPVersion)
	if preferIPVersion == "" {
		if v, ok := lastGoodFamily.Load().(string); ok {
			preferIPVersion = v
		}
	}
	sortIPsByPreference(ips, preferIPVersion)
}

// filterByNetwork 在调用方限定 tcp4/tcp6 时剔除另一地址族。
func filterByNetwork(ips []string, network string) []string {
	want := ""
	switch network {
	case "tcp4", "udp4":
		want = "4"
	case "tcp6", "udp6":
		want = "6"
	}
	if want == "" {
		return ips
	}
	out := ips[:0:0]
	for _, ip := range ips {
		if ipFamily(ip) == want {
			out = append(out, ip)
		}
	}
	return out
}

// dialIPs 以 Happy Eyeballs（RFC 8305）方式拨号已排序的 IP 列表：
// 首选地址族先拨，fallbackDelay 后或首选地址族全部失败时并行拨另一地址族，先连上者胜出。
// timeout 是整个拨号过程的总时限，同一地址族内的多个地址按剩余时间平分。
func dialIPs(ctx context.Context, dial dialFunc, network, port string, ips []string, timeout time.Duration) (net.Conn, error) {
	ips = filterByNetwork(ips, network)
	if len(ips) == 0 {
		return nil, errors.New("failed to dial to any of the resolved IPs: no usable address")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	primaryFamily := ipFamily(ips[0])
	var primary, fallback []string
	for _, ip := range ips {
		if ipFamily(ip) == primaryFamily {
			primary = append(primary, ip)
		} else {
			fallback = append(fallback, ip)
		}
	}

	if len(fallback) == 0 {
		conn, ip, err := dialSerial(ctx, dial, network, port, primary)
		return finishDial(conn, ip, err)
	}

	type result struct {
		conn    net.Conn
		ip      string
		err     error
		primary bool
	}
	raceCtx, cancelRace := context.WithCancel(ctx)
	defer cancelRace()
	results := make(chan result, 2)
	start := func(list []string, isPrimary bool) {
		go func() {
			conn, ip, err := dialSerial(raceCtx, dial, network, port, list)
			results <- result{conn: conn, ip: ip, err: err, primary: isPrimary}
		}()
	}

	start(primary, true)
	fallbackTimer := time.NewTimer(fallbackDelay)
	defer fallbackTimer.Stop()
	fallbackStarted := false
	pending := 1
	var errs []error

	for {
		select {
		case <-fallbackTimer.C:
			if !fallbackStarted {
				fallbackStarted = true
				pending++
				start(fallback, false)
			}
		case res := <-results:
			pending--
			if res.err == nil {
				cancelRace()
				// 另一路可能也已连上，需关闭以免泄漏。
				go func(n int) {
					for i := 0; i < n; i++ {
						if r := <-results; r.conn != nil {
							r.conn.Close()
						}
					}
				}(pending)
				return finishDial(res.conn, res.ip, nil)
			}
			errs = append(errs, res.err)
			if res.primary && !fallbackStarted {
				fallbackStarted = true
				fallbackTimer.Stop()
				pending++
				start(fallback, false)
			}
			if pending == 0 {
				return finishDial(nil, "", errors.Join(errs...))
			}
		}
	}
}

func finishDial(conn net.Conn, ip string, err error) (net.Conn, error) {
	if err != nil {
		return nil, fmt.Errorf("failed to dial to any of the resolved IPs: %w", err)
	}
	if family := ipFamily(ip); family != "" {
		lastGoodFamily.Store(family)
	}
	return conn, nil
}

// dialSerial 依次拨号同一地址族的地址，每次尝试分到剩余时间的均分（不少于 minAttemptTimeout）。
func dialSerial(ctx context.Context, dial dialFunc, network, port string, ips []string) (net.Conn, string, error) {
	var errs []error
	for i, ip := range ips {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		attemptCtx := ctx
		cancel := context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			per := remaining / time.Duration(len(ips)-i)
			if per < minAttemptTimeout {
				per = minAttemptTimeout
			}
			if per < remaining {
				attemptCtx, cancel = context.WithTimeout(ctx, per)
			}
		}
		conn, err := dial(attemptCtx, network, net.JoinHostPort(ip, port))
		cancel()
		if err == nil {
			return conn, ip, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", ip, err))
	}
	return nil, "", errors.Join(errs...)
}

func connectTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > maxConnectTimeout {
		return maxConnectTimeout
	}
	return timeout
}

func newBaseDialer() *net.Dialer {
	return &net.Dialer{KeepAlive: 30 * time.Second}
}
