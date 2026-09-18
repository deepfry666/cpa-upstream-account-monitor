# 上游账户监控 0.5.1 发布说明

发布日期：2026-09-19
上一版本：0.5.0
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.1 修复 v0.5.0 发布后复核发现的两项问题：阈值降低后缓存状态不能正确回到健康，以及损坏缓存恢复失败未被管理页面明确展示且仍可能被自动保存覆盖。偏好和缓存格式仍为版本 `2`，从 0.5.0 可直接升级。

## 用户可见变化

- 调整周期额度阈值后，已有缓存立即在本地重新评估；降低阈值可使原本的 warning / critical 恢复为 ok，不发起额外上游请求。
- 阈值重算保留余额不足、字段查询失败、过期、禁用等非阈值告警，不会把真实异常错误地清除。
- 快照缓存读取或解析失败时，状态栏显示“快照缓存恢复失败”及原因。
- 缓存恢复失败期间保留原文件和原内存数据，并拒绝自动覆盖不可读文件；修复或恢复缓存成功后警告自动消失。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 账户 ID、PAT AAD、归档和历史结构未改变。
- 智谱 / Z.ai 现金余额仍是无公开可验证接口的外部限制，不声明已补齐。

## 发布包

```text
upstream-monitor_0.5.1_linux_amd64.zip
upstream-monitor_0.5.1_linux_amd64.zip.sha256
upstream-monitor_0.5.1_linux_arm64.zip
upstream-monitor_0.5.1_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。生成的头文件、缓存、截图、密钥、PAT、真实账户响应和本地构建目录不得发布。

## 升级与回滚

1. 停止 CPA，备份当前动态库、偏好文件、偏好密钥和快照缓存。
2. 替换对应架构的 `upstream-monitor.so`，保持文件名和 `preferences_path`、`cache_path` 配置不变。
3. 启动 CPA，确认缓存恢复警告是否消失，并分别执行一次重新加载、刷新当前和刷新全部。
4. 修改一次监控阈值，确认账户状态按新阈值重算且不会访问上游。

0.5.0 到 0.5.1 不需要数据迁移。回滚到 0.4.1 仍需恢复成套旧二进制、偏好、密钥和缓存，详见 `docs/MIGRATION_AND_ROLLBACK.md`。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- Playwright 全量 34 项通过，包括缓存恢复警告状态栏断言。
- Linux AMD64 与 ARM64 由对应 CGO 环境构建，检查 ELF 架构、导出 ABI 和包内容。
- 对 Git 跟踪文件和发布包执行敏感信息扫描，确认无 PAT、密钥、生产缓存和真实账户数据。
