# 上游账户监控设计系统

## Product Surface

CPA/CPAMP 上游监控管理页。用户在桌面浏览器中进行高频检查，也可能在手机上快速确认异常账户。

## Visual Direction

克制的浅色运维控制台：冷白背景、深色正文、蓝色主动作、绿色/琥珀/红色仅用于状态。内容优先，边界清晰，避免大面积装饰。

## Color Tokens

- Canvas: `#f7f9fc`
- Surface: `#ffffff`
- Muted surface: `#f1f4f8`
- Border: `#d8dee8`
- Ink: `#172033`
- Muted ink: `#617087`
- Accent: `#315fd3`
- Success: `#147d55`
- Warning: `#a96800`
- Danger: `#b42318`
- Unknown: `#6b5ca5`

## Typography

使用系统无衬线字体，中文优先。正文 14px，辅助信息 11–13px，页面标题 26px。金额、额度和百分比使用 `font-variant-numeric: tabular-nums`，避免数字跳动影响比较。

## Layout

页面最大宽度 1540px。首屏顺序是页面标题、连接状态、摘要、标签页、账户工作区。监控页在桌面采用左侧账户导航和右侧详情，在窄屏切换为账户下拉选择。

## Components

- Status badge: 文字加状态点，不能只靠颜色。
- Summary strip: 展示账户总数、健康数、需关注数和不可用数。
- Account rail: 名称、Provider、计费类型、主指标和状态。
- Detail panel: 当前情况、余额、账户总额、当前 Key、周期额度、计费倍率、速率限制、Token/费用、每日用量、模型统计和来源诊断。
- Provider configuration: CPA 供应商目录、监控开关、Adapter 选择、NewAPI 管理 PAT 和清除记录操作。
- Token connection bar: CPA Management Key 输入和明确的连接/刷新动作，不把凭据写入 URL。

## Interaction

打开页面先读取持久化快照；过期时后台刷新。手动刷新是显式动作。筛选包括全部、余额、额度和需关注。取消勾选只移出监控，不删除 CPA 配置；清除记录单独执行。错误和过期快照在账户导航和详情区都要有文字说明。
