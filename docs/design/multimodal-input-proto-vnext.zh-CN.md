# Sandbox 启动接口：任意文件输入（vNext）

- 状态：接口提案（本 PR 实现）
- 日期：2026-11-16
- 承接：[`multimodal-input-interface.zh-CN.md`](multimodal-input-interface.zh-CN.md)（业界调研与 provider 能力矩阵）
- 测试报告：[`multimodal-input-e2e-report.zh-CN.md`](multimodal-input-e2e-report.zh-CN.md)

## 0. 结论

1. **接口只表达"一段文本 + 若干文件"**，不表达图片、PDF、音频等具体模态。任何文件类型都接受，类型只作为可选元数据。
2. **实现不与任何 provider 耦合**：daemon 把文件写进 sandbox 的 workspace（harness 的工作目录），并把 sandbox 内路径追加到提示词。harness 用自己已有的文件工具读取，codex/claude/opencode/pi/dsh 走完全相同的路径。
3. **文件在本轮与后续追问都可用**：文件落在 workspace 的 `.agent-compose/uploads/<run-id>/turn-0/` 下，不随 run 结束删除；同一 sandbox 的后续 run 仍能访问。
4. **限制集中可配**：`AGENT_INPUT_FILE_LIMIT_BYTES`（默认 10 MiB）、`AGENT_INPUT_TURN_LIMIT_BYTES`（默认 32 MiB）、`AGENT_INPUT_FILE_LIMIT`（默认 20 个文件），未配置时使用默认值；`AGENT_INPUT_FILE_LIMIT` 只统计文件，不统计文本段。
5. **不新增 RPC**：`RunAgent` / `StartAgentRun` / `StreamAgentRun` / `AttachAgentRun` 共用 `RunAgentRequest`；只在 `RunAgentRequest` 上追加 `input`。
6. **runtime 不需要改动**：不传 `--content-file`、不构造 provider content block；guest 镜像保持原样即可（K8s 通过既有 `GuestFileWriter` 把文件推进 Pod 的 workspace）。

## 1. 为什么改掉"provider 原生模态"

初版按业界 part 模型把图片/PDF 作为原生附件交给各 provider，代价是：

- 每个 provider 的实现方式不同（codex 路径、claude base64、opencode `-f`、pi `@path`、dsh attachment ref），任意一个升级都可能破坏；
- 非图片/PDF 类型还要再做一套"通用文件"分支；
- 能力矩阵需要按 provider 声明 `media_types`/`media_ordering`，调用方也要理解这些差异。

改为"落 workspace + 提示词给路径"后：

- 接口只有一种文件语义，Word、代码、二进制、压缩包与图片完全一样；
- 新增 provider 不需要任何多模态适配；
- 文件天然对后续追问可见，不需要为多轮保存附件状态；
- 代价是**模型看到的是路径而不是原生图片块**，能否理解取决于 harness 的文件工具（这是所有编码 agent 的常规工作方式）。

## 2. proto 定义

```proto
// AgentInputPart is one ordered piece of one human turn.
message AgentInputPart {
  oneof part {
    // Plain text. An empty string is invalid; omit the part instead.
    string text = 1;
    // One file. Exactly one source must be set.
    AgentInputFile file = 2;
  }
}

message AgentInputFile {
  // Required. Display name and, after sanitizing, the materialized file name.
  string filename = 1;
  // Optional IANA media type. Informational only; it never gates acceptance.
  string media_type = 2;
  oneof source {
    AgentInputFileBytes bytes = 3;
    AgentInputFileWorkspacePath workspace_path = 4;
    // Added after this baseline by the two-step upload change.
    AgentInputFileUpload upload = 5;
  }
}

message AgentInputFileBytes {
  bytes data = 1;
  string sha256 = 2;
}

message AgentInputFileWorkspacePath {
  // Workspace-relative, slash-separated; same grammar as SandboxWorkspaceEntry.path.
  string path = 1;
  string sha256 = 2;
}

message AttachHumanMessage {
  string text = 1;
  map<string, string> metadata = 2;
  // Ordered text and file input for this human turn; same materialization as
  // RunAgentRequest.input.
  repeated AgentInputPart input = 3;
}

message EngineInputLimits {
  uint64 max_file_bytes = 1;
  uint64 max_turn_bytes = 2;
  uint32 max_files = 3;
}
```

字段落点：

```proto
message RunAgentRequest {
  // ... existing fields 1..17 unchanged ...

  // Ordered text and file input. A non-empty prompt is prepended as the first
  // text part. Files are materialized inside the sandbox workspace and their
  // paths are appended to the prompt. Any file type is accepted up to the
  // configured limits.
  repeated AgentInputPart input = 18;
}

message GetEngineCapabilitiesResponse {
  // ... existing fields 1..6 unchanged ...
  EngineInputLimits input_limits = 7;
}
```

`EngineProviderCapabilities` 不新增任何字段：文件输入与 provider 无关。

`AgentInputFile` 的字段 5 在本文档描述的 #852 基线中是 `reserved`；后续的“先上传后引用”变更已把它实现为 `AgentInputFileUpload upload = 5`（配合 `UploadRunInput`），不需要改动现有字段布局。

## 3. 语义

**R1 prompt 与 input 合并。** `input` 为空 → 完全等价于旧 `prompt`；两者同时非空 → `prompt` 作为首个 text part 前置并产生一条 warning；`input` 非空且无 prompt → 直接用 `input`。一轮至少需要一个非空文本 part。

**R2 任意文件类型。** 不按 MIME 做白名单，也不做魔数校验（类型是可选元数据）。只校验：文件名非空、来源合法、大小/数量上限、可选 sha256。

**R3 两种来源。**

- `bytes`：daemon 把字节写到 `<sandbox>/workspace/.agent-compose/uploads/<run-id>/turn-0/<safe-name>`；K8s 通过 `GuestFileWriter` 推到 guest 的同名路径。
- `workspace_path`：不拷贝，解析为 `<guest workspace>/<path>`；daemon 能读到 workspace 时顺带校验大小，读不到（K8s）时跳过大小校验（见 §7）。

**R4 文件名。** `filename` 只作为展示名与物化名的基础：去目录、只保留 `[A-Za-z0-9._-]`、截断到 128 字节；重名时追加 `-<index>`；没有扩展名时按 `media_type` 补一个常见扩展名。

**R5 提示词注入。** 物化完成后，在提示词末尾追加：

```
Attached files (read them with your file tools):
- /workspace/.agent-compose/uploads/<run-id>/turn-0/data.csv (text/csv)
```

顺序按 `input` 中文件的出现顺序；文本本身不变。

**R6 生命周期。** 文件在 workspace 内，不随 run 结束删除；一次性 sandbox 随 sandbox 回收。同一 sandbox 的后续 run 可以直接使用上一轮路径（提示词已包含）。

**R9 对话轮次。** 非交互多轮就是同一 sandbox 上的连续 `RunAgent`：第 N 轮可以带新文件，第 N+1 轮即使不带文件也能读取此前所有路径。交互式 `AttachAgentRun` 的 `AttachHumanMessage.input` 使用同一套物化逻辑（目录按 `turn-<n>` 编号），因此首轮与中间轮注入的文件都能被后续轮访问。

**R7 幂等。** `client_request_id` 语义不变；附件不参与 key。

**R8 失败显式。** 超限、空文件、非法 workspace 路径、sha256 不符都在 run 创建前返回 `InvalidArgument`；`command` 与 `input` 互斥。

## 4. 沙箱内落盘与提示词

```text
client ── RunAgent(input=[text, file, ...]) ──▶ daemon
                                               │ 1. 合并 prompt/input，校验限制
                                               │ 2. bytes → <sandbox>/workspace/.agent-compose/uploads/<run>/turn-0/<name>
                                               │ 3. K8s: GuestFileWriter 推送到 guest 同路径
                                               │ 4. prompt = 文本 + "Attached files" 路径清单
                                               ▼
                              agent-compose-runtime prompt --message-file <prompt.txt>
                                               ▼
                              harness 用文件工具读取 workspace 内路径
```

runtime、guest ABI、provider 适配层都不参与文件处理。

## 5. 限制与配置

| 环境变量 | 默认 | 语义 |
| --- | --- | --- |
| `AGENT_INPUT_FILE_LIMIT_BYTES` | 10 MiB | 单个文件上限 |
| `AGENT_INPUT_TURN_LIMIT_BYTES` | 32 MiB | 单轮全部文件总字节 |
| `AGENT_INPUT_FILE_LIMIT` | 20 | 单轮文件数上限（文本段不计入） |

非法或非正数配置会记录 warning 并回退默认值。`GetEngineCapabilities.input_limits` 返回实际生效值，调用方可据此预校验。

## 6. 安全

- 只接受 workspace 相对路径，拒绝绝对路径、`..` 逃逸、NUL、超长路径；
- 文件名经过严格清洗，不参与路径解析，重名由 `-<index>` 消解；
- 上传目录固定在 `.agent-compose/uploads/`，不覆盖 workspace 内已有文件（重名只影响本次上传目录）；
- `bytes` 与 `workspace_path` 均可选 sha256 校验：`bytes` 在准入时直接比对，`workspace_path` 在共享文件系统上由 daemon 读取文件并比对（K8s 见 §7）；共享文件系统上，`workspace_path` 的每个分量由 daemon `lstat` 检查，拒绝符号链接、缺失文件与非普通文件，单文件与单轮总量都计入配置上限；上传目录以 workspace 根为锚逐段创建，已存在的符号链接一律拒绝，文件以 `O_EXCL` 写入，不跟随 workspace 中预置的符号链接；
- 不把类型当安全边界：任意类型都可能包含主动内容，但文件只交给 sandbox 内的 agent 工具，不进入控制面解析。

## 7. 已知限制

- **K8s 上的 workspace 路径**：daemon 读不到 Pod 内文件，`workspace_path` 来源不预检存在性、符号链接、大小与 sha256；文件不存在或不可读时由 harness 在读取时报错。是否需要 daemon 侧预检由驱动的共享文件系统能力决定（非 K8s 驱动传 nil `GuestFileWriter`，K8s 传非 nil），与 daemon 本地 workspace 目录是否存在无关。内联 `bytes` 始终在 daemon 侧校验。需要严格上限与摘要校验的调用方应使用 `bytes`。
- **模型是否"看懂"文件**：取决于 harness 的工具与上游模型；本接口只保证文件可访问、路径已给出。
- **交互式 attach 的实时文件流**：服务端与单测已支持 `AttachHumanMessage.input`；本轮用同 sandbox 连续 run 做了多轮实测，attach 的实时 h2c 客户端未纳入本次自动化（不影响同一物化逻辑）。
- **不持久化文件描述**：`RunDetail` 只记录文本投影，不记录附件列表；后续 PR 再补。

## 8. 兼容性

| daemon | 客户端 | 行为 |
| --- | --- | --- |
| 新 | 旧（只发 prompt） | 完全不变 |
| 新 | 新（prompt + input） | prompt 前置，文件落盘并注入路径 |
| 旧 | 新（发 input） | 旧 daemon 丢弃未知字段，附件不会生效；客户端应先读 `input_limits` 判断支持 |
| 新 | 旧 guest | 正常：只用到既有 prompt 文件与 workspace |

回滚：`input` 是新字段；任一层回滚都不影响纯文本 run。

## 9. 实施顺序

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0 | proto + 生成代码 + config 限制 + capability `input_limits` | `task lint`、`task test`；纯文本回归 |
| P1（本 PR） | 准入、物化到 workspace、提示词注入、任意类型、大小/摘要/路径校验 | 5 provider 文本回归；CSV/ZIP/图片等文件可被 agent 读取；超限/逃逸/摘要失败各一条 |
| P1（本 PR，续） | 续轮 `AttachHumanMessage.input`（按 turn 编号落盘） | 交互 attach 的 start/human 帧单测；同 sandbox 多轮 run 实测 |
| P2 | 响应侧附件描述 | `RunDetail` 可展示文件 |

## 10. 接口示例

### 10.0 多轮对话

```text
turn 1  RunAgent(input=[data.csv], sandbox=KEEP_RUNNING)   → 读取 beta=2
turn 2  RunAgent(sandbox_id, prompt="再读一次 data.csv")    → 仍能访问，beta=2
turn 3  RunAgent(sandbox_id, input=[second.csv])           → 读取 gamma=3
turn 4  RunAgent(sandbox_id, prompt="两个文件都读")          → 2,3
```

文件按上传轮次落在 `.agent-compose/uploads/<run-id>/turn-<n>/`，不随 run 删除。

### 10.1 文本 + CSV

```jsonc
{
  "projectId": "prj_7f3c9a12",                    // 现有
  "agentName": "reviewer",                         // 现有
  "source": "RUN_SOURCE_API",                      // 现有
  "prompt": "分析这份 CSV。",                       // 现有（前置为首个 text part）
  "input": [                                       // 🆕
    {
      "file": {
        "filename": "data.csv",
        "mediaType": "text/csv",                   // 可选，仅元数据
        "bytes": { "data": "bmFtZSx2YWx1ZQpiZXRhLDIK", "sha256": "…" }
      }
    }
  ]
}
```

daemon 写入 `/workspace/.agent-compose/uploads/<run>/turn-0/data.csv`，提示词追加：

```
Attached files (read them with your file tools):
- /workspace/.agent-compose/uploads/<run>/turn-0/data.csv (text/csv)
```

### 10.2 图片或任意二进制

```jsonc
"input": [
  { "text": "这两张图有什么区别？" },
  { "file": { "filename": "before.png", "mediaType": "image/png", "bytes": { "data": "iVBORw0KGgo..." } } },
  { "file": { "filename": "after.png", "mediaType": "image/png", "bytes": { "data": "iVBORw0KGgo..." } } }
]
```

文件同样落在 workspace，模型/harness 用 Read 等工具查看；不经过 provider 原生图片通道。

### 10.3 引用 workspace 已有文件

```jsonc
"input": [
  { "text": "检查这个文件。" },
  { "file": { "filename": "report.docx", "workspacePath": { "path": "docs/report.docx" } } }
]
```

不拷贝，提示词给出 `/workspace/docs/report.docx`。

### 10.4 能力查询

```jsonc
{
  "inputLimits": {
    "maxFileBytes": "10485760",
    "maxTurnBytes": "33554432",
    "maxFiles": 20
  }
}
```

### 10.5 常见拒绝

| 请求 | 返回 |
| --- | --- |
| `input` 与 `prompt` 同时非空 | 正常：prompt 前置，warnings 说明 |
| 没有文本 part | `invalid_argument` |
| 单个文件超限 | `invalid_argument`，detail 带实际上限 |
| `workspacePath` 逃逸根 | `invalid_argument` |
| `sha256` 不匹配 | `invalid_argument` |
| `command` + `input` | `invalid_argument` |
| 交互式启动带 `input` | 正常：`prompt` 前置，文件写入 `turn-0` |
