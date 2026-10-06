# Capset 调用中的用户身份

状态：已按本文实现。本文约定 AC 在 capset 调用上传递哪些身份、不传递哪些身份，以及
scheduler 相关路径的行为。

## 背景

AC 中的 agent 可以由人发起运行（对话、API 调用、手动触发某个 trigger），也可以由
scheduler 的定时或事件 trigger 自行启动。两种运行都可能经 capproxy 调用外部 capset。

有人发起时，受信入口在请求上注入 `x-mpi-*` 头，AC 把它绑定到本次运行使用的 sandbox，
capproxy 转发时改写成 `x-octobus-ext-*` 交给上游。这是 AC 已有的能力。

此前 scheduler 相关的路径没有一致地遵守这条规则：手动触发 scheduler 时请求头在执行
阶段丢失；同一个脚本里不同的调用方式看到的身份不同；sandbox 复用时会沿用上一个使用者
留下的绑定。

## 模型

Agent 本身没有身份。一次运行替谁做事，取决于是谁发起了它。

AC 只做一件事：**有发起者的身份就如实传递，没有就什么都不传。**

| 运行 | AC 传给上游的用户身份 |
| --- | --- |
| 有人发起，入口注入了身份头 | 该请求的身份头 |
| 有人发起，入口没有注入身份头 | 无 |
| 无人值守（timer、event、webhook） | 无 |

AC 不为没有身份的运行补一个身份。不使用 project 的创建者、上一次的调用者、scheduler
上保存的某个用户，也不使用 daemon 自身。要求身份的调用由上游拒绝。

### 部署身份不在 AC

“这次调用来自哪个部署”由上游的连接凭证表达，不由 AC 保存或声明。Project 在
`octobus_servers` 里为每个上游配置地址和 token，capproxy 转发时带上该 token。上层平台
为每个部署签发专用 token 时，上游凭它就能识别并验证调用来自哪个部署，无人值守的运行
也不例外。

因此 AC 不保存 project 级或 scheduler 级的身份属性。无人值守的运行应以什么主体访问
下游，由持有并验证该 token 的一方决定。

### 不做的事

- **代表某个用户持续自动执行。** 例如每天以某个用户的权限替他查询数据。这是委托，
  需要授权、凭证、配额和撤销机制，不能靠保存一个用户名实现。
- **判断身份是否有效或有权。** AC 只做语法校验（名称字符集、值为 1–256 字节可打印
  ASCII），用户是否存在、是否有权由上游决定。

## 行为

### 手动触发 scheduler 带上发起者的身份

`RunScheduler` 与 `StartSchedulerRun` 提交后，运行不随请求结束而取消，所以执行 ctx
派生自 daemon 的 root ctx。执行 ctx 上会恢复请求的元数据（受信头与 trace 上下文），
做法与 `StartAgentRun` 的异步执行相同。`InvokeScheduler` 直接在请求 ctx 上执行脚本，
本来就能看到受信头。

timer 与 event 触发的运行没有请求，执行 ctx 上没有受信头。

### 一次执行内所有调用看到同一个身份

Scheduler 脚本进入 sandbox 有三种方式：`agent`、`command` 和 `CallSandboxRPC`。其中
按项目运行的 `agent` 走项目运行的代码，其余经 `SchedulerSandboxRunner` 或
`SandboxRPCBridge` 创建、恢复 sandbox。

所有这些路径在绑定 sandbox 的 capability token 时，都从执行 ctx 的受信头读取身份。
同一个脚本里先调 `agent` 再调 `command`，两者的身份一致。

`SandboxRPCBridge` 同时也是外部 `SandboxService` 的实现。外部的 `ResumeSandbox` 是生命
周期操作而不是一次运行：恢复者不一定是之后的使用者，恢复之后也没有“结束”可以用来清理
绑定。因此它不绑定请求的身份，行为与此前一致；下一次运行开始时再绑定该运行的身份。

### 进入正在运行的 sandbox 时重新绑定

Sandbox 的 capability 绑定按 sandbox 保存，后写覆盖。一次执行进入一个已经在运行的
sandbox 时，绑定会被替换成本次执行的身份；本次执行没有身份时，绑定被清空。

这保证无人值守的运行不会以上一个使用者的身份调用 capset，一个用户也不会继承另一个
用户的身份。

### 带用户身份的执行不使用 sticky sandbox

Sticky sandbox 会在多次运行之间保留，并且由该 trigger 的无人值守运行共用。带用户身份
的执行如果进入其中，用户的绑定和运行中产生的数据都会留在这个共用的 sandbox 里。

因此，执行 ctx 上有受信头时：

- 脚本里的 `agent` 与 `command` 一律按 `sandbox_policy: new` 处理，使用本次执行自己的
  sandbox，结束后随 sandbox 一起清理绑定；
- 带 `trigger_id` 的手动 `RunAgent` 不加入该 trigger 的 sticky sandbox。

没有受信头的执行（无人值守，以及入口未注入身份的手动触发）不受影响，继续按配置的
策略复用 sticky sandbox。由于不带身份的执行与带身份的执行不再落在同一个 sandbox，
手动触发不会销毁或污染定时任务积累的 sandbox。

## 入口一览

| 入口 | 发起方 | 执行看到的用户身份 | Sandbox |
| --- | --- | --- | --- |
| `RunAgent`、`StreamAgentRun`、`StartAgentRun`、`AttachAgentRun` | 外部 | 请求头 | 按请求 |
| 带 `trigger_id` 的手动 `RunAgent` | 外部 | 请求头 | 有身份时不用 sticky |
| `RunScheduler`、`StartSchedulerRun` | 外部 | 请求头 | 有身份时不用 sticky |
| `InvokeScheduler` | 外部 | 请求头 | 有身份时不用 sticky |
| `SandboxService.ResumeSandbox` | 外部 | 不绑定，恢复后为空 | 已有 sandbox |
| timer 触发 | 内部 | 无 | 按配置 |
| event / webhook 触发 | 内部 | 无 | 按配置 |
| `Exec` 系列、`GetSandboxProxy`、Jupyter 路由 | 外部 | 沿用 sandbox 当前绑定 | 已有 sandbox |

Webhook 的请求来自外部，但它只是投递一个事件，由事件触发的运行没有发起用户，不继承
投递请求上的任何头。

## 安全与兼容

- **不提升权限。** 没有任何路径会让一次运行获得发起者以外的身份。
- **不可伪造。** Guest 提供的 `x-octobus-ext-*` 在转发前全部删除，只写入绑定中的值。
- **无人值守的行为不变。** timer 与 event 触发的运行此前没有身份，现在仍然没有。
- **行为变化。** 以下三点与此前不同：
  - 手动触发 scheduler 的运行现在带发起者的身份（仅在请求带有受信头时）；
  - 这类运行不再复用 sticky sandbox，每次使用新的 sandbox（仅在请求带有受信头时）；
  - 进入一个已经在运行的 sandbox 时，绑定会被替换而不是沿用：有身份的执行写入本次执行的身份，
    无身份的执行清空上一个使用者留下的绑定。这一条与本次执行有没有身份无关，对脚本的 `agent`、
    `command` 与 `sandbox.resume` 都成立。受影响的典型场景是保留运行中的 sandbox，之后被另一次
    执行进入（见下文“仍然存在的边界”）。

### 公开手册只描述未注入身份的部署

`docs/pages` 中的 `sandbox_policy` 说明（`agent-compose-yaml-manual.md`）描述的是没有注入身份的部署，
那种部署里 `sticky` 的语义没有变化。`x-mpi-*` 是部署的受信入口与 AC 之间的内部契约，不是公开配置项，
所以公开手册不描述本文的身份豁免。若受信头将来成为公开契约，需要同时更新
`docs/pages/agent-compose-yaml-manual.md` 与 `docs/pages/zh-CN/agent-compose-yaml-manual.md`。

## 仍然存在的边界

- **`CallSandboxRPC` 由脚本自行管理 sandbox。** 脚本可以用它进入任意已有的 sandbox，
  AC 会把绑定替换成本次执行的身份，但不会阻止带身份的执行进入一个长期存在的 sandbox。
- **同一 sandbox 上的并发运行。** 绑定按 sandbox 后写覆盖，两个不同身份的运行同时使用
  一个 sandbox 时，以后进入者为准。
- **运行结束后的绑定。** 不被清理的 sandbox（例如显式指定 `sandbox_id` 复用的会话）在
  运行结束后仍保留最后一次的绑定，`Exec` 等接口沿用它，直到下一次运行重新绑定或
  sandbox 停止。
- **daemon 重启。** 重启后绑定以空身份重建，进行中的运行会丢失身份。

这些边界的共同原因是身份绑定在 sandbox 上，而不是绑定在一次运行上。彻底的解决方式是
为每次运行签发只在该运行内有效的 capability token，使身份跟随运行。这是独立的改造，
不在本文范围内。
