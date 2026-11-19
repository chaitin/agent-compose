# Run 输入文件：大文件先上传、再引用

> 状态：草案，待评审。替代 PR #849 的"文件分块随 `AttachAgentRun` 流上传"契约。
> 基线：main `af15eac8`（已含 #852 有序多模态输入）、PR #849 head `ea14a45f`。

## 1. 背景

### 1.1 需求

ADP 工作台对话需要"发消息时带附件，agent 在这一轮能读到"（AGENTBUILDER-69）。职责边界已确认：浏览器上传、持久化、配额、owner ACL 在 ADP；把文件放进会话 sandbox 的 workspace 在 AC。ADP 的对话每轮是一个新 Run，复用同一个 sandbox，每轮结束 `STOP_ON_COMPLETION`。附件可能是几十 MiB 的数据文件或压缩包。

### 1.2 main 已有的能力（#852）

#852 已合入 main，给 `RunAgentRequest` 增加了 `repeated AgentInputPart input = 18`，`AttachHumanMessage` 增加了 `input = 3`：

- **入口**：`RunAgent` / `StartAgentRun` / `StreamAgentRun` / `AttachAgentRun` 首帧共用 `RunAgentRequest.input`；交互续轮用 `AttachHumanMessage.input`。
- **来源**：`AgentInputFile.source` 二选一：内联 `bytes`（可选 sha256 校验）或已在 workspace 内的 `workspace_path`。**field 5 已 `reserved`，注释写明留给"流式上传 RPC 返回的 upload_id"。**
- **物化**：`pkg/execution/agent_input_files.go` 的 `WriteAgentInputFiles` 把文件写到 `<workspace>/.agent-compose/uploads/<run>/turn-N/<safe-name>`，重名去重，不覆盖 workspace 已有文件；K8s 通过 `GuestFileWriterFunc` 推进 Pod。`AppendFileReferences` 把路径清单追加到提示词，harness 用自己的文件工具读取。
- **跨轮**：文件留在 workspace，不随 Run 结束删除，同一 sandbox 的后续 Run 可继续访问。
- **限制**：`AGENT_INPUT_FILE_LIMIT_BYTES`（默认 10 MiB）、`AGENT_INPUT_TURN_LIMIT_BYTES`（默认 32 MiB）、`AGENT_INPUT_FILE_LIMIT`（默认 20 个），通过 `EngineInputLimits` 对外声明。
- **失败**：超限、空文件、非法路径、摘要不符都在 Run 创建前返回 `InvalidArgument`。

### 1.3 PR #849 与 main 的关系

#849 在 #852 之前开发，两者对"把文件交给某一轮 agent"做了两套实现：

| 方面 | #852（main） | #849 |
|---|---|---|
| 字段 | `RunAgentRequest.input = 18` | `RunAgentRequest.input_files = 18`，编号冲突 |
| 入口 | 全部 Run 入口 + 交互续轮 | 只有 `AttachAgentRun` |
| 落盘位置 | `.agent-compose/uploads/<run>/turn-N/` | workspace 根目录，重名加 ` (N)` |
| 提示词注入 | `AppendFileReferences` | `promptWithInputFiles` |
| Driver | 含 K8s | docker / boxlite / microsandbox |
| 字节传输 | 请求内联 | 流内分块 `file_chunk = 8` / `input_complete = 9` |
| 单文件上限 | 10 MiB | 100 MiB |
| 送达证据 | 物化失败即 Run 失败 | `project_run_input` 表 + RECEIVING/PUBLISHING/READY/FAILED/UNKNOWN |

#849 唯一 main 没有的能力是**大文件**：内联字节受单个 Connect 请求体积约束（base64 后还要膨胀约 1/3），不适合几十到上百 MiB 的附件。

### 1.4 #849 的耦合问题

kingfs 对 #849 的评审（基于 `ea14a45f`）提出的阻塞问题中，下列几项的根源都是"字节传输嵌在 Run 状态机里"：

| 问题 | 与耦合的关系 |
|---|---|
| `MarkRunning` 失败留下 PENDING + READY 的不可恢复 Run，泄漏 sandbox | Run 多出"等待上传/已就绪未运行"分支，重放路径不重新获取执行权 |
| DETACH 模式下客户端断开，准备阶段要等满 5 分钟 | 断开通知走被客户端 context 取消的会话输入通道 |
| 输入冲突/存储错误被分类为 `InvalidArgument` | 输入证据的读写错误混进 `BeginRun` 的请求校验错误 |
| 上传要等 sandbox 启动或 resume 完成 | `prepareRunInput` 在 `ensureProjectRunSandbox` 之后才读分块；ADP 每轮都要 resume |
| 上传失败只能换 `client_request_id` 重来 | 上传失败就是 Run 失败 |

此外 #849 给 `AttachAgentRun` 加的 4 MiB 读上限会与 #852 的 32 MiB 内联字节冲突。

### 1.5 为什么现在决定

#849 未合并，其 proto 改动未发版；#852 已经占用 field 18 并为上传引用预留了 field 5。现在按 #852 的扩展点补上传能力，不需要任何字段迁移；若沿用 #849 的契约，则必须与 #852 长期维护两套文件交付语义（参见 `docs/design/proto-v2-field-layout-freeze.md`）。

## 2. 目标与非目标

**目标**

1. 支持单文件最大 100 MiB 的 Run 输入，字节传输与 Run 解耦。
2. 引用复用 #852 的 `AgentInputFile`：所有 Run 入口和交互续轮自动支持，物化、提示词注入、跨轮可见性与 #852 完全一致。
3. 保留现有保证：agent 启动前文件完整就位；引用无效时 Run 不创建。
4. ADP 使用体验不变：SDK 把"上传 + 发起 Run"包装成一次调用。

**非目标**

- 不替代内联 `bytes`：小文件仍然一步发送。
- 不做用户文件存储、配额或 ACL（仍在 ADP）。
- 不做通用 sandbox workspace 上传接口。
- v1 不做断点续传（契约预留位置，见 §3.1）。
- v1 不支持 K8s 上的 `upload` 来源（见 §5.3）。
- 不保留 #849 的 `project_run_input` 送达证据表和 UNKNOWN 对账（见 §7）。

## 3. 契约

### 3.1 上传 RPC

```proto
service RunService {
  // ...
  // Stages one file for later reference from AgentInputFile.upload. The first
  // frame must be info. A response means the bytes were verified and stored;
  // any error leaves nothing referenceable.
  rpc UploadRunInput(stream UploadRunInputRequest) returns (UploadRunInputResponse);
}

message UploadRunInputRequest {
  oneof frame {
    RunInputUploadInfo info = 1;   // 必须是第一帧，且只出现一次
    bytes chunk = 2;               // 按顺序，单块最大 256 KiB
  }
}

message RunInputUploadInfo {
  string project_id = 1;           // 暂存对象的归属范围，与 RunAgentRequest.project_id 解析规则相同
  uint64 size_bytes = 2;           // 必填，> 0
  string sha256 = 3;               // 必填，64 位小写 hex，与 AgentInputFileBytes.sha256 格式一致
  // 4 预留给断点续传的 offset。
}

message UploadRunInputResponse {
  string upload_id = 1;
  google.protobuf.Timestamp expires_at = 2;
  // True when the same content already existed in this project; the client
  // may stop sending chunks once it sees the response.
  bool deduplicated = 3;
}
```

- **内容寻址，幂等**：同一 project 内 sha256 相同只存一份。收到 `info` 后若对象已存在，daemon 立即返回已有 `upload_id`（`deduplicated=true`），并刷新 `expires_at`。
- **校验**：收齐后核对大小和 sha256；不符返回 `InvalidArgument`，不留下可引用的对象。超过声明大小的字节立即拒绝。
- **不带文件名**：对象只代表内容，文件名在引用时由 `AgentInputFile.filename` 给出。
- **`upload_id` 不透明**：不等于 sha256，调用方不能从 ID 推测内容，也便于以后换存储布局。
- **限制**：单个对象上限 `AGENT_INPUT_UPLOAD_FILE_LIMIT_BYTES`（默认 100 MiB）；容量见 §4.3。每帧在传输层限制在 `2 × max_upload_chunk_bytes`（覆盖 Connect JSON 编码的 base64 膨胀），避免 daemon 在检查前把超大帧读进内存。

### 3.2 引用：`AgentInputFile.upload = 5`

```proto
message AgentInputFile {
  string filename = 1;
  string media_type = 2;
  oneof source {
    AgentInputFileBytes bytes = 3;
    AgentInputFileWorkspacePath workspace_path = 4;
    // A file staged by UploadRunInput in the same project. The daemon copies it
    // into the workspace before the turn starts; the staged object is never
    // exposed to the agent directly.
    AgentInputFileUpload upload = 5;
  }
}

message AgentInputFileUpload {
  string upload_id = 1;
}
```

- 用消息而不是裸 `string`，给以后的字段（例如期望 sha256）留位置。
- 准入解析出的 sha256 写回 `File.SHA256`，与内联、workspace 来源保持同一形状。
- 准入（Run 创建前）：`upload_id` 存在、属于同一 project、未过期；否则 `NotFound` / `InvalidArgument`，effect 为 `NOT_APPLIED`。
- 限制：`upload` 来源的单文件上限用 `AGENT_INPUT_UPLOAD_FILE_LIMIT_BYTES`；单轮总量新增 `AGENT_INPUT_UPLOAD_TURN_LIMIT_BYTES`（默认 200 MiB），与内联字节的 32 MiB 分开计算，避免大附件挤占内联预算。文件个数仍受 `AGENT_INPUT_FILE_LIMIT` 约束。
- 交互续轮 `AttachHumanMessage.input` 同样可用 `upload` 来源，在该轮物化前解析；引用无效时与其他非法续轮输入一样取消交互 Run。
- 已知限制：daemon 重启后由 `reattachInteractiveRun` 恢复的会话没有重建文件物化流程，续轮带文件（包括 `upload` 来源）会被当作非法输入取消。这与内联文件来源的既有行为一致；若需要支持，应在恢复路径补上 `Materialize`。

### 3.3 能力声明

`EngineInputLimits` 追加：

```proto
message EngineInputLimits {
  uint64 max_file_bytes = 1;
  uint64 max_turn_bytes = 2;
  uint32 max_files = 3;
  // Limits for the upload source; zero means UploadRunInput is unavailable.
  uint64 max_upload_file_bytes = 4;
  uint64 max_upload_turn_bytes = 5;
  uint32 max_upload_chunk_bytes = 6;
}
```

#849 新增的 `RunFileInputCapabilities` 不再需要。

### 3.4 从 #849 撤回的契约

#849 未发版，以下改动直接撤回，不需要 `reserved`：

- `RunAgentRequest.input_files = 18`（已被 #852 的 `input` 占用）
- `AttachAgentRunRequest.file_chunk = 8`、`input_complete = 9`
- `AttachAgentRunResponse.input_ready = 7`、`RunInputPreparation` 及其状态枚举
- `RunFileInputCapabilities`
- `AttachAgentRun` 的 4 MiB 读上限

`GetRun` 按 `client_request_id` 身份查询若 ADP 仍需要，作为独立 PR 提交，不与文件输入绑定。

## 4. 暂存区

### 4.1 布局与元数据

```
<DATA_ROOT>/run-inputs/<project-id>/<upload-id>          # 内容，只读（0444）
<DATA_ROOT>/run-inputs/<project-id>/<upload-id>.json     # 元数据：sha256、大小、创建/过期时间
<DATA_ROOT>/run-inputs/<project-id>/.upload-<uuid>       # 上传中的临时文件
```

- **不用 SQLite**：对象寿命只有分钟级，只有 daemon 一个写者。进程内索引（互斥锁保护）是运行期的权威来源，元数据文件只用于启动时重建索引。这样不需要迁移，也不会把短命数据写进共享库。
- 发布顺序：写临时文件 → fsync → 校验大小和 sha256 → 设为只读 → rename 成内容路径 → 写元数据（临时名 + rename）→ 加入索引。崩溃最多留下临时文件或"有内容无元数据"的文件。
- 启动时不可能有进行中的上传，所以加载时直接删除临时文件、无元数据的内容、内容缺失或大小不符的元数据，以及已过期的对象。
- 同一 project 内两个相同内容的上传并发提交时，后提交的一方丢弃自己的副本，返回先提交的对象（`deduplicated=true`）。

### 4.2 生命周期

- **TTL**：默认 15 分钟，可配置（`AGENT_INPUT_UPLOAD_TTL`）。对象只需要存活到物化完成：ADP 是"发送时上传"，上传完成到 Run 准入是秒级，准入到物化是 sandbox resume 加 workspace 准备的时间。物化之后文件已在 workspace 内，#852 保证同一 sandbox 后续轮次可直接访问，后续轮次不必再引用同一个 `upload_id`，因此不需要为跨轮复用保留对象。去重命中和被 Run 准入都会把 `expires_at` 刷新为 `max(expires_at, now + TTL)`。
- **不做 lease**：上传和使用之间的间隔远小于 TTL，准入时续期即可覆盖物化窗口。极端情况下（sandbox 准备超过 TTL）对象在物化前被回收，物化返回明确的"上传对象已过期"错误，Run 失败、不启动 agent；调用方重新上传并用新的 `client_request_id` 重试。这个失败是显式的，不会产生歧义状态，不值得为它引入引用计数。
- **回收**：daemon 启动时清理一次；之后作为一条策略挂在现有的 cleanup runner 上（`CLEANUP_INTERVAL`，默认 1 小时），回收已过期的对象，以及超过 1 小时的 `.upload-*` 临时文件和无元数据的内容文件。对象被回收后留下的空项目目录同样在老化后由 cleanup 删除。容量记账不依赖这个周期：每次准入新上传前先把已过期对象从记账中剔除并删除。
- **删除 project**：暂存对象按 TTL 过期，空的项目目录由 cleanup 在老化后删除；v1 不在删除 project 时同步清理暂存区（后续如需可接入 project 删除流程）。

### 4.3 容量

- 稳态占用约为"上传速率 × 对象存活时间"。TTL 缩短到 15 分钟后，占用主要来自正在上传和等待物化的对象，量级为"并发轮次 × 单轮上限"。
- daemon 级总容量上限 `AGENT_INPUT_UPLOAD_STORE_LIMIT_BYTES`，默认 8 GiB（按 20 个并发轮次 × 200 MiB，再留一倍余量）。
- 磁盘水位：`DATA_ROOT` 所在文件系统剩余空间低于 10% 时拒绝上传，避免固定上限在小盘机器上写满磁盘。
- 单 project 上限默认为总容量的 1/4，防止一个 project 占满全局配额。
- 以上任一超限都返回 `RESOURCE_EXHAUSTED`（reason `STORAGE_UNAVAILABLE`，effect `NOT_APPLIED`），不做淘汰（淘汰可能删掉调用方马上要引用的对象）。statfs 读不到时只按字节预算判断，不阻塞上传。
- 配额按**已收到的字节**记账：`Begin` 只检查声明大小能否放得下，不预占；每收到一个 chunk 才计入 `reserved`/项目份额。因此声明了大小却迟迟不发数据的连接不会占用容量，容量不足可能在上传中途以 `RESOURCE_EXHAUSTED` 结束（文件被删除，nothing referenceable）。
- 后续可作为一种 cache 类型接入 `CacheService`（List / Prune），v1 不做。

## 5. 物化

### 5.1 流程

在 `WriteAgentInputFiles` 的 `switch file.Source` 中增加 `SourceUpload` 分支，其余流程（目录、命名去重、提示词注入、失败语义）与 `SourceInline` 完全相同：

1. 准入时 `resolveAgentInputUploads` 已把 `upload_id` 解析成暂存路径和大小，并续期；`Validate` 拒绝未解析的引用，执行层不再查暂存区。
2. 用与内联字节相同的 `os.Root` 锚定和 `O_EXCL` 在 `.agent-compose/uploads/<run>/turn-N/<safe-name>` 创建目标文件，再 `io.Copy`。Linux 上文件间的 `io.Copy` 走 `copy_file_range`，在支持 reflink 的文件系统上共享 extent，效果等同 clone；不需要再抽 `cloneWorkspaceFile`（它不经过 `os.Root`）。
3. 复制后核对字节数。不再重新计算 sha256：暂存对象在上传时已校验，且在 daemon 私有目录中只读。
4. 失败时删除半成品。暂存对象在物化前被回收时，返回"upload expired before it reached the workspace; upload it again"。

### 5.2 为什么不能硬链接

同一个暂存对象可能被多个 Run、多个 sandbox 引用。硬链接共享 inode，agent 修改 workspace 里的文件会改坏暂存的原件。此外 `DATA_ROOT` 与 sandbox workspace 在部署上可能不在同一文件系统。

### 5.3 K8s

当前 `GuestFileWriterFunc` 接收完整 `[]byte`，大文件需要整块读入内存再推送，不适合 100 MiB 级别。v1 中：默认 driver 为 K8s 时 `EngineInputLimits` 的上传上限报告为 0；Run 实际落在 K8s（有 guest writer）上时，物化返回 `ErrUnsupported`，Run 失败、不启动 agent。后续可给 writer 增加流式接口再放开。

## 6. SDK 与 ADP

**Go chat SDK（#850）**

`SendWithInput` 改为：

1. 小于内联上限的文件直接放 `bytes`；其余文件并行调用 `UploadRunInput`，得到 `upload_id`。
2. 用 `AgentInputFile{upload}` 组装 `input`，发起 attach。
3. 上传失败只重试上传，不影响 Run，不需要换 `client_request_id`。

**ADP**

- `feat/workbench-file-input` 分支目前依赖 #849/#850 的伪版本，需要升级到新契约。
- ADP 每个附件已有 sha256，直接作为上传的校验值。

## 7. 为什么不保留送达证据表

#849 的 `project_run_input` 表和 UNKNOWN 对账用来回答"Run 内上传到哪一步了"。上传移出 Run 后：

- 上传结果由 `UploadRunInput` 的响应同步给出，幂等可重试；
- 物化是一次本地文件操作，失败即 Run 失败，语义与 #852 的内联字节一致；
- 物化成功的证据是 Run 提示词中的文件路径清单。

不存在"客户端以为没送达但其实送达了"的歧义窗口，因此不需要额外的状态机。若 ADP 有独立的审计需求，再单独讨论。

## 8. 迁移

1. 基于 main 新开分支实现本方案；#849 关闭，或在征得同意后 force-push 到原分支。
2. 从 #849 复用：分块接收与大小/sha256 校验、`os.Root` 锚定、临时名 + 原子发布（迁到暂存区层）。
3. 随耦合一起删除：Run 内准备阶段、5 分钟时限、断开通知、重放分支里的 `onStarted`、`project_run_input` 迁移、`promptWithInputFiles`、4 MiB 读上限。
4. #850 按 §6 重写上传部分。
5. 中英文公共手册补充 `UploadRunInput`、`upload` 来源、限制与错误码，运行 `task docs:build`。

## 9. 测试计划

- **契约**：新 RPC 的帧顺序（`info` 必须第一帧、只出现一次）、`AgentInputFile.upload = 5` 字段号。
- **暂存区**：大小/摘要不符不留可引用对象；超出声明大小立即拒绝；去重命中立即返回并刷新 TTL；崩溃残留由 GC 回收；超过容量、项目份额或磁盘水位返回 `RESOURCE_EXHAUSTED`；并发上传相同内容只留一份。
- **准入**：引用不存在、跨 project、已过期时 Run 不创建、sandbox 不启动；`upload` 总量超限被拒绝且不占用内联预算；同一 `client_request_id` 的重试在上传过期后仍返回第一次创建的 Run。
- **物化**：对象在准入后、物化前被回收时，Run 以"上传对象已过期"失败且不启动 agent；复制后大小不符时删除半成品；agent 修改 workspace 文件不影响暂存原件；与内联文件混合时命名去重一致；交互续轮 `AttachHumanMessage.input` 可用 `upload` 来源。
- **K8s**：有 guest writer 时 `upload` 来源返回 `ErrUnsupported`；默认 driver 为 K8s 时 `EngineInputLimits` 上传上限为 0。
- **e2e**：真实 Docker sandbox 上，首轮与复用 sandbox 的后续轮次都能读到 50 MiB 级别的上传文件；改造 `test:e2e:docker-run-input`。

## 10. 开放问题

1. 容量默认值依赖"20 个并发轮次"的假设，需要按 ADP 工作台实际的同时在线对话数校准。
2. ADP 是否依赖 #849 的 `GetRun` 按 `client_request_id` 查询；若依赖，单独拆 PR。
3. 断点续传是否需要在 v1 做（`info` 预留 field 4 + 查询已接收长度的接口）。
4. K8s 何时支持 `upload` 来源（取决于 `GuestFileWriter` 是否提供流式接口）。
