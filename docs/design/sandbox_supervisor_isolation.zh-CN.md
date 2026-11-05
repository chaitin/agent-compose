# 独立 Sandbox Supervisor 隔离设计（设计稿，未实现）

> 状态：设计稿，代码未实现。
>
> 目标：说明当前“单容器内 ac-init 既是 PID 1 又是控制面”的弱点和独立 supervisor 的目标形态。本文件不承诺 supervisor 已落地；当前分支只交付 opt-in Landlock 和 init 目录/控制目录加固。

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
