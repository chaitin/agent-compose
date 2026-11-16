# Sandbox 启动接口 vNext：多模态输入 proto 定义

- 状态：接口提案（未实现）
- 日期：2026-11-16
- 承接：[`multimodal-input-interface.zh-CN.md`](multimodal-input-interface.zh-CN.md)（业界调研与 provider 能力矩阵）；本文只回答两件事：**加哪些字段**，以及**沙箱内哪些是 harness 原生、哪些要我们的启动脚本做**。
- 关联契约：[`proto_v2_api_contract.md`](proto_v2_api_contract.md)、[`engine_capability_matrix.md`](engine_capability_matrix.md)、[`guest-image-abi`](../pages/guest-image-abi.md)

## 0. 一页结论

1. **不新增 RPC**。启动 sandbox 的四个入口本来就共享同一个请求类型：
   - `RunAgent(RunAgentRequest)`、`StreamAgentRun(RunAgentRequest)`、`StartAgentRun(StartAgentRunRequest{run})`、`AttachAgentRun(AttachAgentRunStart{request})`。
   - 因此多模态输入只加在 `RunAgentRequest` 上，另外三个自动继承；续轮（后续 PR）加在 `AttachHumanMessage` 上。`StartAgentRunRequest`、`AttachAgentRunStart` 本身**不加字段**。
2. **本 PR 只加 5 个字段、5 个新消息、1 个新枚举**：`RunAgentRequest.input = 18`、`EngineProviderCapabilities.{media_types,max_attachment_bytes,media_ordering} = 4/5/6`、`GetEngineCapabilitiesResponse.input_limits = 7`；新消息为 `AgentInputPart`、`AgentInputMedia`、`AgentInputMediaBytes`、`AgentInputMediaWorkspacePath`、`EngineInputLimits`；新枚举为 `EngineProviderMediaOrdering`。续轮 `AttachHumanMessage.input` 与响应侧 `RunDetail.input_attachments` 见 §3.3，留给后续 PR。
3. **字段编号全部顺手递增、无空洞**，不 renumber、不改既有字段语义，`TestV2FieldNumbersHaveNoUnexplainedGaps` / `TestV2FreezeFieldLayout` 不需要改判据（只需把新消息补进 freeze 清单）。
4. **沙箱内的分工是四层，不是两层**：
   - **harness 原生**：只负责"消费它本来就支持的东西"（codex 读图片路径、claude 收 content block、opencode `-f`、pi `@file`、dsh attachment ref），以及缩放/格式转换/模型能力门禁。
   - **启动脚本公共层**（`agent-compose-runtime` 的 prompt/stream）：解析 manifest、guest 侧复核、构造 `TurnContent`、分发 runner。与 provider 无关。
   - **runner adapter**（各 runner + DSH profile）：part → 具体 CLI/SDK 调用形状，这是唯一与 provider 强相关的部分。
   - **daemon**：负责准入、物化、推送到 guest、写 manifest、能力协商。它不认识任何 provider 的调用形状。
5. **有一个无法回避的差异**：claude 与 dsh 都必须由启动脚本"读懂文件再构造内容"（base64 / attachment ref），而 codex/opencode/pi 原生吃路径。这不影响接口形状，但决定 runner 的实现复杂度：claude 必须从字符串 prompt 切到异步迭代。

---

## 1. 基线：现有"启动 sandbox"的 RPC 面

| RPC | 请求 | 响应 | 与 sandbox 启动的关系 |
| --- | --- | --- | --- |
| `RunAgent` | `RunAgentRequest` | `RunAgentResponse` | 创建持久 Run，内部创建/复用 sandbox，等待终态 |
| `StartAgentRun` | `StartAgentRunRequest{run=1, interactive=2}` | `StartAgentRunResponse` | 同上，但提交后立即返回 |
| `StreamAgentRun` | `RunAgentRequest` | `stream StreamAgentRunResponse` | 同上，流式投影事件 |
| `AttachAgentRun` | `stream AttachAgentRunRequest`（首帧 `AttachAgentRunStart{request}`） | `stream AttachAgentRunResponse` | 交互式启动/附加；后续 `human_message` 帧是续轮 |
| `Exec` / `StreamExec` / `AttachExec` | `ExecRequest` | `ExecResponse` | **不启动 agent、不携带 prompt**，不在本次范围 |

当前 `RunAgentRequest` 的 17 个字段都描述"怎么跑"（`prompt`、`env`、`cleanup_policy`、`output_schema_json`、`client_request_id`、`command`、`jupyter`、`driver`、`sandbox_id`、`volumes`、`payload_json`、`labels`…），没有任何"内容结构"概念：输入只有 `prompt` 这个字符串。`RunDetail.prompt` 是唯一的输入回显，同样只有文本。

**结论**：接口面不需要扩张，需要扩张的是"输入内容"这个值对象。

---

## 2. 候选方案与取舍

| 方案 | 表达力 | provider 覆盖 | 传输成本 | 兼容性 | 结论 |
| --- | --- | --- | --- | --- | --- |
| A. `prompt` + `repeated Attachment attachments` | 只能"一段文本 + N 个附件" | 覆盖 5/5（CLI 本来就是这么工作的） | 低 | 最好 | 不选：与业界 part 模型脱节，未来加交错/文档/音频要再改一次 |
| B. `repeated AgentInputPart input`（有序 part） | 文本与媒体可交错；可扩展 | 覆盖 5/5（降级语义显式） | 低（路径零拷贝） | 好（新增字段，旧客户端不受影响） | **选它** |
| C. 直接暴露 provider 原生 content block | 最大 | 只覆盖单 provider | 中 | 差（Anthropic/OpenAI/Gemini 形状不同） | 不选：控制面会被 provider 方言绑死 |

B 与 OpenAI Responses 的 `input[]`、Anthropic 的 `content[]`、Gemini 的 `parts[]`、A2A 的 `parts[]` 同构；同时用 `workspace_path` 这一 source 表达"文件已在执行环境内"，这是 agent sandbox 相对模型 API 的唯一结构性差异。

---

## 3. 完整 proto 定义

以下为可直接合并进 `proto/agentcompose/v2/agentcompose.proto` 的片段（注释风格与现有文件一致）。

### 3.1 新增消息与枚举

```proto
// AgentInputPart is one ordered piece of one human turn.
//
// The array is authoritative: text and media may interleave, and the order is
// the order the model should see. Providers that cannot interleave (codex,
// opencode, pi) receive every media part attached to the same turn and their
// text parts joined in order; providers that can (claude, dsh) preserve the
// exact sequence. The engine reports which behavior applies through
// EngineProviderCapabilities.media_ordering.
message AgentInputPart {
  oneof part {
    // Plain text. An empty string is invalid; omit the part instead.
    string text = 1;

    // Non-text media. Exactly one source must be set.
    AgentInputMedia media = 2;
  }
}

message AgentInputMedia {
  // Required. Canonical lowercase IANA media type without parameters, for
  // example "image/png" or "application/pdf". The server rejects a type the
  // selected provider does not accept and verifies the declared type against
  // the content's magic bytes.
  string media_type = 1;

  // Optional display name. Never interpreted as a path.
  string filename = 2;

  oneof source {
    // Bytes supplied by the caller. The daemon materializes them inside the
    // sandbox before the turn starts.
    AgentInputMediaBytes bytes = 3;

    // A file that already exists inside the sandbox workspace. This is the
    // zero-copy carrier and the local equivalent of a provider file id.
    AgentInputMediaWorkspacePath workspace_path = 4;
  }
}

message AgentInputMediaBytes {
  // Raw bytes; JSON transports encode them as base64.
  bytes data = 1;

  // Optional content digest. When set, the server verifies it and rejects a
  // mismatch with INVALID_ARGUMENT.
  string sha256 = 2;
}

message AgentInputMediaWorkspacePath {
  // Workspace-relative, slash-separated path with the same grammar as
  // SandboxWorkspaceEntry.path. Absolute paths, ".." escapes, NUL bytes, and
  // components that are symlinks are rejected. Only the workspace root is
  // addressable: home, state and mounted volumes are deliberately out of
  // reach, because they carry credentials and daemon-owned control files.
  string path = 1;

  // Optional content digest. The daemon verifies it when it shares the sandbox
  // filesystem (docker, boxlite, microsandbox); otherwise the guest runtime
  // verifies it before the turn starts (k8s has no shared mount).
  string sha256 = 2;
}


// EngineProviderMediaOrdering is how a provider consumes mixed text/media
// input. UNSPECIFIED is treated as AGGREGATE so a provider added later cannot
// make an older client assume interleaving.
enum EngineProviderMediaOrdering {
  ENGINE_PROVIDER_MEDIA_ORDERING_UNSPECIFIED = 0;
  // The exact part sequence reaches the model.
  ENGINE_PROVIDER_MEDIA_ORDERING_INTERLEAVED = 1;
  // Text parts are joined in order and all media is attached to the turn.
  ENGINE_PROVIDER_MEDIA_ORDERING_AGGREGATE = 2;
}

// EngineInputLimits are daemon-wide admission limits. They are reported so a
// client can validate before sending a request that would be rejected.
message EngineInputLimits {
  // Maximum bytes for one AgentInputMedia part.
  uint64 max_part_bytes = 1;
  // Maximum total media bytes in one turn.
  uint64 max_turn_bytes = 2;
  // Maximum number of AgentInputPart entries in one turn.
  uint32 max_parts = 3;
  // Canonical media types accepted by at least one provider.
  repeated string allowed_media_types = 4;
}
```

### 3.2 既有消息的修改（本 PR）

```proto
message RunAgentRequest {
  // ... existing fields 1..17 unchanged ...

  // Ordered multimodal input for this run's opening human turn.
  //
  // A non-empty prompt is prepended as the first text part, so an existing
  // caller keeps sending prompt and a new caller can add media without
  // repeating or moving its text. When input is empty the prompt alone is the
  // turn, which is exactly the pre-vNext behavior. Both together is the
  // supported upgrade path, not an error.
  //
  // At least one non-empty text part (from prompt or input) is required; a
  // media-only turn would reach providers that disagree about empty prompts.
  repeated AgentInputPart input = 18;
}

message EngineProviderCapabilities {
  // ... existing fields 1..3 unchanged ...
  repeated string media_types = 4;
  uint64 max_attachment_bytes = 5;
  EngineProviderMediaOrdering media_ordering = 6;
}

message GetEngineCapabilitiesResponse {
  // ... existing fields 1..6 unchanged ...
  EngineInputLimits input_limits = 7;
}
```

### 3.3 本 PR 未含（后续 PR）

下面两项在调研文档里已经定稿，但本 PR 只做"启动 sandbox"的写路径，不扩大范围：

```proto
message AttachHumanMessage {
  string text = 1;
  map<string, string> metadata = 2;
  // 续轮多模态输入。text 会前置为首个 text part。
  repeated AgentInputPart input = 3;
}

message RunDetail {
  // ...
  repeated RunInputAttachment input_attachments = 13;
}
```

未实现原因：续轮需要在双向 attach 流里物化内联字节（attach 走 native interaction，不经过 `GuestFileWriter`）；响应侧描述需要持久化字段与 DB 迁移。两者都不会改变本 PR 的请求契约，因此可以独立叠加。

在它们落地之前，`AttachAgentRun` / `StartAgentRun(interactive=true)` 的 start 帧只要带非空 `input` 就返回 `Unimplemented`（`UNSUPPORTED`）——显式拒绝，而不是只跑文本投影、把媒体静默丢掉。

### 3.4 编号与契约测试

| 消息 | 现有最大编号 | 新增（本 PR） | 连续性 |
| --- | --- | --- | --- |
| `RunAgentRequest` | 17 | `input = 18` | ✅ |
| `EngineProviderCapabilities` | 3 | `media_types = 4`、`max_attachment_bytes = 5`、`media_ordering = 6` | ✅ |
| `GetEngineCapabilitiesResponse` | 6 | `input_limits = 7` | ✅ |

新枚举从 0 连续编号；新消息都从 1 开始且无空洞。`TestV2FieldNumbersHaveNoUnexplainedGaps` 不会触发，`TestV2FreezeFieldLayout` 只需把新消息补进清单，不需要改动既有条目。§3.3 的两项留给后续 PR，编号建议在其中一并冻结（`AttachHumanMessage.input = 3`、`RunDetail.input_attachments = 13`）。

---

## 4. 字段级论证

| 字段 | 为什么需要 | 不加会怎样 |
| --- | --- | --- |
| `RunAgentRequest.input` | 唯一能表达"文本 + 媒体 + 顺序"的入口；被 4 个启动 RPC 共享 | 只能加并列的 attachments，丢掉顺序与未来扩展性 |
| `AgentInputPart.text` | 让文本也是 part，顺序才有意义；与业界 `content[]` 同构 | 文本与媒体分成两个数组，交错语义无处安放 |
| `AgentInputPart.media` | 把非文本内容收进同一个判别式 oneof，新增媒体种类不需要新 part 类型 | 每加一种媒体就加一个 part case，part 类型爆炸 |
| `media_type` | 业界的共性；provider 白名单与魔数校验都需要它；opencode/pi/dsh 都靠它分流 | 只能靠扩展名猜，SVG/多态文件无法拦截 |
| `filename` | opencode 的 `FilePartInput.filename`、DSH 的展示名、UI 回显 | 展示退化为哈希路径 |
| `bytes` source | 远程调用方唯一可行的入口（今天没有 upload RPC） | 调用方必须先 Exec 写文件，二进制不可靠且需要 sandbox 已运行 |
| `workspace_path` source | 零拷贝；agent 自己产出的文件（截图、图表）天然在 workspace；resume 后仍有效 | 所有媒体都要 base64 穿过控制面，大图成本翻倍 |
| `sha256`（可选） | 幂等重试与 TOCTOU 校验；K8s 下由 guest 侧复核 | 无法判断两次请求是不是同一张图；路径被并发改写无法察觉 |
| `EngineProviderCapabilities.media_types` | 调用方要在发请求前知道能不能发 PDF/图 | 只能发出去再收 `INVALID_ARGUMENT`，CLI/UI 无法预判 |
| `max_attachment_bytes` | opencode 10 MiB 与 Anthropic 5MB 的差异需要暴露 | 客户端只能按全局上限猜，容易踩 provider 上限 |
| `media_ordering` | 保序与否直接改变模型看到的内容，必须可查询 | 调用方会假设所有 provider 都交错，产生难以复现的预期差 |
| `GetEngineCapabilitiesResponse.input_limits` | 一次性拿到全局上限与允许类型；也是"本 daemon 是否支持多模态"的探测点 | 客户端只能硬编码常量；旧 daemon 兼容探测缺失 |

### 明确不加的字段

| 不加入 | 理由 |
| --- | --- |
| `url` source | codex/opencode/pi 的 CLI 不吃 URL；引入 SSRF/egress 策略问题；业界也不统一。留作 P4 的增量 oneof case |
| `audio` / `video` part 类型 | 没有任何 provider 把音频/视频当模型输入消费（DSH 语音是 STT 预处理）。schema 用 `media_type` 留了扩展位，语义不提前编造 |
| `detail`（OpenAI/Codex 的 `auto\|low\|high\|original`） | 当前没有任何一条我们会走的路径真正消费它；加一个被静默忽略的字段，正是 `protocolbridge` 把 `ProviderOptions` 标记为 deprecated 要避免的模式 |
| `file_id` / `attachment_id` | 沙箱内路径已经是句柄，再引入一套 id 命名空间会带来两套生命周期与两套鉴权 |
| 并列的 `attachments` 字段 | 与 `input` 重复，且制造"谁优先"的歧义 |
| 新的 `UploadAgentInput` RPC | v1 用 `bytes` + `workspace_path` 已覆盖；独立上传 RPC 涉及生命周期、GC、鉴权，放 P3 |
| `SchedulerSpec.prompt` 的多模态化 | 定时/事件触发的 prompt 是文本模板，没有媒体来源；明确保持文本 |

---

## 5. 沙箱内的分工：原生 vs 启动脚本

### 5.1 四层职责

| 层 | 代码位置 | 负责 | 不负责 |
| --- | --- | --- | --- |
| 控制面（daemon） | `pkg/runs`、`pkg/agentcompose/api`、`pkg/execution` | 形状/MIME/上限/provider 白名单校验；`bytes` 落盘并推送到 guest；写 content manifest；能力协商；持久化描述 | 任何 provider 的调用形状 |
| 启动脚本（guest runtime） | `runtime/javascript/src/prompt.ts`、`stream.ts` | 解析 manifest；guest 侧复核（K8s 无共享 FS 时）；构造 `TurnContent`；分发 runner；错误映射 | 网络上传、能力广告 |
| runner adapter | `runtime/javascript/src/runners/*.ts`、`assets/.dsh/profiles/agent-compose/runner.js` | part → 具体 CLI/SDK 调用形状；保序/聚合；provider 错误透传 | 校验、落盘 |
| harness 原生 | 各 CLI/SDK 二进制 | 读文件、格式校验、缩放、base64、API 编码、模型能力门禁 | 我们的 part 语义 |

关键判断：**只有 runner adapter 这一层是我们的 provider 专属代码**；daemon 与启动脚本公共层（manifest 解析、路径校验、`TurnContent`）各写一次，五个 provider 共享。

### 5.2 逐项：原生 vs 需要脚本

| 能力项 | codex | claude | opencode | pi | dsh |
| --- | --- | --- | --- | --- | --- |
| 直接接受本地路径 | ✅ 原生 `-i` | ❌ 必须 base64 | ✅ 原生 `-f` | ✅ 原生 `@path` | ❌ 必须 ref |
| 直接接受 base64 | ❌（仅 app-server 的 `image` 变体） | ✅ 原生 content block | ❌（内部自己转） | ✅ RPC `images[]` | ✅ `saveImages` 入口 |
| 原生 PDF | ❌ | ⚠️ API 层支持，CLI stdin 未文档化 | ✅ 白名单含 `application/pdf` | ❌ 非图片按 UTF-8 文本读，PDF 乱码 | ❌ file block 投影为路径文本 |
| 原生缩放/重编码 | ✅ 2048px | ✅ recompress | ✅ | ✅ 2000px | ✅ 2048px |
| 原生保序 | ❌ | ✅ | ❌ | ❌ | ✅ |
| 需要脚本转 base64 | ❌ | **✅** | ❌ | ❌ | ❌ |
| 需要脚本构造 content block | ❌ | **✅** | ❌ | ❌ | **✅** |
| 需要脚本管理 attachment ref | ❌ | ❌ | ❌ | ❌ | **✅** |
| 需要脚本把句柄传给 harness | ✅ 路径参数 | ✅ 读文件 | ✅ `-f` | ✅ `@path` | ✅ `DSH_ATTACHMENTS_FILE` |

### 5.3 启动脚本必须做的事（按文件）

**共享部分（一次实现）**

1. `runtime/javascript/src/prompt.ts`：新增 `--content-file <path>`；解析 JSON manifest；构造 `TurnContent{parts}`。`--message-file` 仍然接收，但 `--content-file` 存在时以它为准（前者只是文本投影与旧行为回退），无该参数时退化为 `[{type:'text',text:promptText}]`。
2. `runtime/javascript/src/types.ts`：新增 `TurnContent`、`InputPart`、`MediaPart` 类型。
3. `runtime/javascript/src/stream.ts`：本 PR 不改；续轮 `content` 帧随 §3.3 的 attach 支持一起做。
4. guest manifest 是**路径清单**，不是 protojson——避免把 base64 再写一遍：

```json
{
  "version": 1,
  "parts": [
    { "text": "比较这两张图" },
    { "media": { "mediaType": "image/png", "filename": "before.png",
                 "path": "/…/state/agents/attachments/<run>/turn-0/part-1",
                 "sizeBytes": 184320, "sha256": "…" } },
    { "media": { "mediaType": "application/pdf", "path": "/…/workspace/report.pdf",
                 "workspacePath": "report.pdf" } }
  ]
}
```

5. guest 侧复核：对 `workspace_path` 附件做大小与魔数校验（daemon 在 K8s 上读不到 workspace）；失败时以非零退出并把 `INVALID_ARGUMENT` 语义带回 run。

**provider 专属（五个 adapter）**

| provider | 启动脚本改动 | 备注 |
| --- | --- | --- |
| codex | `runners/codex.ts`：`runStreamed(Input[])`；text 用 `\n\n` 连接，media → `{type:"local_image", path}` | 路径必须绝对；codex 对不支持格式会静默占位，故 MIME 白名单必须在 daemon 侧前置 |
| claude | `runners/claude.ts`：`query({prompt: AsyncIterable<SDKUserMessage>})`；读文件 → `{type:"image",source:{type:"base64",...}}`；PDF → `document` | 唯一必须改调用形态的 provider；单条字符串模式不支持图片 |
| opencode | `runners/opencode.ts`：`buildArgs` 为每个 media 追加 `-f <path>` | opencode 自己做 data URL 转换与 10 MiB 校验，脚本不重复 |
| pi | `runners/pi.ts`：在 prompt 前插入 `@<path>`；或切 `--mode rpc` 发 `images[]` | 非图片必须拒绝，否则会被当 UTF-8 文本读成乱码 |
| dsh | `runners/dsh.ts` 传 `DSH_ATTACHMENTS_FILE`；`assets/.dsh/profiles/agent-compose/runner.js` 用 `ctx.attachments.saveImages/saveFile` 换 ref，再拼 `type:'image'` 或 `type:'file'` 的 attachment block | DSH 框架原生提供 store/ref/序列化，**接入代码是我们的 profile** |

### 5.4 daemon 必须做的事（与 provider 无关）

1. `pkg/agentcompose/api`：proto → 领域类型映射；`input` 非空时校验 `prompt` 为空。
2. `pkg/runs`：准入校验（part 形状、MIME 语法、魔数、provider 白名单、单 part/单轮上限、part 数）。
3. `pkg/execution/agent_input_files.go`（新文件，单一职责）：
   - `bytes` 写宿主 `state/agents/attachments/<run-id>/turn-<n>/part-<i>`（`0600`），并通过既有 `GuestFileWriterFunc` 推送到 guest（K8s 路径已存在）；
   - 写 `content.json`（上述路径清单）；
   - 返回 guest 侧 `--content-file` 路径。
4. `pkg/agentcompose/adapters/agent_runner.go`：在 `WriteAgentPromptFile` 之后、`BuildAgentExecSpec` 之前调用上面的写入；`agentPromptArgs` 追加 `--content-file`。
5. `pkg/runs/prompt_attach.go`：`RunAttachInput` 增加 content，转发到 guest `human_message` 帧。
6. `pkg/capmatrix` + `guest-images/guest-abi.json` + `scripts/tests/test-provider-lock.sh`：能力声明与门禁（见 §6）。
7. 清理：turn 结束后删除该 turn 的附件目录（持久 sandbox 多轮场景），一次性 sandbox 随 sandbox 回收。

### 5.5 路径附件的校验归属（容易漏）

| 场景 | daemon 能否读 workspace | 谁做魔数/大小校验 |
| --- | --- | --- |
| docker / boxlite / microsandbox | 能（共享宿主目录） | daemon 准入阶段 |
| k8s | 不能（workspace 在 Pod 内） | 启动脚本在 turn 开始时 |
| `mode: mount` 或未知 workspace 类型 | 不保证 | 启动脚本 |

因此 `AgentInputMediaWorkspacePath.sha256` 的校验也是"能校验就早校验，不能就 guest 校验"，这一点写进了字段注释，不能假设 daemon 一定能读。

---

## 6. 能力协商与版本门禁

1. `pkg/capmatrix` 的 `canonicalFeatures` 追加一个粗粒度 key：`media_input`。语义仅为"该 guest runtime 能接收 `--content-file` 并处理附件"，用于**版本门禁**，不表达具体媒体种类。
2. 具体媒体种类由 `EngineProviderCapabilities.media_types` 表达（daemon 静态表）：codex/pi/dsh 为四种位图，claude/opencode 另加 `application/pdf`。
3. `guest-images/guest-abi.json` 的每个 provider `features` 增加 `media_input`；`scripts/tests/test-provider-lock.sh` 的 known-keys 白名单同步增加（否则该测试会报 unknown capability key）。
4. 旧 guest 镜像没有该 key → `DeclaredProvidersFromManifest` 得到 false → daemon 在 run 创建前返回 `Unimplemented` + `UNSUPPORTED`，detail 提示升级 guest 镜像。**不做"新 daemon 发新参数、旧 runtime 忽略"的静默降级。**
5. `media_ordering` 与 `media_types` 来自 daemon 静态表，与 manifest 的 `media_input` 取交集：manifest 为 false 时整组能力视为不可用。
6. `GetEngineCapabilitiesResponse.input_limits` 的**存在性**同时充当"本 daemon 支持多模态"的探测点：旧 daemon 不返回该字段。

---

## 7. 启动时序

```text
client ── RunAgent(request.input=[...]) ──▶ daemon
                                             │ 1. 形状/合并/MIME/上限/provider 白名单/摘要
                                             │ 2. bytes 落盘 <sandbox>/state/agents/attachments/<run>/turn-0/
                                             │ 3. GuestFileWriter 推送（仅 K8s 等无共享 FS 的 driver）
                                             │ 4. 写 content.json（路径清单，不含字节）
                                             │ 5. prompt.txt 仍写（文本投影，供日志与纯文本 run）
                                             ▼
                              agent-compose-runtime prompt --content-file <guest path>
                                             │ 6. 解析 manifest，guest 侧复核 workspace 附件
                                             │ 7. TurnContent → runner
                                             ▼
                              runner adapter（codex/claude/opencode/pi/dsh）
                                             │ 8. 文本合并 或 base64 或 -f 或 @path 或 attachment ref
                                             ▼
                                   harness CLI/SDK ──▶ 模型 API
```

注意第 2 步发生在 sandbox `running` 之后、exec 之前——这正是现有 `ExecuteAgentRun` 写 prompt/schema 的位置，不需要改动 sandbox 生命周期。

---

## 8. 兼容性矩阵

| daemon | guest runtime | 客户端 | 行为 |
| --- | --- | --- | --- |
| 新 | 新 | 新 | 全功能 |
| 新 | 旧 | 新 | manifest 无 `media_input` → run 创建前 `Unimplemented`，不产生半初始化 run |
| 新 | 旧 | 旧 | 纯文本路径完全不变 |
| 旧 | 新 | 新 | ⚠️ 旧 daemon 按 protobuf 未知字段丢弃 `input`，**附件被静默忽略**。新客户端必须先读 `GetEngineCapabilities.input_limits` 再决定是否发送 |
| 旧 | 新 | 旧 | 不变 |

第四行是唯一需要客户端配合的兼容风险，必须写进 SDK 文档：**发送 `input` 前检查能力**，而不是假设"字段能编码就一定能生效"。

回滚：`input` 是新字段，旧 daemon 忽略即回到纯文本；guest 侧 `--content-file` 缺失时 `prompt.ts` 继续走原路径，因此任一层回滚都不会让纯文本 run 失败。

---

## 9. 实施顺序

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0 | proto 新增消息/字段/枚举；`task generate`；freeze 清单补新消息；capmatrix 的 `media_input` 门禁与三个能力字段；guest ABI `media_input`；provider-lock 白名单 | `task lint`、`task test`；旧客户端纯文本回归；`GetEngineCapabilities` 新字段返回正确 |
| P1（本 PR） | daemon 准入 + 物化 + manifest；`--content-file`；五个 adapter（含 dsh）；K8s 推送复用现有 writer；能力字段 | 每个 provider 一条 e2e（发送可判别的纯色图，断言模型回答颜色）；prompt+input 合并/超限/不支持 MIME/非法路径/旧 guest 各一条失败用例 |
| P2（后续） | `stream` 的 `human_message.content`；`AttachHumanMessage.input` | 交互会话两轮：先文本后图片，第二轮生效；resume 后仍可追加 |
| P3 | `RunDetail.input_attachments` 持久化与 UI；可选 `UploadAgentInput` RPC | 重启后历史可读；sandbox 回收后不报 500 |

---

## 10. 待确认

1. **v1 是否允许"只有图片、没有文本"的一轮**？本文按不允许设计（要求至少一个非空 text part），因为 codex/opencode 对空 prompt 的行为不一致；如果产品需要，建议由调用方注入一个明确的短文本，而不是 daemon 静默补默认值。
2. **PDF 在 claude 上的端到端支持**：SDK 类型与 CLI 解析层接受 `document` block，Anthropic API 也原生支持 PDF，但 `claude -p --input-format stream-json` 传 PDF 没有官方文档。P1 先按"支持"实现并加端到端用例，验证失败则把 claude 的 `media_types` 收窄为四种位图。
3. **`max_part_bytes` 的默认值**：本文建议 10 MiB（对齐 opencode 硬上限量级），但 Anthropic 单图 5MB，claude 依赖 CLI recompress。需要在 P1 用真实图片验证后再定稿。
4. **附件是否随 run 历史保留字节**：本文只保留描述；若 UI 需要重新展示，再在 P3 增加 artifacts 目录与下载 RPC。

---

## 11. 接口示例（新增 vs 现有）

图例：`// 🆕 新增` 表示本次提案引入；`// 现有` 表示 v2 已有、语义不变；`// 说明` 不是字段。

> 示例里的 `//` 注释只是为了标注归属，实际请求体是纯 JSON。
> Connect JSON 使用 protojson 默认编码：字段名 lowerCamelCase、枚举用名字、`bytes` 用 base64、`uint64`/`int64` 用字符串、时间戳用 RFC 3339。

### 11.1 示例 1：图文混合（一次 Run）

```http
POST /agentcompose.v2.RunService/RunAgent
Content-Type: application/json
Connect-Protocol-Version: 1
Authorization: Bearer <daemon-token>
```

```jsonc
{
  "projectId": "prj_7f3c9a12",                       // 现有
  "agentName": "reviewer",                            // 现有
  "source": "RUN_SOURCE_API",                         // 现有
  "clientRequestId": "review-2026-11-16-001",         // 现有：幂等键
  "cleanupPolicy": "RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION", // 现有
  "driver": "docker",                                 // 现有（可选）
  "env": [                                            // 现有
    { "name": "LOG_LEVEL", "value": "debug" }
  ],
  "outputSchemaJson": "{\"type\":\"object\",\"properties\":{\"summary\":{\"type\":\"string\"}}}", // 现有

  "input": [                                          // 🆕 新增字段（替代 prompt）
    { "text": "这两张截图有什么区别？只列 UI 变化。" },   // 🆕 文本 part
    {                                                 // 🆕 媒体 part
      "media": {
        "mediaType": "image/png",                     // 🆕 必填，小写无参数
        "filename": "before.png",                     // 🆕 可选，仅展示
        "bytes": {                                    // 🆕 source A：内联字节
          "data": "iVBORw0KGgoAAAANSUhEUg...",         // 🆕 base64（protojson）
          "sha256": "9f2c1d7e...e7"                   // 🆕 可选，服务端校验
        }
      }
    },
    {
      "media": {
        "mediaType": "image/png",
        "filename": "after.png",
        "bytes": { "data": "iVBORw0KGgoAAAANSUhEUg...", "sha256": "1a7b3e09...c4" }
      }
    }
  ]
}
```

说明：这里只发 `input`、没有 `prompt`，是合法的等价写法。更常见的兼容升级写法是**两者同时发**：`prompt` 会被前置为首个 text part，`input` 追加媒体（见示例 4）。

### 11.2 示例 2：纯文本（零改动，完全向后兼容）

```json
{
  "projectId": "prj_7f3c9a12",                        // 现有
  "agentName": "reviewer",                             // 现有
  "prompt": "把这个 PR 的 diff 总结成三条要点",          // 现有（可与 input 同时发）
  "source": "RUN_SOURCE_API",                          // 现有
  "clientRequestId": "review-2026-11-16-002"           // 现有
}
```

旧客户端、旧 CLI、旧 SDK 发的就是这种请求，新 daemon 行为不变。

### 11.3 示例 3：workspace 路径（零拷贝，复用已有 sandbox）

```json
{
  "projectId": "prj_7f3c9a12",                         // 现有
  "agentName": "reviewer",                              // 现有
  "source": "RUN_SOURCE_API",                           // 现有
  "sandboxId": "sbx_4d21ab77",                          // 现有：复用 sandbox（文件必须已在其中）
  "input": [                                            // 🆕
    { "text": "报告第 2 页的结论是什么？" },               // 🆕
    { "media": {
        "mediaType": "application/pdf",                 // 🆕
        "filename": "report.pdf",                       // 🆕
        "workspacePath": {                              // 🆕 source B：不拷贝字节
          "path": "reports/2026-Q3.pdf",                // 🆕 workspace 相对路径
          "sha256": "5c9d0b41...2f"                     // 🆕 可选
        }
    }}
  ]
}
```

说明：`workspacePath.path` 与 `SandboxWorkspaceEntry.path` 同一套语法（相对、无 `..`、无符号链接分量）；`home`/`state`/挂载卷不可寻址。PDF 只有 `claude`、`opencode` 接受，其它 provider 返回 `invalid_argument`，detail 带 `supportedMediaTypes`。

### 11.4 示例 4：prompt + input 合并（线上旧调用方的升级路径）

这是本 PR 最重要的兼容场景：调用方保留原来的 `prompt`，只追加 `input` 媒体。

```jsonc
{
  "projectId": "prj_7f3c9a12",                       // 现有
  "agentName": "reviewer",                            // 现有
  "prompt": "这两张截图有什么区别？",                    // 现有：会被前置为首个 text part
  "source": "RUN_SOURCE_API",                         // 现有
  "clientRequestId": "review-2026-11-16-003",         // 现有
  "input": [                                          // 🆕
    { "media": { "mediaType": "image/png", "filename": "before.png",
                 "bytes": { "data": "iVBORw0KGgo..." } } },
    { "media": { "mediaType": "image/png", "filename": "after.png",
                 "bytes": { "data": "iVBORw0KGgo..." } } }
  ]
}
```

服务端归一化后的 part 序列是 `[text(prompt), image(before), image(after)]`，`RunDetail.prompt` 仍是 `"这两张截图有什么区别？"`，`warnings` 里会说明 prompt 被前置。响应示例见 §11.5。

> 续轮 `AttachHumanMessage.input` 与双向流里的 `content` 帧不在本 PR（§3.3）；接入方式与上面一致，届时 `text` 同样前置为首个 part。

### 11.5 示例 5：响应（RunAgentResponse）

```jsonc
{
  "run": {
    "summary": {
      "runId": "run_01J9Z6K3T4",                      // 现有
      "projectId": "prj_7f3c9a12",                    // 现有
      "projectName": "code-review",                   // 现有
      "projectRevision": "12",                        // 现有（uint64 → 字符串）
      "agentId": "agt_2c8e91",                        // 现有
      "agentName": "reviewer",                        // 现有
      "source": "RUN_SOURCE_API",                     // 现有
      "status": "RUN_STATUS_SUCCEEDED",               // 现有
      "exitCode": 0,                                  // 现有
      "startedAt": "2026-11-16T04:31:02.123Z",        // 现有
      "completedAt": "2026-11-16T04:31:19.887Z",      // 现有
      "durationMs": "17800",                          // 现有（int64 → 字符串）
      "createdAt": "2026-11-16T04:31:01.900Z",        // 现有
      "updatedAt": "2026-11-16T04:31:20.010Z",        // 现有
      "sandboxId": "sbx_4d21ab77",                    // 现有
      "runShortId": "01J9Z6K3",                       // 现有
      "sandboxShortId": "4d21ab77"                    // 现有
    },
    "prompt": "这两张截图有什么区别？只列 UI 变化。",    // 现有：文本投影
    "output": "1. 主按钮由灰变蓝；2. 间距加大…",         // 现有
    "resultJson": "{\"summary\":\"主按钮由灰变蓝…\"}",   // 现有
    "driver": "docker",                                // 现有
    "imageRef": "chaitin/agent-compose-guest:latest"   // 现有
  },
  "warnings": [
    "prompt was prepended as the first text part because input is also set" // 🆕 合并说明
  ]
}
```

说明：本 PR 的响应**没有新增字段**；`prompt` 仍是文本投影（多个 text part 按序用两个换行连接），媒体不回传。§3.3 的 `inputAttachments` 在后续 PR 加入，用于 UI/审计展示"这一轮发了什么"。

### 11.6 示例 6：能力查询（GetEngineCapabilities）

```jsonc
{
  "providers": [
    {
      "provider": "codex",                             // 现有
      "preferredProtocols": ["responses"],                // 现有（codex 只支持 Responses）
      "features": [                                    // 现有
        { "feature": "structured_output", "supported": true },
        { "feature": "session_resume", "supported": true },
        { "feature": "streaming", "supported": true },
        { "feature": "skill_injection", "supported": false }
      ],
      "mediaTypes": ["image/png", "image/jpeg", "image/gif", "image/webp"], // 🆕
      "maxAttachmentBytes": "10485760",                // 🆕
      "mediaOrdering": "ENGINE_PROVIDER_MEDIA_ORDERING_AGGREGATE"          // 🆕
    },
    {
      "provider": "claude",
      "preferredProtocols": ["anthropic_messages"],   // 现有
      "mediaTypes": ["image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"], // 🆕
      "maxAttachmentBytes": "10485760",                // 🆕
      "mediaOrdering": "ENGINE_PROVIDER_MEDIA_ORDERING_INTERLEAVED"        // 🆕
    }
    // 其余 provider（opencode / pi / dsh）省略
  ],
  "inputLimits": {                                     // 🆕 新增字段
    "maxPartBytes": "10485760",                        // 🆕
    "maxTurnBytes": "33554432",                        // 🆕
    "maxParts": 20,                                    // 🆕
    "allowedMediaTypes": ["image/png", "image/jpeg", "image/gif",
                          "image/webp", "application/pdf"] // 🆕
  }
}
```

客户端契约：**`inputLimits` 不存在 = 该 daemon 不支持多模态**，不要发 `input`（旧 daemon 会丢弃未知字段而不报错）。

### 11.7 Go 调用形态

```go
import (
    "connectrpc.com/connect"

    agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
    "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

client := agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)

resp, err := client.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
    ProjectId: "prj_7f3c9a12",                          // 现有
    AgentName: "reviewer",                               // 现有
    Source:    agentcomposev2.RunSource_RUN_SOURCE_API,  // 现有
    // Prompt 可留空，也可与 Input 同时发（会前置为首个 text part）。 // 说明
    Input: []*agentcomposev2.AgentInputPart{             // 🆕
        { Part: &agentcomposev2.AgentInputPart_Text{
            Text: "这两张截图有什么区别？",
        } },
        { Part: &agentcomposev2.AgentInputPart_Media{
            Media: &agentcomposev2.AgentInputMedia{
                MediaType: "image/png",
                Filename:  "before.png",
                Source: &agentcomposev2.AgentInputMedia_Bytes{
                    Bytes: &agentcomposev2.AgentInputMediaBytes{Data: png, Sha256: digest},
                },
            },
        } },
    },
}))
```

workspace 路径写法：

```go
Source: &agentcomposev2.AgentInputMedia_WorkspacePath{
    WorkspacePath: &agentcomposev2.AgentInputMediaWorkspacePath{
        Path:   "reports/2026-Q3.pdf", // 相对 workspace
        Sha256: digest,                // 可选
    },
},
```

### 11.8 字段清单

| 消息 | 字段 | 状态 | 说明 |
| --- | --- | --- | --- |
| `RunAgentRequest` | `projectId` … `labels`（1–17） | 现有 | 语义不变 |
| | `prompt` | 现有 | 可与 `input` 同时发，会前置为首个 text part |
| | `input` | 🆕 | 有序 part；为空时退回纯 prompt |
| `RunDetail` | `prompt`、`output`、`resultJson`… | 现有 | 语义不变（`inputAttachments` 见 §3.3） |
| `AgentInputPart` | `text` | 🆕 | 空字符串非法 |
| | `media` | 🆕 | 与 `text` 二选一 |
| `AgentInputMedia` | `mediaType` | 🆕 | 必填 |
| | `filename` | 🆕 | 可选 |
| | `bytes` / `workspacePath` | 🆕 | source 二选一 |
| `AgentInputMediaBytes` | `data`、`sha256` | 🆕 | `sha256` 可选 |
| `AgentInputMediaWorkspacePath` | `path`、`sha256` | 🆕 | 路径相对 workspace |
| `EngineProviderCapabilities` | `provider`、`preferredProtocols`、`features` | 现有 | 语义不变 |
| | `mediaTypes`、`maxAttachmentBytes`、`mediaOrdering` | 🆕 | 能力协商 |
| `GetEngineCapabilitiesResponse` | `drivers`…`observations` | 现有 | 语义不变 |
| | `inputLimits` | 🆕 | 存在性兼作支持探测 |

### 11.9 常见拒绝

| 请求 | 返回 |
| --- | --- |
| `input` 与 `prompt` 同时非空 | 正常：`prompt` 前置为首个 text part，`warnings` 说明 |
| `mediaType` 缺失/非规范 | `invalid_argument` |
| 声明 MIME 与魔数不符 | `invalid_argument` |
| 图片超过单 part 上限 | `invalid_argument`，detail 带实际上限 |
| 给 codex 发 PDF | `invalid_argument`，detail 带 `supportedMediaTypes` |
| `workspacePath` 逃逸根/不存在 | `invalid_argument` / `not_found` |
| guest 镜像不支持（无 `media_input`） | `unimplemented`（`UNSUPPORTED`），提示升级镜像 |
