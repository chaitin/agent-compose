# 可恢复、可寻址的 Agent 交互会话设计

## 目标

在不破坏现有 `AttachAgentRun` 客户端语义的前提下，支持 Web 刷新、网络重连以及 IM/Web 切换后继续同一个 Agent 交互会话。`waiting_human` 等上层业务状态不属于 agent-compose 领域模型。

## 当前边界

`RunAgent`/`StartAgentRun` 面向非交互执行；`AttachAgentRun` 的首帧携带完整 `RunAgentRequest` 并创建新 run，输入和 runtime interaction 绑定在该 RPC 生命周期内。断开连接会请求取消运行。因此现有 API 不支持按 `run_id` 恢复交互，也不能让 `StartAgentRun` 创建的普通后台 run 获得输入通道。

## 核心模型

```text
Run
 └── InteractiveSession
      ├── RuntimeInteraction
      ├── 单写者输入队列
      ├── 输出事件/续传游标
      └── 短生命周期 Attachment
```

`Run` 继续负责持久化状态和 sandbox；`InteractiveSession` 负责 runtime 交互生命周期；`Attachment` 仅负责连接收发。runtime interaction 不应由 RPC 调用栈独占。

## 兼容协议方向

保留 `AttachAgentRun` RPC。对 `AttachAgentRunStart` 增加可选字段：

- `run_id` 为空：保持现有创建新交互运行的行为；
- `run_id` 非空：连接已有 InteractiveSession，不创建新的 run/sandbox；
- `disconnect_policy` 默认保持当前断开即取消语义；新客户端可选择 detach，使 session 继续运行。

这是 protobuf 向后兼容扩展，不改变旧客户端生成代码或现有首帧语义。

## 生命周期与并发

第一阶段只允许一个 attachment 持有输入租约，多个 attachment 可订阅输出。输入复用现有 `client_frame_id` 做幂等去重，并由 session 内部单写者队列保证顺序。终态、超时、取消和空闲清理必须由 session manager 统一处理。

去重覆盖落库和执行两侧：已记录过的 human message 不会再次交给 agent。同一 `client_frame_id`、同一文本视为重发，daemon 以非终止的 `AttachError` 回应 `duplicate_message`；同一 `client_frame_id` 对应不同文本时回应 `client_frame_id_reused`，同样不执行，会话继续。两者都在 `details["client_frame_id"]` 中指明所回应的消息。prompt 模式下首帧的 `client_frame_id` 同时是首轮消息的身份，因此首轮消息的重发也能被识别；不带 `client_frame_id` 的消息按位置派生身份，始终视为新消息。

## 输出恢复

attachment 应支持从事件序号继续订阅。初期可复用现有 `ListRunEvents`/`FollowRunLogs`，后续将历史补发与实时订阅合并，避免刷新时丢事件。

## 分阶段交付

1. 引入 session manager，支持 attach/detach 和 human message；session 仅在 daemon 生命周期内有效。
2. 为 `StartAgentRun` 增加可选 interactive 启动选项，使后台提交的 run 可被 attach。
3. 持久化 session 元数据，补充事件续传、租约、多实例 ownership，并按 Docker/BoxLite/Microsandbox 声明能力差异。

## 实施拆解与依赖

任务按从底层生命周期到传输适配的顺序执行：

| 阶段 | 任务 | 依赖 | 完成定义 |
| --- | --- | --- | --- |
| A | 定义 `InteractiveSession` 状态、输入/输出端口和错误分类 | 无 | `pkg/runs` 中有纯领域类型及状态转换测试，不涉及 RPC |
| B | 实现进程内 session manager（创建、查找、attach、detach、关闭） | A | manager 可并发安全地管理 session；输入单写者、重复 frame 可验证；无资源泄漏 |
| C | 将现有 `AttachAgentRun` 的 runtime interaction 所有权迁移到 session manager | A、B | 旧 attach 行为测试全部通过；连接断开仍按旧策略取消 |
| D | 扩展 `AttachAgentRunStart` 的可选 `run_id`/断开策略字段并重新生成代码 | C | 空 `run_id` 路径与现有客户端完全一致；非空路径不会创建新 run |
| E | 接入已有 run 的输出续传和 attachment 授权 | B、D | 重连可从事件游标继续；未找到、终态、未授权、租约冲突有确定错误 |
| F | 为 `StartAgentRun` 增加可选 interactive 启动能力 | C、D、E | 后台提交的交互 run 可通过 `run_id` attach；普通 run 资源和行为不变 |
| G | driver 能力矩阵、持久化 session 元数据和 daemon 重启策略 | F | Docker/BoxLite/Microsandbox 有明确测试；不支持场景 fail closed |

阶段 A/B 是纯 domain/infrastructure 基础；C/D 是最小可用闭环；E/F 才覆盖 Web 刷新、IM/Web 接管和后台启动后的追问；G 属于生产化增强。任何阶段都不得通过新增平行 RPC 绕过 session 生命周期。

## 非目标

不新增 `SendHumanMessage` 平行 RPC；不把 `waiting_human` 加入公共 `RunStatus`；不改变现有 CLI `run -it`/`exec -it` 的断开和取消行为。

## 当前能力矩阵

| 能力 | Docker | BoxLite | Microsandbox |
| --- | --- | --- | --- |
| daemon 存活期间 detach/re-attach | 支持 | 支持 | 支持 |
| human message | wrapper stream 支持时 | wrapper stream 支持时 | wrapper stream 支持时 |
| stdin / EOF | 按 driver interaction capability | 按 driver interaction capability | 按 driver interaction capability |
| signal / resize | 按 driver interaction capability | 按 driver interaction capability | 按 driver interaction capability |
| daemon 重启后恢复 runtime interaction | 不支持 | 不支持 | 不支持 |

session manager 当前是进程内资源。daemon 重启后，持久化 run 记录可能仍为 running，但 runtime 的 stdin、输出流和进程控制句柄无法从数据库重建；按 `run_id` attach 必须返回 session not found，而不能创建新的 runtime 或假装恢复。未来只有 driver 提供稳定的 operation lookup/reattach 原语后，才可增加持久化 session ownership 和启动恢复。

历史输出恢复复用 `ListRunEvents` 和 `FollowRunLogs`；`AttachAgentRun` 的 session 输出订阅只负责连接后的实时数据。客户端应先读取持久事件/日志，再 attach 实时流，并依据事件/frame ID 去重。

实时订阅采用有界缓冲。慢订阅者耗尽缓冲时，服务端关闭该 attachment 的输出订阅，而不是静默丢帧；客户端随后按持久事件/日志恢复并重新 attach。
