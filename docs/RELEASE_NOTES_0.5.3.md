# 上游账户监控 0.5.3 发布说明

发布日期：2026-09-19
上一版本：0.5.2
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.3 修复旧位置型账户 ID 迁移到稳定 ID 时的快照和历史迁移缺口，并把 UI 回归测试和脱敏真实接口核对纳入可重复执行的发布证据。偏好和缓存格式仍为版本 `2`，从 `0.5.2` 可直接升级。

## 用户可见变化

- 旧位置型 ID 唯一匹配稳定身份时，活动快照和历史会与 PAT、监控设置一起迁移，不再因为后续清理旧位置键而丢失历史。
- 迁移历史按查询时间排序、去重并保留最多 100 条；活动快照以较新记录为准，不会被较旧记录覆盖。
- 歧义或 `relink_required` 记录仍不自动继承旧 PAT 和缓存，避免同域多 Key 或轮换场景串用。
- 新增 `make ui-test`，自动寻找空闲端口、启动静态服务并清理；完整 Playwright 套件为 34/34 通过。
- 新增脱敏真实接口验收记录，覆盖 NewAPI、Command Code、DeepSeek、Sub2API 和智谱 / Z.ai 的只读核对结果。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 账户 ID、PAT AAD、归档和历史结构未改变。
- 0.5.2 到 0.5.3 可直接替换动态库，不需要修改配置。
- 从 0.4.1 升级时仍需按迁移说明成套管理二进制、偏好、密钥和缓存。
- 智谱 / Z.ai 现金余额仍是外部接口限制，不声明已补齐。

## 发布包

```text
upstream-monitor_0.5.3_linux_amd64.zip
upstream-monitor_0.5.3_linux_amd64.zip.sha256
upstream-monitor_0.5.3_linux_arm64.zip
upstream-monitor_0.5.3_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。生成的头文件、缓存、截图、密钥、PAT、真实账户响应和本地构建目录不得发布。

## 升级与回滚

1. 停止 CPA，备份当前动态库、偏好文件、偏好密钥和快照缓存。
2. 替换对应架构的 `upstream-monitor.so`，保持文件名和 `preferences_path`、`cache_path` 配置不变。
3. 启动 CPA，确认旧账户的 PAT 状态、活动快照和历史条目一致。
4. 停止并再次启动 CPA，确认第二次启动不重复迁移、不重复追加历史。
5. 回滚到 0.5.2 只需恢复旧动态库；回滚到 0.4.1 仍需恢复成套旧二进制和数据。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- `make ui-test` 完整 Playwright 34 项通过。
- 发布 CI 必须在 Linux AMD64 与 ARM64 目标架构加载器中完成 `dlopen` 和导出符号检查；跳过加载检查的诊断构建不得作为正式发布包。
- 检查 ELF 架构、包内容和 SHA-256。
- 对 Git 跟踪文件和发布包执行敏感信息扫描，确认无 PAT、密钥、生产缓存、真实账户数据或 QEMU 二进制。
