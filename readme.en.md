# komari-agent

[简体中文](readme.md) | English

## Configuration

Agent parameters can be passed as command-line flags, environment variables, or a JSON configuration file.

Minimal startup example:

```bash
./komari-agent --endpoint "https://example.com" --token "your-token"
```

Using environment variables:

```bash
export AGENT_ENDPOINT="https://example.com"
export AGENT_TOKEN="your-token"
./komari-agent
```

Using a JSON configuration file:

```bash
./komari-agent --config ./config.json
```

Example `config.json`:

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

Configuration precedence, from lowest to highest: defaults, command-line flags, environment variables, JSON configuration file.

Common options:

The "Supported since" column shows the first release tag in which the option itself appeared. Environment variables and the JSON configuration file are supported from `1.1.33` onwards; options that predate the earliest tag are marked `0.0.9`.

| JSON field | Environment variable | Command-line flag | Description | Supported since |
| --- | --- | --- | --- | --- |
| `endpoint` | `AGENT_ENDPOINT` | `--endpoint`, `-e` | Panel address | `0.0.9` |
| `token` | `AGENT_TOKEN` | `--token`, `-t` | Agent token | `0.0.9` |
| `interval` | `AGENT_INTERVAL` | `--interval`, `-i` | Data collection interval, in seconds | `0.0.9` |
| `disable_auto_update` | `AGENT_DISABLE_AUTO_UPDATE` | `--disable-auto-update` | Disable automatic updates | `0.0.9` |
| `disable_web_ssh` | `AGENT_DISABLE_WEB_SSH` | `--disable-web-ssh` | Disable remote control | `0.0.9` |
| `ignore_unsafe_cert` | `AGENT_IGNORE_UNSAFE_CERT` | `--ignore-unsafe-cert`, `-u` | Ignore unsafe certificates | `0.0.9` |
| `include_nics` | `AGENT_INCLUDE_NICS` | `--include-nics` | Only count the listed NICs, comma-separated | `0.0.22` |
| `exclude_nics` | `AGENT_EXCLUDE_NICS` | `--exclude-nics` | Exclude the listed NICs, comma-separated | `0.0.22` |
| `include_mountpoints` | `AGENT_INCLUDE_MOUNTPOINTS` | `--include-mountpoint` | Only count the listed mount points, semicolon-separated | `0.1.0` |
| `month_rotate` | `AGENT_MONTH_ROTATE` | `--month-rotate` | Day of month to reset traffic statistics, `0` disables | `0.1.0` |
| `auto_discovery_key` | `AGENT_AUTO_DISCOVERY_KEY` | `--auto-discovery` | Auto-discovery key | `1.0.40` |
| `custom_dns` | `AGENT_CUSTOM_DNS` | `--custom-dns` | Custom DNS server | `1.0.80` |
| `enable_gpu` | `AGENT_ENABLE_GPU` | `--gpu` | Enable detailed GPU monitoring | `1.0.80` |
| `disable_compression` | `AGENT_DISABLE_COMPRESSION` | `--disable-compression` | Disable v2 transport compression | `1.2.10` |
| `prefer_ip_version` | `AGENT_PREFER_IP_VERSION` | `--prefer-ip-version` | Preferred IP version, `4` or `6` | Unreleased |

For the full parameter list, run:

```bash
./komari-agent --help
```

See also `cmd/flags/flags.go` and `cmd/root.go`.
