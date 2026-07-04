# CLI runtime capabilities progress

本文档把 `cli-runtime-capabilities` 的 spec 和实施计划拆成可独立执行、独立验收的任务清单。任务按依赖顺序排列；标记为 `可并行` 的子任务可在同一父任务内并发推进，并发上限为 5。

## 文档索引

- Spec：[docs/spec/cli-runtime-capabilities-spec.md](docs/spec/cli-runtime-capabilities-spec.md)
- 实施计划：[docs/plan/cli-runtime-capabilities-implementation-plan.md](docs/plan/cli-runtime-capabilities-implementation-plan.md)
- Harness：[AGENTS.md](AGENTS.md)
- 测试标准：[TESTING.md](TESTING.md)
- Task runner：[Taskfile.yml](Taskfile.yml)
- CLI 手册：[docs/zh-CN/command-line-manual.md](docs/zh-CN/command-line-manual.md)
- 旧 CLI 改进设计：[docs/zh-CN/design/agent-compose-cli-improvement-plan.md](docs/zh-CN/design/agent-compose-cli-improvement-plan.md)

## 执行规则

- 严格按顶层任务顺序执行；只有依赖已满足且标记为 `可并行` 的子任务可以并发。
- 每次只完成一个顶层 checkbox 任务；如果拆分并行子任务，父任务收口时必须统一验收。
- 不实现 `build`、`push`，不为 `up` 增加 attach/detach，不新增 run output chunk DB 表。
- 不实现 TTY、PTY、terminal resize、WebSocket TTY endpoint、Connect bidi stdin 或运行中 stdin 透传。
- 不新增 `ExecInteractive`；`ExecStream` 保持一次性 command server streaming。
- `pull` 只面向 OCI image reference 和 image backend/store，不挂到 Docker、BoxLite 或 MicroSandbox runtime driver。
- Jupyter expose 统一走 agent-compose proxy，不请求 runtime driver host port mapping。
- 涉及 proto 变更时必须重新生成 Go proto、Connect Go 和 `proto-client` TS 产物，并记录生成命令和结果。
- 涉及 CLI 用户可见行为时必须更新或准备更新 `docs/zh-CN/command-line-manual.md`；最终集中校准在阶段 10 完成。
- 每个任务完成后必须把 checkbox 改为 `[x]`，并按 `状态`、`变更`、`验证`、`审计与例外`、`下一目标` 记录完成总结。
- 质量门禁以 `task lint`、`task build`、`task test` 为最终权威；阶段内先运行任务列出的最小测试。

## 阶段 0：协议生成和错误基线

- [x] 0.1 建立 proto 生成、typed error 和 CLI 错误输出基线

  依赖：无。

  工作内容：
  - 确认并记录 Go proto/Connect Go 生成命令，覆盖 `proto/health/v1`、`proto/agentcompose/v1`、`proto/agentcompose/v2`。
  - 确认 `proto-client` 生成命令为 `cd proto-client && npm ci && npm run gen && npm run build`。
  - 审计并统一 typed unsupported、not found、invalid argument 的错误映射，优先复用 owner package sentinel error 和 `pkg/agentcompose/api` 的 Connect code 映射。
  - 在 CLI 层区分 unsupported、not found 和普通 execution failure 的展示与退出。
  - 不改变用户可见功能语义，只建立后续阶段可复用基础。

  可并行子任务：
  - 可并行：审计 `proto/agentcompose/v2/agentcompose.proto` 字段号和生成产物布局。
  - 可并行：审计 `pkg/model/errors.go`、`pkg/images/errors.go` 和 `pkg/agentcompose/api/*` 的错误映射。
  - 可并行：审计 `cmd/agent-compose/main.go` 的 Connect error 到退出码/输出映射。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/driver`
  - 如触达 proto-client：`cd proto-client && npm ci && npm run gen && npm run build`
  - `task build`

  验收标准：
  - 后续任务可复用统一 typed error。
  - proto 生成命令可重复执行并保持产物一致。
  - 未引入功能语义变化时，现有相关测试通过。

  完成总结：
  - 状态：已完成。
  - 变更：
    - 确认 Go proto/Connect Go 稳定生成命令为 `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/health/v1/health.proto proto/agentcompose/v1/agentcompose.proto proto/agentcompose/v2/agentcompose.proto`，并同步修正 `docs/plan/cli-runtime-capabilities-implementation-plan.md` 中的命令，避免 `-I proto` 造成 generated source path 漂移。
    - 确认 `proto-client` 命令为 `cd proto-client && npm ci && npm run gen && npm run build`。
    - 新增 `domain.ErrUnsupported` 和 `pkg/agentcompose/api.ConnectErrorForDomain`，统一 unsupported、not found、invalid argument、failed precondition、already exists、context cancel/deadline 的 Connect code 基线。
    - 让 loader、run、project 相关映射复用或识别新的 unsupported/domain sentinel；保留 loader 未分类错误默认 invalid argument 的既有语义。
    - 让 CLI 将 `connect.CodeUnimplemented` 映射到独立 `exitCodeUnsupported`，避免 unsupported 与普通 execution failure 混淆。
    - 补强 `images.IsNotFound`，同时识别 `imagecache.ErrorKindNotFound`，为后续 image inspect-and-skip 复用。
    - 补充 API、CLI、domain、image backend 错误分类测试。
  - 验证：
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/driver`：通过。
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/health/v1/health.proto proto/agentcompose/v1/agentcompose.proto proto/agentcompose/v2/agentcompose.proto`：通过；除本地 `protoc` 版本注释差异外，生成布局稳定，未提交 generated churn。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `task build`：通过。
  - 审计与例外：
    - `proto/agentcompose/v2/agentcompose.proto` 当前字段号连续且 append-friendly；后续 `AgentSpec` Jupyter 字段应追加为 `11`，`RunService`/`SandboxService` 新 RPC 和消息应追加新字段号，不复用旧号。
    - 当前本机 `protoc` 为 `libprotoc 3.21.12`，仓库中 `proto/health/v1/health.pb.go` 头部记录为 `protoc v7.34.1`；该差异只影响版本注释，已避免提交无意义 generated churn。
    - 未引入 CLI 命令功能语义变化；仅建立 unsupported/not-found/invalid-argument 错误分类和退出码基线。
  - 下一目标：1.1。

## 阶段 1：OCI image `pull` inspect-and-skip

- [x] 1.1 实现 driver-independent OCI image `pull <image>` inspect-and-skip

  依赖：0.1。

  工作内容：
  - 在 image service/backend 中实现 pull 前 inspect，判断本地 OCI image store/backend 是否已有 `<image>`。
  - Docker daemon 只作为 Docker-backed OCI image backend 使用，不作为 runtime driver pull 能力。
  - OCI cache 或其他 daemon-less store 使用已有 inspect/cache status 能力。
  - 本地命中时返回 succeeded，`PullImageResponse.warnings` 加入 skipped/local already exists 信息，`image` 填充本地镜像信息。
  - CLI 文本输出显示已存在并跳过；JSON 输出保留 warnings。
  - 保持 deprecated `agent-compose image pull <image>` 与顶层 `pull` 行为一致。
  - 不在 Docker、BoxLite、MicroSandbox runtime driver interface 上新增 pull/image-store 语义。

  可并行子任务：
  - 可并行：补 image service/backend fake，断言 inspect 命中时不调用 pull。
  - 可并行：补 Docker-backed OCI image inspect 命中/未命中测试。
  - 可并行：补 OCI cache inspect 命中/未命中测试。
  - 可并行：补 CLI 文本/JSON 输出和 deprecated wrapper 行为测试。
  - 可并行：补 service/CLI 测试，证明 runtime driver 选择不影响 `pull` 行为。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/adapters ./pkg/images ./pkg/imagecache ./pkg/driver`
  - `task build`

  验收标准：
  - 本地已有 OCI image 不会再次 pull。
  - inspect 失败和 pull 失败仍返回非 0，并带 image reference 和 image backend/store 上下文。
  - `pull` 结果不随 runtime driver 为 Docker、BoxLite、MicroSandbox 而改变。
  - deprecated `image pull` 无行为分叉。

  完成总结：
  - 状态：已完成。
  - 变更：
    - `pkg/agentcompose/api.ImageHandler.PullImage` 在调用 backend pull 前先执行 `InspectImage`；本地命中时直接返回 succeeded，填充本地 image/resolved ref，并在 `warnings` 中记录 skipped/local already exists。
    - inspect 返回 typed not found 时继续执行 pull；inspect 其他错误通过 `ConnectErrorForImageBackend("inspect image before pull", ...)` 返回，保留 image backend/store 上下文。
    - CLI 文本输出统一走 `writeImagePullText`，有 skipped/already exists warning 时显示 `Skipped <image>` 并打印 warning；JSON 输出继续保留 `warnings`。
    - 保持顶层 `pull` 与 deprecated `image pull` 共享同一 `PullImage` API 调用路径。
  - 验证：
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/adapters ./pkg/images ./pkg/imagecache ./pkg/driver`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本实现只改 image service/backend 调用链，没有在 Docker、BoxLite、MicroSandbox runtime driver interface 上新增 pull/image-store 语义。
    - `pull` 请求和 v2 `ImageService.PullImage` 不包含 runtime driver 参数；runtime driver 选择不会参与 `pull <image>` 行为。
    - deprecated `agent-compose image pull <image>` 仍复用 `runComposeImagePullCommand`，没有新增行为分叉。
  - 下一目标：2.1。

## 阶段 2：`run --rm` terminal 清理

- [x] 2.1 修正 `run --rm` 在成功、失败和取消后的 cleanup 语义

  依赖：0.1。

  工作内容：
  - 将 `--rm` cleanup 语义下沉到 `pkg/runs.Controller` 或同等 service run pipeline。
  - 扩展 v2 cleanup policy，首选新增 `REMOVE_ON_COMPLETION`；默认仍为 stop-on-completion，`--keep-running` 仍为 keep-running。
  - 只清理本次 run 创建的 session/sandbox；用户显式传入的已有 `--sandbox`/`--session-id` 不删除。
  - terminal 状态包括 succeeded、failed、canceled，均尝试 cleanup。
  - cleanup 失败写入 `project_run.cleanup_error`，并按 spec 保持原始 run 错误优先。
  - 如短期保留 CLI cleanup，必须放在失败退出前执行，并输出 cleanup warning；CLI 不能继续作为权威 cleanup owner。

  可并行子任务：
  - 可并行：补 `pkg/runs` fake runtime/store，覆盖 succeeded、failed、canceled cleanup。
  - 可并行：补指定已有 sandbox 时不删除的测试。
  - 可并行：补 cleanup 失败写入 `cleanup_error` 的测试。
  - 可并行：补 CLI exit code 和 stderr warning 测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/sessionstore`
  - `task build`

  验收标准：
  - `--rm` 对所有 terminal run 生效。
  - 不会删除用户显式指定的已有 sandbox。
  - cleanup 错误不覆盖原始 run 错误。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `RunSessionCleanupPolicy` 新增 `RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION = 3`，并重新生成 Go proto/Connect Go 与 proto-client TS 产物。
    - CLI `run --rm` 改为向 `RunAgentStream` 发送 `REMOVE_ON_COMPLETION`；默认仍为 `STOP_ON_COMPLETION`，`--keep-running` 仍优先映射为 `KEEP_RUNNING`。
    - CLI 不再在 run 成功后调用 `SandboxService.RemoveSandbox` 作为权威 cleanup owner；改为读取 `RunDetail.cleanup_error`，成功 run 的 cleanup 失败返回非 0，失败 run 的 cleanup 失败追加 cleanup warning 且保留原始退出码。
    - `pkg/runs.Controller` 在 terminal transition 后统一执行 cleanup；`REMOVE_ON_COMPLETION` 只删除本次 run 新建的 session，显式传入的已有 `--sandbox`/`--session-id` 只按策略停止、不删除。
    - controller cleanup 覆盖 succeeded、failed、`context.Canceled` canceled、agent config/executor 缺失失败、session start failure 等路径；cleanup 失败写入 `project_run.cleanup_error`，不覆盖原始 run status/error。
    - session 删除成功后通知 dashboard `session_removed`，保持与 sandbox remove API 的可观测性基本一致。
    - 补充 CLI `--rm` cleanup policy、JSON 输出、cleanup warning/exit code 测试，以及 `pkg/runs` remove-on-completion 成功、失败、取消、已有 session、cleanup 失败和 session start failure 测试。
  - 验证：
    - `go test ./cmd/agent-compose ./pkg/runs`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/sessionstore`：通过。
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本任务没有实现后台 run supervisor、cancel map 或 `StopRun` 对 live execution 的强取消；当前 `StopRun` 仍是既有的 DB 状态标记路径，后续后台/cancel 语义按计划中 `run -d/--detach` 阶段处理。
    - `run --rm --keep-running` 保持现有 flag 优先级，`--keep-running` 优先于 `--rm`，没有新增互斥 usage error。
    - CLI 手册已描述 `--rm` 为运行结束后删除 sandbox；本阶段未新增稳定命令或参数，最终文案校准仍留到阶段 10。
  - 下一目标：3.1。

## 阶段 3：`run --trigger` managed trigger 解析

- [x] 3.1 实现手动 `run --trigger <trigger_id>` 的 trigger 解析和 prompt 注入

  依赖：0.1。

  工作内容：
  - 新增 `ResolveTriggerForManualRun(ctx, projectID, agentName, triggerID)` 或等价内部函数。
  - 从 `project_scheduler` 反查当前 project/agent 的 managed loader，再复用 `loaders.Controller.LoadLoaderForRun(ctx, loaderID, triggerID)` 或等价逻辑。
  - 校验 trigger 存在且属于当前 project/agent。
  - 解析 scheduler id、managed loader id、trigger prompt/template、环境变量、上下文和 Jupyter 默认配置。
  - 在 `BeginRun` 前解析 trigger，并将实际 prompt 写入 run start request 和 `ExecuteAgentRequest.Message`。
  - disabled trigger 手动运行允许执行，但 CLI stderr 和 JSON 输出要包含 warning。
  - trigger 不存在、agent/project 不匹配、prompt/template 解析失败时返回清晰错误；未创建 run 前失败不得落可执行 run。

  可并行子任务：
  - 可并行：补 loader/project store 查询和 trigger resolver 单测。
  - 可并行：补 run executor fake，断言 `ExecuteAgentRequest.Message` 使用解析后 prompt。
  - 可并行：补 disabled trigger warning 测试。
  - 可并行：补 CLI `--trigger` 与 `--prompt`/`--command` 互斥和 JSON warnings 输出测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/loaders ./pkg/projects ./pkg/storage/configstore`
  - 针对 `up` 注册 managed loader 后 `run --trigger` 的 integration 测试。
  - `task build`

  验收标准：
  - `project_run.prompt` 保存实际执行 prompt。
  - run summary/detail 保留原始 `trigger_id`。
  - `run --trigger` 不依赖 scheduler 自动触发路径。

  完成总结：
  - 状态：已完成。
  - 变更：
    - `pkg/runs.Controller` 在 `BeginRun` 前解析 manual `run --trigger`：按 project/agent 反查 `project_scheduler`，加载对应 managed loader，并校验 loader 的 managed project、agent、scheduler 归属。
    - 新增 manual trigger capture 流程：执行指定 loader trigger callback 到 `scheduler.agent(...)` capture host，只提取 prompt、`sessionEnv` 和 output schema 等 agent request 输入，不创建 loader run，也不依赖 scheduler 自动触发路径。
    - 解析后的 prompt 写入 `project_run.prompt`，并进入 `ExecuteAgentRequest.Message`；原始 `trigger_id` 和解析出的 `scheduler_id` 保留在 run summary/detail。
    - disabled scheduler/trigger 允许手动运行，并把 warning 附加到 run response；CLI 文本输出写 stderr，`--json` 输出写入 run JSON 的 `warnings` 字段。
    - v2 `RunAgentResponse`、`RunAgentStreamResponse`、`RunSummary`、`RunDetail` 增加 `warnings` 字段，并重新生成 Go proto/Connect Go 与 proto-client TS 产物。
    - 补充 `pkg/runs` manual trigger 成功解析、disabled trigger warning、missing trigger 不创建 run 测试；补充 CLI trigger warning 文本/JSON 输出测试。
  - 验证：
    - `go test ./pkg/runs ./pkg/agentcompose/app ./pkg/agentcompose/api ./cmd/agent-compose`：通过。
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/loaders ./pkg/projects ./pkg/storage/configstore`：通过。
    - `task build`：通过。
  - 审计与例外：
    - trigger resolution 只面向当前 project/agent 的 managed scheduler loader；全局 loader trigger id 不能绕过 project/agent 归属校验。
    - manual trigger resolution 不创建 `loader_run`，避免把 operator 手动 run 混入 scheduler 自动触发审计链路。
    - 当前 compose trigger YAML 只有 prompt/session options，没有 Jupyter 字段；Jupyter 默认配置仍按后续 Jupyter 阶段实现。
  - 下一目标：4.1。

## 阶段 4：`logs --follow` 文件 tail

- [x] 4.1 统一 run 过程中的 `logs_path/output.txt` 实时 append

  依赖：0.1、2.1。

  工作内容：
  - 审计 command run 当前 `state/runs/<run_id>/output.txt` 写入路径。
  - 审计 agent run 当前 `state/cells/<cell_id>/output.txt` 写入路径。
  - command run 的 stream writer 同步 append 到 `project_run.logs_path`。
  - agent run 如 cell artifact 不能增量写入，则在 `AgentExecutionStream.OnChunk` 或等价 stream sink 中同步 append 到 run log artifact。
  - 保持 `project_run.output` 为汇总视图，不作为 follow 权威来源。
  - 不新增 DB output chunk 表。

  可并行子任务：
  - 可并行：审计 command run 日志写入和测试 fixture。
  - 可并行：审计 agent execution stream/cell artifact 增量写入能力。
  - 可并行：补日志 append helper 单测，覆盖 stdout/stderr 顺序和文件 flush。

  测试方案：
  - `go test ./pkg/runs ./pkg/execution ./pkg/storage/sessionstore`
  - command run 和 agent run 相关 integration 测试。
  - `task build`

  验收标准：
  - running run 期间 `logs_path` 文件可增量增长。
  - run 结束后 `project_run.output` 与日志文件关系明确且稳定。
  - 未创建 run output chunk DB 表。

  完成总结：
  - 状态：已完成。
  - 变更：
    - command run 在 `ExecStream` writer 收到每个 chunk 时同步 append 到 `state/runs/<run_id>/output.txt`，该路径继续作为 `project_run.logs_path`。
    - agent run 在 `AgentExecutionStream.OnChunk` 收到每个 chunk 时同步 append 到 `state/cells/<cell_id>/output.txt`，该路径继续作为 `project_run.logs_path`。
    - 新增共享 append helper，按 chunk 顺序写入 stdout/stderr 文本，不创建 run output chunk DB 表。
    - 保持 `project_run.output` 为 terminal transition 的汇总输出；`logs_path` 是后续 follow 的文件权威来源。
    - 补充 command run、agent run 和 append helper 测试，覆盖日志文件内容与 stdout/stderr chunk 顺序。
  - 验证：
    - `go test ./pkg/runs ./pkg/execution ./pkg/storage/sessionstore`：通过。
    - `task build`：通过。
  - 审计与例外：
    - command run 结束时仍通过 `WriteCellArtifacts` 用累积结果重写 `output.txt`，因此最终文件与 `project_run.output` 保持一致；运行中同一路径已可随 stream chunk 增长。
    - agent executor 结束时仍会写完整 cell artifacts；本阶段只保证 `OnChunk` 期间同一 `logs_path` 可增量读取。
    - 本阶段未新增 DB output chunk 表，也未实现 follow API；`logs --follow` 的服务端 tail 语义留到 4.2。
  - 下一目标：4.2。

- [x] 4.2 实现 `FollowRunLogs` API 和 CLI `logs --follow`

  依赖：4.1。

  工作内容：
  - 在 v2 `RunService` 新增 server streaming `FollowRunLogs`。
  - 定义 request：`project_id`、`run_id`、`tail_lines`、`start_offset`、`follow`。
  - 定义 response：`data`、`offset`、`is_final`、`run_status`、`created_at`。
  - service 按 byte offset 读取 `logs_path`，terminal 后 flush 剩余内容并发送 final。
  - `--tail N` 由 service 计算起始 offset，CLI 不直接读取 daemon 本地文件。
  - CLI `logs --follow --run-id <id>` 调用新 API；保留 `logs --json --follow` 互斥错误。
  - 如多 run follow 首版串行处理，必须在手册中说明。

  可并行子任务：
  - 可并行：proto/message 设计和 Go/TS 生成。
  - 可并行：service file tail helper 和 offset/tail 单测。
  - 可并行：CLI follow 输出和重复输出测试。
  - 可并行：缺失日志文件按 run 状态返回清晰错误或空 final 的测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/configstore ./pkg/storage/sessionstore`
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `task build`

  验收标准：
  - `logs --follow` 不再轮询 `RunDetail.output`。
  - offset、tail、terminal final 行为可测试。
  - 缺失日志文件有清晰错误或按 run 状态返回空 final。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `RunService` 新增 server streaming `FollowRunLogs`，请求包含 `project_id`、`run_id`、`tail_lines`、`start_offset`、`follow`，响应包含 `data`、`offset`、`is_final`、`run_status`、`created_at`，并重新生成 Go proto/Connect Go 产物。
    - `pkg/agentcompose/api.RunHandler` 按 `project_run.logs_path` 从 byte offset 读取日志；`tail_lines` 由服务端计算起始 offset，terminal run 会 flush 剩余内容并发送 `is_final` chunk。
    - missing log file 对 terminal run 返回空 final chunk；project mismatch/not found 返回 Connect not found。
    - CLI `logs --follow --run-id <id>` 改为调用 `FollowRunLogs`；project-level `logs --follow` 首版按当前匹配 run 列表串行 follow；`logs --json --follow` 保持互斥 usage error。
    - CLI `--tail N --follow` 通过 `FollowRunLogsRequest.tail_lines` 交给 daemon 处理，不直接读取 daemon 本地文件。
    - 补充 API streaming 测试覆盖 full read、`start_offset`、`tail_lines`、terminal final、missing log file 和 project mismatch；补充 CLI follow 测试，断言不再轮询 `RunDetail.output`。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/configstore ./pkg/storage/sessionstore`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本阶段没有新增 run output chunk DB 表；follow 权威来源仍是 4.1 建立的 `logs_path/output.txt` 文件。
    - `logs --follow` 不再轮询 `RunDetail.output`；非 follow 的 `logs` 输出仍沿用既有 detail/list 路径。
    - 多 run follow 首版为串行处理，后续 CLI 手册集中校准时需记录该行为。
  - 下一目标：5.1。

## 阶段 5：`stats` driver optional interface

- [x] 5.1 实现稳定 JSON model、`GetSandboxStats` 和 CLI `stats`

  依赖：0.1。

  工作内容：
  - 在 v2 API 中扩展 `SandboxService.GetSandboxStats`，避免新增语义分散的 service。
  - 定义 `SandboxStats`：CPU、memory、network、block IO、uptime、driver、sampled_at。
  - 为各 metric 字段明确可空性：使用 proto optional/wrapper，或定义 `MetricValue { value, status }`，其中 `status` 至少表达 ok、unknown、unavailable。
  - JSON 输出保持字段 key 稳定，不因 Docker、BoxLite、MicroSandbox 不同而省略字段。
  - driver 层使用 optional interface 承载 stats。
  - Docker runtime 基于 Docker stats API 实现单次 snapshot。
  - MicroSandbox runtime 映射 SDK `Metrics` 中已有字段。
  - BoxLite runtime 如 metrics 回调稳定可用，映射可获得字段；不可获得字段返回 unknown/null。
  - driver 没有稳定指标入口时由 service 返回 typed unsupported，不显示为 execution failed。
  - CLI 新增 `stats <sandbox>`，支持表格输出和 `--json`。

  可并行子任务：
  - 可并行：proto/API 和生成产物。
  - 可并行：Docker stats sample parser 测试。
  - 可并行：MicroSandbox metrics sample 映射测试。
  - 可并行：BoxLite metrics fake callback 映射和缺失字段测试。
  - 可并行：CLI 表格/JSON 输出稳定 key 测试。
  - 可并行：service unsupported/stopped/not found 测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/storage/sessionstore ./pkg/driver`
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `task build`

  验收标准：
  - Docker sandbox 可返回 CPU/memory/network/block IO/uptime。
  - MicroSandbox sandbox 可返回 SDK 已有的 CPU/memory/disk/network/uptime 字段。
  - BoxLite sandbox 返回可获得字段；缺失字段不显示为 execution failed。
  - JSON 字段保持稳定；driver 差异只通过 value null、status unknown/unavailable 或表格 `-` 表达。
  - 无指标入口的 driver 返回明确 unsupported。
  - `stats` 不影响现有 `ps`、`inspect` 行为。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `SandboxService` 新增 `GetSandboxStats`；新增 `MetricStatus`、`MetricValue`、`SandboxStats`、`GetSandboxStatsRequest/Response`，并重新生成 Go proto/Connect Go 产物。
    - 新增共享 sandbox stats domain/driver model，metric 字段统一使用 `value + unit + status + message`，状态覆盖 `ok`、`unknown`、`unavailable`。
    - API `SandboxHandler.GetSandboxStats` 解析 sandbox/session、reconcile runtime state、拒绝 stopped sandbox，并通过 runtime optional `Stats` interface 返回指标；无稳定入口映射为 typed unsupported。
    - Docker runtime 使用 Docker one-shot stats API 映射 CPU、memory、network、block IO 和 uptime。
    - MicroSandbox runtime 映射 SDK `Metrics` 的 CPU、memory、disk IO、network 和 uptime 字段。
    - BoxLite cgo runtime 当前 wrapper 未暴露稳定 metrics 调用，返回稳定 unknown metric 字段；非 cgo stub 仍按无稳定入口返回 unsupported。
    - CLI 新增 `stats <sandbox>`，支持表格输出和 `--json`；JSON 保持稳定 key，unknown/unavailable metric 使用 `value: null` 和 status 表达，文本表格显示 `-`。
    - 更新 `docs/zh-CN/command-line-manual.md`，补充 `stats` 命令说明，并移除 stats 暂缓说明。
    - 补充 API service 测试、CLI 表格/JSON/unsupported 测试、Docker stats mapping 测试、MicroSandbox metrics mapping 测试。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/driver`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/storage/sessionstore ./pkg/driver`：通过。
    - `task build`：通过。
  - 审计与例外：
    - `stats` 是单次 snapshot，不实现持续 watch/stream。
    - 本阶段没有改变现有 `ps`、`inspect` 行为；新增命令直接调用 sandbox v2 API。
    - BoxLite 当前没有可调用的稳定 metrics binding，因此可得字段为空且全部以 unknown 表达；后续如 wrapper 暴露 metrics，可在 optional `Stats` 实现内补充映射，不需要改 CLI JSON shape。
  - 下一目标：6.1。

## 阶段 6：Jupyter CLI/YAML 和 proxy expose

- [x] 6.1 增加 agent YAML Jupyter schema、解析和 validation

  依赖：0.1。

  工作内容：
  - proto `AgentSpec` 增加 `JupyterSpec`：`enabled`、`guest_port`。
  - YAML parsing/normalization 支持 `agents.<name>.jupyter.enabled` 和 `guest_port`。
  - `guest_port` 为 0 时使用 daemon 默认，非 0 时校验合法 TCP port。
  - YAML 中出现 host bind、host port、expose 类字段时返回 validation error。
  - project agent/agent definition 通过 spec JSON 保留 Jupyter 配置；除非 UI 列表必须展示，不新增 DB 列。

  可并行子任务：
  - 可并行：proto 和 TS client 生成。
  - 可并行：compose YAML parsing/normalization 单测。
  - 可并行：validation error 单测。
  - 可并行：project agent/agent definition spec JSON 保留测试。

  测试方案：
  - `go test ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/compose ./pkg/projects ./pkg/storage/configstore ./pkg/config`
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `task build`

  验收标准：
  - YAML 可表达 agent 默认 Jupyter proxy 配置。
  - 默认 disabled。
  - YAML 不允许隐式 host expose。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `AgentSpec` 追加 `JupyterSpec jupyter = 11`，新增 `JupyterSpec.enabled` 和 `JupyterSpec.guest_port`，并重新生成 Go proto 产物与 `proto-client` TS 产物。
    - compose YAML 支持 `agents.<name>.jupyter.enabled` 和 `guest_port`；`guest_port == 0` 保留为 daemon 默认含义，非 0 端口必须在 `1..65535` TCP 端口范围内。
    - `jupyter` nested schema 只允许 `enabled`、`guest_port`；`host_port` 等 host bind/expose 类字段由 strict YAML validator 以 unknown field 拒绝。
    - normalized/canonical project spec、redacted output、spec hash、API proto mapping 和 proto-origin YAML shape 均保留非默认 Jupyter 配置。
    - `project_agent.spec_json` 通过 normalized agent JSON 保留 Jupyter 配置；project-managed `agent_definition.config_json` 通过现有 JSON object 写入 `jupyter` 配置，不新增 DB 列。
    - 补充 compose parser/normalizer/canonical output/hash、API mapping、project record 和 project controller dry-run 覆盖。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./pkg/compose ./pkg/agentcompose/api ./pkg/projects`：通过。
    - `go test ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/compose ./pkg/projects ./pkg/storage/configstore ./pkg/config`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本阶段只实现 schema、解析、validation、canonical/API mapping 和 spec JSON preservation；未接入 runtime/session/proxy resolved config，未新增 CLI `--jupyter`、`--jupyter-expose` 或 host port mapping。
    - 默认/空 `jupyter: {}` 归一化为未设置，保持默认 disabled 且避免既有 spec hash churn；显式启用或设置非 0 `guest_port` 才进入 canonical spec。
    - YAML 和 proto API 均不提供 host bind、host port 或 expose 字段；后续 6.2 的访问路径仍必须走 agent-compose proxy。
  - 下一目标：6.2。

- [x] 6.2 将 Jupyter resolved config 接入 run/session/proxy 和 CLI expose intent

  依赖：6.1。

  工作内容：
  - run/session 创建时解析 agent YAML default 和 CLI 覆盖。
  - `run --jupyter` 覆盖 enabled=true。
  - `run --jupyter-expose` 只来自 CLI，效果是创建/标记 agent-compose proxy access endpoint。
  - proxy listen/bind 由 daemon 部署配置决定，不作为 agent YAML 或 runtime driver port mapping。
  - session/proxy 状态只表达 agent-compose proxy route/access URL 和 guest endpoint。
  - 若 `ProxyState.HostPort` 继续存在，只能表示 daemon proxy 层访问结果，不得写入 Docker、BoxLite、MicroSandbox driver host port mapping。
  - runtime 创建参数不得因 `--jupyter-expose` 请求 driver host port expose。
  - trigger-created sandbox 使用 agent resolved Jupyter config。
  - 更新 `docs/zh-CN/command-line-manual.md` 中 Jupyter 说明，或在阶段 10 集中收口时记录待同步项。

  可并行子任务：
  - 可并行：session/proxy state 测试。
  - 可并行：CLI flag 到 request/session options 测试。
  - 可并行：trigger-created sandbox Jupyter config integration 测试。
  - 可并行：Docker、BoxLite、MicroSandbox runtime create request 不包含 Jupyter host port mapping 的 fake 测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/sessionstore ./pkg/config ./pkg/driver`
  - `task build`

  验收标准：
  - YAML enabled 会让 trigger-created sandbox 按 resolved config 启动 Jupyter proxy。
  - YAML enabled 不产生外部 host bind/host port 配置。
  - CLI `--jupyter-expose` 只创建 agent-compose proxy access endpoint。
  - Docker、BoxLite、MicroSandbox runtime 创建请求均不包含 Jupyter host port mapping。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `RunAgentRequest` 新增 `RunJupyterSpec jupyter`，支持 run-level `enabled`、`guest_port`、`expose` intent，并重新生成 Go proto/Connect Go 与 `proto-client` TS 产物。
    - CLI `run` 新增 `--jupyter` 和 `--jupyter-expose`；`--jupyter-expose` 会携带 expose intent 并同时启用 Jupyter，不新增 runtime-driver 级外部发布参数。
    - `runs.Controller` 从 project revision 中解析 agent YAML Jupyter 默认值，并叠加 run request 覆盖；scheduler/trigger-created run 通过同一 pipeline 使用 YAML 默认配置。
    - `sessionstore` 新增 `CreateSessionWithOptions` 和 Jupyter session options；默认 `CreateSession` 不启用 Jupyter，enabled 时写入 proxy state 的 guest port、token、proxy path 和 expose marker。
    - `ProxyState`/driver proxy state 增加 `enabled`、`exposed`；disabled session 不启动或探测 Jupyter，proxy route对 disabled session 返回明确错误。
    - Docker、BoxLite、MicroSandbox driver 仅在 resolved Jupyter enabled 时启动 Jupyter/等待 ready；disabled session 使用 idle runtime，不创建 Jupyter port config。
    - CLI 手册更新 `--jupyter`、`--jupyter-expose` 说明，并移除其“暂未稳定发布”的说明。
    - 补充 CLI request mapping、app request adapter、session/proxy state、runs controller YAML+CLI resolution 和 driver proxy state 覆盖。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/storage/sessionstore ./pkg/config ./pkg/driver ./pkg/sessions`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本阶段没有实现 `--no-jupyter`；当前只支持 YAML default 和 CLI/API enable/expose 的正向覆盖。
    - `--jupyter-expose` 只写入 agent-compose proxy expose intent，不传递用户可控 host bind/host port 给 runtime driver。
    - Docker、BoxLite、MicroSandbox 在 Jupyter enabled 时仍保留既有 daemon-internal loopback/SDK connector，用于 agent-compose proxy 访问 guest Jupyter；该端口不来自 YAML/CLI host bind，不作为外部暴露语义。
    - 已避免在 disabled 默认路径启动 Jupyter 或创建 Jupyter driver port config；后续如要完全移除 driver-local connector，需要先替换 daemon 到 guest 的 proxy reachability 机制。
  - 下一目标：7.1。

## 阶段 7：`run -d/--detach` 和统一取消

- [x] 7.1 实现 daemon-owned `StartRun`/后台 supervisor 和统一 `StopRun` cancellation

  依赖：4.2。

  工作内容：
  - 在 v2 `RunService` 新增 `StartRun(StartRunRequest) returns (StartRunResponse)`，或实现等价 detach API；首选 `StartRun`。
  - 复用现有 `RunAgentRequest` 字段和 run pipeline。
  - daemon 创建 run 后返回 `run_id`、`session_id`、status。
  - 后台 goroutine/supervisor 持有 run context 和 cancel handle。
  - supervisor registry 保存 cancel handle，`StopRun` 通过统一 run context cancellation 请求停止。
  - prompt 和 command 执行都沿用 common execution context，取消信号传递到 `RunAgentStream`/`ExecStream(ctx)` 及下游 JS runtime transcript 执行。
  - run 状态机统一进入 canceling/canceled 或 failed terminal 状态；不得因 driver/provider/子进程无法即时终止而长期保持 running。
  - Docker、BoxLite、MicroSandbox 如需要底层强制终止，只能封装为 best-effort cleanup，不暴露成不同用户语义。
  - 不实现 durable queue。

  可并行子任务：
  - 可并行：proto/API 和生成产物。
  - 可并行：in-memory supervisor registry 单测。
  - 可并行：`StopRun` cancel active run 并稳定落 terminal 状态测试。
  - 可并行：fake runtime 验证 context cancellation 传递到 prompt/command 执行路径。
  - 可并行：fake runtime 忽略 cancellation 时的 cancel requested 和最终 terminal 状态测试。

  测试方案：
  - `go test ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `task build`

  验收标准：
  - `StartRun` 立即返回，后台 run 最终 terminal。
  - CLI 进程不持有后台 run 生命周期。
  - `StopRun` 在 Docker、BoxLite、MicroSandbox 上保持同一 API 和状态语义。
  - 差异只允许存在于底层 best-effort 终止能力。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 `RunService` 新增 `StartRun(StartRunRequest) returns (StartRunResponse)`，`StartRunRequest` 复用现有 `RunAgentRequest`，并重新生成 Go proto/Connect Go 与 `proto-client` TS 产物。
    - `runs.Controller` 将 run 创建拆为 `StartProjectRun` 和 shared execution closure；同步 `RunAgent`/`RunAgentStream` 继续复用同一执行 pipeline。
    - 新增 app-level in-memory `RunSupervisor`，使用 daemon root context 启动后台 goroutine，维护 active run cancel registry，并在执行结束后 unregister。
    - `StartRun` 先同步创建 pending run 并立即返回 run summary；后台 goroutine 后续负责 session 创建、执行、terminal transition 和 cleanup。
    - `StopRun` 对 active supervised run 先取消 run context，再将 run 标记为 canceled；非 active pending/running run 保留既有 DB fallback cancel 语义。
    - run pipeline 在 session start 后检查已被 `StopRun` 标记 terminal 的 run，避免被取消 run 再进入 running；prompt 和 command 仍沿用传入 execution context。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `go test ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本阶段只新增 daemon-owned `StartRun` API 和 supervisor；CLI `run -d`、输出提示和 daemon restart reconcile 留到 7.2。
    - 未实现 durable queue；daemon 进程退出后 active registry 不保留，后续 7.2 负责 restart reconcile。
    - 对忽略 context cancellation 的 provider/runtime，`StopRun` 会先把 DB 状态标记为 canceled；后台执行若随后返回成功，terminal transition 会被状态机拒绝，最终用户语义仍保持 canceled。
    - Docker、BoxLite、MicroSandbox 的底层强制终止仍是后续 best-effort cleanup 能力，不暴露为分叉用户语义。
  - 下一目标：7.2。

- [x] 7.2 接入 CLI `run -d`、日志提示和 daemon restart reconcile

  依赖：7.1。

  工作内容：
  - CLI `run -d` 调用后台启动 API。
  - `run -d` 与 `-i` 互斥。
  - 文本输出 run id、sandbox id、初始 status、查看日志命令。
  - JSON 输出 run/session/status/logs command 或 logs URL。
  - daemon restart reconcile 将 orphan pending/running run 标记 failed，error 写明 `daemon interrupted`。
  - `run -d --command` 可通过 `logs --follow` 观察输出。

  可并行子任务：
  - 可并行：CLI 参数互斥和输出测试。
  - 可并行：restart reconcile 测试。
  - 可并行：`run -d --command` + logs integration 测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
  - `task build`

  验收标准：
  - `run -d` 立即返回。
  - daemon restart 不留下永久 running run。
  - 后台 run 可被 `logs` 和 `stop`/`StopRun` 操作。

  完成总结：
  - 状态：已完成。
  - 变更：
    - CLI `run -d/--detach` 复用现有 `RunAgentRequest` 构造逻辑，调用 v2 `RunService.StartRun`，不再走 `RunAgentStream`，提交后立即返回。
    - 新增预留 `-i/--interactive` flag；`-d` 与 `-i` 明确互斥，单独使用 `-i` 返回 unsupported，不实现 TTY/PTY/运行中 stdin。
    - detached 文本输出包含 run id、sandbox/session id、初始 status 和可直接执行的 `agent-compose logs --run-id <run-id> --follow` 提示；提示会保留 `--host`、`--file` 和 `--project-name`。
    - detached JSON 输出复用 run summary 字段，并新增可省略的 `logs_command` 字段；warnings 保留在 JSON 或文本 stderr。
    - daemon startup reconcile 的 stale project run error 调整为包含 `daemon interrupted`；已有 pending/running run 会通过 `Coordinator.MarkFailed` 标记 failed。
    - CLI 手册更新 `run -d` 参数、示例、互斥规则和日志观察方式。
  - 验证：
    - `go test ./cmd/agent-compose -run 'TestIntegrationCLIRunDetach|TestCLIRunInputModeUsageErrors|TestCLIRunInteractiveReservedUnsupported'`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 本阶段没有新增 durable queue；daemon 重启后，未完成的 pending/running run 按 startup reconcile 标记 failed，错误包含 `daemon interrupted`。
    - detached `StartRunResponse.Run` 是启动时 summary，session id 可能在后台 session 创建完成前为空；CLI 文本用 `-` 占位，JSON 保持空字符串。
    - `run -d --command` 的输出观察依赖阶段 4 已实现的 run logs append 与 `FollowRunLogs`，未新增 DB output chunk 表。
    - `-i/--interactive` 只作为互斥和 unsupported 基线保留，没有实现 TTY、PTY、WebSocket TTY、resize 或 stdin 透传。
  - 下一目标：8.1。

## 阶段 8：command transcript 基础与 `exec <sandbox>`

- [x] 8.1 定义统一 `TranscriptEvent` 并实现 JS runtime command transcript/artifacts

  依赖：0.1、4.1。

  工作内容：
  - v2 定义统一 `TranscriptEvent` message，字段包含 `kind`、`text`、`is_stderr`、`name`、`payload_json`、`created_at`。
  - `RunAgentStreamResponse` 和 `ExecStreamResponse` 使用 `TranscriptEvent transcript` 承载过程输出。
  - v2 不需要保持兼容，可移除或弃用原 `chunk/is_stderr` 直出字段，避免 CLI 同时维护两套输出逻辑。
  - `runtime/javascript/src/command.ts` 在启动 command 前输出 `$ <command>` transcript。
  - stdout/stderr 继续原样写入 `stdout.txt`、`stderr.txt`、`output.txt` 和进程 stdout/stderr。
  - 非零 exit code 时输出简短 exit 摘要；结构化结果仍写入 `command-result.json`。

  可并行子任务：
  - 可并行：proto `TranscriptEvent` 设计和 Go/TS 生成。
  - 可并行：runtime JS command transcript 单测。
  - 可并行：runtime JS command artifact 写入单测。

  测试方案：
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`
  - `task build`

  验收标准：
  - prompt 和 command stream 可复用同一 transcript event 模型。
  - `agent-compose-runtime exec` 写入 stdout/stderr/output/result artifacts。
  - 不新增 `ExecInteractive`、WebSocket、TTY、PTY、resize 或运行中 stdin。

  完成总结：
  - 状态：已完成。
  - 变更：
    - v2 新增 `TranscriptEvent{kind,text,is_stderr,name,payload_json,created_at}`，并为 `RunAgentStreamResponse` 与 `ExecStreamResponse` 增加 `transcript` 字段；Go proto/Connect Go 与 `proto-client` TS 已重新生成。
    - run stream 和 exec stream 的 output event 现在填充统一 `TranscriptEvent`；CLI `run` 与 `exec` 优先读取 `transcript`，并保留 legacy `chunk/is_stderr` fallback。
    - JS runtime command 在启动前向 runtime stdout 输出 `$ <command>` transcript，非零退出时向 runtime stderr 输出简短 exit 摘要。
    - JS runtime command 的 child stdout/stderr 仍原样写入 `stdout.txt`、`stderr.txt`、`output.txt` 和进程 stdout/stderr；`command-result.json` 继续保存结构化结果。
    - 补充 CLI transcript-only stream 测试，以及 JS runtime transcript header、非零 exit 摘要和 raw artifact 不污染测试。
  - 验证：
    - `protoc -I . --go_out=. --go_opt=module=agent-compose --connect-go_out=. --connect-go_opt=module=agent-compose proto/agentcompose/v2/agentcompose.proto`：通过。
    - `cd proto-client && npm ci && npm run gen && npm run build`：通过。
    - `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`：通过。
    - `task build`：通过。
  - 审计与例外：
    - 为降低仓库内同步切换风险，本阶段没有移除 legacy `chunk/is_stderr` 字段；所有新服务端 output event 已填充 `transcript`，CLI 已切换为优先使用统一 transcript。
    - 本阶段没有把 `run --command` 或 `ExecStream` 切到 guest `agent-compose-runtime exec`；该执行路径切换按 8.2 完成。
    - 没有新增 `ExecInteractive`、WebSocket、TTY、PTY、resize 或运行中 stdin。
  - 下一目标：8.2。

- [x] 8.2 让 `run --command` 和 `ExecStream` 通过 guest `agent-compose-runtime exec` 执行

  依赖：8.1。

  工作内容：
  - `executeProjectRunCommand` 不再直接 `bash -lc` 执行用户命令并由 host 拼 artifacts。
  - 为每个 command run 写入 runtime command request JSON，调用 guest `agent-compose-runtime exec`。
  - 将 runtime stream chunk 转换为 `TranscriptEvent`，同时 append 到 `project_run.logs_path`。
  - 保持 `ProjectRun` 的 `output`、`result_json`、`artifacts_dir`、`logs_path` 和 exit code 语义。
  - `ExecStream` 继续一次性提交 `ExecRequest`，服务端解析 target、校验 session running。
  - 为每个 exec 创建 `<session>/state/exec/<exec_id>/` artifact dir 和 request file。
  - 调用 guest `agent-compose-runtime exec`，返回 transcript stream 和最终 `ExecResult`。
  - `exec` 不创建 `ProjectRun`；如需要 run 审计，用户使用 `run --command`。

  可并行子任务：
  - 可并行：run command fake runtime 测试。
  - 可并行：ExecStream target resolve/stopped sandbox failed precondition 测试。
  - 可并行：exec artifact dir 和 final result 测试。
  - 可并行：logs_path append 与阶段 4 行为回归测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/execution`
  - `task build`

  验收标准：
  - `run --command` 和 `exec <sandbox>` 的执行都由 JS runtime command transcript 驱动。
  - `ExecStream` 仍是 server streaming；没有新增 `ExecInteractive`、WebSocket 或 bidi stream。
  - command artifacts 由 JS runtime 生成，host 不重复覆盖 `command-result.json`。

  完成总结：
  - 状态：已完成。
  - 变更：
    - 新增通用 runtime command request/spec helper，保留 loader command 兼容入口；`run --command` 和 `ExecStream` 均写入 `command-request.json` 后调用 guest `agent-compose-runtime exec`。
    - `run --command` artifact dir 仍为 `<session>/state/runs/<run_id>/`，guest artifact dir 为 `${GuestStateRoot}/runs/<run_id>`；`ProjectRun.output`、`result_json`、`artifacts_dir` 和 exit code 继续来自 runtime `__COMMAND_RESULT__`。
    - `ExecStream` 为每次 exec 创建 `<session>/state/exec/<exec_id>/`，写入 request file，流式返回 runtime transcript，并把最终 runtime command result 映射为 `ExecResult`。
    - run/exec transcript stream 追加到 `transcript.txt`；raw `stdout.txt`、`stderr.txt`、`output.txt` 和 `command-result.json` 由 guest runtime 生成，host 仅在测试/缺失场景镜像 raw artifacts，不覆盖 `command-result.json`。
    - 补充 run command request/output artifact、transcript log、runtime wrapper spec 测试，以及 ExecStream request/output artifact 和 wrapper spec 测试。
  - 验证：
    - `go test ./pkg/execution ./pkg/runs ./pkg/agentcompose/api`：通过。
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs ./pkg/execution`：通过。
    - `task build`：通过。
  - 审计与例外：
    - `ExecStream` 仍是一次性 server streaming；没有新增 `ExecInteractive`、WebSocket、bidi stream、TTY、PTY、resize 或运行中 stdin。
    - `exec` 不创建 `ProjectRun`；需要 run 审计时仍使用 `run --command`。
    - 为避免污染 raw command `output.txt`，实时 follow 使用 `transcript.txt` 作为 `logs_path`，而 `ProjectRun.output` 保持 runtime command result 的 raw output。
  - 下一目标：8.3。

- [ ] 8.3 统一 CLI command transcript 输出

  依赖：8.2。

  工作内容：
  - `run --command` 和 `exec` 共用 transcript 打印 helper。
  - 文本模式按 transcript 原样输出。
  - JSON 模式输出最终 result/detail，不打印流式 transcript，不污染 stdout。
  - 不实现 `exec -i`；如后续提供 CLI sugar，也必须实现为“每轮一次 `ExecStream`/`RunAgentStream`”，不是运行中 stdin。

  可并行子任务：
  - 可并行：CLI transcript 打印 helper 单测。
  - 可并行：JSON 模式 stdout/stderr 分离测试。
  - 可并行：`exec -i` unsupported/usage 文案测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app`
  - `task build`

  验收标准：
  - `run --command` 和 `exec <sandbox>` 输出呈现一致。
  - JSON 模式机器可读且无流式 transcript 污染。
  - 普通 prompt run、command run 和 exec 行为不退化。

  完成总结：
  - 状态：待完成。
  - 变更：待记录。
  - 验证：待记录。
  - 审计与例外：待记录。
  - 下一目标：9.1。

## 阶段 9：`run -i` prompt/command REPL

- [ ] 9.1 实现 CLI `run -i` 参数互斥、REPL loop 和 session 复用

  依赖：2.1、4.2、8.3。

  工作内容：
  - 新增 `-i/--interactive` bool。
  - `run -i --prompt` 进入 prompt REPL；`--prompt <text>` 可作为第一轮 prompt。为支持无值 `--prompt`，按需设置 pflag `NoOptDefVal`。
  - `run -i --command` 进入 command REPL；`--command <cmd>` 可作为第一轮 command。`-i` 场景下允许 `--command` 无值作为模式标记。
  - 未显式 `--prompt` 或 `--command` 时，如果 `-i` 已指定，返回 usage error。
  - `run -i` 与 `--trigger`、`-d` 互斥；prompt 模式与 command 模式互斥。
  - 空输入不创建 run；`/exit` 和 Ctrl+D 退出。
  - Ctrl+C best-effort cancel 当前轮等待并请求 `StopRun`；不承诺 provider adapter 或 command 子进程强中断。
  - REPL 启动时创建或解析一个 session/sandbox；每轮调用 `RunAgentStream` 并传入同一个 `session_id`。
  - 每轮使用 keep-running cleanup policy，并生成独立 `ProjectRun`。
  - REPL 默认保留 sandbox；用户显式 `--rm` 时退出后清理本 REPL 创建的 sandbox。

  可并行子任务：
  - 可并行：CLI REPL input loop 测试。
  - 可并行：CLI 参数互斥和 `--prompt`/`--command` 无值模式测试。
  - 可并行：integration 测试同一 session 连续两轮 run。
  - 可并行：`--rm` 退出后清理语义测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
  - `task build`

  验收标准：
  - 一条用户输入对应一条 run。
  - REPL 生命周期内 workspace/session 连续。
  - 每轮 `logs_path` 可独立查看。
  - 不依赖 TTY、stdin 透传或 terminal resize。

  完成总结：
  - 状态：待完成。
  - 变更：待记录。
  - 验证：待记录。
  - 审计与例外：待记录。
  - 下一目标：9.2。

- [ ] 9.2 实现 prompt REPL provider 复用和 unsupported 语义

  依赖：9.1。

  工作内容：
  - 每轮把用户输入作为 `RunAgentRequest.prompt`。
  - 未指定 provider 时使用 agent definition provider。
  - Codex、Claude/cc、OpenCode 允许 interactive prompt。
  - Gemini 返回 unsupported，并列出支持 provider。
  - runtime/provider 测试确认 provider session file 复用。
  - `state/agents/providers/<provider>.json` 在连续两轮后仍是同一 conversation/session。
  - 不新增 durable conversation resource；审计以每轮 `ProjectRun` 为准。

  可并行子任务：
  - 可并行：Codex runner session reuse 测试。
  - 可并行：Claude/cc runner session reuse 测试。
  - 可并行：OpenCode runner session reuse 测试。
  - 可并行：Gemini unsupported 测试。
  - 可并行：CLI provider unsupported 错误输出测试。

  测试方案：
  - `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
  - `task build`

  验收标准：
  - `run -i --prompt` 可对 Codex、Claude/cc、OpenCode 完成连续多轮交互。
  - provider session 文件可证明复用。
  - Gemini 明确 unsupported。

  完成总结：
  - 状态：待完成。
  - 变更：待记录。
  - 验证：待记录。
  - 审计与例外：待记录。
  - 下一目标：9.3。

- [ ] 9.3 实现 command REPL 复用 workspace 和 JS runtime transcript

  依赖：9.1、8.3。

  工作内容：
  - 每轮把用户输入作为 `RunAgentRequest.command`。
  - 使用阶段 8 的 JS runtime command transcript 和 artifacts。
  - command REPL 连续两轮复用同一 session/workspace/home/state。
  - 每轮生成独立 `ProjectRun`、独立 `logs_path` 和 command artifacts。
  - 不支持运行中 stdin；需要 stdin 的程序应改成一次性命令或脚本输入。

  可并行子任务：
  - 可并行：command REPL 连续两轮 session/workspace 复用测试。
  - 可并行：每轮独立 `ProjectRun` 和 `logs_path` 测试。
  - 可并行：需要 stdin 的命令 usage/unsupported 文案测试。

  测试方案：
  - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runs`
  - `cd runtime/javascript && TEST_SHAPE=unit npm run test:unit`
  - `task build`

  验收标准：
  - `run -i --command` 可连续执行多轮 command。
  - 每轮有独立 run 记录、日志和 artifacts。
  - 输出由 JS runtime transcript 驱动。

  完成总结：
  - 状态：待完成。
  - 变更：待记录。
  - 验证：待记录。
  - 审计与例外：待记录。
  - 下一目标：10.1。

## 阶段 10：文档和最终质量门禁

- [ ] 10.1 同步 CLI 手册、旧设计说明和完整质量门禁

  依赖：1.1、2.1、3.1、4.2、5.1、6.2、7.2、8.3、9.3。

  工作内容：
  - 更新 `docs/zh-CN/command-line-manual.md`：
    - 标记或移除 `build`、`push` 已实现暗示，说明后续单独设计。
    - 移除 `up` attach/detach 描述。
    - 补齐 OCI image `pull`、`run --rm`、`run --trigger`、`logs --follow`、`stats`、Jupyter proxy expose、`run -d`/`StopRun` 统一取消、`run -i` prompt/command REPL、`exec <sandbox>` command transcript 的最终语义。
    - 明确不提供 TTY、PTY、WebSocket TTY、terminal resize 或运行中 stdin 透传。
    - 写明 stats 缺失字段按 unknown/null/`-` 表达，只有无稳定指标入口时才 unsupported。
  - 如 `docs/zh-CN/design/agent-compose-cli-improvement-plan.md` 仍含旧决策，追加 supersede 说明或同步修正。
  - 确认 Go generated code、TS proto-client、runtime JS 相关产物无遗漏。
  - 运行最终质量门禁并记录结果。

  可并行子任务：
  - 可并行：文档一致性审计。
  - 可并行：proto-client 生成产物审计。
  - 可并行：runtime JS/package 验证。
  - 可并行：Go lint/build/test 门禁。

  测试方案：
  - `task lint`
  - `task build`
  - `task test`
  - `cd proto-client && npm ci && npm run gen && npm run build`
  - `cd runtime/javascript && npm ci && npm run test:unit`
  - 如涉及 runtime SDK：`cd runtime/agent-compose-runtime-sdk && npm ci && npm test && npm run test:packaging`

  验收标准：
  - 手册与实现一致。
  - `build`、`push` 不作为本轮已实现命令出现。
  - `up` 不再有 attach/detach 语义。
  - CI 对应 Go、coverage、runtime、proto-client 任务可通过。

  完成总结：
  - 状态：待完成。
  - 变更：待记录。
  - 验证：待记录。
  - 审计与例外：待记录。
  - 下一目标：无。

## 风险和停止条件

- 如果现有 executor 无法在 agent run 过程中增量写入 cell artifact，阶段 4 必须先在 stream sink 增加 run log append；不能退回轮询 `RunDetail.output`。
- 如果 trigger prompt/template 解析需要 scheduler runtime 执行脚本才能得到 prompt，阶段 3 必须停止并补充 trigger payload/template 设计；不得猜测执行结果。
- 如果 OCI image backend 无法区分“已拉取的 OCI image”和 BoxLite/MicroSandbox materialized runtime artifact，阶段 1 必须先补充 image domain metadata；不得把 `pull` 退回 runtime driver 语义。
- 如果 Jupyter proxy/expose 当前由全局配置强绑定，阶段 6 必须先把 resolved session Jupyter config 写入 session/runtime state；不得用全局变量模拟 agent 级配置。
- 如果 Jupyter expose 需要 runtime driver host port mapping 才能工作，阶段 6 必须先改成通过 agent-compose proxy 暴露；不得为 Docker、BoxLite、MicroSandbox 增加分叉 host expose 行为。
- 如果后台 run supervisor 无法在 daemon 内持有 cancel handle，阶段 7 必须先补齐 in-memory run registry；不得实现成 CLI fork 后台进程。
- 如果某个 runtime 的 `ExecStream(ctx)` 或 prompt 执行路径忽略 context cancellation，阶段 7 必须记录 best-effort 限制并补齐 service 侧 cancel requested/terminal 状态处理；不得暴露 driver-specific `StopRun` 语义。
- 如果 `agent-compose-runtime exec` 无法在运行过程中稳定写入 command artifacts 和 transcript，阶段 8 必须先修复 JS runtime command 流式归档；不得回退到 host 侧重复拼写 `command-result.json`。
- 如果 provider runner 不能稳定复用 session 文件，阶段 9 只允许对可证明复用的 provider 开启 prompt REPL，其他 provider 返回 unsupported。
