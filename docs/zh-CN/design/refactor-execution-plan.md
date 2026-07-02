# agent-compose 结构重构执行计划

## 执行原则

本计划基于 `docs/zh-CN/design/refactor-architecture-plan.md` 执行。

本轮重构只调整代码组织形式，不改变对外接口、业务逻辑和运行行为。

硬约束：

- 不直接向 `main` 合并重构代码。
- 在 `main` 上保留本执行计划，作为任务拆分和验收依据。
- 创建专门的重构主分支 `refactor/architecture-main`。
- 所有并行任务分支从 `refactor/architecture-main` 切出。
- 每个 worker 使用独立 git worktree，避免相互污染工作区。
- 每个 worker 只修改自己负责的文件集合，不回滚其他人的改动。
- 当前快速拆解阶段不要求每个任务独立跑测试；worker 必须说明是否跳过测试、修改了哪些文件、哪些路径需要集中修复。
- 等 `refactor-architecture-plan.md` 中定义的结构迁移任务整体完成后，再集中进行编译、测试、路径/import 修复和行为回归。

## 分支与 Worktree 策略

主工作区：

```text
/data/src/github.com/kingfs/agent-compose
branch: main
用途: 保存方案文档和执行计划，不直接承载重构代码。
```

重构主 worktree：

```text
/data/src/github.com/kingfs/agent-compose-refactor-main
branch: refactor/architecture-main
用途: 集成所有重构任务，作为重构主线。
```

worker worktree 命名：

```text
/data/src/github.com/kingfs/agent-compose-wt-bootstrap
/data/src/github.com/kingfs/agent-compose-wt-domain
/data/src/github.com/kingfs/agent-compose-wt-transport
/data/src/github.com/kingfs/agent-compose-wt-persistence
/data/src/github.com/kingfs/agent-compose-wt-loader
/data/src/github.com/kingfs/agent-compose-wt-project
```

worker 分支命名：

```text
refactor/bootstrap-shell
refactor/domain-models
refactor/transport-handlers
refactor/domain-image
refactor/domain-dashboard
refactor/domain-capability
refactor/domain-project
```

## 当前集成状态

截至 `refactor/architecture-main` 当前进度：

- T0 已完成：架构方案、中文方案、执行计划已保留在 `main`，重构代码未合入 `main`。
- T1 已完成：已建立 bootstrap 兼容外壳，`agentcompose.Setup/Register/StartBackground` 保持兼容。
- T2 已部分完成：已拆出主要 Connect handler wrapper，业务逻辑仍委托旧 `Service` 实现。
- T3 已完成第一轮域包种子：
  - `internal/agentcompose/image`：Docker/OCI/Auto image backend、image mapper 等低耦合逻辑已迁入域包。
  - `internal/agentcompose/dashboard`：overview 聚合、状态分类、clone helper 已迁入域包。
  - `internal/agentcompose/capability`：capability provider、gateway settings、session binding、guide path/preamble 已迁入域包。
  - `internal/agentcompose/events`：topic event 模型、状态归一化、topic 校验、payload hash、record normalize、webhook topic match、dispatcher 核心逻辑已迁入域包。
  - `internal/agentcompose/exec`：exec 纯模型、cell 类型规范化、artifact helper、stream accumulator、agent result summarizer、trace event 解析已迁入域包。
  - `internal/agentcompose/workspace`：workspace 模型、file/git config、路径 normalize、file content root、文件列表、复制、tar 解包、git helper 已迁入域包。
- T4 已进入大粒度域迁移阶段：
  - `internal/agentcompose/loader` 已承载 loader 模型、调度规则、cron 解析、bus publish 规则、topic payload helper，以及 LoaderEngine、run executor、event dispatcher 主业务逻辑；`pkg/agentcompose` 保留 Manager/Store/Connect glue 和兼容 wrapper。
  - `internal/agentcompose/project` 已承载 project normalize、schema ensure、SQL store、project-session 查询、proto response mapper、managed scheduler trigger/script build helper；`pkg/agentcompose` 保留 Service 编排、跨域 Session 适配和兼容 wrapper。
  - `internal/agentcompose/run` 已承载 run coordinator、project agent run orchestration、run preparation、exec env/result mapper、run session helper；`pkg/agentcompose` 保留 Connect/Service、ConfigStore、session/runtime glue。
  - `internal/agentcompose/session` 已承载 session 模型、列表过滤、proto option 解析、watch stream broker、reconcile rule、VM start/stop 编排；`pkg/agentcompose` 保留 Connect 映射、workspace/capability/loader/dashboard/ConfigStore glue。
  - `internal/agentcompose/llm` 与 `internal/agentcompose/config` 已承载 LLM client 主逻辑、provider/model/target/token 域类型、runtime env helper、配置 normalize 和 SQLite/time/bool/int helper；`pkg/agentcompose` 保留 ConfigStore SQL/schema 与配置解析 wrapper。
  - `internal/agentcompose/transport/httpapi` 已承载 Connect 注册、HTTP route 注册、Jupyter proxy/rewrite、workspace/webhook/runtime LLM facade route 入口、capability proxy server 构造；`pkg/agentcompose` 保留兼容注册入口和业务 handler glue。
- 当前验证：`refactor/architecture-main` 上 `go test ./pkg/agentcompose ./cmd/agent-compose` 通过，`task build` 通过。
  - 注：上述验证对应进入快速拆解阶段前的集成点。快速拆解阶段的新合入按策略暂未集中测试，后续收口阶段统一执行编译、测试和路径/import 修复。

下一批优先级：

- 继续按“域优先，大步迁移”的方式推进，不再把任务拆成零散 helper。
- `session`、`run`、`config/llm`、`transport/http` 四个大域主干已完成快速迁移并合入 `refactor/architecture-main`。
- loader/project 后续只做收口：把仍留在 `pkg/agentcompose` 的 Store/Manager/Service 适配逐步压薄，避免再扩大同域内的小任务数量。
- 下一步进入剩余 `pkg/agentcompose` 表面积收口：按域清理兼容 wrapper、迁移仍明显属于实现细节的 store/service 切片，并准备集中编译修复。

## 任务依赖图

```text
T0 文档与基线
  -> T1 Bootstrap 兼容外壳
    -> T2 Transport handler 拆分
    -> T3 域包种子
      -> T4 高复杂域迁移
        -> T5 持久化归域
          -> T6 pkg/agentcompose 到 internal/agentcompose
            -> T7 其他 pkg/* 归类
```

并行策略：

- T1 是首个阻塞任务，先由负责人完成或单独 worker 完成。
- T2 与 T3 可以在 T1 合入重构主分支后并行。
- T3 优先选择低耦合域，例如 image、capability、dashboard。
- T4 需要依赖对应领域的 T3 结果，按 project、loader、session、run 分组并行。
- T5 依赖域包 service/repository 边界基本稳定。
- T6 必须在大部分包边界稳定后执行，不提前做全量 import 迁移。
- T7 是最后的清理任务，不与核心拆分并行。

## 任务拆分

### T0：文档与基线

负责人：重构负责人。

范围：

- 保留 `refactor-architecture-plan.md`。
- 新增本执行计划。
- 创建 `refactor/architecture-main`。
- 创建重构主 worktree。
- 记录当前 `main` commit。
- 运行或记录可用的 baseline 命令。

交付：

- `docs/zh-CN/design/refactor-execution-plan.md`
- 重构主分支与 worktree。

验收：

- `main` 不包含重构代码。
- `refactor/architecture-main` 从当前 `main` 创建。
- 工作区状态清楚，无误改用户已有修改。

### T1：Bootstrap 兼容外壳

负责人：bootstrap worker 或重构负责人。

目标：

- 新增 `internal/agentcompose/bootstrap`。
- 将 constructor registration、Connect route registration、HTTP route registration、background startup 从 `pkg/agentcompose/service.go` 分离出来。
- 保留 `pkg/agentcompose.Setup(di)` 作为兼容入口。
- 不移动业务逻辑。

主要文件：

- `pkg/agentcompose/service.go`
- `internal/agentcompose/bootstrap/*`
- `cmd/agent-compose/main.go` 仅在必要时修改 import。

验收：

- Connect route path 不变。
- `agentcompose.Setup(di)` 仍可用。
- `go test ./pkg/agentcompose ./cmd/agent-compose`
- `task build`

### T2：Transport Handler 拆分

负责人：transport worker。

目标：

- 把 Connect handler 从单一 `Service` 上拆为按服务分组的 handler。
- handler 只做 proto/connect 映射和委托。
- 初期可以委托到旧实现，避免同时改 application 逻辑。

建议分批：

- T2.1 v1 session/kernel/agent/config/loader/dashboard/capability handler。
- T2.2 v2 project/run/exec/image handler。
- T2.3 HTTP route：proxy、webhook、workspace、runtime LLM facade。

主要文件：

- `pkg/agentcompose/service.go`
- `pkg/agentcompose/session_*.go`
- `pkg/agentcompose/agent_*.go`
- `pkg/agentcompose/loader_service.go`
- `pkg/agentcompose/project_service.go`
- `pkg/agentcompose/run_service.go`
- `pkg/agentcompose/exec_service.go`
- `pkg/agentcompose/image_service.go`
- `pkg/agentcompose/proxy.go`
- `pkg/agentcompose/webhook*.go`
- `pkg/agentcompose/workspace_routes.go`

验收：

- proto 文件不变。
- route path 不变。
- handler 中不新增业务逻辑。
- `go test ./pkg/agentcompose`

### T3：域包种子

负责人：domain worker。

目标：

- 建立真正的 `internal/agentcompose/<domain>` 域包，而不是继续只做同 package 文件拆分。
- 优先从低耦合域开始，例如 `image`、`capability`、`dashboard`。
- 域包可以包含 model、service、repository interface、adapter/mapper，但文件职责必须清楚。
- 纯模型/规则文件不得 import proto、connect、echo、SQL、driver。

建议分批：

- T3.1 `image` 域包：image service、backend selection、ensure policy。
- T3.2 `capability` 域包：capability service/gateway/config。
- T3.3 `dashboard` 域包：overview aggregator/hub。
- T3.4 `events` 域包：topic event model/store/dispatcher。

主要文件：

- `pkg/agentcompose/image_*.go`
- `pkg/agentcompose/capability_*.go`
- `pkg/agentcompose/dashboard_overview.go`
- `pkg/agentcompose/event_dispatcher.go`
- `pkg/agentcompose/topic_event_*.go`

验收：

- 新域包边界清楚。
- transport 通过域 service 或 mapper 调用，不直接持有业务逻辑。
- 现有行为测试通过。
- `go test ./pkg/agentcompose`

### T4：高复杂域迁移

负责人：按领域拆多个 worker。

目标：

- 将 `project`、`loader`、`session`、`run` 等复杂域按完整业务域迁入对应域包。
- 每个 worker 负责一个完整域的主干迁移，而不是只移动少量 helper。
- 优先形成清晰的 domain package，再通过 `pkg/agentcompose` 兼容 wrapper 保持外部接口不变。
- 行为保持不变。

建议分组：

- T4.1 `project`：schema/store/session relation/response mapper/managed resource builder，已完成主干迁移；后续只做 Service 编排压薄。
- T4.2 `loader`：engine/executor/event dispatcher，已完成核心迁移；后续只做 Manager/Store/Service 收口。
- T4.3 `session`：create/resume/stop/watch/reconcile/stream/session store，以生命周期聚合为边界大步迁移。
- T4.4 `run`：run coordinator、run preparation、project agent run、exec orchestration，以执行编排为边界大步迁移。
- T4.5 `llm`、`config`、`workspace`：仅在 session/run 边界稳定后处理，避免过早触碰横切配置。

验收：

- transport 只做映射和委托。
- 域 service 不 import connect/echo。
- 用例测试仍通过。
- `go test ./pkg/agentcompose`

### T5：持久化归域

负责人：persistence worker。

目标：

- 把 SQL store 按业务域归入对应域包。
- 不改 DB schema、table name、migration、JSON encoding、稳定 ID。

建议分组：

- session repository。
- project repository。
- loader repository。
- config/workspace/agent definition/capability repository。
- topic event repository。

主要文件：

- `pkg/agentcompose/store.go`
- `pkg/agentcompose/config_store.go`
- `pkg/agentcompose/project_store.go`
- `pkg/agentcompose/loader_store.go`
- `pkg/agentcompose/topic_event_store.go`

验收：

- migration 测试通过。
- 临时 DB 集成测试通过。
- 现有 data 目录兼容性不破坏。
- `go test ./pkg/agentcompose`

### T6：迁移到 `internal/agentcompose`

负责人：重构负责人。

目标：

- 将稳定后的 agent-compose daemon 实现移动到 `internal/agentcompose`。
- 更新 `cmd/agent-compose` import。
- 删除或保留极薄的 `pkg/agentcompose` deprecated wrapper。

验收：

- daemon 能正常 build。
- 外部协议、CLI、runtime 行为不变。
- `task build`
- `task test`

### T7：其他 `pkg/*` 归类

负责人：重构负责人。

目标：

- 逐包判断 `pkg/compose`、`pkg/capability`、`pkg/imagecache`、`pkg/driver`、`pkg/auth`、`pkg/config`、`pkg/health`、`pkg/fxgo` 是否应留在 `pkg`。
- 只移动明确是实现细节的包。

验收：

- 对外 Go library surface 有明确说明。
- internal/package 边界符合文档。
- `task build`
- `task test`

## 冲突控制

优先避免多个 worker 同时修改同一热点文件：

- `service.go`：T1/T2 顺序执行，T1 合入后 T2 再切分支。
- `project_service.go`：T2 只拆 handler 外壳，T4.1 再抽 application，避免并发修改。
- `loader_manager.go` 与 `loader_engine.go`：归 loader worker 独占。
- `project_store.go`：T3 只抽模型，T5 再拆 repository。
- `config_store.go`：T5 独占。

负责人集成规则：

- 每个 worker 完成后，负责人先在 worker worktree review diff。
- 负责人将 worker 分支 merge 到 `refactor/architecture-main`。
- 发生冲突时，负责人解决冲突，不要求 worker 自行 rebase 其他 worker 的改动。
- 快速拆解阶段冲突解决后不强制立即运行测试；只做必要的结构审查和明显 import/path 修正。
- 集中收口阶段再统一运行 `go test ./pkg/agentcompose ./cmd/agent-compose`、`task build`、`task test`，并修复编译与行为回归。

## Worker 交付格式

每个 worker 完成时必须汇报：

```text
分支：
worktree：
任务：
行为是否保持不变：
修改文件：
测试命令：
测试结果：
风险/待负责人确认：
```

快速拆解阶段允许 `测试命令` 填写为“未运行”，但必须列出预计需要集中修复的风险点。

## 第一批执行安排

第一批只启动低冲突任务：

1. 负责人完成 T0。
2. Bootstrap worker 执行 T1。
3. Domain worker 先做只读准备，等待 T1 合入后开始 T3.1/T3.2。
4. Transport worker 先做 route/handler 清单核对，等待 T1 合入后开始 T2。

第一批不同时修改 `project_service.go` 和 `loader_manager.go`，避免过早冲突。

## 第一批调研结论

### Domain 第一批边界

Domain 第一批只抽纯规则、常量和稳定 ID helper，不搬 SQL/proto DTO。

优先移动：

- Project run status/source 常量。
- Project run 状态 normalize 与 transition 规则。
- Project 稳定 ID 生成 helper。
- Loader runtime/trigger/session policy/concurrency policy/run status 常量。
- Loader 规则 helper，例如 trigger kind normalize、source hash、topic match、schedule 判断。

暂缓移动：

- `ProjectRecord`、`ProjectRevisionRecord`、`ProjectAgentRecord`、`ProjectSchedulerRecord`、`ProjectRunRecord`。
- `LoaderSummary`、`Loader`、`LoaderTrigger`、`LoaderRunSummary`、`LoaderEvent`、`LoaderBinding`。
- `Session`、`SessionSummary`、`NotebookCell`、`SessionEvent`、`WorkspaceConfig`。

原因：

- 这些 record/model 当前同时承担 SQL DTO、proto response 输入和业务模型职责。
- 第一批强行移动会扩大 import 修改范围，并把旧耦合带入新包。
- 更稳妥的做法是在旧 package 保留 alias/wrapper，逐步把调用点迁到 domain。

### Transport 第一批边界

Transport 第一批只拆轻 handler 外壳，不抽重业务逻辑。

优先拆：

- v2 `ImageService`。
- v1 `LoaderService`，业务主体仍委托现有 `LoaderManager`。
- v1 `CapabilityService`。
- v1 `DashboardService`。
- v2 `ExecService` 外壳，重逻辑暂留原位置。
- v2 `RunService` 外壳，`runProjectAgent` 暂不移动。

暂缓拆：

- v2 `ProjectService`，因为 `project_service.go` 同时包含 validate/apply/reconcile/dry-run。
- v1 `AgentDefinitionService` 中 create session/delete 相关逻辑。
- v1 `ConfigService` 中 workspace config 创建、更新、删除逻辑。
- HTTP `llm_facade`、`proxy`、`workspace_routes`、`webhook`。

原因：

- T2 的目标是打破 “一个 `Service` 实现所有 Connect service” 的结构，不顺手重写业务。
- `project_service.go`、workspace 和 LLM facade 的业务/安全边界较重，应等 application 层稳定后再拆。

### Project 域迁移第一批边界

`project_service.go` 是当前风险最高的文件，第一批不要直接大规模迁入 `internal/agentcompose/project`。原因是 `ProjectRecord`、`AgentDefinition`、`Loader`、`ConfigStore` 等类型仍在 `pkg/agentcompose`，直接建立域包容易造成 import cycle，或迫使模型/存储一起大迁移。

Project application 第一批应先拆无副作用 helper：

- proto spec normalize/validation shell。
- proto spec 到 YAML shape 的转换 helper。
- compose parse/normalize/hash 和 issue mapping。
- `ProjectSpecResponse` 及 response 子 mapper。
- managed resource 构建中的纯函数，例如 project agent record、managed agent definition、scheduler loader trigger/script 构建。

暂缓移动：

- `ApplyProject` 主体。
- `reconcileProjectManagedAgentDefinitions`。
- `reconcileProjectManagedSchedulers`。
- `validateInlineSchedulerScript`。
- `ensureProjectAgentImages`。
- `downProject`。
- `runProjectAgent`。

推荐过渡策略：

- 如果会产生 import cycle，不要强行新建 `internal/agentcompose/project`。
- 可以先新建 `pkg/agentcompose/projectapp` 放完全不依赖父包类型的 normalize/response helper。
- 对依赖 `ProjectRecord`、`Loader` 等父包类型的纯 helper，先拆到 `pkg/agentcompose/project_build.go` 这类同包文件，降低 `project_service.go` 体积；等 model/store 下沉后再迁入 `internal/agentcompose/project`。

Project application 第一批测试重点：

- duplicate env/agent、driver conflict、expected hash mismatch。
- `ProjectSpecResponse` scheduler script 字段。
- scheduler trigger kind、inline script validation triggers、main-only zero triggers。
- apply revision 幂等、validation failure 不落库。
- scheduler reconcile failure 的 staged resource cleanup 行为。
