# Sandbox workspace 文件读取 RPC

## 1. 背景

页面会话的常见需求是：agent 在 sandbox workspace 里生成一个报告文件，用户在页面上把它下载下来。

agent-compose 之前没有公开的 workspace 读取 API。现有可用的替代路径都不适合跨机器：

- `SandboxService.GetSandbox` 只返回 `workspace_path`（daemon 宿主路径），ADP 与 engine 不共享卷时无法读取；
- `ExecService.Exec` 可以 `cat`/`tar`，但要求 sandbox 处于 `running`（`pkg/agentcompose/api/exec_target.go:105`），而 chat SDK 每轮结束使用 `STOP_ON_COMPLETION`（`sdk/go/chat/conversation.go:458`），所以回合结束后直接 Exec 会失败；
- K8s 停止会删除 Pod（`pkg/driver/k8s_runtime.go:793`），agent 写在 Pod 文件系统里的文件随之消失；
- 让 agent 主动上传需要 token、网络和 agent 配合，不是稳定契约。

因此需要两个只读 RPC：列出一层目录、流式下载一个文件。它们只提供文件访问，不引入 deliverables、artifact 或面板语义；这些属于业务层。

## 2. 定位

这是**控制面 Connect RPC，携带数据面载荷**：

- 调用方是可信的控制面客户端（例如 ADP server），使用 daemon token；
- 进入现有 descriptor、错误契约和 RPC inventory；
- 不扩展 HTTP allowlist，不参与浏览器直连；
- 浏览器下载仍由业务层自己的 HTTP 端点代理。

## 3. 接口

```protobuf
rpc ListSandboxWorkspace(ListSandboxWorkspaceRequest) returns (ListSandboxWorkspaceResponse);
rpc DownloadSandboxWorkspaceFile(DownloadSandboxWorkspaceFileRequest)
    returns (stream DownloadSandboxWorkspaceFileResponse);
```

定义在 `proto/agentcompose/v2/sandbox_workspace.proto`，RPC 挂在 `SandboxService`。

### List

- `path` 是 workspace 相对路径；空字符串或 `.` 表示根；
- 只列一层，不递归；
- `offset`/`limit` 与其他 List RPC 一致；`limit` 默认 100，最大 500；
- 返回 `total`，按名字排序当前页；
- 单目录可能很大。实现先计数，同时只保留最小的 `offset+limit` 个名字；内存随请求的页深增长，不随目录大小增长，因此没有额外的页深上限；
- 目录在分页之间变化是 best-effort。

### Download

- `path` 必须是 workspace 相对路径的普通文件；
- 第一帧是 `info`，之后是 64 KiB 的 `chunk`；空文件只有 `info`；
- 流式读取，不落临时文件，不把整个文件放进内存；
- 取消请求时停止读取并关闭文件。

## 4. 语义

### 路径

- 根固定为 sandbox 自己的 `<sandbox>/workspace`；
- 不开放 home、state、volume，不返回宿主路径；
- 拒绝绝对路径、`..`、NUL、超长路径；反斜杠不作为路径分隔符，在 Unix 上是合法文件名字符，列表按原样返回；
- 符号链接在列表中报告为 `SYMLINK`，不跟随；下载和进入目录都拒绝任何路径分量上的符号链接；
- 非 UTF-8 文件名不会出现在响应里，也不计入 `total`，避免破坏 protobuf string。

### 生命周期

- workspace 已回收或正在回收：`FailedPrecondition`；
- workspace provisioning 未 ready：`FailedPrecondition`；
- Docker/BoxLite/Microsandbox 停止后宿主 workspace 还在，可以读；
- K8s 第一版返回 `Unimplemented`，因为 workspace 在 Pod 内，读取需要额外的 streaming guest 能力；
- `mode: mount` 第一版返回 `Unimplemented`，不猜测挂载源映射。

### 错误

| 情况 | code | reason |
| --- | --- | --- |
| sandbox 不存在 | `NotFound` | `SANDBOX_NOT_FOUND` |
| 请求的 path 不存在 | `NotFound` | `RESOURCE_NOT_FOUND` |
| path 非法 | `InvalidArgument` | `INVALID_REQUEST` |
| workspace 已回收/未 ready | `FailedPrecondition` | `PRECONDITION_FAILED` |
| mount 模式、K8s 或未知 workspace 类型 | `Unimplemented` | `UNSUPPORTED` |
| workspace 权限不足 | `FailedPrecondition` | `PRECONDITION_FAILED` |
| 其他文件系统读取失败 | `Internal` | `INTERNAL` |
| 取消/超时 | `Canceled`/`DeadlineExceeded` | `OPERATION_CANCELED`/`OPERATION_TIMEOUT` |

## 5. 实现

- `pkg/workspaces/sandbox_files.go`：`SandboxFiles` 接口和传输中立的 entry/info 类型；
- `pkg/agentcompose/adapters/workspace_files.go`：共享文件系统实现，使用 `os.Root`；不启动 runtime；
- `pkg/agentcompose/api/sandbox_workspace.go`：参数校验、分页、流组装、错误映射；
- `pkg/agentcompose/api/sandbox.go`：`WithWorkspaceFiles` 依赖注入；
- `pkg/agentcompose/app/app.go`：构造 `WorkspaceFiles`。

分页实现使用一个按名字排序的有界 max-heap，只保留最小的 `offset+limit` 个条目，因此目录大小只影响扫描时间，不影响常驻内存；内存随请求的页深增长。

## 6. 业务层用法

ADP 不需要 artifact 存储，也不需要 chat SDK 改动：

1. 会话页请求 ADP 自己的 `ListConversationFiles`；
2. ADP 校验 session 和 `conversation.read`，解析 conversation 的 engine sandbox；
3. 调用 `ListSandboxWorkspace`；
4. 用户点文件时，ADP 调用 `DownloadSandboxWorkspaceFile`，把第一帧 `info` 和后续 `chunk` 转发给浏览器；
5. 浏览器断开时取消 engine 请求。

文件随 sandbox 生命周期存在；如果业务层需要长期保存，应由业务层显式保存到自己的存储，而不是让 engine 缓存。

## 7. 范围边界

当前决定：

- **K8s 不在 v1**：K8s 返回 `Unimplemented`，等待独立的流式 guest 读取实现；
- **ACL 不在这一层**：daemon token 是管理员边界，接口只提供管理员级只读原语；用户、项目、会话级授权由上层业务模块负责；
- **安全与运维留给上层**：内容过滤、审计、限流、下载策略不由这个 RPC 实现；上层不得把该 RPC 直接暴露给浏览器或终端用户；
- **性能与资源按流式处理**：下载已经是 `info` + 64 KiB `chunk` 的服务端流，不缓存完整文件；列表只保留 `offset+limit` 个名字，内存不随目录大小增长。列表为了满足仓库统一的 `offset/limit/total` 分页契约，仍会扫描目录计数；如果未来要求列目录本身也严格流式，需要改成 server-streaming/cursor 形状，并同时调整分页契约。

## 8. 验证

- `proto/agentcompose/v2/sandbox_workspace_contract_test.go`：RPC streaming 形状、字段编号、enum 连续性；
- `pkg/agentcompose/adapters/workspace_files_test.go`：分页、排序、路径 round-trip（空格/反斜杠文件名）、任意路径分量上的符号链接、无页深上限、权限错误分类、回收状态、K8s/mount 不支持；
- `pkg/agentcompose/api/sandbox_workspace_integration_test.go`：Connect 列表映射、info/chunk 顺序、错误 code；
- `TestDaemonV2RPCInventoryReachesConcreteHandlers`：两个新 RPC 都有具体 handler；
- `TestDaemonHTTPRouteAllowlist`：HTTP 路由集合不变。
