# Codex Remote 中心服务 / 本机客户端

中心服务端使用 Docker Compose 部署，MySQL 保存账号、Token、设备、会话和事件，`data` 卷仅保存普通控制台回退时的附件。运行 Codex 的电脑只运行轻量客户端 Agent；它主动通过 WebSocket 连接服务端，因此客户端不开放 HTTP 端口，也不需要远程桌面。

```text
控制面：浏览器 -- HTTPS / WSS --> Relay <-- Agent WebSocket -- Agent --> Codex
         登录、设备、SDP/ICE 信令          登记、保活、信令、普通业务回退入口

控制台数据面：浏览器 === WebRTC DataChannel === Agent --> Codex                 [优先]
              浏览器 -- HTTP/SSE --> Relay -- Agent WebSocket --> Agent --> Codex [回退]

端口映射数据面：外部 TCP --> Relay 公开端口 === 独立 DataChannel === Agent --> 目标主机
```

Relay 接收控制面请求并提供普通控制台回退，但不会在 P2P 成功时承载控制台业务字节。端口映射中，Relay 必须接收外部 TCP 连接；仅 Relay 到 Agent 的这一段严格要求 P2P，且没有 Agent WebSocket/HTTP/TURN 兜底。

## 部署服务端

先复制环境变量示例。Compose 已提供默认密码，生产环境建议在 `.env` 中覆盖为强密码：

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

## 版本与镜像标签

仓库根目录的 `VERSION` 保存客户端和 relay 共用的三段式版本号，初始基线为 `0.2.3`。本地首次使用时执行：

```powershell
.\scripts\install-git-hooks.ps1
```

Git hook 会在每次提交前自动递增修订号，例如 `0.2.3` 到 `0.2.4`。每次推送到 `main` 后，镜像 CI 会自动构建并推送服务器镜像。要发布客户端时，为包含目标改动的提交创建并推送与 `VERSION` 一致的标签：

```powershell
$version = (Get-Content VERSION -Raw).Trim()
git tag "v$version"
git push origin main --follow-tags
```

客户端发布 CI 只监听 `vX.Y.Z` 标签，在 GitHub Runner 上测试并交叉编译 Windows x64 客户端，压缩为 `codex-remote-agent-windows-amd64.zip`，创建同名 GitHub Release，仅发布压缩包。镜像 CI 独立监听推送到 `main` 的代码，在 GitHub Runner 上构建并推送 `ghcr.io/ayflying/codex_link:<VERSION>` 与 `ghcr.io/ayflying/codex_link:latest`。两条 CI 都不依赖本地机器或远程构建服务器。Compose 默认使用 `latest`；需要回滚或跳转版本时，把镜像改为 `ghcr.io/ayflying/codex_link:0.2.3` 等具体标签后重新部署。CI 不会自动操作生产主机，仍需手动执行 `docker compose up -d --pull always`。`scripts\publish-relay.ps1` 和 `scripts\package-remote.ps1` 仅作为 GitHub Actions 不可用时的手动兼容路径。

账号、Token、设备、会话和事件保存在 MySQL volume `mysql_data`，服务端中转的文件保存在 Docker volume `data`。P2P 模式下文件直接写入 agent 的本地 `data-remote-agent/uploads`，不会经过 relay。新数据库从空数据开始，不会导入旧的 `relay-store.json`。服务端不保存本机 Codex/CCS/API Key。

服务端启动时会自动执行 `cmd/codex-relay-server/migrations` 下的数据库迁移。迁移文件使用 `001_init.sql`、`002_add_xxx.sql` 这样的递增版本号；已执行版本会记录在 MySQL 的 `schema_migrations` 表中，空数据库会自动建表，已有旧数据库会自动补齐当前版本。已执行迁移的名称或内容被修改时，服务端会拒绝启动，应该新增更高版本的迁移文件修正 schema。当前版本也会通过认证后的 `/api/health` 返回 `schemaVersion`。

## P2P、STUN 与回退

网页选择在线设备后，会通过已登录网页会话连接 `/api/p2p/ws`，relay 只转发 SDP/ICE 信令。浏览器和 agent 建立 WebRTC DataChannel 后，线程、会话命令、事件和文件分块上传都走直连；页面状态栏显示“P2P 直连”。如果浏览器不支持 WebRTC、UDP 打洞失败、STUN 不可达或 DataChannel 中断，页面状态会显示“服务端中转”，自动恢复原来的 HTTP/SSE/WebSocket 路径。

relay 自带一个 STUN-only UDP 监听器，容器端口为 `8787`，Compose 将它映射到宿主机 UDP `18787`，与网页 TCP `18787` 共用端口号。TCP 和 UDP 使用独立的端口空间，二者不会冲突。它只响应 STUN Binding 请求，返回请求方的公网映射地址，不接收、转发或中继 WebRTC DataChannel 数据，因此这个端口不会产生 TURN 中转流量。公网防火墙需要同时放行 `18787/tcp` 和 `18787/udp`。

## 自定义端口映射

工作区侧栏的“P2P 端口映射”用于远程调试。每个登录用户都可以选择自己设备、目标主机地址和目标端口，再设置服务端公开 TCP 端口。目标主机由所选设备连接：填写 `127.0.0.1` 表示设备本机；也可填写该设备能访问的局域网 IP 或主机名，例如 `192.168.1.20`、`nas.local`。服务端只在该公开端口接收连接，并为每个连接与 agent 建立独立的 WebRTC DataChannel；连接到目标主机的 TCP 服务后才开始复制字节。

这条链路是 P2P-only：打洞失败、目标设备离线或 DataChannel 断开时，外部 TCP 连接会被关闭，不会使用普通控制通道或 HTTP relay 兜底。公开端口入口经过服务端是监听连接的必要条件，但业务字节只在 P2P DataChannel 建立后转发。

Docker 不会根据数据库动态添加端口。每创建一个公开端口，都要把相同端口加入 Compose 的 TCP 发布列表，并放行宿主机防火墙，例如：

```yaml
ports:
  - "18787:8787/tcp"
  - "18787:8787/udp"
  - "19022:19022/tcp"
```

映射功能当前仅支持 TCP；服务端 HTTP 端口不能再次作为映射端口使用。

以下是 relay 的公开编排配置，不需要放入 `.env`。需要修改 STUN 地址、端口映射、公网地址或 P2P-only 策略时，直接编辑 `docker-compose.yml` 中 relay 的 `environment` 和 `ports`：

```yaml
environment:
  WEBRTC_STUN_PORT: "8787"
  WEBRTC_STUN_PUBLIC_PORT: "18787"
  WEBRTC_STUN_PUBLIC_HOST: ""
  WEBRTC_P2P_ONLY: "false"
ports:
  - "18787:8787/tcp"
  - "18787:8787/udp"
```

`WEBRTC_STUN_PUBLIC_PORT` 是宿主机 UDP 映射端口，`WEBRTC_STUN_PORT` 是容器内监听端口；当前容器内 TCP 网页服务和 UDP STUN 都使用 `8787`，宿主机也都使用 `18787`。如果修改宿主机端口，需要同步修改 `WEBRTC_STUN_PUBLIC_PORT` 和 `ports` 中的 TCP/UDP 映射；两条映射不能合并，否则未标注协议的映射只会发布 TCP。`WEBRTC_STUN_PUBLIC_HOST` 留空时，relay 会从网页请求的 Host 自动生成候选地址。当前默认只使用 relay 自己的 STUN-only 服务；只有明确通过环境变量 `WEBRTC_STUN_SERVERS` 增加地址时，才会额外使用外部 STUN 服务。

默认 `WEBRTC_P2P_ONLY=false`，打洞失败时仍兼容服务端 HTTP/SSE/WebSocket 回退。设置为 `true` 后，业务请求、事件流、图片上传以及 agent 的事件/会话通知在 P2P 未建立或中断时直接失败或丢弃，绝不回退到服务端中转；网页登录、设备发现和 P2P 信令仍需要通过 relay 完成。项目没有配置 TURN，无法打洞的网络在该模式下不可用。

## 构建客户端

正式客户端从 GitHub Releases 下载 `codex-remote-agent-windows-amd64.zip`。客户端每次启动都会检查最新正式 Release；只有版本更高、压缩包下载完成、文件大小有效且包含固定 exe 时才会替换自身并以原参数重启。GitHub 不可用、下载失败或压缩包损坏时会继续运行当前版本。

以下命令保留用于离线手动交付：

```powershell
.\scripts\package-remote.ps1
```

该脚本会连接通过 `-Remote` 或 `CODEX_LINK_BUILD_SERVER` 指定的远程服务器进行交叉编译，生成：

```text
release\codex-remote-agent\codex-remote-agent.exe
```

例如：

```powershell
$env:CODEX_LINK_BUILD_SERVER = "root@your-build-host"
.\scripts\package-remote.ps1
```

把整个 `release\codex-remote-agent` 目录复制到安装 Codex 的 Windows 电脑。

直接双击 `codex-remote-agent.exe` 会打开图形客户端，填写服务端地址和 Token 后即可连接。关闭窗口只会隐藏到 Windows 系统托盘，点击托盘图标恢复窗口；在托盘图标上点击右键并选择“退出客户端”才会结束客户端进程。命令行 `login` 和 `agent` 模式仍然可用。

## 登录并启动客户端

第一次在安装 Codex 的电脑执行：

```powershell
.\codex-remote-agent.exe login `
  --server "https://你的服务端地址" `
  --token "网页 Token" `
  --device "办公室电脑"
```

Token 可以从网页的“账号安全 -> 客户端 Token”中创建和复制。也可以不填写 `--token`，程序会在控制台交互式读取。

登录命令只负责向服务端登记设备并保存本机配置，完成后会正常退出；请再执行下面的常驻命令：

```powershell
.\codex-remote-agent.exe agent
```

或双击 `start-agent.cmd`。同一个数据目录重复登录同一个服务端会复用原设备 ID，不会新增重复设备；网页设备列表可以删除离线设备以清理历史重复记录。客户端配置默认保存为 Windows 的 `%LOCALAPPDATA%\Codex Link\remote-agent\remote-agent.json`，包含服务端地址、Token、设备 ID 和设备名称；同目录还会保存本机 Codex 会话缓存。客户端每次启动和重连都会校验 Token；Token 刷新或删除后，客户端需要重新登录。网络故障会每 5 秒自动重连。首次启动会迁移发布目录下旧的 `data-remote-agent` 文件。

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
