# Proto v2 selector and mutation contract

This document freezes the selector, field-presence, and collection-update
semantics of the v2 API. Clients must preserve protobuf presence and oneof
cases when translating CLI flags or Web forms into requests; they must not
infer a priority between selector fields or omit explicit zero values from
`Set` requests.

## Selector audit

Single-resource requests use one of these forms:

| Selector | Contract |
| --- | --- |
| `ProjectRef` | Exactly one non-empty `project_id`, `name`, or `source_path`. IDs are stable, names are daemon-unique project identities, and source paths are exact mutable lookup keys. |
| `ExecRequest.target` | Exactly one `sandbox_id`, `run_id`, or `selector`. Empty selected scalar cases are invalid. |
| `ExecSandboxSelector.project` | Exactly one non-empty `project_id` or `project_name`; `agent_name` is an optional narrowing filter, not an alternative project selector. |
| Hierarchical project resources | Scheduler requests combine one `ProjectRef` with required `agent_name` and, where applicable, `trigger_id` or `run_id`. These fields identify nested resources and are not mutually exclusive alternatives. |
| Direct resource IDs | Run, sandbox, image, volume, cache, workspace-preset, and session operations use one required resource ID or name. Query/list fields are filters and may be combined. |

Handlers reject a missing or empty selected case with `InvalidArgument`. A
oneof makes simultaneous alternatives unrepresentable in generated clients;
malformed wire input follows normal protobuf last-one-wins decoding. A lookup
with no match is `NotFound`, while a selector that matches multiple resources
is `InvalidArgument`.

## 精确执行历史删除

`ProjectService.RemoveRunHistory` 是精确删除命令，不接受列表筛选或
`ProjectRef` 名称解析。必须传入规范的完整 `project_id`、明确的
`RunHistoryKind` 和完整 `run_id`；Scheduler Run 还必须有完整
`scheduler_id`。空白、短 ID、历史前缀、未知类型或归属不符不会被静默忽略。

- `RUN` 删除独立 Project Run 的结果、事件和标签；Scheduler 创建的子 Run
  必须通过所属 Scheduler Run 一并删除，不能单独隐藏执行证据。
- `SCHEDULER_RUN` 删除指定父 Run、精确关联的子 Run、Scheduler 事件、
  event delivery/link 记录和规范专属目录。保留 Topic Event、Project、
  Agent、Scheduler、配置和 sticky binding。
- 只允许终态执行，且所有子 Run 与 completion 收尾必须结束。相关 Scheduler
  仍有运行或历史删除时拒绝；不会自动 Stop，也不会在持有全局调度锁时清理磁盘。
- 先完成 `RemoveSandbox`；仍有相关沙箱材料、审计归档、外部沙箱引用、绑定
  或其他 Run 的重叠材料路径时拒绝。此接口不删除沙箱或审计归档，也不代替其
  生命周期。保留或共享沙箱的历史不能使用该接口清除。
- 数据库写事务固定检查范围，专属材料先清理、历史行后删除；清理期间其他
  SQLite 写入可能等待。失败或中断保留身份及路径，重试必须传入同一完整身份。
  数据库检查阶段拒绝为 `NOT_APPLIED`，清理开始后的错误为 `UNKNOWN`。
- 返回成功表示范围内历史及专属材料均已不存在，重复请求同样成功。父记录被
  旧 prune 提前删除时，仍可用 Scheduler/Run 完整身份清理规范目录及直接链接。
  Project 或 Scheduler 本身不存在、归属不符时仍报 `NotFound`，不能把任意
  `NotFound` 当作已完成清理。执行历史与启动幂等记录一起移除，因此客户端
  不应在删除后重放旧的启动请求；此接口不保留永久执行去重墓碑。

该 RPC 不增加 CLI 命令，不改变 `scheduler prune` 的条件批量清理语义，
不新增 schema migration。公开调用沿用 daemon 控制面的鉴权，不接受 guest
提供的文件路径作为删除目标。

## Driver model

`DriverSpec` is a single-config model. Exactly one of `boxlite`, `docker`,
`microsandbox`, or `k8s` is selected in `config`, and the required `name`
field must match that case. `name` remains in the wire and JSON representation
for compatibility with existing project output; the server always emits both.
There is no persistence of inactive driver configurations. A missing config,
missing name, or mismatch is invalid. This matches compose normalization,
which has always required exactly one runtime configuration, and prevents
silent ignored configuration.

## Mutation semantics

| Request | Field semantics |
| --- | --- |
| `ApplyProject` | `spec` is required and declaratively replaces the complete project. Repeated agents, variables, volumes, workspaces, MCP servers, OctoBus servers, and every nested repeated/map field are replaced. Empty clears; omitted entries are deleted. `source` is optional normalization metadata, `submitted_spec_hash` optionally verifies the normalized submitted spec (it is not a stored-revision concurrency guard), and `dry_run` explicitly chooses plan versus apply. |
| `PatchProject` | Updates an existing project with a complete desired `spec`; it is not JSON Patch, FieldMask, or a mutation DSL. `project` and `expected_current_spec_hash` are required, and a stale hash fails with `ABORTED`. The project name and persisted source cannot change. A `********` marker preserves an existing secret only at the same stable location; new, moved, and non-secret marker use is rejected. A real value replaces the secret, and omitted collection entries are deleted. `dry_run` performs the same checks without persistence. |
| `SetSchedulerEnabled` | `project` and `agent_name` are required. `enabled` is an explicit value; `false` disables and is never a no-op. |
| `SetSchedulerTriggerEnabled` | `project`, `agent_name`, and `trigger_id` are required. `enabled` is an explicit value; `false` disables and is never a no-op. |
| `UpdateGlobalEnv` | `env` is a complete replacement keyed by name. Empty clears all entries and omitted names are deleted. For an entry marked secret, absent `value` retains the stored secret with that name; present empty clears it. `secret` is an explicit replacement value. Duplicate names normalize with the last occurrence winning. |
| `UpdateCapabilityGatewayConfig` | Field-level patch. Absent `addr`/`token` is no-op; present empty explicitly clears that field. |
| `UpdateWorkspacePreset` | `preset_id` identifies the resource. All other fields are complete replacement values. Empty `comment` clears it; empty name/type is rejected; empty `config_json` is normalized to the provider default rather than treated as no-op. |

Create requests provide complete initial values. Delete, remove, prune, stop,
and other action requests are commands rather than patches: their ordinary
bool/scalar zero values are explicit documented command modes or optional
filters. List and stream repeated fields are filters; empty means no filter,
not mutation.

The protobuf definitions are the shared CLI/Web boundary. Both clients must
construct the same oneof case and preserve optional-field presence described
above. JSON object omission means absent/no-op only for proto `optional`
fields; an explicitly supplied empty string means clear.
