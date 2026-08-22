# Codex Remote API 文档

本服务默认地址：

```text
http://127.0.0.1:8787
```

局域网或 Tailscale 访问时，把主机换成对应 IP：

```text
http://<tailscale-ip>:8787
```

不要把服务裸露到公网。API 不会返回 Codex/CCS/API Key 配置内容。

## 认证

首次未设置网页访问密码时，所有 API 默认可调用。设置密码后，API 支持两种认证方式：

- 浏览器 Cookie：调用 `/api/auth/login` 后自动写入 HttpOnly Cookie，适合网页。
- Bearer Token：调用 `/api/auth/token` 创建，适合脚本、外部系统、自动化调用。

创建或轮换 API Token：

```powershell
$base = "http://127.0.0.1:8787"
$login = Invoke-WebRequest "$base/api/auth/login" `
  -Method POST `
  -ContentType "application/json" `
  -Body '{"password":"你的访问密码"}' `
  -SessionVariable s

$tokenResult = Invoke-RestMethod "$base/api/auth/token" `
  -Method POST `
  -WebSession $s

$token = $tokenResult.token
$token
```

完整 Token 只会在创建时返回一次。之后调用 API：

```powershell
$headers = @{ Authorization = "Bearer $token" }
Invoke-RestMethod "$base/api/health" -Headers $headers
```

删除 API Token：

```powershell
Invoke-RestMethod "$base/api/auth/token" -Method DELETE -Headers $headers
```

机器可读 OpenAPI：

```text
GET /api/openapi.json
```

## 通用约定

- 请求体和响应体使用 JSON，除 SSE 外。
- 错误响应格式：`{"error":"错误说明"}`
- SSE 使用 `text/event-stream`，事件数据里的 JSON 结构为 `RemoteEvent`。
- 断线重连可传 `?after=<lastEventId>`，也可使用 `Last-Event-Id` 请求头。

## 主要流程

1. `GET /api/threads` 获取 Codex 历史对话。
2. `POST /api/threads/{id}/resume` 恢复某个历史对话，得到 session。
3. `GET /api/sessions/{id}/events` 打开 SSE 事件流。
4. `POST /api/sessions/{id}/messages` 发送消息。
5. 如收到 `approval.requested`，调用 `POST /api/sessions/{id}/approvals` 批准或拒绝。

新建对话则直接调用 `POST /api/sessions`。

## 接口

### GET /api/auth/status

查看认证状态。

响应示例：

```json
{
  "authenticated": true,
  "passwordSet": true,
  "apiToken": {
    "enabled": true,
    "prefix": "crm_abc12345",
    "createdAt": "2026-06-05T10:00:00+08:00"
  }
}
```

### POST /api/auth/login

网页 Cookie 登录。

请求：

```json
{
  "password": "你的访问密码"
}
```

### POST /api/auth/password

设置或修改网页访问密码。

请求：

```json
{
  "currentPassword": "旧密码，首次设置可为空",
  "newPassword": "新密码"
}
```

### GET /api/auth/token

查看 API Token 状态，不返回完整 Token。

### POST /api/auth/token

创建或轮换 API Token。

响应：

```json
{
  "token": "crm_xxx",
  "status": {
    "enabled": true,
    "prefix": "crm_xxx",
    "createdAt": "2026-06-05T10:00:00+08:00"
  },
  "note": "请立即保存 token，服务端不会再次显示完整值。"
}
```

### DELETE /api/auth/token

删除当前 API Token。

### GET /api/health

健康检查，返回 Go 服务、Codex runtime、当前设置状态。

### GET /api/settings

读取运行设置。

### POST /api/settings

更新运行设置。

请求：

```json
{
  "approvalMode": "on-request",
  "workMode": "edit"
}
```

可选值：

- `approvalMode`: `on-request`, `on-failure`, `never`
- `workMode`: `edit`, `plan`

### GET /api/threads

列出 Codex 历史对话。

响应：

```json
{
  "sessions": [
    {
      "id": "019e...",
      "title": "查找 Codex 手机应用",
      "mode": "host-new-session",
      "status": "done",
      "createdAt": "2026-05-31T00:29:27+08:00",
      "updatedAt": "2026-06-04T14:13:06+08:00",
      "cwd": "C:\\Users\\ay\\Documents\\Codex\\app-codex"
    }
  ]
}
```

### POST /api/threads/{id}/resume

恢复已有 Codex 对话，返回可继续发送消息的 session。

### DELETE /api/threads/{id}

归档/删除 Codex 对话，并同步移除本地缓存。

### GET /api/sessions

列出本地会话缓存。

### POST /api/sessions

新建 Codex 会话。

请求：

```json
{
  "prompt": "你好，帮我检查当前项目"
}
```

### GET /api/sessions/{id}/events

SSE 事件流。

示例：

```powershell
curl.exe -N `
  -H "Authorization: Bearer $token" `
  "$base/api/sessions/$sessionId/events"
```

事件类型：

- `session.status`
- `user.message`
- `assistant.delta`
- `tool.started`
- `tool.output`
- `approval.requested`
- `approval.resolved`
- `turn.done`
- `error`

### POST /api/sessions/{id}/messages

发送消息。

请求：

```json
{
  "text": "继续，修复这个问题",
  "attachments": []
}
```

响应状态码为 `202` 表示已提交，实际输出从 SSE 读取。

### POST /api/sessions/{id}/approvals

提交审批决定。

请求：

```json
{
  "approvalId": "approval-id",
  "decision": "approved"
}
```

`decision` 可选 `approved` 或 `rejected`。

### POST /api/sessions/{id}/cancel

取消当前 turn。

### POST /api/uploads

上传图片附件。请求体使用 Data URL。

请求：

```json
{
  "name": "screenshot.png",
  "mimeType": "image/png",
  "dataUrl": "data:image/png;base64,..."
}
```

响应：

```json
{
  "attachment": {
    "id": "abc",
    "name": "screenshot.png",
    "mimeType": "image/png",
    "path": "C:\\...\\data-go\\uploads\\abc.png",
    "url": "/uploads/abc.png"
  }
}
```

把 `attachment` 放入 `/messages` 的 `attachments` 数组即可。

