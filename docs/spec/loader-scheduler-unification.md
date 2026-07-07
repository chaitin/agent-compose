# Scheduler 概念统一设计

> **范围**：只设计 agent-compose 自身的项目调度语义、后端实现边界、compose 配置和 CLI 行为。UI 和 CLI 是兼容性硬约束：本设计不改变它们已有的可见接口、字段、命令、输出或行为。

## 速读结论

项目里的 `scheduler:` 对应一个清晰的一等概念：**Project Scheduler**。它的规范主记录是 `ProjectSchedulerRecord`。项目调度相关的查询、投影、手动触发、停用、删除清理和变更检测，都以 `ProjectSchedulerRecord` 为入口。底层执行所需的脚本、触发器和运行机制只是执行产物（代码中为 scheduler execution，下文也称"执行体"），不承载项目 Scheduler 的主语义。

## 先看懂：一个例子看清三个 "scheduler"

你在 compose 里写：

```yaml
agents:
  reviewer:
    scheduler:                      # ← 关键字是 scheduler
      triggers:
        - name: daily-review
          cron: "0 8 * * *"
          prompt: "每天早八点跑一下"
```

它会经历 4 步：

```text
① 你写的 scheduler: 块                    「配置写法」，给用户看
        │ normalize / reconcile
        ▼
② 落成 ProjectSchedulerRecord            主记录（权威），持有 SpecJSON
   并从它生成一份 scheduler execution     派生的执行体，持有脚本 + 触发器
   自动生成脚本（真实形式）：
   scheduler.cron("<触发器id>", "0 8 * * *",
     async function(event) { return scheduler.agent("每天早八点跑一下"); });
        │ 到早上 8 点
        ▼
③ 后台 scheduledRunDispatcher 把它叫醒     「闹钟」，纯内部机制
        │
        ▼
④ 脚本里 scheduler.agent(...) 真正启动 agent
```

①③④ 三处都出现了 "scheduler"，但它们互相**没有任何关系**——这就是"看着乱"的根源：

| 出现的地方 | 它其实是什么 | 打个比方 |
|-----------|------------|---------|
| ① compose 的 `scheduler:` 块 | 用户的**配置写法**（编译成一个 Project Scheduler） | 菜单上的"套餐 A" |
| ③ 后台 `scheduledRunDispatcher` | 内部的**闹钟机制**（到点触发） | 厨房里的定时器 |
| ④ 脚本里 `scheduler.cron/agent(...)` | 执行脚本的 **API 名字** | 服务员记单子的本子 |

> 注意区分两个易混文件：后台闹钟在 `pkg/loaders/scheduled_run_dispatcher.go`；配置翻译在 `pkg/projects/scheduler.go`。两者代码上无关。

三个 scheduler 逐个说明：

- **① compose 的 `scheduler:` 块** —— 声明式配置，是"在某个触发器上跑某个 agent"这个常见需求的语法糖，用户不用手写 JS。定义在 `pkg/compose/spec.go` 的 `SchedulerSpec`；翻译在 `pkg/projects/scheduler.go` 的 `SchedulerExecutionTriggersAndScript`（把配置转成触发器 + 一段自动生成的 JS）。
- **④ 脚本里的 `scheduler.*`** —— 执行脚本运行时，宿主注入的全局对象，脚本靠它注册触发器、调 agent、打日志。签名 `scheduler.cron(triggerId, expression, callback, options?)`（也可省略 `triggerId`）。定义在 `pkg/loaders/engine.go`。执行体的"运行时类型"名也是 `"scheduler"`（`LoaderRuntimeScheduler`，目前唯一一种）。
- **③ 后台 `scheduledRunDispatcher`** —— 后台 goroutine，盯着所有执行体的触发器，到点就唤醒执行。跟前两个毫无关系，纯实现细节。

## 目标模型

沿用上面的 `scheduler:` 例子，系统按下面的模型理解：

```text
compose scheduler:
        │ normalize / reconcile
        ▼
ProjectSchedulerRecord
  - ProjectID
  - AgentName
  - SchedulerID
  - Enabled
  - TriggerCount
  - SpecJSON
  - ManagedLoaderID（执行产物引用）
        │ generate execution artifact
        ▼
执行脚本 + 触发器（scheduler execution）
        │ scheduledRunDispatcher 到点唤醒
        ▼
scheduler.agent(...) 启动 agent
```

核心边界：

| 概念 | 规范职责 | API 版本 | 对外兼容要求 |
|------|----------|:---:|--------------|
| `scheduler:` | compose 声明式项目调度配置 | —（compose） | YAML 结构和语义不变 |
| `ProjectSchedulerRecord` | 项目 Scheduler 的主记录和状态源 | **v2** `ProjectService` | Project projection（`schedulers` / `scheduler_count`）语义不变 |
| scheduler execution（执行产物） | 保存生成后的执行脚本和触发器，供后台运行 | **v1** `LoaderService` | 不作为项目 Scheduler 主语义暴露 |
| `scheduler.*` | 执行脚本里的运行时 API | —（脚本运行时） | 名称和语义不变 |
| `scheduledRunDispatcher` | 后台触发循环，按触发器唤醒执行产物 | —（内部） | 纯内部实现，不影响 UI/CLI |

> **权威分层落在 API 版本边界上**：Project Scheduler 是 **v2** 项目域的一等概念（`ProjectService` / `message ProjectScheduler` / `scheduler_count`）；执行产物是 **v1** 运行时（`LoaderService` / `message Loader`）。"主记录 vs 执行体"的边界，就是 v2 项目域与 v1 loader 运行时的边界。
>
> **面向 v1 移除铺垫**：v1 计划移除。本轮把权威锚在 v2 侧的 `ProjectSchedulerRecord`，正是为此做准备——当 v1 被删除时，执行产物不是任何东西的权威源，可以安全内部化或下线，项目调度状态（v2）不受影响。反过来若把权威放在 v1 `Loader`，删 v1 就会删掉权威源。因此 record-authority 不只是低风险，更是 v1 移除的前置条件。

## 要解决的问题

### 名字撞车

`scheduler` 同时出现在 compose 配置、脚本 API、CLI 命令和后台触发循环里。业务语义上的 Scheduler 保留给项目调度概念；后台循环用 `scheduledRunDispatcher` 这类内部名字，不占用业务名。

### 主记录与执行产物的边界

项目 Scheduler 同时有主记录和执行产物，两边有关联字段和状态同步，但职责不同：

```text
ProjectSchedulerRecord（主记录，权威）        scheduler execution（执行体，派生副本）
────────────────────────────────           ──────────────────────────────────────
ProjectID       "proj"          ═══════      Summary.ManagedProjectID   "proj"
AgentName       "reviewer"      ═══════      Summary.ManagedAgentName   "reviewer"
SchedulerID     "sch-abc"       ═══════      Summary.ManagedSchedulerID "sch-abc"
Enabled         true            ═══════      Summary.Enabled            true
TriggerCount    1               ═══════      len(Summary.Triggers)      1
ManagedLoaderID "ldr-xyz"       ═══════      Summary.ID                 "ldr-xyz"
SpecJSON        {...}                        （无对应物）
```

`═══════` 两边是同一份信息，但**以主记录为准**：执行体由 `NewSchedulerExecutionFromRecord` 从主记录生成，`GetSchedulerExecution` 按主记录里的 `ManagedLoaderID` 取回；执行体上的 `Summary.Managed*` 只是执行期反查用的派生副本，不作权威源。

关键差异是最后一行：`SpecJSON` 代表 compose 中的 Scheduler 规格，是变更检测和项目投影所需的声明式状态，在执行体上**没有对应物**。这正是变更检测和项目投影必须落在主记录、而不能反推自执行体的根本原因。

因此正确边界不是删除或降级 `ProjectSchedulerRecord`，而是明确它是项目 Scheduler 主状态，执行产物只由它引用和驱动。本设计不消除这份字段重复，而是把它定性为"主记录 + 派生执行体"并明确权威。

"执行产物引用"只表示主记录到执行数据的内部关联；不新增 schema 字段，不进入 Project API / CLI 输出，也不改变现有返回结构。

## 设计要点

### A. 内部触发循环命名

后台触发循环命名为 `scheduledRunDispatcher`（`pkg/loaders/scheduled_run_dispatcher.go`），不占用 Scheduler 业务名。这是纯实现细节命名，不改变 UI、CLI、compose、脚本 API 或项目投影。

### B. ProjectSchedulerRecord 作为主入口

项目 Scheduler 相关行为都以 `ProjectSchedulerRecord` 为入口：

| 行为 | 规范入口 | 关键符号 |
|------|----------|---------|
| 手动触发 | 按 project / agent 找到 scheduler record，用 trigger id 校验声明式规格并定位执行产物中的触发器 | `manualTriggerSchedulerExecution`：`ListProjectSchedulers` → `GetSchedulerExecution` |
| project down / remove | 遍历项目 scheduler records，停用 record 并同步停用执行产物 | `ListProjectSchedulers` → `SetProjectSchedulerEnabled(false)` → `DisableSchedulerExecution` |
| reconcile 删除清理 | 对比当前 spec 与已有 scheduler records，停用被移除的 scheduler | `ListProjectSchedulers` → `SetProjectSchedulerEnabled(false)` |
| Project API 投影 | 从 scheduler records 生成 `schedulers` 和 `scheduler_count`（apply / get / remove 一致；apply dry-run 用本次构建的 records） | `ProjectSchedulerRecord` → proto |
| 变更检测 | 用 scheduler record 的声明式 `SpecJSON` 判断变化 | `SchedulerRecordUnchanged` / `SchedulerChangeAction` |
| CLI 展示和触发 | 通过现有 Project API / scheduler API 看到相同行为 | — |

两个语义细节必须保留：

- **`SpecJSON` 是变更检测的权威判据**：主记录独有、执行体无对应物，change detection 以它为准。这是把权威锚在主记录的技术根据。（实现上 reconcile 还会另发一条派生的 `loader` 变更项、并把执行体 diff 一并计入 `unchanged` 标志；因执行体由同一份 spec 确定性生成，两者结果一致、不构成独立判据。）
- **disabled 行不过滤**：`ListProjectSchedulers` 无 `WHERE enabled`，down / 移除只把 record + 执行体一起置 `enabled=false`、行仍留在表里，所以 Project API / CLI 输出**包含**被禁用的 scheduler。此语义保持不变。

主记录与执行产物之间有同步逻辑，但**方向单一**：以 record 为主修复或重建执行产物，绝不反过来让执行产物决定项目 Scheduler 是否存在、是否启用、如何投影。收敛机制上，reconcile 每次都用 `NewSchedulerExecutionFromRecord` 从主记录重新生成执行体并 upsert（upsert record → upsert execution → replace triggers → enable execution → enable record），执行体天然朝主记录自愈。

因为主记录与执行体是多步分别写入的，任一步中断会留下 partial state，必须明确覆盖并按"以主记录为准"修复：

- 主记录存在、执行产物缺失：按主记录重建执行产物，投影仍以主记录为准。
- 主记录启用、执行产物缺失或未启用：以主记录为准修复执行产物状态。
- 主记录 disabled、执行产物仍启用：以主记录为准停用执行产物的后台自动触发；手动触发仍按 disabled 处理规则保持兼容。
- 主记录不存在、遗留执行产物存在：不得把遗留执行产物投影成项目 Scheduler；清理逻辑停用或忽略它。
- 主记录与执行产物引用不一致：以主记录中的引用为准，不从执行产物反推主记录。

### C. 执行产物职责

执行产物只负责运行：

- 保存生成后的执行脚本。
- 保存可运行的触发器。
- 被 `scheduledRunDispatcher` 唤醒。
- 执行 `scheduler.agent(...)` 等脚本 API。
- 记录运行结果和事件。

它不作为以下内容的权威源：项目 Scheduler 是否存在、是否启用、Project API / CLI 中的 scheduler projection、compose scheduler 规格是否变化、被移除 scheduler 的清理判断。

## UI / CLI 兼容约束

### UI 不受影响

- 不改现有 RPC 路径、消息字段、枚举值或 JSON shape，覆盖 **v2 `ProjectService`** 与 **v1 `LoaderService`** 两套。
- 不改 v2 Project projection 中的 `schedulers`（`ProjectScheduler`）和 `scheduler_count`。
- 不改 v1 `runtime: "scheduler"` 取值。
- 不改 `scheduler.*` 脚本 API。

> **两个 freeze 性质不同**：v2 `ProjectService` 是长期契约，稳定保持；v1 `LoaderService` 是**本轮临时冻结**——前端当前仍直连 v1（把 loader 叫 "Automation Task"），故本轮不动；但 v1 计划移除，待前端迁移到 v2 后即可下线。本设计只保证"不在本轮改坏 v1"，不把 v1 当作永久对外契约。
- disabled Scheduler 的可见性保持当前语义：Project projection / `inspect project` 仍展示 disabled Scheduler；可触发列表和手动触发仍按当前规则处理 disabled 状态。

### CLI 不受影响

- `agent-compose scheduler ...` 命令名不变。
- `scheduler ls / inspect / trigger` 参数和 JSON 输出不变。
- `inspect project`、`up`、`down` 的 project scheduler 输出不变。
- 手动触发的结果字段、run 展示和日志读取不变。

CLI 继续调用现有服务端接口；服务端只在内部保证数据来源锚在 `ProjectSchedulerRecord`，对 CLI 输出保持等价。服务端返回给 UI/CLI 的 project scheduler 字段、run 字段、错误码和错误语义都必须等价——只调整内部数据来源，不改变外部可观察契约。历史 run / session / event 的 API 和 CLI 可见性不变。

### 存量数据不受影响

- 不删除表、不删除字段、不做不可逆迁移、不清理历史运行记录。
- 执行产物和历史数据继续可读。

## 决策记录

| # | 决策 | 说明 |
|---|------|------|
| D1 | Project Scheduler 的主记录是 `ProjectSchedulerRecord` | 项目调度是用户可见语义，应有明确主记录。 |
| D2 | 执行产物不是项目 Scheduler 主状态源 | 它只负责被触发和运行，不能决定 project projection。 |
| D3 | 后台循环命名为 `scheduledRunDispatcher` | 释放 Scheduler 业务名，降低内部概念冲突。 |
| D4 | UI/CLI 契约冻结 | 不改命令、字段、JSON shape、脚本 API、运行时取值。 |
| D5 | 不做 destructive migration | 保留存量数据和历史运行记录。 |
| D6 | 不消除主记录与执行体的字段重复 | 前端接口冻结、`SpecJSON` 无执行体对应物；把重复定性为"主记录 + 派生执行体"并明确权威，比合并存储风险低、更贴合语义。 |
| D7 | 权威锚在 v2、执行产物(v1)只作派生，是为 v1 移除铺垫 | v1 计划移除；权威放 v2 后，删 v1 不会带走任何权威源。record-authority 是 v1 下线的前置条件，v1 freeze 为本轮临时措施。 |

## 验收标准

- 文档不把执行产物描述为项目 Scheduler 主实体。
- 文档明确 `ProjectSchedulerRecord` 是项目 Scheduler 主记录。
- 文档明确 UI 不受影响。
- 文档明确 CLI 不受影响。
- 文档明确主记录与执行产物的权威边界与 partial-state 处理。
