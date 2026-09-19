# 上游账户监控 0.5.8 发布说明

发布日期：2026-09-19
上一版本：0.5.7
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.8 修正 Sub2API 可选计费详情接口的 404 处理。部分 Sub2API 实例没有实现 `/v1/sub2api/billing`，这不应被当成账户查询失败。

## 用户可见变化

- HTTP 404 和 405 会显示为“该实例未提供计费详情接口”，不再显示为查询错误。
- 账户不会因为这个可选接口缺失而进入 warning 状态。
- “需要处理”中不再产生对应的计费接口告警。
- 余额、用量、模型统计和其他已成功获取的数据继续正常展示。

## 错误语义

- 404 / 405：`unsupported`，属于供应商兼容差异。
- 401 / 403 / 5xx / 网络错误：仍按真实查询故障处理。
- 成功响应但 JSON 无效：仍标记为 `INVALID_RESPONSE`。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 0.5.7 到 0.5.8 可直接替换动态库，不需要迁移数据。

## 发布包

```text
upstream-monitor_0.5.8_linux_amd64.zip
upstream-monitor_0.5.8_linux_amd64.zip.sha256
upstream-monitor_0.5.8_linux_arm64.zip
upstream-monitor_0.5.8_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 36 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

