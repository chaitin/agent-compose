# OctoBus 集成快速开始

本指南带你完成 agent-compose 与 [OctoBus](https://github.com/chaitin/OctoBus)（一个把企业内部 API、工具和服务安全暴露给 AI Agent 的本地网关）的对接，并验证 Agent 能在自己的沙箱内成功调用一个能力。全程约 15 分钟。

配置字段参考见 [agent-compose.yml 配置手册](https://github.com/chaitin/agent-compose/blob/main/docs/pages/zh-CN/agent-compose-yaml-manual.md)（`octobus_servers`、`capset_ids`）；内部实现原理见 [OctoBus 集成设计文档](https://github.com/chaitin/agent-compose/blob/main/docs/design/octobus_integration.md)。

## 一分钟理解核心概念

| 概念 | 含义 | 归属方 |
| --- | --- | --- |
| **Capability Gateway（能力网关）** | 在 agent-compose 设置页配置的 OctoBus 连接（`addr` + `token`）。 | agent-compose daemon |
| **capset（能力集）** | OctoBus 发布的一组能力，由 `capset -> service -> instance -> method` 绑定构成。 | OctoBus |
| **`capset_ids`** | Agent 配置字段，声明其沙箱允许使用哪些能力集。 | 你的 `agent-compose.yml` |
| **capability proxy（capproxy）** | daemon 内部的 gRPC 代理。沙箱从不直接连接 OctoBus，由 capproxy 做鉴权检查并转发调用。 | agent-compose daemon |
| **`CAP_GRPC_LISTEN`** | daemon 启动变量：capproxy 监听 guest gRPC 调用的地址。 | daemon 部署配置 |
| **`CAP_GRPC_TARGET`** | daemon 启动变量：从沙箱内部视角看到的 capproxy 地址，以环境变量形式注入沙箱。 | daemon 部署配置 |
| **`CAP_TOKEN`** | 沙箱创建时生成的凭据，capproxy 用它解析调用方沙箱被授权的能力集。 | 自动注入 |

运行时数据流：

```text
guest agent ──gRPC──▶ capproxy (CAP_GRPC_TARGET) ──gRPC──▶ OctoBus daemon
```

guest 只能看到 `CAP_GRPC_TARGET` 和 `CAP_TOKEN`。OctoBus 的地址和 token 始终留在 daemon 内，不会进入沙箱。

## 前置条件

- 一个运行中的 **agent-compose daemon**（Docker 驱动即可，见[快速开始](https://github.com/chaitin/agent-compose/blob/main/README.zh-CN.md)）。
- 已连接该 daemon 的 **`agent-compose` CLI**（通过 `--host` 或默认端点）。
- **Docker**（用于运行 OctoBus；或者用 Node.js 20+ 从 npm 运行）。
- **Web UI**（`with-ui` profile），用于设置页配置步骤。如果只跑 daemon，可以通过 Settings API 配置全局网关。

## 第 1 步 — 启动 OctoBus

把 OctoBus 跑在与 agent-compose daemon 相同的 Docker 网络里，让 daemon 可以用容器名访问它。仓库根目录 `docker-compose.yml` 默认创建的网络是 `agent-compose_default`：

```bash
docker run -d --name octobus \
  --network agent-compose_default \
  -v octobus-data:/var/lib/octobus \
  ghcr.io/chaitin/octobus:latest
```

OctoBus 在容器内监听 `0.0.0.0:9000`。如果你更想直接在宿主机上跑：

```bash
npx @chaitin-ai/octobus serve
```

这种情况下，记下 daemon 容器访问宿主机可用的地址（Docker Desktop 上通常是 `host.docker.internal`，Linux 上是宿主机 LAN IP）。

## 第 2 步 — 从 OctoBus 发布一个能力集

OctoBus 自带一个计算器示例服务。用 OctoBus CLI（容器内或本地 `octobus` 命令）导入它、创建实例，并通过名为 `dev` 的能力集暴露它的方法：

```bash
# 导入示例服务（此处以 OctoBus 仓库检出目录为例）。
octobus service import calculator ./examples/calculator-js

# 创建并启动一个实例。
octobus instance create calculator-test \
  --service calculator \
  --config-json '{"label":"primary"}'

# 创建 "dev" 能力集，并把该实例的方法暴露进去。
octobus capset create dev --name DevAgent
octobus capset add-instance dev calculator-test

# 确认 catalog 中已经暴露了计算器方法。
octobus catalog dev --all --json
```

如果 OctoBus 跑在 Docker 里，先克隆 [OctoBus 仓库](https://github.com/chaitin/OctoBus)，再用 `docker exec -it octobus octobus <args>` 执行上述命令。换任何其他服务包流程都一样——agent-compose 后面只需要能力集 id（`dev`）。

> 能力集的访问 token 是可选的。如果你添加了（`octobus capset add-token dev local --token-stdin`），记下它供第 3 步使用——daemon 会在每次上游调用时携带 `Authorization: Bearer <token>`。

## 第 3 步 — 把 agent-compose 指向 OctoBus

需要分别配置两件互相独立的事：

1. **Capability Gateway 连接**（控制面：daemon 如何从 OctoBus 读取能力集），以及
2. **能力代理地址对**（数据面：沙箱内 guest 如何发起调用）。

### 3a. Capability Gateway（控制面）

打开 Web UI，进入 **Settings → Capability Gateway**，设置：

- **地址（Address）**：从 daemon 容器视角可达的 OctoBus admin API 地址，例如 `http://octobus:9000`（Docker 网络）或 `http://host.docker.internal:9000`（OctoBus 跑在宿主机）。
- **Token**：如果你在第 2 步配置了能力集 token 就填上，否则留空。

设置页会立即探测 OctoBus 的 `GET /admin/v1/status`，并显示连接状态和已发布能力集数量。状态为绿色且能看到 `dev` 能力集，说明控制面已经接通。

token 只保存在 daemon：读取时会被脱敏，不会写入沙箱元数据、不会注入 guest 环境变量、不会进入日志。

### 3b. 能力代理（数据面）

沙箱内发起的能力调用经过 capproxy，它由 **daemon 启动时**的两个环境变量决定。把这两个变量加到 daemon 服务的部署配置里（仓库 Compose 部署的话，加到 `docker-compose.yml` 或 override 文件中 `agent-compose` 服务的 environment 下）：

```yaml
services:
  agent-compose:
    environment:
      # capproxy 在 daemon 容器内的监听地址。
      CAP_GRPC_LISTEN: 0.0.0.0:7411
      # 同一个监听器在沙箱容器视角下的地址。
      CAP_GRPC_TARGET: agent-compose:7411
```

| 变量 | 含义 | 示例 |
| --- | --- | --- |
| `CAP_GRPC_LISTEN` | daemon 内部 gRPC 能力代理的监听地址。 | `0.0.0.0:7411` |
| `CAP_GRPC_TARGET` | guest 可达的代理地址；作为环境变量 `CAP_GRPC_TARGET` 注入沙箱。 | `agent-compose:7411` |

两个变量必须在 daemon 启动时都设置。缺了任何一个，设置页仍会显示 OctoBus 已连接，但新建沙箱拿不到可用的能力连接变量。**修改后需要重启 daemon**，并且只有*新建*沙箱才会带上这些配置——已有沙箱保留创建时的环境变量。

这个监听端口只需对沙箱容器可达，除非有明确理由，不要把它发布到宿主机。

## 第 4 步 — 在项目中绑定能力集

在 `agent-compose.yml` 中给 agent 声明 `capset_ids`：

```yaml
name: octobus-demo

agents:
  coder:
    provider: claude
    image: chaitin/agent-compose-guest:latest
    workspace:
      provider: file
      path: .
    capset_ids:
      - dev
```

校验并应用：

```bash
agent-compose config --quiet
agent-compose up
```

### 可选：项目级 OctoBus 服务器

未限定的 `dev` 使用第 3a 步配置的 daemon 全局网关。如果不同项目需要连接不同的 OctoBus 部署，可以在顶层声明具名服务器，并用限定写法选择能力集：

```yaml
octobus_servers:
  internal:
    url: http://octobus:9000
    token: ${OCTOBUS_INTERNAL_TOKEN}

agents:
  coder:
    capset_ids:
      - dev              # daemon 全局网关
      - internal/dev     # 项目服务器 "internal"
```

限定写法 `internal/dev` 会走名为 `internal` 的项目服务器，而 OctoBus 收到的能力集 id 仍然是 `dev`。限定与未限定条目可以混用，声明 `octobus_servers` 不会改变未限定条目的路由。完整路由矩阵见 [agent-compose.yml 配置手册](https://github.com/chaitin/agent-compose/blob/main/docs/pages/zh-CN/agent-compose-yaml-manual.md)。

## 第 5 步 — 验证沙箱注入

创建一个沙箱（或等调度器运行创建），检查 agent 实际看到的内容：

```bash
agent-compose sandbox ls
agent-compose inspect <sandbox-id> --json
```

一个带 `capset_ids: [dev]` 创建的沙箱会有：

- 环境变量 **`CAP_GRPC_TARGET`** —— 第 3b 步配置的 capproxy 地址。
- 环境变量 **`CAP_TOKEN`** —— 每个沙箱独立的凭据（标记为 secret；它是 agent-compose 签发的 token，*不是* OctoBus token）。
- 一个 **`capset=dev`** 标签，记录授权关系。
- 一份渲染好的能力指南，写入 MPI catalog 的 `runtime/mpi/catalog.md`（guest 内路径 `/data/runtime/mpi/catalog.md`），列出每个 gRPC 方法以及调用时要带的 `x-octobus-capset` / `x-octobus-instance` 元数据。Agent 运行时会自动把这份 catalog 读进系统上下文，所以 agent 一启动就知道自己有哪些能力，不需要自己去读文件。

注入在设计上**尽力而为（best-effort）**：如果创建沙箱时 OctoBus 暂时不可达，沙箱仍会正常启动；失败会记录为沙箱事件，能力调用在 OctoBus 恢复前会在运行时报错。

## 第 6 步 — 调用一个能力

直接让 agent 用计算器即可：

```bash
agent-compose run coder "Use the calculator capability to add 20 and 22, and tell me the result."
```

底层发生的调用过程：

1. Agent 连接 `$CAP_GRPC_TARGET`（明文 HTTP/2 gRPC）。
2. 携带元数据 `x-capability-sandbox-token: $CAP_TOKEN`，以及能力指南中给出的 `x-octobus-capset: dev` 和 `x-octobus-instance: calculator-test`。
3. 用相同的 capset 元数据通过 gRPC server reflection 发现请求/响应结构。

capproxy 把 token 解析为沙箱，检查 `dev` 在它的授权能力集范围内，在服务端注入 OctoBus token，然后转发调用。预期结果是 agent 回答 `42`。

## 故障排查

| 现象 | 可能原因 | 处理 |
| --- | --- | --- |
| 设置页显示"未配置" | 网关地址为空 | 在 Settings → Capability Gateway 填写地址 |
| 设置页显示连接错误 | daemon 容器访问不到该地址，或 token 不对 | 用 `docker exec agent-compose wget -qO- http://octobus:9000/admin/v1/status` 测试可达性；重新核对 token |
| 控制面正常，但沙箱没有 `CAP_GRPC_TARGET` 环境变量 | daemon 启动时缺少 `CAP_GRPC_LISTEN` / `CAP_GRPC_TARGET` | 两个都配上，重启 daemon，新建沙箱 |
| Agent 连不上代理 | `CAP_GRPC_TARGET` 用了沙箱解析不了的宿主机地址 | 把 `CAP_GRPC_TARGET` 的主机部分改成 daemon 容器名（Compose 网络） |
| 业务调用返回 gRPC `FailedPrecondition` | 缺少 `x-octobus-instance` 元数据 | Agent 必须带上能力指南中的实例 id；检查 `catalog.md` 内容 |
| gRPC 报能力集相关权限错误 | 沙箱调用了授权集之外的能力集 | 把该能力集加进 agent 的 `capset_ids`，新建沙箱 |
| 配置看起来都对但调用失败 | 调用时刻 OctoBus 不可用 | 检查 `octobus status`；运行时能力错误不会阻塞沙箱本身 |

## 安全说明

- OctoBus token 不会离开 daemon：不会以明文返回给前端、不会注入 guest 环境变量、不会写入沙箱元数据、不会进入日志。
- `CAP_TOKEN` 只能向 capproxy 证明"调用方是这个沙箱"，它本身不能调用 OctoBus。
- 沙箱创建时绑定的能力集就是 capproxy 强制执行的隔离边界；guest 只能在已授权的能力集内部选择实例。
- 能力只通过 gRPC 暴露给 guest。OctoBus 的 MCP / Connect RPC / REST 端点不会代理进沙箱。
