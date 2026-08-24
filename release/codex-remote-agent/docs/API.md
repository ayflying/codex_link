# Codex Link API

中心服务端提供网页、手机和外部系统使用的 HTTP API。运行 Codex 的电脑使用 Go 客户端，通过主动建立的 WebSocket 连接接入服务端，不开放本机 HTTP 端口。网页控制台会优先使用 WebRTC DataChannel 直连 agent，失败时自动使用下面的 HTTP/SSE API。

默认服务地址：

```text
http://<服务端地址>:18787
```

机器可读接口描述：

```text
GET /api/openapi.json
```

## 认证方式

网页登录使用 HttpOnly Cookie。外部调用和本机客户端使用 `Authorization: Bearer <Token>`。

Token 只能在网页登录后创建、刷新或删除。完整 Token 保存在服务端 MySQL 中，并会显示在当前账号的 Token 管理页面；不要把 Token 提交到 Git 或写入公开日志。

```powershell
$base = "http://127.0.0.1:18787"
$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
Invoke-RestMethod "$base/api/auth/login" -Method POST -WebSession $session `
  -ContentType "application/json" -Body '{"username":"alice","password":"至少 8 位的密码"}'

$token = (Invoke-RestMethod "$base/api/auth/tokens" -Method POST -WebSession $session `
  -ContentType "application/json" -Body '{"name":"脚本调用"}').token.token

$headers = @{ Authorization = "Bearer $token" }
Invoke-RestMethod "$base/api/devices" -Headers $headers
```

除注册、登录、客户端登录、OpenAPI 外，`/api/*` 接口都需要 Cookie 或 Bearer Token。未认证请求返回 `401`。

## 账号

### POST `/api/auth/register`

注册账号。只有 `ALLOW_REGISTRATION=true` 时允许注册。

```json
{
  "username": "alice",
  "password": "至少 8 位的密码"
}
```

### POST `/api/auth/login`

```json
{
  "username": "alice",
  "password": "密码"
}
```

### GET `/api/auth/status`

返回当前网页会话状态和当前账号的 Token 列表。已登录时还会返回 `userId`、`username` 和 `isAdmin`；未登录时 `authenticated` 为 `false`。

### POST `/api/auth/logout`

删除当前网页登录会话。

### POST `/api/auth/password`

```json
{
  "currentPassword": "旧密码",
  "newPassword": "至少 8 位的新密码"
}
```

## 系统管理

系统管理接口只允许管理员调用。第一个注册用户自动成为管理员；已有数据库执行迁移后，注册时间最早的用户会被设置为管理员。删除最后一个管理员以外的管理员账号后，系统会把剩余账号中注册最早的一个设置为管理员；系统始终至少保留一个管理员。

### GET `/api/admin/users`

返回用户列表，仅包含用户 ID、用户名、管理员状态和注册时间。

### PATCH `/api/admin/users/{id}`

设置其他用户的管理员状态：

```json
{
  "isAdmin": true
}
```

不能修改当前登录账号的管理员状态；取消最后一个管理员会返回 `409`。

### DELETE `/api/admin/users/{id}`

删除其他用户及其设备、秘钥、会话和图片元数据。删除最后一个管理员后，会自动提升剩余账号中注册最早的用户。

### 端口映射

端口映射只允许管理员操作，当前协议固定为 TCP。服务端监听 `listenPort`，每个连接都会单独与目标设备建立 WebRTC DataChannel，再连接客户端本机的 `targetHost:targetPort`。P2P 打洞失败会关闭该 TCP 连接，绝不会回退到 agent WebSocket 或 HTTP relay。

```text
GET    /api/admin/port-mappings
POST   /api/admin/port-mappings
PATCH  /api/admin/port-mappings/{id}
DELETE /api/admin/port-mappings/{id}
```

创建或更新请求：

```json
{
  "deviceId": "设备 ID",
  "name": "远程调试",
  "targetHost": "127.0.0.1",
  "targetPort": 9222,
  "listenPort": 19022,
  "protocol": "tcp",
  "enabled": true
}
```

`listenPort` 还必须在宿主机防火墙和 `docker-compose.yml` 中显式发布，例如 `19022:19022/tcp`。映射端口不能使用服务端 HTTP 端口。API 返回 `listening`、`p2pConnected` 和 `lastError`，用于查看监听及当前连接状态。

## Token 管理

### GET `/api/auth/tokens`

返回当前账号拥有的全部 Token，包括名称、完整值、脱敏前缀、创建/刷新时间和最后使用设备。

### POST `/api/auth/tokens`

创建一个 Token。

```json
{
  "name": "办公室电脑"
}
```

响应中的 `token.token` 是完整 Token，例如 `crs_xxx`。

### POST `/api/auth/tokens/:id/refresh`

刷新指定 Token。Token ID 不变，旧完整值立即对新连接失效；已建立的 WebSocket 不会被服务端主动断开。

### DELETE `/api/auth/tokens/:id`

删除指定 Token。已建立的 WebSocket 不会被服务端主动断开，但客户端下次启动或重连时校验会失败。

### 兼容接口 `/api/auth/token`

保留旧路径：

- `GET` 等价于 `GET /api/auth/tokens`。
- `POST` 等价于创建 Token，可传 `{ "name": "..." }`。
- `DELETE` 使用 query 参数 `tokenId`，例如 `/api/auth/token?tokenId=<id>`。

## 本机客户端

### POST `/api/agent/login`

客户端使用网页创建的 Token 注册或更新设备。客户端只保存 Token、设备 ID 和服务端地址，不保存网页密码。

```json
{
  "token": "crs_xxx",
  "deviceId": "客户端生成的设备 ID",
  "deviceName": "办公室电脑"
}
```

### GET `/api/agent/validate?deviceId=<id>`

客户端启动和重连前校验 Token 与设备绑定关系。Token 无效、已刷新、已删除或设备不属于当前 Token 时返回 `401`。

### GET `/api/agent/ws?deviceId=<id>`

客户端 WebSocket 反向连接。请求头必须包含：

```text
Authorization: Bearer <Token>
```

客户端登录示例：

```powershell
codex-remote-agent.exe login `
  --server "http://服务端地址:18787" `
  --token "crs_xxx" `
  --device "办公室电脑"
```

### WebRTC 信令 `/api/p2p/ws`

网页使用当前登录 Cookie 连接：

```text
GET /api/p2p/ws?deviceId=<设备 ID>&clientId=<本次网页连接 ID>
```

该 WebSocket 只转发 `p2p.signal` 消息中的 SDP offer/answer 和 ICE candidate，不承载会话内容或图片。`connected` 消息的 payload 包含 `iceServers` 和 `p2pOnly`；浏览器会将 `iceServers` 放入 offer，agent 使用 offer 中的同一组 STUN 地址。建立 DataChannel 后，浏览器使用与 agent WebSocket 相同的 `command`、`response`、`event` 和 `session` envelope。`p2pOnly=true` 时，P2P 失败不会回退到服务端中转。

## 设备和会话

### GET `/api/devices`

返回当前账号的设备列表，包含设备名称、在线状态、最后连接时间、Token 名称和脱敏前缀。离线设备不能执行控制台操作。

### DELETE `/api/devices/{id}`

删除当前账号的离线设备记录；在线设备必须先停止客户端 agent。历史会话和事件记录会保留。

### GET `/api/health`

返回服务端模式、MySQL 状态、当前数据库迁移版本、当前账号的在线设备数量和设备列表。该接口仍受认证保护。

### GET `/api/threads`

从选中的在线客户端读取 Codex 历史对话并同步会话元数据。可使用 `?deviceId=<设备 ID>` 指定设备；网页控制台在 P2P 直连时会改用 DataChannel 的 `threads.list` 命令。

### GET `/api/models`

读取在线客户端通过 Codex app-server 暴露的可用模型目录。网页控制台在 P2P 直连时会改用 DataChannel 的 `models.list` 命令；模型选择会保存到客户端设置，并作用于新会话和后续消息。

### GET `/api/sessions`

读取服务端缓存的会话元数据，按 `updated_at` 倒序返回。可使用 `?deviceId=<设备 ID>` 筛选。

### POST `/api/sessions`

创建新会话并转发到在线客户端。

```json
{
  "prompt": "用中文说明当前项目状态"
}
```

### POST `/api/threads/:id/resume`

恢复指定历史对话。服务端只补齐最近的事件缓存，避免长对话一次性加载。

### DELETE `/api/threads/:id`

归档 Codex 对话，并删除服务端对应的会话和事件缓存。

## 消息、审批和流式事件

### GET `/api/sessions/:id/events`

SSE 事件流。使用 `after` query 参数或 `Last-Event-ID` 请求头补齐事件，例如：

```text
GET /api/sessions/<session-id>/events?after=120
```

服务端最多回放 `EVENT_BACKLOG_LIMIT` 条事件，默认 120 条；实时事件仍通过进程内广播发送。事件类型包括：

`session.status`、`user.message`、`assistant.delta`、`tool.started`、`tool.output`、`approval.requested`、`approval.resolved`、`turn.done`、`context.usage`、`queue.changed`、`goal.updated`、`goal.cleared`、`error`。

### POST `/api/sessions/:id/messages`

发送消息，可附带已上传的文件附件。

```json
{
  "text": "请检查这些附件",
  "attachments": [
    { "id": "附件 ID" }
  ]
}
```

### GET `/api/sessions/:id/queue`

读取当前线程的原生 Codex 待发送队列。

### POST `/api/sessions/:id/queue`

管理原生队列。`operation` 可为 `add`、`update`、`delete`、`reorder` 或 `promote`；`promote` 会把指定消息调整到当前运行中的 turn，空闲时则直接启动它。

```json
{
  "operation": "add",
  "text": "等当前操作完成后再运行测试",
  "attachments": [{ "id": "附件 ID" }]
}
```

### GET / POST `/api/sessions/:id/goal`

读取或设置 Codex 线程目标。POST `{ "objective": "完成发布并验证" }` 设置目标，POST `{ "clear": true }` 清除目标。

### POST `/api/sessions/:id/approvals`

提交审批决定。`decision` 使用 `approved` 或 `rejected`。

```json
{
  "approvalId": "审批请求 ID",
  "decision": "approved"
}
```

### POST `/api/sessions/:id/cancel`

取消当前 turn。

## 文件附件

### POST `/api/uploads`

上传 Base64 文件。文件二进制写入 Docker `data` volume 的 `UPLOAD_DIR`，MySQL 只保存文件路径、名称、类型、大小和所属用户。

```json
{
  "name": "截图.png",
  "mimeType": "image/png",
  "dataUrl": "data:image/png;base64,..."
}
```

单个文件上限为 16 MB。响应返回附件 ID，之后在消息或队列中引用该 ID。

### GET `/api/uploads/:id`

读取当前用户自己的文件附件。

## 错误响应

错误响应统一为：

```json
{
  "error": "错误说明"
}
```

常见状态码：`400` 请求无效，`401` 未登录或 Token 无效，`404` 资源不存在，`409` 资源冲突，`502` 本机 Codex 客户端转发失败，`503` 没有在线设备。
