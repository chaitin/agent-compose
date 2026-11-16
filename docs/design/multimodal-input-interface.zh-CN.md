# Agent 多模态输入接口调研与设计

- 状态：调研完成 + 接口提案（未实现）
- 日期：2026-11-16
- 适用面：`RunAgent` / `StartAgentRun` / `AttachAgentRun` / Go SDK `chat.Conversation`，以及它们背后的 guest runtime 与五个 provider runner
- 接口定稿：[`multimodal-input-proto-vnext.zh-CN.md`](multimodal-input-proto-vnext.zh-CN.md)（proto 全文与原生/启动脚本分工）
- 相关文档：[`engine_capability_matrix.md`](engine_capability_matrix.md)、[`proto_v2_api_contract.md`](proto_v2_api_contract.md)、[`sandbox-workspace-file-api.zh-CN.md`](sandbox-workspace-file-api.zh-CN.md)、[`runtime-bidirectional-stream-design.md`](runtime-bidirectional-stream-design.md)

## 0. 结论（TL;DR）

1. **业界已经收敛出一个共同形状**：一次用户输入 = **有序的、带判别标签的 content part 数组**；每个非文本 part 携带 **显式 MIME**，字节通过 **内联 base64 / 远程 URL / 上传句柄** 三者之一提供。OpenAI Chat/Responses、Anthropic Messages、Gemini、MCP、A2A 全部如此，差别只在嵌套层级和字段拼写。
2. **我们的上游不是模型 API，而是跑在沙箱里的 agent CLI**。因此"上传句柄"的本地等价物是 **沙箱内文件路径**：文件已经在执行环境里，`codex -i`、`opencode -f`、`pi @path` 都原生吃路径。这决定了我们的接口应该是"业界 part 模型 + 沙箱路径作为一种 source"。
3. **五个 provider 的能力差异很大，但有一个稳定的最小公倍数：位图图片（PNG/JPEG/GIF/WebP）**。PDF 只有 claude 与 opencode 能真正原生消费；音频/视频没有任何一个 provider 作为模型输入接受（DSH 的语音是 STT 预处理，不是输入模态）。**因此 v1 只定义 media part + 位图/PDF 的 provider 白名单，不定义 audio/video 语义**——schema 留好扩展位，语义不提前编造。
4. **保序能力必须写进契约**：claude、dsh 能真正按 part 顺序交错文本与媒体；codex、opencode、pi 的 CLI 把"本轮所有媒体"整体附加到同一条用户消息上，文本会被合并。这是可接受但必须显式声明的降级。
5. **当前链路的每一层都是纯文本**：proto `prompt string` → 沙箱内 `prompt.txt` → `agent-compose-runtime prompt --message-file` → `runPrompt(promptText: string)` → CLI。多模态不是"加个字段"，而是贯穿 5 层的契约升级；其中 **guest 镜像存在版本偏斜风险**，必须用 guest ABI manifest 的 provider feature 做显式门禁，不能靠"新 runtime 忽略旧参数"静默降级。
6. **内部已有现成先例可复用**：依赖 `github.com/chaitin/ai-api-protocol-bridge` 的 `FilePart{type: image|document, media_type, data|url|file_id, filename}` 已经是 LLM 侧的标准形状，且 `pkg/llms` 的 facade 对请求体是**逐字节透传**（只改 model 和文本 part 的 type）。控制面的 part 模型应当与它同构，避免两层模型互相翻译。

---

## 1. 背景

### 1.1 现状：多模态在最外层就不存在

一次 Agent Run 的输入今天只能是一段文本，链路如下（每一层都是 `string`）：

| 层 | 位置 | 现状 |
| --- | --- | --- |
| Connect RPC | `proto/agentcompose/v2/agentcompose.proto:1296` | `RunAgentRequest.prompt string` |
| Connect RPC（追加消息） | `proto/agentcompose/v2/agentcompose.proto:1865` | `AttachHumanMessage.text string` |
| Go SDK | `sdk/go/chat/conversation.go:182`、`message.go:26` | `Send(ctx, text string)`、`Message.Text string` |
| daemon → guest | `pkg/agentcompose/adapters/agent_runner.go:552` | 把 prompt 写进 guest 文件，执行 `agent-compose-runtime prompt --message-file <path>` |
| guest runtime | `runtime/javascript/src/prompt.ts:89` | 读文件得到 `promptText`，分发 `runPrompt(promptText)` |
| provider runner | `runtime/javascript/src/runners/*.ts` | `runPrompt(promptText: string)`，见 codex.ts:101、claude.ts:373、opencode.ts:62、pi.ts:166、dsh.ts:92 |
| 交互会话 | `runtime/javascript/src/stream.ts:78`、`:218` | `human_message` 帧只接受 `message: string` |
| DSH profile | `assets/.dsh/profiles/agent-compose/runner.js:146` | `createUserMessage({content:[{type:'text', text}]})` |

另外，数据面只有"读"没有"写"：`proto/agentcompose/v2/sandbox_workspace.proto` 只提供 `ListSandboxWorkspace` 和 `DownloadSandboxWorkspaceFile`，调用方无法把本地图片放进沙箱（`docs/design/sandbox-workspace-file-api.zh-CN.md` 明确解释了为什么当时不做 upload）。这意味着"调用方给 agent 一张图"今天必须绕道 workspace preset 或让 agent 自己去下载。

### 1.2 目标

- 定义一套**科学、可论证、与业界共识同构**的多模态输入接口，覆盖单轮 run、续轮 human message、交互 attach 三种入口。
- 明确五个 provider 各自的能力边界与适配方式，避免"接口定义了但一半 provider 静默忽略"。
- 为后续 `agent-compose` 调用 agent sandbox 的 API 升级提供可直接落地的 proto / SDK / runtime 契约。

### 1.3 非目标（本提案明确不做）

- **不做音频/视频输入语义**。没有 provider 把音频当模型输入消费；DSH 的 voice bundle 是语音转文字，不是多模态输入。schema 上它们只是 `media_type` 的取值，v1 一律按不支持拒绝。
- **不做 daemon 侧转码/缩放**。缩放交给各 CLI（codex 2048px、pi 2000px、DSH 2048px、Claude Code 自行 recompress），我们只做准入校验。
- **不做 URL 抓取**。见 §5.4 的取舍。
- **不做模型级模态路由**。`pkg/llms` 的 model catalog 目前没有 modality 元数据，v1 由 provider 侧报错。

---

## 2. 业界接口调研

### 2.1 主流接口对照

| 接口 | 文本 | 图片载体 | 文档载体 | 音频 | 视频 | 顺序 | 上传/句柄 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| OpenAI Chat Completions | `{"type":"text"}` | `image_url.url` = HTTPS URL 或 `data:` URL，带 `detail` | `file.file_data`(b64) 或 `file_id` | `input_audio.data`(b64)+`format` wav/mp3 | 无 | `content[]` 有序 | Files API → `file_id` |
| OpenAI Responses | `{"type":"input_text"}` | `input_image.image_url`（扁平）+ `detail` 或 `file_id` | `input_file.file_data` / `file_id` / `file_url` | `input_audio.data`+`format` | 无 | `input[]` 有序 | Files API → `file_id` |
| Anthropic Messages | `{"type":"text"}` | `image.source` = `base64`(jpeg/png/gif/webp) 或 `url` | `document.source` = base64 PDF / `text` / `content` / `url` | 无 | 无 | `content[]` 有序 | 无 `file_id`（SDK 0.x 类型中确认） |
| Gemini `generateContent` | `{"text":...}` | `inline_data{mime_type,data}` 或 `file_data{mime_type,file_uri}` | 同图片的 Blob/FileData | 支持 | 支持（`video_metadata`） | `parts[]` 有序 | Files API → `file_uri` |
| MCP | `{"type":"text"}` | `{"type":"image","data":b64,"mimeType"}` | `resource`（`text`/`blob`）或 `resource_link`(uri) | `{"type":"audio","data":b64}` | 无 | `content[]` 有序 | `resource_link` 由客户端解析 |
| A2A v1 | `oneof text` | `oneof raw`(bytes) 或 `url` | 同左，带 `filename`/`media_type` | 无专门类型 | 无专门类型 | `parts[]` 有序 | `url` 由对端可抓取 |
| DSH SDK prompt | `{"type":"text"}` | `{"type":"image","data":b64,"mimeType"}`（4 种位图） | `{"type":"file","receiptId"}`（仅 Web GUI 上传） | 无 | 无 | `content[]` 有序 | 上传 receipt → ref |
| 内部 `protocolbridge` | `Part{type:text}` | `FilePart{type:image, media_type, data\|url\|file_id}` | `FilePart{type:document,...}` | 无 | 无 | `Parts[]` 有序 | `file_id` |

外部依据：OpenAI Python SDK 的 `ChatCompletionContentPartParam` / `ResponseInputItemParam` 类型定义、Anthropic Messages 类型定义（`ImageBlockParam.source = Base64ImageSource | URLImageSource`、`DocumentBlockParam.source = Base64PDFSource | PlainTextSource | ContentBlockSource | URLPDFSource`）、Gemini `content.proto` 的 `Part oneof`、MCP 2025-06-18 schema 的 `ContentBlock`、A2A v1 `a2a.proto` 的 `Part oneof content`。

### 2.2 收敛出来的共同模式

1. **有序数组**：`content[]` / `parts[]` / `input[]`，文本与媒体共享一个序列，顺序即语义。
2. **判别标签**：`type`（OpenAI/Anthropic/MCP）、`kind`（A2A v0.3）、proto `oneof`（Gemini/A2A v1）。标签是封闭集合，未知值应拒绝而不是猜测。
3. **载体三选一**：内联字节、可抓取 URL、上传后句柄。三者不是互斥的产品选择，而是同一 part 的三种 source。
4. **显式 MIME**：`media_type` / `mimeType` / `mime_type`。OpenAI 图片是唯一例外（MIME 只能从 data URL 或 HTTP 响应推断），这也是它最容易出错的地方。
5. **工具结果也是 part**：Anthropic `tool_result.content`、MCP tool content、Gemini `function_response.parts` 都复用同一套 part 模型。这意味着 part 模型应该成为整个输入/输出的统一词汇，而不是只给"用户消息"用。

### 2.3 没有收敛的地方

| 维度 | 分歧 | 对我们的影响 |
| --- | --- | --- |
| 音频 | OpenAI/MCP/Gemini 有；Anthropic 没有 | 不定义语义，只留 MIME 扩展位 |
| 视频 | 只有 Gemini 原生支持 | 同上 |
| URL 可信度 | A2A 假设对端能抓；MCP `resource_link` 由客户端解析；agent CLI 基本不吃 URL | 引入 SSRF/出口策略问题，推迟 |
| 句柄语义 | OpenAI/Anthropic 是上传文件 id；Gemini 是 `file_uri`；DSH 是 attachment ref | 我们选"沙箱路径"作为本地句柄 |
| 嵌套层级 | Chat 嵌套 `image_url.url`，Responses 扁平，Anthropic 全在 `source` 下 | 我们不再造第四种嵌套：统一 `media.source` |

### 2.4 对 agent sandbox API 的关键推论

我们的调用链是 `调用方 → agent-compose daemon → 沙箱内 agent CLI → 模型 API`。这与"调用方直接调模型 API"有一个本质区别：

> **字节已经（或可以）位于执行环境内部，因此"沙箱内路径"就是最自然的 file handle。**

这与业界的 `file_id` 语义等价——都是"不透明句柄，由执行侧解析"，只是解析器从"供应商文件服务"变成"沙箱文件系统"。它带来三个好处：

- 零拷贝：图片不需要 base64 穿过 Connect 层两次；
- 多轮天然有效：同一个 sandbox 的 workspace 在 resume 后仍然存在；
- 可与现有只读 API 对齐：`DownloadSandboxWorkspaceFile` 能读到的路径，就是可以作为附件引用的路径。

但它要求调用方先把文件放进沙箱，而今天没有 upload RPC。因此 v1 必须同时支持 **内联字节**（远程调用方唯一可行的路径）和 **workspace 路径**（零拷贝、适合 agent 自己产出的文件）。

---

## 3. 五个 provider 的支持矩阵

调研方法：本地安装的 CLI 实测 + 包内 SDK 类型定义 + 仓库内既有 runner 代码。版本：`codex-cli 0.162.0`、`claude 2.1.220`、`opencode 1.18.14`、`pi 0.84.2`、DSH 取本地 checkout。

### 3.1 总表

| Provider | 图片 | PDF/文档 | 音频 | 提交方式（字节载体） | 单轮 | 续轮/多轮 | 保序 | 主要限制 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| codex | ✅ png/jpeg/gif/webp | ❌ | ❌ | **本地路径**（`-i` / SDK `local_image`） | ✅ | ✅ `resume`/`fork` 都有 `-i` | ❌（文本合并，媒体整体附加） | 2048px 自动缩放；不支持则替换为占位文本（软失败） |
| claude | ✅ png/jpeg/gif/webp | ✅ PDF（document block） | ❌ | **base64** content block（stream-json / SDK 异步迭代） | ✅ | ✅ 同一形状续轮 | ✅ | Anthropic API 5MB/图；CLI 自行 recompress |
| opencode | ✅ png/jpeg/gif/webp | ✅ PDF | ❌ | **本地路径**（`-f/--file`，内部转 data URL） | ✅ | ✅ `--session` | ❌ | 10 MiB 常量上限；MIME 白名单；模型需 `capabilities.attachment` |
| pi | ✅（MIME 探测） | ⚠️ 非图片按 UTF-8 **文本内联**（`<file>` 块），PDF 会乱码 → 按不支持处理 | ❌ | **本地路径**（位置参数 `@path`）或 RPC `images[]`（base64） | ✅ | ✅ `--session-id` | ❌（CLI 模式） | 自动缩放到 2000×2000；`--mode rpc` 才是结构化入口 |
| dsh | ✅ png/jpeg/gif/webp | ⚠️ `file` block 无原生模态，模型侧投影为路径文本 | ❌ | **attachment ref**（`ctx.attachments.saveImages/saveFile` → `content` block） | ✅ | ✅ `DSH_RESUME` | ✅ | 4 种位图；模型路由需声明 image 输入，否则 `MODEL_DOES_NOT_SUPPORT_IMAGES` |

### 3.2 codex

- CLI：`codex exec -i/--image <FILE>...`，`codex exec resume` 与 `codex exec fork` 同样有 `-i`（本地 `--help` 实测）。只接受**路径**（Rust `PathBuf`），不接受 URL/data URL。
- SDK（`@openai/codex-sdk`，runtime 依赖 `^0.144.1`）：`UserInput = {type:"text",text} | {type:"local_image",path}`，`Input = string | UserInput[]`（`dist/index.d.ts` 实测）；内部仍拼成 `codex exec ... --image <path>`。
- 交互：Ctrl+V 把剪贴板图片写成临时 PNG 再引用路径。
- 当前 runner：`runtime/javascript/src/runners/codex.ts:101` 调用 `thread.runStreamed(promptText, ...)`，改造成 `Input[]` 即可。

### 3.3 claude

- CLI：没有 `--image` 参数。多模态入口是 `claude -p --input-format stream-json`，stdin 每行一个 `SDKUserMessage`：`{"type":"user","message":{"role":"user","content":[{"type":"text",...},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]},"parent_tool_use_id":null}`。
- SDK（`@anthropic-ai/claude-agent-sdk`，runtime 依赖 `^0.3.206`）：`query(prompt: string | AsyncIterable<SDKUserMessage>)`，`SDKUserMessage.message` 就是 Anthropic `MessageParam`，content 可以是 `image` / `document` 块。
- **注意**：SDK 的单条字符串模式不支持图片，必须切到异步迭代模式——这是当前 runner（`claude.ts:373` 传 `prompt: promptText`）必须改的地方。
- 图/文档限制沿用 Anthropic API：单图 5MB、`image/jpeg|png|gif|webp`；PDF 走 `document` 的 base64 source。

### 3.4 opencode

- CLI：`opencode run -f/--file <path>...`（`string[]`，本地 `--help` 实测）。传给服务端后成为 `FilePartInput{type:"file", mime, filename, url}`，`url` 是 `data:<mime>;base64,...`（编译产物中的构造逻辑可验证）。
- MIME 白名单：`application/pdf, image/gif, image/jpeg, image/png, image/webp`；附件字节上限为 10 MiB 量级常量（`10485760`），超出直接拒绝并给出文本说明。
- 模型能力：provider 模型带 `capabilities.attachment: boolean`，不支持附件的模型会在 UI/日志层暴露。
- 当前 runner：`opencode.ts:62` 的 `buildArgs` 只拼 prompt；加 `-f <guest path>` 即可。

### 3.5 pi

- CLI：`pi @a.png @b.md "问题"`。`@file` 由 `processFileArguments`（包内 `dist/cli/file-processor.js`）处理：识别为图片的读成 `{type:"image", mimeType, data(base64)}` 附件；**非图片按 UTF-8 文本内联**为 `<file name="...">...</file>` 块。后者对 PDF 等二进制会产生乱码，因此 v1 对 pi 只放行图片。图片默认缩放到 2000×2000（`docs/settings.md:184`）。
- RPC：`pi --mode rpc` 的 `prompt` 命令支持 `{"type":"prompt","message":"...","images":[{"type":"image","data":"<b64>","mimeType":"image/png"}]}`（`docs/rpc.md`），这是比 CLI 位置参数更干净的编程入口。
- 当前 runner：`pi.ts:139` 起用 `--mode json` + `args.push(promptText)`；要么在 prompt 前插入 `@path`，要么整体切到 RPC 模式。

### 3.6 dsh

- DSH 自身的入口（Web GUI / SDK / ACP）接受图片 base64 和上传 receipt；headless CLI 只有文本。附件在 DSH 内部是 durable ref（`ImageAttachmentRef{attachmentId, mediaType, bytes, width, height}` / `FileAttachmentRef`），不是裸字节。
- 我们的集成不是调 DSH CLI，而是**内嵌 profile**：`assets/.dsh/profiles/agent-compose/runner.js:146` 调 `agent.followup(createUserMessage({content:[{type:'text',text}]}))`。
- 因此 dsh 的适配点在 profile，而不是 runner 命令行：runner 需要把附件清单（路径 + MIME + 文件名）通过环境变量/fixed path 交给 profile，profile 用 `ctx.attachments.saveImages([{data, mediaType, name}])` / `saveFile(...)` 换 ref，再拼 `{type:'image', attachment}` / `{type:'file', attachment}` content block。
- 限制：图片仅 4 种位图；file block 在模型侧被投影成路径文本（不是原生文档模态）；模型路由必须声明 image 输入。

### 3.7 关键结论

- **最小公倍数 = 位图图片**；**第二档 = PDF**（claude、opencode 真支持；pi 退化为文本；dsh 退化为路径文本；codex 不支持）。
- **字节载体的最小公倍数 = 沙箱内路径**（codex/opencode/pi CLI 都吃路径；claude 需要 base64；dsh 需要 ref）。所以 runner 层应以"沙箱路径"为内部主表示，由每个 adapter 决定读成 base64 还是直接传路径。
- **保序是加分项而不是基线**：只有 claude、dsh 能真正交错。契约必须声明三档能力，不能让调用方假设所有 provider 都保序。

---

## 4. 差距分析

| 层 | 现状 | 需要改成 |
| --- | --- | --- |
| `RunAgentRequest` | `prompt string`（field 3） | 追加 `repeated AgentInputPart input = 18`；`prompt` 非空时前置为首个 text part（合并，不互斥） |
| `AttachHumanMessage` | `text string`（field 1） | 后续 PR：追加 `repeated AgentInputPart input = 3` |
| Go SDK | `Send(ctx, text)` | 保留 `Send`，新增 `SendMessage(ctx, Message{Parts})`（不改既有签名） |
| guest 文件写入 | 写 `prompt.txt` | 写 `content.json`（有序 parts + 附件元数据）；附件字节写入 daemon 拥有的 `state/agents/attachments/<run-id>/` |
| guest 运行时命令行 | `prompt --message-file` | 追加 `--content-file <path>`；旧 guest 不支持时由 manifest 门禁拒绝 |
| guest 交互协议 | `human_message.message: string` | 后续 PR：帧内追加 `content` |
| runner 接口 | `runPrompt(promptText: string)` | `runPrompt(turn: TurnContent)`，`TurnContent.parts: Part[]` |
| codex adapter | `runStreamed(promptText)` | `runStreamed(Input[])`，媒体 → `{type:"local_image", path}` |
| claude adapter | `query({prompt: string})` | `query({prompt: AsyncIterable<SDKUserMessage>})`，媒体 → base64 image/document block |
| opencode adapter | 只拼 prompt | 追加 `-f <guest path>` |
| pi adapter | 只拼 prompt | 追加 `@<guest path>` 位置参数，或切 RPC |
| dsh adapter + profile | text-only followup | 附件清单 → `ctx.attachments.saveImages/saveFile` → content blocks |
| capmatrix | 4 个 feature | 追加一个粗粒度门禁 `media_input`，并暴露 `media_types` / `max_attachment_bytes` / `media_ordering`（定稿见 vNext 文档） |
| guest ABI manifest | provider `features[]` | guest 镜像声明 `media_input`，缺失即 false（现有语义天然支持，同时充当版本门禁） |
| 运行历史 | 只存 prompt 文本 | 存附件描述（mime/name/size/sha256），字节的去留见 §8 |

---

## 5. 接口提案

### 5.1 设计原则

1. **有序 part 数组**，与 OpenAI/Anthropic/Gemini/MCP/A2A 同构；文本也是一个 part。
2. **part 类型封闭、MIME 开放**：`text` / `media` 两种 part 足以表达今天全部能力；媒体种类由 `media_type` 表达，新增音频/视频不需要新 part 类型。
3. **三种 source 只实现两种**：`bytes`（内联）+ `workspace_path`（沙箱内）。`url` 明确留空并说明为什么（§5.4）。
4. **不定义没人能消费的语义**：v1 不出现 `audio`/`video` 字段或枚举，避免"定义了却永远 Unimplemented"。
5. **不引入第二套文件身份**：沙箱 workspace 路径就是 file handle，复用 `sandbox_workspace.proto` 的路径语法与校验。
6. **失败显式**：不支持的媒体类型、超限、provider 不支持，都在 run 开始前失败，绝不静默丢弃（codex 的占位文本软失败要在准入阶段规避）。
7. **与内部 `protocolbridge.FilePart` 同构**，让 part 从控制面到 LLM 请求只做一次映射。

### 5.2 proto 定义

```proto
// InputPart 是用户一轮输入中的有序片段。顺序即语义：调用方按发送顺序排列，
// 文本与媒体可以交错。支持交错的 provider（claude、dsh）保持原序；其余
// provider 把本轮全部媒体附加到同一条用户消息上，文本按顺序拼接（见 §5.3）。
message InputPart {
  oneof part {
    // 纯文本。空字符串非法。
    string text = 1;
    // 非文本媒体。至少需要一个 source。
    InputMedia media = 2;
  }
}

message InputMedia {
  // 必填。规范化的 IANA 媒体类型（小写 "type/subtype"，不带参数），
  // 例如 "image/png"、"application/pdf"。服务端按 provider 白名单校验，
  // 并用魔数嗅探复核；声明与内容不符视为 InvalidArgument。
  string media_type = 1;
  // 可选展示名，仅用于 UI 与日志，不参与路径解析。
  string filename = 2;
  oneof source {
    InputMediaBytes bytes = 3;
    InputMediaWorkspacePath workspace_path = 4;
  }
}

message InputMediaBytes {
  // 原始字节。Connect/JSON 传输时按 base64 编码。
  bytes data = 1;
  // 可选。提供时服务端校验摘要，不匹配返回 InvalidArgument。
  string sha256 = 2;
}

message InputMediaWorkspacePath {
  // workspace 相对路径，语法与 SandboxWorkspaceEntry.path 相同：
  // 斜杠分隔、非绝对、不逃逸根。指向的文件必须在 run 开始时可读。
  string path = 1;
  // 可选。提供时在解析后校验摘要（缓解 guest 并发改写导致的 TOCTOU）。
  string sha256 = 2;
}
```

字段落点：

```proto
message RunAgentRequest {
  // ... 既有字段 1..17 不变 ...
  // 有序多模态输入。prompt 非空时前置为首个 text part；两者可同时使用。
  repeated AgentInputPart input = 18;
}

// AttachHumanMessage.input 与 RunDetail.input_attachments 留给后续 PR。
```

> 为什么保留 `prompt` 而不是直接换掉：`RunAgentRequest` 是全仓最热的请求类型，CLI、Web、scheduler、SDK 全部在构造它。保留 `prompt` 作为"首个 text part 的语法糖"可以让旧调用方零改动；新调用方加媒体时把 `prompt` 前置即可，不需要把已有文本搬进 `input`，也不会出现"两个来源谁优先"的歧义——规则固定为 prompt 在前。

### 5.3 语义规则

**R1 顺序与降级。** `content` 数组顺序是权威顺序。provider 分为三档，必须在能力矩阵中如实暴露：

| 档位 | provider | 行为 |
| --- | --- | --- |
| 保序 | claude、dsh | part 顺序原样进入模型请求 |
| 媒体聚合 | codex、opencode、pi(CLI) | 文本 part 按序拼接（`\n\n`），全部媒体作为同一轮附件；媒体之间相对顺序保留 |
| 不支持 | 旧 guest runtime | 在 run 开始前 `Unimplemented` |

**R2 prompt 与 input 合并，不互斥。** `input` 为空 → 完全等价于 `prompt`（旧路径）。`input` 非空且 `prompt` 非空 → `prompt` 作为首个 text part 前置，并产生一条 warning。`input` 非空且 `prompt` 为空 → 直接用 `input`。

**R3 source 解析。**

- `bytes`：daemon 校验后写入 guest 的 `state/agents/attachments/<run-id>/turn-<n>/part-<i>`（通过既有 guest file writer，Docker/BoxLite/K8s 同一抽象），再以该绝对路径交给 runner。按 turn 分目录，因为交互会话的同一个 run 会追加多轮。
- `workspace_path`：不做拷贝，直接解析为 guest 绝对路径后交给 runner。路径在 turn 开始时解析一次；guest 在 turn 中途改写文件属于同一信任域内的行为（agent 本来就有 workspace 写权限），不作为安全边界。
- 同一 part 两个 source 同时设置 → `InvalidArgument`（oneof 在 wire 上已不可能，但 JSON 解码后仍要校验）。

**R4 provider 白名单在准入阶段强制。** 校验顺序：part 形状 → MIME 语法 → 魔数与声明一致 → provider 支持该 MIME → 大小/数量上限。任一失败都在 run 创建前返回，不产生半初始化的 run。

**R5 附件不进入 workspace。** 内联字节落在 `state/agents/attachments/`，不写 workspace：避免污染 agent 的 diff、避免被 `DownloadSandboxWorkspaceFile` 意外暴露、并在 sandbox 回收时一并清理。agent 仍然可读（同一 sandbox 用户），只是不把它当作项目文件。

**R6 幂等。** `client_request_id` 的语义不变：同一 key 解析到同一个 run。附件字节不参与 key；用同一 key 重试时以首次请求为准。

**R7 多轮。** 每个 human message 是独立的一轮，附件在该轮开始时落盘；同一 sandbox 的后续轮次不自动继承上一轮附件（与文本一致——上下文由 provider 自己维护）。

**R8 失败可见。** provider 在运行期拒绝媒体（例如模型不支持图片）时，run 以失败结束并保留 provider 错误信息；不做"降级为纯文本继续跑"的静默兜底。

### 5.4 URL 载体为什么推迟

三个理由，缺一不可：

1. **provider 接受度不均**：codex CLI 只吃本地路径；opencode `-f` 也按本地文件处理；pi 只吃 `@path`。只有 claude（image source url）和 OpenAI 系 API 原生吃 URL。
2. **安全边界**：让 daemon 或 guest 去抓调用方给的 URL，会把 SSRF、内网探测、DNS rebinding 引到控制面，而沙箱本来有出口策略（egress policy）。做这件事必须先设计"URL 抓取策略"，不能顺手加。
3. **业界也没有统一**：A2A 的 `FileWithUri` 假设对端可抓取；MCP 用 `resource_link` 把解析责任推给客户端；OpenAI 的 `file_id` 和 URL 是两条独立产品线。

因此 schema 里不出现 `url` source。等有明确调用场景（例如"图片已经在对象存储、且部署方接受 egress 策略"）再以新增 oneof case 的方式加入——这是纯增量，不破坏 v1。

### 5.5 校验与错误

| 情况 | code | reason | 说明 |
| --- | --- | --- | --- |
| `prompt` 与 `input` 同时非空 | 正常 | — | 合并：`prompt` 前置为首个 text part，`warnings` 说明 |
| part 既无 text 也无 media，或 text 为空 | `InvalidArgument` | `INVALID_REQUEST` | 指明 part 下标 |
| `media_type` 缺失/非法 | `InvalidArgument` | `INVALID_REQUEST` | 要求 `type/subtype` 小写 |
| 声明 MIME 与魔数不符 | `InvalidArgument` | `INVALID_REQUEST` | 防伪造/多态文件 |
| MIME 不在该 provider 白名单 | `InvalidArgument` | `INVALID_REQUEST` | detail 带 `supported_media_types`；这是"该请求对该 provider 非法"，不是"能力缺失" |
| 单 part / 单轮超限 | `InvalidArgument` | `INVALID_REQUEST` | detail 带上限与实际值 |
| `workspace_path` 非法或不存在 | `InvalidArgument` / `NotFound` | `INVALID_REQUEST` / `RESOURCE_NOT_FOUND` | 复用 workspace 下载的路径校验 |
| `sha256` 不匹配 | `InvalidArgument` | `INVALID_REQUEST` | |
| provider 完全不支持媒体 | `Unimplemented` | `UNSUPPORTED` | 例如全文本模型路径 |
| guest runtime 过旧 | `Unimplemented` | `UNSUPPORTED` | detail 提示升级 guest 镜像 |

v1 复用现有 reason（`INVALID_REQUEST` / `UNSUPPORTED`），不新增枚举值——`errors.proto` 的 reason 集合是跨端契约，新增值要同步改 CLI/UI 的映射与契约测试；等确有机器可判别的需求（例如 UI 需要"文件太大"单独提示）再加 `INPUT_ATTACHMENT_TOO_LARGE` 之类的值。

### 5.6 限制（建议默认值）

| 项 | 默认 | 依据 |
| --- | --- | --- |
| 单 part 字节 | 10 MiB | opencode 的硬上限量级；高于 Anthropic 5MB/图，claude CLI 会 recompress，但上游仍可能拒绝——那种失败按 R8 呈现，不在准入阶段假装成功 |
| 单轮总字节 | 32 MiB | base64 后约 43 MiB，仍在现有 HTTP 请求可接受范围 |
| 单轮 part 数 | 20 | 与 DSH 的 20 图/消息对齐 |
| 允许的图片 MIME | `image/png`、`image/jpeg`、`image/gif`、`image/webp` | 五个 provider 的交集；显式排除 SVG（主动内容）与 BMP/TIFF/HEIC |
| 允许的文档 MIME | `application/pdf`（仅 claude、opencode） | pi/dsh/codex 无原生文档模态 |

限制值应集中在一处配置（而非散落在 handler 与 runner），并允许部署方通过环境变量收紧；放宽上限属于兼容性变更，需要走契约评审。

### 5.7 能力协商

复用现有机制，不新造一套：

1. `pkg/capmatrix` 的 `canonicalFeatures` 追加一个粗粒度 key：`media_input`。它只表达"该 guest runtime 能接收 `--content-file` 并处理附件"，用作**版本门禁**，不表达具体媒体种类。
2. 具体媒体种类由 `EngineProviderCapabilities.media_types` 表达（daemon 静态表）：codex/pi/dsh 为四种位图，claude/opencode 另加 `application/pdf`。不用 `image_input`/`document_input` 布尔值，避免与 `media_types` 形成两套会漂移的事实。
3. guest ABI manifest 的 `providers[].features[]` 增加 `media_input`。`DeclaredProvidersFromManifest` 对未知 feature 忽略、缺失即 false 的既有语义，天然形成"旧 guest 镜像 → false → 拒绝多模态请求"的门禁；错误信息里要明确提示升级 guest 镜像。

> 本节的能力字段已按 [`multimodal-input-proto-vnext.zh-CN.md`](multimodal-input-proto-vnext.zh-CN.md) §6 定稿；proto 全文以该文档为准。
4. `EngineProviderCapabilities` 追加：

```proto
message EngineProviderCapabilities {
  // ... 1..3 不变 ...
  // 该 provider 接受的媒体类型全集，例如 ["image/png","image/jpeg","application/pdf"]。
  repeated string media_types = 4;
  // 单个 part 的字节上限；0 表示不限制（当前不会出现）。
  uint64 max_attachment_bytes = 5;
  // part 顺序语义。UNSPECIFIED 按 AGGREGATE 处理，保证新 provider 不会让
  // 旧调用方误以为保序。
  EngineProviderMediaOrdering media_ordering = 6;
}
```

5. 顺序能力必须用独立字段表达，不能塞进 bool feature：调用方需要区分"能发图"和"能交错"，而 `EngineProviderFeature{supported bool}` 无法承载三档语义。

---

## 6. 各 provider 适配方案

### 6.1 codex

```ts
// before
await thread.runStreamed(promptText, turnOptions)
// after
await thread.runStreamed([
  { type: "text", text: "这两张图有什么区别？" },
  { type: "local_image", path: "/…/state/agents/attachments/<run>/part-0" },
], turnOptions)
```

- 媒体先物化为 guest 路径（`bytes` 由 daemon 写盘；`workspace_path` 直接解析）。
- 非图片 MIME 在准入阶段拒绝（codex 只有 image 变体）。
- 保留 `resumeThread`：`-i` 在 resume/fork 上同样可用。
- 注意软失败：codex 对不支持的图片会用占位文本继续，所以 MIME 白名单必须前置。

### 6.2 claude

```ts
async function* turn(parts: Part[]): AsyncIterable<SDKUserMessage> {
  yield {
    type: "user",
    parent_tool_use_id: null,
    message: {
      role: "user",
      content: parts.map(toAnthropicBlock), // text | image(base64) | document(base64 pdf)
    },
  }
}
const stream = claudeQuery({ prompt: turn(parts), options: this.queryOptions(stored) })
```

- 必须从字符串 prompt 切到异步迭代；这是唯一一个"不改调用形态就做不到"的 provider。
- 图片需要 runner 读文件并 base64；PDF 走 `document` 的 base64 source。
- 这是保序 provider，part 顺序原样映射。

### 6.3 opencode

```ts
args.push(...parts.filter(p => p.media).flatMap(p => ["-f", p.guestPath]))
```

- 路径直接交给 `-f`；opencode 内部转 data URL 并做 10 MiB / MIME 白名单校验。
- 非保序：所有文件附加到同一条消息。
- 模型没有 `attachment` 能力时由 opencode 自己报错；建议 runner 把该错误透传到 run 事件。

### 6.4 pi

- 方案 A（最小改动）：在 prompt 位置参数之前插入 `@<guest path>`，图片由 pi 自己读成 base64 附件。注意非图片会变成文本内联，语义与"原生文档附件"不同，需要在文档里写明。
- 方案 B（推荐中期）：切到 `--mode rpc`，发 `{"type":"prompt","message":..., "images":[...]}`，避免 CLI 参数长度的隐式限制，并为将来的 streaming steer 打基础。
- pi 的 2000px 自动缩放可以缓解大图问题，但**不能**替代准入校验（10 MiB 仍是我们的上限）。

### 6.5 dsh

```js
const refs = await ctx.attachments.saveImages(images.map(i => ({
  data: await fs.readFile(i.path), mediaType: i.mediaType, name: i.name,
})))
const fileRef = await ctx.attachments.saveFile({ /* … */ })
agent.followup(createUserMessage({ content: [...textBlocks, ...imageBlocks, fileBlock] }))
```

- 附件清单通过新增 `DSH_ATTACHMENTS_FILE`（JSON）传给 profile；保持"所有 per-run 可变值走 spawn 环境 / 受控文件"的既有设计（见 [`dsh_agent_provider_design.md`](dsh_agent_provider_design.md) §3.2）。
- 图片仅 4 种位图；`file` block 没有原生模态，模型侧是路径文本——因此 `media_types` 只列四种位图，不含 `application/pdf`。
- 模型路由未声明 image 输入时，DSH 直接拒绝（`MODEL_DOES_NOT_SUPPORT_IMAGES`），应在错误里透传。

---

## 7. 分阶段实施

### P0 契约定稿（无行为变更）

- 本文件评审通过；`proto` 增加 `InputPart` / `InputMedia` 与两个字段；`task generate`；补 field-layout / closed-set 契约测试。
- `capmatrix` 增加 `media_input` 门禁与 `media_types` / `max_attachment_bytes` / `media_ordering`；更新 [`engine_capability_matrix.md`](engine_capability_matrix.md)。
- 验收：`task lint`、`task test`；旧客户端行为不变（`prompt` 路径回归）。

### P1 单轮图片（codex / claude / opencode / pi）

- guest runtime 增加 `--content-file`；runner 接口改为 `TurnContent`；四个 adapter 按 §6 改造。
- daemon 写附件到 `state/agents/attachments/<run-id>/turn-<n>/`；manifest 声明 `media_input`；旧 guest 返回 `Unimplemented`。
- 验收：
  - 每个 provider 一条 e2e：发送 1 张 PNG + 文本，模型侧确实"看到"图（用可判别的图片内容断言，例如纯色块问答）；
  - `prompt` + `input` 合并、超限、MIME 不支持、非法路径、旧 guest 各一条失败用例；
  - 同一 `client_request_id` 重试不产生第二个 run。

### P2 交互 attach（后续 PR）

- `stream` 协议 `human_message` 帧增加 `content`；`AttachAgentRun` 透传；内联字节在 attach 流里物化。
- dsh 的 `DSH_ATTACHMENTS_FILE` 与 profile 属于 P1（本 PR 已含）。
- 验收：交互会话中先文本后图片两轮，第二轮图片生效；resume 后仍可追加图片。

### P3 历史与可见性

- `RunDetail` 暴露附件描述（不含字节）；UI 能显示"用户发送了 2 张图"；按需增加 `DownloadRunAttachment` 或在 run artifacts 目录保留字节并提供下载。
- 验收：重启 daemon 后历史可读；sandbox 已回收时不报 500。

### P4 增量能力（按需）

- URL source（先解决 egress 策略与抓取白名单）；
- 输出侧媒体（agent 产出的图片作为事件/附件回流）；
- daemon 侧转码/缩放；
- 音频/视频（等某个 provider 真正把音频当模型输入）。

---

## 8. 风险与待决问题

### 风险

1. **guest 版本偏斜**：daemon 与 guest runtime 不同步会导致新参数被旧 runtime 拒绝或忽略。必须走 manifest feature 门禁，且在错误信息里明确"升级 guest 镜像"。
2. **模型级不支持**：`pkg/llms` 的 model catalog 没有 modality 元数据，v1 只能在 provider 侧失败。建议 P3 在 catalog/探测里补 `supports_images`，把失败提前到 run 创建前。
3. **包体过大**：32 MiB 上限 + base64 膨胀会让 Connect 请求变大；如果部署在反向代理后面，需确认 body 限制。中期用"上传 RPC + workspace 路径"替代大块内联。
4. **保序降级引发预期差**：调用方可能假设"图文交错"在所有 provider 上都成立。契约、能力矩阵、文档三处都要写明；测试要覆盖聚合模式的拼接顺序。
5. **主动内容**：SVG/HTML 可能携带脚本，PDF 有解析漏洞面。只允许位图 + PDF，并做魔数校验；不做"相信 MIME 声明"。
6. **隐私**：附件字节是否长期保留需要产品决策（见下）。

### 待决问题

| # | 问题 | 建议 |
| --- | --- | --- |
| Q1 | 附件字节是否进入 run 历史/审计？ | 默认不持久化字节，只存描述；需要重放时由调用方重新上传 |
| Q2 | 附件是否应放进 workspace 让 agent 当普通文件用？ | 不。放 state 目录，避免污染 diff 与下载面 |
| Q3 | 是否需要独立的 `UploadAgentInput` RPC？ | 需要，但放在 P3；先用内联字节 + workspace 路径覆盖 |
| Q4 | 是否把 pi 切到 RPC 模式？ | 中期切，CLI `@path` 作为过渡 |
| Q5 | 是否统一用 `protocolbridge.FilePart` 作为唯一内部表示？ | 是。控制面 part → bridge `FilePart` 一次映射，避免三套模型 |
| Q6 | 音频/视频的产品需求是否存在？ | 当前没有 provider 支持，保持 schema 可扩展、语义不定义 |

---

## 9. 附录：证据

### 本地实测

```text
codex-cli 0.162.0
  codex exec --help        → -i, --image <FILE>...
  codex exec resume --help → -i, --image <FILE>
  codex exec fork --help   → -i, --image <FILE>
  @openai/codex-sdk d.ts   → UserInput = {type:"text"} | {type:"local_image", path}
  runtime: codex.ts:101 runStreamed(promptText)

claude 2.1.220
  claude --help            → --input-format "text"|"stream-json"（仅 --print）
  @anthropic-ai/claude-agent-sdk d.ts → query(prompt: string | AsyncIterable<SDKUserMessage>)
                                        SDKUserMessage.message = Anthropic MessageParam
  runtime: claude.ts:373 query({prompt: promptText})

opencode 1.18.14
  opencode run --help      → -f, --file  file(s) to attach to message [array]
  @opencode-ai/sdk types   → FilePartInput{type:"file", mime, filename?, url}
                             Model.capabilities.attachment: boolean
  编译产物                  → url 为 data:<mime>;base64,...；白名单 pdf/png/jpeg/gif/webp；10 MiB 常量

pi 0.84.2
  pi --help                → pi [options] [@files...] [messages...]；--mode text|json|rpc
  dist/cli/file-processor.js → 图片 → {type:"image", mimeType, data}；文本 → <file> 块
  docs/rpc.md              → prompt 命令支持 images:[{type:"image",data,mimeType}]
  docs/settings.md:184     → images.autoResize，2000×2000

dsh（本地 checkout）
  assets/.dsh/profiles/agent-compose/runner.js:146 → followup(createUserMessage({content:[{type:'text'}]}))
  packages/attachment/attachment/src/index.ts:116  → saveImages(...)
  packages/attachment/attachment/src/index.ts:216  → saveFile(...)
  packages/llm/llm/src/types.ts:86-109             → ImageBlock/FileBlock（attachment ref）
```

### 仓库内相关位置

- proto：`proto/agentcompose/v2/agentcompose.proto`（`RunAgentRequest` 1292、`AttachHumanMessage` 1865、`EngineProviderCapabilities` 2789）、`proto/agentcompose/v2/sandbox_workspace.proto`
- 执行链：`pkg/agentcompose/adapters/agent_runner.go:552`、`runtime/javascript/src/prompt.ts:89`、`runtime/javascript/src/stream.ts:78`
- 能力矩阵：`pkg/capmatrix/provider_matrix.go:15`、`pkg/guestabi/manifest.go:20`
- facade 透传：`pkg/llms/facade_bridge.go:11`（只改 model 与文本 part 的 type，媒体 part 原样通过）
- 既有文件 API：`docs/design/sandbox-workspace-file-api.zh-CN.md`（明确解释了为什么先只做读）

### 外部参考

- OpenAI Chat Completions content parts（Python SDK `ChatCompletionContentPartParam`）：<https://platform.openai.com/docs/api-reference/chat>
- OpenAI Responses input items（`ResponseInputItemParam`，含 `input_image` / `input_file` / `input_audio`）：<https://platform.openai.com/docs/api-reference/responses>
- Anthropic Messages content blocks（`ImageBlockParam` / `DocumentBlockParam`；image 仅 base64/url）：<https://platform.claude.com/docs/en/api/messages>
- Gemini `Part` oneof 与 Files API（`Blob{mime_type,data}` / `FileData{mime_type,file_uri}`）：<https://ai.google.dev/api/caching#Part>
- MCP 2025-06-18 schema：`TextContent` / `ImageContent` / `AudioContent` / `ResourceLink` / `EmbeddedResource`：<https://modelcontextprotocol.io/specification/2025-06-18/schema>
- A2A v1 `a2a.proto`：`Part oneof content { text, raw, url, data }`：<https://a2a-protocol.org/v1.0.0/specification/>
- DSH 附件子系统：`docs/subsystems/attachment.md`（本地 checkout）
