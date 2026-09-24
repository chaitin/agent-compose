# Agent 超时与资源治理设计

## 目标

agent-compose 不应使用一个固定时长去判断所有业务 agent 是否“应该结束”。业务复杂度、网络条件和模型响应时间不可预知；平台真正需要保证的是资源可回收、运行可观测、坏死任务最终不会无限占用资源。

因此本设计只向用户暴露一个主要概念：`timeout`。它表示调用方对任务时长的期望，是可被平台授予和续期的软租约，不是无条件的业务失败时间。平台内部始终保留不可绕过的资源安全边界。

## 对外契约

用户配置保持最小化：

```yaml
agent:
  timeout: 10m
```

未设置时使用 `auto` 策略。用户不需要配置 `keepAlive`、优先级、CPU、内存、心跳间隔、TERM/KILL 宽限期或采集器参数。

agent 可以在发现任务需要更长时间时请求续租，例如 `extendLease()`。续租由平台根据当前活动状态、资源余量和硬上限决定；agent 只能获得平台批准的剩余租约，不能关闭或突破平台保护。

现有 `timeoutMs` 参数保持兼容，但语义统一为“期望的 agent/run 租约时长”。对外返回的 timeout 错误应包含稳定错误码和实际批准时长；内部记录 configured timeout、effective timeout、deadline 和终止结果。

## 控制边界

### 平台必须控制

- TCP/HTTP/RPC 建连、response header 和单次 API 调用；
- Docker、BoxLite、Microsandbox 的创建、停止和销毁；
- 镜像拉取、Jupyter readiness、数据库和文件锁等待；
- CPU、内存、磁盘、进程数、并发 execution 和 sandbox 数量；
- provider/runtime 不响应取消时的进程树、容器或 VM 强制回收；
- daemon shutdown 和最终 cleanup。

这些是控制面和资源安全边界，不能交给 agent 决定。

### agent/workflow 可以控制

- 单次工具或模型调用的业务等待时间；
- 重试、退避和任务阶段的时间分配；
- 是否继续探索、换方案或主动放弃；
- 在有实际进展时请求额外租约。

agent 的选择始终受平台资源配额和 hard ceiling 约束。

## 租约模型

平台不以“开始时间 + 固定 N 分钟”作为唯一控制方式，而是授予一个初始租约：

```text
requested timeout
  -> effective lease = min(requested, platform ceiling, remaining run budget)
```

租约可以在运行过程中续期，但续期必须满足：

- agent 仍有可验证的活动或阶段变化；
- provider/runtime 可响应取消；
- 未超过资源配额和绝对 hard ceiling；
- 当前资源池允许继续占用。

续租失败应返回明确结果，agent 可以降级、保存中间结果或退出。没有续租调用的旧 agent 仍按默认策略运行。

## 可靠且高效的采集

采集目标不是建立完整 APM，而是判断“活着、可控、没有明显失控”。第一版只采集已有边界可以可靠获得的信号：

1. sandbox/container/VM 是否仍存在；
2. runtime/provider 主进程是否存在；
3. cgroup 或 driver 提供的 CPU 时间、RSS 内存、进程数；
4. 最近一次协议事件、工具调用、命令状态变化或输出时间；
5. cancel 请求时间以及进程/driver 是否确认终止。

优先使用 cgroup v2、Docker/BoxLite/Microsandbox 原生统计和 `/proc` 快照；避免在 agent 内注入高频采集器、解析全部日志或依赖不可靠的自报资源值。采集应由宿主 watchdog 低频采样，并在执行边界复用已存在的 event/stream/cell 状态。

平台不应把“没有 stdout”单独视为坏死。合法的编译、下载、测试和模型思考可能长时间无输出；坏死判断需要结合协议事件、命令状态、CPU/IO 活动、取消响应和进程变化。

## 自动坏死处理

平台内部将任务分为 `ACTIVE`、`IDLE`、`STUCK`：

- `ACTIVE`：有协议事件、工具/命令状态变化或可观测资源活动，正常运行；
- `IDLE`：一段时间没有有效事件，先记录诊断并向 runtime 发送状态询问，不立即终止；
- `STUCK`：长时间无有效活动、资源明显失控或取消不响应，进入强制回收流程。

强制回收流程必须有确定的终止上界：

```text
soft cancel/TERM
  -> termination grace
  -> driver/runtime KILL 或销毁 sandbox
  -> 确认资源释放
  -> 写入 agent/cell/run terminal 状态
```

hard ceiling 是最后的安全保险丝，不应成为正常业务控制手段。它用于 provider 卡死、进程泄漏、监控失效和管理员关闭等异常情况。

## 现有 timeout 的归类

| 机制 | 角色 |
| --- | --- |
| HTTP/Connect client timeout | 客户端等待边界；不自动等价于服务端取消 |
| `AGENT_TIMEOUT` | agent 初始租约/兼容配置，不应是唯一资源保护 |
| scheduler run timeout | run 租约和编排边界 |
| `req.timeout_ms` | 单次 command/exec 操作边界 |
| sandbox start/stop/graceful-stop | 平台生命周期边界 |
| `IMAGE_PULL_TIMEOUT` | 外部资源操作边界 |
| `JUPYTER_READY_TIMEOUT` | readiness 边界 |
| daemon shutdown timeout | 进程退出和后台组件收敛边界 |
| runtime workflow/agent timeout | 业务层软租约，可被续期 |

这些值应共享同一个绝对 deadline 或剩余预算，不能在子层反复创建互相独立的完整 timeout。

## 配置与可解释性

对普通用户只解释三句话：

1. `timeout` 是对任务时长的期望；
2. 任务有进展时平台可能自动延长租约；
3. 平台在检测到坏死或资源失控时会强制终止。

资源配额、采样周期、idle 阈值、grace period 和 hard ceiling 属于平台默认策略，不作为第一版公开配置。管理员如需调节，使用少量 profile 或部署级配置，而不是为每个 agent 暴露几十个参数。

## 实施顺序

1. 在 `AgentExecutor`/driver 边界增加统一 watchdog、取消后 grace 和强制回收确认；
2. 复用现有 cell、event、stream 和 sandbox 状态记录活动时间与终止状态；
3. 统一 absolute deadline、错误码和 terminal 状态；
4. 增加可选的 `extendLease()`，默认不要求 agent 修改；
5. 最后再根据 driver 能力补充 cgroup/进程数等资源配额。

该顺序先解决“超时后资源是否真的停止”的安全问题，再逐步增加自适应能力，不要求一次性建设复杂资源调度平台。

## 设计结论

固定时间不是业务正确性的可靠判据，但没有任何最终边界也不符合平台资源安全要求。agent-compose 应采用：

```text
简单 timeout（用户意图）
  + 后台自动活动检测
  + 资源配额
  + 可选租约续期
  + soft cancel -> hard cleanup
  + 少量不可绕过的 hard ceiling
```

agent 决定任务是否值得继续；平台决定它是否仍有资格占用资源。
