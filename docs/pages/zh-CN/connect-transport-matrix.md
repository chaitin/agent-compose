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
