package flags

var (
	AutoDiscoveryKey     string
	DisableAutoUpdate    bool
	DisableWebSsh        bool
	MemoryModeAvailable  bool
	Token                string
	Endpoint             string
	Interval             float64
	IgnoreUnsafeCert     bool
	MaxRetries           int
	ReconnectInterval    int
	InfoReportInterval   int
	IncludeNics          string
	ExcludeNics          string
	IncludeMountpoints   string
	MonthRotate          int
	CFAccessClientID     string
	CFAccessClientSecret string
	MemoryIncludeCache   bool
	CustomDNS            string
	EnableGPU            bool // 启用详细GPU监控
	ShowWarning          bool // Windows 上显示安全警告，作为子进程运行一次
	CustomIpv4           string
	CustomIpv6           string
	GetIpAddrFromNic     bool // 从网卡获取IP地址
)
