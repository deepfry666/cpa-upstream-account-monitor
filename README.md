# 上游账户监控

[English](./README_EN.md)

“上游账户监控”是一个适用于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) / CPA Manager Plus 的原生动态库插件，用于集中查看 AI 上游账户的余额、周期额度、Key 额度、用量和健康状态。

插件技术 ID 仍为 `upstream-monitor`，因此升级后原有的配置路径、管理路由和供应商勾选数据都可以继续使用。

## 主要功能

- 从 CPA 凭证和静态供应商配置中自动发现上游账户。
- 支持 DeepSeek、智谱 / Z.ai、Moonshot、Kimi Coding、NewAPI、Sub2API、OpenCode Go、Command Code GOAT 和 OpenAI 兼容中转等适配器。
- 区分现金余额、周期额度、账户总额度和 Token / Key 专属额度，不使用单一百分比掩盖实际计费方式。
- 完整显示剩余量、总额、已用量、单位、重置时间和套餐周期。
- NewAPI 支持可选的管理 PAT，在保留当前 Key 额度之外，额外读取整个账号的总额度。
- Sub2API 支持展示倍率、速率限制、Token 用量、每日用量和模型统计等结构化数据。
- 供应商可以勾选、取消勾选、自定义备注名称并保存监控配置。
- 支持单账户刷新、批量刷新、缓存、陈旧快照回退和异常提示。
- 中文优先的响应式管理页面，同时检查桌面和手机布局。
- 提供只读机器接口，供 Hermes 等巡检工具读取。
- 插件只读取上游状态，不修改 CPA 路由、凭证或请求处理逻辑。

## 支持环境

当前正式发布包提供：

```text
Linux AMD64
Linux ARM64
```

动态库文件名必须保持为 `upstream-monitor.so`，CPA 会从文件名推导插件 ID。

## 安装

1. 从 GitHub Releases 下载对应架构的压缩包。
2. 解压并复制动态库：

```text
plugins/linux/amd64/upstream-monitor.so
plugins/linux/arm64/upstream-monitor.so
```

3. 在 CPA 配置中启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    upstream-monitor:
      enabled: true
      cache_ttl_seconds: 300
      request_timeout_seconds: 8
      read_token_env: UPSTREAM_MONITOR_READ_TOKEN
      cache_path: /CLIProxyAPI/data/upstream-monitor-snapshots.json
      preferences_path: /CLIProxyAPI/data/upstream-monitor-preferences.json
      thresholds:
        warning_percent: 20
        critical_percent: 10
```

4. 重启 CPA，然后在 Management Center 或 CPA Manager Plus 中打开“上游账户监控”。

## 账户类型

页面和机器报告使用 `kind` 明确描述账户的计费方式：

```text
balance       现金余额，例如 46.76 CNY
period_quota  滚动或固定周期额度，例如 66.75 / 70 credits
quota         账户或 Key 总额度
unsupported   已发现供应商，但没有可验证的查询接口
```

当上游同时返回账户总额度和当前 Key 额度时，插件会分别显示，不会把 Key 配额误认为整个账号的总额度。

## 供应商配置

插件支持通过 CPA Management API 读取供应商列表，并保存以下设置：

- 是否纳入监控。
- 自定义显示名称。
- 适配器覆盖。
- 可选的 NewAPI 管理 PAT。

管理 PAT 使用独立密钥加密保存，不会回显明文。插件不会把推理 API Key 自动当作管理凭据使用。

## 管理接口

以下路由由 CPA Management API 鉴权保护：

```text
GET  /v0/management/upstream-monitor/summary
GET  /v0/management/upstream-monitor/state
POST /v0/management/upstream-monitor/refresh
PUT  /v0/management/upstream-monitor/providers
GET  /v0/management/upstream-monitor/history
POST /v0/management/upstream-monitor/cleanup
GET  /v0/management/upstream-monitor/config
PUT  /v0/management/upstream-monitor/config
```

Hermes 只读接口使用单独的 `UPSTREAM_MONITOR_READ_TOKEN`：

```text
GET /v0/resource/plugins/upstream-monitor/api/v1/health
GET /v0/resource/plugins/upstream-monitor/api/v1/report
```

只读 Token 仅接受 `Authorization: Bearer ...`，不接受 URL 查询参数。

## 安全说明

- 缓存和机器报告中的凭证会被脱敏。
- 管理 PAT 和供应商配置加密保存。
- 插件拒绝未选择账户的自动探测。
- 插件不读取 Sub2API Balance 插件的数据库或运行状态。
- 插件不会修改 CPA 的供应商、认证文件或路由配置。

## 构建

需要 Go 1.26+、CGO 和目标平台对应的 C 编译器。

```bash
make test
make vet
make build
make package VERSION=0.4.1
```

发布压缩包位于 `dist/`，并同时生成 SHA-256 校验文件。

## 许可证

[MIT](./LICENSE)
