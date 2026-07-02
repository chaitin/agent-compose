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
- 每个任务合并前必须说明是否保持行为不变、修改了哪些文件、运行了哪些测试。

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
refactor/persistence-repositories
refactor/loader-application
refactor/project-application
```

## 任务依赖图

```text
T0 文档与基线
  -> T1 Bootstrap 兼容外壳
    -> T2 Transport handler 拆分
    -> T3 Domain model/rule 抽取
      -> T4 Application service 抽取
        -> T5 Persistence repository 拆分
          -> T6 pkg/agentcompose 到 internal/agentcompose
            -> T7 其他 pkg/* 归类
```

并行策略：

- T1 是首个阻塞任务，先由负责人完成或单独 worker 完成。
- T2 与 T3 可以在 T1 合入重构主分支后并行。
- T4 需要依赖对应领域的 T2/T3 结果，按 project、loader、session、run 分组并行。
- T5 依赖 T4 中接口边界基本稳定。
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

### T3：Domain Model 与规则抽取

负责人：domain worker。

目标：

- 把纯模型、常量和状态规则移动到 domain 包。
- domain 包不得 import proto、connect、echo、SQL、driver。
- 不改变 JSON tag、状态字符串、hash 输入和稳定 ID。

建议分批：

- T3.1 session/workspace model。
- T3.2 loader model、trigger、run status。
- T3.3 project/project run model 与状态流转。
- T3.4 agent definition、capability、image、llm 纯模型。

主要文件：

- `pkg/agentcompose/model.go`
- `pkg/agentcompose/loader_model.go`
- `pkg/agentcompose/project_store.go`
- `pkg/agentcompose/run_coordinator.go`
- `pkg/agentcompose/agent_definition.go`
- `pkg/agentcompose/capability_*.go`
- `pkg/agentcompose/image_*.go`
- `pkg/agentcompose/llm_*.go`

验收：

- domain 包依赖干净。
- 现有 JSON/状态相关测试通过。
- `go test ./pkg/agentcompose`

### T4：Application Service 抽取

负责人：按领域拆多个 worker。

目标：

- 将用例编排从 transport 和 store 文件中抽出。
- application service 依赖 domain 和 ports，不依赖 Connect/Echo。
- 行为保持不变。

建议分组：

- T4.1 `application/project`：validate/apply/down/reconcile。
- T4.2 `application/loader`：manager/engine/executor/event dispatch。
- T4.3 `application/run`：run coordinator、run preparation、project agent run。
- T4.4 `application/session`：create/resume/stop/watch/reconcile/stream。
- T4.5 `application/exec`、`image`、`llm`、`config`、`capability`、`dashboard`。

验收：

- transport 只做映射和委托。
- application 不 import connect/echo。
- 用例测试仍通过。
- `go test ./pkg/agentcompose`

### T5：Persistence Repository 拆分

负责人：persistence worker。

目标：

- 把 SQL store 按 aggregate 拆成 repository。
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
- 冲突解决后必须运行相关测试。

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

### Project Application 第一批边界

`project_service.go` 是当前风险最高的文件，第一批不要直接大规模迁入 `internal/agentcompose/application/project`。原因是 `ProjectRecord`、`AgentDefinition`、`Loader`、`ConfigStore` 等类型仍在 `pkg/agentcompose`，直接建立 internal application 包容易造成 import cycle，或迫使模型/存储一起大迁移。

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

- 如果会产生 import cycle，不要强行新建 `internal/agentcompose/application/project`。
- 可以先新建 `pkg/agentcompose/projectapp` 放完全不依赖父包类型的 normalize/response helper。
- 对依赖 `ProjectRecord`、`Loader` 等父包类型的纯 helper，先拆到 `pkg/agentcompose/project_build.go` 这类同包文件，降低 `project_service.go` 体积；等 domain/model 下沉后再迁入 application 包。

Project application 第一批测试重点：

- duplicate env/agent、driver conflict、expected hash mismatch。
- `ProjectSpecResponse` scheduler script 字段。
- scheduler trigger kind、inline script validation triggers、main-only zero triggers。
- apply revision 幂等、validation failure 不落库。
- scheduler reconcile failure 的 staged resource cleanup 行为。
