# 上游账户监控

[English](./README_EN.md)

“上游账户监控”是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) / CPA Manager Plus 的原生动态库插件。它从 CPA 配置和宿主凭证中独立发现上游账户，查询余额、额度、用量和健康状态，并提供中文优先的响应式管理页和只读机器接口。

当前版本：`0.5.0`。插件技术 ID 和动态库名保持为 `upstream-monitor`，CPAMP 挂载入口不变。

## 核心行为

- 打开页面先读取已持久化的快照，不等待上游网络查询。快照超过 `cache_ttl_seconds` 后按 `last_success_at` 标记过期，并继续展示上次成功数据和真实采集时间。
- 后台调度默认每 `sync_interval_seconds=60` 秒重新读取宿主凭证目录和 `source_config_path`，按 TTL 刷新需要更新的账户。单账户查询有 `account_timeout_seconds` 总预算，单次 HTTP 有 `request_timeout_seconds` 超时。
- 后台查询使用插件自有、支持 `context` 的 HTTP 客户端，不长期依赖某次管理请求的 `host_callback_id`。停止插件时会取消调度、取消在途请求并等待退出。
- “重新加载”只读取当前状态；“刷新当前”只提交一个账户 ID；“刷新全部”显式使用 `scope=all`。刷新为异步任务，可查询成功、失败和跳过数量；同一账户的并发刷新会合并。
- 配置保存采用修订号检查和原子替换。批量校验或写盘失败不会产生半更新；两页面并发编辑会收到 `409`，不会静默覆盖。
- 账户使用稳定的 `account_id`。CPA 重排、删除前置条目、修改备注或刷新不会把凭证和历史串到其他账户；同域不同 Key 保持独立。
- 取消监控会立即移出当前报告并保留历史；只有“清除记录”会删除该账户的活动快照和历史。旧请求不能在取消或清除后复活账户。

## 支持与语义

当前适配器：

```text
DeepSeek / 智谱 / Z.ai / Moonshot / Kimi Coding
NewAPI / Sub2API / OpenCode Go / Command Code GOAT
OpenAI 兼容中转 / Codex API Key
```

额度数据按以下维度分别展示：

- `balance`：现金余额，保留金额字符串、币种和币种范围。
- `quota`：账户或当前 Key 的总额度，明确区分 `account` 与 `token`。
- `period_quota`：5 小时、一周、一月等周期窗口，分别记录总量、已用、剩余和超额。
- `sections`：余额、计费、Key 额度、账户额度、用量、订阅等字段组的状态和更新时间。

协议中显式声明的百分比按百分比处理，比例按 `0–1` 处理；不会通过“数值是否小于等于 1”猜测单位。`expires_at` 只表示套餐或凭证到期，`reset_at` 只接收协议明确给出的周期重置时间。窗口 `used > total` 时保留真实已用量和超额量，界面进度条限制为 100%，但告警仍按超额状态判断。

NewAPI 的当前 Key 配额和 PAT 整账户查询互不短路：PAT 未配置、失效或账户查询失败时，有效的 Key 结果仍保留；反之亦然。PAT 只用于账户查询，不会把推理 Key 当作管理凭据。

Command Code 只展示服务端明确返回的 five-hour、weekly、month 窗口和 credits 池。缺少字段显示“未提供”，只有明确 `unlimited` 才显示不限额，不会用硬编码套餐总额补齐窗口。

### 智谱 / Z.ai 现金余额限制

当前官方可用接口只提供可验证的套餐周期额度，没有可验证的公开现金余额查询接口。插件会明确显示“暂不支持自动查询现金余额”，并提供供应商控制台入口；不会用套餐额度冒充现金余额。该项是外部接口限制，不是已补齐功能。

## 同步、缓存和恢复

快照缓存使用独立格式版本 `2`，偏好文件使用独立格式版本 `2`。两者不依赖插件版本号判断格式。

- 缓存保存活动快照和每账户最多 100 条历史，使用同目录临时文件、文件同步、原子重命名和目录同步。
- 首次失败会创建错误账户行并记录 `last_attempt_at`；后续失败保留上次成功指标，同时保留旧 `last_success_at`，失败时间和数据来源时间含义分开。
- 未知未来格式、损坏 JSON 或身份不一致会在内存替换前失败，原文件不会被静默覆盖。
- CPA 目录短暂读取失败时保留上次成功目录并标记同步失败，不会把失败当成所有账户被删除。
- 关闭浏览器后，后台调度仍适用于可读取的宿主凭证和静态配置来源。页面可见时轮询轻量状态；刷新任务进行时缩短轮询，不可见时减频。

## 代理

账户和供应商代理均为显式三态：

```text
inherit  继承宿主/供应商代理
direct   忽略继承代理并直连
url      使用指定 http、https 或 socks5 代理
```

账户覆盖高于供应商，供应商高于宿主凭证。支持 HTTP、HTTPS、SOCKS5 和 SOCKS5 用户名密码认证。跨主机重定向被拒绝，代理认证信息不写入日志；无效模式或 URL 在配置保存时给出中文错误，不会静默直连。

## 管理页面

页面包含四个工作区：

```text
账户监控       账户摘要、筛选、详情分组、历史、告警定位
供应商配置     CPA 同步、监控开关、备注、协议、PAT、账户阈值覆盖
监控设置       百分比阈值、币种现金阈值
数据状态       TTL、同步周期、超时、目录同步状态和配置来源
```

账户默认按首次发现顺序稳定排列，新条目追加。详情按“当前额度、计费与限制、用量统计、历史、诊断”组织；原有计费、倍率、速率限制、Token、每日用量和模型统计均保留。桌面、平板和手机提供等价核心操作，PAT 草稿只存在当前页面内存，不写入 `localStorage`、`sessionStorage` 或 URL。

## 安装

发布包提供 Linux AMD64 和 Linux ARM64：

```text
plugins/linux/amd64/upstream-monitor.so
plugins/linux/arm64/upstream-monitor.so
```

CPA 配置示例：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    upstream-monitor:
      enabled: true
      cache_ttl_seconds: 300
      sync_interval_seconds: 60
      request_timeout_seconds: 8
      account_timeout_seconds: 30
      read_token_env: UPSTREAM_MONITOR_READ_TOKEN
      source_config_path: /CLIProxyAPI/config.yaml
      cache_path: /CLIProxyAPI/data/upstream-monitor-snapshots.json
      preferences_path: /CLIProxyAPI/data/upstream-monitor-preferences.json
      thresholds:
        warning_percent: 20
        critical_percent: 10
        cash_by_currency:
          CNY:
            warning: "10"
            critical: "1"
```

单账户协议、管理根路径、代理和阈值覆盖使用 `monitors` 或管理页保存。内部 HTTP 默认被拒绝；确需使用时必须在对应账户显式设置 `allow_internal_http: true`。HTTP 与 HTTPS 代理 URL 支持认证信息，SOCKS5 支持用户名密码。

## 管理接口

以下路由由 CPA Management API 鉴权保护：

```text
GET  /v0/management/upstream-monitor/summary
GET  /v0/management/upstream-monitor/state
GET  /v0/management/upstream-monitor/refresh?job_id=...
POST /v0/management/upstream-monitor/refresh
GET  /v0/management/upstream-monitor/config
PUT  /v0/management/upstream-monitor/config
PUT  /v0/management/upstream-monitor/providers
GET  /v0/management/upstream-monitor/history?account_id=...&limit=50
POST /v0/management/upstream-monitor/cleanup
```

异步单账户刷新：

```json
{"account_ids":["acct_example"],"force":true,"async":true}
```

刷新全部：

```json
{"scope":"all","force":true,"async":true}
```

空请求仍兼容为刷新全部，但空 ID、未知 ID 和拼写错误不会被解释成刷新全部。供应商标记操作使用明确的 `pat_action=keep|replace|clear`。

Hermes 只读接口使用独立的 `UPSTREAM_MONITOR_READ_TOKEN`：

```text
GET /v0/resource/plugins/upstream-monitor/api/v1/health
GET /v0/resource/plugins/upstream-monitor/api/v1/report
```

只读 Token 仅接受 `Authorization: Bearer ...`，不接受 URL 查询参数。报告保留 `schema_version`，账户提供 `last_attempt_at`、`last_success_at`、`sections` 和稳定告警 ID。

## 安全

- 诊断和机器报告不返回管理 PAT 或原始推理 Key。
- 管理 PAT 使用 AES-GCM 加密，AAD 绑定稳定账户 ID；PAT 仅返回 `missing`、`ready` 或 `decrypt_error` 状态。
- 已有密文但密钥丢失或错误时，不覆盖旧密文，也不把 PAT 标为可用；非 PAT 功能和其他账户继续工作。
- 插件不读取 Sub2API Balance 插件的数据库或运行状态，不修改 CPA 路由、凭证或请求处理逻辑。
- URL、日志、缓存、报告和发布材料不包含 PAT、密钥或真实账户响应。

## 构建与发布

需要 Go 1.26+、CGO 和目标平台 C 编译器。发布时必须分别使用 Linux AMD64 与 ARM64 工具链构建 `c-shared` 动态库。

```bash
make test
make vet
make package VERSION=0.5.0 GOOS=linux GOARCH=amd64
make package VERSION=0.5.0 GOOS=linux GOARCH=arm64
make checksums VERSION=0.5.0 GOOS=linux GOARCH=amd64
```

`$(basename upstream-monitor.so).h`、`dist/`、缓存、测试截图和真实凭证不进入 Git 或 Release。升级与回滚见 [迁移和回滚说明](./docs/MIGRATION_AND_ROLLBACK.md)，逐项证据见 [实施测试矩阵](./docs/IMPLEMENTATION_TEST_MATRIX.md)。

## 许可证

[MIT](./LICENSE)
