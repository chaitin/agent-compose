# Kubernetes Pod 运行时驱动 —— 设计与现状

Agent-Compose 作为集群中执行引擎的角色。

## 1. 目标与范围

Kubernetes 运行时驱动把一个 Agent-Compose sandbox 作为一个 Kubernetes Pod 来运
行，跟 docker/boxlite/microsandbox 驱动处在同一层抽象，只是换了执行后端：

1. 调用方（CLI 的 `agent-compose run`、Run API，或一次 scheduler 触发）发起一次
   agent 执行。
2. 部署在目标集群里的 Agent-Compose daemon 创建一个 Pod，运行这个 agent。
3. daemon 暴露这次执行的状态和结果，调用方按现有方式查询（`agent-compose logs`/
   `ps`/`scheduler runs` 等）。

按任务指定镜像、受控的 runtime profile、完成结果的可靠投递、重启后恢复、节点能力
上报（用于亲和性调度）——这些需求本文档记录为后续工作，不属于当前这一版 Pod 生命
周期实现的范围。

## 2. V1 架构

### 2.1 部署拓扑

使用 `driver.k8s` 的 daemon 不要求运行在目标集群内部：驱动用 kubeconfig 构建 client
（`K8S_KUBECONFIG`、`KUBECONFIG`、`~/.kube/config`，可用 `driver.k8s.context` /
`namespace` 覆盖），只有拿不到 kubeconfig 时才回退到 Pod 内的 ServiceAccount 配置。
V1 推荐的默认拓扑是 daemon 与 sandbox Pod 在同一个集群内：

```text
调用方（CLI / Run API / Scheduler）
          |
          v
   Kubernetes Service
          |
          v
Agent-Compose daemon（1 副本） ---- 创建/exec/删除 ----> Sandbox Pod
          ^                                                |
          |                                                |
          +------------------ 运行时 LLM 回调 ---------------+
```

这个拓扑不需要额外的网络打通或共享的宿主机存储，就能提供两个特性：

- sandbox 的回调可以通过集群内 Service DNS 直接连到 daemon；
- daemon 可以把自己的状态持久化在集群内的 PVC 上。

部署模型是**一个集群一个 daemon，V1 不支持一个 daemon 管理多个集群**。原因不只是
"没必要"：sandbox 必须能回调到 daemon 可达的地址（`K8S_RUNTIME_BASE_URL`），跨集群
时这个地址要在每个集群里分别打通一次，guest → daemon 的回调问题也就在每个集群原样
复现，等于又要在那个集群单独解决一遍——不比直接部署第二个 daemon 省事。
`context`/`namespace` 覆盖字段作为代码保留（同一集群内切换 namespace 仍然有效、
有用），但跨集群不是这个驱动要支持的能力。

把 daemon 部署在集群外，再通过 VPN、ingress 或导出宿主机文件系统的方式打通回调路径
——驱动允许这种拓扑（kubeconfig 优先就包含了集群外场景），但需要自行保证
`K8S_RUNTIME_BASE_URL` 对 sandbox Pod 可达，因此它不是主设计推荐的部署方式。Helm
Chart 是**受支持的安装入口**，而不是驱动的硬性要求。

### 2.2 Pod 模型与调度

V1 里一个 sandbox 对应一个 Pod、一个 agent 容器。不支持 sidecar，也不支持多容器
sandbox。

驱动默认不指定 Kubernetes 节点，Pod 的调度完全交给 Kubernetes 自己。以后的
runtime profile 可能会加上由管理员控制的 `nodeSelector`、affinity、tolerations；
但 daemon 自身的部署拓扑不应该被当成一种隐式的调度手段来用。

### 2.3 存储模型

daemon 的持久化数据和 sandbox 的文件，归属完全不同：

```text
Daemon PVC（RWO）
  - SQLite 以及 scheduler/run 状态
  - 从 sandbox Pod 拉取回来的 artifact

Sandbox Pod 的临时文件系统
  - daemon 推送过去的 prompt、配置、skills、workspace
  - 需要时由 daemon 拉取的 provider 状态和 artifact
  - stdout/stderr 通过 Kubernetes Exec 实时流式传输
```

daemon 以 `replicas: 1` 运行，挂载一个 PVC 作为自己的数据根目录。RWO 就够用，因为
现在的 SQLite 和 scheduler 模型本来就假定只有一个 daemon 进程持有状态。

（1）push/pull-over-Exec 已经
覆盖了目前梳理出的全部访问模式（见下），没有共享挂载的必要；（2）真要共享，daemon
自己的 PVC 就得从 RWO 升级成 RWX，重新引入 §2.1 本想绕开的存储后端选型问题（RWX
不是每个集群都有，k3d 自带的 `local-path-provisioner` 就不支持）；（3）会把 daemon
自己的状态和多个 sandbox 的并发写入混进同一个卷里，是跟具名 volume/bind mount
（本节最后一段）同一类"并发共享写入"复杂度，没必要为了省事把它引到 daemon 自己的
存储上。

取而代之，文件跨越驱动边界的方式是显式的：

- daemon → guest：在 guest 命令用到这些内容之前，把文件或目录打包推送过去；
- guest → daemon：在产生该内容的 `Exec` 调用一返回，就立刻把关联的 artifact 拉
  回来；
- 实时输出：通过 `ExecStream` 捕获 stdout/stderr；
- 生命周期内的 artifact：在 daemon 主动删除 Pod 之前，尽力做一次收尾拉取。

这样就不需要为 sandbox 的日常运行准备 RWX 存储，文件的归属和同步时机也都变得明确。

如果 Pod 在被拉取之前就消失了（比如节点故障或被驱逐），Pod 本地的数据仍然可能丢
失。状态监听 + 终态收尾拉取可以降低这个风险，但硬性的节点故障没法保住临时数据。
V1 接受这个限制。

顶层的具名 volume 和 bind mount 都是设计给多个 sandbox 并发共享的，但在 k8s 下能
不能支持，结论完全不同：

- **具名 volume（`type: volume`）：已支持，映射到 PVC。** 现有 YAML 的顶层
  `volumes.<name>.driver: k8s` 选择 PVC driver，agent 的 mount 继续使用现有
  `type: volume`/`source`/`target`/`read_only` 字段。`VolumeRecord.Options` 支持
  `size`（默认 `1Gi`）、`storage_class`、`access_mode`（默认 `ReadWriteOnce`）、
  `namespace`（默认 daemon 的 `K8S_NAMESPACE`/`default`）；PVC 使用 daemon 所在的
  Kubernetes 集群，名称由 volume ID 稳定生成。PVC 与 Pod 必须在同一 namespace，
  否则 sandbox 创建失败。
  不做单独的 RWX 能力探测：如果集群不支持指定的 access mode，PVC 会保持 Pending，
  由既有 sandbox 启动超时失败。
- **bind mount（`type: bind`）：继续拒绝，且没有计划支持。** 不是"还没做"，是架构
  上做不到——它的语义是"daemon 所在机器上一个已经存在的具体路径"，而 daemon 和
  sandbox Pod 现在可能分布在集群里任意不同节点上，没有共享文件系统这个前提本身就
  是这份设计的出发点（§2.1）。唯一能让它工作的办法（把该路径通过 NFS 导出成网络存
  储，或者用 `hostPath`/`local` PV 把 sandbox Pod 强行钉在 daemon 所在节点）都已经
  在更早的架构选型里被否决过，不会重新引入。

## 3. 配置

```yaml
agents:
  worker:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s:
        namespace: agent-compose
```

两个字段都是可选的覆盖项：

- `namespace`：`driver.k8s.namespace` -> `K8S_NAMESPACE` -> `default`。同一集群
  内切换 namespace，实际有效，是常用的覆盖项。
- `context`：`driver.k8s.context` -> kubeconfig 的 `current-context`，如果没配
  kubeconfig 就用 in-cluster 的 ServiceAccount 配置。**在主推荐的 in-cluster 部
  署下通常不要设置这个字段**——daemon 一般没有 kubeconfig 文件，只有 in-cluster
  ServiceAccount 配置，这种情况下不管填什么 context 名字都会静默落回 daemon 自己
  所在的集群（见 §2.1）。字段本身作为代码保留，只有在额外给 daemon 挂了一份带多
  个具名 context 的 kubeconfig 时才真正生效——而这种用法本身不是 V1 支持的部署方
  式（同样见 §2.1）。

目前还没加一个显式的、daemon 全局的 `K8S_CONTEXT` 默认值。既然 `context` 本身在
主部署模式下就不建议用，这个默认值也不必补。

可选的 `K8S_RUNTIME_BASE_URL` 会覆盖 k8s sandbox 容器里 daemon 全局的
`AGENT_COMPOSE_RUNTIME_BASE_URL`。正常情况下应该直接把 daemon 的 Service DNS 名
当作 daemon 全局的值来用，这样就不需要这个驱动专属的覆盖了。

逐 agent 的 k8s 设置沿着现有的配置路径传递：

```text
compose driver spec
  -> protobuf DriverSpec
  -> AgentDefinition.ConfigJSON
  -> CreateSandboxOptions
  -> 持久化的 VMState
  -> k8sRuntime
```

选定的 context 和 namespace 会持久化在 sandbox 状态里，这样后续对该 Pod 的操作
才能用跟创建时一致的目标。Kubernetes client 按 context 缓存，namespace 则是每次
sandbox 操作时单独解析。

## 4. Guest 文件传输

k8s 运行时把 guest 文件相关的能力做成可选接口，而不是把只有一个后端会用到的方法
加进核心的 `SandboxRuntime` 接口：

- `GuestFileReader` 和 `GuestDirReader`；
- `GuestFileWriter` 和 `GuestDirWriter`。

调用方拿到某个 session 对应的 runtime 后，在这些能力可用时才使用它们。Docker、
BoxLite、Microsandbox 继续用它们原有的共享文件系统行为。

读取：单个文件用 `cat`，目录通过 Kubernetes Exec 用 `tar`。目录解包时会拒绝路径
穿越。写入使用 Kubernetes Exec 子资源的 stdin：单文件直接流式写入，目录则边打包
tar 边传输，不经过命令行参数，也不会把整个 workspace archive 留在 daemon 堆内存
里。这个 stdin 通道是 k8s 驱动内部的文件传输边界，不扩张通用的 `ExecSpec`。

`WriteGuestFile`/`WriteGuestDir` 在推送前会先调用 `EnsureSandbox`。这是必要的，
因为 sandbox 环境准备这一步发生在正常的 runtime 启动调用之前；`EnsureSandbox` 让
这两条路径的创建操作都变成幂等的。

### 4.1 已接入的写入路径

daemon 目前会把下面这些内容推送进 k8s sandbox：

- agent 的 prompt、system prompt、output schema；
- agent 的 skills；
- agent 的 MCP 配置；
- Codex 和 OpenCode 的 MCP 配置；
- 交互式 prompt-attach 路径写入的等价文件。
- Jupyter cell 的 shell/Python/JavaScript 脚本；
- scheduler command 和非交互 project run command 的
  `command-request.json`。

本地副本作为 daemon 一侧的记录会继续保留。skills 是作为目录拷贝推送到 guest 的
agent 和 Claude skill 位置的；目录推送会先替换目标目录，所以 sandbox 准备重试时
不会保留上一次同步留下的陈旧文件。

环境准备完成后，daemon 还会把 provision 好的 workspace 和 sandbox home 快照同步
到 guest。后者覆盖各 provider 写在 home 下的 runtime config；长期上游凭证仍然不
会复制进 guest，而是继续通过现有 runtime facade 的短期 token 边界访问。

### 4.2 已接入的读取路径

执行结束后可以从 k8s guest 读取 agent 的 resume 状态。解析出的 thread ID 已经足
以支持功能性的 resume。provider 的日志路径和 thread-state 路径在 k8s 上不会上
报，因为那样会指向根本不存在的 daemon 本地路径。

Scheduler command 和非交互 project run command 在 `ExecStream` 返回后会立即把
对应的 guest artifact 目录拉回 daemon，包括 guest 生成的
`command-result.json`、stdout/stderr/output 以及 runtime SDK 写入该目录的其他
artifact。拉取失败会使本次 command 执行失败，不会静默返回一个缺少 artifact 的
成功结果。

普通 Jupyter cell 的 stdout/stderr/output 本来就由 daemon 从 Exec 结果和实时流
生成，source script 也先写在 daemon 一侧，因此不另做一次 guest 目录拉取；agent
cell 同理，额外的 resume 状态按上一段所述单文件拉取。

以下读取路径还没做：

- Pod 删除前的生命周期 artifact 收尾拉取。

### 4.3 `agent-compose exec`

`agent-compose exec` 命令目前对 k8s sandbox 完全不能用。它不是直接调用驱动底层
的 `Exec`：它实现的是一套双向的文件 RPC——daemon 写一个请求文件，guest 里的一个
runtime helper 写一个响应文件回去。

要支持它，需要在两个方向之间做一次单独的决策：

1. 保留这套文件 RPC，改成推送请求、拉取响应；
2. 在 k8s 上把这个命令改成直接走驱动底层的 `Exec`。

这个应该被明确地单独设计，而不是藏在那几个单向 artifact 读取 helper 背后顺带解
决。

## 5. 实现现状

| 能力 | 状态 | 备注 |
| --- | --- | --- |
| Compose/protobuf/API schema | 已实现 | `driver.k8s.context` 和 `namespace` 在项目配置里完整走通 |
| 逐 sandbox 的 context 和 namespace | 已实现 | 持久化在 VM state 里；client 按 context 缓存 |
| Pod 创建/删除/exec | 已实现 | 每个 sandbox 一个 Pod、一个容器 |
| Sandbox 的 `hostPath` 挂载 | 已移除 | Pod 创建时刻意不设置任何 sandbox volume 或 volume mount |
| Guest 文件推送原语 | 已实现 | 文件通过 stdin 写入；目录使用流式 tar，不受 argv 长度限制 |
| Guest 文件拉取原语 | 已实现 | 文件和目录读取，带安全的 tar 解包 |
| Agent 输入推送调用点 | 已实现 | prompt/config/skills 逐项推送；workspace 和 sandbox home 在环境准备后整体同步 |
| Agent resume 拉取 | 已实现 | thread ID 恢复功能可用 |
| Cell 和 run 的 artifact 同步 | 已实现 | cell 脚本先推送；scheduler/project command 的 guest artifact 在 Exec 返回后立即拉取 |
| 具名 volume 和 bind mount 处理 | 部分实现 | `driver: k8s` 的具名 volume 映射为 PVC 并挂入 Pod；bind mount 永久拒绝 |
| `agent-compose exec` | 不支持 | 需要设计双向 RPC 方案 |
| 删除前的 artifact 收尾拉取 | 未实现 | 尽力而为；绝不能阻塞 Pod 删除 |
| In-cluster 部署清单 | 已实现 | `charts/agent-compose` Helm chart 提供 Deployment、Service、PVC 和集群级 RBAC |
| Runtime profile | 未实现 | 安全上下文、资源、调度相关的控制项 |
| 按任务的镜像/profile 覆盖 | 未实现 | 需要改 Run API、CLI、run 解析逻辑 |
| 完成结果重试与重启恢复 | 未实现 | agent-compose 共享能力，非 k8s 驱动专属 |
| 节点能力上报（用于亲和性调度） | 未实现 | agent-compose 共享能力，非 k8s 驱动专属 |

当前实现已经用真实 LLM 凭证在一个真实的 k3d 集群上跑通过一次完整的 agent run。
那次测试验证了 kubeconfig 的兜底逻辑、Pod 调度、guest 输入推送、Kubernetes
Exec，以及响应路径本身；也确认了上面提到的 `agent-compose exec` 不可用这个问
题。

## 6. 所需的部署资源

要支持这套设计，部署时必须提供：

- 单副本的 daemon `Deployment`；
- 挂载在 daemon 数据根目录上的一个 PVC；
- 一个同时承载 daemon API 和 sandbox 运行时回调的 `Service`；
- 一个专用的 `ServiceAccount`，配上 `ClusterRole` 和 `ClusterRoleBinding`，只给
  驱动所需的最小 Pod 和 `pods/exec` 权限。必须是**集群级别**，不能是
  namespace 级别：`driver.k8s.namespace`（见 §3）允许逐 agent 覆盖成 daemon 自
  己所在 namespace 以外的目标，如果用 namespace 级别的 `Role`/`RoleBinding`，
  这个覆盖在没建过对应 Role 的 namespace 上会静默失效。

这些资源已经由 `charts/agent-compose` Helm chart 提供，并通过 `helm lint`/
`helm template` 静态契约测试验证。Chart 只覆盖 in-cluster 拓扑，不包含已经被
否决的 VPN 部署方案。

## 7. 后续优先级

1. 决定并实现 `agent-compose exec` 在 k8s 上的行为。
2. 加上尽力而为的终态/删除前 artifact 收尾拉取。
3. 为安全上下文、资源、Kubernetes 调度约束设计管理员可控的 runtime profile。
4. 为镜像、拉取策略、runtime profile 加上 Run API 层面的覆盖能力。
5. 把可靠的完成结果投递、幂等重试、重启恢复、节点能力上报，作为 agent-compose
   共享的能力来解决，不是 k8s 驱动专属的问题。
6. 为 PVC volume 增加跨 namespace/context 的显式绑定策略（当前要求 PVC namespace
   与 Pod namespace 相同；跨 namespace 不支持）。`type: bind` 保持永久拒绝，不在此列。
