# 任意文件输入接口 E2E 测试矩阵（provider-neutral 版）

- 日期：2026-11-16
- 分支：`feat/sandbox-multimodal-input`
- 镜像：`agent-compose:local`、`agent-compose-guest:local`（均由本分支源码构建）
- 上游：`http://10.2.68.223:38080`，`chat_completions`，模型 `grok-4.6`
- 项目：`mm-e2e`，5 个 agent（codex/claude/opencode/pi/dsh），共享 workspace
- 素材：`data.csv`、`bundle.zip`（含 `secret.txt=BANANA`）、`report.docx`、`red.png`

## 结论

- 接口只表达"文本 + 任意文件"；文件被写入 `<workspace>/.agent-compose/uploads/<run>/turn-0/`，路径追加到提示词。
- 与 provider 解耦：同一请求在 codex / claude 上走完全相同的落盘与提示词路径，不再有 `media_types`、`media_ordering` 或原生 content block。
- 任意类型可用：CSV、ZIP（二进制）、DOCX、PNG 都被 agent 用自己的工具成功读取。
- 文件对后续追问可见：同一 sandbox 的第二次 run 不重复上传，直接读取第一次的文件路径。
- 大小限制可配：默认 10 MiB；通过 `.env` 改为 5 字节后，能力查询与拒绝行为同步变化。

## A. 契约与准入

| ID | 用例 | 结果 | 证据 |
| --- | --- | --- | --- |
| A1 | 纯文本请求（5 provider） | ✅ | 全部 SUCCEEDED，无新增 warning |
| A2 | `prompt` + `input` 合并 | ✅ | warning `prompt was prepended as the first text part because input is also set`；文件路径追加在提示词末尾 |
| A3 | 仅 `input`（无 prompt） | ✅ | codex/claude/pi 成功；`RunDetail.prompt` 为文本投影 |
| A4 | 任意类型（zip/docx/svg 等） | ✅ 接受 | 不再有 MIME 白名单与魔数校验 |
| A5 | 单文件超默认 10 MiB | ✅ 拒绝 | `part 1 is 11534340 bytes, limit is 10485760` |
| A6 | `.env` 覆盖上限（5 字节） | ✅ | 能力查询 `maxFileBytes: "5"`；10 字节文件被拒 `limit is 5` |
| A7 | 非法 workspace 路径（`../`） | ✅ 拒绝 | `workspace path "../etc/passwd" escapes the workspace` |
| A8 | sha256 不匹配 | ✅ 拒绝 | `part 1 sha256 does not match its bytes` |
| A9 | `command` + `input` | ✅ 拒绝 | `input cannot be combined with command` |
| A10 | 交互式启动带 `input` | ✅ 拒绝 | `unimplemented: multimodal input is not supported for interactive attach yet` |
| A11 | 幂等键重放 | ✅ | 同一 `clientRequestId` 返回原 runId |

## B. 文件类型与访问方式

| 文件 | 方式 | 结果 | agent 实际动作 |
| --- | --- | --- | --- |
| `data.csv`（内联） | 落盘 + 提示词路径 | ✅ 回答 `2` | codex `cat /workspace/.agent-compose/uploads/<run>/turn-0/data.csv` |
| `data.csv`（workspace 引用） | 不拷贝 | ✅ 回答 `2` | codex `cat /workspace/data.csv` |
| `bundle.zip`（二进制） | 落盘 + 提示词路径 | ✅ 回答 `BANANA` | codex 用 `python3 zipfile` 解包读取 |
| `report.docx` | 落盘 + 提示词路径 | ✅ 读取 `word/document.xml` | codex 用 `python3 zipfile` 解包 |
| `red.png` | 落盘 + 提示词路径 | ✅ 回答 `red` | claude 用 `Read` 工具查看图片 |
| 多轮：turn 1 | 上传 `data.csv` | ✅ `2` | 首轮文件落盘并被读取 |
| 多轮：turn 2 | 不带文件，仅提示"再读一次" | ✅ `2` | 读取 turn 1 的路径 |
| 多轮：turn 3 | 中间轮新增 `second.csv` | ✅ `3` | 新文件落盘并被读取 |
| 多轮：turn 4 | 不带文件，要求读两个文件 | ✅ `2,3` | 同时访问 turn 1 与 turn 3 的文件 |

## C. Provider 解耦验证

- 提示词注入是唯一的"多模态"动作，5 个 provider 收到相同文本；没有 provider 专属参数或内容块。
- codex 读取 CSV/ZIP/DOCX，claude 读取 PNG，都通过各自工具完成。
- `GetEngineCapabilities.providers` 不再有 `media_types`/`media_ordering`；`input_limits` 是唯一输入上限来源。
- guest runtime 未改动：`agent-compose-runtime prompt --message-file` 仍是唯一入口，guest 镜像无需为本功能升级。

## C. 对话轮次矩阵（同一 sandbox）

| 轮次 | 输入 | 结果 |
| --- | --- | --- |
| 1 | `data.csv`（beta=2） | `2` |
| 2 | 无文件，引用上一轮路径 | `2` |
| 3 | 新增 `second.csv`（gamma=3） | `3` |
| 4 | 无文件，要求读两个文件 | `2,3` |

文件目录按上传轮次编号：`.agent-compose/uploads/<run-id>/turn-<n>/`。交互式 attach 的 `AttachHumanMessage.input` 复用同一物化逻辑，已有 start/human 消息的单元测试覆盖。

## D. 已知限制

- **K8s 的 workspace 路径**：daemon 读不到 Pod 内文件，`workspacePath` 不做存在性/符号链接/大小预检；内联 `bytes` 始终校验。需要严格上限时使用 `bytes`。
- **模型是否理解文件**：取决于 harness 工具与上游模型；接口只保证可访问、路径已给出。
- **续轮 `AttachHumanMessage.input`**：不在本 PR；同一 sandbox 的后续 run 已可访问既有路径。
- **`RunDetail` 不含附件描述**：仅文本投影；后续 PR 补。

## E. 复现要点

```bash
# 镜像（部署 proxy/mirror）
PROXY=http://proxy.in.chaitin.net:8123
DOCKER_CONFIG=$PWD/.cache/docker REGISTRY_MIRROR=registry-mirrors.dev.in.chaitin.net \
HTTP_PROXY=$PROXY HTTPS_PROXY=$PROXY http_proxy=$PROXY https_proxy=$PROXY \
IMAGE_TAG=agent-compose:local task image:agent-compose

# daemon .env
LLM_API_ENDPOINT=http://10.2.68.223:38080/v1
LLM_API_PROTOCOL=chat_completions
LLM_API_KEY=<key>
LLM_MODEL=grok-4.6
# 可选：
# AGENT_INPUT_FILE_LIMIT_BYTES=10485760
# AGENT_INPUT_TURN_LIMIT_BYTES=33554432
# AGENT_INPUT_FILE_LIMIT=20

# 请求（Connect JSON）
POST /agentcompose.v2.RunService/RunAgent
{"projectId":"...","agentName":"codex-agent","source":"RUN_SOURCE_API",
 "prompt":"Read the attached CSV and answer the value for beta.",
 "input":[{"file":{"filename":"data.csv","mediaType":"text/csv",
                    "bytes":{"data":"<base64>","sha256":"<hex>"}}}]}
```

提示词末尾会追加：

```
Attached files (read them with your file tools):
- /workspace/.agent-compose/uploads/<run-id>/turn-0/data.csv (text/csv)
```
