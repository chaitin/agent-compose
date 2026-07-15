# agent-compose 总体设计

本文档描述当前代码中的 agent-compose 架构和已经落地的 daemon + CLI 设计。早期重构过程、阶段计划和验收清单不再作为设计文档保留。

当前代码事实以以下入口为准：

- CLI 和 daemon 入口：`cmd/agent-compose/main.go`
- daemon 服务注册：`pkg/agentcompose/app/app.go`
- compose 解析和规范化：`pkg/compose/`
- v2 API：`proto/agentcompose/v2/agentcompose.proto`
- project/run 持久化：`pkg/storage/configstore/project_store.go`、`pkg/storage/configstore/run_coordinator_store.go`
- Jupyter 代理：`pkg/agentcompose/proxy/proxy.go`
- loader 运行时和调度：`pkg/loaders/engine.go`、`pkg/agentcompose/app/loader_controller.go`、`pkg/agentcompose/adapters/loader_session_runner.go`
- 领域模型：`pkg/model/`
- project/run owner helper：`pkg/projects/`、`pkg/runs/`
- sandbox 执行 owner helper：`pkg/sessions/` 兼容 lifecycle package、`pkg/execution/`
- 独立前端镜像：`agent-compose-ui` 仓库

## 架构目标

agent-compose 是一个 agent/sandbox 控制面。它采用类似 Docker Engine + CLI + Compose 的形态，但保留自身的 agent、scheduler、workspace、runtime driver 和 notebook 代理领域模型。

核心边界：

- daemon 是状态权威，负责持久化、scheduler、runtime 生命周期、Connect API、HTTP API 和 Jupyter proxy。
- CLI 是 daemon 客户端，负责读取本地 `agent-compose.yml`、做本地语法校验和规范化、调用 daemon API 并渲染输出。
- `agent-compose.yml` 描述 project 和 agent definition，不直接描述一个已经运行的 sandbox。
- Web/UI 不再打进 daemon 镜像，也不由 daemon 进程托管静态资源；它作为独立前端服务部署。
- v1 session-centric API 继续保留给现有 Web/UI 和兼容客户端；v2 API 是 CLI 和新客户端的主路径。

```text
CLI / Web / Connect 客户端
  |
  | Unix socket 或 HTTP/Connect
  v
agent-compose daemon
  |
  | v1/v2 Connect handler、HTTP 路由、scheduler、store
  v
project / run / loader / sandbox 控制面
  |
  | runtime driver
  v
boxlite / docker / microsandbox runtime
  |
  v
guest Jupyter + agent runtime
```

## 构建产物和编译能力

当前构建只有两个正式 profile，Task、CI 和 Dockerfile 都选择 profile，不自行拼装 CGO 或 build tags：

| Profile | 产物 | `compiled_drivers` |
|---|---|---|
| `darwin-docker` | macOS 原生 `darwin/amd64`、`darwin/arm64` 二进制 | `docker` |
| `linux-full` | Linux 原生二进制和发布的 `linux/amd64`、`linux/arm64` daemon 镜像 | `docker,boxlite,microsandbox` |

`task build:agent-compose` 按宿主选择原生 profile；`task build:agent-compose:darwin` 和 `task build:agent-compose:linux` 是显式入口。`scripts/build-agent-compose-binary.sh` 是 CGO、build tags、目标元数据和 `BuildVersion` linker flags 的唯一 owner，Task、两个 Dockerfile 和 CI 只选择 profile。`linux-full` 在执行 `go build` 前会校验 BoxLite 的 header、library、runtime executable，以及 Microsandbox 的 executable 和 shared library；缺少任一产物都会提前失败。`build:agent-compose:boxlite` 已废弃，只保留为 `linux-full` 的兼容 alias。Linux full 构建从 Dockerfile owner 导出 BoxLite/Microsandbox native artifact，因此普通 Linux full 开发构建以 Docker 为标准前置依赖。

`agent-compose --json version` 的稳定字段是 `version`、`os`、`arch` 和 `compiled_drivers`；daemon `/api/version` 在兼容 envelope 中追加相同 build 信息，`status --json` 保留完整响应，`version` 文本和 status 表格保持兼容。`compiled_drivers` 是编译能力：它只证明真实 driver 实现已编入当前 binary，并保持 `docker`、`boxlite`、`microsandbox` 的稳定顺序。它不是 runtime availability 或 health 探测，不证明 Docker daemon 可连接、宿主存在 KVM、runtime artifact 完整或 VM 能成功启动。

产品识别某个 driver 名称不等于当前产物编译了该 driver。默认 driver 缺失时 daemon 在 service graph 构造阶段失败；create、apply、scheduler 和 runtime 操作会在持久化或其他副作用前拒绝未编译 driver。历史对象仍可读取，失败不会改写已保存的 driver 或 runtime state。该错误在 Connect API 中映射为 `CodeUnimplemented`，CLI 保持 unsupported exit code。BoxLite 和 Microsandbox wrapper 保持 lazy，构造 provider 不初始化 native runtime 或访问 KVM；纯 compose 解析仍接受所有产品支持的 driver 名称。

macOS/Linux 原生二进制只用于本地开发和 CI 验证，不进入 GitHub Release。发布边界保持为 GHCR 中的 multi-arch Linux daemon/guest 镜像；GitHub Release 只包含 `install.sh`、installer bundle 和校验和，没有 per-architecture binary 资产。完整 Linux daemon 镜像可以由 macOS Docker Desktop 运行 Docker driver，但镜像仍是 Linux 产物，也不承诺在 Docker Desktop 中提供 BoxLite/Microsandbox 所需的 KVM 能力。

## 进程和传输

`cmd/agent-compose/main.go` 使用 Cobra 提供单二进制多子命令。不带子命令时仍会启动 daemon，推荐显式使用：

```bash
agent-compose daemon
```

daemon construction 已拆成可测试的 app construction：

- 加载 `.env` 和环境配置。
- 初始化 Echo、结构化日志和 DI。
- 注册 `/api/version`、v1/v2 Connect handlers、webhook/event routes、workspace HTTP routes 和 Jupyter proxy routes。
- 通过 `agentcompose.Register(di)` 注册服务图。
- 通过 `agentcompose.StartBackground(di)` 启动 loader manager、event dispatcher、capability proxy 和启动时 sandbox 校准。
- graceful shutdown 时关闭所有 listener，并清理 Unix socket 文件。

daemon 默认监听 Unix socket：

- `AGENT_COMPOSE_SOCKET` 显式设置时使用该路径。
- 未设置时优先使用 `$XDG_RUNTIME_DIR/agent-compose.sock`。
- 否则使用 `/var/run/agent-compose.sock`。

只有显式设置 `HTTP_LISTEN` 时才额外启用 TCP HTTP/Connect listener。CLI 连接优先级是 `--host`、`AGENT_COMPOSE_HOST`、默认 Unix socket。`HTTP_LISTEN` 是 daemon internal API 入口，不是浏览器公网入口；当它绑定非 loopback 地址时，配置加载会输出告警，提醒只能放在可信网络或 agent-compose-ui server 后面。

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
agent-compose --host http://127.0.0.1:7410 status
```

## CLI 语义

CLI 不直接操作 runtime、sandbox 文件或 SQLite reconcile 逻辑。它读取并规范化本地 compose 文件，然后调用 daemon v2 API。

当前主命令：

- `config`：本地解析和规范化 `agent-compose.yml`，支持 `--json` 和 `--quiet`，不连接 daemon。
- `up`：调用 `ProjectService.ApplyProject`，创建或更新 project、revision、受管 agent definition 和 scheduler/loader；不会直接创建 run/sandbox。
- `down`：调用 `ProjectService.RemoveProject`，禁用受管 scheduler/loader，并停止该 project 的 running sandboxes；默认保留 project、run 和 sandbox 历史。
- `ps`：查询 project、agent、latest run 和 running sandbox 状态。
- `run <agent>`：调用 `RunService.RunAgentStream` 手动执行一次 agent；默认创建新 sandbox，支持 `--sandbox` 复用已有 sandbox，默认完成后停止 runtime，`--keep-running` 可保留。
- `logs`：按 project、agent、run id 或 sandbox id 查看 run 输出，支持 `--follow`。
- `exec`：调用 `ExecService.ExecStream` 在 running sandbox 内执行命令；用 positional `<sandbox>` 定位目标。
- `images`、`image ls`、`pull`、`image pull`、`rmi`、`image rm`、`image inspect`：调用 `ImageService` 管理 daemon image store。默认 store 由 daemon 的 `IMAGE_STORE_MODE` 决定。
- `cache ls`、`cache inspect`、`cache prune`、`cache rm`：调用 `CacheService` 查看、检查、dry-run 和显式删除 daemon runtime cache items。CLI 不直接读取或删除 daemon cache path。
- `inspect <project|agent|run|sandbox>`：查看 project 相关对象详情。`inspect session` 保留为 deprecated compatibility alias。

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

```

Docker project 可以显式启用相互隔离的命名 bridge 网络：

```yaml
name: networked-project

networks:
  frontend: {}
  backend:
    driver: bridge

agents:
  api:
    networks: [frontend, backend]
    expose: ["8080"]
  web:
    networks: [frontend]
    ports: ["127.0.0.1:18080:8080"]
  worker:
    networks: [backend]
```

命名网络只支持 Docker agent，当前仅支持 `bridge` driver。agent 可以通过
agent 名或唯一的 `<agent>-<sandbox-short-id>` alias 解析同一网络中的 peer。
没有显式 attachment 的 agent 会加入隐式的 project `default` 网络；第一项
attachment 是 primary network，并提供默认网关。

`expose` 只写入 Docker 端口元数据，它不是防火墙规则，也不会发布宿主机端口。
`ports` 显式发布 TCP 端口，短格式默认绑定 `127.0.0.1`；省略 host port 时由
Docker 动态分配。只配置端口而不配置 named networks 时仍使用现有单 bridge。

任何有效的 named-network attachment 都会在 daemon/sandbox baseline bridge
之外增加 project bridge，因此进入多网络模式。该模式要求通过 Unix socket
访问本机 Linux Docker Engine，并要求 agent-compose daemon 是 native 进程，
或者使用 `network_mode: host` 的容器。默认 bridge-mode Compose 部署仍可校验
和保存这类 project，但启动 sandbox 时会在创建 project network/container
之前失败。未配置 named networks 的 project 保持现有共享单 bridge 行为。
旧的单数 `network` 字段已经删除；`networks` 是唯一的 project 网络配置入口。

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

CLI 编写的 compose 文件也可以使用显式脚本来源：

```yaml
agents:
  reviewer:
    scheduler:
      script:
        url: ./scripts/reviewer-scheduler.js
```

支持无 scheme 的相对/绝对路径、`file://`、`http://` 和 `https://`。`config`
与 `up` 在 CLI 本机解析来源，在计算 hash 或发送 v2 请求前替换成内联快照。
daemon、v2 API、持久化 revision 和 loader runtime 仍只接收脚本文本；URL 不是
运行时 import，只有再次执行 `config` 或 `up` 才会重新获取。

规范化规则：

- `name` 为空时，从 compose 文件所在目录推导。
- agent map key 必须是稳定标识，输出按 agent name 排序。
- driver 采用 one-of：`boxlite`、`docker`、`microsandbox`。省略时默认 `docker`。
- `firecracker` 可出现在 schema 中，但当前规范化直接返回 unsupported。
- trigger 支持 `cron`、`interval`、`timeout`、`event`，每个 trigger 必须恰好指定一种类型。
- `scheduler.script` 可以是 inline QJS scalar，也可以是仅含非空 `url` 字段的
  mapping。URL 内容会规范化成相同的受管 loader 内联 `script` 快照。空白 inline
  脚本视为未设置，URL 内容为空则报错。
- `scheduler.script` 与非空 `scheduler.triggers` 互斥；scalar 不会自动识别成
  URL。当前不支持 `scheduler.script_file`、`import` / `require`、bundling、鉴权
  header 或后台刷新。
- URL 获取总超时 10 秒，最多 5 次重定向，解码后内容上限 1 MiB。文件最终必须
  是普通文件，内容必须为 UTF-8；HTTP(S) 要求 2xx，并拒绝 userinfo、不支持的
  redirect scheme 和 HTTPS 降级到 HTTP。
- `${NAME}` 从 CLI 进程环境或显式注入环境读取；缺失变量会报字段路径化错误，空变量值合法。
- `secret: true` 的值参与 normalized spec 和 hash，但在 YAML/JSON 展示输出中脱敏。
- spec hash 基于 canonical JSON 计算，对 YAML/JSON 字段顺序不敏感。

workspace provider 当前在 project run 准备阶段支持：

- `local`：从 project source path 下的相对路径物化成 file workspace snapshot。
- `git`：生成 git workspace snapshot，在 sandbox 的一次性 workspace provisioning 中 clone。

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
- `CacheService`
  - `ListCaches`
  - `InspectCache`
  - `PruneCaches`
  - `RemoveCache`

`RemoveProject(remove_history=true)` 当前返回 unimplemented；默认 `down` 语义是保留历史。`ImageService` 支持 Docker daemon store 和 OCI cache store；request store 为 `UNSPECIFIED` 时由 daemon 的 image store mode 决定。`CacheService` 是 OCI 物理存储、materialized image、runtime-derived shared image 和 skill artifact 的跨 sandbox 生命周期边界；sandbox 自有 runtime state 归 `SandboxService`。

v2 `ProjectSpec` 是 CLI 和 API 客户端传递 compose 当前态的 wire shape。`AgentSpec.scheduler` 包含：

- `enabled`
- 声明式 `triggers`
- inline QJS `script`（包括已经由 CLI 快照化的 URL 来源）

URL authoring 形态不会进入 wire shape。服务端收到 v2 `ProjectSpec` 后会先转换回 compose YAML shape，再走 `pkg/compose` 的 parse/normalize 规则；`ProjectSpecResponse` 也会把 normalized `scheduler.script` 回传给 CLI 和 API 响应。这样本地 `config`、CLI `up`、`ValidateProject` 和直接 v2 API 调用使用同一套字段、互斥规则和 spec hash 计算。

### HTTP 路由

除 Connect API 外，daemon 还注册以下 HTTP 路由：

- `/api/version`
- webhook / event ingress：`/api/webhooks/:topic`、`/api/events...`
- file workspace 辅助路由：`/api/agent-compose/workspaces/:workspaceID/files`、`upload`、`download`
- Jupyter proxy：`<JupyterProxyBasePath>/:sessionID` 和 `<JupyterProxyBasePath>/:sessionID/*`。当前配置默认 base path 是 `/jupyter`。

Jupyter proxy 的实现位于 `pkg/agentcompose/proxy/proxy.go`。`GetSessionProxy` 只返回 proxy 入口信息；真实 HTTP/WebSocket 转发由上述 HTTP routes 完成。通过 v1-compatible API 创建 sandbox 时会把 `Config.JupyterProxyBasePath` 写入 `proxyPath`，当前代码默认值是 `/jupyter`。

## Project 应用和调度

Project 是 `agent-compose.yml` 在 daemon 中的持久化实例。project id、受管 agent id、scheduler id、loader id 和 run id 都由稳定规则生成。

`ApplyProject` 的当前行为：

- 校验并规范化 v2 `ProjectSpec`。
- 将 project revision 按单调递增序列保存。连续重复 apply 同一 spec 会复用当前
  revision；如果中间经历过其他 spec，再回到之前出现过的 spec hash，也会创建新的
  revision。
- 写入 `project_agent`。
- 将每个 agent spec reconcile 成受管 `AgentDefinition`，通过 `managed_project_id`、`managed_project_revision`、`managed_agent_name` 与手工 agent definition 隔离。
- 将 scheduler 编译成受管 Loader/Trigger。声明式 `scheduler.triggers` 生成受管 loader script；inline `scheduler.script` 直接作为受管 loader script，并用 loader validation 返回的 triggers 写入 `loader_trigger` 和 `ProjectScheduler.trigger_count`。
- 删除或禁用 spec 中移除的 scheduler，并刷新 loader manager。
- 不直接创建 run 或 sandbox。
- reconcile 失败时返回 `issues`，并避免留下会继续触发错误 agent 的半成品 scheduler。

受管资源只修改带 managed metadata 的 agent definition、loader 和 trigger。同名手工资源不会被覆盖或删除。

`ValidateProject` 和 `ApplyProject` 对 scheduler 使用同一条构建路径。声明式 scheduler 只做 compose 和 loader trigger 结构校验；inline QJS scheduler 会调用现有 `LoaderManager.Validate(ctx, "scheduler", script)`，由 QJS loader engine 求值脚本并收集 `scheduler.interval`、`scheduler.timeout`、`scheduler.on`、`scheduler.cron` 注册出来的 triggers。语法错误、重复 trigger name、非法 timer/cron/event 参数会转换成路径为 `agents.<name>.scheduler.script` 的 project validation issue。

reconcile 顺序保持保守：先把 `ProjectScheduler` 和受管 `Loader` staged 为 disabled，替换 loader triggers，再启用 loader 和 scheduler。替换 trigger 或启用失败时会执行 cleanup，避免留下已经启用但 trigger/script 不一致的 scheduler。

## Run 执行流水线

Run 是一次 agent 执行记录，可来自 CLI manual run、scheduler trigger 或后续 API 客户端。

`RunService.RunAgent` 和 `RunAgentStream` 复用同一条 coordinator 路径：

1. 按 project id + agent name 解析 project agent 和受管 agent definition。
2. 创建 `project_run` pending 记录，记录 source、scheduler/trigger、prompt、driver、image 等元数据。
3. 合并运行环境，优先级从低到高为 global env、project variables、agent env、run request env。
4. 按 project/agent workspace spec 准备 local/git workspace snapshot。
5. 创建新 sandbox，或按 `--sandbox` 复用已有 sandbox。
6. 给 sandbox 写入 project、agent、run_id、scheduler_id、source 等 tag。
7. 标记 run 为 running，并调用现有 agent executor。
8. streaming 请求实时发送 start/output/completed 事件。
9. 成功、失败、取消、workspace 准备失败、sandbox 启动失败、agent 执行失败和 stream 发送失败都会落库为终态 run。
10. 默认停止 runtime 并保留 sandbox/run 历史；`KEEP_RUNNING` cleanup policy 可保留运行中 sandbox。

状态类查询以 SQLite 中的 project/run 关系为主要来源；sandbox tag 用于兼容查询、`down` 停止 project sandbox 和文件级调试。

### Agent system prompt（Phase 1）

`AgentDefinition.system_prompt` 持久化在 agent definition（手动与受管）上，并通过 v1/v2 API 与 Agents UI 暴露。执行时 host 解析该字段，并为 guest runtime 物化 agent identity。

分层 prompt 模型：

1. **Agent Identity** — 每个 agent 的 `system_prompt`（为空时省略）
2. **Capabilities (MPI)** — OctoBus capset catalog，位于 `runtime/mpi/catalog.md`
3. **Per-turn task** — `--message-file` 中的用户消息（不与 identity 混合）

传输使用 sandbox state 树下的**固定约定路径**：

```text
<sandbox>/state/agents/system-prompts/system-prompt.txt  ->  guest /data/state/agents/system-prompts/system-prompt.txt
```

解析路径：

- 受管 project run：`RunService` 将 `run.ManagedAgentID` 传入 `ExecuteAgentRequest`
- Loader run：`loaderRunHost.Agent` 传入 loader 绑定的 agent definition id
- v1 session chat 兼容路径：依赖 sandbox tags `source=agent` 与 `agent_id`

Guest JS runtime（`runtime/javascript`）从 `--state-root` 读取约定文件，通过
`buildSystemContext` 组合 identity + MPI，并注入 Codex `developer_instructions`、
Claude `systemPrompt.append` 或 Gemini user prompt prepend。

详见 [agent_system_prompt_design.md](agent_system_prompt_design.md) 与
[agent-compose-runtime_contract.md](agent-compose-runtime_contract.md)。

## 命令执行和镜像

`ExecService` 不创建 sandbox。它只能在已有 running sandbox 内执行命令，定位方式包括：

- 显式 `sandbox_id`。
- 显式 `run_id`，再通过 run 关联 sandbox。
- project/agent selector，要求能唯一匹配一个 running sandbox。

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
- `RemoveImage` 默认只删除命中的 metadata ref；同一 image identity 有多个 ref 时需要 `force`，`prune_children` 在 OCI cache 中不会清理 blob，会返回 warning。OCI cache inventory 把仍存在于 metadata 的 manifest 视为 REQUIRED 引用；显式 cache prune 可以回收无引用 manifest，再 mark/sweep 不可达 blob，同时保留共享 layer。`RemoveImage` 和 CLI `rmi` 不删除 materialized 或 runtime-derived cache。
- not found、invalid reference、conflict、internal 和 unavailable 错误会分别映射为稳定 Connect code；错误消息保留 operation、image ref 和 cache endpoint。

## Sandbox 与 cache 生命周期

所有权边界只取决于制品能否跨 sandbox 生命周期复用。`SandboxService` 管理 runtime instance 和 sandbox-local state；`CacheService` 只管理跨 sandbox 共享制品。volume、外部 workspace source、run artifact 和逻辑 image ref 仍由各自 domain 管理。

创建外部 runtime 前，daemon 会原子写入 `<SANDBOX_ROOT>/.lifecycle/<sandbox-id>.json`。journal 只包含 driver/runtime identity、sandbox 自有 path/resource、共享 cache dependency、生命周期状态和已完成删除阶段，不包含 env、token 或 credential。启动时只为有效历史 sandbox 安全回填缺失记录，并只恢复已经处于 `deleting` 的 journal；损坏、版本未知或路径越界的 ownership 在删除时不会被替换或猜测。

remove 与 resume、proxy start、已有 sandbox 的 run binding 和 exec 共享按 sandbox 锁。删除会持久化 intent 和 `DELETING` 状态；显式 force 时停止 running runtime；随后使用 driver 官方 remove API、释放 accessories、删除 sandbox data/metadata，最后删除 journal。每个阶段都持久、幂等、可重试。`stop` 保留可恢复状态，`rm` 删除全部 sandbox 自有状态。daemon 全局 orphan inventory 仅通过 `sandbox prune --include-orphans` 启用；仍关联任何 project sandbox 记录的资源会被排除，ownership 不完整或不安全的对象即使 forced 也只能展示。

Cache 生命周期由 daemon 权威且显式管理。`pkg/cache` 负责 inventory model、filter、reference policy、TTL classification、path safety 和 dry-run/remove 规则，并且不导入 Connect。所有 remover 都在 source lock 下按 cache ID 重新 inventory，并重新检查 root containment/symlink，不信任 request path。

daemon 组合 OCI 物理存储、materialized image、skill artifact 和已编译 runtime driver 的 `cache.Source`。普通 CGO 不会隐式增加 native driver 能力。Microsandbox shared image 通过 SDK inventory/remove；BoxLite v0.9.7 ABI 不提供安全 image remove/prune，相关 inventory 为只读 `unknown`，agent-compose 绝不直接删除 BoxLite 内部 image 目录。

Cache domains：

- `oci-image-store`：由 image cache 管理的 OCI image metadata/layout。
- `materialized-image-cache`：从镜像派生出的 runtime 输入，例如 `<DATA_ROOT>/image-cache` 下的 BoxLite OCI layout 和 Microsandbox rootfs。
- `runtime-derived-cache`：runtime driver home 下的共享 runtime image artifact。
- `skill-artifact-cache`：完成的 content-addressed skill artifact 和中断的临时/lock 项。

OCI inventory 遍历 manifest graph，包括嵌套 index 和共享 config/layer blob。image metadata 是 manifest 的 `REQUIRED` 引用。manifest 删除会先在 image-cache lock 下更新 OCI index，再 mark/sweep 不可达 blob；中断 sweep 只会留下可识别 orphan blob，不会生成悬空 index。materialized image metadata 和配置中的 skill spec 是 `ADVISORY`，running/stopped sandbox journal 记录的实际依赖是 `REQUIRED`。skill resolve 使用 root shared lock 和 artifact lock，prune 使用 root exclusive lock 和 artifact lock。artifact manifest 只保存 source type、content/commit identity、created/last-used time，不保存 source credential。

保护规则保持保守：

- `active`、`referenced` 和 `unknown` 永不删除。未指定 reference policy 时保守按 `REQUIRED` 处理；`ADVISORY` 不阻止删除。
- `unused`、`expired`、`orphaned` 只有在 request 强制执行时删除。
- `cache prune` 和 `cache rm` 默认 dry-run；真实删除必须显式传 `--force`，但 force 不能绕过引用保护。

`CACHE_TTL` 默认 `168h`；`0` 禁用 expired 判定，负数或非法值会导致启动配置校验失败。TTL 只改变 inventory 状态，不触发后台/启动删除；operator 必须显式执行 `cache prune --expired --force`。`--older-than` 保持独立过滤能力。

`up/run` 对 `docker` driver 会确保所需 image 可用；`boxlite` 和 `microsandbox` 的 project/run prepare 不因为 Docker daemon 不可用而失败。runtime 启动时它们按 Docker-first 规则解析镜像：Docker daemon 可用时沿用本地 Docker materialization；Docker 不可用或 Docker image miss 时使用 OCI cache，BoxLite 消费 OCI layout，Microsandbox 消费展开后的 rootfs。Docker runtime 不直接消费 OCI cache。

## 存储模型

默认数据根目录：

- `DATA_ROOT` 为空时使用 `$XDG_DATA_HOME/agent-compose`。
- 如果 `XDG_DATA_HOME` 为空，则使用 `$HOME/.local/share/agent-compose`。
- `SANDBOX_ROOT` 默认为 `<DATA_ROOT>/sandboxes`。为兼容旧数据，当新旧 root
  环境变量都未设置且 `<DATA_ROOT>/sessions` 是非空目录时，daemon 会使用该目录并输出 warning。
- `IMAGE_CACHE_ROOT` 为空时为 `<DATA_ROOT>/images`。

image store 相关配置：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `IMAGE_STORE_MODE` | `auto` | `UNSPECIFIED` ImageService request 的默认 store 选择模式。合法值：`auto`、`docker`、`oci`。 |
| `IMAGE_CACHE_ROOT` | `<DATA_ROOT>/images` | daemonless OCI cache root；保存 metadata 和 OCI Image Layout。runtime materialization 目录位于该 root 的同级 `image-cache/` 下。 |
| `CACHE_TTL` | `168h` | 共享 cache 的 expired 分类阈值。`0` 禁用 expired；非法值或负数导致启动失败。该配置不会触发自动删除。 |
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
├── skills/<content-id>/
└── sandboxes/
    ├── .lifecycle/<sandbox-id>.json
    └── <sandbox_id>/
        ├── metadata.json
        ├── workspace/
        ├── context/
        ├── home/
        ├── runtime/
        ├── state/
        │   ├── cells.json
        │   ├── events.jsonl
        │   └── workspace-provisioning/
        │       └── attempt-<id>/
        ├── logs/
        ├── vm/
        │   └── runtime.json
        └── proxy/
            └── jupyter.json
```

sandbox 目录保存 sandbox metadata、workspace、home backing、runtime 共享目录、cell/event timeline、VM state 和 proxy state。默认配置下，`images/` 是 OCI cache root；`image-cache/<image-id>/oci` 是 BoxLite materialized OCI layout，`image-cache/<image-id>/rootfs` 是 Microsandbox materialized rootfs。

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
- `project_revision`：project spec 的追加式历史，主键为 `(project_id, revision)`。
  `spec_hash` 用于标识内容并建立普通索引方便查询，但不唯一，因为不同 revision
  可以有意保存相同的 spec 内容。
- `project_agent`
- `project_scheduler`
- `project_run`

受管 agent definition 和 loader 通过现有表上的 managed metadata 列隔离：

- `managed_project_id`
- `managed_project_revision`
- `managed_agent_name`
- `managed_scheduler_id`，仅 loader

## Sandbox 和 Runtime

Sandbox 是底层 runtime 生命周期单位。当前支持三个 runtime driver：

- `boxlite`
- `docker`
- `microsandbox`

默认 driver 由 `RUNTIME_DRIVER` 控制，空值时为 `docker`。默认 guest image 是 `debian:bookworm-slim`。

### Workspace Provisioning 与 Resume

Workspace Source 是 sandbox workspace 的一次性 seed。file workspace 内容、project local file workspace snapshot，或保存在 sandbox snapshot 中的 Git 配置，只会由该 sandbox 首次进入 `ready` 之前的初始 provisioning attempt 读取和物化。生成的 `<sandbox>/workspace` 是独立且可写的副本，不会与 source 持续同步。

Sandbox metadata 持久化 version 1 workspace provisioning 状态，取值如下：

- `pending`：带 workspace 的 sandbox 已存在，但首次 provisioning 尚未完成。
- `failed`：最近一次首次 provisioning 在 runtime 启动前失败；后续启动可以经 `pending` 重试。
- `ready`：provisioning 已完成，或 legacy sandbox 已被认定为完成初始化；这是该 sandbox 生命周期内的终态。

允许的状态转换只有 `pending -> ready|failed` 和 `failed -> pending`；每次成功转换都会更新 UTC `updated_at`。未知 version 或 status 会 fail closed，不修改 workspace。该记录是 sandbox `metadata.json` 内部的 `workspace_provisioning` 字段，不会暴露到 `SandboxSummary`、公开 API 或列表过滤条件。

所有可能启动或重新启动 workspace-backed sandbox 的生命周期路径共享同一个进程级 `Provisioner`：v1 session create/resume、`sessions.Lifecycle.ResumeLoaded`、Jupyter `EnsureProxyReady`、loader sandbox create/sticky resume，以及 project run sandbox create/reuse。它重新加载持久化 metadata，并根据 provisioning 状态决策，而不依赖 create/resume flag 或 workspace 目录内容。对于 `pending` 和 `failed`，它在同一文件系统的 staging 目录中物化 source，提升结果，并在允许 runtime driver 启动前持久化 `ready`。provisioning 或状态持久化出错时不会启动 driver。

同一 sandbox ID 的调用在进程内由 `singleflight` 串行化。共享操作会在决策前重新加载权威 metadata，每个调用方在返回前再次加载，避免陈旧内存对象覆盖最终状态。等待者可以取消自身 context，不会取消已在执行的共享 attempt；不同 sandbox 可并行 provisioning。这不是多个 daemon 共享同一 `SANDBOX_ROOT` 时的分布式锁。

对于 `pending` 和正在重试的 `failed` sandbox，provider 只会收到 `<sandbox>/state/workspace-provisioning/attempt-<id>` 下的 staging workspace。Provisioner 只清理遗留的 `attempt-*` entry，在 staging 中完整物化 source，然后通过同文件系统 rename 提升到权威 `<sandbox>/workspace`。它要求持久化 workspace path 与该权威路径一致，并拒绝使用 symlink 的 sandbox、state 或 provisioning root。因此 provider 无法在 promotion 前写入正式 workspace。Sandbox metadata 更新使用已 sync 的临时文件和同目录 rename，保存失败不会截断旧 metadata。

materialization、staging 或 promotion 失败会持久化 `failed`；后续启动会先转回 `pending`，再从新 attempt 重试。如果持久化失败状态或 `ready` 也失败，返回错误会保留该状态写入错误，runtime 仍不会启动。runtime driver 在 `ready` 之后启动失败则语义不同：provisioning 保持 `ready`，下次 resume 只重试 runtime 启动。

对于 `ready`，Provisioner 不解析 workspace config，不读取或检查 Workspace Source 或 sandbox workspace，也不调用 provider materializer。使用相同 data root 和 sandbox root 的 stop/resume 与 daemon restart 因而会保留修改、删除、生成文件、symlink 及其他 workspace 状态。后续 source 或 config 变更、source 删除、Git remote 变化都不会刷新已有 sandbox。sandbox 内的修改不会复制回 file/local source，也不会自动 commit 或 push 到 Git。

带 workspace 但 metadata 缺少 provisioning 状态的 legacy sandbox，会在 runtime 启动前直接迁移为 `ready`。迁移不解析 config、不检查 workspace 内容、不根据 runtime status 推断，也不尝试重建。新 sandbox 彼此独立，并使用其当前 snapshot 或 config 选定的最新 source 完成自身首次 provisioning；source 变更因此只影响新建 sandbox，不影响已有 `ready` sandbox。删除 sandbox 会删除整个 sandbox 目录，包括可写 workspace、provisioning metadata 和所有 provisioning staging attempt。

当前 sandbox 启动流程由 v1-compatible `CreateSession` 及其他创建路径共同使用：

1. 解析请求里的 env、tags、workspace id、driver、guest image。
2. 合并 global env 和请求 env。
3. 创建 sandbox 目录，初始化 metadata、VM state 和 proxy state；workspace id 或 snapshot 会把 provisioning 初始化为 `pending`。
4. 运行共享 Provisioner；它会建立并持久化 `ready`、迁移 legacy metadata，或在 runtime 启动前返回错误。
5. 准备受管 capability、runtime、state 和 home 资源。
6. 通过 driver 启动 runtime。
7. 标记 sandbox 为 `RUNNING`。
8. 记录 sandbox created 状态。Loader lifecycle event 使用 `loader.sandbox.*`；历史 `agent-compose.session.*` topic prefix 仅在 v1 兼容 event bus 仍发送处保留。

`ResumeSession` 是 v1-compatible resume 方法；它加载同一个 sandbox，运行共享 Provisioner，再启动其 runtime，`ready` workspace 保持不变。`StopSession` 会停止 runtime 并将 sandbox 标记为 `STOPPED`，但不改变 workspace 或 provisioning 状态。

服务启动时会校准 persisted sandbox runtime state；`GetSession`、`ListSessions` 和 `StopSession` 也会触发校准逻辑。

guest 路径默认值：

| host 路径 | guest 路径 | 用途 |
|---|---|---|
| `<sandbox>/workspace` | `/workspace` | Jupyter root、cell/agent/command cwd |
| `<sandbox>/state` | `/data/state` | cell artifact、agent prompt、provider state |
| `<sandbox>/runtime` | `/data/runtime` | 运行期共享资源 |
| `<sandbox>/logs` | `/data/logs` | Jupyter 日志 |
| `<sandbox>/home` 或其子路径 | `/root` 或其子路径 | Codex、Claude、Gemini、git 等工具配置和状态 |

更细的 mount manifest 设计见 [runtime_mount_manifest_design.md](runtime_mount_manifest_design.md) 和 [runtime_mount_manifest_driver_specific_design.md](runtime_mount_manifest_driver_specific_design.md)。

## Loader 运行时

loader 当前 runtime 是 `scheduler`，支持：

- `interval`
- `timeout`
- `event`
- `cron`

project compose 的 `scheduler.script` 使用同一套 runtime。脚本在 validate/apply 时会被求值以收集 triggers；`scheduler.agent`、`scheduler.llm`、`scheduler.exec`、`scheduler.shell`、`scheduler.event.publish` 和 v1-compatible session RPC bridge 等有副作用或 host 依赖的 API 应放在 `main()` 或 trigger callback 中。

`scheduler` 是 loader QJS 环境的唯一产品级全局对象。它的职责是注册 trigger、维护轻量状态、发布事件，并把需要 sandbox 能力的工作交给 runtime sandbox 执行。QJS 层不负责承载复杂 Node.js 工作流、npm 依赖或长时间业务逻辑。

需要完整 Node.js 能力时，当前实现通过 `scheduler.exec` / `scheduler.shell` 在 loader sandbox 内调用 workspace 脚本，或通过 `scheduler.agent` / `scheduler.llm` 调用现有 agent 和 LLM 能力。独立的 `scheduler.run(file, input, options)`、runtime workflow context、workflow bridge token 和 `agent-compose-runtime workflow` 子命令尚未成为当前 API 契约；设计文档不把这些草案接口视为已实现能力。

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

UI/数据库中的 global env 会覆盖进程环境变量。`chat_completions` 仅用于单次文本生成，不会创建具备 workspace 能力的 agent sandbox，也不提供文件、命令或 MCP 工具访问。使用 `outputSchema` 时，`chat_completions` 通过 prompt 引导并设置 `json_object`，不等价于 Responses API strict JSON Schema。

Guest agent provider（`codex`、`claude`、`gemini`、`opencode`）仍是 guest 容器内的 CLI runner，使用各自的 API key 和 provider-native session 状态。

loader 的主 sandbox 生命周期 API 是：

- `scheduler.sandbox.createSandbox(request)`
- `scheduler.sandbox.resumeSandbox(request)`
- `scheduler.sandbox.stopSandbox(request)`
- `scheduler.sandbox.getSandbox(request)`
- `scheduler.sandbox.listSandboxes()`
- `scheduler.sandbox.getSandboxProxy(request)`

这些方法对外使用 sandbox-shaped request/response JSON，当前内部仍桥接到 v1 生命周期 service。loader 还保留以下 deprecated v1 `SessionService` alias：

- `scheduler.session.createSession(request)`
- `scheduler.session.resumeSession(request)`
- `scheduler.session.stopSession(request)`
- `scheduler.session.getSession(request)`
- `scheduler.session.listSessions()`
- `scheduler.session.getSessionProxy(request)`

方法名使用 lower camel case，也保留 PascalCase alias。新脚本应使用 `scheduler.sandbox.*`；通过 `scheduler.session.*` 调用会产生 deprecated warning。

## Compose 部署拓扑

发布部署由基础文件和显式 KVM overlay 组成：

- `docker-compose.yml` 是默认部署入口，保留发布镜像、Docker socket、data、只读 `.env`、端口和 frontend profile，但不设置 `privileged`，也不映射 `/dev/kvm`。它可以在 Linux 无 KVM 环境和 macOS Docker Desktop 中独立启动 Docker driver。
- `docker-compose.kvm.yml` 是最小增量 overlay，只为 `agent-compose` 增加 `privileged: true` 和 `/dev/kvm:/dev/kvm`。它必须与基础文件组合使用，不承担 image、environment、volume、port 或 frontend 配置。
- 普通项目命令通过部署目录 `.env` 中持久化的 `COMPOSE_FILE` 选择同一文件集合。installer 新安装时检测 KVM，有 KVM 时选择两文件，无 KVM 时选择基础文件；已有显式选择和首次确定的 legacy 选择不会随临时 KVM 状态反复切换。
- `docker-compose.override.yml` 保留给本地 build override，不作为 KVM overlay。完整 Linux image 即使编译了三个 driver，基础 Compose 的默认 `RUNTIME_DRIVER` 仍是 `docker`。

## 前端服务

daemon 不托管 Web/UI 静态资源，也不再支持 `HTTP_ROOT` / `UI_ROOT` 静态根目录配置。daemon 主进程只注册 API、Connect、webhook/workspace 和 Jupyter proxy 路由。

当前 Docker 部署提供独立前端服务：

- `agent-compose-ui` 仓库构建并发布前端镜像。
- compose 中有 `agent-compose` daemon 和 `agent-compose-frontend` 两个服务。
- 默认只启动 daemon；启用 `with-ui` profile 时启动 `agent-compose-frontend`。
- 前端镜像在 nginx 后运行 agent-compose-ui server。nginx 负责静态资源、访问日志、body size、超时和 WebSocket upgrade 等通用入口能力；UI server 负责浏览器认证、OAuth、cookie session 和鉴权后的反向代理。
- `/api/auth/*` 和 `/oauth/*` 属于 UI server，不再由 daemon 注册。
- UI server 将 daemon v1/v2 Connect API、health API、workspace/event/webhook HTTP API、`/jupyter/*` 或配置的 `JUPYTER_PROXY_BASE`、以及兼容的 `/agent-compose/session/*` 代理到 daemon。
- 浏览器入口应暴露 agent-compose-ui server 的端口。daemon 的 `HTTP_LISTEN` 在 Compose 中只作为容器网络和本机 loopback 可达的 internal API；直接对外使用时必须由可信网络、反向代理、VPN、mTLS 或上层机器认证保护。

本机 CLI 不经过 UI server。它默认通过 Unix socket 访问 daemon，并使用 socket peer credential 信任模型；显式设置 `--host` 或 `AGENT_COMPOSE_HOST` 时才访问 TCP/HTTP daemon API。

Webhook 和浏览器登录不是同一个安全边界。`/api/webhooks/*` 的业务处理、source token 校验和 provider signature 校验仍由 daemon handler 完成；UI server 对 webhook 路径只做入口转发和通用 HTTP 代理，不把 webhook token 转换为浏览器 cookie session。

共享 playground 的具体构建、部署和验证流程见 [playground_setup.md](playground_setup.md)。

## 关键约束

- daemon 是状态和 reconcile 权威；CLI 不直接写 SQLite 或 sandbox 文件。
- `agents.<name>` 是 agent definition，不是常驻 runtime。
- `up` 管理定义和 scheduler，不等同于运行 agent。
- `run` 是一次性执行，默认结束后停止 runtime。
- `down` 禁用受管 scheduler/loader 并停止 project running sandboxes，默认不删除历史。
- v1 API 必须保持兼容，v2 API 承载 project/run/exec/image 主路径。
- Web/UI 作为独立服务部署，不进入 daemon Docker 镜像；浏览器 auth/OAuth 属于 agent-compose-ui server。
- daemon TCP API 不应作为公网浏览器入口；浏览器 cookie/OAuth 配置不保护 daemon `HTTP_LISTEN`。
