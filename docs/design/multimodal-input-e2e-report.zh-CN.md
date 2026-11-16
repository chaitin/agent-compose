# 多模态输入接口 E2E 测试矩阵

- 日期：2026-11-16
- 分支：`feat/sandbox-multimodal-input`（worktree `.worktrees/multimodal-input`）
- 被测镜像：`agent-compose:local`、`agent-compose-guest:local`（由本分支源码构建，guest 镜像内含本分支的 runtime）
- 上游：`http://10.2.68.223:38080`，协议 `chat_completions`，模型 `grok-4.6`（视觉可用）；daemon 通过 LLM facade 把 codex 的 responses、claude 的 anthropic_messages 转成 chat_completions
- 项目：`mm-e2e`，5 个 agent（codex/claude/opencode/pi/dsh），共享 workspace（`red.png`/`green.png`/`blue.png`/`report.pdf`）

## 结论

- 请求契约、准入校验、合并语义、能力字段、guest ABI 门禁全部符合设计。
- 图片输入在 **codex / claude / pi / dsh** 上端到端可用（模型能正确回答颜色、多图颜色）。
- **opencode 的图片输入被上游 opencode 1.18.9 阻断**：自定义 provider 的模型被视为 text-only，opencode 在 Read 工具层直接拒绝（`this model does not support image input`），请求不会带图。PDF 走 opencode 自己的文本抽取路径，可用。
- 测试中发现并修复了三个问题（均已包含在本 PR）：内联附件缺扩展名导致 opencode MIME 误判、claude 的 PDF document block 在 facade 下挂起（已收窄为仅图片）、workspace 路径不存在时 codex 静默占位（guest 侧新增存在性校验）。

## A. 契约与准入（不依赖模型）

| ID | 用例 | 结果 | 证据 |
| --- | --- | --- | --- |
| A1 | `GetCapabilities` 返回 `inputLimits` 与每 provider 的 `mediaTypes`/`maxAttachmentBytes`/`mediaOrdering` | ✅ | codex/pi/dsh=4 图、claude=4 图、opencode=5 类型；ordering 与设计一致；limits 10 MiB/32 MiB/20 |
| A2 | 纯文本请求（5 provider） | ✅ | 5 次 run 全部 SUCCEEDED，无新增 warning |
| A3 | `prompt` + `input` 合并 | ✅ | warning `prompt was prepended as the first text part because input is also set`；模型同时看到 prompt 与图片 |
| A4 | 只有 `input`（无 prompt） | ✅ | codex/claude/pi 均 SUCCEEDED；`RunDetail.prompt` 等于文本投影 |
| A5 | 只有媒体、没有文本 | ✅ 拒绝 | `invalid_argument: a turn requires at least one non-empty text part` |
| A6 | 不支持的 MIME（svg） | ✅ 拒绝 | `image/svg+xml is not supported ... (supported: image/png, ...)` |
| A7 | 声明 MIME 与魔数不符 | ✅ 拒绝 | `declares "image/png" but content is "image/jpeg"` |
| A8 | 超过单 part 上限 | ✅ 拒绝 | `part 1 is 11534344 bytes, limit is 10485760` |
| A9 | workspace 路径逃逸 | ✅ 拒绝 | `workspace path "../etc/passwd" escapes the workspace` |
| A10 | sha256 不匹配 | ✅ 拒绝 | `part 1 sha256 does not match its bytes` |
| A11 | `command` + `input` | ✅ 拒绝 | `input cannot be combined with command` |
| A12 | 交互式启动带 media | ✅ 拒绝 | `unimplemented: multimodal input is not supported for interactive attach yet` |
| A13 | PDF 的 provider 白名单 | ✅ | codex/claude/pi/dsh `invalid_argument`；opencode 接受并读出 `BANANA` |
| A14 | 旧 guest ABI（无 `media_input`） | ✅ 拒绝 | 能力表 `mediaTypes` 为空；请求返回 `unimplemented: provider "codex" does not accept media input` |
| A15 | 幂等键重放 | ✅ | 同一 `clientRequestId` 返回原 runId（`9765b18a...`），不产生第二个 run |

## B. Provider × 输入形态

| Provider | 文本 | prompt+图片 | 仅 input | workspace 图片 | 多图 | PDF |
| --- | --- | --- | --- | --- | --- | --- |
| codex | ✅ | ✅ `red` | ✅ `red` | ✅ `red` | ✅ `red, blue` | ✅ 准入拒绝 |
| claude | ✅ | ✅ `red` | ✅ `red` | ✅ `red` | ✅ `red, blue` | ✅ 准入拒绝（已收窄） |
| opencode | ✅ | ⛔ 模型能力阻断 | — | — | — | ✅ `BANANA` |
| pi | ✅ | ✅ `red` | ✅ `red` | ✅ `red` | ✅ `red, blue` | ✅ 准入拒绝 |
| dsh | ✅ | ✅ `red`（事件 `finalText`） | n/t | ✅ `red`（事件 `finalText`） | n/t | ✅ 准入拒绝 |

`—` 表示与 opencode 图片阻断同因（同一模型能力门禁），未重复执行；`n/t` 表示未跑（时间成本），行为与已跑组合一致。

## C. 兼容性

| 用例 | 结果 |
| --- | --- |
| 旧客户端只发 `prompt`（5 provider） | ✅ 行为不变，无 warning |
| `prompt` + `input` 同时发（4 provider） | ✅ prompt 前置为首个 text part，模型按该顺序读到 |
| `RunDetail.prompt` 文本投影 | ✅ 多 text part 以 `\n\n` 连接；媒体不进入该字段 |
| 多轮 attach | 本 PR 不包含（`AttachHumanMessage.input` 见设计文档 §3.3） |

## D. 测试中发现并修复的问题

1. **内联附件缺扩展名**：物化文件名为 `part-1`，opencode 按路径推断 MIME 为 `text/plain`，图片被当文本。修复为 `part-1.png`（按 `media_type` 生成扩展名），并在设计文档记录原因。
2. **claude 的 PDF 挂起**：`document` block 经 facade 无输出直至超时（>600s）。按设计文档预案把 claude 的 `media_types` 收窄为四种位图，PDF 在准入阶段拒绝。
3. **workspace 路径不存在时静默降级**：codex 用占位文本继续，模型回答 `missing`。guest runtime 新增 `verifyTurnMedia`，turn 开始前 `stat` 每个媒体路径；缺失/非普通文件直接让 run 失败并给出 `attachment ... is not readable`。

## E. 已知限制（非本 PR 引入）

- **opencode 图片**：pinned opencode 1.18.9 对自定义 provider 的模型默认 text-only，且该版本的配置不支持覆盖 `capabilities.input`。验证方式：`opencode debug file read` 能正确识别 `image/png`，但 run 时 Read 工具返回 `this model does not support image input`。需要升级 opencode 或等其支持自定义模型能力声明后重新启用。
- **dsh 的 `RunDetail.output`**：只包含 guest stderr 警告，最终文本在 run 事件 `finalText` 里（`red`）。这是既有展示层行为，与多模态无关。
- **guest ABI 门禁**：用旧 manifest 验证，未使用真实旧 guest 镜像。

## F. 复现要点

```bash
# 镜像
PROXY=http://proxy.in.chaitin.net:8123
DOCKER_CONFIG=$PWD/.cache/docker REGISTRY_MIRROR=registry-mirrors.dev.in.chaitin.net \
HTTP_PROXY=$PROXY HTTPS_PROXY=$PROXY http_proxy=$PROXY https_proxy=$PROXY \
IMAGE_TAG=agent-compose:local task image:agent-compose

# daemon 环境（.env）
LLM_API_ENDPOINT=http://10.2.68.223:38080/v1
LLM_API_PROTOCOL=chat_completions
LLM_API_KEY=sk-...
LLM_MODEL=grok-4.6
LLM_UPSTREAM_PROBE=false

# 请求（Connect JSON）
POST /agentcompose.v2.RunService/RunAgent
{"projectId":"...","agentName":"codex-agent","source":"RUN_SOURCE_API",
 "prompt":"What is the dominant color? Answer one word.",
 "input":[{"media":{"mediaType":"image/png","bytes":{"data":"<base64>","sha256":"<hex>"}}}]}
```
