# CLI runtime capabilities implementation plan

## 背景

本计划对应 `docs/spec/cli-runtime-capabilities-spec.md`，输出路径与 spec 同名。目标是补齐 CLI 手册中已经出现但当前缺失或不完整的 runtime 能力，并保持每个阶段完成后项目可构建、可测试。

范围内能力：

- OCI image `pull` 本地已有镜像 inspect-and-skip。
- `run --rm` terminal 后清理。
- `run --trigger` 解析 managed trigger。
- `logs --follow` 基于日志文件的增量 follow。
- `stats` 按 driver optional interface 渐进映射资源统计。
- Jupyter CLI/YAML 配置。
- `run -d/--detach` 后台运行。
- prompt/command 统一 JS runtime transcript 输出。
- `run -i` prompt/command REPL。
- `exec <sandbox>` 一次性 command transcript 执行。

范围外能力：

- 不实现 `build` 和 `push`。
- 不给 `up` 增加 attach/detach。
- 不新增 run output chunk DB 表。
- 不实现 durable background run queue。
- 不保证 provider turn 强中断。
- 不实现 TTY、PTY、terminal resize、WebSocket TTY endpoint 或运行中 stdin 透传。
- 不新增 `ExecInteractive`；不把 v2 `ExecStream` 改成 bidirectional streaming。
- 不要求所有 driver 首版支持完整 stats 字段；缺失字段按 unknown/null/`-` 表达。

## Harness 约束

- 遵循 `AGENTS.md`：主入口是 `cmd/agent-compose/main.go`，服务图在 `pkg/agentcompose/app.Setup(di)`，其中 `Register(di)` 注册依赖和路由，`StartBackground(di)` 启动后台 manager；v2 API 位于 `proto/agentcompose/v2/`，runtime driver 支持 `docker`、`boxlite`、`microsandbox`。
- 遵循 `TESTING.md`：新增行为优先用单测证明；跨 API、persistence、driver、runtime 边界时增加 integration/E2E 覆盖。`task test` 是测试质量门禁，覆盖率 baseline 为 unit/integration/e2e 均不低于 60%，combined 不低于 70%。
- 遵循 `Taskfile.yml`：主命令为 `task lint`、`task build`、`task test`。涉及 runtime 包时还要运行对应 npm 测试；涉及 proto-client 时运行 `cd proto-client && npm ci && npm run gen && npm run build`。
- CI 约束：`.github/workflows/ci.yml` 会运行 Go tests、coverage gate、runtime SDK、runtime JS unit test、proto-client build。
- 文档约束：用户可见 CLI 行为变更必须同步更新 `docs/zh-CN/command-line-manual.md`；废弃或移除的临时设计不得继续作为已实现行为展示。

## 阶段 0：协议生成和测试基线整理

### 目标

先建立后续阶段共用的协议生成、错误模型和测试入口，避免每个阶段重复处理生成代码和 unsupported 错误。

### 依赖

- 已确认 `docs/spec/cli-runtime-capabilities-spec.md`。
- 当前 proto 定义：`proto/agentcompose/v2/agentcompose.proto`。
- 当前 CLI/API/run 测试：`cmd/agent-compose/main_test.go`、`cmd/agent-compose/main_integration_test.go`、`pkg/agentcompose/api/*_test.go`、`pkg/agentcompose/app/*_test.go`、`pkg/runs/*_test.go`、`pkg/driver/*_test.go`。

### 实施工作

- 在 `proto/agentcompose/v2/agentcompose.proto` 中预留后续阶段需要的 message/service 增量时，保持字段号单调增加，不复用旧字段号。
- 确认 Go proto 生成命令并在实施记录中固定。推荐使用仓库 tool 依赖安装插件后运行：
  - `go install google.golang.org/protobuf/cmd/protoc-gen-go`
  - `go install connectrpc.com/connect/cmd/protoc-gen-connect-go`
  - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/health/v1/health.proto proto/agentcompose/v1/agentcompose.proto proto/agentcompose/v2/agentcompose.proto`
- TS client 生成使用既有 package script：`cd proto-client && npm ci && npm run gen && npm run build`。
- 在 API/app delegate 错误处理层统一 typed unsupported 映射，优先复用 `pkg/model/errors.go`、`pkg/images/errors.go`、`pkg/agentcompose/api/image.go` 的 Connect error 映射模式；如果现有错误不足，在 owner package 新增内部 sentinel error，并在 `pkg/agentcompose/api` 或 `pkg/agentcompose/app` delegate 中映射到 Connect `CodeUnimplemented` 或 `CodeFailedPrecondition`。
- 在 CLI 错误输出中把 unsupported 和 not found 与普通 execution failure 区分。

### 测试和验证

- 运行 `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/driver`。
- 涉及 proto-client 时运行 `cd proto-client && npm run gen && npm run build`。

### 验收标准

- 后续阶段可以复用统一错误类型。
- 生成后的 Go/TS proto client 与源码一致。
- 未引入任何用户可见行为变化时，现有测试全部通过。

### Harness 命令

- `task build`
- 合并前：`task lint`、`task test`

## 阶段 1：OCI image `pull` 本地已有镜像 skip

### 目标

让 `agent-compose pull <image>` 面向 OCI image reference 工作，在本地 OCI image store/backend 已有镜像时直接成功并输出 skipped 信息。该能力与当前 runtime driver 无关；Docker daemon 只是当前可用的 OCI image backend，BoxLite/MicroSandbox 的 runtime artifact materialization 不属于 `pull` 阶段。

### 依赖

- 当前 CLI：`cmd/agent-compose/main.go` 中 `runComposePullCommand`、`pullImage`。
- 当前 image API adapter：`pkg/agentcompose/api/image.go`。
- 当前 image backend：`pkg/images/*`、`pkg/imagecache/*`，以及 `pkg/agentcompose/adapters/images.go` 中的 Docker daemon、OCI cache、Auto backend selector。`pkg/driver/docker_image.go` 仍属于 runtime driver/image resolver 兼容路径，本阶段不得把 `pull` 语义回挂到 runtime driver。

### 实施工作

- 在 `pkg/agentcompose/api.ImageHandler.PullImage` 或 `pkg/images.Backend` 实现 pull 前 inspect：
  - Docker-backed OCI image store 使用 Docker image inspect。
  - OCI cache 或其他 daemon-less store 使用已有 inspect/cache status 能力。
- 当 inspect 命中本地镜像时，`PullImageResponse.status` 返回 succeeded，`warnings` 包含 skipped/local already exists 说明，`image` 填充本地镜像信息。
- CLI 文本输出显示镜像已存在并跳过拉取；JSON 输出保留 `warnings`。
- 保持 `agent-compose image pull <image>` deprecated wrapper 与顶层 `pull` 行为一致。
- 不在 Docker/BoxLite/MicroSandbox runtime driver interface 上新增 pull/image-store 语义；后续 `build`、`push` 也应挂到同一 OCI image domain。

### 测试和验证

- `pkg/agentcompose/api` 测试：覆盖 inspect 命中时不调用真实 pull backend，并确认 Connect 错误码仍由 image backend error 分类决定。
- `pkg/images`/`pkg/imagecache` backend 测试：覆盖 Docker-backed OCI image inspect 命中和未命中、OCI cache inspect 命中和未命中。
- `cmd/agent-compose/main_test.go`：覆盖文本和 JSON 输出中 skipped/warnings。
- API/CLI 测试覆盖选择不同 runtime driver 时 `pull` 行为不变。

### 验收标准

- 本地已有镜像不会再次 pull。
- inspect 失败和 pull 失败仍返回非 0，并包含 image reference 和 image backend/store 上下文，不包含 runtime driver 语义。
- BoxLite/MicroSandbox 只在启动 runtime 时消费 OCI image 并 materialize 自身 artifact，materialization 失败不表现为 `pull` 的 driver-specific 错误。
- deprecated `image pull` 不出现行为分叉。

### Harness 命令

- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/adapters ./pkg/images ./pkg/imagecache ./pkg/driver`
- `task build`

## 阶段 2：`run --rm` terminal 清理语义

### 目标

修正当前 `run --rm` 只在成功 run 后清理的问题，保证成功、失败、取消进入 terminal 后都尝试清理本次 run 创建的 sandbox。

### 依赖

- 当前 CLI：`runComposeRunCommand` 中 `normalizedOptions.Remove` 逻辑。
- 当前 run API/app adapter：`pkg/agentcompose/api/run_handler.go`、`pkg/agentcompose/app/run_controller.go`。
- 当前 run 状态机和 cleanup：`pkg/runs/controller.go`、`pkg/runs/session.go`、`pkg/runs/coordinator.go`。
- 当前 sandbox 删除路径：`pkg/agentcompose/api/sandbox.go` 调用 `SessionDelegate.StopSession` 和 `pkg/storage/sessionstore.Store.RemoveSession`；`pkg/runs.cleanupProjectRunSession` 当前只停止 VM，不删除 session/sandbox。

### 实施工作

- 把 `--rm` 语义尽量下沉到 `pkg/runs.Controller`：
  - 如果 run 创建了新 session/sandbox，则 terminal 后由 controller cleanup。
  - 如果请求指定了已有 `--sandbox`/`--session-id`，不得删除该 sandbox。
- 为区分默认 stop-on-completion 和 `--rm` 删除语义，扩展 v2 协议：
  - 首选在 `RunSessionCleanupPolicy` 增加 `REMOVE_ON_COMPLETION`，CLI `--rm` 映射到该值，默认仍为 stop-on-completion，`--keep-running` 仍为 keep-running。
  - 如不扩 enum，则新增显式 bool 字段表达 remove intent；不得继续让 CLI 在成功后单独调用 `RemoveSandbox` 作为权威 owner。
- `pkg/runs.Controller.ensureProjectRunSession` 已返回 `SessionResult.Created`；terminal cleanup 必须使用该标记判断是否允许删除，指定已有 `SessionID` 时只能按 cleanup policy 停止或保留，不能删除。
- 对 remove-on-completion，`pkg/runs.Controller` 应复用 session removal 语义：先按需 stop running VM，再调用 `sessionstore.Store.RemoveSession`，并发布 session/dashboard 事件；避免绕过 `SandboxHandler` 导致行为分叉。
- 如短期必须保留 CLI cleanup，也要把 cleanup 放到 completed detail 获取之后、失败退出之前执行，并记录 cleanup warning。
- 明确 exit code：
  - run 失败优先返回 run 失败。
  - run 成功但 cleanup 失败返回 cleanup 失败。
  - run 失败且 cleanup 失败时 stderr 附加 cleanup warning。
- 在 run detail 的 `cleanup_error` 中保留 cleanup 失败信息。

### 测试和验证

- owner package 单测：
  - 成功 run 后清理。
  - 失败 run 后清理。
  - context cancel/canceled run 后清理。
  - 指定已有 sandbox 时不删除 sandbox。
  - cleanup 失败写入 `cleanup_error`。
- CLI 单测：
  - run 失败且 cleanup 成功时返回 run 失败。
  - run 成功且 cleanup 失败时返回 cleanup 错误。

### 验收标准

- `--rm` 对所有 terminal run 都生效。
- 不会删除用户显式指定的已有 sandbox。
- cleanup 错误不覆盖原始 run 错误。

### Harness 命令

- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/sessionstore`
- `task build`

## 阶段 3：`run --trigger` 解析 managed trigger

### 目标

让手动 `run --trigger <trigger_id>` 真正使用 trigger 定义的 prompt/config，而不是只把 trigger id 写入 run metadata。

### 依赖

- 当前 loader store：`pkg/storage/configstore/loader_store.go`。
- 当前 project scheduler store：`pkg/storage/configstore/project_store.go` 中的 `project_scheduler` 读写方法。
- 当前 loader/project domain model：`pkg/model/loader_model.go`、`pkg/model/project_model.go`。
- 当前 loader/project owner package：`pkg/loaders/*`、`pkg/projects/scheduler.go`、`pkg/projects/reconcile.go`。
- 当前 run pipeline：`pkg/runs/controller.go`、`pkg/runs/preparation.go`、`pkg/runs/coordinator.go`。

### 实施工作

- 新增 `ResolveTriggerForManualRun(ctx, projectID, agentName, triggerID)`：
  - 校验 trigger 存在。
  - 校验 trigger 属于当前 project/agent。
  - 返回 scheduler id、managed loader id、trigger prompt、trigger env/context、warning 列表。
- resolver 应从 `project_scheduler` 反查当前 project/agent 的 managed loader，再调用或复用 `loaders.Controller.LoadLoaderForRun(ctx, loaderID, triggerID)`；不能只凭 trigger id 全局查找，也不能复用 scheduler 自动触发路径来隐式创建 run。
- `RunAgentRequest.trigger_id` 保留，但 `pkg/runs.Controller` 在 `BeginRun` 前解析 trigger，并将解析后的 prompt 写入 run start request。
- disabled trigger 手动运行允许执行，但产生 warning；warning 在 CLI 文本输出中写 stderr，JSON 输出包含 `warnings`。
- trigger 不存在或 agent 不匹配时返回 not found/invalid argument，不创建可执行 run。
- trigger prompt/template 解析失败时返回 invalid argument，不创建 run；如果 run 已创建后才失败，则必须标记 failed 并保留日志。

### 测试和验证

- owner package 单测：
  - 不存在 trigger 失败且不创建 run。
  - trigger agent/project 不匹配失败。
  - disabled trigger 手动运行成功且有 warning。
  - trigger prompt 进入 `ExecuteAgentRequest.Message`。
- integration 测试：
  - `up` 注册 managed loader 后，`run --trigger` 能执行对应 prompt。
- CLI 单测：
  - `--trigger` 与 `--prompt`/`--command` 互斥。
  - JSON 输出包含 warnings。

### 验收标准

- `project_run.prompt` 保存解析后的实际 prompt。
- run summary/detail 仍保留原始 `trigger_id`。
- `run --trigger` 不依赖 scheduler 自动触发路径。

### Harness 命令

- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/loaders ./pkg/projects ./pkg/storage/configstore`
- `task build`

## 阶段 4：`logs --follow` 基于日志文件增量输出

### 目标

替换当前轮询 `RunDetail.output` 的 follow 方式，使用 `project_run.logs_path` 指向的 `output.txt` 做 offset/tail 增量读取。

### 依赖

- 当前 run 输出：`project_run.output`、`project_run.logs_path`、`state/runs/<run_id>/output.txt`、`state/cells/<cell_id>/output.txt`，相关 store schema 在 `pkg/storage/configstore/project_schema.go`。
- 当前 CLI logs：`runComposeLogsCommand`、`followOrPrintProjectLogs`。
- 当前 run API 和状态机：`pkg/agentcompose/api/run_handler.go`、`pkg/agentcompose/app/run_controller.go`、`pkg/runs/controller.go`、`pkg/runs/coordinator.go`。

### 实施工作

- 在 `RunService` 新增 server streaming API：
  - `FollowRunLogs(FollowRunLogsRequest) returns (stream RunLogChunk)`。
  - request 字段：`project_id`、`run_id`、`tail_lines`、`start_offset`、`follow`。
  - response 字段：`data`、`offset`、`is_final`、`run_status`、`created_at`。
- 统一 command run 和 agent run 的实时 append：
  - command run 当前在 `pkg/runs.executeProjectRunCommand` 中直接 driver `ExecStream` 后再 `WriteCellArtifacts`；需要在 writer 收到 chunk 时同步 append 到 `logs_path`。
  - agent run 当前通过 `execution.AgentExecutionStream.OnChunk` 推送 chunk，`TransitionFromAgentCell` 把 `logs_path` 指到 cell artifact；如果当前 cell artifact 只在结束时落盘，需要在 executor stream sink 中同步 append 到 run log artifact。
- server follow 读取文件时按 byte offset 推送；terminal 状态后 flush 剩余内容并发送 `is_final=true`。
- `--tail N` 在 follow 前由 API/server 计算起始 offset；避免 CLI 直接读取 daemon 文件。
- CLI `logs --follow --run-id <id>` 调用新 API；project/agent/sandbox filter follow 可以先 list runs，再对匹配 running run 启动 follow。首版若多 run follow 实现复杂，按 run 顺序串行 follow，并在文档中说明。
- 不新增 DB chunk 表；如需要结构化 metadata，只预留同目录 `output.jsonl` sidecar，不在本阶段实现。

### 测试和验证

- API/run 单测：
  - 从 offset 读取增量。
  - `tail_lines` 起始位置正确。
  - running run 持续 append 后推送 chunk。
  - terminal 后发送 final 并退出。
  - 缺失日志文件时返回清晰错误或空 final，按 run 状态区分。
- CLI 单测：
  - `logs --json --follow` 仍被拒绝。
  - `logs --follow --run-id` 输出 chunk，不重复输出已打印内容。
- integration 测试：
  - command run 持续输出时 `logs --follow` 能实时看到中间行。

### 验收标准

- `logs --follow` 不再轮询 `RunDetail.output`。
- run 结束后 `project_run.output` 和 `logs_path` 内容一致或有明确汇总/日志差异。
- 不引入新的 DB output chunk 表。

### Harness 命令

- 修改 proto 后生成 Go/TS client。
- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/configstore ./pkg/storage/sessionstore`
- `cd proto-client && npm run gen && npm run build`
- `task build`

## 阶段 5：`stats` driver optional interface

### 目标

新增 `agent-compose stats <sandbox>`，提供 sandbox runtime resource stats。通过 driver optional interface 渐进映射 Docker、BoxLite、MicroSandbox 可获得指标；缺失字段显示 unknown/null 或文本表格 `-`，只有 driver 没有稳定指标入口时才返回 typed unsupported。

### 依赖

- 当前 sandbox/session API 和 state：`pkg/agentcompose/api/sandbox.go`、`pkg/storage/sessionstore/store.go`、`pkg/agentcompose/adapters/session_rpc_bridge.go`。
- 当前 driver interface：`pkg/driver/types.go`。
- 当前 Docker runtime：`pkg/driver/docker_runtime.go`。
- 当前 BoxLite cgo runtime 和 header：`pkg/driver/boxlite_cgo.go`、`build/boxlite/include/boxlite.h`。
- 当前 MicroSandbox runtime 和 SDK：`pkg/driver/microsandbox_runtime.go`、`github.com/superradcompany/microsandbox/sdk/go`。

### 实施工作

- v2 proto 新增 `StatsService` 或在 `SandboxService` 增加 `GetSandboxStats`。为避免新增过多 service，首选扩展 `SandboxService`。
- 定义 `SandboxStats` message：CPU percent、memory usage/limit/percent、network rx/tx、block read/write、uptime、driver、sampled_at。
- `SandboxStats` 的各 metric 字段必须有明确可空性。实现上使用 proto optional/wrapper，或定义 `MetricValue { value, status }`，其中 `status` 至少能表达 ok、unknown、unavailable；JSON 输出保持字段 key 稳定，不因 driver 不同省略字段。
- `BoxRuntime` 或单独 optional interface 增加 `Stats(ctx, session, vmState)`。推荐使用 optional interface，避免强迫所有 driver 返回完全相同字段：
  - Docker runtime 基于 Docker stats API 实现单次 snapshot，目标返回 CPU、memory、network、block IO 和 uptime。
  - MicroSandbox runtime 映射 SDK `Metrics` 中已有的 CPU、memory、disk IO、network 和 uptime 字段。
  - BoxLite runtime 如当前 box metrics 回调稳定可用，映射 CPU、memory、network 和命令统计等可获得字段；block IO、memory limit、uptime 等无法可靠获得的字段返回 unknown/null。
  - driver 没有稳定指标入口时由 `SandboxService` 返回 typed unsupported。
- CLI 新增 `stats` cobra command：
  - `agent-compose stats <sandbox>` 表格输出。
  - `agent-compose --json stats <sandbox>` JSON 输出。
- `stats` 使用单次 snapshot，不做持续 watch。

### 测试和验证

- driver 单测：
  - Docker stats JSON/stream sample 解析为统一结构。
  - MicroSandbox metrics sample 映射为统一结构。
  - BoxLite metrics sample 或 fake callback 映射为统一结构；不可获得字段为 unknown/null。
- API/driver 单测：不存在 sandbox、stopped sandbox、unsupported driver、缺失字段输出。
- CLI 单测：表格输出字段、JSON 输出、unsupported 错误；JSON 输出在 Docker、MicroSandbox、BoxLite 样例下 key 集合稳定，缺失值为 null 或 status=unknown/unavailable。

### 验收标准

- Docker sandbox 可返回 CPU/memory/network/block IO/uptime。
- MicroSandbox sandbox 可返回 SDK 已有的 CPU/memory/disk/network/uptime 字段。
- BoxLite sandbox 返回可获得字段；缺失字段不显示为 execution failed。
- JSON 字段保持稳定；driver 差异只通过 value null/status unknown/unavailable 或表格 `-` 表达。
- 无指标入口的 driver 返回明确 unsupported，不显示为 execution failed。
- `stats` 不影响现有 `ps`、`inspect` 行为。

### Harness 命令

- 修改 proto 后生成 Go/TS client。
- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/storage/sessionstore ./pkg/driver`
- `cd proto-client && npm run gen && npm run build`
- `task build`

## 阶段 6：Jupyter CLI/YAML 配置

### 目标

让 Jupyter 默认关闭，支持 `run --jupyter` 单次启用，并在 agent YAML 中声明默认 Jupyter 配置，使 trigger-created sandbox 也能启动 Jupyter。Jupyter 访问统一走 agent-compose proxy；YAML 不支持外部 host expose，runtime driver 不直接做 host port expose。

### 依赖

- 当前 project schema/proto：`proto/agentcompose/v2/agentcompose.proto` 中的 `AgentSpec`、`ProjectAgent`。
- 当前 compose parsing/normalization：`pkg/compose/spec.go`、`pkg/compose/normalize.go`、`pkg/compose/output.go`。
- 当前 project reconciliation/persistence：`pkg/projects/records.go`、`pkg/projects/controller.go`、`pkg/storage/configstore/project_store.go`、`pkg/storage/configstore/agent_definition.go`。
- 当前 project API adapter：`pkg/agentcompose/api/project_handler.go`、`pkg/agentcompose/api/project.go`。
- 当前 session/proxy：`pkg/storage/sessionstore/store.go`、`pkg/model/model.go` `ProxyState`、`pkg/driver/types.go` `ProxyState`、`pkg/agentcompose/proxy/proxy.go`。
- 当前 Jupyter config：`pkg/config/config.go`。

### 实施工作

- proto `AgentSpec` 增加 `JupyterSpec`：
  - `bool enabled`
  - `uint32 guest_port`
- YAML parsing/normalization 增加 `agents.<name>.jupyter.enabled` 和 `guest_port`。
- project validation：
  - `guest_port` 为 0 时使用 daemon 默认。
  - 非 0 时必须在合法 TCP port 范围。
  - YAML 不接受 host bind/host port 字段；出现时返回 validation error。
- agent definition/project agent 持久化保留 `jupyter` spec JSON，无需新增 DB 列，除非 UI 列表必须直接展示。
- run/session 创建时解析 Jupyter config：
  - agent YAML default。
  - CLI `--jupyter` 覆盖 enabled=true。
  - `--jupyter-expose` 只来自 CLI，效果是创建/标记 agent-compose proxy access endpoint；proxy listen/bind 由 daemon 部署配置决定，不作为 agent YAML 或 runtime driver port mapping。
- session/proxy 状态只表达 agent-compose proxy route/access URL 和 guest endpoint。若 `ProxyState.HostPort` 继续存在，只能表示 daemon proxy 层访问结果，不得写入 Docker/BoxLite/MicroSandbox driver host port mapping。
- runtime 创建参数不得因 `--jupyter-expose` 请求 driver host port expose；所有 provider 使用同一 proxy 路径。
- trigger-created sandbox 使用 agent resolved Jupyter config。
- 更新 `docs/zh-CN/command-line-manual.md` 中 Jupyter 说明。

### 测试和验证

- project validation 单测：
  - default disabled。
  - enabled true + guest_port。
  - YAML host expose 字段被拒绝。
- project/session 测试：
  - trigger-created sandbox 使用 YAML Jupyter config。
  - YAML enabled 不产生外部 host bind/host port 配置。
  - CLI `--jupyter-expose` 创建 agent-compose proxy access endpoint。
  - Docker、BoxLite、MicroSandbox runtime 创建请求均不包含 Jupyter host port mapping。
- CLI 单测：
  - `--jupyter`、`--jupyter-expose` 参数进入 request/session options。

### 验收标准

- YAML 能表达 agent 默认 Jupyter proxy 配置。
- Jupyter 访问统一通过 agent-compose proxy；runtime driver 不直接暴露 host port。
- 外部可达性只由 CLI expose intent 和 daemon/proxy 部署配置共同决定。
- 默认行为仍不启动 Jupyter。

### Harness 命令

- 修改 proto 后生成 Go/TS client。
- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/compose ./pkg/projects ./pkg/storage/configstore ./pkg/storage/sessionstore ./pkg/config ./pkg/driver`
- `cd proto-client && npm run gen && npm run build`
- `task build`

## 阶段 7：`run -d/--detach` 后台运行

### 目标

让 `run -d` 创建 run 后立即返回，由 daemon 持有后台执行，并通过 `logs`/`StopRun` 观察和控制。

### 依赖

- 阶段 4 的日志 follow。
- 当前 run API/app adapter：`pkg/agentcompose/api/run_handler.go`、`pkg/agentcompose/app/run_controller.go`。
- 当前 run 状态机：`pkg/runs/*`。
- 当前 `StopRun` 只在 `pkg/agentcompose/api/run_handler.go` 中把 DB 中非 terminal run 标记为 canceled；还没有 daemon supervisor registry 或 execution context cancellation 关联。

### 实施工作

- proto `RunAgentRequest` 增加 `bool detach`，或新增 `StartRun` RPC。为减少 API 分叉，首选新增 `StartRun(StartRunRequest) returns (StartRunResponse)`，内部复用 `RunAgentRequest` 字段。
- `pkg/runs`/app delegate 实现 daemon supervisor：
  - 创建 run 后返回 `run_id`、`session_id`、初始 status。
  - goroutine 执行现有 `runProjectAgent` pipeline。
  - supervisor registry 保存 run cancel handle，`StopRun` 通过统一 run context cancellation 请求停止。
  - prompt 和 command 执行都沿用 common execution context，取消信号传递到 `RunAgentStream`/`ExecStream(ctx)` 及其下游 JS runtime transcript 执行。
  - run 状态机统一进入 canceling/canceled 或 failed terminal 状态；不得因 driver/provider/子进程无法即时终止而长期保持 running。
  - Docker/BoxLite/MicroSandbox 如需要底层强制终止，只能封装为 best-effort cleanup，不暴露成不同用户语义。
- daemon restart reconcile：
  - 对重启时仍为 pending/running 且没有 active supervisor 的 run 标记 failed。
  - error 写明 `daemon interrupted`。
- supervisor 可放在 `pkg/runs`，由 `pkg/agentcompose/app.NewRunController` 注入并由 `RunHandler.StopRun` 或 app delegate 调用；不要在 CLI 侧 fork 后台进程，也不要让 `api.RunHandler` 独自持有不可测试的全局状态。
- CLI：
  - `run -d` 与 `-i` 互斥。
  - 成功后文本输出 run id、sandbox id、查看日志命令。
  - JSON 输出包含 run/session/status/logs command 或 logs URL。
- 不实现 durable queue 和 restart 后自动恢复执行。

### 测试和验证

- service 单测：
  - `StartRun` 立即返回。
  - 后台 run 最终 succeeded/failed。
  - `StopRun` 能取消 active run，并稳定产生 terminal 状态。
  - fake runtime 验证 context cancellation 会传递到 prompt/command 执行路径。
  - 当 fake runtime 忽略 cancellation 时，run supervisor 仍记录 cancel requested 并按超时/最终错误落 terminal 状态。
  - restart reconcile 标记 orphan pending/running run failed。
- CLI 单测：
  - `-d` 与 `-i` 互斥。
  - 文本/JSON 输出字段。
- integration 测试：
  - `run -d --command "..."` 后 `logs --follow` 能跟随输出。

### 验收标准

- CLI 进程退出不影响 daemon 中的后台 run。
- run 可通过 `logs` 查看，可通过 `stop`/`StopRun` 请求停止。
- daemon restart 不留下永久 running run。
- `StopRun` 在 Docker/BoxLite/MicroSandbox 上保持同一 API 和状态语义；差异只允许存在于底层 best-effort 终止能力。

### Harness 命令

- 修改 proto 后生成 Go/TS client。
- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
- `cd proto-client && npm run gen && npm run build`
- `task build`

## 阶段 8：command transcript 基础与 `exec <sandbox>`

### 目标

统一 command 执行的呈现和归档路径：`run --command` 和 `exec <sandbox>` 都通过 guest `agent-compose-runtime exec` 执行一次 command，并流式返回 JS runtime transcript。`ExecStream` 保持 server streaming，不新增 `ExecInteractive`、WebSocket、TTY、PTY、resize 或运行中 stdin。

### 依赖

- 阶段 0 的 proto 生成和错误模型。
- 阶段 4 的 run 日志 append 语义。
- 当前 exec API adapter：`pkg/agentcompose/api/exec.go`。
- 当前 run command path：`pkg/runs/controller.go` 中的 `executeProjectRunCommand`。
- 当前 JS runtime command：`runtime/javascript/src/command.ts`、`runtime/javascript/src/cli.ts`。
- 当前 host runtime command helper：`pkg/execution/command_runtime.go` 中的 `RuntimeCommandRequestPayload`、`BuildLoaderCommandExecSpec`、`MirrorRuntimeCommandArtifacts`。
- 当前已使用 JS runtime command path 的参考实现：`pkg/agentcompose/adapters/loader_command_executor.go`。

### 实施工作

- v2 proto 输出模型整理：
  - 定义统一 `TranscriptEvent` message，字段包含 `kind`、`text`、`is_stderr`、`name`、`payload_json`、`created_at`。
  - `RunAgentStreamResponse` 和 `ExecStreamResponse` 使用 `TranscriptEvent transcript` 承载过程输出。
  - v2 不需要保持兼容，可移除或弃用原 `chunk/is_stderr` 直出字段，避免 CLI 同时维护两套输出逻辑。
- JS runtime command transcript：
  - `runtime/javascript/src/command.ts` 当前已负责写 `stdout.txt`、`stderr.txt`、`output.txt`、`command-result.json` 并把子进程 stdout/stderr 透出；本阶段在启动 command 前补充 `$ <command>` transcript，并在非零 exit code 时输出摘要。
  - stdout/stderr 继续原样写入 `stdout.txt`、`stderr.txt`、`output.txt` 和进程 stdout/stderr。
  - 非零 exit code 时输出简短 exit 摘要；结构化结果仍写入 `command-result.json`。
- run controller command 路径：
  - `executeProjectRunCommand` 不再直接 `bash -lc` 执行用户命令并由 host 拼 artifacts。
  - 为每个 command run 写入 runtime command request JSON，调用 guest `agent-compose-runtime exec`。
  - 将 runtime stream chunk 转换为 `TranscriptEvent`，同时 append 到 `project_run.logs_path`。
  - 保持 `ProjectRun` 的 `output`、`result_json`、`artifacts_dir`、`logs_path` 和 exit code 语义。
- exec API 路径：
  - `ExecStream` 继续一次性提交 `ExecRequest`，服务端解析 target、校验 session running。
  - 为每个 exec 创建 `<session>/state/exec/<exec_id>/` artifact dir 和 request file。
  - 调用 guest `agent-compose-runtime exec`，返回 transcript stream 和最终 `ExecResult`。
  - `exec` 不创建 `ProjectRun`；如需要 run 审计，用户使用 `run --command`。
- CLI 输出：
  - `run --command` 和 `exec` 共用 transcript 打印 helper。
  - 文本模式按 transcript 原样输出；JSON 模式输出最终 result/detail，不打印流式 transcript。
  - 不实现 `exec -i`；如后续提供该 CLI sugar，也必须实现为“每轮一次 `ExecStream`/`RunAgentStream`”，不是运行中 stdin。

### 测试和验证

- runtime JS 测试：
  - `agent-compose-runtime exec` 写入 stdout/stderr/output/result artifacts。
  - transcript 包含 command start、stdout/stderr 原文和非零 exit 摘要。
- API/run 单测：
  - `run --command` 通过 runtime command request 执行，并写入 run logs/artifacts。
  - `ExecStream` 创建 exec artifact dir，返回 transcript 和最终 result。
  - target 解析错误、stopped sandbox failed precondition。
- CLI 单测：
  - `run --command` 和 `exec` 共用 transcript 输出 helper。
  - JSON 模式不污染 stdout。

### 验收标准

- `run --command` 和 `exec <sandbox>` 的输出呈现一致，均由 JS runtime transcript 驱动。
- `ExecStream` 仍是 server streaming；没有新增 `ExecInteractive`、WebSocket 或 bidi stream。
- command artifacts 由 JS runtime 生成，host 不重复覆盖 `command-result.json`。
- 普通 prompt run、command run 和 exec 行为不退化。

### Harness 命令

- 修改 proto 后生成 Go/TS client。
- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/execution`
- `cd proto-client && npm run gen && npm run build`
- `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`
- `task build`

## 阶段 9：`run -i` prompt/command REPL

### 目标

支持 run-level interactive REPL：prompt 和 command 都按“一条用户输入对应一条 `ProjectRun`”执行，同一个 REPL 复用同一个 session/sandbox。prompt 复用 provider conversation；command 复用 workspace、home、state 和 runtime session。不提供 process-level TTY/stdin/resize。

### 依赖

- 阶段 2 的 cleanup 语义。
- 阶段 4 的日志输出。
- 阶段 8 的 command transcript 基础。
- 当前 runtime prompt：`runtime/javascript/src/prompt.ts`。
- 当前 provider runner：`runtime/javascript/src/runners/codex.ts`、`claude.ts`、`opencode.ts`、`gemini.ts`。
- 当前 run API/agent/command path：`RunAgentStream`、`pkg/runs.RunAgentRequest.Prompt`、`pkg/runs.RunAgentRequest.Command`、`execution.ExecuteAgentRequest.Message`。

### 实施工作

- CLI：
  - 新增 `-i/--interactive` bool。
  - `run -i --prompt` 进入 prompt REPL；`--prompt <text>` 可作为第一轮 prompt。为支持无值 `--prompt`，按需设置 pflag `NoOptDefVal`。
  - `run -i --command` 进入 command REPL；`--command <cmd>` 可作为第一轮 command。`-i` 场景下允许 `--command` 无值作为模式标记。
  - 未显式 `--prompt` 或 `--command` 时，如果 `-i` 已指定，返回 usage error，要求用户选择 prompt 或 command 模式。
  - `run -i` 与 `--trigger`、`-d` 互斥；prompt 模式与 command 模式互斥。
  - 空输入不创建 run。
  - `/exit` 和 Ctrl+D 退出。
  - Ctrl+C best-effort cancel 当前轮等待并请求 `StopRun`；不承诺 provider adapter 或 command 子进程强中断。
  - verbose/JSON 模式每轮展示 `run_id`、`session_id`、status、exit code 和日志查看命令。
- session 复用：
  - REPL 启动时创建或解析一个 session/sandbox。
  - 每轮调用 `RunAgentStream`，传入同一个 `session_id`，cleanup policy 为 keep running。
  - REPL 退出时默认保留 sandbox；用户可显式 `--rm` 时在退出后清理本 REPL 创建的 sandbox。
- prompt 模式：
  - 每轮把用户输入作为 `RunAgentRequest.prompt`。
  - 未指定 provider 时使用 agent definition provider。
  - Codex、Claude/cc、OpenCode 允许 interactive prompt。
  - Gemini 返回 unsupported，并列出支持 provider。
- command 模式：
  - 每轮把用户输入作为 `RunAgentRequest.command`。
  - 使用阶段 8 的 JS runtime command transcript 和 artifacts。
  - 不支持运行中 stdin；需要 stdin 的程序应改成一次性命令或脚本输入。
- provider session 复用：
  - runtime/provider 测试确认 provider session file 复用。
  - `state/agents/providers/<provider>.json` 在连续两轮后仍是同一 conversation/session。
- 不新增 durable conversation resource；审计以每轮 `ProjectRun` 为准。

### 测试和验证

- CLI 单测：
  - REPL 命令参数互斥：`-i`/`-d`、prompt/command、`--trigger`。
  - `/exit`、Ctrl+D、空输入。
  - provider unsupported 错误。
  - `--prompt`/`--command` 无值作为 interactive 模式标记，带值作为第一轮输入。
- run/integration 测试：
  - 同一 session 连续两轮 run。
  - 每轮生成独立 `ProjectRun`。
  - 每轮 `logs_path` 可独立查看。
  - command REPL 连续两轮复用同一 workspace，并通过 JS runtime command transcript 输出。
- runtime JS 测试：
  - Codex、Claude/cc、OpenCode runner 连续两轮复用 session 文件。
  - Gemini interactive unsupported。

### 验收标准

- `run -i --prompt` 可对 Codex、Claude/cc、OpenCode 完成连续多轮交互。
- `run -i --command` 可连续执行多轮 command，每轮有独立 run 记录和日志。
- 一条用户输入对应一条 run。
- REPL 生命周期内 workspace 连续；prompt 模式 provider conversation 连续。
- 不依赖 TTY、stdin 透传或 terminal resize。
- Gemini 明确 unsupported。

### Harness 命令

- `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
- `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`
- `task build`

## 阶段 10：手册、兼容性和最终质量门禁

### 目标

同步用户文档，移除过时语义，并运行完整质量门禁。

### 依赖

- 阶段 1 到阶段 9 全部完成。
- 所有新增 proto、Go、TS、runtime 代码已生成并提交。

### 实施工作

- 更新 `docs/zh-CN/command-line-manual.md`：
  - 标记或移除 `build`、`push` 已实现暗示，说明后续单独设计。
  - 移除 `up` attach/detach 描述。
  - 补齐 OCI image `pull`、`run --rm`、`run --trigger`、`logs --follow`、`stats`、Jupyter proxy expose、`run -d`/`StopRun` 统一取消、`run -i` prompt/command REPL、`exec <sandbox>` command transcript 的最终语义。
  - 明确不提供 TTY、PTY、WebSocket TTY、terminal resize 或运行中 stdin 透传。
  - 写明 stats 缺失字段按 unknown/null/`-` 表达，只有无稳定指标入口时才 unsupported。
- 如 `docs/zh-CN/design/agent-compose-cli-improvement-plan.md` 仍包含旧决策，追加“已被 spec/plan supersede”说明或同步修正。
- 确认 proto-client 生成产物、Go generated code、runtime JS build 产物不遗漏。
- 更新 PR checklist 测试记录。

### 测试和验证

- `task lint`
- `task build`
- `task test`
- `cd proto-client && npm ci && npm run gen && npm run build`
- `cd runtime/javascript && npm ci && npm run test:unit`
- 如涉及 runtime SDK：`cd runtime/agent-compose-runtime-sdk && npm ci && npm test && npm run test:packaging`

### 验收标准

- 手册与实现一致。
- `build`、`push` 不作为本轮已实现命令出现。
- `up` 不再有 attach/detach 语义。
- CI 对应的 Go、coverage、runtime、proto-client 任务可通过。

### Harness 命令

- `task lint`
- `task build`
- `task test`

## 风险和停止条件

- 如果现有 executor 无法在 agent run 过程中增量写入 cell artifact，阶段 4 必须先在 stream sink 增加 run log append；不能退回轮询 `RunDetail.output`。
- 如果 trigger prompt/template 解析需要 scheduler runtime 执行脚本才能得到 prompt，阶段 3 必须停止并补充 trigger payload/template 设计；不得猜测执行结果。
- 如果 OCI image backend 无法区分“已拉取的 OCI image”和 BoxLite/MicroSandbox materialized runtime artifact，阶段 1 必须先补充 image domain metadata；不得把 `pull` 退回 runtime driver 语义。
- 如果 Jupyter proxy/expose 当前由全局配置强绑定，阶段 6 必须先把 resolved session Jupyter config 写入 session/runtime state；不得用全局变量模拟 agent 级配置。
- 如果 Jupyter expose 需要 runtime driver host port mapping 才能工作，阶段 6 必须先改成通过 agent-compose proxy 暴露；不得为 Docker/BoxLite/MicroSandbox 增加分叉 host expose 行为。
- 如果后台 run supervisor 无法在 daemon 内持有 cancel handle，阶段 7 必须先补齐 in-memory run registry；不得实现成 CLI fork 后台进程。
- 如果某个 runtime 的 `ExecStream(ctx)` 或 prompt 执行路径忽略 context cancellation，阶段 7 必须记录 best-effort 限制并补齐 run supervisor 侧 cancel requested/terminal 状态处理；不得暴露 driver-specific `StopRun` 语义。
- 如果 `agent-compose-runtime exec` 无法在运行过程中稳定写入 command artifacts 和 transcript，阶段 8 必须先修复 JS runtime command 流式归档；不得回退到 host 侧重复拼写 `command-result.json`。
- 如果 provider runner 不能稳定复用 session 文件，阶段 9 只允许对可证明复用的 provider 开启 prompt REPL，其他 provider 返回 unsupported。
