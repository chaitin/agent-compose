# Scheduler Event 存储改造方案

对应 [issue #565](https://github.com/chaitin/agent-compose/issues/565)。该 issue 是未经代码核实的
调查性 issue，指出 `scheduler_event`/`project_run` 等表存了不少跟 artifact 文件重复的日志/输出内容。
本方案用真实数据核实（§2.1），把范围收紧到 `scheduler_event` 一张表；`project_run`、`scheduler_run`
及 issue 提到的其余表不在本方案范围内。

## 1. 目标与范围

本期实施两项，**只处理新写入，不动历史数据**：

1. `loader.command.completed`/`scheduler.command.completed` 的 `scheduler_event.message` **一律置空**，不分输出大小，完整内容只存在 sandbox cell artifact 里（§4）；
2. **`scheduler logs`/`scheduler logs --json` 行为不变**，服务端按事件类型分发（§6）：遇到 command.completed 就按 `linked_sandbox_id`/`linked_cell_id` 读 artifact 重建全文再返回。要接入的 RPC 不止一个：`StreamProjectSchedulerEvents`（主路径）和 `ListProjectSchedulerEvents`（daemon 版本落后时 CLI 的兼容降级路径，`cli_scheduler_query.go` 的 `listProjectSchedulerLogEvents`）都要改；`ListSchedulerEvents` 才是真正的 UI 专用 RPC（`agent-compose-ui/loaders.ts:466`，零调用方），不用动。三个名字相似的 RPC 容易凭名字猜错谁是谁，这里是核实过代码路径的结论。不新增独立 API，不改 Proto。

本期明确不做：**不清理历史已写入的 `message` 数据**（已经在库里的约 262 MiB 不会因为本期改造而缩小，见 §9）；不删 `resultJson`（会改 CLI 契约，收益仅约 11 MiB）；不改 `scheduler_run`/`scheduler.log()`；不去重 Agent/LLM payload；不迁移 sandbox RPC payload；不新增对外 artifact API；不支持归档读取；不加自动 retention；不改 Proto/表结构。

职责边界：

```text
scheduler_run  = 一次 Run 的权威状态、输入、结果和错误
scheduler_event = 轻量、可查询的事件时间线
artifact        = 完整且体积不可控的执行输出
```

## 2. 现状与主要问题

`scheduler_event.message` 是当前库里最大的单一数据来源，主要来自 `loader.command.completed`/
`scheduler.command.completed`——这两类事件把完整 `result.Output`/`Stdout`/`Stderr` 整体写入
`message`，而完整内容已经存在于 sandbox cell artifact（`.../state/cells/<cell-id>/{stdout,stderr,
output}.txt`），DB 里也已经有 `linked_sandbox_id`/`linked_cell_id` 这个逻辑引用。

`scheduler.run.completed.payload_json.resultJson` 与 `scheduler_run.result_json`、
`<run-artifacts>/result.json` 重复。

其余事件类型（`scheduler.run.started`、sandbox 生命周期事件、`scheduler.event.published`、
deprecated warning 等）体积本身很小，审计后确认不是本方案要处理的对象。

### 2.1 devboard 环境实测数据

取自 devboard 真实 `data.db`，采样 2026-08-17，文件 717,230,080 字节（≈684 MiB）：

```text
scheduler_event 表大小：349,954,048 字节（≈334 MiB，含索引）≈ 全库的 49%
scheduler_event 行数：  96,427
```

**`message` 长度分布**：

| 区间 | 行数 | 占比 | 总字节 |
|---|---:|---:|---:|
| <256B | 88,564 | 91.8% | 2.3 MiB |
| 256B–1KiB | 2,900 | 3.0% | 1.9 MiB |
| 1KiB–8KiB | 3,791 | 3.9% | 14.3 MiB |
| 8KiB–64KiB | 855 | 0.9% | 17.3 MiB |
| ≥64KiB | 317 | 0.3% | 233.7 MiB |

（表内数字均为二进制 MiB=1,048,576 字节，与 684/334 MiB 那两个头部数字口径一致；总计约 269.4 MiB。）

0.3% 的行占了 87% 的字节（最大单行 4,153,756 字节），是"个别行过大"而非"行数过多"的问题，靠
retention 减少事件数量边际收益有限。这份分布是"证明该不该做"的证据，不是"决定截断阈值"——§4 最终
决定不分大小一律置空。

**按类型拆分**：`loader.command.completed`（11,794 行，233.6 MiB）+ `scheduler.command.completed`
（2,384 行，28.2 MiB）合计 261.7 MiB（274,444,931 字节），占全表 message 字节（269.4 MiB）的 97%。
`loader.*`（改名前）和 `scheduler.*`（改名后）在真实数据里**同时存在**且旧名占多数，任何按类型处理
的代码都要兼容两种前缀。

`payload_json` 总字节仅 22.5 MiB，没有类似厚尾，本身不是问题。

**`resultJson` 重复验证**：按 `scheduler_id+scheduler_run_id=run_id` 关联对比，`scheduler.run.
completed`（18,018/18,018）和 `loader.run.completed`（7,513/7,513）与 `scheduler_run.result_json`
**100% 逐字节一致**，合计约 11.4 MiB，删除不会丢数据（本期不做，见 §1）。

这 261.7 MiB（274,444,931 字节）是本期改造后**新写入不会再产生**的部分；已经在库里的这份数据，
本期不做迁移，不会因为这次改造而消失（详见 §9）。

## 3. 兼容性原则

不改表结构、不改 Proto。`scheduler logs`/`scheduler logs --json` 在 artifact 仍可读时输出字节级
不变（脚本/`jq` 无需感知）。**已知例外**：artifact 读不到时（sandbox 已归档，见 §5；或写入时就
没能成功落盘，见 §4 明确不做安全网检查）一律退化为 §6 生成的提示 +
`artifactAvailable:false`——前者是刻意不支持的能力边界，后者是权衡复杂度后接受的已知数据丢失风险，
表现一样但性质不同。

## 4. `scheduler.command.completed` 存储策略（本期）

`message` 始终为空字符串，不分大小，不做"小的留原样"的分支。

**为什么连小输出也不留**：`scheduler logs` 已经会直接从 artifact 读（§6），DB 里存不存内容对它没有影响；
留一份内容采样唯一的用处是软化"artifact 也读不到"这个已经判定"不支持"（§5）的场景，逻辑上矛盾，
索性不留，规则和实现都更简单。

**代价**：
- `scheduler logs` 对这个类型的每条事件都要走文件 I/O，包括原本 90.3% 免费的小输出，是真实的性能取舍；
- 小输出原来跟 sandbox 生命周期无关，现在也依赖 artifact 是否还在。

`payload_json` 保留精简元数据（`mode`/`exitCode`/`success`/`stdoutTruncated`/`stderrTruncated`/
`outputBytes`/`sandboxId`/`cellId` 等），**不再需要 `messageTruncated`**：`message` 永远是空的，
`ResolveEventMessage`（§6）按事件类型分发即可，不用看任何标志位——少一个字段，也就少一类"忘了同步
导致 §6 读不到内容"的 bug。

`scheduler.command.failed` 本期保持现状。

**不设写入顺序安全网（明确决定，接受数据丢失风险）**：`message` 无条件置空，不等待、不检查 artifact
是否落盘成功。artifact 写入一旦失败，输出会永久静默丢失——DB 和文件都没有，唯一能看到的是 §6 用
`outputBytes` 当场生成的"内容不可用"提示。这是权衡复杂度后的明确选择：加一层顺序保证不难，但团队
认为为小概率写入失败场景专门维护安全网不值得。

## 5. Sandbox 归档的处理

`SANDBOX_RETENTION_TTL` 触发后，sandbox `state/cells` 归档为 `.tar.zst`，仍含
`stdout.txt`/`stderr.txt`/`output.txt`，但当前没有 archive list/download/restore/单文件读取 API，
**本方案也不新增**。sandbox 一旦归档，任何接口都拿不回完整内容，只能看 §6 临时生成的"内容不可用"
提示——这是本方案明确划定的能力边界，不是"下阶段补上"的待办。`.tar.zst` 本身也不适合高频低延迟的
按需读取；未来如需支持，是完全独立的子系统，需单独立项。

## 6. 代码组织

事件内容策略属于 `pkg/schedulers`，storage 层只负责持久化。写入路径不需要新写"计算/截断"函数——
`message` 就是常量空字符串，把原来赋值 `result.Output` 的地方改成空字符串即可。

读取路径新增一个函数（**不是纯函数**——它会做文件 I/O，需要 `ctx` 支持取消/超时/链路追踪，也需要
能注入依赖才好测试）：

```go
func ResolveEventMessage(ctx context.Context, event domain.SchedulerEvent) (text string, artifactAvailable bool)
```

按 `event.Type` 分发：`(loader|scheduler).command.completed` 直接按 `linked_sandbox_id`/
`linked_cell_id` 读 artifact（不看 `message`/`payload_json` 任何字段），读到返回全文，读不到用
`outputBytes` 当场拼一句提示 + `artifactAvailable:false`；其它类型直接返回 DB 的 `message`，不做
文件 I/O。`StreamProjectSchedulerEvents` 和 `ListProjectSchedulerEvents` 的 handler 都要在发送前
调用它替换 `message`；`ListSchedulerEvents`（UI 专用）不调用。三者共用的 `schedulerEventToProto`
（`project_scheduler_event_handler.go:89`）**才是**无 ctx 的纯 DB→proto 转换函数，不能塞 I/O，
`ResolveEventMessage` 不能塞进它内部，要在各自 handler 里、转完 proto 之后再单独调用。

## 7. 测试要求

- `scheduler.command.completed` 的 `message` 始终为空（不分大小、不管 artifact 是否写入成功）；
- payload 含 `outputBytes` 等元数据，不再有 `messageTruncated`；
- artifact 写入失败时 `message` 仍会被置空（钉住这个预期行为，避免以后被悄悄改回安全网）；
- §6 读取失败时生成的提示格式正确、含字节数、`artifactAvailable:false`；
- **`scheduler logs`/`scheduler logs --json` 对 command.completed 事件端到端返回完整内容**——覆盖
  原本 4KiB 以内现在也被清空的小输出，不能只测 DB 层；
- `ListProjectSchedulerEvents` 接入 §6 后行为与 `StreamProjectSchedulerEvents` 一致（都读 artifact）；
  `ListSchedulerEvents` 不接入，原样返回 DB 的空 `message`；
- **历史数据（本期改造前写入的行）读取行为不受影响**：这些行的 `message` 本来就是完整内容，
  `payload_json` 没有新字段，`scheduler logs` 直接读 DB 就是完整正文，不会误判成需要走 artifact；
- `scheduler.log` 超限报错、payload 始终合法 JSON、failed/canceled 仍可见错误摘要；
- Scheduler Run prune 后 artifact 状态正确。

## 8. 实施顺序

1. `scheduler.command.completed` 写入时 `message` 无条件置空，`payload_json` 补齐 `outputBytes` 等；
2. 实现 `ResolveEventMessage`（§6）并接入 `StreamProjectSchedulerEvents` 和 `ListProjectSchedulerEvents`；
3. 补充写入路径、§6 artifact 读取路径的单元与集成测试，包含"历史行（无 `outputBytes`/新 payload
   形状）读取行为不受影响"这条路径。

## 9. 预期收益

根据 §2.1 实测：command completed message 占全部 `scheduler_event.message` 字节的约 97%（261.7
MiB，274,444,931 字节）。**本期收益是"止血"，不是"消肿"**：按 §4 决定，新写入的这类事件不会再往
`message` 里塞完整内容，数据库增长曲线会被压平；但已经在库里的这 261.7 MiB 不会因为本期改造而
缩小——684 MiB 的 devboard 库文件本期改造后大概率还是 684 MiB 左右，只是不会再涨那么快。

现有存量数据要缩小，只能靠现有的 `scheduler prune` 自然淘汰老 run（连带删除其 `scheduler_event`，
但依赖 retention 策略确实在跑），或者未来单独评估做历史迁移，两者都不在本期范围内。代价是
`scheduler logs` 对新事件的每条都要走 §6 读 artifact（含原本免 I/O 的小输出）。

```text
scheduler_event 保存轻量时间线
scheduler_run 保持现有权威状态和结果
artifact 保存完整、体积不可控的执行输出
retention 控制历史总量
```
