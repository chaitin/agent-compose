# Guest 程序使用已授权 OB 工具

## 问题与范围

AC 已经给 Sandbox 提供 CAP 连接与鉴权，也能够运行普通命令和兼容 Codex 的程序。
原有 Markdown 工具目录适合智能体阅读，但程序需要自行解析目录和组装 gRPC 调用。
本功能补充结构化发现与 Runtime SDK，复用已有配置、Scope、代理和进程协议。

任何 guest 程序均可读取结构化目录并使用标准 gRPC；Node.js 程序另有
`runtime.capabilities.list/describe/call`。程序自己决定业务目标、模型工具循环和
报告格式，AC 不注册具体业务方法。首版支持 OB unary gRPC，不支持流式方法，
不添加新的 provider、自定义执行协议、模型网关或自动 Agent loop。

## 目录与调用契约

AC 在准备能力指南时生成 `RUNTIME_ROOT/mpi/capabilities.json`：

- version 固定为 1，sandbox_id 标识此快照所属 Sandbox。
- tools 包含 id、capset、instance、method、request_type、response_type。
- id 由完整能力集声明、实例、完整方法计算；限定的 server/capset 保持原样。
- unavailable_capsets 显式列出未能发现的能力集；不静默把网关故障当成无授权。
- 不包含上游地址、凭据和任意 endpoint metadata；空授权覆盖旧结构化目录。

目录读取沿用 Markdown 指南的可信 project/user Scope。限定能力集使用项目 server，
不限定能力集仍使用全局网关。SDK 拒绝坏目录、不匹配 Sandbox、重复身份和不完整快照。
目录不是实时查询或权限凭据；恢复不可用网关后重新准备运行或创建 Sandbox。

`describe()` 返回方法和顶层消息的 Protobuf 文本，同时返回 Base64 编码的标准
FileDescriptorSet，包含导入依赖。可由调用方的 Protobuf 库加载；不是 JSON Schema
或不可变的 schema 审批绑定。`call()` 使用标准 Protobuf JSON，经 grpcurl 编解码。

所有调用继续由 capproxy/OB 鉴权。guest 可修改目录或绕过 SDK，因此 SDK 不是安全边界。
本功能不从方法名推断读写性质，不替代产品的数据权限或操作审批。依赖删除流程使用
既有 SandboxAccessRevoker 撤销 CAP 缓存，避免已删除 Sandbox 的凭据继续使用。

调用不自动重试。参数走 stdin，CAP 凭据使用环境展开，不进入 argv。输入上限 256 KiB，
输出默认 256 KiB、最高 1 MiB，包含返回的二进制 descriptor；超过限制时报错。
超时选项按 grpcurl 操作生效；共享端到端期限通过 AbortSignal 表达。
临时描述文件无论成功失败均删除，上游 stderr 不附加到 SDK 错误。

## 验证

单元测试覆盖身份/Scope、同方法多实例、限定 server 路由、空授权、坏目录、流式拒绝、
超时、取消、输出大小、descriptor 清理及不回显凭据。组件集成测试通过现有 CodexRunner
和官方 Codex SDK 子进程，验证真实 capproxy 路由、未授权拒绝与凭据撤销。

独立 Docker E2E 通过公开 up/RunAgent/Exec/Sandbox API 验证：

1. 普通程序和同一份 Codex 兼容程序都能发现并调用新建 OB 工具。
2. 完整 descriptor 可加载另一个文件导入的类型。
3. 绕过 SDK 的未授权 gRPC 请求被 capproxy 拒绝。
4. 正常停止、daemon 重启与恢复后仍能调用。
5. 删除容器后，之前有效的 CAP 凭据返回 Unauthenticated。

测试 fixture 只有合成数据，使用文档保留 IP，无模型请求或外部业务依赖。
不以此用例宣称已验证其他 driver、强杀恢复、业务子进程取消或产品端到端。

## 本地 Docker E2E

先构建 daemon、guest runtime 与 Runtime SDK。创建独立本机 OB，导入
`test/fixtures/ob-ip-evidence`（先在该目录 npm ci），创建 ip-evidence-test 实例，
加入 dev capset。通过环境提供 AC_LOCAL_OB_ADMIN_TOKEN；项目 server 当前同一凭据
用于目录与调用，专用测试凭据需有这两个权限。不得复用共享或生产实例。

在最小构建上下文准备：runtime/javascript 的 package.json、package-lock.json、
dist（命名 runtime-dist），SDK 离线包 sdk.tgz、Linux grpcurl、
test/e2e/testdata/capability-program.mjs（命名 program.mjs）。使用
`test/e2e/testdata/Dockerfile.capabilities` 构建专用 guest。上下文不包含整个 checkout
或本地模型配置；BASE_IMAGE 可指定兼容的现有 guest。

设置 AC_CAPABILITIES_E2E_IMAGE、AC_LOCAL_OB_ADDR（只接受字面量回环地址）后：

```bash
AGENT_COMPOSE_E2E_BINARY="$PWD/build/agent-compose" \
  go test -race -count=1 -timeout=6m \
  -run '^TestE2EDockerRuntimeCapabilities$' -v ./test/e2e
```

此 E2E 使用 Docker Desktop 的 host.docker.internal；不代表其他 Docker 网络布局
已经验收。测试资源均为本轮独立创建，结束后清理。
