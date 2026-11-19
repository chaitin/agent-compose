# Sandbox 资源限制（policy.resources）

## 背景

在本设计之前，sandbox 的 CPU、内存只有 daemon 级的配置，而且只对 microVM 生效：

- `SANDBOX_CPUS`（默认 4）、`SANDBOX_MEMORY_MIB`（默认 4096）、`SANDBOX_DISK_SIZE_GB`（默认 6）
  由 `pkg/config` 读取，BoxLite 和 Microsandbox 通过 `configuredSandboxResources` 用它们决定 VM 规格。
- Docker driver 的 `dockerSandboxHostConfig` 不设置 `Memory`、`MemorySwap`、`NanoCPUs`、`PidsLimit`，
  能力矩阵把 Docker 的 `resource_limits` 标成 `not_configured`。一个 sandbox 可以吃满宿主机。
- K8s driver 创建的 Pod 不带 `resources.requests/limits`，同样是 `not_configured`。
- compose 里没有按 agent 声明资源的入口。
- `capmatrix` 已有 required / `best_effort:` 两档的 isolation requirement 解析和启动闸门
  （`enforceIsolationPreflight`），但 compose 从不产出 requirement，闸门实际是空操作。

## 目标与非目标

目标：

- 按 agent 声明 CPU 和内存上限，四个 driver 都真正生效。
- 不声明时行为与之前逐字节一致，包括 spec hash。
- 声明了但无法生效时必须失败，不能静默不受限地运行。

非目标：

- 磁盘配额：Docker 和 K8s 的机制差异大，另行设计。
- request 与 limit 分离、CPU shares、PidsLimit。
- 对已存在 runtime 的在线调整（`docker update` 等）。
- 部署级或按租户的资源配额，原因见"声明优先于默认值"。
- 自动重建限制不一致的已有 runtime：会丢掉可写状态，见"复用已有 runtime"。

## 声明

放在 agent 的 `policy` 块（也可以放在 `policies` 库里按名引用）：

```yaml
policy:
  resources:
    cpus: 0.5      # 核数，精度 0.001
    memory: 1Gi    # 必须带 Mi / Gi
```

规范化结果是 `{cpu_millis, memory_mib}`，带 `omitempty`，未声明时 canonical JSON 和 spec hash 不变。
apply 阶段的校验：空块报错；`cpus` 必须 > 0，最多 1024 核，精度不超过 0.001；`memory` 范围是
64Mi 到 1024Gi；BoxLite 和 Microsandbox 不接受小数 `cpus`，也不接受超过 255 的 `cpus`。

### 经 v2 API 传递

`ac up` 先对本地规范化后的 spec 算 hash，再把 proto 形式的 spec 和这个 hash 一起发给 daemon；
daemon 把 proto 渲染回 YAML（`ProjectSpecYAMLShape`）、重新规范化，再比较 hash。所以 resources
必须在 proto 里走一个来回而不丢失：`SandboxSpec` 新增字段 3 `SandboxResourcesSpec`，携带作者写的
形式（`optional double cpus` + `string memory`），由 daemon 重新规范化，单位和校验只定义在 compose 一处。
`SandboxResourcesSpecToProto` 把 `cpu_millis` 还原成十进制核数、把 `memory_mib` 还原成 `Mi`/`Gi`，
两者都能无损地规范化回原值。proto 是根模块用 `replace ./proto` 引用的本地模块，契约和实现在同一个
PR 里改。`TestIntegrationApplyCarriesPolicyResources` 走完 client hash → ApplyProject → 落库 →
`ResolveResourceLimits` 整条链路。

新客户端对接旧 daemon 时，旧 daemon 不认识字段 3，重新规范化后的 hash 和客户端不一致，apply 以
`submitted_spec_hash` 校验失败被拒，不会静默丢掉声明。

## 硬性要求：为什么不提供 require 开关

声明本身就是硬性要求，没有 `required` / `best_effort` 选项：

1. 资源限制保护的是宿主机和其他租户，不是声明它的 agent。写 YAML 的人没有动机需要"限制不生效也照跑"，
   需要放宽的只会是运营方。
2. 四个 driver 都有限制的配置入口，"driver 做不到"在静态层面不存在。剩下的失败都发生在运行时
   （旧 sandbox-host、Docker 静默丢弃限制、复用了限制不一致的已有 runtime），此时继续运行只会让
   声明变成谎言。
3. 真有放宽的需求时，应参照 `EGRESS_UNENFORCEABLE_POLICY` 做成部署级开关，不放进项目 YAML。

## 执行路径

```
agent spec JSON ──ResolveResourceLimits──▶ driverSandboxResourceLimits.resolve
  (pkg/runs)                                 (pkg/agentcompose/adapters)
                                             · driver.RequireSandboxResourceLimits
                                               （读能力矩阵的静态事实 + microVM 约束）
                                                        │
                                               driver.Sandbox.ResourceLimits
                    ┌──────────────┬───────────────┬────┴─────────────────┐
                 Docker           K8s        sandbox-host (resource_limits 能力)
          HostConfig 写入 +    requests =     BoxLite / Microsandbox
          创建后回读校验         limits         VM 规格
```

- 声明和网络策略走同一个 `driverRuntimeAdapter.driverSandbox` 接缝，所以任何会让 driver 创建
  runtime 的路径（包括 guest 文件操作隐式 ensure）都会带上它。每个 adapter 绑定自己的 driver 名，
  不依赖 sandbox 上可能为空的 driver 字段。
- 拒绝统一归类为 `ErrFailedPrecondition`，driver 侧哨兵是 `ErrSandboxResourceLimitsUnavailable`。
  sandbox-host 里产生的拒绝以 gRPC FailedPrecondition 加 `ErrorInfo` reason
  `SANDBOX_RESOURCE_LIMITS_UNAVAILABLE` 传回，daemon 据此还原成同一个哨兵，不会和同样走
  FailedPrecondition 的 `ErrGuestRuntimeNotReady` 混淆。
- 声明和网络策略目前各读一次 agent 的 `SpecJSON`。两次读之间如果恰好有一次 apply，两者可能来自不同
  revision；下一次 ensure 会读到一致的结果。合并成一次读需要改网络策略那一侧（它还服务 forward
  proxy），留作后续重构。
- `RequireSandboxResourceLimits` 直接读 `RuntimeCapabilityFactsFor` 的 `resource_limits` 维度，
  启动闸门和 `EngineService.GetCapabilities` 不会不一致（有测试约束）。

### Docker

`NanoCPUs = cpu_millis × 10⁶`，`Memory = MemorySwap = memory_mib × 1Mi`（禁止使用 swap）。
Docker daemon 在缺少 cgroup memory 控制器的主机上会接受内存限制，只返回一条 warning，并把
`Memory` 重置为 0 后保存。所以创建后会 inspect 回读，不一致就删除容器并失败。已在 Docker Desktop
上验证：回读值一致，容器内 `memory.max` 生效。

### Kubernetes

容器 `resources.requests` 等于 `resources.limits`，只写声明了的维度。命名空间的 LimitRange 或
ResourceQuota 违规时由 API server 拒绝创建 Pod，错误照常返回。

### BoxLite / Microsandbox

声明的维度覆盖 daemon 默认值，未声明的维度保持默认值。两者都只接受整数 vCPU，小数直接拒绝，
VM 最多 255 vCPU。

Microsandbox 的 SDK 只接受整数。BoxLite v0.13 的 `boxlite_options_set_cpus` 虽然是 double，
但小数的实现是：guest 看到 ceil(n) 个 vCPU，再由宿主机 cgroup `cpu.max` 限额。jailer 开启时
（默认）`cpu.max` 写失败只记一条 error 日志并继续启动（`vmm/controller/spawn.rs` 的
`apply_cpu_quota`，注释写明是有意的 best-effort），也就是说没有 cgroup CPU 委派的宿主机会让
`cpus: 0.5` 的 sandbox 拿到完整 1 个 vCPU，daemon 看不到任何信号，违背"声明即生效"。向上取整同理
会给出比声明更多的 CPU，所以也不做。等 BoxLite 能把 quota 失败报出来，或我们能可靠回读 VM 的
cgroup 后，再放开小数。

VM driver 跑在 sandbox-host 里，`Sandbox` 经 gRPC 传递，因此 `sandboxhost.Sandbox` 新增
`resource_limits` 字段。旧 host 会丢掉未知字段、按自己的默认值启动 VM，所以 daemon 必须知道 host
认不认识这个字段。

没有用升协议版本的办法：daemon 只接受 N 和 N-1，如果 host 升到 3，还停在 2 的旧 daemon 会直接拒绝
这个 host，所有 VM driver 都不可用；而 sandbox-host 镜像是单独升级的，先升 host 是现实场景。
改为在 `HandshakeResponse` 新增 `repeated string features`，host 声明 `resource_limits`。旧 daemon
忽略这个字段，照常使用新 host；新 daemon 只在 host 声明了该能力时放行带资源声明的 sandbox，否则
以 `ErrSandboxResourceLimitsUnavailable` 拒绝。协议版本保持 2，接受的 host 版本范围不变。
daemon 和 host 可以按任意顺序升级：

| daemon | host | 结果 |
| --- | --- | --- |
| 旧 | 新 | 正常；旧 daemon 不会发资源声明 |
| 新 | 旧 | 正常；声明了资源的 microVM sandbox 被拒绝，提示升级 sandbox-host |
| 新 | 新 | 声明生效 |

## 复用已有 runtime

限制只在创建 runtime 时写入，运行中的容器、Pod、VM 改不了。但 ensure 经常复用已有 runtime：
`retain` 后 resume、runtime 仍在运行、升级前创建的 runtime、运行中修改了声明。不检查的话，这些
sandbox 会带着旧限制（或没有限制）启动，违背"声明即生效"。

所以四个 driver 在复用前都回读 runtime 实际的规格，和声明比较（`requireReusedRuntimeResourceLimits`）：

| Driver | 回读来源 |
| --- | --- |
| Docker | `HostConfig.NanoCPUs`、`Memory`、`MemorySwap`；`MemorySwap` 不等于 `Memory` 视为内存不受限 |
| Kubernetes | 沙箱容器的 `resources`；requests 不等于 limits 视为该维度不受限 |
| BoxLite | `CBoxInfo.cpus`、`memory_mib` |
| Microsandbox | `SandboxHandle.Config()` 的 `CPUs`、`MemoryMiB` |

只比较声明了的维度。不一致时以 `ErrSandboxResourceLimitsUnavailable`（FailedPrecondition）拒绝，
错误里写明 runtime、实际值和声明值，并提示删除 sandbox。读不到规格时，带声明的 sandbox 同样拒绝。

不自动重建：重建会丢掉 runtime 的可写层，对 `retain` 来说就是数据丢失；也不降级成 warning 继续跑，
那等于声明不生效。代价是修改已有 sandbox 的声明后，要手动删除 sandbox 再启动一次。删掉声明不会触发
拒绝，因为未声明的维度不比较。

## 声明优先于默认值

`SANDBOX_CPUS` / `SANDBOX_MEMORY_MIB` 只是 microVM 未声明时的默认规格。声明了就以声明为准，
可以比默认值大也可以比默认值小，只受 255 vCPU、1024Gi 这些硬性范围约束。不提供部署级上限：

1. Docker 不声明时本来就不限制，项目在 Docker 上已经能吃满宿主机。只在 microVM 上拦住
   "比默认值大"的声明，算不上真正的防护，反而让两类 driver 的规则不一致。
2. 如果复用 `SANDBOX_*` 当上限，想让某个 agent 用更大的 VM 就得把全局默认调大，所有未声明的
   agent 跟着变大。如果新增独立的 `SANDBOX_MAX_*`，它又约束不了默认值（默认 4096、上限 2048 时，
   声明 4Gi 被拒，不声明反而拿到 4096），语义自相矛盾。
3. K8s 有原生的 `ResourceQuota` / `LimitRange`，粒度比一个全局值细。

当前部署是写 YAML 的人和运营方同一拨人。真有多租户需求时，再按项目或租户单独设计配额。

## 生效时机与已知限制

- 每次 ensure 都会重新读取声明，但只在创建 runtime 时应用；复用已有 runtime 时的处理见上一节。
- microVM 的内存是整个 guest 的内存，guest 内核和 runtime 也会占用。
- Docker 和 K8s 的限制覆盖 sandbox 内所有进程，包括 agent runtime。超限时内核 OOM killer 按
  oom_score 在 cgroup 里挑一个进程杀掉，通常是占内存最多的，但也可能是 agent runtime。Docker 默认
  不设 `memory.oom.group`，所以一般只有该进程被 SIGKILL，命令以 137 退出，容器继续运行；K8s 的
  kubelet 在 cgroup v2 上开启整组 OOM 时整个容器被终止。run 只记录退出码和错误文本，目前看不出是
  OOM。后续工作：读 sandbox cgroup 的 `memory.events` 中 `oom_kill` 计数，在命令前后比较，把 OOM
  写进错误文本；独立的失败原因字段还需要改 v2 API proto。
- BoxLite 和 Microsandbox 的 cgo 路径在本地只做了类型检查（`go vet`，BoxLite 用 v0.13 头文件），
  没有在带 KVM 的环境里冒烟；单元测试覆盖的是共享的 `microVMSandboxResources` 和
  `requireReusedRuntimeResourceLimits`。
