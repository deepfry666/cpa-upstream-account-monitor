# 上游账户监控使用指南

适用版本：`upstream-monitor 0.6.1`。本指南根据当前页面、适配器和部署配置编写。

![小黑分别查看余额与周期额度，记录更新时间，不把未知读数当作零](../assets/upstream-monitor-illustrations/01-account-watch.png)

这个插件把 CPA 已配置的上游账户整理成一份监控清单。你可以查看余额、Key 配额、套餐周期额度和最近查询状态，发现额度不足或查询失败后再到对应平台处理。它适合同时使用多个官方 API、兼容中转或订阅账户的人。

查看数据时请逐项核对单位：USD、CNY、Credits、5 小时窗口各有自己的含义，不能相加成一个“总余额”。没有查到可信读数的记录，也不等于余额为零。

阅读顺序：[看懂数据](#先分清余额配额和周期) → [安装与升级](#安装与升级) → [首次使用](#首次使用) → [日常查看](#四个工作区怎么用) → [常见问题](#常见问题)。

## 先分清余额、配额和周期

| 页面里的数据 | 它回答什么 | 阅读时注意 |
| --- | --- | --- |
| 余额 | 还剩多少货币金额 | 保留供应商返回的币种和范围；不同币种不能直接相加 |
| 当前 Key 配额 | 这个 Key 还能使用多少 | Key 的限制可能小于账户的可用额度 |
| 账户额度 | 这个上游账户还剩多少额度 | NewAPI 主指标展示当前剩余值，历史累计发放总额不等于当前可用量 |
| 周期额度 | 当前 5 小时、一周、一月等窗口还能用多少 | 各窗口独立生效，不能相加；单位可能是 Credits、Token、次数等 |
| 重置时间 | 某个额度窗口何时重置 | 仅在上游明确提供时显示；套餐到期时间不是重置时间 |
| 未知、暂无数据、不支持 | 当前没有可信的可用数值 | 查询失败或接口不支持不等于余额为零 |

同一账户可能同时有现金余额、Key 配额和多个周期窗口。某一项有数据，并不意味着其他项也已查询成功。详情中的字段组会保留各自的状态与更新时间；读数超额时仍保留实际用量，不会因为进度条达到 100% 就隐藏超额。

插件状态反映已查询到的余额、额度和接口结果，不是对所有模型都能正常推理的承诺。插件不会充值、重置配额、切换 CPA 路由或轮换凭据。

## 支持哪些上游

当前适配器覆盖 DeepSeek、智谱 / Z.ai、Moonshot、Kimi Coding、NewAPI、Sub2API、OpenCode Go、Command Code GOAT、Cline Pass、OpenAI 兼容中转和 Codex API Key 来源。

“支持”表示已有相应查询逻辑，能展示的字段仍取决于对方实际开放的接口、账户权限和返回结果。尤其注意：

- 智谱 / Z.ai 可以查询套餐周期额度，当前没有可验证的公开现金余额接口。页面会提示“暂不支持自动查询现金余额”，可转到供应商控制台核对。
- NewAPI 的当前 Key 查询与 PAT 整账户查询分别执行。没有 PAT 或 PAT 失效时，已成功取得的 Key 结果仍可展示。
- Sub2API 计费详情接口可选。该接口返回 404 或 405 时，余额和用量结果仍保留，也不会仅因此产生“需要处理”告警。
- Command Code 的月度窗口只有在服务端提供相应月度字段时才出现。Cline Pass 的 5 小时、一周和 30 天窗口根据服务端上限与 usage 数据计算。
- OpenAI 兼容接口只约定推理协议，不保证存在通用余额接口。自动识别失败时可检查实际平台协议；不要把所有中转都当作 NewAPI。

账户从 CPA 配置及宿主凭证发现。页面可选择监控范围、备注和查询方式，账户及 Key 本身仍应在 CPA / CPAMP 中维护。

## 安装与升级

### 准备三个组件

`0.6.1` 的完整浏览器管理流程需要：

| 组件 | 作用 | 部署要求 |
| --- | --- | --- |
| `upstream-monitor.so` | 发现账户、查询上游、保存快照与偏好 | 支持本插件原生动态库接口的 Linux CPA；选匹配的 AMD64 或 ARM64 包 |
| 会话代理 | 校验 CPAMP 管理密钥，并在服务器侧访问 CPA | 可访问 CPAMP `/status` 和 CPA Management API；持有服务器侧 CPA secret |
| HTTPS 反向代理路由 | 让浏览器通过同一来源访问会话和管理接口 | 与插件页面相同的协议、域名和端口；保留 `/upstream-monitor/` 路径 |

这里有两套管理密钥：页面输入的是 **CPAMP 管理密钥**；代理读取的是 **CPA Management Key**。它们可以不同，后者不需要输入浏览器。会话 cookie 设置了 `Secure`，实际部署应通过 HTTPS 访问。

升级已有安装前，按 [迁移与回滚说明](./MIGRATION_AND_ROLLBACK.md) 停止写入并成套备份动态库、偏好文件、偏好密钥和快照缓存。`0.5.x` / `0.6.0` 到 `0.6.1` 虽然无需改数据格式，管理页仍需新增的会话代理。

### 1. 安装插件本体

从 [v0.6.1 Release](https://github.com/deepfry666/cpa-upstream-account-monitor/releases/tag/v0.6.1) 下载架构匹配的 `upstream-monitor_0.6.1_linux_amd64.zip` 或 `upstream-monitor_0.6.1_linux_arm64.zip`，用同页 `checksums.txt` 核对 SHA-256。解压后保持动态库名为 `upstream-monitor.so`，放入 CPA 使用的对应架构插件目录，例如：

```text
plugins/linux/amd64/upstream-monitor.so
plugins/linux/arm64/upstream-monitor.so
```

使用 [README 的 CPA 配置示例](../README.md#安装) 开启插件，并确认这些路径是 **CPA 进程或容器内** 实际可读写的路径：

- `source_config_path`：CPA 当前配置文件，供插件发现静态 API Key 上游。
- `cache_path`：持久化快照和查询历史。
- `preferences_path`：监控选择、备注、阈值和加密 PAT；旁边的 `.key` 文件必须一起保管。

容器部署应将数据目录持久化，避免重建容器时丢失偏好与历史。启动 CPA 后先确认动态库成功加载，随后完成会话代理部署。

### 2. 准备会话代理

Linux AMD64 可直接下载 `upstream-monitor-session-proxy_0.6.1_linux_amd64.zip`，用 Release 中的 `SHA256SUMS` 核对后，解压到独立的部署目录。包内有代理二进制、Dockerfile、Compose、Nginx 片段和发布说明；此代理包与插件 `.so` 包是两份资产。

当前 Release 未提供 ARM64 会话代理包。需要 ARM64 时，在本仓库根目录用 Go 1.26+ 构建，并将部署文件放在源码仓库之外：

```bash
mkdir -p ../upstream-monitor-session-proxy-deploy
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../upstream-monitor-session-proxy-deploy/upstream-monitor-session-proxy ./cmd/session-proxy
cp deploy/session-proxy.Dockerfile deploy/session-proxy.compose.yaml deploy/nginx-upstream-monitor.conf ../upstream-monitor-session-proxy-deploy/
```

后续操作在代理部署目录进行：

1. 为 `secrets/cpa-management-key` 创建受限文件，只填服务器实际使用的 CPA Management Key。该文件不是 CPAMP 密钥，也不是已哈希的配置值。不要将它提交到 Git。
2. 将 secrets 目录权限设为仅维护者可访问、密钥文件设为 `600`；二进制需有执行权限。
3. 编辑 `session-proxy.compose.yaml`，核对以下设置与现有容器网络一致。

| Compose 配置 | 模板值 | 需要确认什么 |
| --- | --- | --- |
| `CPA_BASE_URL` | `http://cli-proxy-api:8317` | 代理容器能访问的 CPA 地址 |
| `CPAMP_BASE_URL` | `http://cpa-manager-plus:18317` | 代理容器能访问的 CPAMP 地址，且 `/status` 支持当前管理密钥校验 |
| `CPA_MANAGEMENT_KEY_FILE` | `/run/secrets/cpa_management_key` | 保持与 Compose secret 挂载路径一致 |
| secret 源文件 | `./secrets/cpa-management-key` | 上一步创建的服务器私密文件 |
| 外部网络名 | `cpa_ai_gateway` | 已存在且能连接 CPA、CPAMP 的实际 Docker 网络 |
| 宿主机端口 | `127.0.0.1:18321:18320` | 由宿主机反向代理访问，避免直接对公网开放 |

`session-proxy.Dockerfile` 只打包已有二进制，不会代替你编译。确认文件齐全后，在目标 Linux 服务器的部署目录运行：

```bash
docker compose -f session-proxy.compose.yaml up -d --build
docker compose -f session-proxy.compose.yaml ps
curl -i http://127.0.0.1:18321/health
```

健康接口应返回 `204`；它只验证代理进程可用，首次登录还会验证 CPAMP 和 CPA 连接。

### 3. 配置同源 HTTPS 路由

在提供 CPAMP / 插件页面的同一个 HTTPS 站点中增加 `/upstream-monitor/` 路由。[仓库 Nginx 片段](../deploy/nginx-upstream-monitor.conf) 使用了已有的限流区和通用 snippet；没有这些配置时，不应原样套用。Nginx 直接运行在宿主机时，可使用以下最小片段：

```nginx
location ^~ /upstream-monitor/ {
    proxy_pass http://127.0.0.1:18321;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 40s;
    proxy_no_cache 1;
    proxy_cache_bypass 1;
    add_header Cache-Control "private, no-store" always;
}
```

`proxy_pass` 后面不追加 `/`，以保留原始路径。若 Nginx 运行在容器中，需要根据实际 Docker 网络调整上游地址，容器里的 `127.0.0.1` 不代表宿主机。通过 `nginx -t` 后再加载新配置。

代理只处理会话和白名单 API，插件页面仍从 CPA / CPAMP 的插件入口打开；直接打开 `/upstream-monitor/` 根路径不会得到管理页面。

## 首次使用

1. 登录 CPAMP，从插件入口打开“上游账户监控”。支持同源认证握手的父页面会自动建立会话；成功后连接区隐藏。
2. 独立打开插件页面或自动认证未完成时，在“CPAMP 管理密钥”中输入当前 CPAMP 密钥，点击“连接并加载”。
3. 打开“供应商配置”，核对发现的账户、Base URL 与查询方式。按需勾选监控账户，填写便于区分的备注；备注只用于本插件。
4. NewAPI 需要整账户信息时，填写该账户的管理 PAT。默认先用“自动识别”，只有已确认平台协议时才手动切换查询方式。
5. 点击“保存监控配置”，再到“账户监控”查看。打开页面会先展示已保存快照；首次查询或后台查询进行中时可稍候，或对所选账户点击“刷新当前”。
6. 在“监控设置”保存适合自己的提醒阈值；在“数据状态”确认目录同步成功，并检查数据的实际采集时间。

支持的适配器不一定都有同名的手动下拉选项，例如 `0.6.1` 的查询方式列表没有单独的 Cline Pass 选项。遇到识别问题，应核对 [适配器配置](../adapters.go) 的 `monitors` 覆盖能力，不要选择不对应的平台协议。

## 四个工作区怎么用

| 工作区 | 常用操作 | 使用提示 |
| --- | --- | --- |
| 账户监控 | 看摘要、搜索和筛选账户、查看详情、刷新、看最近查询 | 桌面从列表选账户；手机可用“选择账户”。详情分为概览、计费与限制、用量统计、历史和诊断 |
| 供应商配置 | 勾选监控、改备注、选查询方式、管理 NewAPI PAT、设账户现金阈值 | 修改后点击“保存监控配置”；账户覆盖优先于币种默认阈值 |
| 监控设置 | 设置剩余百分比与各币种现金提醒金额 | 点击“保存监控设置”立即重新评估已有数据的告警，不发起上游查询 |
| 数据状态 | 看缓存有效期、请求超时、刷新状态、同步方式和 CPA 目录同步结果 | 用于判断数据是否新鲜；修改 TTL、同步周期等运行参数需调整 CPA 插件配置 |

取消监控时，在“供应商配置”取消勾选并保存。它会移出当前监控报告，保留历史。“清除记录”是另一个操作：确认后删除该账户已保存快照和查询历史。两者都不会删除 CPA 中的供应商或 API Key。

## 刷新、缓存与历史

| 操作或参数 | 实际含义 |
| --- | --- |
| 重新加载 | 读取当前状态和 CPA 账户目录，不强制所有账户重新查询上游；发现缺失快照时可唤醒后台任务 |
| 刷新当前 | 对当前选中账户提交一次异步强制刷新 |
| 刷新全部 | 对全部已纳入监控的账户提交异步强制刷新，不限于当前筛选结果 |
| `cache_ttl_seconds` | 成功快照有效期，默认 300 秒；不是浏览器页面缓存时间 |
| `sync_interval_seconds` | 后台目录同步与调度的基准间隔，默认 60 秒；查询仍按 TTL 判断是否需要更新，失败时会退避 |
| `request_timeout_seconds` / `account_timeout_seconds` | 默认单次 HTTP 请求 8 秒、单账户总预算 30 秒 |
| 最近查询 / 历史 | 每账户最多保留 50 条快照历史，不是完整请求日志或长期账单 |

刷新会显示成功、失败和跳过数量；等待当前任务完成后再发起下一次。关闭页面后，插件仍会为可读取的宿主凭证和静态配置来源执行后台同步。

查询失败时，已有成功读数会保留，同时标记错误或过期。`last_success_at` 是最近一次成功数据时间，`last_attempt_at` 是最近尝试时间，报告生成时间只代表页面整理报告的时间。不能把刚刚打开页面当作余额刚刚更新。

## 告警怎样判断

周期额度默认在剩余比例降到“注意 20% / 严重 10%”阈值时提醒；具体保存值以“监控设置”为准，严重阈值必须小于注意阈值。现金余额按币种单独配置金额阈值，也可在“供应商配置”为某个账户设置覆盖。金额阈值的注意值和严重值需要成对填写。

“需要处理”列出当前告警，点击相应条目可定位账户或具体指标；每条告警可单独“关闭”。关闭只隐藏该条持续中的提醒，不会修复问题或改变账户自身的注意、严重状态。问题消失后关闭记录会清理，之后再次发生会重新提醒。

告警也可能来自查询错误、字段组失败或过期数据。先核对“诊断”和数据时间，再判断是否需要充值或调整套餐。

## 管理 PAT 与安全保存

NewAPI 的推理 Key 与管理 PAT 用途不同：Key 查询当前 Key 的可用额度，PAT 用于整账户查询。PAT 留空会保留已保存值；输入新值后保存会替换旧值；需要移除时点击“清除 PAT”再保存。

PAT 草稿只保留在当前页面内存。服务器使用 AES-GCM 加密存储 PAT，接口不会回显原文。备份必须同时保留偏好文件与配套 `.key`；丢失密钥后不能仅凭 JSON 恢复 PAT。出现 `decrypt_error` 时先恢复匹配密钥；`relink_required` 表示旧账户身份无法唯一匹配，需要确认账户并重新关联。

浏览器通过最长 12 小时的 HttpOnly 会话 cookie 访问代理，CPAMP 密钥由 CPAMP 实时校验后建立会话，CPA 密钥只留在服务器。代理重启会清空内存会话，页面需重新连接。只读报告使用独立的 `UPSTREAM_MONITOR_READ_TOKEN`，通过 Authorization 请求头提供，不放进 URL。

备份、排错截图和日志分享前仍需脱敏：即使没有密钥，账户名称、地址、余额和用量也可能属于私密信息。

## 常见问题

| 现象 | 检查方法 |
| --- | --- |
| 安装 `.so` 后页面提示“安全会话暂时不可用” | 检查代理进程、`/upstream-monitor/` 路由、HTTPS、CPAMP `/status` 可达性；这是完整部署的一部分 |
| 提示 CPAMP 管理密钥无效或已失效 | 使用当前 CPAMP 管理密钥；服务器 secret 中的 CPA Management Key 用于另一条连接 |
| 登录看似成功，但数据请求仍未授权 | 核对同源 HTTPS 与 cookie；检查代理使用的 CPA secret、CPA 管理接口权限和地址。会话过期或代理重启后重新打开页面连接 |
| 供应商配置中缺少新账户 | 在 CPA 中确认配置有效，点击“重新加载”；检查“数据状态”的目录同步结果及容器内 `source_config_path` |
| 余额一直是旧值 | 看最近成功时间、是否过期和刷新失败原因；TTL 内读取旧快照是正常行为，需立即查询时用“刷新当前” |
| 有 Key 额度却没有账户余额 | NewAPI 两路查询独立；检查 PAT 状态。其他平台也可能只开放部分字段 |
| 智谱 / Z.ai 没有现金余额 | 当前官方接口限制，到供应商控制台核对；套餐剩余额度不能代替现金 |
| Sub2API 提示不支持计费详情 | 实例未开放可选计费接口时可正常出现，继续查看已成功取得的余额和用量 |
| 保存时提示已被其他页面修改 | 修订号冲突保护生效；核对并保留需要的草稿，重新加载最新配置后再保存 |
| 取消监控后旧历史仍存在 | 这是预期行为；确需删除快照与历史时使用“清除记录”并确认 |
| 出现 PAT 解密失败或缓存恢复失败 | 保留原文件，按 [迁移与回滚](./MIGRATION_AND_ROLLBACK.md) 检查成套备份、密钥和权限，不手工删除格式字段 |
| 通过代理查询上游失败 | 核对 `inherit`、`direct`、`url` 三态与代理地址。账户设置优先于供应商，供应商优先于宿主；无效代理不会静默改成直连 |

高级参数与 API 见 [README](../README.md)，版本变化见 [CHANGELOG](../CHANGELOG.md)，部署模板见 [deploy](../deploy/)。
