# Codex Link

Codex Link 是一个手机优先的 Vue 控制台，用于通过中心服务远程操作本机 Codex。系统不使用远程桌面、屏幕控制或 UI 自动化。

本项目以 MIT License 开源，详见 [LICENSE](LICENSE)。

## 架构

系统由浏览器、Relay、本机 Agent、Codex app-server 和 MySQL 组成。它们不是一条固定链路，而是共享同一组身份与信令的四个独立平面：

| 平面 | 参与方 | 传输内容 | Relay 是否承载业务字节 |
| --- | --- | --- | --- |
| 控制面 | 浏览器、Relay、Agent | 登录、设备发现、P2P SDP/ICE 信令、Agent 登记与保活 | 否；普通控制台回退属于下方数据面 |
| 控制台数据面 | 浏览器、Agent、Codex | 对话命令、流式事件、审批、附件 | P2P 成功时否；回退时是 |
| 端口映射数据面 | 外部 TCP 客户端、Relay、Agent、目标主机 | 指定公开端口的 TCP 字节流 | 仅接入侧 `外部客户端 <-> Relay`；Relay 到 Agent 必须是 P2P DataChannel |
| 存储面 | Relay、MySQL、Relay `data` 卷、Agent 本地目录 | 账号、Token、元数据和附件 | 不适用；P2P 附件不落入 Relay |

### 组件边界

```text
                             控制面：HTTPS / WSS
浏览器  <------------------------------->  Relay 服务端  <------------------ Agent
  |             登录、设备、信令              |             Agent WebSocket（主动出站）  |
  |                                           |                                      |
  |                                           +--> MySQL + data 卷                  v
  |                                                                          Codex app-server
  |
  +---------------- 控制台数据面：WebRTC DataChannel（优先） ---------------------> Agent
```

Relay 容器的 `data` 卷仅保存服务端中转附件的二进制。Agent 本地数据目录保存 P2P 附件、客户端配置与本地会话缓存。

| 组件 | 负责内容 | 不负责内容 |
| --- | --- | --- |
| 浏览器 | 网页控制台、账号操作、设备选择、会话操作、P2P 发起 | 不直接访问 Codex 或目标电脑的本地 HTTP 端口 |
| Relay 服务端 | 网页与认证、MySQL 持久化、Agent WebSocket、P2P 信令、普通业务回退、公开端口监听 | 不保存 Codex/CCS/API Key，不运行用户的 Codex 任务 |
| 本机 Agent | 主动连接 Relay、调用本机 Codex app-server、处理 P2P 命令与附件、连接端口映射目标主机 | 不开放本机 HTTP 控制端口，不保存网页密码 |
| MySQL / `data` 卷 | 账号、Token、设备、会话、事件、附件元数据与服务端中转附件 | 不保存 P2P 直传附件的二进制或本机 Codex 配置 |
| 本机 Codex app-server | 线程、turn、模型、队列、审批和实际工具执行 | 不与公网直接通信 |

### 连接建立与三条数据链路

先建立控制面连接，再选择数据链路：

```text
1. Agent -- Token + WSS --> Relay                  （设备登记、保活、信令入口）
2. 浏览器 -- Cookie + HTTPS/WSS --> Relay          （登录、设备选择、P2P 信令入口）
3. 浏览器 <-- Relay 转发 SDP/ICE --> Agent          （仅为建立 DataChannel）
4. 按场景选择下方 1、2 或 3 的数据链路
```

控制面始终经过 Relay，但它本身不等于业务数据中转。`WEBRTC_P2P_ONLY` 只影响下面的“控制台服务端回退”链路，**不改变**网页登录、设备发现、Agent 保活或 P2P 信令。

#### 1. 控制台 P2P 直连：浏览器 <-> Agent

```text
浏览器 --(经 Relay 交换 SDP/ICE)--> Agent
浏览器 ================= WebRTC DataChannel ==================> Agent --> Codex
          对话命令、流式事件、审批和附件分块均优先走这里
```

- 浏览器选择在线设备后，Relay 只在建链阶段传递 SDP/ICE 信令。
- 建立 DataChannel 后，会话命令、事件和附件优先直接在浏览器与 Agent 之间传输。
- 目标电脑无需对外开放 HTTP 端口；Agent 始终是主动出站连接。

#### 2. 控制台服务端回退：浏览器 <-> Relay <-> Agent

```text
浏览器 -- HTTP / SSE --> Relay -- 既有 Agent WebSocket --> Agent --> Codex
```

- 仅当浏览器与 Agent 的 DataChannel 不可用，且 `WEBRTC_P2P_ONLY=false` 时启用。
- 此时 Relay 通过已有 Agent WebSocket 承载控制台命令和事件，因此它是**显式启用的普通业务中转**，不是 P2P 信令服务。
- 当 `WEBRTC_P2P_ONLY=true` 时，这条回退路径被禁用；P2P 失败会直接报错。

#### 3. P2P-only 端口映射：受控 TCP 接入 + Relay 到 Agent 的 P2P

```text
外部 TCP 客户端 -- TCP --> Relay 公开端口
                                || 为本次连接协商独立 WebRTC DataChannel
                                vv
                          Agent -- TCP --> targetHost:targetPort
                                           ├─ 127.0.0.1：Agent 本机
                                           └─ 192.168.x.x / 主机名：Agent 可访问的局域网主机
```

- Relay 必须监听公开 TCP 端口以接收传统 TCP 客户端；这不是浏览器到 Agent 的端口直通。
- Relay 到 Agent 的业务字节只能使用每连接独立的 P2P DataChannel，绝不复用 Agent WebSocket。
- 打洞失败、设备离线、DataChannel 中断或目标主机不可达时，Relay 立即关闭外部连接；**不会**改用 Agent WebSocket、HTTP/SSE 或 TURN 兜底。
- 端口映射仅支持 TCP。Docker 与防火墙不会自动开放端口，必须显式发布例如 `19022:19022/tcp`。

### 链路选择规则

| 场景 | 实际业务链路 | Relay 是否传输业务内容 | 失败结果 |
| --- | --- | --- | --- |
| 网页登录、设备发现、P2P 协商 | 浏览器 <-> Relay；Agent <-> Relay | 是，属于控制面请求和信令 | 返回认证或连接错误 |
| 控制台 P2P 建立成功 | 浏览器 <-> Agent | 否 | 继续使用直连 |
| 控制台 P2P 失败且 `WEBRTC_P2P_ONLY=false` | 浏览器 <-> Relay <-> Agent | 是 | 使用服务端中转 |
| 控制台 P2P 失败且 `WEBRTC_P2P_ONLY=true` | 不建立业务通道 | 否 | 明确报错，不中转 |
| P2P 端口映射 | 外部 TCP <-> Relay；Relay <-> Agent 为独立 DataChannel | 仅接入侧必经 Relay | 关闭外部 TCP，不使用任何兜底 |

### 部署拓扑与网络入口

```text
Internet / 受控网络
  |
  +-- 浏览器 ---------- 18787/tcp ---> Docker Relay:8787/tcp（网页、API、SSE、WebSocket）
  |                                      |
  |                                      +--> Docker 网络 --> MySQL:3306（不对外暴露）
  |
  +-- 浏览器 / Agent -- 18787/udp ---> Docker Relay:8787/udp（STUN-only，帮助发现公网映射地址）
  |
  +-- 调试客户端 ------ 19022/tcp ---> Docker Relay:19022/tcp（按映射显式发布）
                                         |
                                          +-- 独立 P2P DataChannel --> Agent --> 内网目标服务

安装 Codex 的电脑 -- 主动 WSS --> 18787/tcp --> Relay
```

| 配置 | 含义 | 默认关系 |
| --- | --- | --- |
| `PORT` | Relay 容器内 HTTP/WebSocket 端口 | `8787` |
| `WEBRTC_STUN_PORT` | Relay 容器内 STUN-only UDP 端口 | `8787`，可与 TCP 共用端口号 |
| `WEBRTC_STUN_PUBLIC_PORT` | 浏览器和 Agent 用于 STUN 的宿主机 UDP 端口 | `18787`，必须与 Compose 的 UDP 映射一致 |
| `WEBRTC_STUN_PUBLIC_HOST` | 公网 STUN 主机名；留空时从网页请求 Host 推导 | 默认留空 |
| `WEBRTC_P2P_ONLY` | 普通控制台在 P2P 失败时是否禁止回退 | 默认 `false` |

Relay 自带 STUN-only 服务，只响应 STUN Binding 请求以发现公网映射地址。它不交换 SDP/ICE、不提供 TURN、也不转发浏览器与 Agent 的 DataChannel 流量。需要外部 STUN 时才额外设置 `WEBRTC_STUN_SERVERS`；默认无需依赖第三方 STUN 服务。

### 安全与数据边界

- API Key、CCS 配置和 Codex 配置只留在运行 Codex 的电脑上。
- 账号、Token、设备、会话、事件和图片元数据保存到 MySQL；仅普通控制台回退时的附件二进制保存到 Docker `data` 卷。
- 控制台 P2P 直传附件保存到 Agent 本地目录，不经过 Relay 附件存储。
- 本机 Agent 不开放入站 HTTP 服务；外部访问只能经过 Relay 的认证入口、信令入口或显式发布的端口映射入口。

STUN 地址、端口映射和 P2P-only 开关都是公开服务配置，应直接在 `docker-compose.yml` 的 Relay `environment` 与 `ports` 中修改，不放入 `.env`。部署细节见 [docs/RELAY.md](docs/RELAY.md)，接口边界见 [docs/API.md](docs/API.md)。

## 部署中心服务端

复制环境变量即可使用预设密码启动；生产环境建议在 `.env` 中覆盖为强密码：

```bash
cp .env.example .env
docker compose up -d --pull always
```

服务端默认访问地址：

```text
http://<服务端地址>:18787
```

## 版本、客户端与镜像发布

根目录 `VERSION` 是客户端和 relay 的统一版本号，格式为 `主版本.次版本.修订号`。启用本地 Git hook 后，每次提交前会自动将修订号加一；首次启用执行：

```powershell
.\scripts\install-git-hooks.ps1
```

服务器镜像 CI 会在每次推送到 `main` 后自动构建并推送版本标签和 `latest` 标签。需要正式发布客户端时，再为包含目标改动的提交创建并推送与 `VERSION` 一致的标签，例如：

```powershell
$version = (Get-Content VERSION -Raw).Trim()
git tag "v$version"
git push origin main --follow-tags
```

客户端发布 CI 只监听 `vX.Y.Z` 标签，在 GitHub Runner 上测试并交叉编译 Windows x64 客户端，创建同名 GitHub Release，上传 `codex-remote-agent-windows-amd64.exe` 及其 `.sha256` 文件。服务器镜像 CI 独立监听推送到 `main` 的代码，在 GitHub Runner 上构建并推送 `ghcr.io/ayflying/codex_link:<VERSION>` 和 `ghcr.io/ayflying/codex_link:latest`。两条 CI 都不依赖本地机器或远程构建服务器。Compose 默认使用 `latest`；需要固定或回滚版本时，将 `docker-compose.yml` 中的镜像标签改为对应版本号后再部署。CI 只上传产物，不会自动修改生产服务器；服务器仍需执行：

```bash
docker compose up -d --pull always
```

`scripts\publish-relay.ps1` 和 `scripts\package-remote.ps1` 仅作为 GitHub Actions 不可用时的手动兼容路径；正式发布不需要配置远程构建服务器。

MySQL 只通过 Compose 内部网络提供给 relay，不对外暴露端口。首次注册完成后，建议在 `.env` 中设置 `ALLOW_REGISTRATION=false`，再执行一次：

```bash
docker compose up -d --pull always
```

完整部署和客户端说明见 [docs/RELAY.md](docs/RELAY.md)，接口说明见 [docs/API.md](docs/API.md)，产品需求与验收范围见 [docs/PRD.md](docs/PRD.md)。

## 构建和登录客户端

正式客户端请从 GitHub Releases 下载 `codex-remote-agent-windows-amd64.exe`。客户端启动时会检查最新正式 Release；发现更高版本后下载校验、自动替换并按原启动参数重启。GitHub 不可访问、下载失败或校验失败不会阻止当前客户端启动。

保留以下远程打包命令用于离线手动交付：

```powershell
$env:CODEX_LINK_BUILD_SERVER = "root@your-build-host"
.\scripts\package-remote.ps1
```

也可以直接传入 `-Remote` 参数覆盖环境变量。

生成目录：

```text
release\codex-remote-agent\
```

把该目录放到安装了 Codex 的 Windows 电脑上。目标电脑不需要 Node 或 Docker，只需要本机已有的 Codex/CCS 配置。

直接双击 `codex-remote-agent.exe` 会打开客户端窗口，填写服务端地址和 Token 后点击“连接并启动”。关闭窗口只会隐藏到 Windows 系统托盘，点击托盘图标可恢复窗口；需要结束客户端时，在托盘图标上点击右键并选择“退出客户端”。命令行 `login` 和 `agent` 模式仍然保留。

首次登录：

```powershell
.\codex-remote-agent.exe login `
  --server "http://服务端地址:18787" `
  --token "网页中创建的 Token" `
  --device "办公室电脑"
```

登录命令只负责向服务端登记设备并保存本机配置，完成后会正常退出；请再执行下面的常驻命令：

```powershell
.\codex-remote-agent.exe agent
```

也可以双击 `start-agent.cmd`。同一个 `DATA_DIR` 重复登录同一个服务端会复用原设备 ID，不会新增重复设备；如需清理已经产生的离线重复设备，可在网页设备列表点击删除。客户端每次启动和重连都会校验 Token；网络故障会自动重连。客户端配置和本地缓存保存在 `DATA_DIR`，默认目录为 Windows 的 `%LOCALAPPDATA%\Codex Link\remote-agent`（其他系统使用用户配置目录），不会打印完整 Token。首次启动会迁移发布目录下旧的 `data-remote-agent` 文件。

## 网页功能

网页登录后先选择在线设备，再进入该设备的 Codex 控制台。支持：

- 查看设备和 Token 使用关系。
- 查看 Codex 历史对话，按项目和对话组织。
- 流式接收回复，断线后补齐最近事件。
- 发送消息、粘贴图片、取消 turn。
- 处理三种审批策略和审批请求。
- 删除对话时同步调用 Codex 归档。
- 首位注册用户自动成为管理员，管理员可在系统管理中维护其他用户和管理员角色。
- 每个登录用户可在工作区侧栏管理自己设备的 TCP 端口映射；目标地址可填设备本机的 `127.0.0.1`，也可填该设备能访问的局域网 IP 或主机名。映射仅使用 WebRTC P2P，打洞失败会拒绝连接，不会回退到服务端控制通道。

当前没有稳定公开的 Codex 桌面版现有任务附着接口，因此客户端使用本机 Codex app-server 创建新会话或恢复已有 thread。

## 安全边界

服务端默认端口是宿主机 `18787`，容器内部端口是 `8787`。只建议通过 Tailscale 或受控内网访问，并使用 Tailscale ACL 和防火墙限制来源，不要直接暴露公网。

不要把 `.env`、密码、Token、CCS key 或本机运行缓存提交到 Git。
