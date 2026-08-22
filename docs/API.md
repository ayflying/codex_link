# Codex Relay Server API

这是中心服务端的 HTTP API。浏览器、手机和外部系统只连接服务端；安装 Codex 的本机客户端使用单独的 WebSocket 通道连接服务端，不会开放本地 HTTP 端口。

服务地址示例：

```text
https://codex.example.com
```

完整部署与客户端登录说明见 [RELAY.md](RELAY.md)。机器可读描述：

```text
GET /api/openapi.json
```

## 认证

网页通过 Cookie 登录；外部 API 可使用 Bearer Token。Token 在网页登录后创建：

```powershell
$base = "https://codex.example.com"
$token = (Invoke-RestMethod "$base/api/auth/token" -Method POST -WebSession $session).token
$headers = @{ Authorization = "Bearer $token" }
Invoke-RestMethod "$base/api/devices" -Headers $headers
```

Token 只会在创建时返回一次。

## 账号

### POST /api/auth/register

创建服务端账号。服务端管理员可设置 `ALLOW_REGISTRATION=false` 关闭注册。

```json
{
  "username": "alice",
  "password": "至少 8 位的密码"
}
```

### POST /api/auth/login

使用同一份账号登录网页或 API。

```json
{
  "username": "alice",
  "password": "密码"
}
```

### POST /api/auth/password

修改当前账号密码。

```json
{
  "currentPassword": "旧密码",
  "newPassword": "至少 8 位的新密码"
}
```

### GET | POST | DELETE /api/auth/token

- `GET` 查询 Token 是否已启用，不返回完整 Token。
- `POST` 创建或轮换 Token。
- `DELETE` 删除 Token。

## 本机客户端

### POST /api/agent/login

本机 Go agent 首次登录时使用网页同一账号，服务端返回仅属于该设备的令牌。

```json
{
  "username": "alice",
  "password": "密码",
  "deviceId": "客户端随机 ID",
  "deviceName": "办公室电脑"
}
```

### GET /api/agent/ws

本机 agent 的 WebSocket 连接。客户端通过请求头传递设备令牌：

```text
Authorization: Bearer <agent-token>
```

并在 URL query 中传递非敏感设备 ID：

```text
/api/agent/ws?deviceId=<device-id>
```

该接口不面向浏览器或第三方业务调用。

## 设备和会话

### GET /api/devices

返回当前账号的客户端和在线状态。多数接口可附带 `?deviceId=<id>` 指定某台在线电脑；未指定时服务端会选择当前账号下的在线客户端，已有会话则固定转发到它所属的客户端。

### GET /api/threads

从选中的在线客户端读取 Codex 历史对话并同步到服务端。

### POST /api/threads/{id}/resume

恢复已有对话。

### DELETE /api/threads/{id}

归档 Codex 对话并删除服务端对应缓存。

### GET /api/sessions

读取服务端已同步的会话缓存。

### POST /api/sessions

在选中的本机客户端上新建 Codex 会话：

```json
{
  "prompt": "检查当前项目"
}
```

### POST /api/sessions/{id}/messages

发送消息，服务端会经 WebSocket 转发给本机 agent。`202` 表示客户端已接收命令；流式输出从 SSE 读取。

```json
{
  "text": "继续处理",
  "attachments": []
}
```

### GET /api/sessions/{id}/events

获取 SSE 流式事件。支持 `?after=<eventId>` 或 `Last-Event-Id` 断线续传。

事件类型包括：

- `session.status`
- `user.message`
- `assistant.delta`
- `tool.started`
- `tool.output`
- `approval.requested`
- `approval.resolved`
- `turn.done`
- `error`

### POST /api/sessions/{id}/approvals

```json
{
  "approvalId": "approval-id",
  "decision": "approved"
}
```

`decision` 可为 `approved` 或 `rejected`。

### POST /api/sessions/{id}/cancel

取消当前 turn。

## 图片和设置

### POST /api/uploads

上传 Data URL 图片。服务端将图片保存到持久化卷，并在转发消息时传给本机 agent，由 agent 写入自己的临时附件目录后交给 Codex 读取。

```json
{
  "name": "screen.png",
  "mimeType": "image/png",
  "dataUrl": "data:image/png;base64,..."
}
```

单张图片上限为 10MB。

### GET | POST /api/settings

读取或更新选中客户端上的 Codex 设置：

```json
{
  "approvalMode": "on-request",
  "workMode": "edit"
}
```

可选值：

- `approvalMode`: `on-request`、`on-failure`、`never`
- `workMode`: `edit`、`plan`
