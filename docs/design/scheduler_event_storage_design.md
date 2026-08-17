# Scheduler Event 存储改造方案

对应 [issue #565](https://github.com/chaitin/agent-compose/issues/565)。该 issue 是未经代码核实的
调查性 issue，指出 `scheduler_event`/`project_run` 等表存了不少跟 artifact 文件重复的日志/输出内容。
本方案用真实数据核实（§2.1），把范围收紧到 `scheduler_event` 一张表；`project_run`、`scheduler_run`
及 issue 提到的其余表不在本方案范围内。

## 1. 目标与范围

本期实施两项：

1. `loader.command.completed`/`scheduler.command.completed` 的 `scheduler_event.message` **一律置空**，不分输出大小，完整内容只存在 sandbox cell artifact 里（§4）；
2. **`scheduler logs`/`scheduler logs --json` 行为不变**，服务端按事件类型分发（§6）：遇到 command.completed 就按 `linked_sandbox_id`/`linked_cell_id` 读 artifact 重建全文再返回。要接入的 RPC 不止一个：`StreamProjectSchedulerEvents`（主路径）和 `ListProjectSchedulerEvents`（daemon 版本落后时 CLI 的兼容降级路径，`cli_scheduler_query.go` 的 `listProjectSchedulerLogEvents`）都要改；`ListSchedulerEvents` 才是真正的 UI 专用 RPC（`agent-compose-ui/loaders.ts:466`，零调用方），不用动。三个名字相似的 RPC 容易凭名字猜错谁是谁，这里是核实过代码路径的结论。不新增独立 API，不改 Proto。

本期明确不做（详见 §10.2）：不删 `resultJson`（会改 CLI 契约，收益仅 11MB vs 主项 274MB）；不改 `scheduler_run`/`scheduler.log()`；不去重 Agent/LLM payload；不迁移 sandbox RPC payload；不新增对外 artifact API；不支持归档读取；不加自动 retention；不改 Proto/表结构。

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
| <256B | 88,564 | 91.8% | 2.4 MiB |
| 256B–1KiB | 2,900 | 3.0% | 2.0 MiB |
| 1KiB–8KiB | 3,791 | 3.9% | 15.0 MiB |
| 8KiB–64KiB | 855 | 0.9% | 18.1 MiB |
| ≥64KiB | 317 | 0.3% | 245.0 MiB |

0.3% 的行占了 87% 的字节（最大单行 4,153,756 字节），是"个别行过大"而非"行数过多"的问题，靠
retention 减少事件数量边际收益有限。这份分布是"证明该不该做"的证据，不是"决定截断阈值"——§4 最终
决定不分大小一律置空。

**按类型拆分**：`loader.command.completed`（11,794 行，244.9 MiB）+ `scheduler.command.completed`
（2,384 行，29.5 MiB）合计 274.4 MiB，占全表 message 字节的 97%。`loader.*`（改名前）和
`scheduler.*`（改名后）在真实数据里**同时存在**且旧名占多数，任何按类型处理的代码都要兼容两种前缀。

`payload_json` 总字节仅 23.5 MiB，没有类似厚尾，本身不是问题。

**`resultJson` 重复验证**：按 `scheduler_id+scheduler_run_id=run_id` 关联对比，`scheduler.run.
completed`（18,018/18,018）和 `loader.run.completed`（7,513/7,513）与 `scheduler_run.result_json`
**100% 逐字节一致**，合计约 11.94 MiB，删除不会丢数据。

**置空收益**：清空前 274,444,931 字节，清空后全部 14,178 行归零（仅剩固定列开销），预计减少
≈274 MiB——比早先"只截断超长行、保留 4KiB 头尾"的方案（约 257 MiB）多释放约 17 MiB，主要来自本来
免费保留的 90.3% 小输出行。此数字未针对"全部置空"重新验证，但量级上应接近 `message` 全部体积。

## 3. 兼容性原则

不改表结构、不改 Proto。`scheduler logs`/`scheduler logs --json` 在 artifact 仍可读时输出字节级
不变（脚本/`jq` 无需感知）。**已知例外**：artifact 读不到时（sandbox 已归档/被 prune 清理，见
§5、§8；或写入/迁移时就没能成功落盘，见 §4、§7 明确不做安全网检查）一律退化为 §6 生成的提示 +
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

读取路径新增一个纯函数：

```go
func ResolveEventMessage(event domain.SchedulerEvent) (text string, artifactAvailable bool)
```

按 `event.Type` 分发：`(loader|scheduler).command.completed` 直接按 `linked_sandbox_id`/
`linked_cell_id` 读 artifact（不看 `message`/`payload_json` 任何字段），读到返回全文，读不到用
`outputBytes` 当场拼一句提示 + `artifactAvailable:false`；其它类型直接返回 DB 的 `message`，不做
文件 I/O。`StreamProjectSchedulerEvents` 和 `ListProjectSchedulerEvents` 的 handler 都要在发送前
调用它替换 `message`；`ListSchedulerEvents`（UI 专用）不调用。三者共用的 `schedulerEventToProto`
（`project_scheduler_event_handler.go:89`）是无 ctx 的纯函数，不能塞 I/O，要在各自 handler 里、
转完 proto 之后再调用 `ResolveEventMessage`。

## 7. 历史数据迁移

新增维护命令，默认 dry-run：

```bash
agent-compose scheduler compact-events [--force]
```

处理全部新旧类型（`loader.command.completed`/`scheduler.command.completed`）——**范围是全部
14,178 行，不只是原来超长的 1,369 行**，因为 §4 决定不分大小。`resultJson`/Agent/LLM 历史 payload
不在本期迁移范围。

**不检查 artifact 是否存在，无条件清空（接受数据丢失风险）**：跟 §4 的写入路径同一个决定——如果某
行 artifact 早已归档/被 prune/从未成功写入，迁移前它是数据唯一剩下的副本，清空后彻底丢失，dry-run
也不单独提示。唯一兜底是执行前的整库备份。

迁移要求：默认 dry-run、分批事务、幂等、不处理 running Run、不修改未知 payload、清空时不需要写任何
标志位到 `payload_json`（§6 按类型分发，不依赖字段）、执行前提示备份、报告实际减少字节数。

清理后 SQLite 不会自动缩小，维护窗口内 `VACUUM INTO` 到新文件、`PRAGMA quick_check` 验证后原子替换，
保留原库备份。

## 8. Retention

事件大小限制只控制单条增长，控制不了历史总量，长期还是要靠现有 `scheduler prune` 定期执行。配置
自动 retention 本期不做（§10.2）。

## 9. 测试要求

- `scheduler.command.completed` 的 `message` 始终为空（不分大小、不管 artifact 是否写入成功）；
- payload 含 `outputBytes` 等元数据，不再有 `messageTruncated`；
- artifact 写入失败时 `message` 仍会被置空（钉住这个预期行为，避免以后被悄悄改回安全网）；
- §6 读取失败时生成的提示格式正确、含字节数、`artifactAvailable:false`；
- 历史迁移：覆盖 `loader.*`，不检查 artifact 存在与否，dry-run 不改库，重复执行结果一致；
- **`scheduler logs`/`scheduler logs --json` 对 command.completed 事件端到端返回完整内容**——覆盖
  原本 4KiB 以内现在也被清空的小输出、覆盖迁移过的历史行，不能只测 DB 层；
- `ListProjectSchedulerEvents` 接入 §6 后行为与 `StreamProjectSchedulerEvents` 一致（都读 artifact）；
  `ListSchedulerEvents` 不接入，原样返回 DB 的空 `message`；
- `scheduler.log` 超限报错、payload 始终合法 JSON、failed/canceled 仍可见错误摘要；
- Scheduler Run prune 后 artifact 状态正确。

## 10. 实施顺序

### 10.1 本期

1. `scheduler.command.completed` 写入时 `message` 无条件置空，`payload_json` 补齐 `outputBytes` 等；
2. 实现 `ResolveEventMessage`（§6）并接入 `StreamProjectSchedulerEvents` 和 `ListProjectSchedulerEvents`；
3. 补充写入路径、§6 artifact 读取路径的单元与集成测试；
4. 提供历史 compact dry-run/force 能力，无条件清空全部 14,178 行，不做 artifact 存在性检查；
5. 历史清理完成后，维护窗口内 `VACUUM INTO` 回收空间。

### 10.2 后续演进，不属于本期

只列范围和延后理由，不展开实现，真正排期时再设计：

1. 删除 `scheduler.run.completed` payload 里重复的 `resultJson`（收益约 11 MiB，但会改 CLI 契约，需单独当破坏性变更处理）；
2. `scheduler.run.failed/canceled/skipped` 的错误信息去重；
3. `scheduler.log` 加单条/单 Run 总量限制；
4. Agent/LLM completed payload 去重；
5. `scheduler.sandbox.rpc.*` 大 payload 迁移到 artifact；
6. 面向前端/下载场景的公开 artifact 读取 API（仅限未归档 sandbox，见 §5）；
7. 配置自动 retention。

## 11. 预期收益

根据 §2.1 实测：command completed message 占全部 `scheduler_event.message` 字节的约 97%（274.4
MiB）。按 §4 决定全部置空，这部分几乎完全消失，是本次改造收益最大、也最简单的一项。代价是
`scheduler logs` 对这个类型的每条事件都要走 §6 读 artifact（含原本免 I/O 的小输出）。结合现有
freelist 回收（`VACUUM INTO`）和 retention，可进一步控制长期增长。`resultJson` 去重（§10.2，约
11 MiB）因涉及 CLI 契约变更，本期不实施。

```text
scheduler_event 保存轻量时间线
scheduler_run 保持现有权威状态和结果
artifact 保存完整、体积不可控的执行输出
retention 控制历史总量
```
