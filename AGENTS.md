# Codex Link 协作规则

## 基本要求

- 项目默认使用简体中文沟通、编写文档和提交 Git commit。
- 每次 Git commit 的提交说明必须使用中文，准确描述本次改动。
- 不提交 API Key、CCS key、密码、设备令牌、Cookie 或本机运行缓存。
- 不删除或覆盖与当前任务无关的用户改动。

## 构建与部署

- 需要编译 Go 客户端、服务端或容器镜像时，使用 `root@192.168.50.217` 远程服务器。
- 中心服务端使用 `docker-compose.yml` 中的 GHCR 镜像 `ghcr.io/ayflying/codex_link:latest` 部署。
- 部署前使用 `docker compose up -d --pull always` 拉取最新镜像。
- 中心服务端宿主机访问端口为 `18787`，容器内部服务端口为 `8787`。
- 本机 Codex agent 使用 WebSocket 主动连接中心服务端，不开放本地 HTTP 服务端口。
- 中心服务端使用 MySQL 8.4；账号、Token、设备、会话、事件和图片元数据写入 MySQL，图片二进制写入 `data` 卷。
- MySQL 不对外暴露端口，relay 通过 Compose 内部网络连接；密码只从 `.env` 注入，禁止提交 `.env`、密码和 Token。
- 部署前必须准备 `.env` 中的 `MYSQL_PASSWORD` 和 `MYSQL_ROOT_PASSWORD`，并用 `docker compose up -d --pull always` 启动健康的 MySQL 与 relay。

## 验证与交付

- 修改代码或部署配置后，必须执行与改动范围匹配的自测。
- 容器改动至少验证 Compose 配置可解析、镜像能启动、网页返回 `200`，并检查关键 API 的认证行为。
- 远程构建或推送完成后，核对远程 Git commit、镜像标签和镜像 digest。
- 需要数据库验证时，至少检查 MySQL health、空库自动建表、注册/登录、多个 Token、Token 刷新/删除、设备列表和 relay 重启后的数据保留。
- 最终反馈必须说明已完成的验证，以及未能执行的检查和原因。
