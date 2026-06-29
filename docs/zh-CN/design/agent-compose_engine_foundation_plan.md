# agent-compose 引擎基础能力建设计划

英文版：[../../design/agent-compose_engine_foundation_plan.md](../../design/agent-compose_engine_foundation_plan.md)

本文档定义 agent-compose 面向最终形态的生产级工程建设计划。目标是把
agent-compose 建设成开源、业务无关、可被上层平台稳定集成的 agent/service
执行引擎。

本文不是从当前实现出发的迁移计划，而是最终目标态下的任务分解。它用于给维护者和
多个并行开发 agent 提供清晰的模块边界、依赖关系、交付物和验收标准。

相关现有文档：

- [总体设计](agent-compose_design.md)
- [Runtime JavaScript contract](agent-compose-runtime-js_contract.md)
- [Runtime 环境变量](runtime_environment_variables_design.md)
- [Runtime mount manifest](runtime_mount_manifest_design.md)
- [Webhook design](webhook_design.md)
- [Runtime LLM Facade](agent-compose-runtime-llm-facade.md)
- [OctoBus integration](octobus_integration.md)

## 1. 目标定位

agent-compose 应定位为业务无关的 runtime engine：

```text
project manifest -> validation/revision -> service/agent/run invocation
  -> isolated runtime -> logs/artifacts/events/metrics -> stable API/SDK
```

引擎负责通用运行基础设施：

- project manifest 校验、应用、diff、revision 和 rollback
- agent provider profile
- 带 input/output schema 的 service entry
- trigger、webhook、event 和 scheduler dispatch
- runtime context 透传和注入
- 隔离 runtime 执行
- run 记录、日志、artifact、event、metric 和 state
- runtime SDK 与 host/runtime 协议
- capability gateway 作为通用扩展点

引擎不负责企业产品概念，例如 tenant、channel binding、组织身份解析、审批流、计费、
市场化包装和产品专属权限策略。上层平台可以把自己的业务 contract 投影成
agent-compose manifest，并通过 runtime context metadata 透传业务上下文。

## 2. 最终概念模型

### 2.1 Project Manifest

Project manifest 是声明式根对象，描述期望运行状态，不表示一次执行。

目标顶层结构：

- `apiVersion` / `kind`
- `metadata`：name、labels、annotations
- `variables`：project 环境变量和 secret reference
- `runtime`：默认 driver、image、env、resources、network、cleanup
- `workspace`：默认 workspace source
- `agents`：AI provider profiles
- `services`：可执行、业务无关的入口
- `triggers`：manual/API/cron/interval/webhook/event 触发器
- `permissions`：通用 capability 和 resource scope
- `artifacts`：artifact retention 和 storage policy

### 2.2 Agent

Agent 是 AI provider profile，描述 provider、model、system prompt、runtime 默认值、
workspace 默认值和 capability scope。Agent 不等同于完整业务服务。

### 2.3 Service Entry

Service entry 是主要可复用执行目标。它具备稳定名称、input schema、output schema、
实现入口文件、runtime 要求、权限、超时、重试策略和示例。

上层平台的产品工作流应优先调用 service entry。直接 agent run 仍作为较低层的引擎能力保留。

### 2.4 Trigger

Trigger 描述什么时候调用目标。目标可以是 service entry 或 agent profile，并可配置 input
映射。Trigger 配置必须与业务实现代码分离。

### 2.5 Runtime Context

Runtime context 是通用上下文信封，承载 source、request id、trace id、metadata、env
override、identity metadata 和 capability scope。引擎只负责存储、审计、注入和转发，
不解释产品专属 key。

### 2.6 Run

Run 是 service、agent、exec、trigger、webhook 执行的统一记录。它记录 input、output、
error、context、status、logs、artifacts、metrics、timeline、project revision、runtime
driver 和 image。

## 3. 目标 API 面

v2 API 应作为最终公开引擎 API。v1 仅作为兼容 API 保留。

### 3.1 ProjectService

目标方法：

- `ValidateProject`
- `ApplyProject`
- `GetProject`
- `ListProjects`
- `RemoveProject`
- `DiffProject`
- `ListProjectRevisions`
- `RollbackProjectRevision`
- `WatchProject`

### 3.2 RunService

目标方法：

- `InvokeService`
- `InvokeServiceStream`
- `RunAgent`
- `RunAgentStream`
- `GetRun`
- `ListRuns`
- `StopRun`
- `WatchRun`

`InvokeService` 应成为上层平台的主集成路径。`RunAgent` 作为较低层能力继续保留。

### 3.3 ArtifactService

目标方法：

- `ListArtifacts`
- `GetArtifact`
- `ReadArtifact`
- `WriteArtifact`
- `DeleteArtifact`

### 3.4 EventService

目标方法：

- `PublishEvent`
- `ListEvents`
- `WatchEvents`

### 3.5 CapabilityService

现有 capability 能力应收敛为通用 gateway contract。OctoBus 是一个实现，不是唯一概念模型。

## 4. 正交任务拆分

以下 workstream 按低耦合并行开发设计。每条线都应包含测试和文档更新。

### W0. 协议与兼容治理

负责范围：

- `proto/agentcompose/v2/`
- `proto-client/`
- 生成的 Go 和 TypeScript client
- 文档中的 API 兼容说明

交付物：

- manifest、service entry、runtime context、unified run、artifact、event、revision 的最终 v2 proto。
- 保持业务无关的字段命名规则。
- 代码生成工作流和 client package 兼容说明。
- API 层 JSON/Connect 字段兼容测试。

验收标准：

- 新 API 可表达 service invocation、runtime context、capability scope、artifact 和 project revision，且不依赖 v1-only 字段。
- v2 proto 中不把 tenant、channel、corp user、ADP 等产品术语作为一等引擎概念。

并行关系：

- 该线应先定义 proto，再解锁 W1、W3、W4、W5、W6。

### W1. Manifest 模型、解析、规范化与 Schema

负责范围：

- `pkg/compose/`
- `cmd/agent-compose/` 中的 CLI config/up 路径
- `pkg/agentcompose/` 中的 project validation 路径
- manifest 文档和示例

交付物：

- metadata、runtime、agents、services、triggers、permissions、artifacts、variables、workspace、network 目标结构。
- 严格 parser 和 normalizer，提供稳定 canonical JSON hash。
- manifest JSON Schema。
- prompt、service entry 文件和 schema 文件引用模型。
- 带稳定 field path 的校验错误。

验收标准：

- 本地 CLI 校验和 daemon 校验使用同一 normalizer。
- 空值和默认值产生确定性 spec hash。
- 非法引用、非法 service schema、重复 target、非法 trigger target 返回字段路径化错误。

并行关系：

- W0 定义 wire shape 后可推进；可与 W2、W7 并行。

### W2. Project Store、Revision、Diff 与 Rollback

负责范围：

- `pkg/agentcompose/project_schema.go`
- `pkg/agentcompose/project_store.go`
- v2 ProjectService handlers

交付物：

- normalized manifest revision store。
- 基于 spec hash 的 apply 幂等。
- current/applied/incoming spec 之间的 diff response。
- 按 revision rollback。
- runtime resource 与 history 的 remove 语义。

验收标准：

- 重复 apply 相同 manifest 不创建重复 revision。
- rollback 通过创建新的 current revision 回到旧期望状态。
- diff 可报告 project、agent、service、trigger、permission、runtime 层面的 created/updated/removed/unchanged。

并行关系：

- 依赖 W1 normalized model。W0 稳定核心 message 后，store 内部可用临时 DTO 先行。

### W3. Runtime Context 与 Capability Scope

负责范围：

- v2 run/invoke proto messages
- run coordinator request structs
- session tags/env injection
- capability proxy 配置
- runtime environment 文档

交付物：

- 通用 `RuntimeContext`：source、client request id、trace id、external run id、metadata、env、identity metadata、capability scope。
- `CapabilityScope`：capset ids 和通用 metadata。
- 注入 session metadata、runtime env、SDK context、logs、run store 的一致路径。
- reserved env/provider credential 过滤规则。

验收标准：

- 上层平台可以传企业 metadata，但无需引擎内置业务字段。
- context 在 run detail 可见，并可在 runtime SDK 内读取。
- capability metadata 可转发给 capability gateway，agent-compose 不解释业务含义。

并行关系：

- W0 后可开始，需要与 W4、W5、W8、W10 紧密协同。

### W4. 统一 Run Store 与生命周期

负责范围：

- `pkg/agentcompose/project_run*`
- RunService handlers
- stream event mapping
- run query 和 stop 逻辑

交付物：

- 统一 run target model：service、agent、exec、trigger、webhook。
- 持久化 input JSON、output JSON、error object、runtime context、metrics、artifacts、logs、timeline、driver、image、cleanup status。
- 稳定 stream event envelope。
- 按 project、target type/name、source、status、scheduler、trigger、request id、时间范围分页过滤。

验收标准：

- `GetRun` 足以还原调用目标、上下文、执行结果和 artifact。
- success、failure、cancellation、startup failure、validation failure、timeout、cleanup failure 都有终态记录。

并行关系：

- 依赖 W0。可与 W5 并行，但需先约定 service run target 字段。

### W5. Service Entry Invocation Engine

负责范围：

- manifest service model
- 新 service invocation coordinator
- host/runtime request files
- service result envelope
- v2 RunService `InvokeService*`

交付物：

- 从 project revision 解析 service entry。
- 使用 `inputSchema` 校验 JSON input。
- 在 runtime 执行 entry file。
- 使用 `outputSchema` 校验 output。
- 标准 result envelope：output、error、artifacts、logs、metrics。
- streaming invocation 路径。

验收标准：

- manifest 定义的 service 可被调用，不需要直接调用 agent prompt。
- 非法 input 尽量在 runtime 启动前失败。
- 非法 output 以结构化 run failure 返回。
- service code 可通过 SDK 调用 agent、LLM、exec、capability、state、artifact、log、event。

并行关系：

- 依赖 W0、W1、W3、W4、W8 contract。完整 SDK 前可先用最小 runtime command 落地。

### W6. Trigger、Scheduler、Webhook 与 Event Targeting

负责范围：

- `pkg/agentcompose/loader_*`
- webhook/event HTTP routes
- manifest trigger model
- scheduler validation 和 generated loader scripts

交付物：

- service 和 agent target model。
- 从静态配置和 event/webhook payload 映射 input。
- 声明式 cron、interval、timeout、event、webhook trigger。
- Loader JS 保留为高级编排能力，并明确边界。
- trigger run 接入统一 run model。

验收标准：

- 声明式 trigger 可以调用 service entry。
- webhook/event payload 可以映射为 service input。
- Loader JS 不再是长耗时业务逻辑的推荐承载层。

并行关系：

- 依赖 W1 target model 和 W5 invocation。如果只改 ingress persistence，webhook queue 加固可并行。

### W7. Runtime Host/Guest 协议

负责范围：

- `runtime/javascript/`
- `pkg/agentcompose/exec.go`
- runtime contract 文档
- guest image Dockerfile

交付物：

- service entry 执行 runtime command。
- request file protocol，承载 service input、runtime context、schemas、workspace、state paths、artifact paths。
- response protocol，承载标准 result envelope。
- exit code 和 error mapping contract。
- prompt 和 exec command 保持兼容，service command 成为 service-entry 主路径。

验收标准：

- host 不通过 ad hoc stdout 解析 service result；结果 payload 结构化且有版本。
- runtime command 可脱离完整 daemon 做 contract test。
- guest image 包含匹配版本的 runtime 和 SDK。

并行关系：

- W5 定义 service request/response envelope 后，可与 W8 并行。

### W8. Runtime SDK 生产化

负责范围：

- `runtime/agent-compose-runtime-sdk/`
- SDK 文档和示例
- SDK 测试与 mock runtime

交付物：

- 稳定 SDK 模块：context、log、agent、llm、exec、shell、service、state、artifact、event、secret、capability。
- runtime context、service input/output、artifact、capability call 的 TypeScript 类型。
- 本地测试 mock runtime。
- dry-run helper 和 schema validation helper。
- 基于 runtime request/response fixture 的 contract tests。

验收标准：

- service code 可在没有 live daemon 的情况下本地编写和测试。
- SDK 不把 daemon 私有路径作为主要 API 暴露。
- SDK 示例与 manifest 示例一致。

并行关系：

- 可与 W7 并行。依赖 W3 context 和 W5 result contract。

### W9. Artifact、Log、State、Event 与 Metrics Services

负责范围：

- session state layout
- run store
- 新 v2 artifact/event services
- runtime SDK host endpoints

交付物：

- artifact registry：metadata、path、content type、size、digest、run association。
- structured log timeline 和 stdout/stderr stream mapping。
- 按 project/service/session 设计的持久 state namespace。
- event timeline 和 publish/watch API。
- 基础 run metrics：duration、exit code、output size、artifact count、runtime startup time。

验收标准：

- artifact 可通过 API 发现，而不是只能靠 filesystem path。
- run timeline 可解释生命周期状态变化和错误。
- service entry 内 SDK artifact/state/event 调用可用。

并行关系：

- 可分片开发。artifact registry 需与 W4、W5 协调；event API 需与 W6 协调。

### W10. Security、Secrets 与策略边界

负责范围：

- config/env filtering
- runtime env injection
- capability proxy metadata
- security docs
- credential leakage tests

交付物：

- reserved env/provider key 过滤策略。
- manifest 和 runtime context 中的 secret reference model。
- runtime context 在 logs/API response 中的脱敏规则。
- 保持实现无关的 capability permission check hooks。
- service entry 执行的 threat model 更新。

验收标准：

- provider key 不进入 guest runtime，除非通过预期 facade 或 scoped token。
- manifest/run/artifact API 不返回 secret value。
- 测试覆盖脱敏、reserved env filtering、context serialization。

并行关系：

- 横切任务。发布前应 review W1、W3、W5、W8、W9 的改动。

### W11. CLI 与开发者体验

负责范围：

- `cmd/agent-compose/`
- examples
- docs README files

交付物：

- `config` 支持新 manifest schema 和 file refs。
- `up` 支持 validation、apply、diff preview、revision 展示。
- `run` 或新增 `invoke` 支持 service invocation 和 JSON input。
- `logs`、`inspect`、`ps` 理解 unified runs 和 services。
- service entry、webhook trigger、scheduler trigger、capability usage 示例 project。

验收标准：

- 开发者不依赖上层平台，也能 author、validate、apply、invoke、inspect、debug 一个 service project。
- CLI 输出脱敏 secret，并展示字段路径化校验错误。

并行关系：

- 完整行为依赖 W1、W4、W5。CLI help/docs 可先用 stub 并行准备。

### W12. 测试策略与发布门禁

负责范围：

- `TESTING.md`
- unit/integration/e2e tests
- CI task definitions
- release notes

交付物：

- parser、proto conversion、store migration、invocation、runtime contract、SDK、driver behavior、scheduler/webhook、artifacts、security redaction 的测试矩阵。
- golden manifest fixtures。
- runtime contract fixtures。
- 已保留 agent run 行为的兼容测试。
- repeated invocation 的性能与可靠性 smoke tests。

验收标准：

- `task lint`、`task build`、`task test` 仍是主门禁。
- 新 service-entry 功能具备 unit、integration 和至少一个 end-to-end smoke test。
- store migration 覆盖空数据库和代表性旧 schema。

并行关系：

- 应尽早开始定义 fixture 和门禁；其他 workstream 各自补测试。

## 5. 建议并行推进分组

### Group A：Contract And Manifest

- W0 协议与兼容治理
- W1 Manifest 模型、解析、规范化与 Schema
- W2 Project Store、Revision、Diff 与 Rollback

该组定义稳定期望状态 contract。

### Group B：Invocation Core

- W3 Runtime Context 与 Capability Scope
- W4 统一 Run Store 与生命周期
- W5 Service Entry Invocation Engine

该组定义 runtime invocation plane。

### Group C：Runtime And SDK

- W7 Runtime Host/Guest 协议
- W8 Runtime SDK 生产化
- W10 Security、Secrets 与策略边界

该组定义 sandbox 内代码可依赖的能力。

### Group D：Automation And Observability

- W6 Trigger、Scheduler、Webhook 与 Event Targeting
- W9 Artifact、Log、State、Event 与 Metrics Services

该组让 run 可自动化、可观测、可审计。

### Group E：Productized Engine UX

- W11 CLI 与开发者体验
- W12 测试策略与发布门禁

该组把引擎变成可用、可发布的开发者产品。

## 6. 面向上层平台的集成契约

上层平台最终应只依赖这些引擎表面：

```text
business contract -> project manifest projection
  -> ValidateProject
  -> ApplyProject
  -> InvokeService / InvokeServiceStream
  -> WatchRun / GetRun / ListRuns
  -> ListArtifacts / ReadArtifact
```

上层平台不应依赖：

- daemon 私有数据库表
- session 目录内部结构
- loader 实现细节
- provider 专属 prompt 文件路径
- runtime magic stdout payload
- 最终 service invocation 所需的 v1-only session 字段

## 7. 文档更新策略

每个实现 PR 都应更新距离变更 contract 最近的模块文档：

- Manifest/parser 变更：`agent-compose_design.md`，范围变化时同步更新本文档。
- Runtime host/guest 协议：`agent-compose-runtime-js_contract.md`。
- 环境变量注入和脱敏：`runtime_environment_variables_design.md`。
- Mount 或路径约定：`runtime_mount_manifest_design.md` 和 driver-specific 文档。
- Webhook/event 变更：`webhook_design.md`。
- Capability gateway 变更：`octobus_integration.md`；如果 OctoBus 专属表述过重，则新增通用 capability gateway 文档。
- 测试门禁：`TESTING.md`。

设计文档应描述当前已实现行为。未来计划在落地前保留在本文档。

## 8. 第一阶段落地切片

第一个生产级切片应保持窄而端到端：

1. 给 v2 run/invoke contract 增加 runtime context 和 capability scope。
2. Manifest 增加 `services`，先支持一种 JavaScript service entry。
3. 实现 `InvokeService` 和 `InvokeServiceStream`，具备 input/output schema 校验和统一 run 记录。
4. 增加 runtime service command，以及 SDK 的 `context`、`log`、`artifact`、`agent`、`llm`、`capability` 基础能力。
5. 增加 CLI service invocation 和一个文档化示例。
6. 增加 parser、API conversion、run store、runtime contract、SDK mock 和一个 Docker-driver smoke path 测试。

这个切片是证明最终模型的最小版本，同时不会把业务概念推入引擎。
