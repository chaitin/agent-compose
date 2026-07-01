# agent-compose 概念定位与对外使用边界

本文档是 agent-compose agent/service 平台化方向的概念层文档。它说明
agent-compose 应该是什么、不应该是什么，以及对外集成时应依赖哪些稳定概念。

对应的工程任务拆分见
[agent-compose_engine_foundation_plan.md](agent-compose_engine_foundation_plan.md)。

## 文档定位

本文件回答：

- agent-compose 的产品和架构定位是什么。
- Project、Manifest、Agent、Service、Trigger、Runtime、SDK 的边界是什么。
- 哪些能力属于 agent-compose 引擎，哪些能力应留给上层平台。

`agent-compose_engine_foundation_plan.md` 回答：

- 为了实现上述定位，需要哪些工程 workstream。
- 每条 workstream 的负责范围、交付物和验收标准是什么。
- 哪些模块文档需要随实现更新。

因此，两份文档不是互相替代关系：

- 本文是概念来源和边界约束。
- foundation plan 是压缩后的工程执行计划。
- 具体已经实现的行为，以代码、proto、manifest schema、CLI help 和 runtime contract
  文档为准。

## 核心定位

agent-compose 应定位为：

```text
面向 agent/service 工作负载的 project manifest 控制面和 runtime 执行平台。
```

更具体地说：

- daemon 是控制面：接收配置、校验配置、准备资源、调度运行、记录状态和提供 API。
- runtime 是执行面：在受控 sandbox 中运行 agent provider、命令、脚本和 service entry。
- project manifest 是声明式入口：描述期望状态，而不是一次执行。
- trigger/scheduler 是触发层：描述什么时候运行，以及把事件路由到哪个目标。
- SDK 是业务逻辑代码调用平台能力的标准工具箱。
- 业务逻辑由用户或上层平台编写；agent-compose 只提供定义、运行、校验、观测和治理所需的通用基础设施。

## 非目标

agent-compose 不应该实现或内置上层业务概念，例如：

- tenant、channel、组织身份、审批流、计费、市场化包装。
- 产品专属权限策略。
- 业务流程中的具体规则、算法和判断。
- 上层 UI 的业务表单和工作流语义。

这些概念可以通过 project manifest、runtime context metadata、capability scope 和
service input/output schema 投影到 agent-compose，但不应成为引擎的一等模型。

## 核心概念

### Project

Project 是一组 agent profile、service entry、trigger、workspace、runtime 约束和权限配置的集合。
它是可版本化、可 apply、可 diff、可 rollback 的期望状态，不代表一次运行。

### Project Manifest / Compose File

Manifest 是 Project 的文本形态。它应只描述结构和引用，不承载大段业务实现代码。

Manifest 应表达：

- `runtime`：driver、image、env、resources、network、cleanup。
- `workspace`：默认 workspace source。
- `agents`：AI provider profile。
- `services`：可复用业务逻辑入口。
- `triggers`：manual/API/cron/interval/event/webhook 等触发。
- `permissions`、`artifacts`、`variables`、`metadata` 等治理信息。

当前仓库中的机器可读 schema 位于：

```text
pkg/compose/schema/agent-compose.manifest.schema.json
```

### Agent

Agent 是 AI provider profile，描述 provider、model、system prompt、workspace 默认值、
runtime 默认值和 capability scope。Agent 不等同于完整业务服务。

### Service Entry

Service entry 是主要可复用执行目标。它描述：

- 稳定名称和描述。
- JS entry 文件引用。
- input/output/error schema。
- timeout、retry、permissions、agents、examples。

Service entry 不表示 agent-compose 内置业务逻辑；它表示 agent-compose 能识别、
校验、运行和管理的用户业务代码入口。

### Trigger / Scheduler / Loader

Trigger 描述什么时候调用目标。目标可以是 service entry 或 agent profile。

推荐路径是声明式 trigger。Loader JS / scheduler script 保留为高级 escape hatch，
用于复杂路由、轻量编排和状态判断，但不应成为承载长耗时复杂业务逻辑的推荐层。

### Runtime

Runtime 是受控执行环境。它负责运行 service entry、agent provider 或 command，
并返回结构化结果、日志、artifact 和 metrics。Runtime 不拥有控制面状态。

### Runtime SDK

Runtime SDK 是 service code 调用平台能力的稳定表面。目标能力包括：

- context、log、agent、llm、exec、shell
- state、artifact、event
- secret、capability
- service invoke
- mock/dry-run 支撑

SDK 不应要求业务代码依赖 daemon 私有数据库、session 目录内部结构或 magic stdout payload。

## 对外集成面

上层平台应优先依赖：

```text
business contract
  -> project manifest projection
  -> ValidateProject / ApplyProject
  -> InvokeService / InvokeServiceStream
  -> WatchRun / GetRun / ListRuns
  -> ListArtifacts / ReadArtifact
```

上层平台不应依赖：

- daemon 私有数据库表。
- session 目录内部结构。
- loader 具体实现细节。
- provider 专属 prompt 文件路径。
- runtime magic stdout payload。
- v1-only session 字段。

## 当前工程状态

当前 `feature/agent-compose-engine-foundation` 分支已经实现概念闭环的主要工程基础：

- v2 ProjectService 覆盖 validate/apply/get/list/remove/diff/revision/rollback/watch。
- v2 RunService 覆盖 InvokeService、InvokeServiceStream、RunAgent、GetRun、ListRuns、StopRun、WatchRun。
- manifest 支持 services、triggers、runtime、workspace、permissions、artifacts 等核心结构。
- 本地 CLI 支持 validate、bundle validate、bundle inspect、up、invoke、logs、inspect。
- runtime service 使用结构化 `service-result.json`，stdout magic payload 仅作为兼容兜底。
- RunDetail 和 stream terminal event 提供标准 run envelope。
- runtime SDK 提供 service bridge、capability bridge、secret、mockRuntime 等能力。
- `examples/service-entry` 提供可被 bundle 校验覆盖的最小 service entry 契约样例。

仍属于后续产品化或外围平台能力的事项：

- 远端 bundle 发布、签名、注册中心。
- 上层 UI 表单、工作流、审批流和市场化包装。
- 完整 capability gateway 治理策略。
- 更完整的示例库、AI codegen 指南和发布级 schema 校验工具链。

这些事项不改变 agent-compose 的核心概念边界。
