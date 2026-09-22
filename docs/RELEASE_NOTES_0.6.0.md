# 上游账户监控 0.6.0 发布说明

发布日期：2026-09-22
上一版本：0.5.9
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.6.0 新增 Cline Pass 订阅监控，并让插件在 CPAMP 嵌入环境中自动继承当前管理会话，不再要求重复输入 Management Key。

## Cline Pass

- 自动识别 CPA 中名为 `cline pass` 的供应商，适配器 ID 为 `clinepass-usage`。
- 只读请求官方账户、余额、套餐权益和 usage 明细接口。
- 展示 USD 余额、Credits 余额，以及 5 小时、7 天、30 天三个滚动窗口。
- 窗口上限来自套餐权益 `inferenceCapThreshold`。
- 已用量按 usage 明细的 `createdAt` 和 `costUsd` 汇总，支持 `limit` 与 `cursor` 分页。
- usage 汇总包含请求数、Token、费用和模型统计。

## 自动连接

- CPAMP 父页面通过同源 `postMessage` 将当前 Management Key 和 API Base 传给插件 iframe。
- 插件收到消息后自动加载状态并隐藏 Management Key 连接框。
- 自动连接失败时恢复连接框，允许手动输入。
- 插件页面被单独打开时继续保留原有手动连接流程。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 0.5.9 到 0.6.0 可直接替换动态库，不需要迁移数据。
- CPAMP 自动连接需要同步部署带 `plugin-resource-auth` 握手的静态前端。

## 发布包

```text
upstream-monitor_0.6.0_linux_amd64.zip
upstream-monitor_0.6.0_linux_amd64.zip.sha256
upstream-monitor_0.6.0_linux_arm64.zip
upstream-monitor_0.6.0_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 38 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

