# Scheduler Event 存储改造方案

对应 [issue #565](https://github.com/chaitin/agent-compose/issues/565)（"design(storage): reduce
large log and output payloads persisted in SQLite"）——该 issue 指出 `scheduler_event`/`project_run`
等表里存了不少跟 artifact 文件重复的完整日志/输出内容，怀疑增加了库体积、备份、查询、prune 成本，
但本身是未经代码核实的调查性 issue。本方案聚焦 issue 提到的 `scheduler_event` 部分，先用真实数据
核实问题的真实构成（详见 §2.1），再给出范围收紧后的第一期实施方案；`project_run`、`scheduler_run`
以及 issue 里提到的其余表暂不在本方案范围内。

## 1. 目标与范围

本方案只改造 `scheduler_event`，暂不修改 `scheduler_run` 的存储行为。

### 1.1 本期实施范围

本期实施以下两项：

1. 处理 `loader.command.completed` / `scheduler.command.completed` 写入完整命令输出的问题：短输出保持原样，长输出在 `scheduler_event.message` 中只保留有界头尾预览，完整 stdout/stderr/output 继续保存在现有 sandbox cell artifact；
2. **保证 `agent-compose scheduler logs` / `scheduler logs --json` 的行为不变**，继续返回完整日志内容，不因为第 1 项而截断。这条命令走的是 `StreamProjectSchedulerEvents` RPC（`cli_scheduler_stream.go:94`），实现方式是在这个 RPC 的服务端处理逻辑里加一层读取顺序：**先读 DB 的 `message`；如果 `payload_json.messageTruncated=true`，再按事件上已有的 `linked_sandbox_id`/`linked_cell_id` 去读 sandbox cell artifact（`stdout.txt`/`stderr.txt`/`output.txt`）重建完整内容后再返回**。DB 里存的 `message` 值本身不变（依然是第 1 项截断后的预览），变的只是这一个 RPC 在返回给调用方之前做的重建。

   这不是新增一个独立、对外暴露的 artifact 读取 API/端点，只是 `StreamProjectSchedulerEvents` 这一个已有 RPC 内部的读取逻辑变化，不涉及 Proto 字段变更，也不需要请求级别的开关：已经确认 `scheduler logs` 用的 `StreamProjectSchedulerEvents` 和 UI 会用的 `ListProjectSchedulerEvents`/`ListSchedulerEvents`（一次性 List 接口）是三个独立的 RPC 方法，而 UI 侧目前唯一可能调用后两者的代码路径（`agent-compose-ui/src/api/loaders.ts:463` 的 `listAutomationEvents`）在整个前端仓库里零调用方（`AutomationRunDetail.svelte` 没有渲染任何事件时间线内容），因此这两个 List 方法本期不需要做任何改动，继续原样返回 DB 里截断后的 `message` 即可，不会跟这次改造冲突。

本期明确不做：

- 不删除 `loader.run.completed` / `scheduler.run.completed` 的 `payload_json.resultJson` 重复副本（原计划中的本期第三项，详见 §12.2：该字段被 `scheduler logs --json` 逐条透传，删除会改变这条 CLI 命令的输出契约，收益（约 11 MiB）相对本期主项（约 257 MiB）也小很多，评估后挪到后续演进，作为一次有文档、有版本号的变更单独排期）；
- 不修改 `scheduler_run` 表或其写入逻辑；
- 不修改 `scheduler.log()`；
- 不去重 Agent/LLM event payload；
- 不迁移 sandbox RPC request/response；
- 不新增独立、对外暴露的 artifact 读取 API/端点（第 2 项里 `StreamProjectSchedulerEvents` 内部的 fallback 读取属于该项范围内的实现细节，不支持 HTTP Range、下载、归档读取，这些仍留给 §6 后续演进）；
- 不新增 archive 读取能力；
- 不新增自动 retention；
- 不修改现有 Proto 字段或数据库表结构。

本文后续章节中超出上述两项的内容均作为后续演进建议，不属于本期验收范围。

目标：

- 保留 Scheduler 事件时间线、CLI 日志和前端展示能力；
- 阻止完整 command 输出持续撑大 SQLite；
- 删除 `scheduler_event` 中可以从 `scheduler_run` 或 artifact 获得的重复正文；
- 保持现有表结构和 Proto API 兼容；
- 为历史数据提供可审计的压缩流程：默认 dry-run 可见影响范围、遇到读不到 artifact 的行会跳过而不是
  强行截断，但压缩本身不提供自动回滚——已截断的行如需恢复，依赖执行前的 `data.db` 文件备份（见 §9）。

职责边界：

```text
scheduler_run
= 一次 Scheduler Run 的权威状态、输入、结果和错误

scheduler_event
= 轻量、可查询的事件时间线

artifact
= 完整且体积不可控的执行输出
```

`scheduler_event` 最终只保存：

```text
事件类型 + 级别 + 短摘要 + 结构化元数据 + 资源关联 + 时间
```

不再保存：

```text
完整 command stdout/stderr
完整 Scheduler Run result
重复的 Agent/LLM 正文
无界 RPC request/response
```

## 2. 现状与主要问题

当前数据库中，`scheduler_event.message` 是最大的单一逻辑数据来源。绝大部分空间来自：

```text
loader.command.completed
scheduler.command.completed
```

这两类事件会把 `result.Output`、`result.Stdout` 或 `result.Stderr` 整体写入 `message`。与此同时，完整 command 输出已经存在于 sandbox cell artifact：

```text
<SANDBOX_ROOT>/.../<sandbox-id>/state/cells/<cell-id>/
  stdout.txt
  stderr.txt
  output.txt
```

数据库中已经保存了定位这些文件所需的逻辑引用：

```text
linked_sandbox_id
linked_cell_id
```

另外，`scheduler.run.completed.payload_json.resultJson` 与以下内容重复：

```text
scheduler_run.result_json
<scheduler-run-artifacts>/result.json
```

### 2.1 devboard 环境实测数据

以下数据取自 devboard 环境真实 `data.db` 文件，采样于 2026-08-17，文件大小 717,230,080 字节 ≈ 684 MiB，
用于验证上述判断，不是代码推测。

**`scheduler_event` 占整个数据库文件的比例：**

```text
db 文件总大小：           717,230,080 字节（≈ 684 MiB）
scheduler_event 表大小： 349,954,048 字节（≈ 334 MiB，dbstat 统计，含索引）≈ 全库的 49%
scheduler_event 行数：    96,427
```

**`message` 字段长度分布（决定"是行数多还是个别行大"）：**

| 区间 | 行数 | 占行数比例 | 总字节 |
|---|---:|---:|---:|
| <256B | 88,564 | 91.8% | 2.4 MiB |
| 256B–1KiB | 2,900 | 3.0% | 2.0 MiB |
| 1KiB–8KiB | 3,791 | 3.9% | 15.0 MiB |
| 8KiB–64KiB | 855 | 0.9% | 18.1 MiB |
| ≥64KiB | 317 | 0.3% | 245.0 MiB |

91.8% 的行体积可以忽略不计，**0.3% 的行占了 87% 的 `message` 总字节**（282.5 MiB 中的 245.0 MiB），最大单行 4,153,756 字节（≈4 MiB）。结论：这是"个别行过大"的问题，不是"行数过多"的问题，缩容收益集中在少数事件类型上，靠 retention（减少事件数量）边际收益有限。

**按事件类型拆分 `message` 字节数（Top 2 主导全表）：**

```text
loader.command.completed      11,794 行   244.9 MiB
scheduler.command.completed    2,384 行    29.5 MiB
——合计 274.4 MiB，占全表 message 总字节（282.5 MiB）的 97%
```

`loader.*`（改名前的旧事件类型名）与 `scheduler.*`（改名后）在这份真实数据里**同时存在**且旧名称占多数，说明历史数据/线上环境里 `loader_*` → `scheduler_*` 的改名没有覆盖已写入的 `type` 字段值。任何按事件类型做处理的代码（本期的 message 截断、后续的 payload 去重）都必须同时兼容两种前缀，不能假设线上只有 `scheduler.*`。

**`payload_json` 字段整体不是问题：**

全表 `payload_json` 总字节仅 23.5 MiB（vs `message` 的 282.5 MiB），没有类似的极端厚尾分布，跟 §2 判断的"command 事件 payload 本身是精简的"一致。

**`resultJson` 重复验证（实测，非推测）：**

按 `scheduler_id + scheduler_run_id = run_id` 关联对比 `payload_json.resultJson` 与 `scheduler_run.result_json`：

```text
scheduler.run.completed   18,018 / 18,018 行完全一致（100%）
loader.run.completed       7,513 /  7,513 行完全一致（100%）
```

两种类型合计占用 payload 字节：4,512,312 + 7,428,980 ≈ 11.94 MiB，与 §13 中"约 11 MiB"的估算吻合，且已确认是逐字节重复，删除后不会丢数据（权威副本 `scheduler_run.result_json` 不受影响）。

**4 KiB 头尾预览截断的实际收益估算：**

```text
截断前（loader.command.completed + scheduler.command.completed 的 message 总字节）：274,444,931 字节
截断后估算（≤4096 字节原样保留，>4096 字节按 4096+提示文本 估算）：17,086,114 字节
预计减少：≈ 257 MiB
实际会被截断的行数：1,369 / 14,178（约 9.7%），其余 90.3% 的行本来就在阈值内，不受影响
```

## 3. 兼容性原则

第一阶段不修改 `scheduler_event` 表结构，也不修改 Proto：

```text
event_id
scheduler_id
scheduler_run_id
trigger_id
type
level
message
payload_json
linked_sandbox_id
linked_cell_id
linked_agent_thread_id
created_at
```

保持：

- `SchedulerEvent.message` 继续存在；
- `payload_json` 继续是合法 JSON；
- CLI 和 UI 继续展示 `message`；
- 短 command 输出保持原样；
- `scheduler.log()` 继续写数据库；
- 历史 `loader.*` 事件保持可读，不强制改名；
- **`agent-compose scheduler logs` / `scheduler logs --json` 在 artifact 仍可读时，输出内容字节级不变**：DB 里的 `message` 可能被截断，但这条命令通过 `StreamProjectSchedulerEvents` 内部的 artifact fallback（见 §1.1 第 2 项、§8）拿到的仍是完整正文，脚本/`jq` 消费方无需感知这次改造。**已知例外**：如果对应 sandbox 已经归档或被 `scheduler prune` 清理掉（见 §7、§10），artifact 读不到，这条命令会退化为返回 DB 里的截断预览 + `artifactAvailable:false` 标记（见 §8、§11）——这种情况下输出不是字节级不变的，是本期设计里显式承认、非静默的降级，不是"完整正文不可达"的意外 bug，也不违反"不新增 archive 读取能力"这条本期边界。

## 4. 统一大小限制

建议增加全局兜底：

```text
message 全局硬上限：16 KiB
payload_json 全局硬上限：256 KiB
```

不同事件使用更精确的上限：

| 事件 | message 上限 |
|---|---:|
| `scheduler.command.completed` | 4 KiB |
| `scheduler.agent.completed/failed` | 16 KiB |
| `scheduler.llm.completed/failed` | 16 KiB |
| `scheduler.log` | 16 KiB |
| 生命周期和固定摘要 | 无需额外截断，受全局上限保护 |

文本截断采用头尾预览：

```text
前 2 KiB
\n… N bytes omitted; open full artifact …\n
后 2 KiB
```

实现必须按 UTF-8 安全边界截断，不能切坏中文或 Emoji。

上表里给出的上限（如 `scheduler.command.completed` 的 4 KiB）指的是头尾原文数据的预算，不含截断
提示文字本身；实现时最终写入 `message` 的总字节数会略高于表格数值，这一点在写测试断言"最大长度"
时需要按实际拼接结果而不是表格数字去核对。

## 5. 各事件类型的存储策略

### 5.1 `scheduler.command.completed`（本期）

`message`：

- 不超过 4 KiB：保持原样；
- 超过 4 KiB：保存前 2 KiB、截断提示和后 2 KiB；
- 不再把完整 stdout/stderr 写入数据库。

`payload_json` 保留精简元数据：

```json
{
  "mode": "shell",
  "command": "",
  "args": [],
  "cwd": "",
  "exitCode": 0,
  "success": true,
  "stdoutTruncated": false,
  "stderrTruncated": false,
  "outputTruncated": false,
  "messageTruncated": true,
  "outputBytes": 4153756,
  "sandboxId": "...",
  "cellId": "..."
}
```

完整正文继续保存在现有 sandbox cell artifact 中。数据库不保存主机绝对路径，通过 `linked_sandbox_id + linked_cell_id` 定位。

`scheduler.command.failed` 本期保持现状，是否采用相同预览规则放到后续单独评估。

**写入顺序要求（硬性）**：只有确认 sandbox cell artifact（`stdout.txt`/`stderr.txt`/`output.txt`）已经落盘成功，才允许截断 `message` 并把 `messageTruncated` 置为 `true`；如果 artifact 写入失败，`message` 必须保持全文不截断，宁可让这一行变大也不能让数据永久丢失。这一条是 §1.1 第 2 项（`scheduler logs` fallback 读取）成立的前提——`scheduler logs` 的完整性现在依赖这份 artifact 确实存在，如果写入顺序反了，会导致 DB 和 artifact 同时没有完整内容，且没有任何补救手段。

### 5.2 `scheduler.run.failed/canceled/skipped`

- `message` 保留错误、取消或跳过原因，最大 16 KiB；
- payload 不再重复完整错误正文；
- daemon 中断等分类信息可以继续保留：

```json
{"reason":"daemon_interrupted"}
```

### 5.3 `scheduler.run.started`

保持现状：

```text
message = "scheduler run started"
payload_json = {"source":"webhook"}
```

该内容很小，不是优化对象。

### 5.4 `scheduler.log`

继续写数据库，因为这是用户脚本自定义日志的主要来源，目前没有文件副本。

限制：

```text
message 最大 16 KiB
payload 最大 256 KiB
```

超限时返回明确错误，不静默生成损坏或不完整的 JSON：

```text
scheduler.log message exceeds 16384 bytes
scheduler.log payload exceeds 262144 bytes
```

建议增加每个 Run 的总量保护：

```text
最多 10,000 条 scheduler.log
或累计最多 10 MiB
```

达到限制后只记录一次：

```text
scheduler.log.limit_reached
```

### 5.5 `scheduler.agent.completed/failed`

`message` 保留最终文本的最多 16 KiB 预览。

当前 payload 中可能同时包含 `Text`、`Output`、`FinalText` 和 `JSON`，其中多个字段可能是同一正文的重复副本。建议 payload 只保留执行元数据：

```json
{
  "agent": "...",
  "success": true,
  "exitCode": 0,
  "stopReason": "completed",
  "sandboxId": "...",
  "cellId": "...",
  "agentThreadId": "...",
  "textTruncated": true,
  "textBytes": 123456
}
```

Event Payload 不再重复保存完整 `Text`、`Output` 和 `FinalText`。

### 5.6 `scheduler.llm.completed/failed`

`message` 保留最多 16 KiB 的文本预览。

payload 不再重复完整 `Text`，只保留模型和结果元数据：

```json
{
  "model": "...",
  "success": true,
  "textTruncated": true,
  "textBytes": 123456
}
```

### 5.7 `scheduler.sandbox.rpc.completed/failed`

`message` 保留 method 和结果摘要：

```text
GetSandbox completed
GetSandbox failed: ...
```

第一阶段对 payload 增加 256 KiB 硬限制。最终形态建议为：

```json
{
  "method": "GetSandbox",
  "requestBytes": 1234,
  "responseBytes": 5678,
  "payloadTruncated": false
}
```

如果完整 request/response 必须长期审计，应写入 Scheduler Run artifact。不能截断 JSON 字符串后继续将其作为合法 JSON 使用。

### 5.8 Sandbox 生命周期事件

保留精简事件：

```text
scheduler.sandbox.created
scheduler.sandbox.resumed
scheduler.sandbox.stopped
scheduler.sandbox.stop_failed
```

payload 只保存 sandbox ID 等资源元数据。它们体积很小，同时承担 trace 和资源关联职责。

### 5.9 Event Publish 事件

保留：

```text
scheduler.event.published
scheduler.event.publish.failed
```

payload 只保存：

```json
{
  "eventId": "...",
  "sequence": 123,
  "topic": "...",
  "correlationId": "..."
}
```

不复制被发布 Topic Event 的完整业务 payload。

### 5.10 Deprecated warning

保留 `scheduler.deprecated_alias.warning` 的短 warning。它占用很小，可以通过 retention 自然清理。

## 6. Artifact 读取接口

本节说的是**对外暴露、给前端/下载场景用的公开接口**，属于后续演进（§12.2），本期不做。跟本期
§1.1 第 2 项里 `StreamProjectSchedulerEvents` 内部为 `scheduler logs` 做的 fallback 读取是两回事：
后者是一个已有 RPC 内部的读取逻辑，不对外暴露成独立端点，也不支持 Range/下载/归档；本节这套才是
真正意义上的"新增 artifact 读取 API"。

新增受控的 artifact 读取接口：

```http
GET /api/sandboxes/{sandboxId}/cells/{cellId}/artifacts/stdout
GET /api/sandboxes/{sandboxId}/cells/{cellId}/artifacts/stderr
GET /api/sandboxes/{sandboxId}/cells/{cellId}/artifacts/output
```

要求：

- 支持 HTTP Range、offset/limit 或流式响应；
- 支持下载；
- 只允许固定 artifact 名称；
- 校验 sandbox/cell 归属；
- 防止路径穿越；
- 文件不存在时明确返回 `NotFound`；
- sandbox 已归档但当前不支持读取时，返回明确的 `Archived` 状态。

前端默认展示 DB 中的预览，并提供按需读取全文的入口：

```text
[查看完整 stdout] [查看 stderr] [下载日志]
```

`scheduler logs`/`scheduler logs --json` 本期已经默认返回完整内容（见 §1.1 第 2 项、§8 的 fallback
逻辑），不需要额外的 `--full` 参数；这个默认行为只在对应 artifact 还能读到时成立——sandbox 一旦被
归档或清理（§7），CLI 现在拿到的也只有 DB 预览，跟前端会看到的一样。等这里的公开 artifact 读取
API 和 §7 的归档读取能力都做完之后，CLI/前端才能在归档场景下也把内容找回来。

## 7. Sandbox 归档的处理

启用 `SANDBOX_RETENTION_TTL` 后，sandbox 的 `state/cells` 会进入：

```text
<SANDBOX_ARCHIVE_ROOT>/<sandbox-id>/<archive-id>.tar.zst
```

归档中仍包含：

```text
sandbox/state/cells/<cell-id>/stdout.txt
sandbox/state/cells/<cell-id>/stderr.txt
sandbox/state/cells/<cell-id>/output.txt
```

但当前版本没有 archive list、download、restore 或单文件读取 API。第一阶段可以在归档后只保留 DB 预览，并明确提示完整日志已归档。

若未来需要从归档读取单个日志，应增加专用 Archive Artifact API。由于 `.tar.zst` 通常需要顺序解压扫描，不能把它当作高频、低延迟读取路径。

## 8. 代码组织

事件内容策略属于 `pkg/schedulers`，不属于 storage。

建议新增：

```text
pkg/schedulers/event_content.go
```

包含纯函数，例如：

```go
func CommandEventMessage(output string) EventMessage
func AgentEventMessage(text string) EventMessage
func LLMEventMessage(text string) EventMessage
func ValidateSchedulerLog(message string, payload any) error
```

返回：

```go
type EventMessage struct {
    Text          string
    OriginalBytes int
    Truncated     bool
}
```

`pkg/storage/configstore` 只负责持久化，不判断不同事件类型的业务语义。

在最终写库边界增加防御性兜底：

- message 超过全局硬限制时拒绝或安全截断，且必须在确认 artifact 落盘成功之后才截断（见 §5.1 写入顺序要求）；
- payload 超过全局硬限制时拒绝；
- 调用点应在到达 storage 前完成事件类型对应的精确处理。

`StreamProjectSchedulerEvents` 的 fallback 读取（§1.1 第 2 项）建议同样放在 `pkg/schedulers` 里，
例如新增一个纯函数：

```go
func ResolveEventMessage(event domain.SchedulerEvent) (text string, truncated bool, artifactAvailable bool)
```

- `payload_json.messageTruncated` 为 `false`：直接返回 DB 的 `message`，不做任何文件 I/O；
- 为 `true`：按 `linked_sandbox_id`/`linked_cell_id` 读取 sandbox cell artifact；读到返回全文；
  读不到（sandbox 已归档/已被 prune 清理）返回 DB 里的截断预览 + `artifactAvailable:false`，
  不能返回空字符串，调用方（CLI 输出）需要能区分"完整"和"读取失败，只有预览"两种情况。
`StreamProjectSchedulerEvents` 的 handler 在序列化响应前调用这个函数替换 `message` 字段；
`ListProjectSchedulerEvents`/`ListSchedulerEvents`（UI 路径，目前零消费）不调用，原样返回 DB 值。

## 9. 历史数据迁移

提供专门维护命令，默认 dry-run：

```bash
agent-compose scheduler compact-events
agent-compose scheduler compact-events --force
```

Dry-run 输出：

```text
匹配事件数
待截断 command message 数
待删除 run result payload 数
预计可减少的逻辑字节数
```

必须同时处理历史旧类型和当前新类型：

```text
loader.command.completed
loader.run.completed

scheduler.command.completed
scheduler.run.completed
```

第一版历史迁移只做本期范围内风险最低的一项：

1. 截断新旧 command message（`loader.command.completed` / `scheduler.command.completed`）。

删除新旧 `run completed` payload 中的 `resultJson` 随 §12.2 一并移到后续演进，不在本期历史迁移范围内。
Agent/LLM 历史 payload 同样不属于本期；如后续实施，需要单独处理不同历史版本的 JSON 结构。

迁移要求：

- 默认 dry-run；
- 分批事务，避免长时间锁库；
- 幂等；
- 不处理 running Run；
- 不修改未知 payload；
- 截断某一行前必须先确认对应 sandbox cell artifact（`stdout.txt`/`stderr.txt`/`output.txt`）确实存在
  且可读；读不到（sandbox 已归档/已被清理）就跳过这一行、保留原文不截断，计入 dry-run/执行报告的
  "跳过"计数，不能因为是历史数据就放松这条安全底线（呼应 §5.1 的写入顺序要求）；
- **截断历史行时必须同步把 `payload_json.messageTruncated` 置为 `true`**（并按 §5.1 的形状补上
  `outputBytes`/`outputTruncated` 等元数据）：历史行是本次改造前写入的，`payload_json` 里通常没有
  `messageTruncated` 这个 key，反序列化后是零值 `false`；§8 的 `ResolveEventMessage` 只在这个字段
  为 `true` 时才会去读 artifact 重建全文，为 `false` 就直接返回 DB 的 `message`、不做任何文件 I/O。
  如果迁移只截断了 `message` 而不写这个标志，历史事件即使 artifact 仍然可读，`scheduler logs` 也会
  一直返回 DB 截断预览，等于让 §8 的 fallback 机制在历史迁移路径上失效，直接违反 §3"artifact 仍可读
  时输出字节级不变"的承诺。这个字段只写入迁移程序能识别的 command 事件 payload 结构（跟 command 的
  已知形状匹配），不匹配的按"不修改未知 payload"处理、原样跳过；
- 执行前提供备份提示；
- 报告实际减少的逻辑字节数。

清理完成后，SQLite 文件不会自动缩小。应在维护窗口执行：

```bash
sqlite3 source.db "VACUUM INTO 'compacted.db';"
sqlite3 compacted.db "PRAGMA quick_check;"
```

验证新库后再原子替换，并保留原数据库备份。

## 10. Retention

事件大小限制只能控制单条增长，不能控制历史总量；长期还是要靠现有 `scheduler prune`（按
scheduler_id/status/age 清理终态 Run 及其直属 `scheduler_event`、event delivery/link 和 artifact）
定期执行。配置自动 retention（保留天数策略、部署定时任务）本期不做，留给 §12.2 后续演进。

## 11. 测试要求

至少覆盖：

- 4 KiB 以下 command 输出完全不变；
- 超长 ASCII、中文和 Emoji 均能安全截断；
- 头尾内容和 omitted bytes 正确；
- command payload 包含原始大小和截断标志；
- artifact 写入失败时 `message` 不会被截断（写入顺序安全底线，见 §5.1）；
- `scheduler.log` 超限返回明确错误；
- payload 始终是合法 JSON；
- failed/canceled 仍能从 message 看到错误摘要；
- 历史 `loader.*` 迁移；
- 历史行 artifact 缺失/已归档时迁移会跳过、`message` 保持全文不截断；
- **历史行截断后 `payload_json.messageTruncated` 被置为 `true`，且 `scheduler logs`/`scheduler logs
  --json` 对这批迁移过的历史事件走 §8 fallback 仍能读到完整内容**（不能只测"DB 里 message 变短了"，
  要测到 CLI 端到端输出，覆盖"迁移后、artifact 仍可读"这个组合场景）；
- dry-run 不修改数据库；
- migration 重复执行结果一致；
- **`scheduler logs` / `scheduler logs --json` 对 `messageTruncated=true` 的事件仍返回完整内容**（走
  `StreamProjectSchedulerEvents` 的 fallback 读取，端到端测试，不只测 DB 层）；
- fallback 读取遇到 artifact 缺失/已归档时，返回明确的 `artifactAvailable:false` + 预览，而不是空
  字符串或悄悄变短的内容；
- `ListProjectSchedulerEvents`/`ListSchedulerEvents`（UI 路径）继续原样返回 DB 截断值，不做 fallback；
- artifact 存在、不存在和已归档的 API 行为；
- Scheduler Run prune 后 artifact 状态正确。

## 12. 实施顺序

### 12.1 本期

1. 为 command completed 定义 UTF-8 安全的 4 KiB 头尾预览规则；
2. 新写入的 `scheduler.command.completed` 超长 message 只保存预览和截断元数据，写入顺序上先确认
   artifact 落盘成功再截断（§5.1）；
3. 实现 `ResolveEventMessage`（§8）并接入 `StreamProjectSchedulerEvents`，保证 `scheduler logs` /
   `scheduler logs --json` 输出不变；`ListProjectSchedulerEvents`/`ListSchedulerEvents` 不改动；
4. 补充新写入行为、fallback 读取行为的单元测试和集成测试；
5. 提供历史 compact dry-run/force 能力，只处理新旧 command completed message；
6. 历史清理完成后，在维护窗口使用 `VACUUM INTO` 回收 SQLite 文件空间。

### 12.2 后续演进，不属于本期

1. 删除 `loader.run.completed` / `scheduler.run.completed` payload 中重复的 `resultJson`：
   目标状态是保留事件（`message = "scheduler run completed"`），payload 删除重复的 `{"resultJson":"..."}`，
   可以为空或只留 `{"resultAvailable":true}`，完整结果继续由现有 `scheduler_run.result_json` 提供；
   实测收益约 11 MiB，但会改变 `scheduler logs --json` 对这类事件的输出契约，需要先确定是当作有文档、
   有版本号的破坏性变更处理，还是保留兼容字段，再排期实施，并同步补上 CLI 端到端测试；
2. 去重 Agent/LLM completed payload；
3. 限制 `scheduler.log` 单条及每 Run 总量；
4. 迁移 sandbox RPC 大 payload；
5. 增加 artifact 按需读取 API；
6. 增加 archive artifact 读取能力；
7. 配置自动 retention。

## 13. 预期收益

根据 devboard 环境真实数据库分析（详见 §2.1，非代码推测）：

- command completed message 占全部 `scheduler_event.message` 字节的约 97%（`loader.command.completed`
  244.9 MiB + `scheduler.command.completed` 29.5 MiB，合计 274.4 MiB）；
- 按 4 KiB 头尾预览截断，预计将这部分从 274.4 MiB 降至约 17.1 MiB，减少约 257 MiB 逻辑数据，且只有约
  9.7%（1,369 / 14,178）的行会被实际截断，其余行原样保留；
- 再结合现有 freelist 回收（`VACUUM INTO`）和 Scheduler Run retention，可进一步缩小数据库文件并控制
  长期增长。

以上是本期（§1.1 范围内）的收益。`resultJson` 去重（§12.2）已确认是逐字节重复（18,018/18,018 +
7,513/7,513 完全一致），预计再减少约 11 MiB，但因涉及 `scheduler logs --json` 契约变更，本期不实施。

最终原则：

```text
scheduler_event 保存轻量时间线
scheduler_run 保持现有权威状态和结果
artifact 保存完整、体积不可控的执行输出
retention 控制历史总量
```
