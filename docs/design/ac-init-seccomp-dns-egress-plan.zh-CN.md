# ac-init 网络安全隔离开发路线（OpenShell 式 seccomp + DNS 伪造）

> 输入：PR #821 `feat(sandbox): add opt-in sandbox init process`（head `c40bd6cb`）。
> 基线：`main @ b729d097` + PR #821；开发分支 `feat/ac-init-dev`，worktree
> `.worktrees/ac-init`。
>
> 目标：照 OpenShell 的实现思路，让 `ac-init` 在沙箱内成为网络中介：
> **seccomp user-notify 拦截 `connect`，DNS 返回合成地址，所有 TCP 流量被改道到
> daemon 侧 egress proxy 转发**。daemon 继续做唯一策略判定点（`pkg/egress`）。
> 不引入 `NET_ADMIN`/nftables/fence，不改网络拓扑。
>
> 原则：小 PR 增量合入，每个 PR 都能单独在 Docker + KVM 真机上验证；
> PR #821 先作为第一步合入，后续都基于它。

---

## 0. 结论先行

### 0.1 目标形态

```text
sandbox 内                                        daemon 侧
┌─────────────────────────────────────────┐      ┌──────────────────────────────┐
│ workload (agent / codex / pi / node ...)│      │ egress proxy (已有)          │
│   │ connect() / DNS                       │      │  认证 sandbox credential     │
│   ▼                                       │      │  每请求读 egress.Policy      │
│ seccomp filter ──USER_NOTIF──▶ ac-init    │      │  Decide → 200 tunnel/403     │
│                               │ duplicate │      │  记录 decision（后续接审计） │
│                               │ socket    │      │                              │
│                               └──CONNECT(host:port)──────▶ tunnel ──▶ internet │
│   dns stub 127.0.0.1:53  ◀── connect:53 ──┘      │                              │
│   返回 198.18.x.x 合成地址，TTL 30s              └──────────────────────────────┘
└─────────────────────────────────────────┘
```

- **拦截点**：`ac-init` 在每次 exec 的 trampoline 里安装 seccomp filter；`connect`
  返回 `SECCOMP_RET_USER_NOTIF`，通知发给 init 持有的 listener fd。
- **改道**：init 用 `pidfd_getfd` 复制目标进程的 socket，**由 init 自己**向 daemon
  egress proxy 发 `CONNECT host:port`（复用现有 proxy，不改协议），然后把
  `connect` 的结果 spoof 给 workload：成功返回 0，拒绝返回 `-ECONNREFUSED`。
  目标进程的 fd 没有被替换（file description 是同一个），所以 epoll/flags/阻塞语义都不变。
- **DNS**：init 在 `127.0.0.1:53` 起 DNS stub，A 记录返回 `198.18.0.0/15` 合成地址
  （TTL 30s），AAAA 返回空；init 维护 `合成 IP ↔ hostname` 映射，`connect` 拦截时
  用 hostname 去 daemon 判定。`connect` 到任意 `:53` 一律改连本地 stub。
- **策略判定仍在 daemon**：init 只做拦截/改道/带身份；`pkg/egress.Decide` 不复制进 guest。
- **TLS 不 MITM**：proxy 只做 CONNECT 隧道，证书端到端由 workload 校验证书。
  因此第一步只做 L4（host/port），L7 留到后续（需要每沙箱 CA，另开 PR）。

### 0.2 与 OpenShell 的对应

| OpenShell | 本方案 |
| --- | --- |
| sandbox runtime 用 seccomp 拦截 TCP open 与 DNS 查询 | ac-init 的 seccomp filter + listener，逻辑相同 |
| DNS 返回 TTL≤30s 的合成地址，AAAA 为空 | init 的 DNS stub + `198.18.0.0/15` 分配表 |
| 所有流量经 host 侧 proxy（10.200.0.1:3128） | 所有 TCP 经 daemon egress proxy（复用现有 CONNECT） |
| supervisor 做 OPA 判定 | daemon `pkg/egress` 判定（已有） |
| 每 sandbox 独立 token/JWT | 现有 per-sandbox proxy credential + init 持有的控制 token |
| 不依赖 proxy env，不读 env 的进程也被强制 | 同上；enforce 模式可不再注入 `HTTP_PROXY` |
| 后续 TLS MITM / 凭据替换 / L7 | 后续独立 PR，不在第一批 |

### 0.3 为什么不用上一条路线（nftables/fence）

- 不需要 `NET_ADMIN`（当前 docker/k8s 都没有，k8s 受限命名空间也不允许）；
- 不需要改 Docker 网络拓扑 / `internal` 网络，四种 driver 都用同一条路径；
- 不需要在 guest 里放 nft/iptables 二进制；
- 不依赖“应用遵守 proxy env”，也不依赖 DNS 配置；
- 与 ac-init 已有的“进程树所有权 + 控制通道”天然契合：只有 init 能给每个 exec 装 filter，
  并且只有 init 知道连接属于哪个 session/run。

nftables/fence 可以作为将来的 defense-in-depth，不是这条路线的前置。

---

## 1. 机制设计

### 1.1 seccomp filter：只拦截 `connect`（+ 堵 io_uring）

每个 exec session 由 `ac-init --exec-child` trampoline 在 `execve` 前安装一个
seccomp filter：

- `connect` → `SECCOMP_RET_USER_NOTIF`
- `io_uring_setup` → `SECCOMP_RET_ERRNO(EPERM)`（否则 io_uring 的 connect 不经 seccomp，
  是已知绕过面；OpenShell 也封 io_uring）
- 其余全部 `SECCOMP_RET_ALLOW`
- 校验 `arch`（x86_64 / aarch64；未知架构 fail closed，拒绝启动）
- 先 `PR_SET_NO_NEW_PRIVS=1`，再 `seccomp(SECCOMP_SET_MODE_FILTER,
  SECCOMP_FILTER_FLAG_NEW_LISTENER, ...)`：man page 明确 filter 安装只需
  `CAP_SYS_ADMIN` **或** no_new_privs；NEW_LISTENER 不额外要 cap。
- filter 在 fork/exec 后仍生效、不能被移除（只能叠加；叠加 ALLOW 也压不过 USER_NOTIF），
  所以 workload 无法自行解除拦截。

trampoline 需要：

- `runtime.LockOSThread()`，确保安装 filter 和 `execve` 在同一线程；
- 用 `socketpair`（ExtraFiles）把 listener fd 通过 `SCM_RIGHTS` 交给 init 后关闭；
- 按需设置 uid/gid/caps（第一阶段保持 root，后续 PR 再降权）；
- `syscall.Exec(target, argv, env)` 启动真正的 workload。

### 1.2 通知处理：复制 socket，由 init 完成 CONNECT

init 每个 exec session 一个 goroutine 读 listener：

1. `SECCOMP_IOCTL_NOTIF_RECV` 取通知；确认 `nr == connect`。
2. 用 `process_vm_readv`（或 `/proc/<tid>/mem` + `pread`）读目标进程里的
   `struct sockaddr`（IPv4/IPv6/Unix）。
3. 判断：
   - dest 是 daemon data-plane 地址 / 沙箱自身地址 / loopback（除 53）→ 直接连接；
   - dest 端口 53（UDP 或 TCP）→ 复制 socket 并 connect 到 `127.0.0.1:53`（DNS stub）；
   - 其它 UDP → `-EACCES`（第一版不做 UDP 代理）；
   - 其它 TCP → 走 daemon egress proxy。
4. 用 `pidfd_getfd(pidfd(tid), fd, 0)` 复制目标进程正在 connect 的 socket
   （同一个 open file description，之后 target 用的还是这个 socket）。
5. 对复制出的 fd 执行真实连接：
   - 目标是 proxy：`connect` 到 daemon data-plane，写
     `CONNECT host:port HTTP/1.1` + `Proxy-Authorization` + 身份头，读响应；
     200 → 成功；403/407 → 拒绝；超时/错误 → fail closed。
   - 非阻塞 socket 的处理：`connect` 返回 `EINPROGRESS` 时 poll 可写，再发 CONNECT，
     再 poll 可读；整个过程中 target 线程被 seccomp 通知阻塞，不会并发使用 socket。
6. `SECCOMP_IOCTL_NOTIF_SEND` spoof 结果（成功 `val=0`，失败 `error=-errno`），
   然后关闭 init 侧的复制 fd。

**关键点：不要用 `SECCOMP_IOCTL_NOTIF_ADDFD` 在 connect 时替换 target 的 fd**。
替换 fd 会让 Node/Go 已经注册在 epoll 里的 file description 失效，出现挂死。
用“复制同一个 socket + 由 init 执行 connect”的方式，target 的 fd/file description/
非阻塞标志/epoll 注册全部不变。

**不要用 CONTINUE 处理被中介的 connect**：CONTINUE 有 TOCTOU（目标进程另一线程可在
check 后改写 sockaddr），必须 spoof 返回值，绝不退回内核执行原 syscall。
非 INET/INET6/UNIX 的 family 直接 `-EACCES`，不做 CONTINUE。

### 1.3 DNS stub 与合成地址

- init 在 `127.0.0.1:53` 同时监听 UDP 和 TCP（root 权限；guest 内没有 systemd-resolved）。
- 解析 A/AAAA（用 `golang.org/x/net/dns/dnsmessage`）：
  - A：从 `198.18.0.0/15` 顺序分配，维护 `hostname → IP` / `IP → hostname`（上限 + LRU）；
    TTL 固定 30s（与 OpenShell 一致）。
  - AAAA：NOERROR、空 answer（避免 v6 连接）。
  - 其它 qtype：NOERROR 空（或按需后续支持）。
- 沙箱启动时 init 把 `/etc/resolv.conf` 指向 `127.0.0.1`（正常 libc/Node/Python 都走它）；
  同时对发往任意 `:53` 的 connect 做重定向，覆盖硬编码 8.8.8.8 的情况。
- `connect` 拦截时：若 dest 是合成 IP，从映射还原 hostname，CONNECT 用 hostname 发给
  daemon proxy（这样 `allowed_ips`、域名 allow/deny 都按声明生效）；否则用 IP 字面量。
- DNS 查询永远不离开沙箱（除 DoH/DoT 这种走 TCP 的加密 DNS，由普通 connect 策略处理），
  DNS 隧道外泄面被关闭。

### 1.4 身份、凭据与绕过规则

- daemon 仍然给 sandbox 发 per-runtime proxy credential（`sandbox-id:token`）。
  现有代码把它放进 workload 的 `HTTP_PROXY` 环境；**enforce 模式下不再注入**
  （否则 workload 可以拿 token 直接连 proxy）。credential 只通过 init 的启动 env
  （不是 cmdline）传给 init；init 启动后 `PR_SET_DUMPABLE=0`，workload 无法读
  `/proc/1/environ`/`/proc/1/mem`（root 也一样，除非有 CAP_SYS_PTRACE，而 trampoline
  已把它从 workload 的 bounding set 去掉）。
- init 在 CONNECT 上加身份头：`X-Agent-Compose-Sandbox`、`X-Agent-Compose-Session`、
  `X-Agent-Compose-Marker`、`X-Agent-Compose-Pid`。daemon 校验 sandbox 头与 credential
  一致；session/marker 用于后续 per-run 策略与审计。
- 绕过规则（不经过 proxy，直接连接）：
  1. daemon data-plane 地址（LLM facade / Jupyter / proxy 本身）；
  2. 沙箱自身接口地址（本地服务；不含其它 sandbox 的地址）；
  3. `127.0.0.1`/`::1` 的非 53 端口；
  4. `AF_UNIX`（本地 IPC）。
  其余全部走 proxy；daemon proxy 已有 engine 的 loopback/metadata/控制面端口拒绝规则，
  所以不需要在 init 里重复策略。
- 失败语义：proxy 不可达/超时/拒绝 → `-ECONNREFUSED`（或 `-ETIMEDOUT`），
  **绝不回退直连**。daemon 不可达时 sandbox 的 egress 是 fail closed。

### 1.5 策略与审计

- 第一版 daemon 侧几乎不用改：现有 CONNECT handler 已经认证 sandbox、按当前声明判定、
  deny 返回 403、allow 建隧道。init 只是把“应用以为自己连的地址”换成“对 proxy 的 CONNECT”。
- daemon 需要的小改动：
  - 读取 init 传来的身份头（仅审计，不参与决策）；
  - `SandboxNetworkEnforcement` / `capmatrix` 增加 `seccomp_redirect` 机制与能力报告；
  - `EGRESS_INTERCEPT_MODE=off|audit|enforce` 配置与准入报告；
  - 后续接线 `EgressProxyRecorder`（现在生产是 nil），把 allow/deny + 身份落库。
- 第一版不改变 `pkg/egress` 决策模型，不引入第二套策略引擎。

---

## 2. 可行性前提（先做 spike，再写 PR3）

### 2.1 内核/运行时要求

| 依赖 | 最低版本 | 说明 |
| --- | --- | --- |
| seccomp user-notify（RECV/SEND） | Linux 5.0 | filter 返回 USER_NOTIF |
| `SECCOMP_USER_NOTIF_FLAG_CONTINUE` | 5.5 | 只有非 INET family 需要；可不用 |
| `pidfd_open` / `pidfd_getfd` | 5.3 / 5.6 | 复制目标 socket，**核心** |
| `SECCOMP_IOCTL_NOTIF_ADDFD` | 5.9 | 本方案不需要，作为备选/后续 |
| guest 内核 | ≥5.6 | microVM 内核需在 init 里探测，不能只信 capmatrix 的 host 探测 |

init 启动时自己做一次能力探测（在 guest 内）：`SECCOMP_GET_ACTION_AVAIL(USER_NOTIF)`、
尝试 `pidfd_getfd`、`process_vm_readv`、内核版本；结果通过控制协议上报 daemon，
能力不满足时 `EGRESS_INTERCEPT_MODE=enforce` 直接拒绝启动，audit 模式只记录。

### 2.2 权限矩阵（关键，决定 driver 支持范围）

| 驱动 | 现状 | 拦截模式需要 | 结论 |
| --- | --- | --- | --- |
| **docker** | init root，默认 caps 减 NET_RAW；seccomp 用 Docker 默认 profile | init 需要 **CAP_SYS_PTRACE**（Docker 默认 profile 对 `pidfd_getfd` 只在 `CAP_SYS_PTRACE` 下放行）；workload 保持 root 即可 | 可行：`CapAdd: SYS_PTRACE`，trampoline 从 workload bounding set 去掉 SYS_PTRACE |
| **boxlite/microsandbox** | guest 内 init root，全量 caps | guest 内核 ≥5.6 即可 | 可行，需真机 probe |
| **k8s** | 无 NET_ADMIN，PodSecurity 通常禁 SYS_PTRACE | init 需要 SYS_PTRACE | 受限命名空间不可行 → 能力报告降级为 proxy env / 或集群显式放开；不作为第一批验收门槛 |

为什么放宽了“workload 降权”这个前置：本方案里 init 是唯一持有 credential、唯一能装 filter
的进程；workload 即使 root，也因为（a）filter 不可移除、（b）`PR_SET_DUMPABLE=0` +
无 SYS_PTRACE 读不到 init 内存/env、（c）trampoline 清掉危险 caps，而无法解除拦截或
伪造身份。它仍然可以 SIGKILL/SIGSTOP init（自 DoS），这是可接受的 v1 限制。
后续再做 workload 非 root + PEERCRED 的加固 PR。

### 2.3 必须验证的 spike（PR2 前或 PR2 内）

1. Docker 默认 seccomp + `--cap-add SYS_PTRACE` 下 `pidfd_getfd` 成功、`/proc/<tid>/mem`
   可读（同 uid root）；
2. trampoline 里 `no_new_privs` + `NEW_LISTENER` 安装成功，listener fd 经 SCM_RIGHTS
   到 init；
3. 目标进程用**非阻塞 socket**（Node/Go 风格）时，复制 socket + init connect + 发 CONNECT
   的流程能正确结束时，target 的 connect 看到 0，epoll 正常；
4. 目标进程是 Node/Python/Go/curl 时，`getsockname/getpeername/SO_ERROR` 行为可接受
   （peer 变成 proxy 地址是已知差异，先记录）；
5. 并发（npm install、pip install）下每连接一次 CONNECT 的性能可接受；
6. `io_uring_setup` 拒绝后，目标 runner 不回归；
7. microsandbox/boxlite guest 内核与 caps 满足。

---

## 3. 增量 PR 计划（每个都独立可合入、可实测）

> 目标：单 PR 尽量 < 1000 行 Go（PR3/PR4 可能略超，但按“基础设施 / 生效行为”拆开）。

### PR #821（已存在，先合入）

- 现状（2026-11-02）：draft；head `c40bd6cb` 的 CI 基本全绿（Lint、Go tests、
  Coverage、Linux full binary、Full image Docker smoke）；`mergeStateStatus=BLOCKED`
  因为 `REVIEW_REQUIRED` + draft；30 条 review 线程，作者已逐条回复修复。
- 要做的：mark ready for review → 请 reviewerscan 复审 → 需要的话 rebase 到最新 main
  → 合入。这一步不在本分支做，它必须先落地。

### PR1：ac-init 身份握手与能力协商（本 PR，S，约 250 行）

- 目的：让 daemon 能识别“这个 init 是不是我创建的那个 sandbox 的”，并为后续
  seccomp/DNS 能力协商留出 additive 演进路径；同时不让 init 成为 workload 可读的
  凭据目标。
- 内容：
  - `pong` 增加 `sandbox_id` 和 `capabilities []string`；能力列表常量在
    `pkg/sandboxinit/protocol.go`，新增能力只做 additive，**不 bump `Version`**
    （daemon/guest 独立升级，旧 init 返回 unknown → 按无能力降级）。
  - sandbox 身份走 driver **已有的 `SANDBOX_ID` 环境变量**（四种 driver 都已注入），
    `ac-init` 读取后回显；不新增命令行参数，避免新 daemon 让旧 init 拒绝启动。
  - daemon 就绪等待改为 `VerifySandbox`：init 报告了身份且与目标不一致时立即失败，
    不当作可重试超时；旧 init（无字段）按 legacy 接受。
  - init 启动即 `PR_SET_DUMPABLE=0`，workload 不能读 `/proc/1/mem`、
    `/proc/1/environ`。
- 未包含（按计划后移）：per-runtime token、`SO_PEERCRED` 强制、socket 0600/0660。
  这些与 PR3 的 proxy credential 下发一起做，避免在还没有凭据通道时先改认证模型。
- 验收：新/旧 init 组合都能降级；sandbox id 不匹配立即失败；`/proc/1/environ`
  对 workload 不可读；现有 exec/interaction/kill 行为不变。
- 测试：`pkg/sandboxinit` 握手/legacy/不匹配用例；Docker/k8s 单测；Docker e2e。

### PR2：exec trampoline + seccomp listener + audit 模式（本 PR，M）

实现：

- `ac-init --exec-child` trampoline：`LockOSThread`、`PR_SET_NO_NEW_PRIVS`、安装
  BPF filter（`connect` → USER_NOTIF，`io_uring_setup` → EPERM，arch 不匹配 →
  KILL_PROCESS）、通过 init 预先建立的 socketpair 用 `SCM_RIGHTS` 交 listener，
  再 `syscall.Exec` 目标；
- init 每个 exec session 一个 supervisor goroutine：`NOTIF_RECV` → `ID_VALID` →
  `process_vm_readv` 读 sockaddr → 记录 `sandbox/session/marker/pid/family/host/port` →
  `SECCOMP_USER_NOTIF_FLAG_CONTINUE`；TTY 与非 TTY 共用同一 trampoline；
- guest 内 capability probe（kernel ≥5.5 + `SECCOMP_RET_USER_NOTIF`）；不满足时
  audit 降级为 off 并在 pong 能力列表里不出现 `egress-audit`；
- 配置 `SANDBOX_INIT_EGRESS=off|audit`（`enforce` 在 PR3 前显式报错）；四种 driver
  把模式作为环境变量注入 sandbox（旧 init 忽略该变量）；
- 修复 `Hooks.Prepare` 在 `SysProcAttr` 之前被覆盖的顺序问题，后续 seccomp/Landlock
  可在同一 trampoline 扩展；
- audit 只看不改：filter 安装失败时 fail-open（记录告警）；filter 已安装但 listener
  交接失败时 trampoline 拒绝 exec，避免 workload 的 connect 变成 ENOSYS。

验收与实测：

- `pkg/sandboxinit` 主机内测试：filter BPF 规则、kernel 版本解析、真实 connect
  audit（非 TTY / TTY / 3 路并发），`-race` 通过；
- Docker 真机（29.8.1 / arm64）：`TestDockerSandboxInitEgressAuditIntegration` 通过——
  exec 进程 `Seccomp_filters >= 2`（容器默认 + init filter），Python connect 成功
  （CONTINUE 生效），container log 出现 `sandbox init egress audit` 且 host/port 正确；
- PR1 的身份/legacy 集成测试在 PR2 镜像上复跑通过。

### PR3：TCP connect 改道到 daemon proxy（本 PR，L，核心）

实现：

- `SANDBOX_INIT_EGRESS=enforce`：init 从现有 `HTTP_PROXY`/`HTTPS_PROXY` 读取 daemon
  发放的 per-sandbox credential，并立即把六个 proxy 环境变量从自身环境 unset；
  workload 由 init fork/exec，因此不继承 credential（`/proc/1/environ` 也对 workload
  不可读，dumpable=0 已在 PR1 落地）；
- 收到 `connect` 通知后：`process_vm_readv` 读 sockaddr → `pidfd_getfd` 复制目标
  socket → 在复制 fd 上 `connect(proxy)` + `CONNECT host:port HTTP/1.1`
  （带 `Proxy-Authorization` 与 `X-Agent-Compose-Sandbox/Session/Marker`）→
  200 时 spoof 成功，403/407/不可达/超时 spoof `ECONNREFUSED`（fail closed）；
- 复制 fd 与目标 fd 是同一个 open file description，不替换 fd，因此非阻塞标志、
  `SO_ERROR`、epoll 注册都不变；
- 直连 bypass：`AF_UNIX`、loopback、沙箱自身接口地址、proxy 自身地址；非 DNS 的 UDP
  拒绝；DNS（UDP/TCP 53）暂时直连，PR4 替换为策略 DNS；
- Docker/Kubernetes 在 enforce 时为 sandbox 容器加 `CAP_SYS_PTRACE`（Docker 默认
  seccomp profile 只在该 cap 下允许 `pidfd_getfd`）；trampoline 在 exec 前把
  `SYS_PTRACE/SYS_ADMIN/NET_ADMIN/NET_RAW/BPF/PERFMON/SYS_MODULE` 从 workload 的
  bounding set 去掉；
- `connect`/CONNECT 有 10s 超时；通知处理并发（每 session 一个 goroutine），不阻塞
  init 的 accept/reaper；
- 没有 proxy credential 时（未声明 policy）enforce 记录告警并退回 off；能力报告的
  daemon 侧接入放到 PR6。

验收与实测：

- 主机内（真实 seccomp）：`TestInitEgressEnforce*` 覆盖——
  CONNECT 到 fake proxy 并隧道 echo、workload 环境无 proxy 变量、proxy 403 → 客户端
  收到 `ECONNREFUSED`、proxy 不可达 → fail closed、loopback 直连且 proxy 不收到请求；
- Docker 真机（29.8.1 / arm64）：`TestDockerSandboxInitEgressEnforce[Deny]Integration`
  通过——Python（proxy-unaware）连接 `203.0.113.9:443` 经 host fake proxy 隧道 echo，
  CONNECT 带 credential/身份头，workload 的 `HTTP_PROXY` 为空；proxy 返回 403 时
  Python 收到 `ConnectionRefusedError`；
- PR1/PR2 的 identity/audit/legacy 集成测试在 PR3 镜像上复跑通过；`-race`、lint 通过。

### PR4：DNS stub + 合成地址 + 域名策略（本 PR，L）

实现：

- init 在 `127.0.0.1:53` / `::1:53` 同时起 UDP 与 TCP 策略 DNS；A 查询返回
  `198.18.0.0/15` 合成地址（TTL 30s），AAAA 返回空 answer，其它类型空 NOERROR；
- 发往任意 `:53` 的 connect（Docker 内嵌 127.0.0.11、外部 8.8.8.8 等）在 enforce
  路径里被改连到 stub；按目标 socket 的 `SO_DOMAIN` 选择 IPv4/IPv6 stub；
- connect 到合成 IP 时从映射还原 hostname，CONNECT 发 `hostname:port`，daemon 的
  域名 allow/deny 与 `allowed_ips` 按声明生效；未映射的合成地址 fail closed；
- engine hostname（`AGENT_COMPOSE_RUNTIME_BASE_URL` 和 proxy host）passthrough 到
  真实地址，facade/Jupyter 不被当作普通 egress；
- 不改写 `/etc/resolv.conf`：拦截 `:53` 已覆盖系统 resolver，同时避免 init 自己的
  解析被 stub 递归劫持；
- stub 启动失败只告警（TCP 改道继续，域名策略退化为 IP 策略），`egress-dns`
  能力只在 stub 真正启动时广告；
- 已知未覆盖：unconnected UDP `sendto` 型 resolver（PR5）；DoH/DoT 走普通 TCP
  策略路径；域名 allow/deny 的 daemon 侧报告与审计在 PR6。

验收与实测：

- 主机内：`TestInitEgressPolicyDNSAnswersSyntheticAddresses` 断言 A 为
  198.18/15、TTL 30、AAAA 为空；`TestInitEgressPolicyDNSPassesEngineHostThrough`
  断言 engine hostname 返回真实地址；`TestInitEgressPolicyDNSRoutesHostnameThroughProxy`
  用 Go 的 `net.LookupHost` 走 stub，再 connect 合成地址，fake proxy 收到
  `CONNECT allowed.example:443` 并隧道 echo；
- Docker 真机：`TestDockerSandboxInitEgressPolicyDNSIntegration` 用 proxy-unaware
  Python，`getaddrinfo("allowed.example")` 得到 198.18.x.x，connect 后 fake proxy
  收到 `CONNECT allowed.example:443` 并 echo；glibc resolver 的 UDP connect 被正确
  重定向；
- PR1–PR3 的 identity/audit/enforce/legacy 集成测试在 PR4 镜像上复跑通过。

### PR5：UDP DNS 与 resolver 兼容矩阵（本 PR，M）

实现：

- seccomp filter 增加 `sendto`（仅当目标指针非空时）、`sendmsg`、`recvfrom`、
  `getpeername` 通知；`sendto`/`sendmsg` 到 `:53` 时 init 读取 payload、用
  `pidfd_getfd` 复制 workload socket、由 init 把原始 DNS 报文转发到 stub；
- stub 响应到达 workload 自己的 socket 队列后，`recvfrom` 通知把数据写回目标缓冲区，
  并把 `source` sockaddr 恢复成 workload 配置的 nameserver；`getpeername` 同样返回
  原 nameserver。c-ares 会校验 peer/source，不做这一步会把所有响应丢掉（实测
  `dns.resolve4` ETIMEOUT）；
- 非 DNS datagram 仍然 fail closed；非 DNS 的 `recvfrom`/`getpeername` 直接 CONTINUE；
- trampoline 的 listener 交接改用 `sendmmsg`，避免 filter 对 `sendmsg` 的通知在
  supervisor 尚不存在时把 trampoline 自己阻塞（这是本 PR 调试中发现并修复的死锁）；
- 已知残余：`sendmmsg` 未被 filter 拦截（trampoline 用它交接），使用它的 resolver
  仍可能绕过策略 DNS；TCP egress 不受影响。

验收与实测：

- 主机内：`TestInitEgressPolicyDNS(Sendto|Sendmsg)Unconnected` 验证 unconnected
  sendto/sendmsg 经 stub 得到合成地址；`TestInitEgressPolicyDNSConnectedGetpeername`
  验证 connect 后 `getpeername`/`recvfrom` 的 source 都是原 nameserver；
- Docker 真机：`TestDockerSandboxInitEgressPolicyDNSDatagramIntegration` 用 Python
  unconnected `sendto` 与 `sendmsg` 拿到 198.18 合成地址；
  `TestDockerSandboxInitEgressPolicyDNSNodeResolverIntegration` 用 Node c-ares
  `dns.resolve4`（系统 nameserver 10.2.255.2）拿到合成地址，验证 source/getpeername
  恢复路径；
- PR1–PR4 的集成测试在 PR5 镜像上复跑通过；`-race`、lint 通过。

### PR6：身份、审计与诚实报告（本 PR，M）

实现：

- init 在 CONNECT 上新增 `X-Agent-Compose-Pid`，四个身份头统一由
  `pkg/egress/identity.go` 定义；
- daemon proxy 校验 sandbox 头与 credential 一致（不一致 403），把
  session/marker/pid 写入 `egress.Request`，随 allow/deny 一起进入 `egress.Record`；
- 新增 `EGRESS_AUDIT_LOG` 配置：forward proxy 的每条决策追加一行 JSONL
  （sandbox、destination、resolved IPs、action、rule、generation、身份、时间）；
  写失败只记录日志，不改变决策；
- admission 报告在 `SANDBOX_INIT_EGRESS=enforce` 时把 `seccomp_redirect` 列为机制，
  用 init 的真实语义替换旧的“只约束遵守 proxy env 的进程”限制，并披露 DNS stub
  的 `sendmmsg` 残余；
- per-run 策略仍未实现：身份字段先进入审计与记录，作为后续策略选择的输入。

验收与实测：

- 单元：proxy 记录 init 身份、拒绝 sandbox 头不匹配/非法 pid；audit writer 的
  JSONL 往返；report 的 `seccomp_redirect` + 限制文案；config 路径校验；
- `EGRESS_AUDIT_LOG` 打开后，daemon 在每条 forward-proxy 决策后写一行；未设置时
  recorder 仍为 nil，行为不变；
- 现有 proxy/driver/app/config 测试通过；Docker 集成套件不依赖 daemon 重启，
  PR1–PR5 回归保持通过。

### PR7：加固与后续（M/L）

- workload 非 root + SYS_PTRACE 只留 init；PEERCRED/权限收紧；
- `getpeername` spoof（保持应用可见的地址语义）、`SO_ORIGINAL_DST` 等兼容；
- seccomp 默认 profile（阻断 ptrace/io_uring/unshare 等）；
- 可选的 nftables/fence defense-in-depth；
- L7（TLS MITM + 每沙箱 CA + 凭据替换）单独大 PR，不在本路线第一阶段。

---

## 4. 真机测试计划（每个 PR 都要做）

### 4.1 环境

- Docker（默认 driver）：宿主内核 ≥5.6；guest image 含新的 `ac-init`；
  `SANDBOX_INIT_ENABLED=true`、`SANDBOX_INIT_EGRESS=enforce`；daemon 开 `HTTP_LISTEN`。
- KVM：microsandbox + boxlite（`docker-compose.kvm.yml`），guest 内核 probe 结果写进
  能力报告；至少一个 VM driver 跑 smoke。
- k8s：可选；若集群不允许 SYS_PTRACE，验证能力报告为不可用并保持 env proxy 降级。

### 4.2 每次必跑的功能矩阵

| 类别 | 用例 |
| --- | --- |
| 基本 TCP | `curl`（清空 proxy env）→ allowed IP/域名 200；denied → 快速失败 |
| TLS | 证书校验通过（无 MITM）；SNI 正确；`curl -v` 看到正确证书 |
| 语言 runtime | Node `fetch`/`https`、Python `requests`/`urllib`、Go `net/http`、git https |
| 包管理器 | `pip install`、`npm install`（并发/连接复用）、`go mod download` |
| Agent harness | codex / pi / claude / dsh 各打一次 prompt（facade 走 data-plane 绕过） |
| DNS | `getent hosts`、Node `dns.lookup`、Python `socket.getaddrinfo`；AAAA 行为 |
| 拒绝 | 未声明域名/IP、控制面端口、metadata、DNS 重绑定、非 53 UDP |
| 绕过 | 清空/伪造 proxy env；`unshare -n`（应仍被 filter 拦截）；io_uring；raw socket；直接连 daemon API（应 407/不可达） |
| 生命周期 | 并发 exec、长连接、daemon 重启后 sandbox 继续可用、kill init 后 sandbox 退出 |
| 失败语义 | daemon proxy 不可达 → fail closed；超时后恢复 |
| 性能 | 建立连接延迟、npm/pip 安装耗时、并发 100+ connect |

### 4.3 测试基建

- `pkg/sandboxinit` 单元测试用“helper 子进程”真实发 connect，验证 spoof/超时/并发；
- `test/e2e/egress_intercept_*_test.go` opt-in，复用现有 Docker network policy e2e 模式；
- CI 不跑需要真实 docker/KVM 的用例，保持现有 `test/e2e` 约定。

---

## 5. 风险与未决问题

1. **Docker/k8s 的 SYS_PTRACE**：Docker 可加 cap；k8s 受限命名空间不可加 →
   k8s 第一版只能 env proxy 降级，能力报告要诚实。是否有必须支持的 k8s 场景需要确认。
2. **guest 内核**：microVM 内 `pidfd_getfd`/seccomp unotify 版本需真机 probe；
   不满足时 init 要 fail closed/降级。
3. **非阻塞 connect 的细节**：复制 socket 后由 init 完成 CONNECT，target 的
   `SO_ERROR`、`getsockname`、`getpeername`、`TCP_INFO` 语义会变化；需要按 runner 验证，
   必要时后续 spoof `getpeername`。
4. **身份可信度**：v1 依赖“init 独占 credential + dumpable=0 + workload 无 SYS_PTRACE”；
   如果 workload 能拿到 container config/宿主权限，则身份可伪造——后续非 root 化解决。
5. **UDP/QUIC**：第一版只支持 DNS UDP；QUIC 会失败回退 TCP；其它 UDP 默认拒绝。
6. **性能**：每连接一次 CONNECT 握手 + init 通知；需要基准和并发上限，避免慢 proxy
   拖死 target 线程。
7. **io_uring / 新 syscall**：必须持续维护 filter 的拒绝列表；`socketcall`/x32 架构处理。
8. **与现有 proxy env 的兼容**：enforce 初期是否完全停发 `HTTP_PROXY` 需要灰度；
   保留一个 `SANDBOX_INIT_EGRESS=audit` 阶段收集真实流量。
9. **PR #821 合入**：review 未完成 + draft；必须先落地，否则后续 PR 无法基于 main。

---

## 6. 代码触点

| 主题 | 位置 |
| --- | --- |
| trampoline / seccomp filter / listener | 新增 `pkg/sandboxinit/exec_child_linux.go`、`seccomp_linux.go` |
| 通知循环 / connect 改道 | 新增 `pkg/sandboxinit/egress_linux.go`（按 notify/connect/dns 拆分文件） |
| DNS stub / 合成地址 | 新增 `pkg/sandboxinit/dns_linux.go` |
| 控制协议/能力协商/token | `pkg/sandboxinit/{protocol,server_linux,client,bridge}.go`、`cmd/ac-init/main.go` |
| init 进程启动/环境/flag | `cmd/ac-init/main.go`、`pkg/config/config.go` |
| Docker init 启动/caps/env | `pkg/driver/docker_sandbox_init.go`、`docker_runtime.go` |
| k8s/VM init 启动 | `pkg/driver/{k8s,boxlite,microsandbox}_sandbox_init*.go`、`sandbox_init_runtime.go` |
| 能力声明/报告 | `pkg/driver/sandbox_init_capability.go`、`network_enforcement.go`、`pkg/capmatrix/*` |
| daemon proxy/凭据/身份头 | `pkg/agentcompose/proxy/egress_proxy.go`、`pkg/agentcompose/adapters/egress_proxy_sandbox.go` |
| 审计 record | `pkg/egress/record.go`、`pkg/agentcompose/proxy/egress_proxy.go`（recorder 接线） |
| 部署配置 | `.env.example`、`docker-compose*.yml`、`docs/pages/*` |

---

## 7. PR #821 提交状态（2026-11-02）

- PR：<https://github.com/chaitin/agent-compose/pull/821>（作者 kingfs，draft）
- head：`c40bd6cb`；CI 已有运行且主要检查通过（Lint / Go tests / Coverage / Linux
  full binary / Full image Docker smoke）。
- 阻塞：`isDraft=true` + `REVIEW_REQUIRED`（reviewer：monkeyscan，30 条线程已逐条回复
  修复）。
- 建议动作：mark ready for review → 请求复审 → 通过后合入；随后把
  `feat/ac-init-dev` rebase 到新 main，开始 PR1/PR2。
- 需要注意：不要为了后续 PR 先改 #821 的分支内容；后续都作为独立 PR 叠在 main 上。
