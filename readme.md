# komari-agent

## 配置方式

agent 参数可以通过命令行参数、环境变量或 JSON 配置文件传入。

最小启动示例：

```bash
./komari-agent --endpoint "https://example.com" --token "your-token"
```

使用环境变量：

```bash
export AGENT_ENDPOINT="https://example.com"
export AGENT_TOKEN="your-token"
./komari-agent
```

使用 JSON 配置文件：

```bash
./komari-agent --config ./config.json
```

`config.json` 示例：

```json
{
  "endpoint": "https://example.com",
  "token": "your-token",
  "interval": 3,
  "disable_auto_update": false,
  "disable_web_ssh": false,
  "ignore_unsafe_cert": false
}
```

配置优先级从低到高为：默认值、命令行参数、环境变量、JSON 配置文件。

常用配置项：

表中支持版本表示该参数本身首次在发布 tag 中出现；环境变量和 JSON 配置文件方式从 `1.1.33` 起支持，早于最早 tag 的参数记为 `0.0.9`。

| JSON 字段 | 环境变量 | 命令行参数 | 说明 | 支持版本 |
| --- | --- | --- | --- | --- |
| `endpoint` | `AGENT_ENDPOINT` | `--endpoint`, `-e` | 面板地址 | `0.0.9` |
| `token` | `AGENT_TOKEN` | `--token`, `-t` | agent token | `0.0.9` |
| `interval` | `AGENT_INTERVAL` | `--interval`, `-i` | 数据采集间隔，单位秒 | `0.0.9` |
| `disable_auto_update` | `AGENT_DISABLE_AUTO_UPDATE` | `--disable-auto-update` | 禁用自动更新 | `0.0.9` |
| `disable_web_ssh` | `AGENT_DISABLE_WEB_SSH` | `--disable-web-ssh` | 禁用远程控制 | `0.0.9` |
| `ignore_unsafe_cert` | `AGENT_IGNORE_UNSAFE_CERT` | `--ignore-unsafe-cert`, `-u` | 忽略不安全证书 | `0.0.9` |
| `include_nics` | `AGENT_INCLUDE_NICS` | `--include-nics` | 仅统计指定网卡，逗号分隔 | `0.0.22` |
| `exclude_nics` | `AGENT_EXCLUDE_NICS` | `--exclude-nics` | 排除指定网卡，逗号分隔 | `0.0.22` |
| `include_mountpoints` | `AGENT_INCLUDE_MOUNTPOINTS` | `--include-mountpoint` | 仅统计指定挂载点，分号分隔 | `0.1.0` |
| `month_rotate` | `AGENT_MONTH_ROTATE` | `--month-rotate` | 流量统计每月重置日期，`0` 为禁用 | `0.1.0` |
| `auto_discovery_key` | `AGENT_AUTO_DISCOVERY_KEY` | `--auto-discovery` | 自动发现密钥 | `1.0.40` |
| `auto_discovery_file` | `AGENT_AUTO_DISCOVERY_FILE` | `--auto-discovery-file` | 自动发现状态文件路径，默认为可执行文件所在目录下的 `auto-discovery.json` | 未发布 |
| `custom_dns` | `AGENT_CUSTOM_DNS` | `--custom-dns` | 自定义 DNS 服务器 | `1.0.80` |
| `enable_gpu` | `AGENT_ENABLE_GPU` | `--gpu` | 启用详细 GPU 监控 | `1.0.80` |
| `disable_compression` | `AGENT_DISABLE_COMPRESSION` | `--disable-compression` | 禁用 v2 传输压缩 | `1.2.10` |
| `prefer_ip_version` | `AGENT_PREFER_IP_VERSION` | `--prefer-ip-version` | 优先使用 IP 版本，可选 `4` 或 `6` | 未发布 |

完整参数可运行：

```bash
./komari-agent --help
```

详见 `cmd/flags/flags.go` 及 `cmd/root.go`

## 自动发现（Auto Discovery）

使用 `--auto-discovery`（或 `AGENT_AUTO_DISCOVERY_KEY`）时，agent 会用密钥向面板注册，并将获得的 UUID / Token 保存到状态文件中，下次启动直接复用，无需重复注册。

状态文件默认保存在可执行文件所在目录下的 `auto-discovery.json`。如果希望保存到其它位置（例如容器内挂载的目录），可以通过 `AGENT_AUTO_DISCOVERY_FILE`（或 `--auto-discovery-file`）指定，文件不存在时 agent 会自动创建（含父目录）。

### Docker 部署示例

```yaml
services:
  komari-agent:
    image: ghcr.io/komari-monitor/komari-agent:latest
    container_name: komari-agent
    restart: always
    environment:
      AGENT_ENDPOINT: https://example.com
      AGENT_AUTO_DISCOVERY_KEY: example-key
      AGENT_AUTO_DISCOVERY_FILE: /data/auto-discovery.json
    volumes:
      - /etc/os-release:/etc/os-release:ro
      - ./data:/data
```

由于状态文件保存在挂载的 `./data` 目录中，直接 `docker compose up -d` 即可，agent 首次运行时会自动创建 `auto-discovery.json`，无需提前 `touch` 状态文件。

如果不设置 `AGENT_AUTO_DISCOVERY_FILE`，行为与历史版本一致：状态文件保存在容器内 `/app/auto-discovery.json`，此时需要将宿主机上已创建的文件以 file-to-file 方式挂载（`-v ./.komari-auto-discovery.json:/app/auto-discovery.json`）才能持久化。
