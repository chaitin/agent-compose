# Run 日志与时间线（Run Timeline）

## 背景

君苦 log 久矣。

上层应用想知道一个 agent run 做了什么：调用了哪些工具、为什么失败、花了多少 token、最终产出是什么。AC 目前在 host 上写了很多份 log，但没有一份能回答这些问题。

**同一件事写了好几份。**

| 产物 | 位置 | 本质 | 读取方 |
|---|---|---|---|
| `stdout.txt` / `stderr.txt` / `output.txt` | `state/cells/<cell>/` | command 的输出流被存成三份：stdout 拆一份，stderr 拆一份，再合并一份。对 agent run 来说，stdout 是协议通道（带前缀的结果 JSON），stderr 是拍平的文本 transcript | `FollowRunLogs`（`run.LogsPath` 指向 output.txt）、`SandboxHistoryCell`、`schedulers/event_message.go` |
| `transcript.txt` | `state/runs/<run>/` | 交互路径和 project command run 的合并文本输出 | `FollowRunLogs` |
| `source.txt` / `exitcode.txt` / `command-result.json` | `state/cells/<cell>/` | 输入和结果，属于状态，不是 log | 崩溃恢复（`RecoverExecResultFromCellArtifacts`） |
| `agent-thread.json`、`AgentResume.ProviderLogPaths` | cell 目录、cell 记录 | resume 元数据和 provider 原生 jsonl 的 host 路径。每个 run 结束时都会遍历一遍 `~/.claude` | **没人读**，也不在 proto 里 |
| sandbox 事件 `agent.tool` / `agent.hook` | `state/events.jsonl` | `AgentTraceEvents` 从文本 transcript 里解析 `[tool:]` 标记得到：结构转成文本后，又从文本还原结构 | `ListSandboxHistory` / `WatchSandbox` |
| ExecService 的 `transcript.txt` | exec 目录 | proto 写明 Exec 是"非持久的"，但实现仍然落盘了 | 无 |

**最有用的那份反而没存下来。** runtime 已经有一套跨 provider 的统一事件模型（`runtime/javascript/src/agent-event.ts`，包括 `text_delta / tool_call / tool_result / usage / error…`），但存在两个问题：

- 只有交互路径接上了 `onEvent`（`interactive.ts:283`）。非交互路径（`RunAgent` / `StartAgentRun` / scheduler 触发）直接把这些事件丢掉了，只输出文本。
- 交互路径的事件只在 `AttachAgentRun` 和 `StreamAgentRun` 流上传过，不落盘，连接一断就没了。

**数据库里只有首尾，没有过程。** `project_run_event` 定义了 `AGENT_ACTIVITY`，但没有任何代码写入它。devboard 上 368 个 run 的分布：

| kind | 条数 |
|---|---|
| status | 359 |
| user_message | 348 |
| agent_message | 198 |
| agent_activity | 0 |

agent_message 少了约 170 条。失败、取消或没有最终回复的 run，库里只剩"说了什么"和"最终状态"，而上层最想看的恰恰就是这类 run。

**cell 和 run 不是一回事。** cell 是从早期 sandbox notebook 模型沿用下来的执行单位，产物放在 `state/cells/<cell_id>/`。非交互的 project run 底层会创建一个 cell，再把 cell 的结果映射回 run（`pkg/runs/execution.go:28`），所以两者是一对一的。但 scheduler run 里每调用一次 `scheduler.shell` / `scheduler.exec` 也会创建一个 cell，这类 cell 没有对应的 project run。devboard 上，每个 scheduler run 带 1 到 25 个这样的 cell，相关事件大约有 1.8 万条。交互式 run 和 project command run 则完全不创建 cell。

**日志的生命周期跟错了对象。** run 的日志放在 sandbox 目录下，`RemoveSandbox` 会把整个目录 `RemoveAll` 掉（`sandboxstore/sandbox_metadata.go:49`）。但 run 记录一直保留到 `RemoveRunHistory`。devboard 上 323 个记录了 `logs_path` 的 run，**只有 23 个对应的文件还在**（约 7%）。用一次性 sandbox 跑的 scheduler run，sandbox 一清理，日志就没了，run 却还能列出来。

**于是上层开始绕过 API。** ac-task-observer 以只读方式挂载 daemon 的数据目录，直接读 cell 文件和 `logs_path`，必要时还去翻 sandbox 的 `home/`，读 agent 原生的对话记录。它的文档里写明这样做是为了"弥补 RPC API 没有暴露的部分"。

## 目标与非目标

目标：

- 每个 run 有一条完整、有序、结构化的时间线，agent run 和 command run 用同一种格式，交互和非交互路径也用同一种格式。
- 上层只通过 API 读取，不依赖磁盘布局。
- 日志属于 run，跟着 run 的历史记录删除，不跟着 sandbox 删除。
- 一个 run 只有三个读取入口：状态、对话、过程，彼此不重叠，名字和功能对得上。
- `FollowRunLogs` 文本模式的契约保持不变。

非目标：

- 对外承诺 provider 原生 session（`~/.claude`、`~/.codex`）的格式。它是 provider 用来 resume 的私有状态。
- 回填历史 run 的结构化过程。
- 日志脱敏。tool 输出可能包含敏感信息，访问控制沿用 run 现有的读权限，脱敏另行设计。

## 设计：状态进数据库，过程进日志

这里参考 podman 的分层：数据库只存容器的配置和状态，日志交给 log driver（`k8s-file` 往容器目录下追加写一个带 stream 标记的文件，用 `log_size_max` 限制大小），`podman logs` 直接读这个文件。

AC 里有两种 run：**project run** 是在 sandbox 里执行一次 agent 或命令；**scheduler run** 是执行一次编排脚本，脚本里可以通过 `scheduler.agent` 启动 project run，也可以直接调用 `scheduler.shell` / `scheduler.exec`。两种 run 都按同一套分层处理：

**project run**

| 层 | 存放位置 | 读取入口 | 内容 |
|---|---|---|---|
| 状态 | 数据库 `project_run` | `GetRun` | 输入、结果、退出码、error。新增**汇总**：工具调用次数、usage 总量、最后一个错误 |
| 对话 | 数据库 `project_run_event` | `ListRunMessages` | 用户输入和 agent 每轮的最终回复，是从日志里摘出来的索引 |
| 过程 | `<data>/runs/<run_id>/run.log.jsonl` | `FollowRunLogs(run_id)` | **这个 run 的完整记录**：用户输入、agent 的过程和回复、进程输出、轮次边界，只用一套 `seq`。文本视图和结构化视图都从这里派生 |

**scheduler run**

| 层 | 存放位置 | 读取入口 | 内容 |
|---|---|---|---|
| 状态 | 数据库 `scheduler_run` | `GetSchedulerRun` | 不变 |
| 时间线 | 数据库 `scheduler_event` | `ListProjectSchedulerEvents(run_id)` | 不变：脚本日志、各步骤的完成和失败、`linked_cell_id`，以及 agent 步骤对应的 project run |
| 步骤输出 | `<data>/scheduler-runs/<id>/cells/<cell_id>.log.jsonl` | `FollowRunLogs(scheduler_run_id, cell_id)` | **新增**：每个 shell/exec 步骤的完整输出，见"scheduler run 的日志" |

scheduler run 里的 agent 步骤不在这里存储：它本身就是一个 project run，按 project run 的三层读取。

数据库里只放数据量小、需要查询的东西；体积大、只追加不修改的过程放在文件里。回放一个 run 只需要读日志；数据库里的消息是为列表页和跨 run 的对话视图准备的快捷索引，每条带 `log_seq`，指向日志里对应的那一行。

### 日志格式

```jsonl
{"seq":1,"ts":"…","turn":1,"text_offset":0,"type":"user","input":[{"text":"…"}]}
{"seq":2,"ts":"…","turn":1,"text_offset":120,"type":"agent","event":{"kind":"tool_call","id":"…","name":"Bash","status":"completed",…}}
{"seq":3,"ts":"…","turn":1,"text_offset":930,"type":"output","stream":"stderr","data":"…"}
{"seq":4,"ts":"…","turn":1,"text_offset":1010,"type":"agent","event":{"kind":"text","text":"…"}}
{"seq":5,"ts":"…","turn":1,"text_offset":1800,"type":"turn_end","stop_reason":"stop","final_text":"…"}
```

- `user`：用户输入。包括初始 prompt，以及交互模式下的每一轮 human message。数据库里 `USER_MESSAGE` 的来源就是这一行。
- `turn_end`：一轮结束，带 `stop_reason` 和这一轮的最终回复。数据库里 `AGENT_MESSAGE` 的来源就是这一行。
- `turn`：每一行都标明属于第几轮，应用层不用自己推断轮次边界。command run 固定为一轮。
- `output`：进程输出。command run 只有这一种行；agent run 里它用来承载 runtime 和 provider CLI 打出来的诊断信息。`data` 原样保存每个 chunk，不做换行规整，所以按 stream 过滤后能得到逐字节一致的 stdout。
- `agent`：经过**合并**的 AgentEvent。`text_delta` 合成段落，同一个 `tool_call` 按 id 合成一条带最终状态的记录，reasoning 默认不落盘。
- `text_offset`：这一行渲染成文本后在整个文本流中的起始字节偏移，供 `FollowRunLogs` 定位，见下文。
- 上限（具体数值见"决定"一节）：单条 tool 输出只保留开头和结尾，同时记录原始长度；整个文件超过上限后停止写入 `output` / `agent` 行，并写一行 `truncated` 标记。里程碑事件在数据库里，不受这个上限影响。

### 条目 schema

API 上不能只返回一个 `payload_json`，否则应用层又得自己写解析器。条目定义成有类型的 proto：外层是固定的信封，每种条目对应 `oneof` 的一个分支。核心类型沿用 `agent-event.ts` 里跨 provider 的统一定义；provider 特有的字段放进 `extra_json` 兜底，不对外承诺稳定。

```proto
message RunLogEntry {
  uint64 seq = 1;
  google.protobuf.Timestamp created_at = 2;
  uint32 turn = 3;
  oneof entry {
    RunLogUserInput user_input = 10;    // 用户输入
    RunLogText text = 11;               // 合并后的助手正文
    RunLogToolCall tool_call = 12;      // 按 id 合并，带最终状态、输入，以及截断后的输出
    RunLogUsage usage = 13;
    RunLogError error = 14;
    RunLogOutput output = 15;           // 进程输出：stream + data
    RunLogTurnEnd turn_end = 16;        // stop_reason + final_text
    RunLogTruncated truncated = 17;     // 日志达到上限
  }
  string extra_json = 30;
}
```

- `tool_call` 和 `tool_result` 在 API 上合成一条：调用方关心的是"调用了什么、结果如何"，不需要自己按 id 去配对。
- 新增条目类型时只加 `oneof` 分支。旧客户端遇到不认识的分支直接跳过，不会因此报错。
- 文件里存的 jsonl 和这个 proto 一一对应，但文件格式本身属于内部实现。

### SDK

`follow=false` 时服务端读完就结束流，语义上已经等同于一次性请求。但对写脚本的人来说，流式协议本身就是门槛：ac-task-observer 的 README 里专门记了一条"FollowRunLogs 是 server-streaming，要用 `application/connect+json`"。所以不再新增一次性返回的 RPC，改为：

- `sdk/go` 提供 `ReadRunLog(ctx, runID) ([]*RunLogEntry, error)`，读完整个 run 的条目，以及 `FollowRunLog(ctx, runID, fromSeq, fn)` 用于实时跟随。
- 文档里给出 curl 示例。

### cell：保留名字，不保留目录

cell 作为存储结构退出：不再有 `state/cells/<id>/` 目录，也不再写 `cells.json`。但 **cell 这个名字和 ID 保留下来**，含义重新定义为"run 里的一个执行步骤"：

- project run：整个 run 就是一个步骤，`cell_id` 照常生成，写进 `result_json.cellId`。
- scheduler run：每次 `scheduler.shell` / `scheduler.exec` 调用是一个步骤，见下一节。
- `scheduler_event.linked_cell_id`、`result_json.cellId`、`SandboxHistoryCell` 等字段名都不变，数据库 schema 和 proto 也不用改，新旧数据共用同一套 ID。

只保留 ID、不保留目录，意味着所有拿 `cell_id` 去拼 `state/cells/<id>/` 路径的地方都要改成读日志，见清理清单。

原来 cell 目录里的文件，按下表分到三个地方（数量是 devboard 上的统计）：

| 文件 | 数量 | 是什么 | 去向 |
|---|---|---|---|
| `stdout.txt` / `stderr.txt` / `output.txt` | 各 2080 | 执行输出 | 日志文件里的 `output` 行 |
| `source.txt` / `exitcode.txt` | 各 44 | 输入和退出码 | run 记录或 scheduler 事件 |
| `command-request.json` | 2057 | host 写给 guest 的调用参数，包括完整的命令和环境变量 | sandbox 里的临时调用目录 `state/exec/<cell_id>/`，这一步结束就删除 |
| `command-result.json` | 2033 | guest 写回给 host 的执行结果 | 同上；host 读完后，结果写进 run 记录或 scheduler 事件 |
| `agent-thread.json` | 23 | 没有任何地方读它 | 删除 |
| `journal-cursor` | — | detach 后 reattach 用的游标 | run 目录 |

临时调用目录有现成的先例：agent 的 prompt 文件本来就单独放在 `state/agents/prompts/`（`agent_files.go:49`），不在 cell 目录里。`command-request.json` 里带有环境变量，现在却一直留在磁盘上，挂载了数据目录的人都能读到。改成用完即删之后，这个问题也就没有了。

### scheduler run 的日志

scheduler run 自己的时间线已经存在**数据库**的 `scheduler_event` 表里了：脚本 `scheduler.log` 打的日志、`*.command.completed`、`*.agent.completed`，以及 `linked_cell_id` / `linked_sandbox_id`。所以 scheduler run 不需要一份完整的 `run.log.jsonl`，缺的只是**每个 shell/exec 步骤的输出内容**。

这部分输出必须完整保存，不能只截一段塞进事件 payload 里。原因有两个：
- shell 里跑什么完全不受限制，可以是 `claude -p`、`codex exec` 或者 `acli run`。AC 看不到命令内部在做什么，不能把它当成轻量命令处理。
- 输出本身可能很大。devboard 上超过 64 KiB 的 command 输出有 173 条，最大的达到 4 MiB，而且已经被截断过（`stdoutTruncated: true`）。

做法：
- 每个步骤一个文件，放在 `<data>/scheduler-runs/<scheduler_run_id>/cells/<cell_id>.log.jsonl`。格式和 `run.log.jsonl` 相同，只有 `output` 行（以及达到上限时的 `truncated` 行），上限沿用 `MaxOutputBytes`。
- 只有调用过 shell/exec 的 scheduler run 才创建这个目录，并且跟着 `PruneSchedulerRuns` 一起删除。scheduler run 的数量是 project run 的 100 多倍（devboard 上 46524 比 369），绝大多数是轻量的轮询脚本，不会产生这个目录。
- `scheduler.agent` 不变：它会创建 project run，日志在那个 run 自己的目录里，scheduler 事件通过 `run_id` 引用它。
- 读取：`FollowRunLogs` 新增 `scheduler_run_id + cell_id` 这种寻址方式，和 `run_id` 互斥，返回的文本模式和结构化模式都和 project run 一样。`event_message.go` 改成读这个文件的尾部。

AC 只能保证 shell 的输出完整保存，**没办法把 shell 里调用的 agent CLI 解析成结构化事件**。需要结构化的 agent 过程，应该使用 `scheduler.agent`。这一点要写进 scheduler 的使用文档。

### 位置与生命周期

`<data>/runs/<run_id>/run.log.jsonl`。由 `RemoveRunHistory` 删除，`RemoveSandbox` 不会动它。

### 写入

- 非交互路径的 runtime 在 stdout 上输出和交互路径相同的 NDJSON frame（`started / agent_event / agent_turn_completed / result`），transport 仍然是 `ExecStream`。host 端两条路径共用同一个 projector。理由见"决定"一节。
- host 端用同一个 projector 合并事件后追加写入 `run.log.jsonl`。每写入一行 `user` 或 `turn_end`，就同时往数据库里写一条对应的消息，带上 `log_seq`。run 进入终态时把汇总写进 run 记录。
- 写入日志失败不影响 run 本身的执行，但要在 run 记录里标记 `log_incomplete`。

### 读取：状态、对话、过程

**状态：`GetRun`。** `RunDetail` 新增 `summary` 字段（工具调用次数、usage、最后一个错误）；`RunSummary` 只带列表页需要的那几项。

**对话：`ListRunMessages`。** 由 `ListRunEvents` 改名而来。现在的名字和功能不符：它叫 "Events"，实际返回的却是对话加终态，而 "event" 这个词应该留给过程事件。拆开来看：

| 现在的 kind | 去向 |
|---|---|
| `USER_MESSAGE` / `AGENT_MESSAGE` | 保留，这就是对话 |
| `STATUS` | 和 `GetRun` 重复（payload 就是 `result_json`），不再返回 |
| `AGENT_ACTIVITY` | 从未写入过，删除。过程统一走 `FollowRunLogs` |

`RunEvent` 改名为 `RunMessage`，并新增 `log_seq` 字段，方便从对话视图跳到日志里对应的上下文。`ListSandboxRunEvents` 不再单独保留：它和 `ListRunEvents` 读的是同一张表，只是过滤条件不同，所以合并成一个 RPC，参照 `ListRuns` 的过滤字段写法：

```proto
message ListRunMessagesRequest {
  // 必须且只能设置其中一个。
  string run_id = 1;
  string sandbox_id = 2;
  uint32 limit = 3;
  uint32 offset = 4;
}
```

`history_available_run_ids` 也去掉。每条消息都带着 `run_id`，调用方翻页时收集起来，得到的就是同一个集合。

**过程：`FollowRunLogs`。** 不新增 RPC，扩展现有的这个，相当于 `podman logs` 用参数控制输出格式。

- 默认是文本模式，契约不变：offset 仍然是渲染后文本的**字节偏移**（UI 的 `model/log-buffer.ts` 依赖这一点做算术）。实现改成从 `run.log.jsonl` 渲染，用 `text_offset` 二分定位 `start_offset`。
- 请求里设置 `structured=true` 时，chunk 改为携带日志条目。游标使用新增的 `start_seq`，与 `start_offset` 互斥。
- 以后 UI 改成自己从结构化条目渲染文本，文本模式就可以标记为 deprecated，`text_offset` 和后端的渲染逻辑也一起删掉。这一步不在本次范围内。

**其他：**

| RPC | 变化 |
|---|---|
| `StreamAgentRun` / `AttachAgentRun` | 发出合并后的事件，不再逐个 token 推送 |
| `ListSandboxHistory` | 新 run 的 cell 不再带完整输出；旧 cell 保持原样 |

### 改名的兼容性

`proto-v2-field-layout-freeze.md` 里有术语修正的先例（`loader_*` 改为 `scheduler_*`），但那次只改了字段名，wire 格式不变。**RPC 改名会改变调用路径**，不同版本的客户端和服务端之间会直接调用失败。因此：

1. 新增 `ListRunMessages`。`ListRunEvents` / `ListSandboxRunEvents` 保留一个版本并标记 deprecated，内部转发到新实现，行为保持原样（包括 STATUS）。所有调用方都迁移后再删除。
2. 停止写入 STATUS 之前，先给 UI 找到替代来源。UI 的 `sessions.ts` 会从 STATUS 的 payload 里取 `cellId`，用来关联旧 cell。`RunDetail.result_json` 里有这个字段，但 UI 是批量处理一个 sandbox 下的所有 run，而 `RunSummary` 里没有 `result_json`。要么在 `RunSummary` 里补上这个字段，要么等旧 cell 的兼容逻辑下线之后再停写。

## 清理清单

| 删除或改动 | 说明 |
|---|---|
| `agent-thread.json`、`ProviderLogPaths` 以及对 HOME 的目录遍历 | 死数据，可以直接删 |
| `AgentTraceEvents`（解析文本得到 `agent.tool` 事件） | 改成从时间线派生 |
| agent run 的 cell `stdout/stderr/output.txt`、`runs/<run>/transcript.txt` | 由 `run.log.jsonl` 取代 |
| command 的 `stdout/stderr/output.txt` | 合并成 `output` 行；`event_message.go` 改成读日志尾部 |
| `source.txt` / `exitcode.txt` / `command-result.json` | 这些信息都已经在 run 记录里。先确认崩溃恢复逻辑不依赖它们，再删除 |
| 未关联 run 的 Exec `transcript.txt` | 不再落盘 |
| scheduler 事件靠 `linked_cell_id` 读 cell 的 output.txt（`schedulers/event_message.go`） | 字段不变。改成读 `scheduler-runs/<id>/cells/<cell_id>.log.jsonl` 的尾部 |
| UI 从 STATUS 的 payload 里取 `cellId` 来关联旧 cell | 见"改名的兼容性"第 2 条 |
| `state/cells/<id>/` 目录、`cells.json`，以及非交互 agent run 写入的 sandbox 事件（`ListSandboxHistory` / `WatchSandbox`） | 新 run 不再写入；`cell_id` 仍然生成，见"cell：保留名字，不保留目录"；旧 cell 保留，只读 |
| guest 内的 `state/agents/providers/<p>.json` | 保留，这是 runtime 内部的 resume 状态 |

## 历史数据

不迁移，按格式分别读取，到期后删除旧代码：

- `project_run` 新增 `log_format` 列。空值表示旧格式，新写入的 run 标为 `jsonl`。这是纯新增，不影响已有的行。
- 旧格式的 run 在文本模式下继续用原来的方式读取；在结构化模式下不返回任何条目，并在响应里标明这是旧格式（没有结构化过程）。文件已经不存在的，返回明确的"日志已清理"错误，不要返回空内容。
- 不把旧的文本日志转换成 jsonl。旧数据本身是拍平的，转换时只能靠猜来还原结构；剩下的文件也只有 23 个。
- 旧格式的 run 全部被 prune 之后，删除旧的读取分支。

## 影响面

| 使用方 | 影响 | 需要的改动 |
|---|---|---|
| agent-compose-ui / agent-compose-desktop | `AGENT_ACTIVITY` 的渲染（`run-timeline`、`sandbox-log-runs`、`run-list`）一直是死代码；STATUS 被用于时间线显示和 `cellId` 关联；实时流里 `transcriptLabel` 根据 `name` 的子串猜分类 | 改用 `ListRunMessages`；工具活动改用 `FollowRunLogs(structured=true)`；终态和工具次数改读 `GetRun` / `RunSummary`；确认合并后的实时流显示正常 |
| agent-compose-ui `internal/tokenproxy/policy.go` | 这是只读 token 的 RPC 白名单 | 加上 `ListRunMessages` |
| ac-task-observer | 直接读 cell 文件和 `logs_path`，会失效；`acq` 调用了 `ListRunEvents` | 改成走 API，重写 `SKILL.md` |
| ac-chat-app | 测试里 stub 了 `ListRunEvents` | 跟着改名 |
| `sdk/go/chat`（本仓库） | `history.go`、`conversation.go` 调用了 `ListRunEvents` | 改用 `ListRunMessages`，新增 `ReadRunLog` / `FollowRunLog` |
| ac-ops health-report | 只展示 `logs_path` | 无 |
| agentkit 自定义 guest（`agent-compose-default-guest`） | 有一份 `command.ts` 的分叉 | 只有改动 guest↔host 的 command 结果协议时才需要同步 |

另外要在文档里写明：sandbox 和 run 的磁盘布局是内部实现，不对外承诺。

## 落地顺序

1. 删除死数据：`agent-thread.json`、`ProviderLogPaths`、HOME 目录遍历。
2. runtime 非交互路径输出结构化事件。
3. host 端统一 projector，写 `run.log.jsonl` 和 run 汇总，加上 `log_format` 列；scheduler 的 shell/exec 步骤改为写 `scheduler-runs/<id>/cells/<cell_id>.log.jsonl`，调用参数文件改到临时调用目录。
4. `FollowRunLogs` 改成渲染实现，并支持 `structured=true`；新增 `ListRunMessages`（可按 run 或按 sandbox 过滤），`ListRunEvents` / `ListSandboxRunEvents` 转发到新实现。
5. `sdk/go` 加上读取辅助函数；UI 和 tokenproxy 接入新接口；ac-task-observer 改成走 API。
6. 下线派生文件、`AgentTraceEvents`，以及 `ListRunEvents` / `ListSandboxRunEvents` 和 STATUS 事件。

## 后续方向：scheduler 的 shell/exec 改为 project run

本次方案（"scheduler run 的日志"一节）里，同样是在 sandbox 里执行命令，却有两套处理方式：project command run 有 run 记录、走 `FollowRunLogs(run_id)`；scheduler 的 shell/exec 只有 `cell_id`、日志放在 `scheduler-runs/` 下面，需要第二种寻址方式。更一致的原则是**编排归 scheduler run，执行统一归 project run**：`scheduler.shell` / `scheduler.exec` 应该和 `scheduler.agent` 一样，每次调用都创建一个 project command run。

本次先不这样做，因为它还牵涉到 scheduler 和 sandbox 的归属关系（见 `scheduler-sandbox-ownership.zh-CN.md`）。但本次方案必须保证以后能平滑迁移过去：

- scheduler 步骤的日志格式和 `run.log.jsonl` 完全一致，迁移时只换存放位置。
- `cell_id` 作为对齐键：以后创建的 command run，其 `cellId` 沿用这一步原来的 `cell_id`，`scheduler_event.linked_cell_id` 前后指向同一个东西。
- `FollowRunLogs(scheduler_run_id, cell_id)` 在 proto 注释和文档里都标为过渡用；UI 和 SDK 把它封装在内部，不直接暴露给上层。
- `scheduler-runs/` 的目录布局不对外承诺。

迁移步骤：

1. `scheduler.shell` / `scheduler.exec` 改为创建 project command run（`cleanup_policy=keep`、`source=scheduler`，sandbox 的归属仍然由 scheduler 的绑定关系决定），scheduler 事件新增 `linked_run_id`。
2. 读取时按事件分发：有 `linked_run_id` 的走 `run_id`，只有 `cell_id` 的走旧路径。
3. 旧的步骤日志不迁移，等 `PruneSchedulerRuns` 自然清理完，再删除旧的寻址方式和 `scheduler-runs/` 目录。
4. UI 的 run 列表默认过滤掉 `source=scheduler` 的 run。devboard 上累计大约有 1.8 万次 shell 调用，不过滤的话会淹没其他 run。

## 决定

**统一 frame 格式和 projector，transport 不统一。** 非交互路径不改走 `InteractionRuntime`。

`InteractionRuntime`（`pkg/runs/controller.go:44`）是 host 和 guest 进程之间的双向会话，用于启动后还要继续输入的场景：交互式 `AttachAgentRun`（PROMPT / COMMAND）、`StartAgentRun(interactive=true)`、`AttachExec`，以及它们 detach 之后的 reattach。不是所有 driver 都实现了它：docker 和 boxlite 实现了，microsandbox 只有带 cgo 构建标签时才实现，**K8s 没有实现**。非交互 run（`RunAgent`、`StreamAgentRun`、scheduler 触发的 run、command）在启动时就拿到了全部输入，用不到双向能力，走的是所有 driver 都支持的 `ExecStream`。

两条路径真正需要合并的是事件格式、projector 和落盘，transport 不需要合并：
- 非交互路径的 runtime 已经能拿到结构化事件，只是没有接上 `onEvent`（交互路径的接法见 `interactive.ts:283`）。改成在 stdout 上输出同一种 NDJSON frame，最终结果放在 `result` frame 里，取代现在带前缀的结果 JSON。
- host 端把 `ExecStream` 的 stdout 按行交给同一个 projector；stderr 记成 `output` 行，用于诊断。
- 两条路径的 thread 延续逻辑本来就一致：都通过 `buildPromptRuntimeOptions` 得到同一个 `sessionRoot`（`prompt.ts:37`），使用同一套 `readStoredThread` / `writeStoredThread`；codex 两条路径用的也是同一个 `decideCodexThreadResume`。所以复用 sandbox 时，跨 run 续聊的行为不受影响。

如果只多输出一份事件、保留两套 projector，日志混乱的根源就还在。非交互路径附带的 notebook cell 那一套（cell 文件、`AgentTraceEvents`、`agent-thread.json`）随之下线，替代方案见清理清单。

**reattach 时两条路径都用 journal offset 续读，既不丢也不重复。** detach 模式下，guest 的 journal（`detach/journal.ts`）用单调递增的字节 offset 记录 stdout 和 stderr。超过上限时，它会丢掉最旧的记录，并把被丢掉的范围记成一个 gap。frame 就在 stdout 里，所以会自动进入 journal。projector 需要做三件事：
- 在每一行日志里记下它来自 journal 的哪个 offset（`src_offset`，只用于内部，不对外暴露）；
- reattach 时，从日志最后一行的 `src_offset` 往后续读，并从 `run.log.jsonl` 的尾部恢复合并状态，包括当前轮次和已经写过的 tool_call id；
- 遇到 gap 就写一行 `truncated`，说明丢了多少。

现在两条路径的 reattach 机制不一样：非交互路径用 `ReplayAll=true` 重放整个 journal（`agent_runner.go:328`），因为结果 JSON 要从完整的 stdout 里解析；交互路径用 cursor 续读，但每次都新建 projector，合并状态不会延续。改成上面的方式后，两条路径的 reattach 逻辑就一样了，`ReplayAll` 也可以去掉。

`seq` 和 `text_offset` 由 host 端在追加写入时分配，天然连续。

**TTY 不影响结构化事件。** prompt attach 固定使用 `TTY: false`（`prompt_attach.go:182`），TTY 只出现在 command 模式下，而 command 模式本来就没有 agent 事件。TTY 的输出是混在一起的终端字节，直接记成 `output` 行，`stream` 字段取值为 `tty`。

**上限沿用现有的数值：**
- 单个日志文件：默认 16 MiB，可以通过 `AGENT_COMPOSE_RUN_LOG_MAX_BYTES` 配置。guest journal 的默认上限是 8 MiB（`DEFAULT_JOURNAL_MAX_BYTES`），host 端的上限不能比它小。
- 单条 tool 输出：保留开头 8 KiB 和结尾 8 KiB，中间截掉，并记录原始长度。报错信息通常在结尾，所以不能只保留开头。
- command 输出：沿用现有的 `MaxOutputBytes`（默认 1 MiB）。

## 待验证

- provider CLI 的实际 resume 行为，比如 claude 拿着保存下来的 thread 跨 run 续聊。代码只能证明两条路径传进去的参数一样，没法证明 provider 那边的表现一样，需要 e2e 测试覆盖。
- 每个 driver（包括 K8s）都要跑一遍完整的 scheduler run，以及 daemon 重启后的 reattach。
- 16 KiB 的 tool 输出截断，对 ac-task-observer 这类排障场景够不够用。上线后观察一段时间，必要时调整。
