package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/komari-monitor/komari-agent/dnsresolver"
	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/komari-monitor/komari-agent/protocol/transport"
	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
	"github.com/komari-monitor/komari-agent/update"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

func DoUploadBasicInfoWorks() {
	ticker := time.NewTicker(time.Duration(flags.InfoReportInterval) * time.Minute)
	for range ticker.C {
		err := uploadBasicInfo()
		if err != nil {
			log.Println("Error uploading basic info:", err)
		}
	}
}
func UpdateBasicInfo() {
	err := uploadBasicInfo()
	if err != nil {
		log.Println("Error uploading basic info:", err)
	} else {
		log.Println("Basic info uploaded successfully")
	}
}
func uploadBasicInfo() error {
	cpu := monitoring.Cpu()

	osname := monitoring.OSName()
	kernelVersion := monitoring.KernelVersion()
	ipv4, ipv6, _ := monitoring.GetIPAddress()

	data := map[string]interface{}{
		"cpu_name":           cpu.CPUName,
		"cpu_cores":          cpu.CPUCores,
		"cpu_physical_cores": cpu.CPUPhysicalCores,
		"arch":               cpu.CPUArchitecture,
		"os":                 osname,
		"kernel_version":     kernelVersion,
		"ipv4":               ipv4,
		"ipv6":               ipv6,
		"mem_total":          monitoring.Ram().Total,
		"swap_total":         monitoring.Swap().Total,
		"disk_total":         monitoring.Disk().Total,
		"gpu_name":           monitoring.GpuName(),
		"virtualization":     monitoring.Virtualized(),
		"version":            update.CurrentVersion,
	}

	// 尝试上传完整数据
	err := tryUploadData(data)
	if err != nil {
		// 兼容 <= 1.0.2
		delete(data, "kernel_version")
		// 兼容 <= 1.2.0
		delete(data, "cpu_physical_cores")
		err = tryUploadData(data)
		if err != nil {
			return err
		}
	}
	return nil
}

func tryUploadData(data map[string]interface{}) error {
	protocolVersion := uploadProtocolVersion()
	if protocolVersion >= 2 {
		err := tryUploadDataWithProtocol(data, 2)
		if shouldFallbackToV1(2, err) {
			log.Printf("v2 basic info failed %d consecutive protocol attempts, falling back to v1", v2ProtocolFallbackThreshold)
			setConnectionProtocolVersion(1)
			return tryUploadDataWithProtocol(data, 1)
		}
		return err
	}
	return tryUploadDataWithProtocol(data, 1)
}

func tryUploadDataWithProtocol(data map[string]interface{}, protocolVersion int) error {
	endpoint := strings.TrimSuffix(flags.Endpoint, "/") + "/api/clients/uploadBasicInfo?token=" + flags.Token
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if protocolVersion >= 2 {
		endpoint = strings.TrimSuffix(flags.Endpoint, "/") + "/api/clients/v2/rpc?token=" + flags.Token
		payload = v2.BuildBasicInfoPayload(data)
	}
	body := payload
	compressed := false
	if protocolVersion >= 2 && !flags.DisableCompression {
		if gz, err := transport.GzipBytes(payload); err == nil {
			body = gz
			compressed = true
		}
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
	}

	// 添加Cloudflare Access头部
	if flags.CFAccessClientID != "" && flags.CFAccessClientSecret != "" {
		req.Header.Set("CF-Access-Client-Id", flags.CFAccessClientID)
		req.Header.Set("CF-Access-Client-Secret", flags.CFAccessClientSecret)
	}

	client := dnsresolver.GetHTTPClientWithPreference(30*time.Second, flags.PreferIPVersion)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	message := string(respBody)

	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: message}
	}
	if protocolVersion >= 2 {
		if len(bytes.TrimSpace(respBody)) > 0 {
			if _, err := parseV2Response(respBody); err != nil {
				return err
			}
		}
		resetV2ProtocolFailures(protocolVersion)
	}

	return nil
}
