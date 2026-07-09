# agent-compose CLI 当前设计

本文面向研发和评审，记录 agent-compose CLI 的当前命令体系、服务边界和运行时能力。最终用户用法见 [命令行使用手册](../command-line-manual.md)。

## 设计目标

CLI 是 agent-compose daemon 的操作入口。它负责读取本地 project 配置、校验命令参数、连接本地或远程 daemon、展示文本或 JSON 输出；daemon 负责 project 状态、scheduler、run、sandbox、image store、Jupyter proxy 和持久化。

当前命令体系以 `project`、`agent`、`run`、`sandbox` 和 `image` 为核心概念：

- `project` 来自 `agent-compose.yml` 或 `agent-compose.yaml`，由 v2 `ProjectService` 管理。
- `agent` 是 project 中定义的可运行单元，可带 provider、model、image、driver、env、workspace、scheduler、capset 和 Jupyter 默认配置。
- `run` 是一次可审计的执行记录，包含 prompt/command、状态、输出、日志路径、artifact 路径、cleanup error 和 warning。
- `sandbox` 是对外统一的运行态隔离环境概念，内部使用 sandbox store、runtime state 和 proxy state；`pkg/storage/sessionstore` 仅是兼容包名。
- `image` 由 daemon image backend/store 管理，`pull`/`rmi` 不挂到 runtime driver。

## CLI 入口和全局行为

当前 CLI 入口集中在 `cmd/agent-compose/main.go` 的 Cobra 命令树。全局参数包括：

- `--host`：连接 HTTP(S) daemon；未设置时使用本地 Unix socket。
- `-f, --file`：指定 project 配置文件路径。
- `--project-name`：覆盖配置文件中的 project name。
- `--json`：输出机器可读 JSON。

配置文件定位规则：

- 未指定 `-f` 时，CLI 在当前目录查找 `agent-compose.yml` 或 `agent-compose.yaml`。
- 两个文件同时存在时返回 usage error，要求显式指定。
- 指定 `-f` 后，project root 为配置文件所在目录。
- `--project-name` 只影响提交给 daemon 的 normalized project name。

远程认证规则：

- 使用 `--host` 或 `AGENT_COMPOSE_HOST` 连接 HTTP(S) daemon 时，CLI 从本机环境变量 `AUTH_USERNAME` 和 `AUTH_PASSWORD` 读取 Basic Auth 凭据。
- Unix socket 本地连接不注入该认证。
- daemon 不再消费浏览器登录用的 `AUTH_*` / `OAUTH_*` 配置；远程 TCP API 应通过网络边界、反向代理或上层服务保护。
- warning 和 deprecated 提示写入 stderr，不污染 `--json` stdout。

## 当前命令体系

| 命令 | 当前语义 |
| --- | --- |
| `daemon` | 启动 daemon。 |
| `version` | 输出 CLI build version。 |
| `status` | 请求 daemon version/status。 |
| `config` | 解析、校验并输出 normalized config；`--quiet` 只校验。 |
| `ls` | 列出 daemon 管理的 projects，支持 `--limit`、`--offset`、`--verbose`、`--json`。 |
| `up` | 读取 project config 并调用 v2 `ProjectService.ApplyProject`；apply 后返回，不 attach 日志，不提供 `-d/--detach`。 |
| `down` | 调用 v2 `ProjectService.RemoveProject`，停止 scheduler 和相关运行态。 |
| `run` | 调用 v2 `RunService` 创建 run，支持 trigger、prompt、command、detach、interactive、Jupyter 和 cleanup policy。 |
| `logs` | 读取 run 日志，支持 agent/run/sandbox filter、tail、timestamp 和 server streaming follow。 |
| `ps` | 以 sandbox 视图列出当前 project 的运行态，默认只显示 running sandbox。 |
| `sandbox` | 统一管理当前 project 的 sandbox，包含 `ls`、`stop`、`resume`、`rm`、`prune`。 |
| `stats` | 通过 v2 `SandboxService.GetSandboxStats` 获取指定 sandbox 或当前 project 所有 running sandbox 的资源统计。 |
| `stop` | 基于 v1 `SessionService.StopSession` 停止 sandbox。 |
| `resume` | 基于 v1 `SessionService.ResumeSession` 恢复 sandbox。 |
| `rm` | 调用 v2 `SandboxService.RemoveSandbox` 删除 sandbox；running sandbox 需要 `--force`。 |
| `exec` | 调用 v2 `ExecService.ExecStream` 在已有 sandbox 中执行一次 command transcript。 |
| `inspect` | 查看 project、agent、run、sandbox、image 或 cache 详情。 |
| `images` | 列出 daemon image store 中的镜像。 |
| `pull` | 拉取当前 project 的 agent images，或拉取指定 OCI image reference。 |
| `rmi` | 删除镜像 metadata/store entry；不删除 materialized/runtime/sandbox cache。 |
| `cache` | 查看、dry-run 和显式清理 daemon runtime cache inventory，包含 `ls`、`inspect`、`prune`、`rm`。 |
| `image` | 旧 image 命令树，保留兼容并输出 deprecated warning。 |

`build` 和 `push` 仍未作为稳定 CLI 发布。它们涉及 image build、制品命名、远端仓库、鉴权和发布策略，需要单独设计。

## Connect API 边界

CLI 面向 v2 API 的当前服务边界：

- `ProjectService`：`ValidateProject`、`ApplyProject`、`GetProject`、`ListProjects`、`RemoveProject`、`WatchProject`。
- `RunService`：`RunAgent`、`StartRun`、`RunAgentStream`、`GetRun`、`ListRuns`、`FollowRunLogs`、`StopRun`。
- `ExecService`：`Exec`、`ExecStream`。
- `ImageService`：`ListImages`、`PullImage`、`InspectImage`、`RemoveImage`。
- `CacheService`：`ListCaches`、`InspectCache`、`PruneCaches`、`RemoveCache`。
- `SandboxService`：`RemoveSandbox`、`GetSandboxStats`。

CLI 仍复用 v1 `SessionService` 的 `StopSession`、`ResumeSession`、`GetSession`、`ListSessions` 和 `GetSessionProxy` 等兼容能力。对外文案使用 sandbox，v1 wire 中的 `sessionId` 字段映射 sandbox ID。

## Project 和 Compose 映射

Compose schema 位于 `pkg/compose/spec.go`、`pkg/compose/normalize.go` 和相关 output helpers。当前 project spec 支持：

- `name`
- `variables`
- `workspace`
- `agents`
- `network.mode`

agent spec 支持：

- `provider`、`model`、`system_prompt`
- `image`
- `driver`
- `env`
- `workspace`
- `scheduler`
- `capset_ids`
- `jupyter`

`jupyter` 支持：

```yaml
agents:
  reviewer:
    provider: codex
    image: debian:bookworm-slim
    jupyter:
      enabled: true
      guest_port: 8888
```

`jupyter.enabled` 默认关闭；`guest_port` 未设置时使用 daemon `JUPYTER_GUEST_PORT`。agent YAML 不支持外部 host bind 或 host port，Jupyter 外部访问统一通过 agent-compose proxy。

当前 project spec 没有 service 模型。`ls` 文本表格中的 `SERVICES` 是兼容早期 CLI 设计的占位列，显示为 `-`；JSON 不输出虚假的 service count。

## Run 输入模式

`agent-compose run <agent>` 当前支持两类单次输入；scheduler trigger 由 `agent-compose scheduler trigger <agent> <trigger>` 执行：

- `--prompt "..."`：向 provider 发送一轮 prompt。
- `--command "..."`：在 agent sandbox 中通过 guest `agent-compose-runtime exec` 执行一次 shell command。

互斥规则：

- `--prompt`、`--command` 一次只能选择一种。
- 使用 `--prompt` 或 `--command` 后不能再传额外位置参数。
- prompt 输入必须使用 `--prompt`；`run <agent> <trigger-name>` 的第二个位置参数不再作为 prompt 处理。
- 复用 sandbox 使用 `--sandbox-id`。

### Trigger 解析

两个位置参数的 run 形式不再由 run 命令支持；run 命令不再在 CLI 侧解析 trigger name，也不会把 trigger id 写入 manual run request。`pkg/runs.Controller` 会在 `BeginRun` 前解析当前 project/agent 的 managed scheduler loader：

- trigger 必须属于当前 project 和 agent。
- disabled scheduler 或 disabled trigger 可由 operator 手动运行，但 run response/summary/detail 会带 warning。
- trigger prompt 会进入实际 `ExecuteAgentRequest.Message`，并写入 `project_run.prompt`。
- 解析出的 scheduler 信息会保留在 run summary/detail。

### Cleanup policy

v2 `RunSessionCleanupPolicy` 当前包括：

- `STOP_ON_COMPLETION`：默认策略，run terminal 后停止 sandbox runtime。
- `KEEP_RUNNING`：`--keep-running`，run 完成后保留 runtime。
- `REMOVE_ON_COMPLETION`：`--rm`，run terminal 后删除本次 run 创建的 sandbox。

`--rm` 由 service 端 `pkg/runs.Controller` 负责，不依赖 CLI 进程存活。它覆盖 succeeded、failed、canceled 等 terminal 状态。显式传入已有 `--sandbox-id` 时，不删除该 sandbox。cleanup 失败写入 `project_run.cleanup_error`；run 原始错误优先于 cleanup 错误。

### Detach 和 StopRun

`run -d/--detach` 调用 v2 `RunService.StartRun`，由 daemon 内 `RunSupervisor` 接管执行并立即向 CLI 返回 run id、初始状态和 `logs --follow` 查看命令。

`StopRun` 会先尝试通过 daemon 内 active run registry/cancel map 请求当前活动 run 取消，再更新 run 状态。daemon 重启后遗留的 pending/running run 会在启动 reconcile 中标记为 failed，并带 `daemon interrupted` 错误；当前不承诺 durable background run queue。

### Interactive REPL

`run -i --prompt` 和 `run -i --command` 是 run-level REPL：

- stdin 每条非空输入创建一条独立 `ProjectRun`。
- 同一 REPL 复用同一个 sandbox。
- prompt 模式复用 provider conversation；当前支持 Codex、Claude/cc 和 OpenCode，Gemini 返回 unsupported。
- command 模式复用同一 workspace/home/state/runtime sandbox。
- 输入 `/exit` 或 Ctrl+D 退出。
- 不提供 TTY、PTY、terminal resize、WebSocket TTY、Connect bidi stdin 或运行中 stdin 透传。

## Command transcript

`run --command` 和 `exec <sandbox>` 都收敛到 guest `agent-compose-runtime exec`：

- host 侧写入 `command-request.json`。
- guest runtime 执行 command，并写入 `stdout.txt`、`stderr.txt`、`output.txt`、`command-result.json`。
- v2 stream response 使用 `chunk`、`stream` 和 `TranscriptEvent.stream` 返回 transcript；host marker filter 会在进入 CLI text output、run log 和 cell output 前剥离协议 payload。
- `run --command` 创建 `ProjectRun`，可被 `logs`、`inspect run` 和 artifact 审计。
- `exec <sandbox>` 不创建 `ProjectRun`；需要审计和日志时应使用 `run --command`。

`ExecStream` 保持一次性 command server streaming。当前没有 `ExecInteractive`，也不把 `ExecStream` 改成 bidirectional streaming。

## Logs 和 Follow

run 日志的权威文件由 `project_run.logs_path` 指向：

- command run 写入 `state/runs/<run_id>/output.txt`。
- agent run 使用对应 cell artifact 或 run log artifact。
- `project_run.output` 是汇总视图，不作为 follow 的唯一来源。

`logs --follow` 调用 v2 `RunService.FollowRunLogs`，服务端按 byte offset/tail 读取日志文件并 stream `RunLogChunk`。`tail_lines` 和 `start_offset` 在服务端处理，CLI 不直接读取 daemon 文件。terminal 状态后服务端 flush 剩余内容并发送 `is_final=true`。

当前没有新增 run output chunk DB 表。后续如需要结构化 provider event、逐 chunk timestamp 或跨节点日志索引，可考虑 sidecar JSONL 或单独日志索引设计。

## Stats

`agent-compose stats [sandbox]` 调用 v2 `SandboxService.GetSandboxStats`。指定 sandbox 时直接查询该 sandbox；未指定 sandbox 时先按当前 compose project 枚举 running sandbox，再逐个查询 stats。service 解析 sandbox、runtime state 和对应 driver optional stats 能力。

JSON 字段集合保持稳定：

- `cpu_percent`
- `memory_usage_bytes`
- `memory_limit_bytes`
- `memory_percent`
- `network_rx_bytes`
- `network_tx_bytes`
- `block_read_bytes`
- `block_write_bytes`
- `uptime_seconds`
- `driver`
- `sampled_at`

每个 metric 用 `value`、`unit`、`status`、`message` 表达。driver 无法提供的字段在文本表格显示 `-`，JSON 中保留 key，并以 `value: null` 和 `status: unknown` 或 `status: unavailable` 表达。只有 driver 没有稳定 stats 能力入口时返回 typed unsupported，CLI 使用 unsupported 退出码。

## Jupyter proxy

Jupyter 默认关闭。启用来源包括：

- agent YAML `jupyter.enabled: true`
- `run --jupyter`
- `run --jupyter-expose`

`--jupyter-expose` 会同时启用 Jupyter，并把 sandbox proxy state 标记为 exposed。它只表达“通过 agent-compose proxy 暴露可访问入口”的意图，不要求 Docker、BoxLite 或 Microsandbox runtime driver 做外部 host port bind。

sandbox store 会把 Jupyter options 写入 `proxy/jupyter.json`。driver 启动 runtime 时读取 sandbox proxy state。`GetSessionProxy` 只返回 proxy 入口信息；真实 HTTP/WebSocket 转发由 `pkg/agentcompose/proxy` 的 proxy routes 处理。

## Image store 和 Pull

`pull` 面向 OCI image reference 和 daemon image backend/store：

- `agent-compose pull` 从当前 normalized project agents 收集 image refs 并去重拉取。
- `agent-compose pull <image>` 拉取指定 image。
- `agent-compose image pull <image>` 是 deprecated wrapper，行为与顶层 `pull <image>` 一致。

`ImageService.PullImage` 在 pull 前先调用 `InspectImage`：

- 本地命中时直接返回 succeeded，填充本地 image/resolved ref，并在 `warnings` 中记录 skipped/local already exists。
- typed not found 时继续 pull。
- inspect 其他错误返回带 image backend/store 上下文的错误。

Docker daemon 只是可选 image backend；OCI cache 是 daemonless backend。BoxLite 和 Microsandbox 可以在启动 runtime 时从 OCI image 派生自身 artifact，但 `pull` 不属于 runtime driver 能力。

`rmi` 同样只面向 image store/backend。materialized image cache、runtime-derived driver cache 和 sandbox-ephemeral state 的清理由 `cache ls|inspect|prune|rm` 显式完成，并复用 `CacheService` 的 dry-run、`--force`、保护状态和安全路径检查。

## Sandbox lifecycle

`ps`、`sandbox`、`stats`、`inspect sandbox`、`stop`、`resume`、`rm` 都以 sandbox 为对外术语。v1 兼容响应中的 `sessionId` 映射 sandbox ID，但 CLI 文案和用户入口统一使用 sandbox。

`sandbox` 命令组是当前推荐的 sandbox 管理命名空间：

- `sandbox ls` 等价于 `ps`，支持 `--all/-a`、`--status`、`--verbose` 和 `--json`。
- `sandbox stop <sandbox...>` 等价于顶层 `stop`。
- `sandbox resume <sandbox...>` 等价于顶层 `resume`。
- `sandbox rm <sandbox...>` 等价于顶层 `rm`，`--force` 仅用于显式删除 running sandbox。
- `sandbox prune` 对当前 compose project 下的 stopped/failed sandbox 做批量清理预览，默认 dry-run，只有带 `--force` 才实际删除。

顶层 `ps`、`stop`、`resume`、`rm` 继续保留兼容，不标记 deprecated。

### Sandbox prune

`sandbox prune` 是 CLI 编排能力，不新增后端批量 RPC。CLI 解析当前 project 后，通过 `ProjectService.GetProject`、v1 `SessionService.ListSessions`、v2 `RunService.ListRuns` 复用 `ps --all` 的聚合结果，再在本地应用过滤条件。实际删除仍逐个调用 v2 `SandboxService.RemoveSandbox`。

过滤规则：

- 默认匹配 `stopped,failed` 状态。
- `--status <status>[,<status>...]` 覆盖默认状态过滤。
- `--agent <agent>` 按 agent name 精确匹配，忽略大小写和首尾空白。
- `--driver <docker|boxlite|microsandbox>` 按 runtime driver 精确匹配，忽略大小写和首尾空白。
- `--older-than <duration>` 使用 sandbox `updated_at` 判断，缺失时回退到 `created_at`；duration 复用 cache prune 的解析规则，支持 `7d`、`168h` 等正时长。

安全规则：

- `sandbox prune` 只处理属于当前 compose project 的 sandbox，不提供 daemon 全局 prune。
- `running` 和 `pending` 状态不能进入 prune；用户显式传 `--status running` 或 `--status pending` 时返回 usage error，并提示使用 `sandbox rm --force <sandbox>` 处理运行中 sandbox。
- forced prune 删除时始终调用 `RemoveSandbox(force=false)`，避免批量强删运行中 sandbox。
- 时间缺失或时间格式无法解析的 sandbox 不进入 matched，会写入 `warnings`。
- `sandbox prune` 不直接删除 `SANDBOX_ROOT`、`DATA_ROOT` 或 runtime driver 私有目录，也不清理 runtime cache 文件；cache inventory 仍由 `cache prune` 或 `cache rm` 管理。

输出规则：

- `--json` 输出稳定字段：`dry_run`、`matched`、`removed`、`skipped`、`warnings`。
- 文本 dry-run 输出 matched/skipped/would remove 数量，并在有匹配项时提示使用 `--force` 实际删除。
- 文本 forced 输出 removed/matched/skipped 数量，并列出 removed、matched、skipped 和 warnings。
- 单个删除失败时记录到 `skipped` 并继续处理后续 sandbox；forced prune 存在 skipped 时先输出结果，再以非零退出码结束。

删除规则：

- 非 running sandbox 可直接删除。
- running sandbox 无 `--force` 时返回 failed precondition，并提示 `is running`。
- running sandbox 带 `--force` 时先 stop，再删除 sandbox metadata 和相关运行态资源。
- `rm` 不删除 project 配置。

`inspect session` 旧入口保留兼容并输出 deprecated warning。

## Deprecated 兼容项

| Deprecated 项 | 替代方式 |
| --- | --- |
| `agent-compose image ls` | `agent-compose images` |
| `agent-compose image pull <image>` | `agent-compose pull <image>` |
| `agent-compose image rm <image>` | `agent-compose rmi <image>` |
| `agent-compose image inspect <image>` | `agent-compose inspect image <image>` |
| `agent-compose inspect session <id>` | `agent-compose inspect sandbox <sandbox>` |

兼容入口必须满足：

- warning 写 stderr。
- `--json` stdout 不混入 warning。
- warning 给出替代命令或参数。

## 明确不提供的能力

当前 CLI runtime 能力不包含：

- `build`
- `push`
- `up` 前台 attach/detach
- TTY/PTY/WebSocket TTY
- terminal resize
- Connect bidirectional stdin
- 运行中 stdin 透传
- `ExecInteractive`
- durable background run queue
- run output chunk DB 表

这些能力如需发布，应单独设计 API、持久化、driver 能力和失败语义。
