# Playground 部署与验证

本文档以当前共享 playground 为准，而不是仓库内历史上的 `./playground` 假设。

当前实际环境：

- 代码目录：`/data/code`
- 部署目录：`/data/playground`
- compose 文件：`/data/playground/docker-compose.yml`
- 当前共享 compose 部署 `agent-compose` daemon 和 `agent-compose-frontend` 独立前端服务

如果需要本地联调，请使用仓库根目录的 `docker-compose.yml`；不要把这份共享 playground 文档和 repo 内本地 compose 混在一起。

## 前置条件

- 宿主机可用 Docker 和 `docker compose`
- 宿主机存在 `/dev/kvm`
- 宿主机允许容器挂载 `/var/run/docker.sock`
- 本机已有 `/data/code` 仓库
- 构建机可以访问构建所需的镜像源和依赖源，或者宿主缓存里已经有对应层

## 当前 daemon compose 事实

共享 playground 的 `agent-compose` daemon 服务当前配置要点：

- 监听端口：`7410`
- `DATA_ROOT=/data`
- `SANDBOX_ROOT=/data/sandboxes`
- `DOCKER_HOST_SANDBOX_ROOT=/data/playground/data/agent-compose/sandboxes`
- `RUNTIME_DRIVER=docker`
- `DEFAULT_IMAGE=${DEFAULT_IMAGE:-debian:bookworm-slim}`
- 数据挂载：`./data/agent-compose:/data`
- 运行时额外挂载：`/var/run/docker.sock:/var/run/docker.sock`

共享 playground 的 `agent-compose-frontend` 服务当前配置要点：

- 监听端口：`8000`
- 使用独立的 `agent-compose-ui` 镜像
- 反向代理 daemon 的 v1/v2 Connect API、`/api/` 和 Jupyter proxy 路由
- 数据挂载：`./data:/data`，用于前端服务运行时数据

对应宿主机数据目录是：

- `/data/playground/data/agent-compose`

如果 agent-compose 通过 `/var/run/docker.sock` 创建 Docker runtime sandbox，Docker bind mount 的 source 必须是宿主机路径。此时 `DOCKER_HOST_SANDBOX_ROOT` 需要指向宿主机上实际 backing `SANDBOX_ROOT` 的 sandboxes 目录。

Web/UI 不应再作为 daemon 容器的内嵌静态资源职责来验证。前端可以由 nginx、静态文件服务器或独立容器提供，并反向代理到 daemon 的 v1/v2 Connect API 和 Jupyter proxy 路由。现有前端继续使用 v1 API；CLI 和新客户端优先使用 v2 API。

## 构建镜像

从代码目录执行：

```bash
cd /data/code
docker build -t debian:bookworm-slim -f guest-images/Dockerfile.agent-compose-guest .
docker build -t agent-compose:latest -f Dockerfile .
```

如果你更习惯用 task：

```bash
cd /data/code
task image:agent-compose-guest
task image:agent-compose
```

## 部署到共享 playground

启动或更新 daemon 和独立前端服务：

```bash
docker compose -f /data/playground/docker-compose.yml up -d agent-compose agent-compose-frontend
```

镜像更新后强制重建容器：

```bash
docker compose -f /data/playground/docker-compose.yml up -d --force-recreate agent-compose agent-compose-frontend
```

查看状态：

```bash
docker compose -f /data/playground/docker-compose.yml ps
docker logs --tail 200 agent-compose
docker logs --tail 200 agent-compose-frontend
```

## 基础验证

### 1. 验证 daemon 状态

```bash
curl -sS http://127.0.0.1:7410/api/version
```

如果本机已有 `agent-compose` CLI，也可以验证：

```bash
agent-compose --host http://127.0.0.1:7410 status
```

### 2. 验证独立前端服务可访问

```bash
curl -i http://127.0.0.1:8000/ | head
curl -i http://127.0.0.1:8000/ui/ | head
```

如果 nginx basic auth 已配置，未带凭据时返回 `401` 也是前端服务已响应的有效信号。

### 3. 验证 v1 SessionService 兼容 API 可访问

```bash
curl -sS -X POST \
  http://127.0.0.1:7410/agentcompose.v1.SessionService/ListSessions \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -d '{}'
```

### 4. 验证 v2 ProjectService 主路径可访问

空请求应返回 validation issue，而不是 404：

```bash
curl -sS -X POST \
  http://127.0.0.1:7410/agentcompose.v2.ProjectService/ValidateProject \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -d '{}'
```

### 5. 使用 CLI 完成 project smoke

准备临时 compose 文件：

```bash
cat >/tmp/agent-compose-smoke.yml <<'YAML'
name: playground-smoke
agents:
  reviewer:
    provider: codex
    model: gpt-test
    image: debian:bookworm-slim
YAML
```

执行主路径：

```bash
agent-compose --host http://127.0.0.1:7410 -f /tmp/agent-compose-smoke.yml config --quiet
agent-compose --host http://127.0.0.1:7410 -f /tmp/agent-compose-smoke.yml up
agent-compose --host http://127.0.0.1:7410 -f /tmp/agent-compose-smoke.yml ps
agent-compose --host http://127.0.0.1:7410 -f /tmp/agent-compose-smoke.yml down
```

### 6. 创建一个 v1 验证 sandbox

这里用 v1 兼容 API 的最小请求即可，不需要额外传 `baseWorkspace`：

```bash
curl -sS -X POST \
  http://127.0.0.1:7410/agentcompose.v1.SessionService/CreateSession \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -d '{"title":"playground-verify"}'
```

说明：

- `base_workspace` 不是当前 playground 烟雾验证的必要参数。
- 如果你要准备真实 workspace，优先使用 `ConfigService` 管理 `workspace_id`，当前支持的 workspace 类型是 `git`。

### 7. 通过 v1 兼容 API 查询 sandbox 状态

```bash
curl -sS -X POST \
  http://127.0.0.1:7410/agentcompose.v1.SessionService/ListSessions \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -d '{}'
```

### 8. 获取 notebook 代理入口

先从上一步响应里拿到 v1 `sessionId` 字段，然后执行：

```bash
curl -sS -X POST \
  http://127.0.0.1:7410/agentcompose.v1.SessionService/GetSessionProxy \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -d '{"sessionId":"<sandbox_id>"}'
```

期望返回：

- `proxyPath`，例如 `/jupyter/<sandbox_id>/lab`
- `notebookUrl`，例如 `/jupyter/<sandbox_id>/lab?token=...`
- `driver`
- `vmStatus`

## 冷启动特征

如果出现以下情况：

- 第一次使用一个新的 `agent-compose-guest` 镜像
- 清空过 `/data/playground/data/agent-compose`
- 删除过 `image-cache` 或 `boxlite` 缓存目录

那么第一次 v1-compatible `CreateSession` 可能会明显变慢。这通常是正常预热，不代表 RPC 层已经卡死。

当前重点缓存目录：

- `/data/playground/data/agent-compose/image-cache`
- `/data/playground/data/agent-compose/boxlite`

排查时优先看：

```bash
docker logs -f agent-compose
```

常见推进日志包括：

- `ensure session begin`
- `using materialized local image rootfs`
- `ensure session box ready`
- `starting box`
- `checking jupyter`
- `jupyter ready`

## 推荐预热步骤

如果你刚清过数据目录，建议部署完成后做一次预热：

1. 更新并启动 `agent-compose` 容器。
2. 创建一个临时 sandbox，例如 `playground-prewarm`。
3. 轮询 `ListSessions`，等它进入 `RUNNING`。
4. 再开始正式功能验证。

## 故障排查

### 1. daemon 状态不可访问

检查：

```bash
docker compose -f /data/playground/docker-compose.yml ps
docker logs --tail 200 agent-compose
docker logs --tail 200 agent-compose-frontend
```

如果是独立前端打不开，先确认前端服务自身、反向代理配置，以及它到 daemon `http://127.0.0.1:7410` 或容器网络地址的连通性。不要用 daemon 容器是否内嵌 `/agent-compose.html` 判断前端是否部署成功。

### 2. `CreateSession` 失败或长期 `PENDING`

检查：

```bash
docker logs --tail 200 agent-compose
```

优先确认：

- `/dev/kvm` 是否可用
- `/var/run/docker.sock` 是否正确挂载
- `DEFAULT_IMAGE` 指向的镜像是否存在于宿主 Docker 或可被拉取
- 是否只是第一次冷启动导致缓存重建耗时较长

### 3. `GetSessionProxy` 返回 502 或 notebook 不可访问

检查：

- `ListSessions` 里的 `vmStatus`
- `docker logs --tail 200 agent-compose`
- sandbox 对应目录下的 proxy / VM 状态文件

常用文件位置：

- `/data/playground/data/agent-compose/sandboxes/<sandbox_id>/metadata.json`
- `/data/playground/data/agent-compose/sandboxes/<sandbox_id>/vm/runtime.json`
- `/data/playground/data/agent-compose/sandboxes/<sandbox_id>/proxy/jupyter.json`

### 4. guest 镜像更新后没有生效

重新构建镜像并强制重建容器：

```bash
cd /data/code
docker build -t debian:bookworm-slim -f guest-images/Dockerfile.agent-compose-guest .
docker build -t agent-compose:latest -f Dockerfile .
docker compose -f /data/playground/docker-compose.yml up -d --force-recreate agent-compose agent-compose-frontend
```
