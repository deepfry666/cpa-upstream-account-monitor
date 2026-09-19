# 上游账户监控 0.5.4 真实接口验收记录

验收日期：2026-09-19

## 验收原则

- 全部调用均为只读查询，不发起消费请求、不修改上游账户或 Key。
- 本文只记录核对所需的脱敏数值、单位和协议结论，不记录账号名、账号 ID、组织 ID、PAT、API Key 或完整生产响应。
- 月度窗口只在官方月度已用量、月度剩余额度和账单周期均可用时生成。

## Command Code

本次线上 Command Code GOAT 只读快照的命令额度字段如下：

| 字段 | 来源 | 脱敏值 |
| --- | --- | --- |
| 5 小时窗口 | `windowLimits.fiveHour` | 已知 `used` / `cap` 和 `resetAt` |
| 一周窗口 | `windowLimits.weekly` | 已知 `used` / `cap` 和 `resetAt` |
| 月度剩余 | `credits.monthlyCredits` | `58.2984561915` |
| 月度已用 | `usage.totalMonthlyCredits` | `11.631896531499997` |
| 账单周期结束 | `subscription.currentPeriodEnd` | 已知有效 RFC3339 时间 |
| `monthlyCreditsGranted` | `credits` | 当前响应未提供 |
| `windowLimits.month` | `windowLimits` | 当前响应未提供 |

## 结论

Command Code 官方账号当前没有返回 `windowLimits.month`，但提供了本账单周期的月度已用和剩余额度。插件使用以下等价换算生成月度窗口：

```text
monthly_used = totalMonthlyCredits
monthly_remaining = monthlyCredits
monthly_total = monthly_used + monthly_remaining
monthly_reset_at = subscription.currentPeriodEnd
```

本次脱敏值换算为月度已用 `11.631897`、剩余 `58.298456`、总额 `69.930353` credits。该窗口标记为 `derived`；服务端未来若明确返回 `windowLimits.month`，插件会优先使用该 `upstream` 窗口。

没有读取或输出任何真实账号标识、凭证、完整响应或代理地址。

