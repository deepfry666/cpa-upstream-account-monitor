# 上游账户监控 0.5.5 发布说明

发布日期：2026-09-19
上一版本：0.5.4
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.5 增加逐条关闭告警的能力。用户确认“需要处理”中的某条告警后，可以将它隐藏，不必因为已知问题一直看到提醒；隐藏范围仅限该条告警，不会关闭监控、账户状态或未来再次发生的同类问题。

## 用户可见变化

- 每条告警右侧保留“查看”，新增“关闭”按钮。
- 关闭后告警从“需要处理”列表移除，条数同步减少。
- 页面提示“已关闭告警；问题再次出现时会重新提醒”。
- 账户本身的 warning、critical 和 stale 摘要继续统计，不会被告警关闭动作篡改。

## 行为语义

- 关闭状态使用稳定 `alert_id` 持久化到插件偏好文件。
- 告警持续存在时保持隐藏，不需要反复关闭。
- 告警从报告中消失后，对应关闭记录自动清理，问题再次发生会重新显示。
- 优先级、错误码或窗口变化会改变告警 ID，新的严重告警不会被旧关闭记录吞掉。
- 管理和 Hermes 只读报告共用同一过滤结果。

## 兼容性

- 偏好格式：`format_version=2`，新增可选 `dismissed_alerts` 字段，旧文件可直接读取。
- 快照缓存格式：`format_version=2`，未改变。
- 账户 ID、PAT AAD、归档和历史结构未改变。
- 0.5.4 到 0.5.5 可直接替换动态库，不需要迁移数据。

## 发布包

```text
upstream-monitor_0.5.5_linux_amd64.zip
upstream-monitor_0.5.5_linux_amd64.zip.sha256
upstream-monitor_0.5.5_linux_arm64.zip
upstream-monitor_0.5.5_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 35 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

