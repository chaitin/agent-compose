# 集成 agent-compose daemon

daemon 是 project、revision、Agent、scheduler、run、sandbox、event、artifact、
image、cache 和 volume 的控制面。外部客户端应使用生成的 Connect RPC client；
CLI 和 Web UI 也使用同一边界。

## 传输和认证

TCP 部署通过 HTTP 提供 Connect RPC，配置 base URL，并发送：

```text
Authorization: Bearer <token>
Content-Type: application/json
```

Unix socket 适合本地客户端。HTTPS 和反向代理终止属于部署边界。不要把 bearer
token 放在 URL、提交到源码仓库的 compose 文件、prompt 或日志中。

Connect procedure path 由 protobuf service 和 method 名称生成，例如：

```text
/agentcompose.v2.ProjectService/ListProjects
```

应使用生成的类型和 client，不要手写 procedure path。

## 资源生命周期

典型 project 流程如下：

1. 调用 `ProjectService.ValidateProject` 校验 compose。
2. 调用 `ProjectService.ApplyProject` 应用配置并创建 revision。
3. 调用 `RunService.RunAgent` 或 `StartAgentRun` 启动运行。
4. 用 `ListRunEvents` 读取事件，或用 `FollowRunLogs`/`StreamAgentRun` 跟随输出。
5. 需要取消时调用 `RunService.StopRun`。

资源 ID 是有作用域的标识，不等于展示名称。命令支持多种资源类型时，应通过
`ResourceService.ResolveID` 解析用户输入。

## Streaming 和取消

Unary 方法返回一个响应；server-streaming 方法持续发送事件或输出；类似
`AttachAgentRun` 的双向流会保持交互连接，必须支持 context 取消。

始终设置有上限的 context deadline，传递取消信号，关闭 transport，并处理终态。
恢复交互运行时要使用服务端返回的 run ID，避免误创建第二个 run。

## 错误和能力边界

当具体 runtime 能力未编译或不可用时，RPC 可能返回 `CodeUnimplemented`。这与生成的
`Unimplemented...ServiceHandler` 不同，后者表示路由或 handler 缺失。客户端应报告
操作、资源和服务端错误，但不能泄露凭据。

控制面与 workspace 文件传输、Jupyter proxy、webhook ingress、runtime LLM facade
等 data-plane route 分离。不要用这些 route 管理 project 或 run。

CLI 到 RPC 的权威映射和允许的 HTTP 例外见
[Connect 传输支持矩阵](connect-transport-matrix.html)。
