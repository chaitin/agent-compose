# Daemon 事件订阅流技术方案

> 状态：Phase 1 已实现（2026-11-27）；Phase 2 webhook 尚未实现
>
> 目标读者：daemon / API / runtime 维护者，以及上层应用（UI server、业务编排服务）的接入方

## 0. 结论（TL;DR）

- **可行，而且不需要引入新的传输栈**。daemon 已经有 Connect server-stream、进程内 fan-out、单实例模型和统一 Bearer Token；缺的是一层“跨 project、跨 subject、带统一事件模型和恢复语义”的观测事件总线。
- **推荐形态**：新增 `EventService.WatchEvents` server-stream RPC。上层应用主动发起一条长连接完成“订阅”，daemon 在这条连接上按过滤条件推送 sandbox、run、scheduler、日志和错误事件。默认关闭；没有订阅者时 daemon 不分配序号、不维护 replay ring，回到零开销。
- **不建议第一阶段就做 daemon 反向 webhook**。回调会引入 URL 暴露、SSRF、签名、重试、死信、持久化 outbox 等一整套可靠性设施；先把事件模型和总线做对，webhook 可以作为同一总线的第二种 delivery sink 在第二阶段加。
- **投递语义**：控制面事件（状态、错误、调度）在内存 ring 内可重放；日志事件只做实时投递，断线后用现有 `FollowRunLogs(start_offset)` 回填。超出 replay 窗口时返回 `SYNC_REQUIRED`，由上层应用调用现有 List/Get API 做状态对账。
- **核心风险**：日志量、慢消费者、project 归属解析，以及“通知语义”与“审计语义”的边界。方案用有界 buffer + 丢弃计数 + gap 事件 + offset 回填 + 状态对账来兜底，不承诺日志不丢。

---

## 1. 背景与问题

### 1.1 现状

agent-compose 的 daemon 是唯一状态权威：project、run、sandbox、runtime driver、scheduler、事件、镜像、缓存、Jupyter 和 LLM facade 都由 daemon 持久化和执行。上层应用（典型是独立的 UI server 或业务编排服务）与 daemon 不在一个进程，且一个 daemon 上可以运行多个 project、任意数量的 scheduler 和 sandbox。

上层应用目前只能：

1. 周期性调用 `ListProjects` / `ListRuns` / `ListSchedulerRuns` / `ListSchedulerEvents` / `ListSandboxes` 等 unary API 做全量或增量轮询；
2. 发现 run/sandbox ID 后，再按 subject 打开 `FollowRunLogs`、`WatchSandbox` 等流；
3. 对 scheduler 的实时变化，只能轮询或反复做有限扫描。

### 1.2 具体痛点

| 痛点 | 现状 | 后果 |
| --- | --- | --- |
| 发现成本 | 新 run、新 sandbox、新 scheduler run 没有全局通知 | 必须轮询 list API，延迟和请求量都随 project 数增长 |
| 连接数 | `WatchSandbox` 是单 sandbox，`FollowRunLogs` 是单 run | N 个 sandbox / run 需要 N 条连接，应用侧连接管理复杂 |
| scheduler 实时性 | `StreamSchedulerRuns` / `StreamProjectSchedulerEvents` 是**有限扫描**，发完即结束 | 看不到“正在发生”的 scheduler 事件，只能反复扫描 |
| 错误信息分散 | run 错误在 `project_run.error`，scheduler 错误在 `scheduler_run.error` / `scheduler_event`，sandbox 错误在 `sandbox_event` / VM 状态 | 应用需要理解多套模型才能拼出“出了什么事” |
| 日志入口不统一 | run 日志在 `run.log` / `logs_path`，scheduler 输出在 artifacts / scheduler event，cell 输出走 `StreamBroker` | 无法用一条流覆盖“执行过程 + 输出 + 错误” |
| project 维度 | 每个 API 各自按 project 选择 | 多 project 场景需要应用自己聚合、去重、排序 |

### 1.3 目标

- 提供一个**默认关闭**、由上层应用主动订阅后才激活的 daemon 能力。
- 一条订阅连接覆盖多个 project、多个 sandbox / run / scheduler，按统一事件模型推送。
- 事件带稳定的类型、序号、时间、project 归属和 subject 引用，应用无需先知道 subject ID。
- 支持断线续传（有限窗口）和状态对账，不要求应用做全量轮询。
- daemon 侧任何执行路径都**不能**因为慢消费者而阻塞；允许丢弃日志，但必须让应用知道丢了什么、怎么补。
- 不改变现有 API 的行为，不引入新的外部依赖（NATS / Redis / Kafka 等）。

### 1.4 非目标（首版不做）

- 不做 daemon 主动 HTTP 回调（webhook sink）。见 §3.3 和 §6 Phase 2。
- 不做跨 daemon / 多副本的事件聚合。当前部署模型是单 daemon（SQLite 单写；PostgreSQL 用 advisory lock 保证单实例），进程内总线就是全局总线。
- 不做永久事件日志。审计仍以数据库记录、`run.log` 文件和 artifacts 为准。
- 不改变现有 `WatchSandbox` / `FollowRunLogs` / scheduler stream 的契约；它们是详情视图和回填通道。

---

## 2. 现状调研（代码证据）

### 2.1 传输层：server-stream 已经是成熟路径

- `proto/agentcompose/v2/agentcompose.proto` 已有多个 server-stream RPC：`WatchProject`、`FollowRunLogs`、`WatchSandbox`、`WatchDashboardOverview`、`StreamSchedulerRuns`、`StreamProjectSchedulerEvents`、`BuildImage`、`StreamExec` 等。
- `pkg/agentcompose/api/transport.go` 的 `PrepareStreamingHeaders` 已经处理了 `Cache-Control: no-cache, no-transform` 和 `X-Accel-Buffering: no`，说明仓库已经考虑反向代理下的流式响应。
- `docs/pages/connect-transport-matrix.md` 明确：HTTP/1.1 支持 server-stream（要求代理不缓冲、及时 flush），h2c / TLS h2 支持完整能力；双向 attach 才必须 HTTP/2。本方案只需要 server-stream。
- daemon 的 HTTP server 使用 `h2c.NewHandler`（`cmd/agent-compose/cli_daemon.go`），没有设置写超时；`AGENT_COMPOSE_AUTH_TOKEN` 的 Bearer 中间件覆盖所有控制面路由（`cmd/agent-compose/daemon_auth.go`），新 RPC 自动受保护。

**结论**：不需要新协议、不需要 WebSocket、不需要 SSE 专用栈；Connect server-stream 足够。

### 2.2 进程内 fan-out 已经存在

| 组件 | 位置 | 缓冲 | 满时行为 | 用途 |
| --- | --- | --- | --- | --- |
| `RunLogHub` | `pkg/runs/run_log_hub.go` | 64 | 非阻塞丢弃 | 按 run 分发实时日志 chunk |
| `StreamBroker` | `pkg/sandboxes/stream.go` | 256 | 非阻塞丢弃 | 按 sandbox 分发状态、cell 输出、sandbox event |
| `dashboard.Hub` | `pkg/dashboard/overview.go` | 8 | 非阻塞丢弃 | 聚合概览 |
| `schedulers.Bus` | `pkg/schedulers/bus.go` | 256 | 非阻塞丢弃 | scheduler topic event 的进程内投递 |

这些组件证明了仓库已经接受“进程内 pub/sub + 有界队列 + 满时丢弃”的模式。本方案可以复用同样的语义，而不是发明新的可靠性模型。

### 2.3 事件源已经比较集中

| 事件源 | 单一/准单一入口 | 说明 |
| --- | --- | --- |
| scheduler 事件 | `schedulers.Controller.AddSchedulerEventRecord` | 所有 scheduler event 都经过这里落库 |
| scheduler run 状态 | `schedulers.RunExecutor` 的 `Prepare` / `Execute` / `Abort` | `scheduler.run.started/completed/failed/canceled/skipped` 已存在 |
| project run 状态 | `runs.Coordinator.TransitionRun`、`runs.CompletionManager` | 状态落库的集中路径 |
| run 日志 chunk | `runs.publishRunLogChunk` → `RunLogHub.Publish` | command / agent / attach 输出都走这里 |
| sandbox 状态 / 事件 / cell 输出 | `sandboxes.StreamBroker.Publish*` | lifecycle、project run、adapter 执行器都会调用 |
| sandbox 事件落库 | `sandboxes.Store.AddEvent` | 调用点较多；大部分 runtime/adapter 路径随后会 `PublishEventAdded`，workspace cleanup / retention 等少数落库事件没有 live fan-out |

### 2.4 持久化与回填能力

- `project_run_event`：按 run 分配 `seq` 的追加表，承载 user/agent/tool/status 等语义事件。
- `scheduler_event` / `scheduler_run`：调度历史，可按时间和 ID 分页。
- `topic_event` + `events.Dispatcher`：已有的“DB outbox + claim + retry”模式，用于 scheduler 触发事件；它证明了 daemon 内可以做一个可恢复的投递循环，但它的语义是“触发输入”，不适合直接复用为“观测输出”。
- `run.log.jsonl`：`pkg/runlog` 新增的只追加日志包（目前还没有调用方）；`Writer.Options.OnAppend` 是未来结构化日志事件的理想注入点。
- 现有 `FollowRunLogs` 支持 `start_offset`，可用于日志回填；`ListRuns` / `ListSchedulerRuns` / `ListSandboxes` 可用于状态对账。

### 2.5 单实例与认证

- `charts/agent-compose/README.md`：daemon 被有意设计为单副本；SQLite 单写，PostgreSQL 模式通过 advisory lock 强制单 daemon。
- `AGENT_COMPOSE_AUTH_TOKEN` 是 daemon 级共享 Bearer Token，不是 per-project token。新订阅流的过滤条件因此只是**查询过滤**，不是授权边界；能拿到 token 的调用方本来就能读所有 project。

### 2.6 现有“流”的局限（为什么不在原 API 上补）

- `WatchProject` 目前是 `CodeUnimplemented`（`pkg/agentcompose/app/project_controller.go`）。
- `StreamSchedulerRuns` / `StreamProjectSchedulerEvents` 的注释和实现都明确是有限扫描（finite scan），发完 `complete` 就结束，不是 live tail。
- `WatchSandbox` 只覆盖一个 sandbox，且只走内存 broker，没有跨 sandbox 的发现能力。
- `FollowRunLogs` 只覆盖一个 run，且需要先知道 run ID。

因此，问题不是“再补一个流”，而是缺少一个**跨 subject 的统一事件平面**。

---

## 3. 方案选型

### 3.1 候选方案

| 方案 | 实时性 | 可靠性 | 实现成本 | 安全面 | 日志适配 | 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| A. 继续轮询 | 取决于间隔 | 好（每次读全量） | 0 | 仅 token | 差 | 不解决根本问题 |
| B. 统一订阅流（server-stream） | 毫秒级 | 有限窗口重放 + 对账 | 低-中 | 仅 token，无入站回调 | 好（有界 + 回填） | **推荐首版** |
| C. daemon → 应用 webhook | 毫秒级 + 重试 | 可做到 at-least-once | 高 | SSRF、签名、URL 暴露 | 差（大量 POST） | 第二阶段可选 |
| D. 裸 SSE endpoint | 毫秒级 | 同 B | 低 | 同 B（浏览器需 query token，较差） | 好 | 可作为 B 的轻量适配，不单独做 |
| E. 外部消息总线（NATS/Redis/Kafka） | 毫秒级 | 好 | 很高 | 取决于部署 | 好 | 当前单 daemon 不需要 |

### 3.2 推荐 B 的理由

1. **复用现有能力**：Connect server-stream、鉴权、代理头、DI、错误契约都是现成的。
2. **没有入站攻击面**：daemon 不主动连应用，不需要注册回调 URL，不需要 SSRF 防护、签名密钥和重试 worker。
3. **应用侧成本低**：UI server / 业务服务通常是长期运行的 Go/Java/Node 进程，维持一条 HTTP 流是常规能力；生成的 Connect client 直接可用。
4. **核心资产可复用**：事件模型、broker、过滤、cursor、gap 语义在 B 和 C 中是同一套；先做 B 不会阻碍以后加 C。
5. **与部署模型一致**：单 daemon 下进程内总线就是完整视图；不需要外部 broker。

### 3.3 什么时候应该选 C（webhook）

只有满足以下任一条件时，才值得进入 Phase 2：

- 应用无法维持长连接（serverless、只允许短请求、严格的反向代理空闲超时）。
- 应用在 daemon 不可达的网络里，但 daemon 能访问应用（方向相反）。
- 业务要求“应用宕机期间事件不丢”，且不能靠应用重连后对账解决。
- 需要多个独立消费者，每个都希望由 daemon 主动推送。

即使进入 C，也建议：**只对控制面事件做持久化 outbox，日志仍然实时/批量投递，不承诺不丢**。具体见 §6.2。

---

## 4. 详细设计

### 4.1 总体架构

```
┌────────────────────────────── daemon process ──────────────────────────────┐
│                                                                            │
│  runs.Controller ──┐                                                       │
│  CompletionManager ─┤                                                      │
│  schedulers.RunExecutor ─┤        ┌──────────────────────────┐              │
│  schedulers.Controller ──┼───────►│  eventstream.Broker      │              │
│  sandboxes.StreamBroker ─┤ publish│  - sequence / cursor     │              │
│  runs.RunLogHub ─────────┘        │  - filter / ring         │              │
│                                   │  - per-subscriber queue  │              │
│                                   └────────────┬─────────────┘              │
│                                                │                            │
│                                   ┌────────────▼─────────────┐              │
│                                   │  api.EventStreamHandler  │              │
│                                   │  WatchEvents (server-    │              │
│                                   │  stream, batching,       │              │
│                                   │  heartbeat, gap)         │              │
│                                   └────────────┬─────────────┘              │
└────────────────────────────────────────────────┼────────────────────────────┘
                                                 │ Connect server-stream
                                    ┌────────────▼─────────────┐
                                    │ 上层应用（UI server 等） │
                                    │ 过滤 / 去重 / 对账 / 回填│
                                    └──────────────────────────┘
```

设计原则：

- **源包不依赖 API/proto**：`pkg/eventstream` 只依赖 `pkg/model`；`runs`、`schedulers`、`sandboxes` 依赖 `eventstream` 的窄接口，不感知 Connect。
- **publish 永不阻塞**：所有 fan-out 都是非阻塞入队，满时记 dropped；日志写入、调度执行、状态落库不能因为订阅者变慢而等待。
- **订阅者隔离**：每个订阅独立的 buffer 和过滤条件；一个慢消费者不影响另一个。
- **可退化**：没有订阅者时 broker 不分配 sequence、不维护 ring；功能关闭时 `Publish` 是一次原子读后返回。

### 4.2 API 设计

新增 `EventService`（与现有服务并列）：

```proto
service EventService {
  // 能力探测：功能是否开启、支持的事件类型、limit。
  rpc GetEventStreamCapabilities(GetEventStreamCapabilitiesRequest)
      returns (GetEventStreamCapabilitiesResponse);

  // 订阅。默认关闭；调用成功即激活该订阅，daemon 在这条流上推送事件。
  rpc WatchEvents(WatchEventsRequest) returns (stream WatchEventsResponse);
}
```

建议把 service 和消息追加到现有 `proto/agentcompose/v2/agentcompose.proto`，而不是新建 proto 文件：`TestErrorContractCoversEveryRPC` 目前只遍历 `File_agentcompose_v2_agentcompose_proto`，新增文件需要同步改测试的遍历范围；追加到现有文件可以少一个容易遗漏的步骤。若拆文件，必须同时更新该测试。

请求/响应：

```proto
enum EventSeverity {
  EVENT_SEVERITY_UNSPECIFIED = 0;
  EVENT_SEVERITY_INFO = 1;
  EVENT_SEVERITY_WARNING = 2;
  EVENT_SEVERITY_ERROR = 3;
}

message GetEventStreamCapabilitiesRequest {}

message GetEventStreamCapabilitiesResponse {
  bool enabled = 1;
  repeated string event_types = 2;       // 稳定的事件类型清单
  uint32 max_subscribers = 3;
  uint32 subscriber_buffer_events = 4;
  uint32 replay_events = 5;
  uint32 heartbeat_seconds = 6;
}

message WatchEventsRequest {
  // 空 = 所有 project（受 token 权限约束）。最多 100 个。
  repeated string project_ids = 1;
  // 精确类型或前缀通配，例如 "run.status"、"run.*"；
  // 空 = 所有类型，日志是否包含由 include_logs 决定。
  repeated string event_types = 2;
  EventSeverity min_severity = 3;
  // 日志是高吞吐事件，必须显式打开；默认 false。
  bool include_logs = 4;
  // 上一次确认处理到的 cursor；空 = 从当前 head 开始。
  string cursor = 5;
  // 客户端希望的批大小，server 会再夹一层上限。
  uint32 max_batch_events = 6;
}

message WatchEventsResponse {
  repeated StreamEvent events = 1;
  // 本批最后一个事件的 cursor；空批时为当前 head/心跳 cursor。
  string next_cursor = 2;
  StreamControl control = 3;
}

message StreamControl {
  enum Kind {
    KIND_UNSPECIFIED = 0;
    KIND_HELLO = 1;          // 首帧：epoch、cursor（head）、replay_floor、heartbeat
    KIND_HEARTBEAT = 2;      // 空闲保活
    KIND_SYNC_REQUIRED = 3;  // cursor 失效，应用需要对账
    KIND_GAP = 4;            // 本订阅因慢消费/缓冲满丢弃了事件
  }
  Kind kind = 1;
  string epoch = 2;
  string cursor = 3;
  string replay_floor = 4;
  uint32 heartbeat_seconds = 5;
  string reason = 6;
  uint64 dropped_events = 7;
  string first_dropped_cursor = 8;
  string last_dropped_cursor = 9;
}
```

事件信封：

```proto
message StreamEvent {
  string id = 1;                 // 全局唯一，应用用于去重
  string cursor = 2;             // epoch + sequence 的不透明编码
  uint64 sequence = 3;           // daemon 内单调递增；重启后 epoch 变化
  google.protobuf.Timestamp occurred_at = 4;
  string type = 5;               // 稳定类型字符串，见 §4.3
  string project_id = 6;         // 尽量填写；sandbox 事件可能为空，见 §4.8
  EventSeverity severity = 7;
  oneof payload {
    StreamRunStatus run_status = 10;
    StreamRunLog run_log = 11;
    StreamSchedulerRunStatus scheduler_run_status = 12;
    SchedulerEvent scheduler_event = 13;       // 复用现有消息
    StreamSandboxStatus sandbox_status = 14;
    StreamSandboxEvent sandbox_event = 15;
    StreamSandboxCellOutput sandbox_cell_output = 16;
  }
}
```

payload 采用“**当前状态快照**”而不是 delta：状态事件携带完整 `RunSummary` / `SchedulerRun` / sandbox 状态字段，应用可以幂等覆盖；这比“只发变化的字段”更容易在对账后恢复一致。

payload 定义（`SchedulerEvent` 复用现有消息，不重复定义）：

```proto
message StreamRunStatus {
  RunSummary run = 1;
  RunStatus previous_status = 2;
  // RunSummary 没有的终态诊断字段，避免接入方为了看错误再发一次 GetRun。
  string cleanup_error = 3;
  string error_stack = 4;
}

message StreamSchedulerRunStatus {
  SchedulerRun run = 1;
  SchedulerRunStatus previous_status = 2;
}

message StreamSandboxStatus {
  string sandbox_id = 1;
  string project_id = 2;          // 可能为空，见 §4.8
  string title = 3;
  string driver = 4;
  string vm_status = 5;
  string previous_vm_status = 6;  // best effort；adapter 缓存缺失时为空
  string error = 7;               // best effort；失败详情通常随后由 sandbox.event 给出
}

message StreamSandboxEvent {
  string sandbox_id = 1;
  string project_id = 2;
  string event_id = 3;
  string type = 4;
  string level = 5;
  string message = 6;
}

message StreamSandboxCellOutput {
  string sandbox_id = 1;
  string project_id = 2;
  string cell_id = 3;
  string stream = 4;              // stdout / stderr
  string data = 5;
}
```

`previous_vm_status` 只能 best-effort 填写：`StreamBroker.PublishSandboxUpdated` 拿到的已经是更新后的 summary。adapter 可以在内存里维护 `sandboxID -> lastVMStatus` 的小缓存；首次看到某个 sandbox 时留空。应用应以 `vm_status` 快照为准，`previous_vm_status` 只用于辅助判断幂等。`error` 同理：启动失败等详情通常不在 summary 里，而是在随后 `PublishEventAdded` 的 `sandbox.event` 中；接入方应以 `sandbox.event` 作为错误详情来源。

`occurred_at` 使用事件源记录的时间（`project_run.updated_at`、`scheduler_run.completed_at`、`scheduler_event.created_at` 等），不是 broker 的发布时刻；`sequence` 只表示 daemon 内的发布顺序。两者分开，应用才能既做顺序处理又做时间比较。

### 4.3 事件目录

| type | payload | 产生位置 | 默认 severity | 量级 | 可 replay |
| --- | --- | --- | --- | --- | --- |
| `run.status` | `StreamRunStatus{run, previous_status}` | run 状态落库后 | info / error | 低 | 是 |
| `run.log` | `StreamRunLog` | run 日志 append 后 | info | 高 | 否（按 offset 回填） |
| `scheduler.run.status` | `StreamSchedulerRunStatus{run, previous_status}` | `UpdateSchedulerRun` 后 | info / error | 低 | 是 |
| `scheduler.event` | 现有 `SchedulerEvent` | `AddSchedulerEventRecord` 后 | level 映射 | 中 | 是 |
| `sandbox.status` | `StreamSandboxStatus` | `StreamBroker.PublishSandboxUpdated` | info / error | 低 | 是 |
| `sandbox.event` | `StreamSandboxEvent` | `StreamBroker.PublishEventAdded` | level 映射 | 中 | 是 |
| `sandbox.cell.output` | `StreamSandboxCellOutput` | `StreamBroker.PublishCellOutput` | info | 高 | 否 |

日志类型（`run.log`、`sandbox.cell.output`）默认不投递；订阅时必须显式 `include_logs=true`。原因：一条 chatty agent 的输出可以轻松到每秒数千 chunk，不能让它拖垮控制面事件。

过滤校验：`event_types` 里出现未知的精确类型时返回 `InvalidArgument`（避免拼写错误静默变成“收不到任何事件”）；精确指定 `run.log` / `sandbox.cell.output` 但 `include_logs=false` 时也返回 `InvalidArgument`。通配（如 `run.*`）不报错，只是日志类型在 `include_logs=false` 时被排除。前缀通配只允许 `foo.*` 形式，且不能匹配空后缀。

`StreamRunLog` 同时兼容当前和未来的日志格式：

```proto
message StreamRunLog {
  string run_id = 1;
  string project_id = 2;
  string sandbox_id = 3;
  uint64 offset = 4;        // 用于 FollowRunLogs(start_offset) 回填
  string stream = 5;        // stdout / stderr / agent
  string data = 6;
  string format = 7;        // "text"（当前）或 "jsonl"（runlog 接线后）
  string entry_type = 8;    // format=jsonl 时：user/text/tool_call/tool_result/error/turn_end...
  bool truncated = 9;
}
```

当前 `pkg/runlog` 的 `Writer.Options.OnAppend(entry, rendered)` 已经在写锁内按序回调；等它接线后，可以直接在同一个 hook 里发布结构化 `run.log` 事件（`format=jsonl`、`entry_type=entry.Type`），不需要改 API。注意：`OnAppend` 在写锁内被调用，发布必须是非阻塞的，broker 的 `Publish` 必须满足这一点。

`offset` 的语义需要特别注意：当前 `FollowRunLogs(start_offset)` 使用的是**文件字节偏移**，而 `runlog.Writer.OnAppend` 目前只暴露 `Entry.TextOffset`（文本视图偏移），不暴露文件字节偏移。等 JSONL 接线时，需要在 `Writer` 上增加“当前文件字节偏移”的读取/回调，或者在 `FollowRunLogs` 上增加基于 `seq` 的回填入口；否则结构化日志事件的 `offset` 不能直接用于回填。首版沿用当前 text + 文件字节偏移的路径即可。

scheduler run 的输出没有 `RunLogHub`：它通过 `scheduler.event`（`scheduler.log`、`scheduler.command.completed` 等）和 scheduler sandbox 的 `sandbox.cell.output` 暴露；`run.log` 只覆盖 project run。文档和事件目录要明确这一点，避免接入方以为 `run.log` 覆盖所有执行。

### 4.4 投递语义

这里刻意区分三类语义，避免“事件流 = 审计日志”的误解：

1. **live**：订阅建立后的实时事件。控制面事件和日志事件都走这条路径。
2. **replay**：仅控制面事件进入 ring（默认 4096 条）。应用带 cursor 重连时，从 ring 中补发 `sequence > cursor.sequence` 且匹配过滤条件的事件。
3. **sync_required**：cursor 的 epoch 不匹配（daemon 重启/空闲回收），或 cursor 早于 ring floor（超出窗口）。server 发送 `SYNC_REQUIRED`，然后从当前 head 继续 live；应用调用 List/Get API 重建状态。

重放与 live 的衔接要显式处理：订阅时先注册 live 队列，再从 ring 复制历史；handler 把两段按 `sequence` 合并，跳过 `sequence <= last_sent` 的重复项，保证同一订阅内单调递增。不能“先回放完再开始接收”，否则回放期间的新事件会丢。

事件 `id` 建议直接使用 `epoch:sequence`，而不是额外生成 UUID：它天然唯一（epoch 变化后不会冲突）、可直接去重，也减少了热路径上的内存分配。`cursor` 可以复用同一字符串，或做一次不透明编码。

日志事件**不进 ring**：

- 它们量大，放进 ring 会显著增加常驻内存。
- 日志本身有 `offset`，断线后应用可以用 `FollowRunLogs(run_id, start_offset=last_offset)` 从文件回填。
- 如果应用只关心实时 tail，可以忽略回填。

**因此不承诺日志 exactly-once，也不承诺日志在断线期间不丢**；承诺的是：应用能知道丢了（gap / offset 跳变）并且能补。

控制面事件的窗口内重放是 at-least-once：重连边界可能重复，应用按 `StreamEvent.id` 去重。窗口外则通过 `SYNC_REQUIRED` 退化为“状态对账”。

幂等建议：

- 状态事件携带完整快照；应用按 `subject` 比较 `occurred_at` / `sequence`，旧事件不覆盖新状态。
- `previous_status` 可以帮助应用识别“事件是否适用于当前状态”；不匹配时可丢弃或触发一次 `GetRun`。
- 所有事件都有 `id`；重复投递直接丢弃。

### 4.5 背压与丢失可见性

每个订阅：

- 有界 channel（默认 512 个事件，可配）。
- `Publish` 对每个订阅做非阻塞发送；满时 `dropped++`，并记录 `first_dropped_cursor` / `last_dropped_cursor`。
- handler 在下一次成功发送前检查 dropped 计数，先发一个 `KIND_GAP` control（带计数和 cursor 范围），再发后续事件。
- 绝不阻塞发布方：run 日志写入、scheduler 执行、sandbox 状态更新都在关键路径上，不能被订阅者拖慢。
- 日志热路径只做“原子序号 + 读锁 + 非阻塞入队”，不做 ring 复制、不做数据库查询；project/sandbox 归属通过缓存解析。若实测原子序号成为瓶颈，可以给日志单独的无锁序号，控制面 cursor 不受影响。

内存上限：

- 控制面 ring：默认 4096 条 × 小对象，量级可控。
- 每订阅 buffer：512 条；日志事件可能携带较大字符串，最坏情况需要按事件数 + 单事件上限双重约束。建议 broker 对单个事件设置 `max_event_bytes`（例如 256 KiB），超过的日志 chunk 截断并标 `truncated=true`，或直接丢弃并计入 gap。
- 最大订阅数：默认 32，超过返回 `RESOURCE_EXHAUSTED`。

日志回填路径：

```
run.log 事件 offset 跳变 / 收到 GAP
        -> 应用记录每个 run 的 last_offset
        -> FollowRunLogs(project_id, run_id, start_offset=last_offset, follow=true)
        -> 从 run.log 文件补齐
```

### 4.6 事件源接入点

尽量复用已有的 notifier / hub，而不是在每个业务分支散落 publish 调用。

| 事件 | 接入点 | 改动量 | 说明 |
| --- | --- | --- | --- |
| `scheduler.event` | `schedulers.Controller.AddSchedulerEventRecord` 落库成功后 | 小 | 入参已有 `SchedulerID`；补 `ProjectID`（`RunExecutor` 知道 scheduler.ProjectID）或按 scheduler 查一次并缓存 |
| `scheduler.run.status` | `schedulers.RunExecutor.Prepare` / `Execute` / `Abort` 的 `UpdateSchedulerRun` 之后 | 小 | 已有 `notify("scheduler_run_updated")`，在旁边加带类型的 publish |
| `run.status` | `runs.Coordinator.TransitionRun` + `runs.CompletionManager` 的 finalize / running 分支 | 中 | 对比 `current.Status` 与新状态，只在变化时发布；必须在落库成功后 |
| `run.log` | `runs.RunLogHub` 增加全局 listener（或 `publishRunLogChunk` 双写） | 小 | 当前事件只有 `RunID/Offset/Data`；project/sandbox 由 adapter 按 run 元数据解析并缓存 |
| `sandbox.status` | `sandboxes.StreamBroker` 增加全局 listener，监听 `PublishSandboxUpdated` | 小 | 覆盖 lifecycle、project run、adapter 三条路径；adapter 缓存 lastVMStatus，只在 VM 状态变化时发布 |
| `sandbox.event` | 同上，监听 `PublishEventAdded` | 小 | 覆盖 `Store.AddEvent` 之后已有的 fan-out 调用点 |
| `sandbox.cell.output` | 同上，监听 `PublishCellOutput` | 小 | 高吞吐，受 `include_logs` 控制 |

推荐做法：给 `RunLogHub` 和 `StreamBroker` 各加一个“全局 observer”回调（不引入对 `eventstream` 的依赖），在 `pkg/agentcompose/app` 组装时把它接到 broker。这样：

- 不需要改 `sandboxes` / `runs` 里十几个 `Publish*` 调用点；
- 未来新增的 event 只要走现有 hub 就自动被观测到；
- observer 为空时零开销。

注意：`workspace_cleanup` / retention 等少数路径只调用 `Store.AddEvent`，没有走 `StreamBroker`。如果这些事件也需要实时推送，应在这些调用点补 `PublishEventAdded`，而不是在 storage 层直接依赖 eventstream（那会把持久化和传输耦合在一起）。首版可以先把它们排除在事件目录之外，文档明确即可。

run 状态和 scheduler 事件没有现成的全局 hub，需要显式的窄接口，例如：

```go
// pkg/eventstream
type Publisher interface {
    Publish(Event)
}

// pkg/runs
type StatusObserver interface {
    RunStatusChanged(ctx context.Context, before, after domain.ProjectRunRecord)
}
```

`runs.Controller` / `CompletionManager` / `schedulers.RunExecutor` 通过构造参数注入可选 observer；nil 时直接跳过。不要为了发事件去装饰 `configstore`：store 不知道状态迁移语义，也拿不到可靠的 before 状态。

### 4.7 默认关闭与生命周期

三层开关，精确对应“默认不开启，订阅时才开始工作”：

1. **部署开关**：`EVENT_STREAM_ENABLED`，默认 `false`。关闭时 `WatchEvents` 返回 `FailedPrecondition`，`GetEventStreamCapabilities` 返回 `enabled=false`；不创建 ring，不启动 goroutine。
2. **订阅激活**：打开开关后，broker 初始为 `inactive`；第一个 `WatchEvents` 成功订阅时进入 `active`，开始分配 sequence、维护 ring、fan-out。
3. **空闲回收**：最后一个订阅断开后进入 `idle`；超过 `EVENT_STREAM_IDLE_TIMEOUT`（默认 5 分钟）没有新订阅，清空 ring、轮换 epoch、回到 `inactive`。期间如果来了新订阅，取消回收。

```
disabled --(EVENT_STREAM_ENABLED=true)--> inactive
inactive --(first WatchEvents)--> active
active --(last subscriber leaves)--> idle
idle --(timeout)--> inactive (ring cleared, epoch rotated)
idle --(new WatchEvents)--> active
```

行为细节：

- `inactive` 时 `Publish` 只做一次原子读就返回；不分配 ID/sequence，不复制字符串。
- `active` 且没有订阅者时，控制面事件仍进入 ring，保证短时间断线可重放；日志事件仍然不 ring。
- epoch 在进程启动时生成；idle 回收时轮换。epoch 变化意味着旧 cursor 一律 `SYNC_REQUIRED`。
- daemon 优雅关闭时 broker 关闭所有订阅 channel；handler 返回，客户端重连后收到新的 epoch。

### 4.8 多 project 与归属解析

- 信封上的 `project_id` 是过滤和聚合的主键。
- `run.*`：一定来自 `ProjectRunRecord.ProjectID`。
- `scheduler.*`：一定来自 scheduler 的 `ProjectID`（`SchedulerSummary.ProjectID`）。
- `sandbox.*`：`domain.SandboxSummary` 本身没有 project 字段；现有投影通过 `sandboxstore.SandboxProjectResolver`（生产实现是 `runs.SandboxRunTargetResolver.ResolveBatch`）解析。建议在 eventstream adapter 里做一个小容量 TTL/LRU 缓存（sandboxID → projectID），避免每个 cell output 都查库；解析不到时留空。
- 过滤语义：`project_ids` 非空时，`project_id` 为空的事件默认不投递；首版不支持按 sandbox/run ID 的 subject 过滤。`project_ids` 为空时，无法归属的 sandbox 事件仍会投递。这样无 project 归属的临时 sandbox（例如纯 `exec` 创建的 sandbox）不会被错误地算到某个 project 下。
- 不要为了填 project_id 在日志热路径做同步数据库查询；缓存未命中时优先使用已知的 run/sandbox 上下文，其次才查一次。

### 4.9 认证、安全与隔离

- 复用 `AGENT_COMPOSE_AUTH_TOKEN`。新 RPC 在现有中间件之后，不需要新认证逻辑。
- 过滤是**查询条件**，不是授权：拿到 daemon token 的调用方本来就可以读所有 project。若未来要 per-project 授权，需要单独设计 token scope；本方案不假装过滤等于隔离。
- 日志可能包含 prompt、命令、工具输出、环境变量片段。`include_logs` 默认 false；文档必须提示部署方保护 token / 网络边界。
- 无入站 URL → 无 SSRF 面。Phase 2 的 webhook 才需要 URL allowlist、私网地址拒绝、HTTPS 要求、HMAC 签名和重放保护。
- DoS 防护：最大订阅数、最大过滤项数、最大批大小、单事件大小上限、有界 buffer、空闲回收。

### 4.10 配置

沿用 `pkg/config` 的 env + 校验模式（参考 `pkg/config/event_delivery.go`）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `EVENT_STREAM_ENABLED` | `false` | 总开关；关闭时不暴露能力 |
| `EVENT_STREAM_MAX_SUBSCRIBERS` | `32` | 并发 `WatchEvents` 上限 |
| `EVENT_STREAM_SUBSCRIBER_BUFFER` | `512` | 每订阅事件 buffer |
| `EVENT_STREAM_REPLAY_EVENTS` | `4096` | 控制面事件 ring 大小；0 = 禁用重放 |
| `EVENT_STREAM_IDLE_TIMEOUT` | `5m` | 无订阅后多久回收 |
| `EVENT_STREAM_HEARTBEAT` | `15s` | 心跳间隔 |
| `EVENT_STREAM_MAX_BATCH_EVENTS` | `128` | 单帧最大事件数 |

校验在 `pkg/config` 启动时完成：负数/非法 duration 直接报错；heartbeat 必须大于 0；buffer 至少 1。默认值只在一处定义。

### 4.11 可观测性

- `slog`：订阅建立/断开（含 project/type 过滤、客户端地址哈希）、`SYNC_REQUIRED` 原因、空闲回收；不要打印事件 payload。
- 计数（可接现有 `pkg/telemetry` OTel metrics，也可先放在 capabilities/status 里）：
  - active subscriptions
  - events published / delivered / dropped（按 type）
  - ring evictions
  - sync_required 次数
- `GetEventStreamCapabilities` 可以返回当前 active 订阅数和 ring floor/head，便于联调和运维；是否对普通调用方暴露由产品决定。

### 4.12 兼容性

- proto 只新增 service/message/enum，不改已有字段；`task generate:proto` 重新生成。
- 默认关闭，现有部署行为不变。
- 不新增数据库迁移（Phase 1）；不改变 `project_run_event` / `scheduler_event` / `run.log` 的现有语义。
- 现有 `WatchSandbox` / `FollowRunLogs` / scheduler streams 保留，作为详情和回填通道。
- 未来 `runlog` JSONL 接线后，`run.log` 事件通过 `format` / `entry_type` 向后兼容；应用忽略不认识的 `entry_type` 即可。

---

## 5. 上层应用接入流程

### 5.1 首次接入

```
1. GetEventStreamCapabilities()
   - enabled=false -> 功能未开启，走现有轮询
2. WatchEvents(cursor="", project_ids=[...], event_types=["run.*","scheduler.*","sandbox.*"], include_logs=false)
3. 收到 HELLO：
   - 记录 epoch / cursor（当前 head）/ heartbeat
   - 开始接收 live 事件（先缓冲）
4. 调用 ListProjects / ListRuns / ListSchedulerRuns / ListSandboxes 建立基线
5. 把缓冲的 live 事件幂等叠加到基线上（按 event.id 去重，按 occurred_at 防回退）
6. 持续消费；每处理完一个事件就更新本地 cursor
```

先订阅、后对账，可以避免“对账完成到订阅建立之间”的窗口丢事件；代价是应用要能处理“基线 + 稍旧事件”的幂等叠加。

### 5.2 断线重连

```
1. WatchEvents(cursor=last_processed_cursor)
2. 如果收到 SYNC_REQUIRED：
   - 用 List/Get API 重建相关 project/run/scheduler/sandbox 状态
   - 继续消费同一连接上的 live 事件（server 已切到 head）
3. 如果收到 GAP：
   - 控制面事件：记录丢失区间，触发一次对账
   - 日志：按每个 run 的 last_offset 调 FollowRunLogs 回填
4. 正常事件：按 event.id 去重后应用
```

### 5.3 日志回填

```
对每个 run：
  last_offset = 已处理的 run.log 最大 offset
  收到 offset 跳变 / GAP / 重连
      -> FollowRunLogs(project_id, run_id, start_offset=last_offset, follow=true)
      -> 从文件补齐，再切回实时事件（按 offset 去重）
```

### 5.4 连接与代理注意事项

- 应用应保持单条 `WatchEvents` 连接，不要按 project 或 subject 开多条。
- 如果经过反向代理，必须关闭响应缓冲并放大空闲超时（`X-Accel-Buffering: no` 已由 `PrepareStreamingHeaders` 设置，但 nginx/网关侧也要配置）。
- 心跳用于检测半开连接；应用超过 2 个心跳周期没有收到任何帧就重连。
- HTTP/1.1 足够；如果同时要 bidi attach，才需要 HTTP/2。

---

## 6. 分期实施计划

### 6.1 Phase 1（MVP，推荐先做）

范围：

- `pkg/eventstream`：Event、Broker、Subscription、filter、cursor、ring、gap、idle 生命周期。
- proto `EventService` + `WatchEvents` + `GetEventStreamCapabilities`；`pkg/agentcompose/api/event_stream.go`；DI 注册和路由。
- 事件源：`run.status`、`scheduler.run.status`、`scheduler.event`、`sandbox.status`、`sandbox.event`、`run.log`。
- `include_logs` 开关；日志不进 ring；`FollowRunLogs` 回填文档。
- 配置、`.env.example`、错误契约（`WatchEvents` 归为 `rpcRead`）、单测 / handler 测试 / race。
- `sandbox.cell.output` 可以放在 Phase 1 末尾或 Phase 1.5（它只需要 StreamBroker 的全局 listener，但需要 project 解析缓存）。

预计工作量：核心 broker + API 约 5–7 人日；事件源接线约 4–6 人日；测试/文档/联调约 3–4 人日。总计约 **2–3 人周**（不含 webhook、per-project auth）。

验收：

- 不开启时零行为变化、零常驻 goroutine。
- 一个应用连一条流，能收到新 project run 的 started/terminal、scheduler run started/completed、scheduler event、sandbox 状态变化、run 日志 chunk。
- 慢消费者不会阻塞 run 日志写入（压测/单测证明）。
- 断线重连在 ring 窗口内不丢控制面事件；窗口外收到 `SYNC_REQUIRED`。
- `go test -race` 通过。

### 6.2 Phase 2（可选）：Webhook delivery sink

只有 §3.3 的条件成立才做。复用 Phase 1 的 `eventstream` 事件模型和过滤，新增：

- `EventSubscription` 资源 + CRUD API（url、secret、filters、enabled）。
- `event_outbox` 表（只存控制面事件；日志不落 outbox）+ delivery worker（claim/lease、重试、DLQ、幂等键）。
- HMAC 签名、时间戳、重放窗口；URL allowlist、拒绝私网/链路本地地址、HTTPS 默认强制、禁止重定向。
- 每个订阅独立的 worker / 队列，慢订阅者不影响其他订阅者。
- 配对 SQLite/PostgreSQL migration + ent schema + DAO 重新生成。
- 预计 **2–3 人周**（安全评审另计）。

具体形态建议：

```proto
service EventSubscriptionService {
  rpc CreateEventSubscription(CreateEventSubscriptionRequest) returns (EventSubscription);
  rpc GetEventSubscription(GetEventSubscriptionRequest) returns (EventSubscription);
  rpc ListEventSubscriptions(ListEventSubscriptionsRequest) returns (ListEventSubscriptionsResponse);
  rpc UpdateEventSubscription(UpdateEventSubscriptionRequest) returns (EventSubscription);
  rpc DeleteEventSubscription(DeleteEventSubscriptionRequest) returns (DeleteEventSubscriptionResponse);
}

message EventSubscription {
  string id = 1;
  string name = 2;
  string url = 3;
  bool enabled = 4;
  repeated string project_ids = 5;
  repeated string event_types = 6;
  EventSeverity min_severity = 7;
  bool include_logs = 8;
  // secret 只在创建/更新时写入，读取时只返回是否已设置。
  bool secret_set = 9;
  string last_error = 10;
  google.protobuf.Timestamp last_delivery_at = 11;
}
```

投递实现要点：

- **只对控制面事件做 durable outbox**。`run.status`、`scheduler.run.status`、`scheduler.event`、`sandbox.status`、`sandbox.event` 进入 `event_outbox`；`run.log` / `sandbox.cell.output` 只走实时批处理（或由订阅者显式选择“日志也投递”，但仍不保证不丢）。
- 每个 subscription 一个逻辑队列 / worker；按 `sequence` 顺序投递，单条 in-flight，失败按指数退避重试，超过上限进 DLQ 并记录 `last_error`。
- 请求头：`X-Agent-Compose-Event-Id`、`X-Agent-Compose-Delivery-Id`、`X-Agent-Compose-Timestamp`、`X-Agent-Compose-Signature: sha256=<hmac>`。签名内容包含 timestamp + body，接收方校验时间窗口防重放。
- 幂等：接收方按 `Event-Id` 去重；daemon 可能因重试重复投递。
- SSRF：注册时解析 URL 并拒绝私网 / 链路本地 / 元数据地址；运行时不跟随重定向；默认只允许 https；提供 operator 级 allowlist 覆盖。
- 不要把 secret 写进日志；存储方式参考现有 webhook source 的签名 secret 处理。

不建议在 Phase 2 之前做 webhook：可靠性设施的成本远高于事件模型本身，而且做完 Stream 后，webhook 只是“同一事件的另一个出口”。

### 6.3 Phase 3（按需）

- 持久化 replay（超出内存 ring 的 cursor 恢复）。
- per-project token / scope。
- 多 daemon / HA 下的外部总线（NATS/Redis Streams）。
- `runlog` JSONL 结构化事件的完整字段（tool call/result、usage、turn）。
- 事件类型扩展到 image build、daemon recovery、event dispatch dead-letter 等。

---

## 7. 测试计划

### 7.1 Unit（`pkg/eventstream`）

- 发布/订阅、按 project/type/severity/include_logs 过滤。
- ring 重放：cursor 在窗口内、窗口外、epoch 不匹配。
- 慢消费者：buffer 满时 publish 不阻塞，dropped 计数和 gap 正确。
- idle：无订阅超时后 ring 清空、epoch 轮换、再次订阅行为正确。
- 最大订阅数 / 非法过滤条件 / 关闭状态。
- 并发：publish 与 subscribe/unsubscribe/close 并发，`-race` 通过。
- 注入 fake clock 和 deterministic epoch，避免时间相关 flaky。

### 7.2 Handler（`pkg/agentcompose/api`）

- 关闭时 `FailedPrecondition`；capabilities `enabled=false`。
- `HELLO`、批处理、`max_batch_events` 夹取、心跳。
- 客户端取消后订阅释放（active count 归零）。
- `SYNC_REQUIRED` / `GAP` 的 frame 顺序。
- 非法 event type / 过多 project_ids / 超过最大订阅数。

### 7.3 Integration

- 真实 `httptest` + Connect client：订阅、发布、接收、断线重连 cursor。
- 事件源接线：scheduler event 落库后能收到；run 状态迁移后能收到；`RunLogHub` chunk 能收到；`StreamBroker` 的 sandbox updated/event 能收到。
- 慢消费者集成：一个订阅不读，另一个正常读；正常订阅不受影响。

### 7.4 Race / 质量门禁

- `go test -race ./pkg/eventstream/... ./pkg/agentcompose/api/...`
- `task lint`、`task build`、`task test`
- 涉及公开文档时 `task docs:build`；涉及 proto 时 `task generate:proto` 并提交生成物。

### 7.5 可选 E2E

- 启动真实 daemon（`EVENT_STREAM_ENABLED=true`），apply project，运行一次 command/agent，订阅并断言 `run.status`、`run.log`、`scheduler.event`。
- 该测试依赖 Docker/KVM，不作为 PR 阻塞门禁；按 `TESTING.md` 的 E2E 规则放 `test/e2e`。

---

## 8. 风险与开放问题

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 日志量过大 | 内存/CPU/网络压力；慢消费者拖垮 daemon | `include_logs` 默认关闭；日志不进 ring；有界 buffer + 丢弃 + gap；offset 回填 |
| 慢消费者 | 事件积压 | 每订阅独立 buffer，publish 非阻塞；最大订阅数；监控 dropped |
| 半开连接 / 不读数据的客户端 | stream.Send 阻塞，占用订阅名额 | 限制最大订阅数；依赖 TCP/代理超时；文档要求应用及时读取；必要时增加发送 watchdog |
| project 归属缺失 | project 过滤漏掉 sandbox 事件 | 优先从 run/scheduler 上下文取；sandbox 用 TTL 缓存解析；文档说明边界 |
| 反向代理缓冲/超时 | 事件延迟或连接断开 | `PrepareStreamingHeaders` + 部署文档；应用心跳重连 |
| 重启/空闲回收导致 cursor 失效 | 应用漏事件 | `SYNC_REQUIRED` + 现有 List/Get 对账；文档给出客户端流程 |
| 认证粒度粗 | 订阅流暴露所有 project | 明确过滤≠授权；Phase 3 再做 per-project scope |
| runlog 格式迁移 | `run.log` payload 变化 | `format` + `entry_type` 字段；应用忽略未知类型；保持 `offset` 语义。注意 JSONL 的 offset 需要 Writer 暴露文件字节偏移，见 §4.3 |
| 未来多副本 | 进程内总线只看到本副本 | 当前明确单实例；HA 需要外部总线，列 Phase 3 |
| 事件源遗漏 | 某些执行路径没有 publish | 用 hub 全局 listener 覆盖大部分路径；列事件目录和接入点；集成测试按源断言 |

需要产品/接入方确认的决策点：

1. **应用能否维持长连接？** 能 → Phase 1 足够；不能 → 需要 Phase 2 webhook。
2. **是否要求应用宕机期间控制面事件不丢？** 是 → 需要 outbox / Phase 2；否 → ring + 对账足够。
3. **日志需要多细？** 原始 chunk 足够，还是必须结构化 tool call / usage / turn？后者依赖 `runlog` 接线。
4. **是否需要 per-project 授权？** 若上层应用是多租户的，过滤不等于授权，需要单独设计 token scope。
5. **预期并发订阅数、project 数、日志 QPS？** 用于确定 buffer / ring / 最大订阅数的默认值。

---

## 9. Phase 1 实现落点

Phase 1 已按本方案落地，主要落点如下：

| 主题 | 实现 |
| --- | --- |
| proto | `EventService.GetEventStreamCapabilities` / `WatchEvents`，事件信封与 control 帧 |
| broker | `pkg/eventstream`：过滤、cursor/epoch、replay ring、每订阅有界队列、gap、idle 回收 |
| API | `pkg/agentcompose/api/event_stream.go`：鉴权后的 Connect server-stream、批量、心跳、`SYNC_REQUIRED`、`GAP` |
| 配置 | `pkg/config/event_stream.go`：`EVENT_STREAM_*`，默认关闭，启动时校验 |
| 组装 | `pkg/agentcompose/app/event_stream.go`：broker 生命周期与领域事件到事件模型的桥接 |
| run | `Coordinator` / `CompletionManager` 的状态观察者；`RunLogHub` 全局 observer |
| scheduler | `Controller` / `RunExecutor` 的观察者；事件落库后发布 |
| sandbox | `StreamBroker` 全局 observer；project 归属由 run 状态/日志缓存，无法归属时留空 |
| 测试 | broker、API、bridge、config、run/scheduler/sandbox observer、认证、race |

与设计稿的已知差异：sandbox 的 `project_id` 采用 run 状态/日志形成的缓存，而不是逐事件数据库解析；日志事件只实时投递、不进 ring；Phase 2 webhook 未实现。

## 10. 附录：关键代码位置

| 主题 | 位置 |
| --- | --- |
| Connect 服务与流式 RPC 定义 | `proto/agentcompose/v2/agentcompose.proto` |
| 流式响应头 | `pkg/agentcompose/api/transport.go` |
| `FollowRunLogs`（当前轮询 + hub） | `pkg/agentcompose/api/run_handler.go` |
| `WatchSandbox` | `pkg/agentcompose/api/sandbox.go` |
| scheduler 有限扫描 | `pkg/agentcompose/api/project_scheduler_stream_handler.go` |
| `WatchProject` 未实现 | `pkg/agentcompose/app/project_controller.go` |
| run 日志 hub | `pkg/runs/run_log_hub.go` |
| run 日志 chunk 发布 | `pkg/runs/execution_log.go` |
| run 状态迁移 | `pkg/runs/coordinator.go`、`pkg/runs/completion.go` |
| scheduler 事件落库 | `pkg/schedulers/controller.go`（`AddSchedulerEventRecord`） |
| scheduler run 生命周期 | `pkg/schedulers/run_lifecycle.go` |
| sandbox fan-out | `pkg/sandboxes/stream.go` |
| sandbox lifecycle notifier | `pkg/sandboxes/lifecycle.go` |
| runlog 写入与 `OnAppend` | `pkg/runlog/writer.go` |
| 现有 topic event outbox（Phase 2 参考） | `pkg/events/dispatcher.go`、`pkg/storage/configstore/topic_event_store.go` |
| daemon 认证 | `cmd/agent-compose/daemon_auth.go` |
| 单实例部署 | `charts/agent-compose/README.md` |
| 传输矩阵 | `docs/pages/connect-transport-matrix.md` |
| 配置模式 | `pkg/config/event_delivery.go` |
