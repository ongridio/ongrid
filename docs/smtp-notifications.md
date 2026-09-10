# SMTP 邮件告警

在「设置 → 通知 → 添加通知渠道」选择「邮件 (SMTP)」，填写并保存：

| 字段 | 说明 |
| --- | --- |
| 名称 | 渠道名称，例如 `ops-email` |
| SMTP 服务器 | 邮箱服务的主机名，不包含协议或端口 |
| 端口、连接加密 | 通常为 STARTTLS + 587，或 TLS + 465，以邮件服务商要求为准 |
| 发件人邮箱 | 邮件服务允许使用的发件地址，可带显示名称 |
| 收件人邮箱 | 用逗号分隔多个邮箱，最多 100 个 |
| 用户名、密码 / 授权码 | SMTP 认证信息；使用邮箱服务提供的授权码或应用密码。免认证 TLS 中继可留空用户名 |

保存后点击「测试」，确认收件箱收到测试邮件，再在告警规则中选择该渠道。现有渠道启用状态、规则筛选、投递记录和失败重试机制同样适用。

密码不会通过列表或详情 API 返回。编辑时留空保留旧值，输入新密码完成轮换，输入 `-` 清除。密码沿用现有通知渠道的数据库配置存储方式，因此数据库及备份应按凭据数据管理。本功能通过 UI/API 配置，不新增 `ONGRID_NOTIFY_SMTP_*` 环境变量。

发送器校验系统信任链和主机名，最低 TLS 1.2，不支持明文 SMTP、跳过证书验证或 OAuth2；认证使用 AUTH PLAIN，且只在 TLS 建立后发送。使用内部 CA 时需将其加入 manager 的系统信任链。服务端不提供 STARTTLS 时会失败，不会降级到明文。

邮件采用 UTF-8 纯文本，包含标题、等级、来源和告警正文。SMTP DATA 确认代表邮件服务器已接收；最终投递和垃圾邮件过滤由邮件服务负责。DATA 确认后的 QUIT 失败不会触发重复发送。

管理员 API 使用既有 `/api/v1/notification-channels` 路径；`type` 为 `smtp`，配置对象为：

```json
{
  "name": "ops-email",
  "type": "smtp",
  "enabled": true,
  "smtp": {
    "host": "smtp.example.com",
    "port": 587,
    "tls_mode": "starttls",
    "username": "alerts@example.com",
    "from": "Ongrid <alerts@example.com>",
    "to": ["ops@example.com", "oncall@example.com"]
  }
}
```

需要认证时通过同级 `secret` 字段提交密码。`endpoint` 仅供 Webhook 渠道使用，SMTP 无需填写。
