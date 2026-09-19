# 上游账户监控 0.5.6 发布说明

发布日期：2026-09-19
上一版本：0.5.5
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.6 修正 NewAPI 账户额度的展示口径。NewAPI 的 `quota + used_quota` 表示历史累计发放额度，不是当前可用余额；主页面改为只展示实时剩余金额，避免把历史充值总额误认为当前总额。

## 用户可见变化

- NewAPI 账户指标从“NewAPI 账户总额”改为“NewAPI 账户余额”。
- 主指标和“配额明细”只显示当前剩余金额与币种。
- 不再显示 `剩余 / 历史总额`，也不再基于历史总额显示进度条。
- “当前 Key”额度、账户诊断和原始字段仍保留。

## 数据语义

- 新字段 `display=remaining` 表示该额度只适合显示剩余值。
- 报告内部仍保留 `remaining`、`total` 和 `used`，用于兼容、审计和后续诊断。
- 该展示语义只应用于 NewAPI 账户查询，不改变其他供应商的总额、已用和窗口显示。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，新增字段为可选字段。
- 0.5.5 到 0.5.6 可直接替换动态库，不需要迁移数据。

## 发布包

```text
upstream-monitor_0.5.6_linux_amd64.zip
upstream-monitor_0.5.6_linux_amd64.zip.sha256
upstream-monitor_0.5.6_linux_arm64.zip
upstream-monitor_0.5.6_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 36 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

