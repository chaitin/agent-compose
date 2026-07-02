# agent-compose 重构架构方案

## 目标

本文档定义当前 agent-compose daemon 与 CLI 代码库的结构性重构方案。

本次重构不是重新设计产品、协议、运行时行为或持久化模型。目标是在不改变现有行为的前提下，通过更清晰的 Go 包边界重新组织职责，让代码更容易理解、修改和测试。

硬性要求：

- 对外 Connect API 保持不变。
- CLI 命令、参数、环境变量和默认行为保持不变。
- 现有业务逻辑、状态流转、调度器行为、持久化格式、runtime driver 行为、代理行为和错误语义保持不变。除非后续变更明确声明并测试了行为变化，否则不得混入行为改动。
- 现有 proto 文件和生成客户端保持兼容。
- 每一步重构都应足够小，可以通过现有测试验证。

非目标：

- 不做 DDD 重写。
- 不引入新的存储引擎。
- 不在包移动之外引入新的 runtime 抽象。
- 不变更 API 版本。
- 不把大规模行为清理隐藏在结构调整里。

## 当前问题

当前最大的结构问题是 `pkg/agentcompose` 是一个过宽的 Go package，承载了 daemon 的大部分职责。它同时包含 API handler、应用编排、领域规则、持久化、runtime 协调、loader 调度、镜像管理、LLM facade、workspace route、webhook、proxy 以及测试辅助代码。

具体症状：

- `pkg/agentcompose` 约有一百个 Go 文件。
- 多个文件超过一千行，包括 `project_service.go`、`service.go`、`loader_engine.go`、`loader_store.go`、`loader_manager.go`、`llm_config.go`、`project_store.go` 和 `exec.go`。
- `pkg/agentcompose/service.go` 中的 `Service` 实现了多个不相关的 Connect service handler，并直接持有大部分 daemon 依赖。
- 业务用例与 Connect request/response 类型、存储细节、runtime 细节和响应映射耦合在一起。
- Store 文件同时包含领域记录、normalize helper、SQL scan、稳定 ID 生成和 repository 行为。
- Project apply 逻辑直接协调 managed agent definition、managed scheduler、loader record、镜像可用性和 project run 状态。
- Loader 逻辑混合了调度、事件处理、runtime 执行、project-run 集成、持久化和 API 转换。
- 很多标识符只是因为处在同一个大 package 里才保持 unexported，Go package 可见性没有表达真实架构边界。

结果是局部推理成本很高。修改一个功能时，经常需要阅读很多无关代码，因为包边界没有体现所有权。

## Go 布局取舍：`pkg` 与 `internal`

在 Go 中，`pkg` 只是社区约定，不会限制 import。代码放在 `pkg` 下通常意味着其他 Go module 可以 import 并依赖它，近似表达“这是可复用库 API”。`internal` 则由 Go 工具链强制限制，只允许父目录树内的代码 import，更适合作为应用自己的实现细节。

agent-compose 本质上是一个可执行服务和 CLI，外加生成的 API 客户端。它稳定的外部契约主要是：

- `proto/` 下的 Connect/protobuf API。
- `proto-client/` 下发布的 TypeScript 生成客户端。
- `cmd/agent-compose` 暴露的 CLI 行为。
- `runtime/` 下的 guest runtime SDK 和脚本。
- Compose 文件语义。

大多数 daemon 实现包并不是稳定的 Go library。因此，当前 `pkg/agentcompose` 中的大部分代码更适合移动到 `internal/agentcompose`。

推荐策略：

- daemon 与 CLI 的实现细节默认放在 `internal/`。
- 只有明确希望作为可复用 Go library 维护的包，才放在 `pkg/`。
- 生成协议包保留在 `proto/`，因为它们是集成契约。
- 不把 `pkg` 当作共享代码垃圾桶。共享实现代码通常应放在 `internal/shared` 或明确边界的 internal package 中。

### 包归类建议

建议的最终归类：

| 当前包 | 建议位置 | 原因 |
| --- | --- | --- |
| `pkg/agentcompose` | `internal/agentcompose/...` | daemon 控制平面实现，不是公共 Go API。 |
| `pkg/auth` | `internal/auth` 或 `internal/agentcompose/transport/auth` | 该 daemon 的 HTTP middleware/login 实现。 |
| `pkg/config` | `internal/config` | 当前二进制的进程/环境配置。 |
| `pkg/dbo` | `internal/dbo` | 数据库装配实现细节。 |
| `pkg/health` | `internal/health` | 服务健康检查实现。 |
| `pkg/driver` | 初期放 `internal/driver` | runtime driver 实现默认属于 daemon 内部，除非明确支持作为库。 |
| `pkg/capproxy` | 初期放 `internal/capproxy` | capability proxy server 实现细节。 |
| `pkg/imagecache` | 初期放 `internal/imagecache`，或明确复用时保留 `pkg/imagecache` | OCI image cache 可能可复用，但当前更像 daemon 自有能力。 |
| `pkg/compose` | `pkg/compose` 或 `internal/compose` | 仅当 compose 解析/normalize 是对外 Go API 时保留在 `pkg`。 |
| `pkg/capability` | `pkg/capability` 或 `internal/capability` | 仅当 capability catalog/client 类型是扩展 API 时保留在 `pkg`。 |
| `pkg/fxgo` | `internal/fxgo` 或逐步移除 | 框架胶水和 response helper 是实现细节。 |
| `proto/...` | 保持不变 | 生成的 API 契约。 |

保守迁移路径：

1. 第一阶段只移动 `pkg/agentcompose` 到 `internal/agentcompose/...`。
2. 暂时保留其他 `pkg/*` 包，降低变更范围。
3. 核心拆分稳定后，再逐包判断剩余 `pkg/*` 是否真的应作为公共库存在。

## 目标架构

目标架构调整为“域优先包结构”。仍然借鉴 DDD 的核心思路，但不再把代码优先拆成横向的 `domain/application/infrastructure` 大目录。对 Go 服务来说，更自然的组织方式是让 package 名表达业务能力，让同一业务域的模型、用例、端口和基础设施适配尽量靠近。

核心原则：

- 先按业务域拆包，再在域内按需要拆文件。
- `transport`、`bootstrap` 作为跨域适配层单独保留。
- `project`、`loader`、`session`、`run` 等域拥有自己的模型、服务、repository interface 和具体实现文件。
- 不为了形式上的 DDD 分层把同一个业务域拆散到多个远距离目录。
- 大多数 daemon 实现代码最终应位于 `internal/agentcompose/<domain>`，而不是公开的 `pkg/<domain>`。

目标依赖方向：

```text
cmd/agent-compose
  -> internal/agentcompose/bootstrap
    -> internal/agentcompose/transport
      -> internal/agentcompose/<domain service>
    -> internal/agentcompose/<domain>
      -> internal/agentcompose/shared
    -> internal/driver, internal/imagecache, internal/config, proto/...
```

允许的依赖规则：

- 域包内部可以包含模型、服务、端口、repository 实现和 mapper，但文件职责要清楚。
- 域模型和纯规则文件不得 import Connect、Echo、SQL、Docker client、runtime driver 或进程配置包。
- 域服务可以依赖本域 repository interface、其他域暴露的窄 service/interface，以及必要的基础设施 adapter。
- `transport` 可以 import proto/connect/echo 和域服务，但不应包含业务编排、SQL 或 runtime driver 直接调用。
- `bootstrap` 负责依赖装配和 handler 注册。
- 跨域协作应通过窄接口或明确的值对象完成，避免随意访问另一个域的存储结构。

## 建议目录结构

```text
internal/
  agentcompose/
    bootstrap/
      register.go
      background.go
      wiring.go

    transport/
      connectv1/
        session_handler.go
        kernel_handler.go
        agent_handler.go
        agent_definition_handler.go
        llm_handler.go
        config_handler.go
        loader_handler.go
        dashboard_handler.go
        capability_handler.go
        mapper.go
      connectv2/
        project_handler.go
        run_handler.go
        exec_handler.go
        image_handler.go
        mapper.go
      http/
        proxy_routes.go
        webhook_routes.go
        workspace_routes.go
        runtime_llm_facade_routes.go

    session/
      model.go
      service.go
      repository.go
      sqlite.go
      stream.go
      reconcile.go
      proto_mapper.go

    project/
      model.go
      service.go
      validate.go
      apply.go
      build.go
      reconcile_agents.go
      reconcile_schedulers.go
      dryrun.go
      repository.go
      sqlite.go
      proto_mapper.go

    loader/
      model.go
      service.go
      manager.go
      engine.go
      executor.go
      schedule.go
      event_dispatcher.go
      repository.go
      sqlite.go
      proto_mapper.go

    run/
      model.go
      service.go
      coordinator.go
      preparation.go
      proto_mapper.go

    exec/
      service.go
      proto_mapper.go

    agent/
      definition.go
      service.go
      repository.go
      sqlite.go
      proto_mapper.go

    config/
      env.go
      workspace.go
      service.go
      repository.go
      sqlite.go
      proto_mapper.go

    image/
      model.go
      service.go
      ensure.go
      docker_backend.go
      oci_backend.go
      auto_backend.go
      proto_mapper.go

    llm/
      service.go
      client.go
      config.go
      facade.go
      runtime_config.go
      proto_mapper.go

    capability/
      model.go
      service.go
      gateway.go
      provider.go
      proxy.go
      repository.go
      sqlite.go
      proto_mapper.go

    workspace/
      model.go
      service.go
      routes_mapper.go

    events/
      model.go
      dispatcher.go
      topic_store.go
      repository.go
      sqlite.go

    dashboard/
      overview.go
      aggregator.go
      hub.go

    runtime/
      provider.go
      driver_adapter.go

    shared/
      ids/
      jsontime/
      errors/
      response/
```

这是目标方向，不是一次性大补丁。迁移过程中允许存在过渡包、wrapper、alias 和同包文件拆分，只要每一步可 review、可测试，并且明确朝域包收敛。

## 边界说明

### Bootstrap

`bootstrap` 替代当前宽泛的 `agentcompose.Setup(di)` 职责。

职责：

- 在依赖注入容器中注册 constructor。
- 构建各域 service、repository、transport handler 和后台 worker。
- 在 Echo 上注册 Connect 与 HTTP route。
- 启动后台 manager。

兼容要求：

- 在所有 import 更新完成前，保留 `agentcompose.Setup(di)` 作为薄 wrapper：

```go
func Setup(di do.Injector) {
    bootstrap.Setup(di)
}
```

### Transport

Transport 包只负责外部协议适配。

职责：

- Connect handler struct。
- HTTP endpoint 的 Echo route 注册。
- 纯协议层面的请求校验。
- proto message 与域 service command/result 的映射。
- 域错误到 Connect 或 HTTP error 的映射。

Transport 包不应：

- 打开 SQL transaction。
- 直接调用 runtime driver。
- 直接启动 scheduler run。
- 自行 normalize compose spec，除非只是调用 project 域 service。
- 持有 project、loader、session、run 的状态流转规则。

### Domain Packages

域包是本次重构的主目标。每个域包以业务能力命名，优先保持该域的相关代码靠近。

域包内部可包含：

- `model.go`：领域模型、状态常量、值对象。
- `service.go`：该域主要用例编排。
- `repository.go`：该域需要的存储接口。
- `sqlite.go`：当前 SQLite 实现。后续如果变大，可迁到域内 `sqlite/` 子包。
- `proto_mapper.go`：该域与 proto 的转换。如果 mapper 变复杂，也可留在 `transport` 中。
- 其他按用例命名的文件，例如 `apply.go`、`manager.go`、`coordinator.go`、`schedule.go`。

域包内部仍要保持文件级边界：

- 纯模型/规则文件不 import proto、connect、echo、SQL、driver。
- service 文件可以编排 repository 和其他域接口，但不直接写 HTTP/Connect。
- SQLite 文件可以 import SQL，但不应持有业务流程。

### Shared

`shared` 只放跨域、无业务归属、稳定且小的工具：

- ID/hash helper。
- 时间/JSON helper。
- 错误分类。
- response 工具。

不要把 `shared` 变成新的垃圾桶。能归属到具体域的逻辑必须留在域包。

## 初始文件映射

第一轮迁移可以尽量机械化。建议映射：

| 当前文件组 | 目标区域 |
| --- | --- |
| `service.go` 中的 setup/registration | `internal/agentcompose/bootstrap` |
| `transport_handlers.go` 与 Connect 方法 | `internal/agentcompose/transport/connectv1`、`connectv2` |
| `model.go` 中的 session/workspace/cell model | `internal/agentcompose/session`、`workspace` |
| `session_*.go`、`store.go` session 部分 | `internal/agentcompose/session` |
| `config_store.go` | 按职责拆到 `config`、`workspace`、`agent`、`capability` |
| `project_store.go`、`project_service.go`、`project_*` | `internal/agentcompose/project`，project run 状态可进入 `run` |
| `project_schema.go` | `internal/agentcompose/project/validate.go` |
| `project_agent_runner.go` | `internal/agentcompose/run` 或 `project`，以编排所有权为准 |
| `project_down.go` | `internal/agentcompose/project/down.go` |
| `run_coordinator.go`、`run_service.go`、`run_preparation.go` | `internal/agentcompose/run` |
| `exec.go`、`exec_service.go` | `internal/agentcompose/exec` |
| `loader_model.go`、`loader_store.go`、`loader_engine.go`、`loader_manager.go` | `internal/agentcompose/loader` |
| `loader_run_executor.go`、`loader_event_dispatcher.go`、`loader_events.go`、`loader_bus.go` | `internal/agentcompose/loader` 或 `events`，以事件所有权为准 |
| `webhook*.go` | `transport/http`，事件摄入委托给 `events` 或 `loader` |
| `proxy.go` | `transport/http`，session proxy 逻辑归 `session` |
| `workspace.go`、`workspace_routes.go` | `workspace` 与 `transport/http` |
| `llm_client.go`、`llm_config.go`、`llm_facade.go`、`llm_runtime_config.go` | `llm`，HTTP facade route 在 `transport/http` |
| `image_*.go` | `image` |
| `capability_*.go` | `capability` |
| `dashboard_overview.go` | `dashboard` |
| `event_dispatcher.go`、`topic_event_*.go` | `events` |

## 迁移阶段

### Phase 0：护栏

移动代码前：

- 记录当前 `main` commit。
- 确认工作区干净，除非存在明确无关的用户修改。
- 跑一次 baseline 测试，并在 PR 描述中记录结果。
- 先提交本文档，并将其作为重构契约。

推荐 baseline：

```bash
task test
task build
```

如果完整测试在中间提交中过慢，每个阶段至少应运行针对性 `go test`，最终阶段必须运行项目质量门禁。

### Phase 1：兼容外壳

创建 `internal/agentcompose/bootstrap`，只移动 setup/wiring 代码。

保留旧入口：

- `pkg/agentcompose.Setup(di)` 仍然可用。
- `cmd/agent-compose` 的 import 初期可以不变。

成功标准：

- 无对外 API 变化。
- `cmd/agent-compose` 仍通过同一路径启动。
- 测试通过。

### Phase 2：Transport Handler 外壳

把 Connect 方法从宽泛的 `Service` 类型中移出，拆成按 service 分组的 handler struct。

此阶段 handler 可以暂时委托旧 `Service` 方法。阶段目标是先打破“一个类型实现所有 service”的模式，不移动业务逻辑。

成功标准：

- 生成的 proto 包不变。
- Connect route path 不变。
- 现有集成测试仍访问相同 endpoint。

### Phase 3：域包种子

选择低耦合域建立真正的域包，而不是继续只做同包文件拆分。

优先顺序：

1. `image`
2. `capability`
3. `dashboard`
4. `events`
5. `loader`
6. `project`
7. `session`

成功标准：

- 每个新域包有清晰公开 surface。
- transport 只依赖域 service 或 mapper。
- 纯规则文件不 import proto/connect/echo/SQL/driver。

### Phase 4：高复杂域迁移

迁移 `loader`、`project`、`session`、`run` 等复杂域。

要求：

- 先迁模型和纯规则，再迁 service 编排，再迁 repository 实现。
- 每个 PR 只迁一个域的一块职责。
- 对 `project_service.go`、`loader_manager.go`、`loader_engine.go` 这类热点文件，优先按同包文件拆分降低体积，再迁入域包。

成功标准：

- `pkg/agentcompose` 不再是主要业务实现包。
- 大文件持续变小。
- 用例可以绕过 Connect handler 单独测试。

### Phase 5：持久化归域

把 SQL-backed store method 按域归入对应域包。

注意：这是包和文件组织调整，不是数据库 schema 调整。

必须保持：

- 现有 DB 路径行为。
- 现有表名。
- 现有 migration。
- 现有 JSON column 与编码。
- 现有稳定 ID。

成功标准：

- 现有 data 目录仍可读取。
- Store migration 测试不改或少改即可通过。
- 使用临时 DB 的集成测试通过。

### Phase 6：从 `pkg/agentcompose` 迁移到 `internal/agentcompose`

内部包边界稳定后，更新 import，让 daemon 使用 `internal/agentcompose`。

可选兼容方式：

- 如果不支持外部 Go import，删除 `pkg/agentcompose`。
- 如果本地迁移需要过渡期，保留一个短期 deprecated wrapper package。

推荐最终状态：

- daemon 实现不再留在 `pkg/agentcompose`。
- `pkg/agentcompose` 被删除，或只在过渡期包含很薄的 deprecated wrapper。

### Phase 7：重新归类其他 `pkg/*` 包

逐个评估剩余 `pkg/*` 包：

- 如果它是文档化的外部 Go library surface，则保留在 `pkg`。
- 如果它是 daemon 实现细节，则移动到 `internal`。

这个阶段应在核心拆分后单独进行，因为它会影响很多 import，但理论上不应改变行为。

## 包依赖规则

这些规则应通过 code review 执行，后续可以引入 import linter 自动检查。

允许：

- `transport -> domain service`
- `domain service -> same-domain repository/interface`
- `domain service -> other-domain narrow interface`
- `domain sqlite/repository implementation -> database/sql`
- `bootstrap -> 所有实现包`
- `cmd -> bootstrap`

禁止：

- 纯模型/规则文件 -> proto/connect/echo/database/sql/driver
- `transport -> database/sql`
- `transport -> runtime drivers`
- `transport -> image backends`
- 域包之间直接访问对方 SQLite 实现
- `shared -> 具体业务域`

## 行为保持检查清单

每个重构 PR 都应显式检查：

- Connect route path 完全一致。
- Proto request/response message 不变。
- CLI 输出格式不变，尤其是 `--json` 输出。
- 状态字符串不变。
- 错误码和重要错误信息不变。
- DB schema 和 migration 行为不变。
- 现有数据仍可加载。
- Loader trigger 调度行为不变。
- Project apply dry-run 输出不变。
- Managed loader/agent reconcile 行为不变。
- Project run 生命周期行为不变。
- Runtime session create/resume/stop 行为不变。
- Jupyter proxy path 和 header 行为不变。
- Webhook 行为不变。
- LLM facade request/response 行为不变。
- Image ensure/pull/cache 行为不变。

## 测试策略

沿用 `TESTING.md` 中定义的测试分层。

每个阶段的最低检查：

- 运行被移动 package 的局部单元测试。
- 运行覆盖变更边界的现有集成测试。
- import path 迁移后运行 `task build`。
- 阶段合并前运行 `task test`。

针对本次结构重构，额外建议：

- 增加轻量 architecture test 或脚本，禁止 domain 包 import 禁止依赖。
- 增加 route registration 测试，断言 Connect path 不变。
- 如果尚不存在，为重要 CLI JSON 输出增加 golden test。
- 如果有现成 fixture DB，增加 repository 兼容性测试。

## Review 策略

PR 应保持小且机械化。

推荐 PR 形态：

1. 增加本文档。
2. 移动 setup/bootstrap，不改行为。
3. 一次拆一个 transport handler 组。
4. 一次移动一组 domain model。
5. 一次抽取一组 application use case。
6. 一次拆一组 repository。

每个 PR：

- 声明是否是行为保持型重构。
- 列出移动的文件和新增包。
- 列出执行过的测试命令。
- 单独说明任何不可避免的行为变化。行为变化不应与机械式 package move 混在一起。

## 待决问题

Phase 7 前需要明确：

- `pkg/compose` 是否是被支持的 Go library API，还是只是 daemon/CLI 内部 parser。
- `pkg/capability` 是否是面向外部集成的 extension API。
- `pkg/imagecache` 是否应成为可复用 image-cache library，还是保持 daemon 自有实现。
- `pkg/driver` 是否有必要被本 module 之外 import。如果没有，应移动到 `internal/driver`。
- 包拆分后是否引入 import-boundary linter。

## 建议的第一个具体步骤

从低风险的兼容步骤开始：

1. 新增 `internal/agentcompose/bootstrap`。
2. 将 constructor registration、route registration 和 background startup 从 `pkg/agentcompose/service.go` 移入 bootstrap function。
3. 保留 `pkg/agentcompose.Setup(di)` 作为 wrapper。
4. 暂不移动业务逻辑。
5. 运行 `go test ./pkg/agentcompose ./cmd/agent-compose` 和 `task build`。

这个步骤可以先建立第一个架构边界，同时不改变运行时对外表现。
