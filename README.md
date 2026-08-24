# Codex Link

Codex Link 是一个手机优先的 Vue 控制台，用于通过中心服务远程操作本机 Codex。系统不使用远程桌面、屏幕控制或 UI 自动化。

本项目以 MIT License 开源，详见 [LICENSE](LICENSE)。

## 架构

```text
手机浏览器 <-> Go relay 服务端（网页 / 登录 / 信令 / MySQL）
       |                 <-> WebSocket <-> Go remote agent <-> 本机 Codex app-server
       +-------------------- WebRTC DataChannel ----------------------^
```

- 中心服务端使用 Docker Compose，运行 GHCR 镜像 `ghcr.io/ayflying/codex_link:latest`。
- 浏览器进入设备后优先与本机 agent 建立 WebRTC DataChannel；relay 只传递 SDP/ICE 信令。
- 会话接口、事件和图片附件优先走浏览器与 agent 的 P2P 通道；打洞失败或连接中断时默认自动回退到原 HTTP/SSE/WebSocket 中转。
- 账号、Token、设备、会话、事件和图片元数据保存到 MySQL 8.4。
- 图片二进制保存到 Docker `data` 卷。
- 本机只运行 Go 客户端，主动连接服务端，不开放本地 HTTP 端口。
- API Key、CCS 配置和 Codex 配置只留在运行 Codex 的电脑上。

P2P 使用 STUN 发现可达地址。relay 自带只响应 Binding 请求的 STUN-only UDP 端口，容器内与网页服务共用 `8787`，Compose 默认映射宿主机 `18787/tcp` 和 `18787/udp`；该端口不转发 DataChannel 流量。STUN 地址、端口映射和 `P2P-only` 开关都是公开服务配置，直接在 `docker-compose.yml` 的 relay 配置和 ports 中修改，不放入 `.env`。设置 `WEBRTC_P2P_ONLY` 为 `true` 后，业务接口和图片传输打洞失败会直接报错，不使用服务端中转。

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

## 版本与镜像发布

根目录 `VERSION` 是镜像版本号，格式为 `主版本.次版本.修订号`。启用本地 Git hook 后，每次提交前会自动将修订号加一；首次启用执行：

```powershell
.\scripts\install-git-hooks.ps1
```

初始基线版本为 `0.2.3`，之后每次提交会自动递增修订号；提交完成后还会自动在配置的远程构建服务器上构建并推送版本标签和 `latest` 标签。首次手动发布或补发镜像时执行：

```powershell
.\scripts\publish-relay.ps1
```

脚本使用通过 `-Remote` 或 `CODEX_LINK_BUILD_SERVER` 指定的远程构建服务器，并复用该服务器上的 Docker/GHCR 登录状态；也可以通过 `CODEX_LINK_GHCR_TOKEN` 临时登录 GHCR。脚本会推送 `ghcr.io/ayflying/codex_link:<VERSION>` 和 `ghcr.io/ayflying/codex_link:latest`。Compose 默认使用 `latest`；需要固定或跳转版本时，将 `docker-compose.yml` 中的镜像标签改为对应版本号。临时不发布镜像时设置 `CODEX_LINK_SKIP_IMAGE_PUBLISH=1`。

MySQL 只通过 Compose 内部网络提供给 relay，不对外暴露端口。首次注册完成后，建议在 `.env` 中设置 `ALLOW_REGISTRATION=false`，再执行一次：

```bash
docker compose up -d --pull always
```

完整部署和客户端说明见 [docs/RELAY.md](docs/RELAY.md)，接口说明见 [docs/API.md](docs/API.md)。

## 构建和登录客户端

客户端交叉编译使用通过参数或环境变量指定的远程服务器：

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
- 管理员可在系统管理中创建 TCP 端口映射；映射仅使用 WebRTC P2P，打洞失败会拒绝连接，不会回退到服务端控制通道。

当前没有稳定公开的 Codex 桌面版现有任务附着接口，因此客户端使用本机 Codex app-server 创建新会话或恢复已有 thread。

## 安全边界

服务端默认端口是宿主机 `18787`，容器内部端口是 `8787`。只建议通过 Tailscale 或受控内网访问，并使用 Tailscale ACL 和防火墙限制来源，不要直接暴露公网。

不要把 `.env`、密码、Token、CCS key 或本机运行缓存提交到 Git。
