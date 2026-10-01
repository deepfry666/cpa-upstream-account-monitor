# 上游账户监控 0.6.1 发布说明

发布日期：2026-09-24

## 修复内容

0.6.0 的嵌入握手把 CPAMP 管理密钥直接当作 CPA Management Key 使用。两套密钥不相同时，页面会自动尝试后返回“Management Key 无效”，用户仍需重复输入 CPA 密钥。

0.6.1 增加专用会话代理：

- 通过 CPAMP 内网 `/status` 实时验证当前管理密钥，再建立 12 小时 HttpOnly、Secure、SameSite=Strict 会话；代理不保存 CPAMP 密钥副本。
- CPA Management Key 只从服务器 secret 文件读取，并仅用于代理到 CPA 内网管理接口。
- 浏览器不再把管理密钥写入 `sessionStorage`、`localStorage`、URL、普通 cookie 或请求日志。
- 代理只开放上游监控所需的路径与 HTTP 方法；写操作要求同源请求标记。
- 自动认证失败时恢复手动连接区；独立打开页面时可手动输入 CPAMP 管理密钥。

## 验证

- 会话代理单元测试、竞态测试和 `go vet`。
- 插件完整 Go 测试与竞态测试。
- Playwright 38 项 UI 回归。
- 生产浏览器流程：登录 CPAMP、进入上游监控、连接区隐藏、账户数据加载。

0.6.0 到 0.6.1 不改变偏好或缓存格式。部署时必须同时启动会话代理并增加同源 HTTPS 下的 `/upstream-monitor/` Nginx 路由；只替换插件不会完成本修复。分架构安装、代理配置与验证方法见 [中文使用指南](./USER_GUIDE.md#安装与升级)。
