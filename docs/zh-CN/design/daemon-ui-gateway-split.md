# agent-compose daemon 与 UI gateway 拆分设计

本文档描述 agent-compose 与 agent-compose-ui 的目标职责边界，以及如何把 UI、浏览器认证和公网入口职责从 agent-compose daemon 中拆分出去。

目标是让 agent-compose 更接近 Docker Engine / dockerd 的形态：daemon 是本机或内部控制面的状态权威，CLI 和上层服务通过受控 API 调用它；agent-compose-ui 则承担浏览器入口、认证、OAuth、静态资源和反向代理职责。

## 背景与问题

当前 agent-compose 已经是 daemon + CLI 的单二进制形态。daemon 默认支持 Unix socket，也可以通过 `HTTP_LISTEN` 暴露 TCP HTTP/Connect API。CLI 默认优先使用 Unix socket，显式配置 `--host` 或 `AGENT_COMPOSE_HOST` 时才访问 TCP 地址。

当前代码中仍有一部分 Web/UI 相关职责位于 agent-compose daemon 内：

- 浏览器登录态：`/api/auth/status`、`/api/auth/login`、`/api/auth/logout`。
- OAuth 流程：`/oauth/authorize`、`/oauth/callback`。
- Cookie session、HTML redirect 到 `/login`。
- 面向浏览器部署的安全入口判断。

这些职责和 daemon core 的关系较弱。daemon core 更应该关注 project、run、session、scheduler、runtime driver、store、event dispatcher、ConnectRPC API 等控制面能力。浏览器登录、OAuth、静态 UI 和公网入口更适合由 agent-compose-ui 中的 web gateway 承担。

## 目标架构

目标架构将系统分成三个边界：

```text
Browser
  |
  | HTTPS / Cookie / OAuth
  v
agent-compose-ui gateway
  |
  | internal HTTP/Connect，默认 TCP
  v
agent-compose daemon
  |
  | runtime driver
  v
boxlite / docker / microsandbox runtime
```

CLI 不经过 UI gateway：

```text
agent-compose CLI
  |
  | HTTP over Unix socket
  v
agent-compose daemon
```

### agent-compose daemon

agent-compose 是 daemon 和 CLI。daemon 是状态权威，负责：

- project、run、session、agent definition、workspace config 的控制面。
- scheduler、loader、event dispatcher、background reconciliation。
- runtime driver 生命周期管理。
- image store、runtime LLM facade、Jupyter proxy 后端能力。
- v1/v2 ConnectRPC、health API 和必要的 daemon HTTP API。
- Unix socket peer credential 信任模型，用于本机 CLI 访问。

daemon 仍然可以使用 HTTP handler 和 ConnectRPC。这里的 HTTP 是 API 传输协议，不等同于公网 Web 服务。类似 dockerd，daemon 可以在 Unix socket 上承载 HTTP API，也可以在受控场景下启用 TCP API。

### agent-compose-ui gateway

agent-compose-ui 从纯静态 UI + nginx 反向代理升级为 Web UI + gateway。目标实现保留 nginx 前置，并在同一镜像内新增 Go gateway：

```text
Browser
  -> nginx
     -> static assets
     -> Go gateway
        -> agent-compose daemon
```

nginx 继续负责静态资源、访问日志、body size、超时和 WebSocket upgrade 等成熟代理能力；Go gateway 负责需要应用逻辑的认证、OAuth、cookie session 和鉴权后反向代理。nginx 不再直接代理 daemon API，而是统一代理到 Go gateway。

agent-compose-ui gateway 整体负责：

- 通过 nginx 服务 Svelte SPA 静态资源。
- 处理浏览器认证和登录态。
- 处理 OAuth authorize/callback。
- 对浏览器 API 请求做 cookie session 校验。
- 反向代理 daemon ConnectRPC、plain HTTP API、Jupyter proxy。
- 作为生产部署中对外暴露的唯一 Web 入口。

agent-compose-ui 应保持现有前端 API path 兼容，优先让 Svelte 代码无需大改：

- `/api/auth/*` 由 UI gateway 自己处理。
- `/oauth/*` 由 UI gateway 自己处理。
- `/agentcompose.v1.*`、`/agentcompose.v2.*`、`/health.v1.*` 代理到 daemon。
- `/api/agent-compose/workspaces/*`、`/api/events*`、`/api/webhook-sources*` 代理到 daemon。
- `/jupyter/*` 或配置的 `JUPYTER_PROXY_BASE` 代理到 daemon。
- `/agent-compose/session/*` 继续代理到 daemon，用于已有 Jupyter proxy path 兼容。

## `HTTP_LISTEN` 的目标定位

`HTTP_LISTEN` 不应被理解为 agent-compose 的公网 Web 入口。拆分后它的定位是：

```text
daemon internal TCP API listener
```

它主要用于：

- 容器网络内的 agent-compose-ui gateway 访问 daemon。
- 本机或内网开发调试。
- 受控环境中的远程 CLI 或自动化客户端。

默认生产入口应是 agent-compose-ui gateway 暴露的端口，而不是 daemon 的 `HTTP_LISTEN`。

推荐部署形态：

```text
public network
  -> agent-compose-ui nginx/gateway :8000/443
       -> agent-compose daemon :7410
```

不推荐：

```text
public network
  -> agent-compose daemon HTTP_LISTEN
```

## 安全模型

### 本机 CLI 访问

本机 CLI 默认使用 Unix socket。daemon 应继续保留当前 peer credential 信任模型：

- socket 文件默认位于 `AGENT_COMPOSE_SOCKET`。
- daemon 在 Unix socket 连接上校验 peer UID。
- 只有 daemon 同用户或 root 可获得本机免密访问。
- CLI 使用 HTTP over Unix socket 调用 ConnectRPC 和 daemon HTTP API。

这部分属于 daemon local trust，不应迁移到 UI gateway。

### 浏览器访问

浏览器访问由 agent-compose-ui gateway 保护。gateway 负责：

- Cookie session 签名和过期时间。
- `/api/auth/status`、`/api/auth/login`、`/api/auth/logout`。
- OAuth state cookie、token exchange、callback redirect。
- HTML 请求未登录时跳转 `/login?next=...`。
- API/RPC 请求未登录时返回 `401 Unauthorized`。

迁移时应保持当前 auth response shape，避免前端调用大规模改动。

### 外部访问 daemon

如果确实希望外部系统直接访问 agent-compose daemon，而不是通过 UI gateway，必须启用一个可配置安全机制。可选机制包括：

- 只绑定 loopback 或私有网络地址，例如 `HTTP_LISTEN=127.0.0.1:7410`。
- 配置 daemon internal API token，客户端通过 `Authorization: Bearer <token>` 访问。
- 配置 daemon basic auth，仅用于非浏览器机器客户端。
- 通过 mTLS、反向代理或 VPN 将 daemon TCP API 放在受控网络内。

目标规则：

- Unix socket 可以使用 peer credential 信任。
- TCP listener 不能默认假定可信。
- 当 `HTTP_LISTEN` 绑定非 loopback 地址且未配置外部访问安全机制时，daemon 应在启动时输出强警告，但不阻止启动。
- 浏览器 cookie/OAuth 不应作为 daemon TCP API 的主要认证机制；它属于 UI gateway。

### Webhook 入口

Webhook 是外部系统调用入口，和浏览器 auth 不是同一个安全模型。拆分第一阶段保留 daemon webhook handler，由 UI gateway 转发到 daemon。

第一阶段规则：

- `/api/webhooks/*` 的业务处理、webhook source token 校验和后续 provider signature 校验继续由 daemon handler 完成。
- UI gateway 不把 webhook token 转换成浏览器 cookie session，也不把 webhook 请求纳入浏览器登录态。
- UI gateway 对 webhook 路径只做入口转发和通用 HTTP 代理能力；公网 webhook ingress 的更细策略后续单独设计。

在实现完成前，不能把 webhook token 和浏览器 cookie session 混为一个认证边界。

## 迁移步骤

### Phase 1：文档和边界确认

新增本文档，明确：

- daemon 与 UI gateway 的职责边界。
- `HTTP_LISTEN` 是 internal TCP API，不是公网 Web 入口。
- auth/OAuth 迁移方向。
- 外部访问 daemon 时必须增加可配置安全机制。

该阶段不改实现代码。

### Phase 2：agent-compose-ui 增加 gateway backend

在 agent-compose-ui 中新增 Go gateway backend。gateway backend 必须使用 Go 实现，并直接复制当前 agent-compose 的浏览器 auth/OAuth 行为到 agent-compose-ui 仓库内。实现时不抽共享 Go module，不引入 Node、Bun 或其他后端 runtime。

agent-compose-ui 容器内保留 nginx 前置。nginx 继续服务静态资源并处理通用代理细节，但所有 API、OAuth、RPC、Jupyter 和 webhook 路径都先转发到 Go gateway，再由 Go gateway 按路径自行处理或代理到 daemon。

gateway backend 需要支持：

- 静态资源仍由 nginx 从 `/srv/agent-compose/frontend` 服务，Go gateway 不处理 SPA 文件。
- daemon backend 默认复用当前 nginx upstream 约定 `http://agent-compose:7410`，不新增必填配置。
- `AUTH_USERNAME`、`AUTH_PASSWORD`、`AUTH_SECRET`、`AUTH_SESSION_TTL` 迁移到 UI gateway，变量名保持不变。
- `OAUTH_APIKEY`、`OAUTH_SECRET`、`OAUTH_BASE_URL`、`OAUTH_CALLBACK_URL`、`OAUTH_SCOPES` 等 OAuth 变量迁移到 UI gateway，变量名保持不变。
- `JUPYTER_PROXY_BASE` 继续用于 Jupyter 代理路径，变量名保持不变。
- Unix socket 访问 daemon 只作为高级部署选项复用现有 `AGENT_COMPOSE_SOCKET`，不作为 UI gateway 默认连接方式。

第一版应保持前端路径和响应格式兼容。

### Phase 3：daemon 移除浏览器 auth/OAuth

agent-compose daemon 中一次性移除或停止注册浏览器 auth/OAuth：

- `/api/auth/status`
- `/api/auth/login`
- `/api/auth/logout`
- `/oauth/authorize`
- `/oauth/callback`
- 面向 HTML 请求的登录页 redirect middleware

daemon 继续保留：

- request logging、recover middleware。
- Unix socket peer credential bypass。
- ConnectRPC 和 daemon internal HTTP API。
- runtime LLM facade 的 session token 校验。
- webhook source token/signature 校验。

如果保留 TCP API 认证，应该命名为 daemon/internal API auth，避免继续使用浏览器 auth 语义。

### Phase 4：部署拓扑调整

Docker Compose 和容器镜像目标：

- agent-compose daemon 不直接发布公网端口。
- agent-compose-ui gateway 发布 `AGENT_COMPOSE_HTTP_PORT`。
- gateway 默认通过当前容器网络服务名 `http://agent-compose:7410` 访问 daemon，不增加用户必填配置。
- auth/OAuth 环境变量归属 agent-compose-ui gateway。
- daemon 的 `HTTP_LISTEN` 只在容器网络内部可达。

README 和 `.env.example` 需要同步说明变量归属：

- UI gateway 变量：`AUTH_*`、`OAUTH_*`、`AGENT_COMPOSE_HTTP_PORT`。
- daemon 变量：`AGENT_COMPOSE_SOCKET`、`HTTP_LISTEN`、runtime、store、LLM、scheduler、driver 相关配置。
- 外部 daemon API 安全机制优先复用现有配置；未配置时非 loopback `HTTP_LISTEN` 只输出强警告。

### Phase 5：测试和兼容

需要覆盖：

- CLI 默认通过 Unix socket 调用 daemon，不依赖 UI gateway。
- gateway 停止时，本机 CLI 仍可管理 daemon。
- 未登录浏览器访问 API/RPC 返回 `401` 或跳转登录页。
- 登录后 RPC、workspace upload、events、webhook source 管理、Jupyter proxy 正常。
- daemon 不再响应 `/api/auth/*` 和 `/oauth/*`。
- `HTTP_LISTEN` 关闭时 daemon 只通过 Unix socket 可访问。
- `HTTP_LISTEN` 绑定非 loopback 且未配置安全机制时输出明确强警告，但仍允许 daemon 启动。

## 与当前代码的对应关系

当前主要迁移点：

- daemon 入口和 listener：`cmd/agent-compose/main.go`。
- daemon 路由注册：`pkg/agentcompose/app/app.go`。
- 当前 auth 实现：`pkg/auth/`。
- Jupyter/workspace/runtime LLM proxy：`pkg/agentcompose/proxy/`。
- webhook HTTP handler：`pkg/events/webhooks/http.go`。
- UI 反向代理：`agent-compose-ui/nginx/nginx.conf`。
- UI auth 调用：`agent-compose-ui/src/api/auth.ts`。

迁移后，`pkg/auth/` 的浏览器认证能力直接复制到 agent-compose-ui gateway 并在 agent-compose 中移除。agent-compose 中只保留 daemon local trust 和 daemon internal API 安全能力。

## 非目标

本拆分不改变以下领域模型：

- project/run/session/agent definition API 语义。
- runtime driver 行为。
- compose 文件模型。
- Jupyter guest 内部运行方式。
- webhook topic/event 存储模型。

本拆分也不要求删除 HTTP/ConnectRPC。daemon 继续通过 HTTP handler 提供 API，只是默认入口和安全边界应从“公网 Web 服务”收敛为“本机或内部 daemon API”。
