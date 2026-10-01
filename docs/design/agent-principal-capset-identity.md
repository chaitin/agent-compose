# Agent capset 调用的 delegated principal

状态：设计提案。本文约定身份语义和选择规则；新增参数、数据库字段及执行绑定尚未实现。

## 背景

AC 中的 agent 可以响应对话，也可以由定时任务或事件触发，两者都可能调用外部 capset。
对话可以从当前调用链取得身份；定时任务运行时，创建任务的对话通常已经结束。

例如，“每天早上查询我的 OKR”需要在每天运行时仍然代表同一个人访问 OKR 系统。
因此，需要在部署 agent 时保存一个委托身份，供没有当前请求身份的执行使用。

AC 不需要为此引入用户管理系统。它负责接收、保存和传递身份引用，具体用户含义和
业务权限由上游身份系统及下游 capability 服务解释。

## 语义与范围

**Delegated principal** 表示 agent 代表谁调用 capset。`user/alice`、`service/okr-reader`
是示意值，具体引用格式需要与身份提供方和 capset 消费方约定。该引用不是登录凭证，
也不等同于 agent ID、sandbox ID 或 OctoBus 的连接 token。

同一个 delegated principal 有两种来源：

| 来源 | 含义 | 生命周期 |
| --- | --- | --- |
| trust header | 当前调用链提供的委托身份 | 当前 session/run |
| configured | apply 时保存的 agent 默认委托身份 | 持久保存，直到更新或清除 |

**Effective delegated principal** 是从这两个来源中为本次执行选出的身份。选择规则统一为：

```text
有效的 trust header 身份 > configured delegated principal > 无身份
```

不按“内部/外部”“对话/事件”分支，也不要求调用入口额外指定 delegation mode。
例如，事件执行如果确实携带了受信的调用身份，也适用同一优先级。

## apply 如何使用

在 apply 操作上增加 `--delegated-principal` 参数。当前 CLI 通过 `up` 执行 apply，
拟议用法为：

```bash
agent-compose up --file agent.yaml --delegated-principal user/alice
```

API 对应字段建议为 `ApplyProjectRequest.delegated_principal_ref`，需要区分“未提供”
和“显式清除”。当前提案将单个参数应用到本次 apply 的各个 agent，分别保存在各自
的 `project_agent` 记录中；不引入按 scheduler 覆盖的配置。

建议更新语义：

| 参数 | 行为 |
| --- | --- |
| 提供非空 principal reference | 创建或更新绑定 |
| 未提供 | 保留已有绑定；新 agent 默认为空 |
| `--delegated-principal=none` | 显式清除绑定；`none` 为保留值 |

apply 不要求提供用户身份或 trust header，也不会从 apply 的 header 自动推导并保存
默认身份。谁可以调用 apply 仍由已有的 daemon 控制面访问机制决定，本提案不增加
“Alice 是否可以代表 Bob 建立委托”的用户授权模型。

AC 校验参数格式并保存引用；如果部署需要限制可配置哪些主体，由控制面或上游平台
实施该策略。保存引用本身不会给调用方新增 capset 权限。

## 为什么不写入 YAML

YAML 描述可复用的 agent 行为、资源和 capset 声明；delegated principal 描述这次部署
中 agent 默认代表谁执行。

同一份 YAML 可以在 Alice 的环境中代表 Alice，在 CI 环境中代表服务账号。通过 apply
参数传入身份，部署系统可以独立管理这个绑定，不必修改项目源码或把某个环境的主体
固定在可共享的 YAML 中。

这是一项配置归属的选择。Principal reference 本身不是秘密；使用参数的主要目的并非
隐藏凭证。凭证不应放入该参数或 YAML。

## 存在哪里

### Agent 默认身份

在 `project_agent` 增加一列，作为 configured 身份的权威来源：

```sql
delegated_principal_ref TEXT NOT NULL DEFAULT ''
```

不放入 YAML 或其 `spec_json`。空值表示未配置委托身份。重新 apply 时即使 YAML 内容
不变，也必须处理这个参数的更新；普通 re-apply 不清除已有值。

### 当前执行的身份

创建 session/run 时解析一次有效身份，并绑定到本次执行。建议执行上下文记录
`principal_ref` 和 `source`（`trustheader`、`configured` 或 `none`）；source 是解析结果，
不是调用者必须选择的模式。

Configured 身份可在 `project_run` 保存快照，避免之后修改 agent 配置影响正在执行的
run 或历史记录。Trust header 的请求上下文仍保留在内存中，不复制到 sandbox metadata。
是否额外保存从 header 提取的主体引用用于审计，需要明确的数据保留约定；本提案不把
请求身份落库作为必需条件。

Sandbox 的 `metadata.json` 保存 sandbox 级状态。身份归属当前 session/run，不作为
可复用 sandbox 的全局用户身份。即使 metadata 中保存 configured 身份的执行快照用于
恢复，它也不能覆盖当前执行由 trust header 选出的身份，更不能被下一次请求直接继承。

## 如何决定 effective delegated principal

在创建本次执行上下文时，统一执行下面的规则：

```text
如果当前执行携带 trust header 身份：
    校验受信来源和身份格式
    无效 -> 返回错误，不回退
    有效 -> 使用该身份，source = trustheader
否则如果 agent 配置了 delegated_principal_ref：
    使用当前配置的快照，source = configured
否则：
    无委托身份，source = none
```

| 当前执行的 trust header 身份 | Agent 配置 | 结果 |
| --- | --- | --- |
| 有效的 Bob | Alice | Bob |
| 有效的 Bob | 空 | Bob |
| 未提供 | Alice | Alice |
| 未提供 | 空 | 无委托身份 |
| 已提供但无效 | Alice 或空 | 报错，不使用 Alice 兜底 |

这里的 trust header 必须是当前执行接受的受信身份上下文。Guest 自己传入的 header、
任意事件 payload 中名为 principal 的字段，都不能直接成为这个高优先级来源。

身份按完整主体选择，不把 Bob 的主体字段与 Alice 的身份属性拼成一个混合身份。
具体哪些 header 构成主体、哪些只是附加元数据，需要在映射协议中明确；不能把任意
一个 `x-mpi-*` header 的存在都理解成“已经提供了完整身份”。

一次执行选定身份后保持不变：

- 更新 `project_agent` 配置只影响之后创建的执行；
- 另一条请求的 header 不改变已有执行；
- 因 daemon 重启、缓存重建等原因丢失请求身份，不等于新执行“未提供 header”。已有
  执行不能因此切换到 configured 身份，需要重新建立原身份上下文才能继续身份相关调用。

## Sandbox 调用 capset 的流程

Sandbox 发起调用时，身份已由 AC 绑定到执行上下文。Capproxy 消费该结果：

1. 验证 capability token，定位它对应的 sandbox 和当前 session/run 绑定；
2. 检查请求的 capset 是否在授权范围内；
3. 读取该执行的 effective delegated principal；
4. 按约定转换为下游的受信身份元数据，覆盖 guest 伪造的身份字段；
5. 使用 daemon 配置的上游连接凭证转发调用。

Principal 描述“代表谁”；`capset_ids` 描述可访问哪些能力集合；上游 token 用于连接
OctoBus。这三者的作用独立，principal 不替代 capset grant 或下游业务权限检查。

无委托身份时，不自动使用 daemon、root 或上一次调用者。允许无身份访问的 capset
可以继续工作；要求身份的调用应明确失败。下游拒绝 Bob 时，也不能换成 Alice 重试。

### Sandbox 复用

例如 agent 默认配置 Alice，Bob 的请求复用了该 agent 的 sandbox：本次执行使用 Bob，
不修改 agent 的 Alice 绑定。随后一个没有 trust header 的新执行仍使用 Alice。

实现必须把调用可靠地关联到本次执行。不能仅凭“这个 sandbox 最近绑定了哪个用户”
推断旧进程或并发执行的身份。若沿用 sandbox token，需要约束执行互斥、处理遗留进程
并管理凭证生命周期；若支持多个身份并发执行，则需要执行级凭证或等价隔离机制。
具体机制留在实现阶段确定，不能把当前 sandbox token 宣称为已经具备 run 级隔离能力。

## 当前实现与待接入边界

现有实现提供了部分基础：

- `cmd/agent-compose/daemon_trusted_headers.go` 接收受信入口注入的 `x-mpi-*`，校验名称
  和值；身份可信性依赖入口剥离客户端伪造值及隔离 daemon 访问，并非 AC 自行认证用户。
- `pkg/agentcompose/adapters/capability_sandbox.go` 将请求 header 放入内存中的 sandbox
  token binding，并转换成 `x-octobus-ext-*`。重新索引可清除旧 header，重建后不恢复它们。
- `pkg/capproxy/proxy.go` 删除 guest 提供的 `x-octobus-ext-*`，再写入 binding 中的受信值。

当前 header 是一组元数据，尚无统一的 principal reference 解析或反向映射协议。接入
configured 身份时，需要与下游约定如何把 reference 映射到相同的身份语义；本提案不
假设 AC 已能签发用户 token，也不要求引入完整 IAM 系统。

“不落库”在本文指请求身份不作为 sandbox 的持久身份。仓库现有 webhook 路径会将
部分受信 MPI header 保存到事件 payload；这类事件数据不等于可恢复的执行身份，不能
直接当作可信凭证使用。其保留策略与身份传播需要在接入该路径时单独明确。

## 每日 OKR 示例

部署时配置默认身份：

```text
apply 参数：--delegated-principal user/alice
  -> project_agent.delegated_principal_ref = user/alice
```

之后的执行遵循同一规则：

| 执行 | 当前请求身份 | 有效身份 |
| --- | --- | --- |
| 每天早上的 OKR 定时任务 | 无 | Alice |
| Bob 发起对话查询 OKR | Bob | Bob |
| 不携带请求身份的手动运行 | 无 | Alice |
| 携带无效身份的调用 | 无效 | 拒绝，不回退 |

Bob 的对话不会改变每日任务的 Alice 配置。创建定时任务的对话身份也不会自动变成
持久绑定；需要通过 apply 参数明确保存，之后每次运行再按优先级解析。
