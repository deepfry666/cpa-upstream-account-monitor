# 上游账户监控 0.5.4 发布说明

发布日期：2026-09-19
上一版本：0.5.3
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.4 修正 Command Code 月度额度显示。此前只在响应包含 `windowLimits.month` 时显示一月窗口，但当前官方账号的实际月度额度来自账单周期用量与剩余 Credits，而不是该限流窗口。本次改为使用官方接口字段进行等价换算，并保留旧版已删除的硬编码套餐金额边界。

## 用户可见变化

- Command Code 详情重新显示“一月”窗口，包含月度已用、剩余、总额、百分比和账单周期重置时间。
- 月度已用量优先使用 `totalMonthlyCredits`；月度剩余使用 `monthlyCredits`；总额为两者之和。
- 服务端若显式返回 `windowLimits.month`，仍优先使用服务端窗口。
- 套餐额度区域同步显示可确认的月度已用和总额；缺少月度依据时不生成一月窗口。

## 数据来源

- `GET /alpha/billing/credits`：`monthlyCredits`、`purchasedCredits`、`freeCredits`、`windowLimits`。
- `GET /alpha/usage/summary`：`totalMonthlyCredits`、`periodBasis`。
- `GET /alpha/billing/subscriptions`：订阅状态、`currentPeriodStart`、`currentPeriodEnd`。

月度窗口的 `source` 为 `derived`，5 小时和一周窗口的 `source` 为 `upstream`。该字段只用于区分数据来源，不改变旧缓存读取。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 账户 ID、PAT AAD、归档和历史结构未改变。
- 0.5.3 到 0.5.4 可直接替换动态库，不需要迁移数据。
- 从 0.4.1 升级时仍需按迁移说明成套管理二进制、偏好、密钥和缓存。

## 发布包

```text
upstream-monitor_0.5.4_linux_amd64.zip
upstream-monitor_0.5.4_linux_amd64.zip.sha256
upstream-monitor_0.5.4_linux_arm64.zip
upstream-monitor_0.5.4_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 34 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

