package flags_pkg

type Config struct {
	AutoDiscoveryKey     string  `json:"auto_discovery_key" env:"AGENT_AUTO_DISCOVERY_KEY"`           // 自动发现密钥
	DisableAutoUpdate    bool    `json:"disable_auto_update" env:"AGENT_DISABLE_AUTO_UPDATE"`         // 禁用自动更新
	DisableWebSsh        bool    `json:"disable_web_ssh" env:"AGENT_DISABLE_WEB_SSH"`                 // 禁用远程控制（web ssh 和 rce）
	MemoryModeAvailable  bool    `json:"memory_mode_available" env:"AGENT_MEMORY_MODE_AVAILABLE"`     // [deprecated] 已弃用，请使用 MemoryIncludeCache
	Token                string  `json:"token" env:"AGENT_TOKEN"`                                     // Token
	Endpoint             string  `json:"endpoint" env:"AGENT_ENDPOINT"`                               // 面板地址
	Interval             float64 `json:"interval" env:"AGENT_INTERVAL"`                               // 数据采集间隔，单位秒
	IgnoreUnsafeCert     bool    `json:"ignore_unsafe_cert" env:"AGENT_IGNORE_UNSAFE_CERT"`           // 忽略不安全的证书
	MaxRetries           int     `json:"max_retries" env:"AGENT_MAX_RETRIES"`                         // 最大重试次数
	ReconnectInterval    int     `json:"reconnect_interval" env:"AGENT_RECONNECT_INTERVAL"`           // 重连间隔，单位秒
	InfoReportInterval   int     `json:"info_report_interval" env:"AGENT_INFO_REPORT_INTERVAL"`       // 基础信息上报间隔，单位分钟
	IncludeNics          string  `json:"include_nics" env:"AGENT_INCLUDE_NICS"`                       // 仅统计网卡，逗号分隔的网卡名称列表，支持通配符
	ExcludeNics          string  `json:"exclude_nics" env:"AGENT_EXCLUDE_NICS"`                       // 统计时排除的网卡，逗号分隔的网卡名称列表，支持通配符
	IncludeMountpoints   string  `json:"include_mountpoints" env:"AGENT_INCLUDE_MOUNTPOINTS"`         // 磁盘统计的包含挂载点列表，使用分号分隔
	MonthRotate          int     `json:"month_rotate" env:"AGENT_MONTH_ROTATE"`                       // 流量统计的月份重置日期（0表示禁用）
	CFAccessClientID     string  `json:"cf_access_client_id" env:"AGENT_CF_ACCESS_CLIENT_ID"`         // Cloudflare Access Client ID
	CFAccessClientSecret string  `json:"cf_access_client_secret" env:"AGENT_CF_ACCESS_CLIENT_SECRET"` // Cloudflare Access Client Secret
	MemoryIncludeCache   bool    `json:"memory_include_cache" env:"AGENT_MEMORY_INCLUDE_CACHE"`       // 包括缓存/缓冲区的内存使用情况
	CustomDNS            string  `json:"custom_dns" env:"AGENT_CUSTOM_DNS"`                           // 使用的自定义DNS服务器
	EnableGPU            bool    `json:"enable_gpu" env:"AGENT_ENABLE_GPU"`                           // 启用详细GPU监控
	ShowWarning          bool    `json:"show_warning" env:"AGENT_SHOW_WARNING"`                       // Windows 上显示安全警告，作为子进程运行一次
	CustomIpv4           string  `json:"custom_ipv4" env:"AGENT_CUSTOM_IPV4"`                         // 自定义 IPv4 地址
	CustomIpv6           string  `json:"custom_ipv6" env:"AGENT_CUSTOM_IPV6"`                         // 自定义 IPv6 地址
	GetIpAddrFromNic     bool    `json:"get_ip_addr_from_nic" env:"AGENT_GET_IP_ADDR_FROM_NIC"`       // 从网卡获取IP地址
	ConfigFile           string  `json:"config_file" env:"AGENT_CONFIG_FILE"`                         // JSON配置文件路径
}

var GlobalConfig = &Config{}
