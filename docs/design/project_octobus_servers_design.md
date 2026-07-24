# Project-scoped OctoBus servers 设计

## 1. 背景与目标

agent-compose 当前只支持在 daemon Settings 中配置一个全局 OctoBus
连接。所有 project、agent 和 sandbox 共享同一个 OctoBus 地址与 token；agent
只通过 `capset_ids` 声明允许使用的 capability sets。

单一全局连接限制了以下使用方式：

- 不同 project 不能连接不同的 OctoBus 部署；
- 同一个 project 不能同时使用多个 OctoBus；
- project 配置不能完整表达 agent 所依赖的 capability 来源；
- project 迁移到其他 daemon 时，还需要额外同步 daemon Settings。

本设计在 `agent-compose.yml` 顶层增加 project-scoped
`octobus_servers`，允许 agent 通过 qualified capset ID 选择 OctoBus server。
同时完整保留现有全局配置和未限定 capset ID 的语义，使已有配置在升级后无需迁移。

本设计的目标是：

1. 一个 project 可以声明多个具名 OctoBus server；
2. agent 继续使用现有 `capset_ids` 字段，不增加新的 agent 配置字段；
3. 未包含 server name 的 capset ID 继续使用 daemon 全局 OctoBus；
4. qualified capset ID 只改变 daemon 选择的上游连接，不改变 OctoBus 的
   gRPC method、header 或鉴权协议；
5. project server 的 normalize、revision、agent definition 和更新生命周期尽量
   与现有 MCP servers 保持一致；
6. OctoBus token 始终留在 daemon，不写入 guest env，不发送给 agent；
7. 已运行 sandbox 对 `capset_ids` 的授权保持不变，project re-apply 后的
   URL/token 更新对其后续调用生效，与现有 MCP agent definition 更新风格一致。

非目标：

- 不替换或删除 daemon 全局 OctoBus Settings；
- 不改变 OctoBus 上游 API 或 metadata 契约；
- 不把 MCP、Connect RPC 或 REST capability 暴露给 guest；
- 不在首版引入 OctoBus 连接池、健康检查调度或故障转移；
- 不为 project OctoBus server 创建独立的用户可管理资源；
- 不在本次设计中引入通用 secret store 或数据库加密。

## 2. 配置格式

### 2.1 完整示例

```yaml
octobus_servers:
  internal:
    url: https://octobus.internal.example
    token: ${OCTOBUS_INTERNAL_TOKEN}

  public:
    url: https://octobus.example
    token: ${OCTOBUS_PUBLIC_TOKEN}

agents:
  coder:
    capset_ids:
      - legacy-capset
      - internal/dev
      - public/web-search
```

语义如下：

| 声明 | daemon 选择的上游 | 发送给上游的 `x-octobus-capset` |
| --- | --- | --- |
| `legacy-capset` | daemon 全局 OctoBus | `legacy-capset` |
| `internal/dev` | 当前 project 的 `internal` server | `dev` |
| `public/web-search` | 当前 project 的 `public` server | `web-search` |

`octobus_servers` 使用 map 而不是列表。map key 是 project 内稳定的 server
name，并作为 `capset_ids` 中 `/` 前的限定符。`url` 与现有 `mcp_servers`、
workspace 和 skill 配置命名保持一致。

`token` 是可选字符串：

- 支持现有 compose 环境变量插值，例如 `${OCTOBUS_INTERNAL_TOKEN}`；
- 空字符串表示不向该 OctoBus 发送 `Authorization`；
- 非空时 daemon 按现有逻辑注入 `Authorization: Bearer <token>`；
- `token` 字段本身具有敏感凭据语义，不增加 `secret` 子字段；
- token 不得进入 guest env、sandbox tag、capability guide 或日志。

### 2.2 Compose 类型

建议在 `pkg/compose` 增加：

```go
type ProjectSpec struct {
    // Existing fields omitted.
    OctoBusServers map[string]OctoBusServerSpec `yaml:"octobus_servers,omitempty" json:"octobus_servers,omitempty"`
}

type OctoBusServerSpec struct {
    URL   string `yaml:"url,omitempty" json:"url,omitempty"`
    Token string `yaml:"token,omitempty" json:"token,omitempty"`
}
```

Go 标识符统一使用项目当前采用的 `OctoBus` 拼写；YAML/JSON 字段固定为
`octobus_servers`。normalized spec 增加对应的
`map[string]NormalizedOctoBusServerSpec`。

不为 token 使用 `EnvVarSpec`，因为这里没有 `value`/`secret` 的复合结构。
环境插值应直接复用 compose 已有的 `interpolateEnvValue`，并在错误中保留完整字段
路径，例如：

```text
octobus_servers.internal.token: environment variable OCTOBUS_INTERNAL_TOKEN is not set
```

### 2.3 Qualified capset ID

`capset_ids` 的外部表示保持 `[]string`。内部解析规则为：

```go
type CapsetReference struct {
    ServerName string
    CapsetID   string
}
```

- 不包含 `/`：`ServerName` 为空，使用 daemon 全局 OctoBus；
- 包含 `/`：按第一个 `/` 分割，前半部分是 server name，后半部分是传给
  OctoBus 的真实 capset ID；
- `internal/a/b` 解析为 server `internal` 和 capset ID `a/b`；
- server name 为空或 capset ID 为空是配置错误；
- server name 使用现有 stable identifier 校验，因此不能包含 `/`；
- 对外继续称为 `capset_ids`，不增加 `capsets`、`capset_refs` 等迁移字段。

qualified value 是 agent-compose 的本地路由表示，不是新的 OctoBus capset ID。
代理在调用上游前必须去掉 server name，OctoBus 永远只看到真实 capset ID。

## 3. 兼容性契约

### 3.1 路由兼容矩阵

| `capset_ids` | project `octobus_servers` | 行为 |
| --- | --- | --- |
| `dev` | 未配置 | 使用 daemon 全局 OctoBus |
| `dev` | 已配置 | 仍使用 daemon 全局 OctoBus |
| `internal/dev` | 存在 `internal` | 使用 project `internal` server |
| `internal/dev` | 不存在 `internal` | project validation error |
| `internal/dev` | 未配置 servers | project validation error |

是否配置 `octobus_servers` 不能改变 unqualified capset 的行为。这样旧 project
增加新 server 时，不会使原有 capset 静默切换上游。

daemon 全局 OctoBus 未配置而 agent 使用 unqualified capset 时，project
声明本身仍然合法，但 validate/apply 应产生 warning。sandbox 创建保持当前
best-effort 行为，实际调用继续返回现有的 gateway not configured 错误。

### 3.2 全局 Settings 和 API

以下现有能力保持不变：

- `capability_gateway` 单行配置表；
- `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig`；
- 全局 capability status、capset list 和 catalog API；
- 未限定 capset 的 guide/catalog 和 data-plane forwarding；
- 全局 token 的读取、脱敏和更新语义。

首版不把多个 project server 合并进现有全局 `ListCapabilitySets` 响应。需要查询
project capability 时，API 必须携带明确的 project/agent 上下文，避免把不同
project 的 catalog 或凭据混在全局视图中。

### 3.3 持久化和 protobuf 兼容

project revision 的 canonical spec 会增加 `octobus_servers`。如果 project API
通过 protobuf 表达结构化 spec，应只追加新的 message/field 和字段号，不修改或
复用已有字段。

旧 revision 不包含 `octobus_servers`，解码后视为 nil/empty map。旧
`capset_ids` 均为 unqualified reference，因此自动走全局 OctoBus，不需要数据
迁移或后台 backfill。

## 4. 配置校验和规范化

### 4.1 Server 校验

compose normalize 对每个 server 执行：

1. 按 key 排序，确保 canonical output 和 spec hash 稳定；
2. 使用 stable identifier 校验 server name；
3. trim `url`，并要求非空；
4. URL 必须是绝对 URL，且 scheme 是当前 OctoBus client 支持的 `http` 或
   `https`；
5. 禁止 URL 中携带 userinfo，避免凭据绕过 token 字段进入日志或输出；
6. trim/interpolate token；
7. 不连接 OctoBus，不在纯 normalize 阶段执行网络探测。

token 允许为空。URL 是否可达、token 是否有效属于运行状态，不应让临时外部
故障破坏离线 validate、dry-run 或 project apply 的确定性。

### 4.2 Agent capset 校验

normalize agent 时，在现有 `NormalizeCapsetIDs` 基础上增加 project server
引用校验：

- unqualified capset 不依赖 project server map；
- qualified capset 的 server name 必须存在；
- qualified capset 的真实 capset ID 必须非空；
- 完整声明字符串用于去重和排序；
- 不向 OctoBus 查询 capset 是否存在。

例如 `dev` 与 `internal/dev` 是两个不同的授权项，即使二者的真实 capset ID
都是 `dev`。它们分别选择全局和 project server。

### 4.3 Warning 与 error 的边界

| 情况 | 结果 |
| --- | --- |
| server name 或 URL 非法 | validation error |
| qualified capset 引用不存在的 server | validation error |
| token 环境变量无法解析 | validation error |
| unqualified capset 且全局 gateway 未配置 | warning |
| OctoBus 当前不可达 | apply 不失败；status/guide/call 报运行错误 |
| catalog 中不存在声明的 capset | apply 不失败；guide/call 报运行错误 |

## 5. 持久化和 MCP 一致性

### 5.1 Project revision

normalized `octobus_servers` 与 `mcp_servers` 一样进入 project canonical spec、
spec hash 和 revision JSON。因此：

- server URL 或 token 变化会形成新的 project revision；
- inspect/diff 能反映 server 配置变化；
- project run 仍记录其创建时的 project revision；
- 旧 revision 可以按缺少该字段的兼容默认解码。

token 会像当前 compose 中其他已经解析的凭据一样进入 daemon 持久化的非脱敏
revision JSON。所有面向用户的 project/proto/YAML/JSON 输出必须对
`octobus_servers.*.token` 脱敏或省略。`token` 的直接字符串格式不表示它可以
安全输出。

建议 canonical output 规则为：

- `redactSecrets=false`：包含实际 token，用于 daemon 内部 revision 和 hash；
- `redactSecrets=true`：token 非空时输出统一占位值，token 为空时保持为空；
- 错误、日志、changes summary 中不得包含 token 或带 userinfo 的 URL。

### 5.2 AgentDefinition.ConfigJSON

复用 MCP server 的选择和持久化方式，扩展现有 agent config payload：

```go
type agentDefinitionConfigPayload struct {
    Jupyter        *compose.JupyterSpec
    MCPServers     map[string]compose.NormalizedMCPServerSpec
    OctoBusServers map[string]compose.NormalizedOctoBusServerSpec
}
```

每个 agent 只复制其 qualified `capset_ids` 实际引用的 server。unqualified
capsets 不复制全局 gateway 配置；全局配置仍由现有 `GatewaySource` 动态读取。

例子：project 声明 `internal`、`public`、`unused`，agent 只引用
`internal/dev` 和 `public/search`，则该 agent definition 只保存 `internal` 和
`public`。

这使 server 更新自然进入现有 managed agent definition reconcile：

- re-apply 会更新同一个 stable managed agent definition；
- server URL/token 变化会被判断为 agent definition 变化；
- 删除仍被 agent 引用的 server 会在 normalize 阶段失败；
- 不需要新增 project OctoBus server 数据表或独立 reconcile controller。

## 6. Sandbox 生命周期和更新语义

### 6.1 复用现有 sandbox 固化信息

当前 sandbox 创建时已经固化：

- capability proxy target；
- daemon 生成的 secret `CAP_TOKEN`；
- 每个允许的 `capset_id` tag；
- env、workspace snapshot、volume mounts、driver 和 guest image 等运行配置。

本设计继续使用这些机制：

- sandbox tag 保存完整授权字符串，例如 `dev` 或 `internal/dev`；
- guest 仍只获得 `CAP_GRPC_TARGET` 和 `CAP_TOKEN`；
- project OctoBus URL/token 不进入 sandbox env 或 tag；
- `CapabilitySandboxResolver` 仍通过 sandbox token 恢复 sandbox 和 allowed
  capsets；
- 不新增通用 sandbox config snapshot 或 credential snapshot 表。

### 6.2 Re-apply 后的行为

本设计采用与当前 MCP server 最接近的更新语义：

1. sandbox 创建时的 allowed `capset_ids` 固定，不因 re-apply 增删；
2. OctoBus URL/token 属于 managed agent definition 的可更新配置；
3. project re-apply 更新 agent definition 后，后续 capability 调用使用最新的
   URL/token；
4. 已运行 sandbox 不需要停止或重建；
5. re-apply 新增的 capset 不会自动授权给旧 sandbox，因为它不在旧 sandbox
   的 allowed tags 中；
6. re-apply 删除的 capset 对旧 sandbox 的授权 tag 仍存在，但如果对应 server
   已不再能从当前 agent definition 解析，请求返回明确的 failed precondition，
   不能回退到其他 server；
7. 新 sandbox 使用最新 agent definition 和最新 `capset_ids`。

这一语义也延续当前全局 OctoBus 的行为：全局 URL/token 在 forwarding 时动态
读取，Settings 更新后不要求重建 sandbox。

为了让运行中的 sandbox 能动态找到最新 agent definition，sandbox binding
需要包含或能恢复以下 daemon 内部身份：

```go
type SandboxBinding struct {
    SandboxID        string
    ManagedProjectID string
    ManagedAgentID   string
    CapsetIDs        []string
}
```

优先复用 sandbox 已有 project/run/agent tags。若现有 sandbox 创建路径不能稳定
恢复 managed agent ID，应增加一个内部 identity tag，而不是把 URL/token 写入
sandbox。手工创建、legacy loader 或没有 managed agent identity 的 sandbox 只能
使用 unqualified capsets；qualified capsets 必须有明确的 project/agent scope。

daemon 重启时，resolver 继续从持久化 sandbox 重建 token index。重建后的
binding 通过 managed agent ID 获取当前 AgentDefinition.ConfigJSON，因此重启前后
具有相同的动态更新语义。

## 7. Control plane：status、list、catalog 和 guide

### 7.1 Scope

当前全局 `capabilities.DynamicProvider` 保留，继续服务 unqualified capset。
新增的 resolver 需要显式 scope：

```go
type CapabilityScope struct {
    ProjectID      string
    AgentID        string
}

type ProviderResolver interface {
    Resolve(ctx context.Context, scope CapabilityScope, serverName string) (capabilities.Provider, error)
}
```

- `serverName == ""`：返回现有全局 provider；
- 非空：从 scope 对应的当前 AgentDefinition.ConfigJSON 读取具名 server；
- 缺少 scope、server 不存在或不属于该 agent 时返回明确错误；
- 缓存 key 至少包含 managed agent identity、server name 和当前配置内容或
  revision，不能只按 URL 缓存。

URL 不是隔离身份。相同 URL 可以被不同 project 配置不同 token，因此不能用
`project ID + URL` 以外缺少 server/agent identity 的共享缓存来复用凭据。

### 7.2 Catalog 与 guide 路由

catalog/guide 接受原始 capset declaration：

```text
dev          -> global provider, catalog/dev
internal/dev -> project internal provider, catalog/dev
```

HTTP 请求的 path 始终使用真实 capset ID，不包含 server name。生成的 guide 中：

- `x-octobus-capset` 必须是 guest 发给 agent-compose proxy 的完整授权字符串；
- 对 `internal/dev`，guide 指示 guest 使用 `internal/dev`，使 proxy 能选择
  server；
- `x-octobus-instance`、method 和其他 OctoBus metadata 保持原有内容；
- guide 不包含 OctoBus URL 或 token。

guide 写入仍是 best-effort：某个 server 不可达时记录不含凭据的 sandbox event
并继续创建/启动 sandbox。多个 capsets 可来自不同 servers，生成器应按 server
分组以减少重复 client 创建，但最终合并到同一 MPI catalog。

## 8. Data plane 路由

### 8.1 请求流程

guest 仍连接单一 `CAP_GRPC_TARGET`。对于 project capset，guest 请求示例为：

```text
x-capability-sandbox-token: <CAP_TOKEN>
x-octobus-capset: internal/dev
x-octobus-instance: <instance_id>
```

daemon 内部流程：

```text
1. CAP_TOKEN -> SandboxBinding
2. 验证完整值 internal/dev 在 binding.CapsetIDs 中
3. 解析 serverName=internal, actualCapsetID=dev
4. binding.ManagedAgentID -> 当前 AgentDefinition.ConfigJSON
5. 读取 internal 的 URL/token
6. 构造原有 outgoing metadata，将 x-octobus-capset 设置为 dev
7. 非 reflection method 继续要求 x-octobus-instance
8. 使用 internal URL 建立上游 gRPC stream
9. token 非空时继续注入 Authorization: Bearer <token>
10. 双向透明转发 raw gRPC frames
```

对于 unqualified `dev`，第 4、5 步替换为现有
`GetCapabilityGateway(ctx)`，其余逻辑不变。

### 8.2 Header 契约

发送给 OctoBus 的 header 必须保持当前语义：

| Header | 行为 |
| --- | --- |
| `x-octobus-capset` | 真实 capset ID；去除 agent-compose server qualifier |
| `x-octobus-instance` | guest 根据 guide 提供；原样转发 |
| `authorization` | daemon 根据选中的 server token 注入 |
| `x-capability-sandbox-token` | daemon 消费并删除，不发送上游 |

不得向 OctoBus 新增 project ID、agent ID、server name 等 header。多 server
能力只改变 `grpc.NewClient` 使用的 target 和对应 token，不改变 OctoBus 的路由
协议、method path、reflection 或 frame forwarding。

### 8.3 授权边界

必须先按 sandbox token 解析 binding，再解释 server name。不能允许 guest 通过
server name 查询 daemon 中任意 project 的配置。

校验对象是完整 declaration，而不是只有真实 capset ID。例如 sandbox 只允许
`internal/dev` 时：

- `internal/dev`：允许；
- `dev`：拒绝，不能借此改走全局 server；
- `public/dev`：拒绝；
- `internal/other`：拒绝。

project/agent identity 只来自 daemon 持久化的 sandbox binding，不能来自 guest
metadata。

## 9. 错误处理

建议保持现有 gRPC error 分类：

| 条件 | Code |
| --- | --- |
| sandbox token 缺失或未知 | `Unauthenticated` |
| capset 不在 sandbox allowed set | `PermissionDenied` |
| qualified capset 缺少 project/agent scope | `FailedPrecondition` |
| 当前 agent definition 不再包含 server | `FailedPrecondition` |
| 全局或 project server 未配置 | `Unavailable` |
| URL 无法连接或 upstream 不可用 | `Unavailable` |
| business call 缺少 instance | 保持当前 `FailedPrecondition` |

错误消息可以包含 project-local server name 和 capset declaration，但不得包含
token、Authorization、完整带 query/userinfo 的敏感 URL，或其他 project 的配置。

## 10. 代码组织建议

按现有 package ownership，建议修改面如下：

| 责任 | 所属位置 |
| --- | --- |
| YAML schema、normalize、canonical output | `pkg/compose` |
| agent config payload 和 server selection | `pkg/projects` |
| agent definition config 解码 | `pkg/capabilities` 或消费它的相邻包 |
| sandbox identity/binding 构建 | `pkg/agentcompose/adapters` |
| server/capset reference 解析与 gateway provider | `pkg/capabilities` |
| raw gRPC 上游选择和转发 | `pkg/capproxy` |
| Connect/protobuf 映射 | `pkg/agentcompose/api`、`proto` |

业务概念应归 `pkg/capabilities`，`pkg/capproxy` 只负责鉴权后的 data-plane
转发，API 层只做 transport mapping。不要把 project server 解析或配置选择逻辑
放入 `cmd/agent-compose`。

建议保留当前全局 `OctoBus` resolver，并将代理配置扩展为能按 binding 和完整
capset declaration 解析 target 的小接口，而不是让 capproxy 直接依赖 config DB
或 project store 的具体实现。

## 11. 测试计划

### 11.1 Compose 单元测试

- 解析多个 `octobus_servers`；
- token 环境变量插值；
- server map canonical 排序；
- spec hash 包含 URL/token 变化；
- redacted JSON/YAML 不泄漏 token；
- 非法 server name、空 URL、相对 URL、unsupported scheme；
- qualified/unqualified capset 解析；
- 缺失 server 引用；
- `internal/a/b` 按第一个 `/` 分割；
- 旧 compose 不含新字段时输出和行为兼容。

### 11.2 Project 和持久化测试

- revision round trip 保存 servers；
- proto/YAML mapping round trip；
- AgentDefinition.ConfigJSON 只包含 agent 引用的 servers；
- URL/token 改变触发 managed agent definition update；
- 未使用 server 改变只影响 project revision，不错误注入 agent config；
- 旧 revision 解码为空 server map；
- API、dry-run 和 inspect 输出不泄漏 token。

### 11.3 Provider 和 guide 测试

- unqualified capset 使用全局 client；
- qualified capset 使用正确 project client；
- 相同 URL、不同 token 的 project 不串用凭据；
- catalog path 使用真实 capset ID；
- guide 对 guest 展示完整 qualified declaration；
- 多 server guide 合并；
- 一个 server 失败不阻塞 sandbox 创建；
- event/log 不包含 token。

### 11.4 Capproxy 测试

- 完整 declaration 的 allow/deny 校验；
- project URL 选择正确；
- 上游只收到真实 capset ID；
- `x-octobus-instance` 和 method 保持不变；
- sandbox token 和 incoming authorization 不向上游泄漏；
- project token 正确替换 incoming authorization；
- unqualified path 与现有全局测试完全兼容；
- reflection 和 business stream 均支持多 server；
- re-apply 更新 URL/token 后，已有 sandbox 的下一次调用使用新配置；
- re-apply 不自动增加旧 sandbox 的 allowed capsets；
- daemon restart rebuild 后仍能恢复 project/agent scope；
- 并发 project 调用不会串 server 或 token。

涉及 resolver index 或并发调用的测试应运行 `go test -race`。

### 11.5 集成测试

至少使用两个独立 `httptest`/gRPC OctoBus stub：

1. project 同时从两个 servers 生成 guide；
2. 同一 sandbox 分别调用 `internal/dev` 和 `public/web-search`；
3. 验证两个上游收到各自 token 和未限定的真实 capset ID；
4. 同时验证 `legacy-capset` 仍到全局上游；
5. re-apply 修改 `internal` URL/token 后，不重建 sandbox 再次调用；
6. 重启 resolver/rebuild 后重复调用并验证隔离。

## 12. 文档和发布

实现时需要同步：

- compose YAML 英文和中文手册；
- project/capability API 文档；
- `docs/design/octobus_integration.md` 当前单全局实现描述；
- compose schema coverage 和生成页面；
- 升级说明，明确旧 `capset_ids` 无需迁移；
- 安全说明，明确 project token 只在 daemon 内使用，redacted 输出不会返回它。

推荐发布说明示例：

> Projects may declare named `octobus_servers` and qualify existing
> `capset_ids` as `<server>/<capset>`. Unqualified capset IDs continue to use
> the daemon-wide OctoBus configuration, so existing projects require no
> migration.

## 13. 实施顺序

建议按以下顺序交付，确保每一步都有明确兼容边界：

1. compose schema、normalize、reference parser、canonical redaction 和测试；
2. protobuf/API mapping 与 project revision round trip；
3. AgentDefinition.ConfigJSON server selection 和 reconcile 测试；
4. sandbox binding 增加 managed project/agent identity；
5. project-aware provider resolver，以及 status/catalog/guide 路由；
6. capproxy 按 server 选择 URL/token，并保持上游 metadata 不变；
7. re-apply、restart rebuild、并发隔离和 regression integration tests；
8. 中英文用户文档和完整质量门禁。

## 14. 最终决策摘要

- 新增顶层 `octobus_servers` map；
- server 字段只有 `url` 和可选字符串 `token`；
- token 支持 `${ENV}`，不增加 `secret` 字段；
- agent 继续使用 `capset_ids`；
- 无 `/` 的 capset 永远使用 daemon 全局 OctoBus；
- 有 `/` 的 capset 按第一个 `/` 选择 project server；
- agent-compose 在转发前去除 server qualifier，上游 header 契约不变；
- server 配置进入 project revision 和选中它的 AgentDefinition.ConfigJSON；
- sandbox 固定 allowed capsets，但 URL/token 在后续调用时读取最新 managed agent
  definition，re-apply 不要求重建 sandbox；
- URL/token 不进入 guest 或 sandbox metadata；
- 全局 Settings/API 和旧 project 行为保持兼容；
- 首版不新增独立 server 表、通用 snapshot 机制或连接池。
