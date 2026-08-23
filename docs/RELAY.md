# Codex Remote 中心服务 / 本机客户端

中心服务端使用 Docker Compose 部署，MySQL 保存账号、Token、设备、会话和事件，`data` 卷保存图片文件。运行 Codex 的电脑只运行轻量客户端 agent；它主动通过 WebSocket 连接服务端，因此客户端不开放 HTTP 端口，也不需要远程桌面。

```text
手机或浏览器 <-> 中心服务端（网页 / API / SSE） <-> WebSocket <-> 本机 agent <-> 本机 Codex
```

## 部署服务端

先复制环境变量示例并设置两个 MySQL 密码：

```bash
cp .env.example .env
```

编辑 `.env` 后，在服务端目录运行。Compose 会始终拉取 GHCR 上的 `latest` 镜像，不需要在部署机器上安装 Go 或 Node：

```bash
docker compose up -d --pull always
```

使用私有 GHCR 镜像时，先登录：

```bash
echo "$CR_PAT" | docker login ghcr.io -u ayflying --password-stdin
```

默认访问地址为：

```text
http://<服务端地址>:18787
```

首次在网页创建账号。建议创建完第一个账号后，把 `docker-compose.yml` 同目录的 `.env` 设置为：

```text
ALLOW_REGISTRATION=false
```

配置完成后重新拉取并重建容器：

```bash
docker compose up -d --pull always
```

账号、Token、设备、会话和事件保存在 MySQL volume `mysql_data`，图片文件保存在 Docker volume `data`。新数据库从空数据开始，不会导入旧的 `relay-store.json`。服务端不保存本机 Codex/CCS/API Key。

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
  --token "网页 Token" `
  --device "办公室电脑"
```

Token 可以从网页的“账号安全 -> 客户端 Token”中创建和复制。也可以不填写 `--token`，程序会在控制台交互式读取。

登录成功后执行：

```powershell
.\codex-remote-agent.exe agent
```

或双击 `start-agent.cmd`。客户端配置保存为本机 `data-go\remote-agent.json`，包含服务端地址、Token、设备 ID 和设备名称。客户端每次启动和重连都会校验 Token；Token 刷新或删除后，客户端需要重新登录。网络故障会每 5 秒自动重连。

网页和客户端使用同一个服务端账号。登录网页后先选择设备，再进入对应 Codex 控制台；设备卡片会显示在线状态和使用的 Token。对话、输出、审批、图片和取消操作都会由服务端转发到所选客户端。

## API 与 WebSocket

浏览器与外部系统继续通过服务端 HTTP API 调用，地址为：

```text
GET /api/openapi.json
```

需要登录后调用的 API 可以使用 Cookie，或在网页账号下创建 `Bearer Token`。本机客户端不对外暴露 API；它使用：

```text
POST /api/agent/login
GET  /api/agent/validate
GET  /api/agent/ws
```

其中 WebSocket 仅由已登录客户端使用设备令牌连接。不要把客户端端口暴露到网络。
