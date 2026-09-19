# 上游账户监控 0.5.7 发布说明

发布日期：2026-09-19
上一版本：0.5.6
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.7 修复账户详情默认“概览”视图没有返回入口的问题。此前首次进入账户时可以看见完整额度与套餐内容，但切换到计费、用量、历史或诊断后，无法直接返回概览，只能离开插件再重新进入。

## 用户可见变化

- 账户详情标签栏新增第一个“概览”标签。
- 在“计费与限制”“用量统计”“历史”“诊断”之间切换后，可以点击“概览”直接返回完整额度与套餐视图。
- 默认进入账户详情时仍自动选中“概览”。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 0.5.6 到 0.5.7 可直接替换动态库，不需要迁移数据。

## 发布包

```text
upstream-monitor_0.5.7_linux_amd64.zip
upstream-monitor_0.5.7_linux_amd64.zip.sha256
upstream-monitor_0.5.7_linux_arm64.zip
upstream-monitor_0.5.7_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 36 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

