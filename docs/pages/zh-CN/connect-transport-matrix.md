# Connect 传输支持矩阵

本矩阵说明 daemon 的 Connect 控制面 RPC 所需的传输方式。“Unary”表示请求/响应 RPC，“server-stream”表示 daemon 发送多个响应，“bidi”表示 `AttachAgentRun`/`AttachExec` 一类双方都可能发送消息的交互调用。

| 传输方式 | Unary | Server-stream | Bidi attach | 部署说明 |
| --- | --- | --- | --- | --- |
| Unix socket | 支持 | 支持 | 支持 | 配置后 CLI 使用本地 socket，不经过 HTTP 代理。 |
| HTTP/1.1 | 支持 | 支持（需要流式响应） | 不支持 | 代理必须保留分块响应并逐个事件 flush；交互式 attach 需要 HTTP/2。 |
| h2c（明文 HTTP/2） | 支持 | 支持 | 支持 | 仅在可信网络或外层隧道提供传输保护时使用。 |
| TLS h2（HTTPS） | 支持 | 支持 | 支持 | 直接暴露 TCP 时推荐；若由代理终止 TLS，bidi 必须端到端保留 HTTP/2。 |

## 代理要求

- 关闭 server stream 的响应 buffering，并让数据到达时立即 flush（使用代理的 streaming/no-buffer 模式）。
- 将 idle/read timeout 设置为长于预期的 stream 或 attach 会话。CLI 的 timeout 为 0 表示等待完成，并不表示关闭代理超时。
- 保留 Connect 协议头和 HTTP/2 stream 语义。仅支持 HTTP/1 的上游无法承载 bidi attach。

请求的组合不可用时，daemon 返回标明操作的 Connect `CodeUnimplemented` 或传输错误。这与 HTTP/1 代理下 unary 调用成功不同：代理可以支持状态/列表操作，但仍会阻止 attach。

## 可恢复的 Agent 连接

当 `AttachAgentRunStart.run_id` 为空时，`AttachAgentRun` 保持现有的“创建并连接”行为。将 `disconnect_policy` 设置为 `DETACH`，可在客户端断开后继续运行该交互任务；重新建立 `AttachAgentRun` 流，并在首帧中传入服务端返回的 `run_id` 即可恢复连接。同一时刻只允许一个 attachment 发送输入。

`StartAgentRun(interactive=true)` 可直接启动相同的 detached prompt session，而无需保持提交请求。普通 `StartAgentRun` 调用仍是非交互任务。

只有 daemon 进程仍存活时才能恢复 attachment。daemon 重启后，已持久化的 run events 和 logs 仍可读取，但 runtime 输入/输出句柄无法重建，此时 attach 会明确失败。页面刷新或 IM/Web 切换后，客户端应先通过 `ListRunEvents`/`FollowRunLogs` 恢复历史，再订阅 attachment 实时输出。

## 预先上传的 Run 输入文件

`UploadRunInput` 是 client-stream RPC：客户端发送多帧，流结束后 daemon 回复；如果项目内已有相同内容，daemon 在收到 `info` 后立即回复。矩阵中的所有传输方式（包括 HTTP/1.1）都可以使用，因为生成的客户端 `CloseAndReceive` 会先关闭发送方向再读取响应。客户端在收到去重响应后如果继续发送 chunk，`Send` 可能失败，此时应停止发送并读取响应。代理必须允许与上传文件同样大小的请求体，且不能超出自身限制地缓冲它。

文件过大、不适合放进 `AgentInputFile.bytes` 内联发送时使用：

1. 首帧发送 `info`，包含 `project_id`、`size_bytes` 和小写十六进制 `sha256`；之后用 `chunk` 帧按顺序发送内容，每帧不超过 `EngineInputLimits.max_upload_chunk_bytes`。响应返回不透明的 `upload_id` 和 `expires_at`。如果该项目已有相同内容，daemon 在收到 `info` 后立即回复并设置 `deduplicated`，客户端应停止发送并读取响应。
2. 在 `RunAgentRequest.input` 或 `AttachHumanMessage.input` 中用 `AgentInputFile{filename, upload{upload_id}}` 引用该文件。daemon 把它复制到 sandbox workspace 的 `.agent-compose/uploads/<run>/turn-<n>/` 下，并在提示词中写明路径，与内联字节的处理完全一致。

限制来自 `EngineInputLimits`：单文件 `max_upload_file_bytes`、单轮 `max_upload_turn_bytes`，与内联限制分开计算。值为 0 表示该 daemon 不支持 upload 来源，K8s 上报告为 0。上传对象在 `AGENT_INPUT_UPLOAD_TTL`（默认 15 分钟）后过期；再次上传相同内容或被 Run 引用都会延长有效期。

未写入任何内容的失败带 `effect = NOT_APPLIED`：流格式错误、大小或摘要不符、单帧超过 `max_upload_chunk_bytes` 返回 `INVALID_ARGUMENT`；单帧超过 daemon 的消息大小上限、暂存区 / 项目份额 / 文件系统空间不足返回 `RESOURCE_EXHAUSTED`；项目不存在返回 `NOT_FOUND`。Run 的首轮引用了不存在、已过期或属于其他项目的上传时，在创建 Run 之前以 `NOT_FOUND`、`NOT_APPLIED` 失败，客户端可以重新上传后用同一个 `client_request_id` 重试。续轮中出现这样的引用属于非法输入，与其他非法的 `AttachHumanMessage.input` 一样会取消该交互 Run。
