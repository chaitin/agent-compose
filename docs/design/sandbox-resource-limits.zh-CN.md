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
- 运营方可以给声明设上限，防止项目申请过大的资源。

非目标：

- 磁盘配额：Docker 和 K8s 的机制差异大，另行设计。
- request 与 limit 分离、CPU shares、PidsLimit。
- 对已存在 runtime 的在线调整（`docker update` 等）。
- 在 v2 API 的 `SandboxSpec` proto 里暴露该字段：proto 在独立模块，需要单独发版。

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
64Mi 到 1024Gi；Microsandbox 不接受小数 `cpus`。

## 硬性要求：为什么不提供 require 开关

声明本身就是硬性要求，没有 `required` / `best_effort` 选项：

1. 资源限制保护的是宿主机和其他租户，不是声明它的 agent。写 YAML 的人没有动机需要"限制不生效也照跑"，
   需要放宽的只会是运营方。
2. 四个 driver 都有限制的配置入口，"driver 做不到"在静态层面不存在。剩下的失败都发生在运行时
   （旧 sandbox-host、Docker 静默丢弃限制），此时继续运行只会让声明变成谎言。
3. 真有放宽的需求时，应参照 `EGRESS_UNENFORCEABLE_POLICY` 做成部署级开关，不放进项目 YAML。

## 执行路径

```
agent spec JSON ──ResolveResourceLimits──▶ driverSandboxResourceLimits.resolve
  (pkg/runs)                                 (pkg/agentcompose/adapters)
                                             · SANDBOX_MAX_* 上限
                                             · driver.RequireSandboxResourceLimits
                                               （读能力矩阵的静态事实 + microVM 约束）
                                                        │
                                               driver.Sandbox.ResourceLimits
                    ┌──────────────┬───────────────┬────┴─────────────────┐
                 Docker           K8s        sandbox-host (proto v3)
          HostConfig 写入 +    requests =     BoxLite / Microsandbox
          创建后回读校验         limits         VM 规格
```

- 声明和网络策略走同一个 `driverRuntimeAdapter.driverSandbox` 接缝，所以任何会让 driver 创建
  runtime 的路径（包括 guest 文件操作隐式 ensure）都会带上它。每个 adapter 绑定自己的 driver 名，
  不依赖 sandbox 上可能为空的 driver 字段。
- 拒绝统一归类为 `ErrFailedPrecondition`，driver 侧哨兵是 `ErrSandboxResourceLimitsUnavailable`。
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

声明的维度覆盖 daemon 默认值，未声明的维度保持默认值。BoxLite v0.13 的 CPU 参数是 double，
小数原样传入；Microsandbox 只接受整数 vCPU，小数直接拒绝而不是向上取整（向上取整等于给了比声明
更多的 CPU）。VM 最多 255 vCPU。

VM driver 跑在 sandbox-host 里，`Sandbox` 经 gRPC 传递，因此 `sandboxhost.Sandbox` 新增
`resource_limits` 字段，协议版本从 2 升到 3。旧 host 会丢掉未知字段、按自己的默认值启动 VM，
所以 daemon 对声明了限制的 sandbox 检查协议版本，低于 3 直接拒绝（与 deny 规则的 v2 门禁同一模式）。
按 N/N-1 兼容窗口，daemon 不再接受协议版本 1 的 host。

## 部署上限

`SANDBOX_MAX_CPUS`（核数，允许小数）和 `SANDBOX_MAX_MEMORY_MIB`。不设置表示没有上限；格式错误时
daemon 启动失败，避免上限被静默取消。上限只约束声明，不约束 microVM 的默认值。

## 生效时机与已知限制

- 每次 ensure 都会重新读取声明，但只在创建 runtime 时应用。`stopped_runtime_policy: retain` 的
  runtime 被 resume 时保持创建时的限制。
- microVM 的内存是整个 guest 的内存，guest 内核和 runtime 也会占用。
- Docker 和 K8s 的限制同样覆盖 sandbox 内的 agent runtime，OOM 时 runtime 会一起被杀。目前不会把
  OOMKilled 映射成单独的失败原因，这是后续工作。
- BoxLite 的 cgo 路径只能在带 KVM 和 libboxlite 的环境里编译和冒烟，单元测试覆盖的是共享的
  `microVMSandboxResources`。
