# 上游账户监控 0.5.2 发布说明

发布日期：2026-09-19
上一版本：0.5.1
技术 ID：`upstream-monitor`
动态库名：`upstream-monitor.so`

## 版本目标

0.5.2 不修改运行时行为、配置格式或缓存格式，补齐《上游账户监控：完整修复实施方案》中 Linux AMD64/ARM64 目标架构真实加载检查这一发布门槛。

## 发布验证变化

- `make package` 在生成 ZIP 前对目标动态库执行真实 `dlopen(RTLD_NOW)`。
- 加载后解析 `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown` 四个必需符号。
- AMD64 使用当前 x86_64 Linux 构建环境原生加载。
- ARM64 使用 `aarch64-linux-gnu-gcc` 编译加载器，并通过 `qemu-aarch64-static` 加载 ARM64 动态库。
- ARM64 构建环境缺少 QEMU 时，从当前 apt 软件源下载 `qemu-user-static` 到临时目录；QEMU 不进入仓库或发布 ZIP。
- 任一架构加载或符号解析失败时，`make package` 失败，Release 工作流不会发布不完整资产。

## 兼容性

- 偏好格式：`format_version=2`，未改变。
- 快照缓存格式：`format_version=2`，未改变。
- 账户 ID、PAT AAD、归档和历史结构未改变。
- 0.5.1 到 0.5.2 可直接替换动态库，不需要迁移数据。
- 智谱 / Z.ai 现金余额仍是无公开可验证接口的外部限制，不声明已补齐。

## 发布包

```text
upstream-monitor_0.5.2_linux_amd64.zip
upstream-monitor_0.5.2_linux_amd64.zip.sha256
upstream-monitor_0.5.2_linux_arm64.zip
upstream-monitor_0.5.2_linux_arm64.zip.sha256
checksums.txt
```

每个压缩包只包含对应架构的 `upstream-monitor.so`、中文 README 和 LICENSE。生成的头文件、缓存、截图、密钥、PAT、真实账户响应、QEMU 可执行文件和本地构建目录不得发布。

## 升级与回滚

1. 停止 CPA，备份当前动态库、偏好文件、偏好密钥和快照缓存。
2. 替换对应架构的 `upstream-monitor.so`，保持文件名和 `preferences_path`、`cache_path` 配置不变。
3. 启动 CPA，确认插件注册、缓存恢复和目录同步状态正常。
4. 执行重新加载、刷新当前、刷新全部各一次，确认任务状态和字段组时间。
5. 回滚到 0.5.1 只需恢复旧动态库，不需要恢复数据文件；回滚到 0.4.1 仍需恢复成套旧二进制和数据。

## 发布门槛

- `gofmt`、`git diff --check` 无输出。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 全部通过。
- Playwright 全量 34 项通过。
- Linux AMD64 与 ARM64 均由对应工具链构建，并在目标架构加载器中完成 `dlopen` 和导出符号检查。
- 检查 ELF 架构、包内容和 SHA-256。
- 对 Git 跟踪文件和发布包执行敏感信息扫描，确认无 PAT、密钥、生产缓存、真实账户数据或 QEMU 二进制。
