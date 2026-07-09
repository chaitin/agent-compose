# OctoBus 接入实现规范

OctoBus 仓库：[chaitin/OctoBus](https://github.com/chaitin/OctoBus)。

agent-compose 接入 OctoBus 已发布的能力集，把选中的能力集注入工作 sandbox 与自动化任务，并为 guest 提供能力调用入口。agent-compose 是唯一集成边界：前端和 guest 都不直连 OctoBus。

## 架构

两条链路：

```text
控制面（前端读，Connect/HTTP）：frontend -> agent-compose CapabilityService -> OctoBus /admin/v1/*
数据面（agent 调，gRPC）：       guest agent -> agent-compose capproxy -> OctoBus daemon gRPC
```

- 控制面只读：连接状态、能力集列表、能力目录。
- 数据面只暴露 gRPC：guest 经 agent-compose 透明代理调用能力，MCP / REST 不对 guest 暴露。
- 控制面 provider 与数据面 capproxy 都从 `ConfigStore` 动态读取 OctoBus 连接配置。

UI 只展示产品概念：

| OctoBus 概念 | agent-compose UI 名称 |
| --- | --- |
| capset | 能力集 |
| method | 可调用能力 |
| service | 接入源 |
| instance | 连接实例 |

## 配置

### OctoBus 连接（页面配置，存 DB，运行时动态读取）

- 表 `capability_gateway`，单行：`addr`、`token`（secret）。
- `ConfigService` 提供 `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig`，`token` 读回时脱敏。
- `addr` 非空即启用。

```proto
message CapabilityGatewayConfig {   // 读响应，绝不回传 token
  string addr = 1;
  bool token_set = 2;               // 是否已设置 token
}

message UpdateCapabilityGatewayConfigRequest {
  string addr = 1;
  string token = 2;                 // 空字符串表示清空
}
```

后端访问 OctoBus 时，`token` 存在则注入 `Authorization: Bearer <token>`。`token` 只在服务端，不进入前端明文响应、sandbox metadata、guest 环境与日志。

### 数据面代理入口（部署固定，启动时绑定一次）

由部署提供（启动参数 / 默认值），不在页面配置：

- 代理 bind 地址：agent-compose 内部 gRPC 透明代理 server 的监听地址。
- guest 可达的代理地址：注入 sandbox 的 `CAP_GRPC_TARGET`，由容器 / 网络映射决定。
- 运行时 gRPC capability 调用要求 daemon 启动时同时配置 `CAP_GRPC_LISTEN`
  和 `CAP_GRPC_TARGET`。`CAP_GRPC_LISTEN` 启动本地 capability gRPC proxy；
  `CAP_GRPC_TARGET` 是注入新 sandbox 的 guest 可达地址。缺少任一项时，控制面仍可能显示
  OctoBus 已连接，但选了 capset 的 sandbox 不会获得可用的 runtime capability
  连接变量。修改这些值后需要重启 agent-compose 并新建 sandbox。

## 控制面 CapabilityService

```proto
service CapabilityService {
  rpc GetCapabilityStatus(GetCapabilityStatusRequest) returns (CapabilityStatusResponse);
  rpc ListCapabilitySets(ListCapabilitySetsRequest) returns (ListCapabilitySetsResponse);
  rpc GetCapabilityCatalog(GetCapabilityCatalogRequest) returns (GetCapabilityCatalogResponse);
}

message GetCapabilityStatusRequest {}
message ListCapabilitySetsRequest {}

message CapabilityStatusResponse {
  bool configured = 1;
  bool ok = 2;
  string status = 3;
  uint32 service_count = 4;
  string error = 5;
  bool runtime_configured = 6;
  bool proxy_listen_configured = 7;
  bool proxy_target_configured = 8;
}

message CapabilitySet {
  string id = 1;
  string name = 2;
  string description = 3;
  bool enabled = 4;
}
message ListCapabilitySetsResponse { repeated CapabilitySet capsets = 1; }

message GetCapabilityCatalogRequest { string capset_id = 1; }

message CapabilityEndpoint {
  string protocol = 1;
  string endpoint = 2;
  string method_path = 3;
  map<string, string> metadata = 4;
  string tool_name = 5;
  string procedure = 6;
  string http_method = 7;
  repeated string content_types = 8;
}
message CapabilityMethod {
  string service_id = 1;
  string instance_id = 2;
  string runtime_mode = 3;
  string method_full_name = 4;
  string request_message_full_name = 5;
  string response_message_full_name = 6;
  string backend_instance_status = 7;
  repeated CapabilityEndpoint endpoints = 8;
}
message GetCapabilityCatalogResponse {
  string capset_id = 1;
  string name = 2;
  string description = 3;
  repeated CapabilityMethod methods = 4;
}
```

后端行为：

| RPC | OctoBus 接口 | 处理 |
| --- | --- | --- |
| `GetCapabilityStatus` | `GET /admin/v1/status` | 返回 `configured` / `ok` / `status` / `service_count` |
| `ListCapabilitySets` | `GET /admin/v1/capsets` | 归一成 UI 能力集列表 |
| `GetCapabilityCatalog` | `GET /admin/v1/catalog/{capset_id}?all=true` | 后端做 URL escaping，归一三协议入口 |

> 同一 catalog 端点还提供 `?format=md`（`text/markdown`，由 OctoBus `RenderCatalogMarkdown` 渲染）；agent-compose 在 sandbox 注入时用 `?format=md&grpc=true` 渲染能力说明写入 guest（见 [sandbox / loader 注入](#sandbox--loader-注入)）。

OctoBus catalog 结构：`?all=true` 返回 `grpc` / `mcp` / `connect_rpc` 三个并列数组，每个方法在三者各出现一次；gRPC 条目的 `metadata` 自带 `x-octobus-capset` / `x-octobus-instance` 路由信息。`service_id` 仍是 catalog/UI 字段，但不再属于当前 OctoBus gRPC 路由 metadata。agent-compose 按 join key `(service_id, instance_id, method_full_name)` 合并成 `CapabilityMethod`，用 `endpoints` 表达 gRPC / MCP / Connect 三类入口。`endpoints` 仅供 UI 展示，不含 OctoBus 地址。

## 数据面转发（仅 gRPC）

guest 连接 `CAP_GRPC_TARGET`，按 `method_full_name` 发起 gRPC 调用，metadata 携带 sandbox 凭证，并带上目标实例（取自[注入的能力说明 markdown](#sandbox--loader-注入)）：

```text
x-capability-sandbox-token: <CAP_TOKEN>
x-octobus-instance: <instance_id>   # guest 提供
```

已废弃的 `x-capability-session-token` metadata key 仅作为兼容 fallback 保留。

边界划分：**capset 是 sandbox 级隔离边界，由 capproxy 校验；instance 是 capset 内的路由，由 guest 选择。** OctoBus instance id 全局唯一，且已关联 service。capproxy 处理每个流：

```text
1. 按 token 查内存索引 -> (sandbox, allowed_capsets)
2. 校验 guest 传入的 x-octobus-capset 属于 sandbox 绑定的 allowed_capsets，guest 不能越权到别的 capset
3. reflection 方法（grpc.reflection.*）：只需 x-octobus-capset，透传
4. 业务方法：
     - 要求 guest 带 x-octobus-instance
     - 注入 OctoBus token（从 ConfigStore 读）
     - 透传到 OctoBus daemon
```

OctoBus 侧业务方法要求 `x-octobus-capset` / `x-octobus-instance`（`findGRPCExposedMethod`）：capset 由 capproxy 强制，instance 由 guest 根据注入的能力说明提供。

实现要点：

- gRPC server 用 `UnknownServiceHandler` + raw passthrough codec，双向流式透传帧到 OctoBus daemon（`grpc.NewClient` + raw codec）。
- token → sandbox 绑定用内存索引 `token -> (sandbox_id, capset_ids)`：启动时从已有 sandbox 重建，sandbox 创建/停止时增量维护。
- capproxy 校验 `x-octobus-capset` 属于 sandbox 绑定集合；业务调用要求 guest 带 `x-octobus-instance`；注入 OctoBus token。
- OctoBus addr / token 在转发时从 `ConfigStore` 读取。
- 鉴权与隔离：capset 集合由 sandbox 绑定，guest 只能在绑定集合内选择；instance 是 capset 内路由，业务调用必须由 guest 指定。`CAP_TOKEN` 是 agent-compose 签发的 sandbox 凭证，只用于解析 sandbox→capset 绑定，不能用于访问 OctoBus；OctoBus token 只在服务端，不进入 guest。

## sandbox / loader 注入

能力注入分两步，因生命周期时序不同而拆开：env/tag 要在建库前合并进创建请求，能力说明 md（MPI catalog）要在 sandbox 目录建好后才能写。两步都按 capset_ids 驱动，工作 sandbox 与 loader run 两条路径都调。

**能力注入是 best-effort，任何一步失败都不阻塞 sandbox / loader 的创建与运行**——能力是附加项，不该把 sandbox/loader 的存活耦合到 OctoBus 可用性（尤其 loader 自动调度）。失败记 sandbox event + log，能力问题留到运行时由 capproxy 透传 gRPC 错误给 agent。capset 的有效性校验放在控制面（前端用 `ListCapabilitySets` 选择），不在创建阶段做。

**步骤 1：`BuildGatewaySandboxVars(capset_ids)`（建库前）** —— 纯本地生成 env items 与 tag，**不调 OctoBus、不校验 capset**：

```text
CAP_GRPC_TARGET=<部署固定的 guest 可达代理地址>
CAP_TOKEN=<每个 sandbox 新生成 uuid>   # secret
```

tag：每个能力集一个 `capset=<capset_id>`。前提仅是至少一个 capset 已选 + `CAP_GRPC_TARGET` 已配置；后者缺失则跳过能力注入并记 warning，不阻塞创建。

**步骤 2：`writeCapabilityGuide(sandbox, capset_ids)`（workspace 准备之后、`StartSessionVM` 之前，best-effort）** —— 对每个 capset 调 OctoBus `GET /admin/v1/catalog/{capset_id}?format=md&grpc=true` 渲染能力说明 markdown，写入**sandbox MPI catalog** `<sandboxDir>/runtime/mpi/catalog.md`（经挂载出现在 guest `/data/runtime/mpi/catalog.md`）。`agent-compose-runtime`（`runtime/javascript`）的 `readMpiContext` 会读这个 catalog，把它作为**高优先级上下文注入 agent system prompt**：codex 进 `config.developer_instructions`，claude 进 `systemPrompt`（preset `claude_code` + `append`）。所以创建 sandbox 后 agent 一启动就知道有哪些能力可调，无需自己 cat 文件。渲染内容含每个 gRPC 方法及其 `x-octobus-*` metadata（capset / instance）和「用 server reflection 获取描述符」的指引，guest 据此在调用时携带 `x-octobus-capset` / `x-octobus-instance`。不含 OctoBus 地址与 token（只取 `grpc` 段）。**OctoBus 不可达 / 渲染失败时记事件并继续，sandbox/loader 照常启动**。

> 覆盖范围：codex、claude 经 `mpiContext` 注入 system prompt；gemini runner 当前未消费 `mpiContext`（已有 gap，本期不处理）。

> 时序约束：env 注入函数在 `Store.CreateSandbox` 之前调用（其返回值并入创建请求），此时 sandbox 目录尚未建立；md 写入必须等 `CreateSandbox` 建目录之后、`StartSessionVM` 挂载之前，否则会写到不存在的目录或赶不上挂载。

proto 字段：

```proto
message CreateSessionRequest {
  repeated string capset_ids = 10;
}
```

agent definition、创建 sandbox 与 loader 都保存能力集选择，`capset_ids` 加入 `AgentDefinition`、`CreateAgentSessionRequest`、`CreateLoaderRequest`、`UpdateLoaderRequest`、`LoaderSummary`、`LoaderDetail`，并作为 `agent_definition.capset_ids` / `loader.capset_ids` 列持久化。

注入链路：

| 环节 | 责任 |
| --- | --- |
| `SandboxRPCBridge.createSession` / `loader_manager.go` loader run | 接收 `capset_ids`；建库前调步骤 1，建库后调步骤 2 |
| `BuildGatewaySandboxVars`（步骤 1） | 纯本地生成 `CAP_GRPC_TARGET` / `CAP_TOKEN` env items + `capset` tags（不调 OctoBus） |
| `Store.CreateSandbox` | 持久化合并后的 `EnvItems` 和 tags，建立 sandbox 目录 |
| workspace 准备 | 填充工作区（git clone / file copy） |
| `writeCapabilityGuide`（步骤 2） | 渲染能力说明 md 写入 sandbox MPI catalog `runtime/mpi/catalog.md`（guest `/data/runtime/mpi/catalog.md`） |
| runtime driver | 将 `sandbox.EnvItems` 注入 guest，挂载工作区与 runtime 目录 |
| `agent-compose-runtime`（guest） | `readMpiContext` 读 catalog → 注入 codex / claude 的 system prompt |

## 前端

设置页「能力接入网关」：

- `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig` 编辑 `addr` / `token`。
- `GetCapabilityStatus` 实探连接状态与能力数。

创建 sandbox 与 loader：

- `ListCapabilitySets` 选择能力集，提交 `capsetIds`。

## 错误处理

| 场景 | 行为 |
| --- | --- |
| OctoBus 未配置（`addr` 空） | `GetCapabilityStatus` 返回 `configured=false` |
| OctoBus 连接失败 | 返回 `ok=false` 与错误摘要 |
| 控制面 OctoBus 返回非 2xx | 返回 Connect error，含 HTTP status |
| 控制面 `GetCapabilityCatalog` 的 capset 不存在 | not found / invalid argument |
| 注入阶段 OctoBus 不可达 / md 渲染失败 | **不阻塞**：记 sandbox event + log，sandbox/loader 照常创建运行（best-effort） |
| 数据面业务调用缺少 `x-octobus-instance` | gRPC `FailedPrecondition`（需 guest 带 `x-octobus-instance`） |
| 数据面 method / instance 未暴露给 capset | 透传 OctoBus gRPC status |
| 数据面 OctoBus 返回 gRPC status | 透传 status code / message |

响应给前端的错误不泄漏敏感网络参数；HTTP client 设置 timeout。

## 实现任务

后端：

1. `ConfigStore` 增加 `capability_gateway` 表（单行 `addr`、`token`）与 `Get` / `Save`。
2. proto：`ConfigService` 增加 `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig`；`CreateSessionRequest`、agent definition 与 loader messages 增加 `capset_ids`；`CapabilityService` 三个 rpc。重新生成 Go / TS。
3. 控制面 provider 依赖 `ConfigStore`，每次调用读 `addr` / `token`。
4. 数据面 capproxy：从 `ConfigStore` 读 OctoBus addr / token；token→sandbox 内存索引；校验 `x-octobus-capset` 属于 sandbox 绑定集合；业务调用要求 guest 带 `x-octobus-instance`；专用 gRPC listener。
5. 两步注入，工作 sandbox 与 loader run 共用：`BuildGatewaySandboxVars`（建库前，生成 `CAP_GRPC_TARGET` / `CAP_TOKEN` env + `capset` tags）；`writeCapabilityGuide`（建库后、VM 启动前，`?format=md&grpc=true` 渲染能力说明 md 写入 sandbox MPI catalog `runtime/mpi/catalog.md`，由 `agent-compose-runtime` 注入 codex / claude 的 system prompt）。

前端：

6. 设置页接 `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig` / `GetCapabilityStatus`。
7. 创建 sandbox、agent definition 与 loader 接 `ListCapabilitySets`，提交 `capsetIds`。

测试：

8. 控制面：未配置、连接失败、capsets 归一、catalog 归一、capset 不存在。
9. 数据面：校验 guest 传入的 capset 属于 sandbox 绑定集合；要求并透传 guest 的 instance；reflection 流校验 capset；OctoBus token 注入；缺少 instance → `FailedPrecondition`；OctoBus 路由错误透传。
10. 注入一致性与容错：loader 与工作 sandbox 经同一函数注入结果一致；能力说明 md 已写入 MPI catalog（`runtime/mpi/catalog.md`，非工作区）且含方法的 instance 路由 metadata；**OctoBus 不可达 / md 渲染失败时 sandbox、loader 仍成功创建运行（best-effort，不阻塞）**。
