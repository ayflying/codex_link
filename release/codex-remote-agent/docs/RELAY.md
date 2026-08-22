# Codex Remote 中心服务 / 本机客户端

中心服务端使用 Docker Compose 部署，网页、HTTP API、账号、设备、会话缓存和 SSE 都在服务端。运行 Codex 的电脑只运行轻量客户端 agent；它主动通过 WebSocket 连接服务端，因此客户端不开放 HTTP 端口，也不需要远程桌面。

```text
手机或浏览器 <-> 中心服务端（网页 / API / SSE） <-> WebSocket <-> 本机 agent <-> 本机 Codex
```

## 部署服务端

在服务端目录运行：

```bash
docker compose up -d --build
```

默认访问地址为：

```text
http://<服务端地址>:8787
```

首次在网页创建账号。建议创建完第一个账号后，把 `docker-compose.yml` 同目录的 `.env` 设置为：

```text
ALLOW_REGISTRATION=false
RELAY_PORT=8787
```

然后执行：

```bash
docker compose up -d
```

服务端数据保存在 Docker volume `codex-relay-data`，包括账号密码哈希、设备令牌、同步的会话元数据、最近事件和图片附件。它不保存本机 Codex/CCS/API Key。

## 构建客户端

开发电脑执行：

```powershell
.\scripts\package-remote.ps1
```

该脚本会连接 `root@192.168.50.217` 进行交叉编译，生成：

```text
release\codex-remote-agent\codex-remote-agent.exe
```

把整个 `release\codex-remote-agent` 目录复制到安装 Codex 的 Windows 电脑。

## 登录并启动客户端

第一次在安装 Codex 的电脑执行：

```powershell
.\codex-remote-agent.exe login `
  --server "https://你的服务端地址" `
  --username "网页账号" `
  --device "办公室电脑"
```

程序会在控制台提示输入密码，密码不会写进命令历史或客户端配置文件。

登录成功后执行：

```powershell
.\codex-remote-agent.exe agent
```

或双击 `start-agent.cmd`。客户端凭据保存为本机 `data-go\remote-agent.json`，只保存设备专用令牌，不保存网页登录密码和 Codex Key。连接断开时会每 5 秒自动重连。

网页和客户端使用同一个服务端账号。客户端在线后，网页顶部的设备下拉框会显示在线状态；对话、输出、审批、图片和取消操作都会由服务端转发到所选客户端。

## API 与 WebSocket

浏览器与外部系统继续通过服务端 HTTP API 调用，地址为：

```text
GET /api/openapi.json
```

需要登录后调用的 API 可以使用 Cookie，或在网页账号下创建 `Bearer Token`。本机客户端不对外暴露 API；它使用：

```text
POST /api/agent/login
GET  /api/agent/ws
```

其中 WebSocket 仅由已登录客户端使用设备令牌连接。不要把客户端端口暴露到网络。
