# 独立 Sandbox Supervisor 隔离设计（设计稿，未实现）

> 状态：阶段 B 的 Docker MVP 已实现（`feat/ac-supervisor`），仅覆盖 egress 转发；
> 完整控制面迁移、supervisor 独立升级和 K8s 侧仍未实现。
>
> 目标：说明当前“单容器内 ac-init 既是 PID 1 又是控制面”的弱点和独立 supervisor 的目标形态。

---

## 1. 当前单容器模型的弱点

当前的 init 与 workload 在同一个容器、同一组 namespace、同一个用户和同一份 capability 集合中：

1. **控制面与 workload 同域。** init、控制 socket、workload 共享 mount/pid/net namespace。workload 若取得足够权限，可以直接干扰 init。
2. **控制 socket 无法被 Landlock 保护。** Linux Landlock 只约束文件系统访问和 TCP bind/connect，不约束 `connect(2)` 到 `AF_UNIX`。因此 Landlock 能拒绝 workload 打开 `/.agent-compose`（init binary/profile），却无法阻止它连接控制 socket。
3. **init 是唯一策略权威。** 一旦 workload 影响 init（ptrace、信号、共享文件、伪造 exec 请求），它就能改变之后所有 session 的启动方式和策略。当前 init 通过非 dumpable、`no_new_privs`、capability bounding-set drop 和 seccomp 降低风险，但这不是新的权限边界。
4. **控制协议没有认证。** socket 权限 0666（为了非 root native daemon 能连接 root init）+ 目录 0700 是唯一的访问门禁。同一目录内的进程或用同一 uid 运行的 workload 可以连接它。
5. **生命周期耦合。** init 退出即容器退出；init 重启会重建全部 session。supervisor 想独立升级、独立崩溃恢复时，这种耦合是阻碍。
6. **策略来源与 workload 共享 mount。** 未来的 security profile 若放在 sandbox 可写目录，需要额外的只读挂载/签名机制；当前没有。

---

## 2. 目标形态

```text
daemon
  │ control UDS（每个 sandbox 一条，peer credential 校验）
  ▼
supervisor（独立进程或独立容器/VM）
  ├─ 不进入 workload 的 pid/mount/user namespace
  ├─ 无 workload 所需 capability；仅保留启动/回收/控制所需最小集合
  ├─ 只读挂载 init binary + security profile
  ├─ 每个 sandbox 一条 host 侧 UDS，bind mount 到 workload 容器
  └─ workload 容器（独立 PID 1 / 独立 namespace）
        └─ agent 及其子进程
```

关键约束：

- **supervisor 不执行 workload。** 它只创建/停止 workload 容器（或 VM）、转发 exec 请求、回收租约；命令仍由 workload 容器自己的 init 执行。
- **supervisor 无网络。** 使用 host network + 本地 UDS 或独立控制网络；不加入 sandbox 的 egress 网络。
- **策略权威在 daemon + supervisor，不在 workload。** profile 由 daemon 签名/带去重 digest，supervisor 以只读方式读取；workload 不能修改。
- **控制认证。** UDS 目录 0700、socket 0600；daemon 用 `SO_PEERCRED` 校验 uid/gid，或使用 daemon 生成、随 sandbox 创建下发的 per-sandbox token（token 不进入 workload 环境）。无法校验时 fail closed。
- **失败语义。** supervisor 不可用/策略缺失 → sandbox 启动失败；supervisor 重启不能静默重建策略；workload 容器退出与 supervisor 退出解耦但可观测。

---

## 3. 为什么这比单容器强

- workload 即使拿到容器内 root，也不能连接控制 socket、修改 profile、向 init 发信号或读取 init 内存。
- control 面和 data 面分离后，控制认证是真正的边界，不再依赖“同一 uid/同一 namespace 大概可信”。
- supervisor 可以独立于 agent 镜像升级；guest 内核/CNI 能力探测和策略执行集中在可信一侧。
- Docker/K8s/VM 三种 driver 共用同一个 supervisor 契约：daemon 只看 UDS，supervisor 负责 driver transport。

---

## 4. 分阶段落地（建议）

1. **当前分支**：opt-in Landlock（enforce，工作区可写、系统目录只读、其余拒绝），控制目录权限校验，init binary 目录告警；单容器模型不变。
2. **阶段 A**：把待执行的 security profile 落到 daemon 侧只读目录，通过控制协议下发摘要；init 拒绝未经 daemon 签名的 profile。
3. **阶段 B**：新增独立 supervisor 进程（Docker 下由 daemon 启动的 sibling container；K8s 下为独立 Pod/Job），使用 host network + per-sandbox UDS；先只承担“创建/停止 + 控制转发”，不接管 workload exec。
4. **阶段 C**：把 seccomp/Landlock 策略执行移到 supervisor 创建 workload 时；init 只保留进程所有权；`SO_PEERCRED` + per-sandbox token 成为强制认证。
5. **阶段 D**：supervisor 独立升级/重启测试，daemon 重启不重建 supervisor，workload 存活语义与 [daemon-upgrade-sandbox-survival](daemon-upgrade-sandbox-survival.zh-CN.md) 对齐。

---

## 5. 与本分支 Landlock 的边界

- Landlock 工作区规则只保护文件系统：拒绝 `/.agent-compose`、控制目录的打开/读写/执行，不阻止 `connect(2)` 到控制 socket。
- init 目录加固（目录 0700/0755 检查、`/.agent-compose` 不入 allowlist）降低替换风险，但不提供新的 uid/namespace 边界。
- 真正的“workload 不能控制 init”需要独立 supervisor；本文件给出目标与阶段，不宣称已实现。

---

## 6. 未决问题

- Docker 下 supervisor 与 workload 容器共享 host UDS 时，如何避免 daemon 之外的本地用户访问（目录属主 + 0600 + peer uid）。
- K8s 下 supervisor Pod 如何通过 `pods/exec` 之外的正式通道访问 workload 控制面，以及 RBAC/NetworkPolicy 的默认拒绝。
- supervisor 崩溃后的 sandbox 所有权判定：以 supervisor 记录为准还是 driver inspect 为准，需要与现有 `VMState.SandboxInitState` 持久化兼容。
- per-run 策略、DNS、credential 的分层仍沿用现有 egress 设计，supervisor 不复制这些职责。


---

## 7. Docker MVP 实现边界（`feat/ac-supervisor`）

本节记录实际交付的内容，避免把设计目标误读成已实现能力。

### 7.1 进程与网络

- 新增 `cmd/ac-supervisor`，由 Docker driver 为每个启用 supervisor 的 sandbox
  以独立进程启动（`SANDBOX_SUPERVISOR=enforce`）。它不进入 workload 的
  pid/mount/user namespace，运行在 daemon 所在网络命名空间，通过 host UDS
  提供服务。
- workload 容器固定使用 `NetworkMode=none`，没有外部网卡；loopback 保留给
  init 的 DNS stub 和 per-connection relay。
- UDS 位于 `<SANDBOX_INIT_SOCKET_DIR>/<sandbox-id>/supervisor.sock`，随既有
  `/run/agent-compose` bind mount 进入容器；socket 0600、目录 0700。
- token 由 daemon 生成并存放在 **不挂载** 的 sibling 目录
  `<SANDBOX_INIT_SOCKET_DIR>/.<sandbox-id>.supervisor/token`（0600），同时通过
  **init 控制 socket** 的 `configure_egress` 请求交给 init。它不进入容器环境，
  因此 `docker exec` 也读不到；init 在配置前会先带 token ping 一次 supervisor，
  只有 supervisor 认可的 token 才会被接受，防止可访问控制 socket 的本地进程抢占
  配置。
- 停止 sandbox 时停止 supervisor 但保留 token，恢复时可以复用同一 token 重启
  supervisor；删除 sandbox 时删除 token/socket/log。daemon 崩溃后 supervisor
  可能继续运行，下一次 ensure 通过 socket ping 接管；若 supervisor 已死，则用
  持久 token 重启并用新签发的 proxy credential 继续工作。

### 7.2 转发与策略层

```text
workload connect
  → ac-init seccomp USER_NOTIF
  → pidfd_getfd 复制 socket
  → 绑定 loopback 源端口并 connect 到 per-connection 127.0.0.1 relay
  → relay 校验 accepted peer 的源端口（只有被复制的 socket 持有该端口）
  → UDS + token + destination/identity 握手
  → supervisor
      ├─ 普通目的地：CONNECT → daemon forward proxy（唯一策略判定点）
      └─ engine-owned endpoint：直接连接 daemon（facade/telemetry）
```

- **普通 egress 的策略权威仍是 daemon forward proxy**（`pkg/egress`）。supervisor
  只做传输中介，不复制策略引擎；proxy 的 allow/deny、L7、credential 校验和审计
  行为不变。
- **engine-owned endpoint 由 daemon 在创建时固定**：runtime base URL 和 telemetry
  的 host/port（含解析后的 IP）作为 supervisor 的 direct allowlist。workload 无法
  扩这个列表，因此不能借 direct 路径绕开声明的策略。
- init 的 DNS stub 继续返回 198.18.0.0/15 合成地址；engine hostname 的真实地址由
  daemon 解析后通过 `SANDBOX_INIT_EGRESS_ENGINE_HOSTS` 注入，因为无网卡时 init
  自己无法解析。合成地址到 hostname 的反查仍在 init 内完成，CONNECT 使用域名，
  域名 allow/deny 继续生效。
- 数据面（facade/telemetry）经由 supervisor 的 direct 连接到达 daemon；普通声明
  egress 仍由 forward proxy 决定。

### 7.3 已实现 / 未实现

已实现：

- 独立 `ac-supervisor` 进程、token 认证、handshake、CONNECT 转发、engine endpoint
  直连、socket 生命周期和 fail-closed；
- Docker driver 的创建/停止/删除/复用生命周期，`NetworkMode=none`，init supervisor
  env 注入，旧模式（默认 off）保持不变；
- init 侧 loopback relay、peer 源端口校验、token/engine-host env 清理，以及
  relay/handshake/token 的单元测试。

未实现 / 已知风险：

- supervisor 仍然运行在 daemon 进程的 host/container 网络命名空间，没有独立的
  supervisor 容器或独立升级；`docker exec` 等非 init 进程无网卡，因此结构性
  default-deny，但它们仍可与 init 同 namespace 交互（phase C/D 未做）。
- Jupyter 端口无法在 `NetworkMode=none` 下发布，supervisor 模式拒绝带 Jupyter 的
  sandbox。
- 不提供 L7 终止、TLS MITM 或 credential 注入；这些仍由 forward proxy 的既有
  能力/限制决定。
- supervisor 崩溃后已建立的 relay 会断开；新的连接需要 daemon 的下一次 ensure
  重启 supervisor。
