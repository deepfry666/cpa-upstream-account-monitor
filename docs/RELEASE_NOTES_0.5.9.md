# 上游账户监控 0.5.9 发布说明

发布日期：2026-09-19
上一版本：0.5.8
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.9 将每账户查询历史上限从 100 条收紧为 50 条，减少长期内存和缓存文件占用，同时保留足够的问题回溯记录。

## 行为变化

- 每次成功查询后，每账户最多保留最近 50 条。
- 历史接口 `limit` 范围改为 `1..50`，默认仍为 50。
- 重启加载旧缓存时，100 条历史会自动裁剪为最近 50 条。
- 账户身份迁移合并历史时同样只保留最近 50 条。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 0.5.8 到 0.5.9 可直接替换动态库；旧缓存会在加载时自动裁剪。

## 发布包

```text
upstream-monitor_0.5.9_linux_amd64.zip
upstream-monitor_0.5.9_linux_amd64.zip.sha256
upstream-monitor_0.5.9_linux_arm64.zip
upstream-monitor_0.5.9_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 36 项通过。
- Linux AMD64 与 ARM64 发布包通过真实 `dlopen` 和导出符号检查。

