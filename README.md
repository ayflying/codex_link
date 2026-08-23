# Codex Link

手机优先的 Vue 控制台，用来通过 Tailscale 远程控制本机 Codex，而不是远程桌面。

## 推荐运行方式：中心服务端 + 本机客户端

中心服务端以 Docker Compose 部署，使用 MySQL 保存账号、Token、设备、会话和事件；运行 Codex 的电脑只运行 Go 客户端，主动通过 WebSocket 连接服务端。网页和客户端登录同一个账号后会自动同步。完整部署说明见 [docs/RELAY.md](docs/RELAY.md)。

中心服务端默认通过宿主机 `18787` 端口访问，Compose 会自动拉取 `ghcr.io/ayflying/codex_link:latest` 镜像，并启动 MySQL 8.4。

旧的单机 Go 便携服务仍保留，适合仅在一台电脑的局域网直接使用。

## 旧单机运行方式：Go 便携服务

交付目录：

```text
release\codex-go-remote
```

目标电脑只需要这一个目录：

- `codex-go-remote.exe`：Go 单文件服务端，负责静态页面、HTTP API、SSE、Codex app-server bridge。
- `web\`：Vue 前端构建产物。
- `start.cmd`：双击启动。

启动后本机访问：

```text
http://127.0.0.1:8787
```

手机连上 Tailscale 后访问：

```text
http://<tailscale-ip>:8787
```

首次打开网页不需要密码。进入控制台后，点击右上角钥匙按钮可以设置访问密码；设置后再次访问 API 和网页操作都需要登录。密码只保存为本机 `DATA_DIR` 下的加盐哈希，浏览器只保存 HttpOnly session cookie。

Go 便携服务在目标电脑上不需要安装 Node、Docker，也不需要把 API Key 放进浏览器。它会读取本机已有 Codex/CCS 配置，并自动尝试发现：

```text
%LOCALAPPDATA%\OpenAI\Codex\bin\*\codex.exe
```

## 能力范围

- 查看本机 Codex 历史对话列表，按项目目录分组展示。
- 打开已有对话时只恢复最近若干 turn，避免长上下文一次性加载导致手机卡顿。
- 从手机发送消息，Go 服务通过本机 Codex app-server 创建或恢复会话，并用 SSE 流式返回输出。
- 支持审批请求、取消当前 turn、删除对话。
- 删除对话会调用 Codex `thread/archive`，并同步移除本地缓存。

目前没有稳定公开的 Codex 桌面版现有任务附着接口，所以 v1 使用 `host-new-session` / `thread/resume` 模式；不是远程桌面，也不做 UI 自动化。

## 打包

在开发机上重新生成便携目录：

```powershell
npm install
npm run package:go
```

这个命令会构建 Vue 前端，并用 Go 标准库编译服务端：

```text
release\codex-go-remote\codex-go-remote.exe
release\codex-go-remote\web
release\codex-go-remote\start.cmd
```

打包机需要 Node 和 Go；目标电脑运行便携包不需要 Node 和 Go。

## 配置

常用环境变量：

- `PORT`：服务端口，默认 `8787`
- `HOST`：监听地址，默认 `0.0.0.0`
- `CODEX_BIN`：Codex 可执行文件路径，默认自动发现，找不到时使用 `codex`
- `CODEX_CWD`：新建 Codex 会话默认工作目录
- `WEB_DIR`：前端静态文件目录，默认使用 exe 同目录下的 `web`
- `DATA_DIR`：本地缓存目录，默认 `data-go`
- `CODEX_HISTORY_TURN_LIMIT`：恢复历史时最多加载最近多少个 turn，默认 `10`
- `EVENT_BACKLOG_LIMIT`：SSE 断线重连时最多回放多少条事件，默认 `120`

示例：

```powershell
$env:CODEX_CWD = "D:\git\sonow\auto-test"
$env:PORT = "8787"
.\codex-go-remote.exe
```

## API

完整接口文档见 [docs/API.md](docs/API.md)。服务也提供机器可读 OpenAPI：

```text
GET /api/openapi.json
```

设置网页访问密码后，外部系统可以用 `POST /api/auth/token` 创建 API Token，并通过请求头调用：

```text
Authorization: Bearer <token>
```

## 安全边界

不要把中心服务端的 `18787` 或单机服务的 `8787` 直接暴露到公网。推荐只通过 Tailscale IP 访问，并使用 Tailscale ACL 和防火墙限制访问设备。

## 旧实现

仓库里仍保留了早期 Docker Compose 和 Node/TypeScript host-agent 代码，方便对照协议和后续迁移；推荐实际使用 Go 便携服务。
