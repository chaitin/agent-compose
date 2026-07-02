# agent-compose 总体设计

本文档描述当前代码中的 agent-compose 架构和已经落地的 daemon + CLI 设计。早期重构过程、阶段计划和验收清单不再作为设计文档保留。

当前代码事实以以下入口为准：

- CLI 和 daemon 入口：`cmd/agent-compose/main.go`
- daemon 服务注册：`pkg/agentcompose/service.go`
- compose 解析和规范化：`pkg/compose/`
- v1 API：`proto/agentcompose/v1/agentcompose.proto`
- v2 API：`proto/agentcompose/v2/agentcompose.proto`
- 持久化 store 和 schema：`pkg/storage/`
- Jupyter 代理：`pkg/agentcompose/proxy.go`
- loader 运行时和调度：`pkg/loaders/`
- 领域服务：`pkg/sessions/`、`pkg/projects/`、`pkg/agents/`、`pkg/llm/`、`pkg/events/`、`pkg/images/`、`pkg/workspaces/`
- 独立前端镜像：`nginx/Dockerfile`

## 架构目标

agent-compose 是一个 agent/session 控制面。它采用类似 Docker Engine + CLI + Compose 的形态，但保留自身的 agent、scheduler、workspace、runtime driver 和 notebook 代理领域模型。

核心边界：

- daemon 是状态权威，负责持久化、scheduler、runtime 生命周期、Connect API、HTTP API 和 Jupyter proxy。
- CLI 是 daemon 客户端，负责读取本地 `agent-compose.yml`、做本地语法校验和规范化、调用 daemon API 并渲染输出。
- `agent-compose.yml` 描述 project 和 agent definition，不直接描述一个已经运行的 session。
- Web/UI 不再打进 daemon 镜像，也不由 daemon 进程托管静态资源；它作为独立前端服务部署。
- v1 session-centric API 继续保留给现有 Web/UI 和兼容客户端；v2 API 是 CLI 和新客户端的主路径。
- `pkg/agentcompose` 是 daemon 组合层，负责 DI service graph 注册、Connect/HTTP 路由注册、后台启动顺序、handler 转发和 Jupyter proxy 路由胶水代码。
- 领域行为归属 owner 包。`pkg/agentcompose` 不再导出 owner 类型、常量、构造器或测试兼容 helper。
- 领域行为测试放在对应 owner 包；`pkg/agentcompose` 测试只覆盖 service graph wiring、route registration、启动协调和跨 owner smoke 路径。

当前 owner 包职责：

- `pkg/model`：共享数据模型，以及 env item、agent provider、topic event 的无依赖归一化 helper。
- `pkg/storage`：SQLite store、schema、migration、规范化持久化记录和 query helper。
- `pkg/sessions`：session lifecycle service、RPC bridge、stream broker、session reconcile 和 Jupyter proxy 支撑。
- `pkg/projects`：project apply/down、project run lifecycle、stale run reconcile、project agent runner 和 v2 exec target 解析。
- `pkg/loaders`：loader CRUD service、scheduler engine、loader manager、loader run host、webhook queue 消费和 loader event 发布。
- `pkg/agents`：agent definition CRUD/validation、agent session 创建，以及 agent definition 到 execution config 的解析。
- `pkg/executor`：cell/agent/loader command 执行、v2 exec service、agent system prompt 处理，以及 runtime LLM facade env 注入点。
- `pkg/llm`：daemon LLM client/service、结构化响应解析、runtime LLM facade routes、session-scoped facade token 和受管 LLM env helper。
- `pkg/events`：webhook/event HTTP routes、event dispatcher，以及 webhook source 与 loader scheduling 的集成。
- `pkg/images`：Docker、OCI cache 和 auto image backend，以及 ImageService。
- `pkg/workspaces`、`pkg/settings`、`pkg/capabilities`、`pkg/dashboard`：各自 owner service 和 HTTP/API handler。

```text
CLI / Web / Connect 客户端
  |
  | Unix socket 或 HTTP/Connect
  v
agent-compose daemon
  |
  | v1/v2 Connect handler、HTTP 路由、scheduler、store
  v
project / run / loader / session 控制面
  |
  | runtime driver
  v
boxlite / docker / microsandbox runtime
  |
  v
guest Jupyter + agent runtime
```

## 进程和传输

`cmd/agent-compose/main.go` 使用 Cobra 提供单二进制多子命令。不带子命令时仍会启动 daemon，推荐显式使用：

```bash
agent-compose daemon
```

daemon construction 已拆成可测试的 app construction：

- 加载 `.env` 和环境配置。
- 初始化 Echo、结构化日志和 DI。
- 注册 `/api/version`、v1/v2 Connect handlers、webhook/event routes、workspace HTTP routes 和 Jupyter proxy routes。
- 按 `HTTP_BASIC_AUTH` 注入可选全局 BasicAuth。
- 通过 `agentcompose.Register(di)` 注册服务图。
- 通过 `agentcompose.StartBackground(di)` 启动 loader manager、event dispatcher、capability proxy 和启动时 session 校准。
- graceful shutdown 时关闭所有 listener，并清理 Unix socket 文件。

daemon 默认监听 Unix socket：

- `AGENT_COMPOSE_SOCKET` 显式设置时使用该路径。
- 未设置时优先使用 `$XDG_RUNTIME_DIR/agent-compose.sock`。
- 否则使用 `/tmp/agent-compose-<uid>.sock`。

只有显式设置 `HTTP_LISTEN` 时才额外启用 TCP HTTP/Connect listener。CLI 连接优先级是 `--host`、`AGENT_COMPOSE_HOST`、默认 Unix socket。

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
agent-compose --host http://127.0.0.1:7410 status
```

## CLI 语义

CLI 不直接操作 runtime、session 文件或 SQLite reconcile 逻辑。它读取并规范化本地 compose 文件，然后调用 daemon v2 API。

当前主命令：

- `config`：本地解析和规范化 `agent-compose.yml`，支持 `--json` 和 `--quiet`，不连接 daemon。
- `up`：调用 `ProjectService.ApplyProject`，创建或更新 project、revision、受管 agent definition 和 scheduler/loader；不会直接创建 run/session。
- `down`：调用 `ProjectService.RemoveProject`，禁用受管 scheduler/loader，并停止该 project 的 running sessions；默认保留 project、run 和 session 历史。
- `ps`：查询 project、agent、latest run 和 running session 状态。
- `run <agent>`：调用 `RunService.RunAgentStream` 手动执行一次 agent；默认创建新 session，支持 `--session-id` 复用已有 session，默认完成后停止 runtime，`--keep-running` 可保留。
- `logs`：按 project、agent、run id 或 session id 查看 run 输出，支持 `--follow`。
- `exec`：调用 `ExecService.ExecStream` 在 running session 内执行命令；可按 session id、run id 或 project/agent selector 定位。
- `images`、`image ls`、`pull`、`image pull`、`rmi`、`image rm`、`image inspect`：调用 `ImageService` 管理 daemon image store。默认 store 由 daemon 的 `IMAGE_STORE_MODE` 决定。
- `inspect <project|agent|run|session>`：查看 project 相关对象详情。

## `agent-compose.yml` 模型

compose 文件解析位于 `pkg/compose`。规范化结果用于本地 `config` 输出、spec hash 和 daemon apply。

示例：

```yaml
name: review-project

variables:
  OPENAI_API_KEY:
    value: ${OPENAI_API_KEY}
    secret: true

workspace:
  provider: git
  url: https://github.com/org/repo.git
  branch: main

agents:
  reviewer:
    provider: codex
    model: gpt-5
    image: ghcr.io/org/agent-runtime:latest
    driver:
      boxlite:
        kernel: s3://bucket/kernel
    env:
      REVIEW_MODE: strict
    scheduler:
      enabled: true
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: "Review the latest workspace state."
        - event:
            topic: git.push
          prompt: "Review changes from the incoming event."

network:
  mode: default
```

同一个 scheduler 也可以用 inline QJS 直接声明 loader 脚本：

```yaml
agents:
  reviewer:
    provider: codex
    image: ghcr.io/org/agent-runtime:latest
    scheduler:
      script: |
        scheduler.interval("hourly-review", function hourlyReview() {
          return scheduler.agent("Review the latest workspace state.");
        }, 3600000);

        function main(payload) {
          return { ok: true, payload };
        }
```

规范化规则：

- `name` 为空时，从 compose 文件所在目录推导。
- agent map key 必须是稳定标识，输出按 agent name 排序。
- driver 采用 one-of：`boxlite`、`docker`、`microsandbox`。省略时默认 `docker`。
- `firecracker` 可出现在 schema 中，但当前规范化直接返回 unsupported。
- `network` 为空或 `mode: default` 可接受，其他网络模式返回 unsupported。
- trigger 支持 `cron`、`interval`、`timeout`、`event`，每个 trigger 必须恰好指定一种类型。
- `scheduler.script` 是 inline QJS scalar，保存到受管 loader 的 `script` 字段；空白脚本视为未设置。
- `scheduler.script` 与非空 `scheduler.triggers` 互斥。当前不支持 `scheduler.script_file`、`import` / `require` 或 bundling。
- `${NAME}` 从 CLI 进程环境或显式注入环境读取；缺失变量会报字段路径化错误，空变量值合法。
- `secret: true` 的值参与 normalized spec 和 hash，但在 YAML/JSON 展示输出中脱敏。
- spec hash 基于 canonical JSON 计算，对 YAML/JSON 字段顺序不敏感。

workspace provider 当前在 project run 准备阶段支持：

- `local`：从 project source path 下的相对路径物化成 file workspace snapshot。
- `git`：生成 git workspace config，后续由现有 workspace provisioning clone。

## API 边界

### v1 Connect API

v1 API 是现有 Web/UI 和兼容客户端的稳定接口。daemon 当前注册：

- `SessionService`
- `KernelService`
- `AgentService`
- `AgentDefinitionService`
- `LLMService`
- `ConfigService`
- `LoaderService`
- `DashboardService`
- `CapabilityService`

v1 仍承担 session、cell、agent event、global env、workspace config、loader、dashboard overview 和 capability 管理等能力。

### v2 Connect API

v2 API 面向 project/run/image/exec：

- `ProjectService`
  - `ValidateProject`
  - `ApplyProject`
  - `GetProject`
  - `ListProjects`
  - `RemoveProject`
  - `WatchProject` 目前仅由 unimplemented handler 覆盖。
- `RunService`
  - `RunAgent`
  - `RunAgentStream`
  - `GetRun`
  - `ListRuns`
  - `StopRun`
- `ExecService`
  - `Exec`
  - `ExecStream`
- `ImageService`
  - `ListImages`
  - `PullImage`
  - `InspectImage`
  - `RemoveImage`

`RemoveProject(remove_history=true)` 当前返回 unimplemented；默认 `down` 语义是保留历史。`ImageService` 支持 Docker daemon store 和 OCI cache store；request store 为 `UNSPECIFIED` 时由 daemon 的 image store mode 决定。

v2 `ProjectSpec` 是 CLI 和 API 客户端传递 compose 当前态的 wire shape。`AgentSpec.scheduler` 包含：

- `enabled`
- 声明式 `triggers`
- inline QJS `script`

服务端收到 v2 `ProjectSpec` 后会先转换回 compose YAML shape，再走 `pkg/compose` 的 parse/normalize 规则；`ProjectSpecResponse` 也会把 normalized `scheduler.script` 回传给 CLI 和 API 响应。这样本地 `config`、CLI `up`、`ValidateProject` 和直接 v2 API 调用使用同一套字段、互斥规则和 spec hash 计算。

### HTTP 路由

除 Connect API 外，daemon 还注册以下 HTTP 路由：

- `/api/version`
- webhook / event ingress：`/api/webhooks/:topic`、`/api/events...`
- file workspace 辅助路由：`/api/agent-compose/workspaces/:workspaceID/files`、`upload`、`download`
- Jupyter proxy：`<JupyterProxyBasePath>/:sessionID` 和 `<JupyterProxyBasePath>/:sessionID/*`。当前配置默认 base path 是 `/jupyter`。

Jupyter proxy 的实现位于 `pkg/agentcompose/proxy.go`。`GetSessionProxy` 只返回 proxy 入口信息；真实 HTTP/WebSocket 转发由上述 HTTP routes 完成。session 创建时会把 `Config.JupyterProxyBasePath` 写入 `proxyPath`，当前代码默认值是 `/jupyter`。

## Project 应用和调度

Project 是 `agent-compose.yml` 在 daemon 中的持久化实例。project id、受管 agent id、scheduler id、loader id 和 run id 都由稳定规则生成。

`ApplyProject` 的当前行为：

- 校验并规范化 v2 `ProjectSpec`。
- 按 spec hash 幂等保存 project revision；重复 apply 同一 spec 不产生重复 revision。
- 写入 `project_agent`。
- 将每个 agent spec reconcile 成受管 `AgentDefinition`，通过 `managed_project_id`、`managed_project_revision`、`managed_agent_name` 与手工 agent definition 隔离。
- 将 scheduler 编译成受管 Loader/Trigger。声明式 `scheduler.triggers` 生成受管 loader script；inline `scheduler.script` 直接作为受管 loader script，并用 loader validation 返回的 triggers 写入 `loader_trigger` 和 `ProjectScheduler.trigger_count`。
- 删除或禁用 spec 中移除的 scheduler，并刷新 loader manager。
- 不直接创建 run 或 session。
- reconcile 失败时返回 `issues`，并避免留下会继续触发错误 agent 的半成品 scheduler。

受管资源只修改带 managed metadata 的 agent definition、loader 和 trigger。同名手工资源不会被覆盖或删除。

`ValidateProject` 和 `ApplyProject` 对 scheduler 使用同一条构建路径。声明式 scheduler 只做 compose 和 loader trigger 结构校验；inline QJS scheduler 会调用现有 `LoaderManager.Validate(ctx, "scheduler", script)`，由 QJS loader engine 求值脚本并收集 `scheduler.interval`、`scheduler.timeout`、`scheduler.on`、`scheduler.cron` 注册出来的 triggers。语法错误、重复 trigger id、非法 timer/cron/event 参数会转换成路径为 `agents.<name>.scheduler.script` 的 project validation issue。

reconcile 顺序保持保守：先把 `ProjectScheduler` 和受管 `Loader` staged 为 disabled，替换 loader triggers，再启用 loader 和 scheduler。替换 trigger 或启用失败时会执行 cleanup，避免留下已经启用但 trigger/script 不一致的 scheduler。

## Run 执行流水线

Run 是一次 agent 执行记录，可来自 CLI manual run、scheduler trigger 或后续 API 客户端。

`RunService.RunAgent` 和 `RunAgentStream` 复用同一条 coordinator 路径：

1. 按 project id + agent name 解析 project agent 和受管 agent definition。
2. 创建 `project_run` pending 记录，记录 source、scheduler/trigger、prompt、driver、image 等元数据。
3. 合并运行环境，优先级从低到高为 global env、project variables、agent env、run request env。
4. 按 project/agent workspace spec 准备 local/git workspace snapshot。
5. 创建新 session，或按 `--session-id` 复用已有 session。
6. 给 session 写入 project、agent、run_id、scheduler_id、source 等 tag。
7. 标记 run 为 running，并调用现有 agent executor。
8. streaming 请求实时发送 start/output/completed 事件。
9. 成功、失败、取消、workspace 准备失败、session 启动失败、agent 执行失败和 stream 发送失败都会落库为终态 run。
10. 默认停止 runtime 并保留 session/run 历史；`KEEP_RUNNING` cleanup policy 可保留运行中 session。

状态类查询以 SQLite 中的 project/run 关系为主要来源；session tag 用于兼容查询、`down` 停止 project session 和文件级调试。

### Agent system prompt（Phase 1）

`AgentDefinition.system_prompt` 持久化在 agent definition（手动与受管）上，并通过 v1/v2 API 与 Agents UI 暴露。执行时 host 解析该字段，并为 guest runtime 物化 agent identity。

分层 prompt 模型：

1. **Agent Identity** — 每个 agent 的 `system_prompt`（为空时省略）
2. **Capabilities (MPI)** — OctoBus capset catalog，位于 `runtime/mpi/catalog.md`
3. **Per-turn task** — `--message-file` 中的用户消息（不与 identity 混合）

传输使用 session state 树下的**固定约定路径**：

```text
<session>/state/agents/system-prompts/system-prompt.txt  →  guest /data/state/agents/system-prompts/system-prompt.txt
```

解析路径：

- 受管 project run：`RunService` 将 `run.ManagedAgentID` 传入 `ExecuteAgentRequest`
- Loader run：`loaderRunHost.Agent` 传入 loader 绑定的 agent definition id
- Session chat：依赖 session tags `source=agent` 与 `agent_id`

Guest JS runtime（`runtime/javascript`）从 `--state-root` 读取约定文件，通过
`buildSystemContext` 组合 identity + MPI，并注入 Codex `developer_instructions`、
Claude `systemPrompt.append` 或 Gemini user prompt prepend。

详见 [agent_system_prompt_design.md](agent_system_prompt_design.md) 与
[agent-compose-runtime_contract.md](agent-compose-runtime_contract.md)。

## 命令执行和镜像

`ExecService` 不创建 session。它只能在已有 running session 内执行命令，定位方式包括：

- 显式 `session_id`。
- 显式 `run_id`，再通过 run 关联 session。
- project/agent selector，要求能唯一匹配一个 running session。

默认 cwd 是 guest workspace 路径 `/workspace`，也可以通过请求覆盖。

`ImageService` 当前实现有三个 backend 入口：

- `ListImages` 支持 reference query、`--all` 和分页。
- `PullImage` 支持 platform。
- `InspectImage` 查询 image 详情。
- `RemoveImage` 支持 force 和 prune children。

store 选择规则：

- request store 为 `DOCKER_DAEMON` 时强制使用 Docker daemon。
- request store 为 `OCI_CACHE` 时强制使用 daemonless OCI cache。
- request store 为 `UNSPECIFIED` 时使用 `IMAGE_STORE_MODE`：`docker` 强制 Docker，`oci` 强制 OCI cache，`auto` 先短超时探测 Docker daemon，Docker 可用时使用 Docker，不可用时使用 OCI cache。

OCI cache 使用 `pkg/imagecache` 和 go-containerregistry 从 registry 拉取镜像，不依赖 dockerd/containerd/Podman。`PullImage` 使用 go-containerregistry `remote.Image`、默认 keychain、platform selector 和配置的 insecure registry 列表；未显式填写 platform 时使用 daemon 所在平台。OCI cache 保存 metadata、OCI Image Layout、BoxLite materialized layout 和 Microsandbox rootfs。OCI image proto 会填充 `Store=OCI_CACHE`、`Oci` metadata、repo tags/digests、manifest/config digest、platform、size、labels 和 store status。

OCI cache 的查询和删除语义与 Docker backend 保持 v2 API 形状一致，但状态来源是 cache metadata：

- `ListImages` 的 query 会匹配 requested ref、normalized ref、repo tag、repo digest、manifest digest、config digest 和 cache key，并支持子串过滤。
- `InspectImage` 使用同一组 lookup key；digest lookup 会忽略 `sha256:` 前缀差异。
- `RemoveImage` 默认只删除命中的 metadata ref；同一 image identity 有多个 ref 时需要 `force`，`prune_children` 在 OCI cache 中不会清理 blob，会返回 warning。blob 清理由后续专门机制处理，当前删除是保守的 metadata 删除。
- not found、invalid reference、conflict、internal 和 unavailable 错误会分别映射为稳定 Connect code；错误消息保留 operation、image ref 和 cache endpoint。

`up/run` 对 `docker` driver 会确保所需 image 可用；`boxlite` 和 `microsandbox` 的 project/run prepare 不因为 Docker daemon 不可用而失败。runtime 启动时它们按 Docker-first 规则解析镜像：Docker daemon 可用时沿用本地 Docker materialization；Docker 不可用或 Docker image miss 时使用 OCI cache，BoxLite 消费 OCI layout，Microsandbox 消费展开后的 rootfs。Docker runtime 不直接消费 OCI cache。

## 存储模型

默认数据根目录：

- `DATA_ROOT` 为空时使用 `$XDG_DATA_HOME/agent-compose`。
- 如果 `XDG_DATA_HOME` 为空，则使用 `$HOME/.local/share/agent-compose`。
- `SESSION_ROOT` 为 `<DATA_ROOT>/sessions`。
- `IMAGE_CACHE_ROOT` 为空时为 `<DATA_ROOT>/images`。

image store 相关配置：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `IMAGE_STORE_MODE` | `auto` | `UNSPECIFIED` ImageService request 的默认 store 选择模式。合法值：`auto`、`docker`、`oci`。 |
| `IMAGE_CACHE_ROOT` | `<DATA_ROOT>/images` | daemonless OCI cache root；保存 metadata 和 OCI Image Layout。runtime materialization 目录位于该 root 的同级 `image-cache/` 下。 |
| `IMAGE_INSECURE_REGISTRIES` | 空 | OCI cache pull 使用的不安全 registry host 列表，支持逗号、分号或换行分隔，并会 trim 每项空白。 |
| `IMAGE_REGISTRY` | `docker.io` | unqualified image reference 的默认 registry，也用于 runtime smoke 默认镜像解析。 |

常见布局：

```text
data/agent-compose/
├── data.db
├── images/
│   ├── metadata.json
│   └── oci/
├── image-cache/<image-id>/
│   ├── oci/
│   └── rootfs/
└── sessions/<session_id>/
    ├── metadata.json
    ├── workspace/
    ├── context/
    ├── home/
    ├── runtime/
    ├── state/
    │   ├── cells.json
    │   └── events.json
    ├── logs/
    ├── vm/
    │   └── runtime.json
    └── proxy/
        └── jupyter.json
```

session 目录保存 session metadata、workspace、home backing、runtime 共享目录、cell/event timeline、VM state 和 proxy state。默认配置下，`images/` 是 OCI cache root；`image-cache/<image-id>/oci` 是 BoxLite materialized OCI layout，`image-cache/<image-id>/rootfs` 是 Microsandbox materialized rootfs。

`DATA_ROOT/data.db` 当前承载：

- global env
- workspace config
- agent definition
- loader / loader trigger / loader binding
- loader run / loader event
- webhook topic event
- project / project_revision / project_agent / project_scheduler / project_run

project 相关表：

- `project`
- `project_revision`
- `project_agent`
- `project_scheduler`
- `project_run`

受管 agent definition 和 loader 通过现有表上的 managed metadata 列隔离：

- `managed_project_id`
- `managed_project_revision`
- `managed_agent_name`
- `managed_scheduler_id`，仅 loader

## Session 和 Runtime

Session 是底层 runtime 生命周期单位。当前支持三个 runtime driver：

- `boxlite`
- `docker`
- `microsandbox`

默认 driver 由 `RUNTIME_DRIVER` 控制，空值时为 `docker`。默认 guest image 是 `debian:bookworm-slim`。

`CreateSession` 当前主流程：

1. 解析请求里的 env、tags、workspace id、driver、guest image。
2. 合并 global env 和请求 env。
3. 创建 session 目录，初始化 metadata、VM state 和 proxy state。
4. 如果设置 workspace id，则准备对应 workspace。
5. 通过 driver 启动 runtime。
6. 标记 session 为 `RUNNING`。
7. 记录 `session.created` 事件，并发布 `agent-compose.session.created` topic event。

`ResumeSession` 会重新准备 workspace 并启动 runtime，成功后记录 `session.resumed` 并发布对应 topic event。`StopSession` 停止 runtime，标记 `STOPPED`，记录 `session.stopped` 并发布对应 topic event。

服务启动时会校准 persisted session runtime state；`GetSession`、`ListSessions` 和 `StopSession` 也会触发校准逻辑。

guest 路径默认值：

| host 路径 | guest 路径 | 用途 |
|---|---|---|
| `<session>/workspace` | `/workspace` | Jupyter root、cell/agent/command cwd |
| `<session>/state` | `/data/state` | cell artifact、agent prompt、provider state |
| `<session>/runtime` | `/data/runtime` | 运行期共享资源 |
| `<session>/logs` | `/data/logs` | Jupyter 日志 |
| `<session>/home` 或其子路径 | `/root` 或其子路径 | Codex、Claude、Gemini、git 等工具配置和状态 |

更细的 mount manifest 设计见 [runtime_mount_manifest_design.md](runtime_mount_manifest_design.md) 和 [runtime_mount_manifest_driver_specific_design.md](runtime_mount_manifest_driver_specific_design.md)。

## Loader 运行时

loader 当前 runtime 是 `scheduler`，支持：

- `interval`
- `timeout`
- `event`
- `cron`

project compose 的 `scheduler.script` 使用同一套 runtime。脚本在 validate/apply 时会被求值以收集 triggers；`scheduler.agent`、`scheduler.llm`、`scheduler.exec`、`scheduler.shell`、`scheduler.event.publish` 和 session RPC 等有副作用或 host 依赖的 API 应放在 `main()` 或 trigger callback 中。

`scheduler` 是 loader QJS 环境的唯一产品级全局对象。它的职责是注册 trigger、维护轻量状态、发布事件，并把需要 sandbox 能力的工作交给 runtime session 执行。QJS 层不负责承载复杂 Node.js 工作流、npm 依赖或长时间业务逻辑。

需要完整 Node.js 能力时，当前实现通过 `scheduler.exec` / `scheduler.shell` 在 loader session 内调用 workspace 脚本，或通过 `scheduler.agent` / `scheduler.llm` 调用现有 agent 和 LLM 能力。独立的 `scheduler.run(file, input, options)`、runtime workflow context、workflow bridge token 和 `agent-compose-runtime workflow` 子命令尚未成为当前 API 契约；设计文档不把这些草案接口视为已实现能力。

`LoaderManager.Start()` 在 daemon background 启动 schedule loop 和 event loop。

主要 JS API：

- `scheduler.log(message, payload)`
- `scheduler.agent(prompt, options)`
- `scheduler.llm(prompt, options)`
- `scheduler.state.get(key)`
- `scheduler.state.set(key, value)`
- `scheduler.state.delete(key)`
- `scheduler.exec(request)`
- `scheduler.shell(script, options)`
- `scheduler.event.publish(topic, payload)`
- `scheduler.interval(...)`
- `scheduler.timeout(...)`
- `scheduler.on(...)`
- `scheduler.cron(...)`

`scheduler.agent` 和 `scheduler.llm` 支持 `outputSchema` / `schema`。传 `scheduler.z` schema 时会生成 JSON Schema 并在返回后本地校验；传 plain JSON Schema 时会做 JSON parse。

### Daemon LLM client

`scheduler.llm`、`LLMService.Generate` 和 SDK `runtime.llm` 都委托给 Go daemon 中的 `LLMClient`。配置是 daemon 全局的：

- `LLM_API_ENDPOINT`、`LLM_API_KEY`、`OPENAI_API_KEY`、`LLM_MODEL`、`LLM_TIMEOUT`
- `LLM_API_PROTOCOL`：`responses`（默认，OpenAI Responses API）或 `chat_completions`（OpenAI 兼容 Chat Completions；别名：`chat`、`chat_completion`）

UI/数据库中的 global env 会覆盖进程环境变量。`chat_completions` 仅用于单次文本生成，不会创建具备 workspace 能力的 agent session，也不提供文件、命令或 MCP 工具访问。使用 `outputSchema` 时，`chat_completions` 通过 prompt 引导并设置 `json_object`，不等价于 Responses API strict JSON Schema。

Guest agent provider（`codex`、`claude`、`gemini`、`opencode`）仍是 guest 容器内的 CLI runner，使用各自的 API key 和 session 状态。

loader 内还暴露 v1 `SessionService` 的 unary RPC bridge：

- `scheduler.session.createSession(request)`
- `scheduler.session.resumeSession(request)`
- `scheduler.session.stopSession(request)`
- `scheduler.session.getSession(request)`
- `scheduler.session.listSessions()`
- `scheduler.session.getSessionProxy(request)`

方法名使用 lower camel case，也保留原始 proto 方法名别名，例如 `scheduler.session.ResumeSession(...)`。

## 前端服务

daemon 不托管 Web/UI 静态资源，也不再支持 `HTTP_ROOT` / `UI_ROOT` 静态根目录配置。daemon 主进程只注册 API、Connect、webhook/workspace 和 Jupyter proxy 路由。

当前 Docker 部署提供独立前端服务：

- `nginx/Dockerfile` 构建 `agent-compose-frontend`。
- compose 中有 `agent-compose` daemon 和 `agent-compose-frontend` 两个服务。
- 前端服务负责服务 `frontend/` 构建产物，并反向代理 daemon 的 v1/v2 Connect API、`/api/` 和 Jupyter proxy routes。

共享 playground 的具体构建、部署和验证流程见 [playground_setup.md](playground_setup.md)。

## 关键约束

- daemon 是状态和 reconcile 权威；CLI 不直接写 SQLite 或 session 文件。
- `agents.<name>` 是 agent definition，不是常驻 runtime。
- `up` 管理定义和 scheduler，不等同于运行 agent。
- `run` 是一次性执行，默认结束后停止 runtime。
- `down` 禁用受管 scheduler/loader 并停止 project running sessions，默认不删除历史。
- v1 API 必须保持兼容，v2 API 承载 project/run/exec/image 主路径。
- Web/UI 作为独立服务部署，不进入 daemon Docker 镜像。
