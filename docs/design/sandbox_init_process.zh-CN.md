# Sandbox Init 进程（Stage 1）设计

> 状态：Stage 1 实现草案
>
> 目标：在启用实验开关后，让 `ac-init` 成为所有 sandbox driver 的 PID 1，统一管理 agent 的 JS runtime、codex/pi 等 harness 以及它们启动的所有子进程；init 退出即 sandbox 退出。Stage 1 不改变网络代理/密钥模型，只为后续 seccomp/Landlock 预留扩展点。

---

## 1. 背景与问题

改造前，各 driver 都没有统一的 sandbox PID 1；以 Docker 为例：

- 容器配置 `Entrypoint: ["sh", "-lc"]`、`Cmd: ["tail -f /dev/null"]`（或 Jupyter 启动命令），并设置 `HostConfig.Init = true`。
- DAEMON 通过 `docker exec` 启动 agent、codex/pi harness、scheduler 命令和交互式 attach。
- `docker exec` 进程由 Docker/runc 创建，不在一个受 sandbox 控制的进程树里；没有统一的父进程、回收器、信号转发、资源统计或后续 seccomp 注入点。
- agent 的子进程、后台进程、孤儿进程散落在容器 PID namespace 中；daemon 只能靠 marker/`docker exec` 做 best-effort 清理。

Stage 1 先建立**统一的进程所有权**，而不是只解决某一个 driver：

1. 每个 sandbox 的 PID 1 都是同一个 `ac-init`；
2. 所有 workload 进程（命令、交互式 attach、Jupyter main）都由 init `fork/exec`，不再依赖 driver 原生 exec 创建 workload；
3. init 负责回收孤儿、转发终止信号、维护进程表；
4. daemon 只通过控制协议操作 init，不依赖 `docker exec`/SDK exec 的 workload 语义；
5. init 被杀死时，sandbox 必须随之退出，不能留下一个没有 init 的 workload 树；
6. feature flag 关闭时完全保留现有 driver 行为。

---

## 2. 非目标（Stage 1）

- 不改变网络/egress 模型（仍走现有 daemon forward proxy）。
- 不实现 seccomp/Landlock/namespace/cgroup 的强制；只保留 `Hooks` 扩展点。
- 不实现完整的 boundary TLS/gRPC、policy DNS、TLS MITM、credential broker。
- 不改变 sandbox 的 root 用户/能力；Stage 1 的 init 与当前容器一样以容器用户运行。后续阶段再讨论降权/seccomp。

---

## 3. 总体结构

```text
daemon
  │  control transport（Unix socket / pod exec bridge / VM transport）
  ▼
sandbox PID 1: ac-init
  ├─ accept loop / JSON protocol
  ├─ process manager / reaper（wait4）
  ├─ optional main process（仅 Jupyter 等真实 main）
  └─ exec sessions（agent、codex/pi、scheduler、attach）
        ├─ stdout/stderr frames
        ├─ stdin / resize / signal
        └─ exit status
```

- init 先创建控制入口，再进入 accept loop；daemon 在 sandbox 启动后能 `Ping` 到 init 才算 ready。
- init 不依赖 keepalive 命令：没有 Jupyter 时，init 自己保持 sandbox 存活，真正的命令由 daemon 通过控制协议启动。
- daemon 的 `Exec`/`ExecStream`/`OpenInteraction` 在 init sandbox 上走 init 协议；legacy sandbox 才走 driver 原生 exec。
- `TerminateProcessTree` 和 `SignalGuestRuntime` 也通过 init，避免控制进程落在 init 树之外。

---

## 4. 控制协议

Unix socket 上使用 newline-delimited JSON。选择 JSON 而不是 gRPC 是为了：

- 第一阶段无 proto 生成/版本迁移成本；
- 便于人工用 `nc`/小工具排障；
- 数据帧 base64，足以承载 stdout/stderr；后续需要更高吞吐时再换长度前缀/二进制协议。

### 4.1 请求

```json
{"version":1,"type":"exec","command":"/bin/bash","args":["-lc","..."],"env":{},"cwd":"/workspace","tty":false,"marker":"exec-..."}
{"version":1,"type":"stdin","session_id":"...","data":"..."}
{"version":1,"type":"stdin_eof","session_id":"..."}
{"version":1,"type":"resize","session_id":"...","rows":24,"cols":80}
{"version":1,"type":"signal","session_id":"...","signal":"TERM"}
{"version":1,"type":"kill","session_id":"...","tree":true}
{"version":1,"type":"kill_tree","marker":"..."}
{"version":1,"type":"list"}
{"version":1,"type":"ping"}
{"version":1,"type":"shutdown"}
```

### 4.2 响应帧

```json
{"type":"started","session_id":"..."}
{"type":"stdout","session_id":"...","data":"<base64>"}
{"type":"stderr","session_id":"...","data":"<base64>"}
{"type":"exit","session_id":"...","code":0}
{"type":"error","message":"..."}
{"type":"killed"}
{"type":"processes","processes":[...]}
{"type":"pong","sandbox_id":"...","capabilities":["exec","interaction","kill-tree","process-list","sandbox-identity"]}
```

- 一个 exec session 一个连接；连接断开不会杀进程（支持 detach/attach 语义），daemon 在 context 取消时显式 `kill`。
- `kill_tree` 先按 manager 中的 marker 找进程组，再扫描 `/proc/<pid>/environ` 的 marker 环境变量，覆盖已脱离原进程组的后代。
- `marker` 由 daemon 生成/传入；Stage 1 沿用现有 execution marker 字符串。

### 4.3 握手与身份

- `pong` 承载握手：`sandbox_id` 是 init 所属 sandbox（来自 driver 已有的 `SANDBOX_ID`
  环境变量，不新增命令行参数，避免新 daemon 让旧 init 拒绝启动），`capabilities` 是
  additive 能力列表。
- 新增能力**不 bump `Version`**：daemon 与 guest image 独立升级，旧 init 不认识新请求时
  返回 `unknown init request`，客户端按“无该能力”降级；旧 init 的 `pong` 没有这两个字段，
  daemon 视为 legacy（不强制身份）而不是失败。
- daemon 在就绪等待里用 `VerifySandbox` 校验 `sandbox_id`：不是目标 sandbox 就立即失败，
  不把它当成可重试的超时。
- init 启动即 `PR_SET_DUMPABLE=0`：workload 不能通过 `/proc/1/mem`、`/proc/1/environ`
  读取 init 持有的凭据；exec 出的子进程 dumpable 恢复默认，不影响 workload 自身调试。

---

## 5. 进程生命周期

### 5.1 main 进程

- `--main-shell` 是**可选的**：普通 sandbox 不启动 tail/sleep 之类的 keepalive，init 自己就是 sandbox 的长期进程。
- 只有需要真实 main 的场景（当前是 Jupyter）才由 init 启动 `/bin/sh -lc <command>`；Jupyter 的 stdout/stderr 直接继承 init，端口绑定/健康检查不变。
- 没有 main 时，init 的 accept loop 保持 sandbox 存活；所有命令都是 init 的 `exec` 子进程。
- main 退出后 init 执行 graceful shutdown，并以 main 的 exit code 退出。
- **init 自身被杀死时，sandbox 必须退出**：`ac-init` 是 PID 1，内核会终止 PID namespace 中剩余进程；daemon 不得在 init 退出后继续把该 sandbox 当作可用。

### 5.2 exec/attach

- `exec` 由 init 直接 `fork/exec`；非 TTY 使用 stdin/stdout/stderr pipes，TTY 使用 PTY。
- init 用 `wait4(-1)` 统一回收：包括 main、exec、以及被 reparent 到 PID 1 的孤儿。若 init 不是 PID 1（测试/包装进程），会设置 `PR_SET_CHILD_SUBREAPER`，让孙进程仍被 reparent 到 init。
- 子进程设置独立进程组（非 TTY `Setpgid`，TTY `Setsid`），终止时按进程组发信号。
- 信号：TERM/INT/HUP/KILL；`kill` 默认杀进程组，`kill` tree=false 只杀 leader（对应 `KillOnlyOnCancel`）。
- `list` 返回 init 当前管理的进程；用于诊断，不参与策略。

### 5.3 断连

- exec 连接断开时，init 不杀进程；输出被 drain 到 `io.Discard`，避免子进程因 pipe 满阻塞。
- 非交互 `Exec` 没有 stdin 输入面，init 客户端在启动命令后立即发送 `stdin_eof`，与 `docker exec` 的空 stdin 语义一致。
- TTY 会话的 stdin EOF 以 EOT（Ctrl-D）写入 PTY，不关闭 master；若程序在 raw 模式下忽略 EOT，500ms 后关闭 master 兜底，避免 CloseSend 永久阻塞。
- leader 被 `wait4` 回收后，退出帧只在有界输出宽限期（250ms）内等待 stdout/stderr EOF；后台后代继续持有时，输出转入 discard，退出帧仍会下发，exec 不再被后代生命周期绑定。
- daemon 的 `Exec`/`ExecStream` 在 context 取消时调用 `kill`（tree 与否取决于 `KillOnlyOnCancel`）；非 `KillOnlyOnCancel` 时还会按 marker 调用 `kill_tree`，清理 `agent-compose-runtime` 放到独立进程组的真实命令及其后代。
- init 关闭时会等待 reaper 就绪退出，避免同一进程内多个 init 实例（测试/wrapper）之间用 `wait4(-1)` 互相偷取子进程退出。
- 交互式 `OpenInteraction` 的 cancel 先发 TERM，再关闭 session；最终由 init/容器停止兜底。
- `docker stop` 给 PID 1 发 SIGTERM；init 把 TERM 转发给所有进程组，并在 PID 1 身份下向整个 PID namespace 发 TERM（回收未登记的孤儿），等待 5s，再 SIGKILL。

---

## 6. Daemon 集成与灰度

### 6.1 Feature flag

```text
SANDBOX_INIT_ENABLED=false            # 默认关闭
SANDBOX_INIT_BINARY=/.agent-compose/ac-init
SANDBOX_INIT_SOCKET_DIR=/run/agent-compose  # 可选
```

- `SANDBOX_INIT_ENABLED=true` 影响**新创建**的 Docker/K8s/BoxLite/Microsandbox sandbox。
- 创建时的进程模型会写入 sandbox 的 `VMState.SandboxInitState` 并与运行时一起持久化。停止/重启/resume 重造 runtime 时以记录为准，因此切换 flag 不会改变已有 sandbox 的模型；Docker/K8s 的 live label/annotation 仍用于判定现有 runtime 的真实控制方式。
- 关闭 flag 后新建 sandbox 回到各 driver 原有路径；运行中（或已停止但记录了 init）的 init sandbox 仍走 init。
- 启用 flag 但 guest image 没有 `/.agent-compose/ac-init` 时，sandbox 启动失败并快速报错；不会静默回退。
- 启用 flag 时 daemon 启动阶段会按最长 64 字节 sandbox ID 校验 `SANDBOX_INIT_SOCKET_DIR` 的最坏路径长度，避免运行到 resume 才失败。

### 6.2 各 driver 启动

**Docker**

```text
Entrypoint = /.agent-compose/ac-init
Cmd        = --control-socket /run/agent-compose/init.sock
             [--main-shell <jupyter command>]   # 仅 Jupyter
HostConfig.Init = false
Labels[agent-compose.sandbox_init] = true
Mount $SANDBOX_INIT_SOCKET_DIR/<sandbox-id> -> /run/agent-compose (rw)
```

没有 Jupyter 时不传 `--main-shell`，init 自己保持容器存活；不创建 `tail -f /dev/null`。

**Kubernetes 的外层默认拒绝（enforce）**

enforce 且策略由 forward proxy 中介时，k8s driver 会为每个 sandbox 建一个
`NetworkPolicy`（Pod 为 owner）：`policyTypes: [Egress]`，只放行 engine endpoints
（facade/telemetry/proxy）的 CIDR+端口，其余 egress 默认拒绝。这样即使进程不是
ac-init 启动的（例如 `kubectl exec`、探针或未来新增的入口），也无法绕过声明；
声明的 allow/deny 仍由 forward proxy 判定。它要求集群 CNI 真正执行
NetworkPolicy（k3s 已实测）；admission 的 `kubernetes_network_policy` 机制会披露
这一依赖，RBAC 不允许创建时 sandbox 启动失败而不是静默降级。

**Docker 的外层默认拒绝（enforce，strict network profile）**

`SANDBOX_INIT_EGRESS=enforce` 配合 `DOCKER_SANDBOX_NETWORK_STRICT=true` 时，Docker
driver 要求 `DOCKER_SANDBOX_NETWORK` 指向一个已存在的、用户创建的 internal 网络
（`docker network create --internal ...`，inspect 的 `Internal=true`），并且只把
sandbox 容器挂到该网络。internal 网络没有离开网桥的路由，因此容器网络命名空间内的
每个进程（包括 daemon 未启动的 `docker exec` 进程）都默认无法访问外部目标；声明的
allow/deny 仍由 forward proxy 判定。

- 失败关闭：网络未配置、是内建网络（`host`/`none`/`bridge`/`default`）、不存在、
  不是 internal、容器化 daemon 未挂载到它，或 `AGENT_COMPOSE_RUNTIME_BASE_URL`
  不是该网络上的 daemon 别名/地址/网关时，sandbox 启动直接失败，不会回退到 daemon
  或默认网络。
- daemon 可达性：容器化 daemon 必须同时挂到该 internal 网络并保留可解析的别名
  （`docker-compose.sandbox-network.yml` 的 `agent-compose` 别名即可）；
  `AGENT_COMPOSE_RUNTIME_BASE_URL` 必须指向该别名、daemon 在该网络上的地址，或
  该网络的网关地址。strict 模式不会为每个 sandbox 生成网关重写。
- 该网络放行：同网络上的 daemon 端点、宿主机网桥网关、以及同网络的其他容器；
  其他目标一律拒绝。Docker 内嵌 DNS（127.0.0.11）仍会应答查询，因此这一层拒绝的是
  可路由目标，而不是 DNS 查询名。
- 该开关只在 enforce 模式读取；audit/off 保持原网络位置不变。启用 strict 之前创建
  的 sandbox 保留旧网络，start 时会失败关闭，必须删除并重新创建。
- admission 的 `docker_internal_network` 机制会披露该层及其放行范围。

**Kubernetes**

```text
Pod container Command = [/.agent-compose/ac-init]
Args = --control-socket /run/agent-compose/init.sock
       [--main-shell <jupyter command>]
Labels[agent-compose.sandbox_init] = true
```

Pod 内不暴露 host 路径；daemon 通过 `pods/exec` 运行 `/.agent-compose/ac-init --client --control-socket ...`，把 JSON 帧经 stdin/stdout 桥接到 init。control client 只是控制 shim，不是 workload；命令仍由 init fork/exec。

**BoxLite**

```text
Entrypoint = /.agent-compose/ac-init
Cmd        = --control-socket /.agent-compose/run/init.sock
             [--main-shell <jupyter command>]
Bind mount $SANDBOX_INIT_SOCKET_DIR/<sandbox-id> -> /.agent-compose/run (rw)
```

**Microsandbox**

```text
WithInit(Init.Cmd("/.agent-compose/ac-init", Args:
  --control-socket /.agent-compose/run/init.sock
  [--main-shell <jupyter command>]))
Control: sandbox-host 通过 microsandbox exec 运行
         /.agent-compose/ac-init --client --control-socket /.agent-compose/run/init.sock，
         把 JSON 帧经 stdin/stdout 桥接到 init。
```

Microsandbox 的 virtiofs 共享目录不会把 guest 内创建的 Unix socket 暴露给宿主（宿主看到的是普通文件），因此不能使用 host bind socket。控制 socket 只存在于 guest 的 `/.agent-compose/run/init.sock`，daemon/sandbox-host 通过 guest exec 的 `ac-init --client` 访问；workload 仍由 init fork/exec。Docker/BoxLite 使用 host-visible socket；K8s 也使用 guest-exec 桥。

Docker/BoxLite 的 host 控制目录由 daemon/sandbox-host 创建为 0700，socket 为 `init.sock`（0666，目录是访问门禁）。`SANDBOX_INIT_SOCKET_DIR` 默认 `$XDG_RUNTIME_DIR/agent-compose`，否则 `/run/agent-compose`；启用 flag 时启动阶段按最长 sandbox ID 校验路径，运行期仍做兜底检查，超限直接报错，不使用 hash/symlink 回退。

### 6.3 Ready 与错误

- `EnsureSandbox` 在 sandbox 启动后 `Ping` init，直到 `SANDBOX_START_TIMEOUT`。
- Docker/K8s 的 init 进程/Pod 提前退出、socket 未出现、协议版本不匹配 → 清理本次 attempt 并返回错误；不会静默回退到原生 exec。
- VM driver 的 init 退出等同于 VM/sandbox 退出；`EnsureSandbox`/`Exec` 返回明确错误，不能继续复用。
- 旧 sandbox（没有 init 标记）不等待，不受影响。

### 6.4 Exec 路径

| daemon 方法 | init 容器 | legacy 容器 |
| --- | --- | --- |
| `Exec` / `ExecStream` | `init client Exec`，帧转换为 `ExecChunk` | `docker exec` |
| `OpenInteraction` | PTY/pipes over init | `docker exec` attach |
| `TerminateProcessTree` | `kill_tree` by marker | marker control exec |
| `SignalGuestRuntime` | init exec of signal script | docker exec signal script |

`ExecResult` 的 `Stdout`/`Stderr`/`Output`/`SecurityFacts` 语义保持不变；`dockerExecCollector` 复用，UTF-8 解码和 security fact 过滤不重复实现。

---

### 6.5 多 driver 一致性

Stage 1 的契约对所有 driver 相同：

| Driver | init 角色 | 控制 transport | sandbox 退出条件 |
| --- | --- | --- | --- |
| docker | 容器 PID 1 | host bind mount 上的 Unix socket | init 退出 / 容器 stop |
| k8s | Pod 容器 PID 1 | `pods/exec` 上的 `ac-init --client` 桥 | init 退出 / Pod 终止 |
| boxlite | VM guest PID 1 | sandbox-host 与 guest 的控制通道（Unix socket/vsock） | init 退出 / VM 停止 |
| microsandbox | VM guest PID 1 | guest exec 上的 `ac-init --client` stdio 桥 | init 退出 / VM 停止 |

设计原则：

1. **控制协议不变。** 帧格式、session、stdin/resize/signal 语义在所有 driver 完全相同；driver 只负责 transport。
2. **init 不依赖 Driver API。** init 只依赖控制协议、`/proc`、`wait4`；Docker/K8s/VM 的概念不进入 init。
3. **同一个二进制。** 所有 driver 使用 `/.agent-compose/ac-init`；不允许某个 driver 有自己的 init 变体。
4. **没有 keepalive。** 没有 Jupyter 时 init 自身保持 sandbox 存活，不启动 `tail`/`sleep` 占位进程。
5. **init 死亡即 sandbox 死亡。** init 是 PID 1；daemon 检测到 init 退出后必须把 sandbox 标记为失败，不能把仍存在的进程当作可用。
6. **所有 workload 都是 init 子进程。** 原生 `docker exec`/SDK exec 只用于调试或控制 shim（如 K8s `--client`），不作为 workload 启动路径。
7. **daemon 与 sandbox 解耦。** daemon 只依赖 `sandboxinit.Client` 和 driver 的 transport 适配器；后续新增 driver 只实现 control transport 和 sandbox 启动参数。

K8s 的 `--client` shim 是唯一允许的“额外 guest 进程”例外：它由 daemon 通过 `pods/exec` 启动，只负责把 stdio 转发给 init，不是 workload；所有真实命令仍由 init 启动并回收。

## 7. Stage 2：seccomp / Landlock / 进程边界（设计，未实现）

Stage 1 只建立进程所有权。Stage 2 的目标是在 init 的 child launch 路径上增加隔离，而不是另起一套 executor。

### 7.1 扩展点

`pkg/sandboxinit.Hooks` 已经预留：

```go
type Hooks struct {
    Prepare    func(*exec.Cmd) error
    AfterStart func(pid int) error
}
```

Stage 2 的 `Prepare` 会在 `fork/exec` 前完成：

1. 校验并解析一份 driver 提供的、guest 不可篡改的 security profile；
2. 设置 child 的 UID/GID、补充组、`no_new_privs`；
3. 打开 Landlock ruleset 的 path FDs，安装 Landlock；
4. 安装 seccomp BPF（或 audit 模式）；
5. 设置 cgroup（pids/CPU/memory）、rlimit、core dump、dumpable；
6. 记录实际生效的 profile（audit/OCSF），失败则拒绝启动。

`AfterStart` 用于在 child 启动后做验证/观测，例如读取 `/proc/<pid>/status` 确认 `NoNewPrivs`、`CapEff`、`Seccomp`，不一致则杀掉 child 并报错。

### 7.2 Profile 来源与信任边界

- profile 由 daemon 生成，随 sandbox 创建写入 control dir（只读挂载）或通过控制协议下发；
- init 只接受 daemon 签名的/带 sandbox ID 的 profile，guest 内的 agent 不能修改；
- profile 变更需要新 init generation 或显式 `reload`；不能由 workload 进程发请求；
- 如果 profile 缺失/解析失败：`enforce` 模式下拒绝启动；`audit` 模式下允许但记录 high-severity finding。

### 7.3 seccomp 设计

- 默认继承现有 Docker/K8s 的 seccomp，不立刻替换；Stage 2 增加自己的 allowlist filter；
- 只允许 workload 需要的 syscall（按 agent runtime 家族：codex/pi/JS runtime 的差异通过 profile 选择）；
- 强制阻断：`ptrace`、`process_vm_*`、`pidfd_getfd`、`bpf`、`mount`、`unshare(CLONE_NEWUSER)`、`io_uring`、`userfaultfd`、`perf_event_open`、`memfd_create`（或按 profile）；
- 支持 `audit`（记录但放行，使用 `SECCOMP_RET_LOG` 或用户态审计）和 `enforce`（`EPERM`/`ENOSYS`）；
- 非特权安装：`PR_SET_NO_NEW_PRIVS`；
- 架构：x86_64 + aarch64 分别编译 filter；未知架构 fail closed；
- 失败语义：filter 安装失败不能退回无 seccomp；拒绝启动。

### 7.4 Landlock 文件系统隔离（MVP 已实现，opt-in）

当前分支交付的是 **enforce-only、默认关闭** 的 MVP，不包含 profile 文件、audit 模式和项目自定义路径。

- 配置：`SANDBOX_INIT_LANDLOCK=off|enforce`（默认 `off`）和
  `SANDBOX_INIT_LANDLOCK_FALLBACK=fail|warn`（默认 `fail`）。daemon 解析后随 sandbox
  环境注入，init 读取；老 guest 的 init 忽略这些变量。
- 安装位置：init 在启动时解析策略；每次 exec 由 `--exec-child` trampoline 在
  `PR_SET_NO_NEW_PRIVS` 和 capability bounding-set drop 之后、`execve` 之前调用
  `landlock_restrict_self`。规则随 exec 和后代继承，workload 无法移除。
- baseline 规则（全部为 deny-by-default）：
  - 只读+可执行：`/usr`、`/bin`、`/sbin`、`/lib*`、`/opt`；
  - 只读：`/etc`、`/var`、`/sys`，以及 `/etc/resolv.conf`、`/etc/hosts`、
    `/etc/hostname` 等常见 bind mount 文件；
  - 读写：`$WORKSPACE`、`$HOME`、`$STATE_ROOT`、`$RUNTIME_ROOT`、`$LOG_ROOT`、
    `$TMPDIR`、`/var/tmp`、`/dev`、`/proc`（默认值分别对应
    `/workspace`、`/root`、`/data/state`、`/data/runtime`、`/data/logs`、`/tmp`）；
  - 其余路径（包括 `/.agent-compose` 和 `/run/agent-compose`）一律拒绝。
- ABI：按内核版本选择 handled access；ABI≥2 才包含 `REFER`、ABI≥3 才包含
  `TRUNCATE`。ABI≥1 即可用；不设置 `handled_access_net`，因此不改变 TCP/DNS/egress
  路径（Landlock 网络权限与 init 的 seccomp egress 互不替代）。
- 不支持的内核（`ENOSYS`/`EOPNOTSUPP`/ABI 0）：`fallback=fail` 时 init 启动失败；
  `fallback=warn` 时记录告警并以无 Landlock 模式继续，pong **不会**广告
  `landlock-enforce`。daemon 在 `fallback=fail` 时要求 ready 握手里出现该 capability，
  否则 sandbox 启动失败；`warn` 时只记录降级告警。
- 已知限制：Landlock 不约束 `connect(2)` 到 `AF_UNIX`，因此无法保护控制 socket；
  `/dev`、`/proc` 为兼容性放宽；不支持按 exec/按项目的额外规则；不处理
  `IOCTL_DEV`（保持不限制）。真正把控制面移出 workload 可信域需要独立 supervisor，
  见 [sandbox_supervisor_isolation](sandbox_supervisor_isolation.zh-CN.md)。

```text
SANDBOX_INIT_LANDLOCK=off|enforce
SANDBOX_INIT_LANDLOCK_FALLBACK=fail|warn
```

### 7.5 进程身份与 cgroup

- init 已经知道每个 exec 的 leader、进程组和 marker；Stage 2 可把 `(pid, exe, digest, cgroup)` 作为 `started` 帧的扩展字段；
- 每个 workload session 放入独立 cgroup（或统一 sandbox cgroup 的子组），限制 pids/CPU/memory；init 负责清理；
- 进程退出/孤儿清理时移除 cgroup；容器/VM 停止时兜底。

### 7.6 配置与灰度

已实现（默认关闭）：

```text
SANDBOX_INIT_LANDLOCK=off|enforce
SANDBOX_INIT_LANDLOCK_FALLBACK=fail|warn
```

仍为设计（未实现）：

```text
SANDBOX_INIT_SECCOMP=off|audit|enforce
SANDBOX_INIT_CGROUP=off|on
SANDBOX_INIT_PROFILE=/.agent-compose/security.json
```

- Landlock 首版默认 `off`，不影响 Stage 1；启用后先在无关键数据的 sandbox 上验证
  workspace 写入、系统目录读取和 agent/provider CLI 启动；
- 严格部署使用 `fallback=fail`（默认）；只有明确接受“无文件系统隔离但 sandbox 可运行”
  时才切 `warn`；
- seccomp/cgroup/profile 仍按后续阶段推进，先 audit 再 enforce；
- 每次 profile 变更都要有 digest/命令审计；失败时 fail closed；
- 与 egress/credential 的集成仍由 daemon 侧负责，init 只做进程边界。

### 7.7 Egress audit（PR2）

Stage 1 之后的第一个 seccomp 能力是**观察**而不是改写：

- 每次 exec 由 `ac-init --exec-child` trampoline 先在 fork 后、exec 前安装 filter：
  `connect` 返回 `SECCOMP_RET_USER_NOTIF`，`io_uring_setup` 返回 `EPERM`（io_uring
  的 connect 不经 seccomp，必须先堵住）；
- trampoline 通过 init 建好的 socketpair 用 `SCM_RIGHTS` 把 listener 交给 init，
  然后 `execve` 目标；filter 随 exec 与后续 fork 继承，workload 无法移除；
- init 每个 exec session 一个 goroutine 读 listener：校验 notification、用
  `process_vm_readv` 读取目标 sockaddr、记录 `sandbox/session/marker/pid/family/
  host/port/unix_path`，然后 `SECCOMP_USER_NOTIF_FLAG_CONTINUE` 放行；
- audit 模式**不改变 syscall 结果**；策略判定仍在 daemon。filter 安装失败时
  audit 模式记录告警并继续（观测不能变成可用性故障）；如果 filter 已安装但
  listener 交接失败，trampoline 拒绝 exec，避免 workload 的所有 connect 变成 ENOSYS。

配置与前提：

```text
SANDBOX_INIT_EGRESS=off|audit
```

- 需要 guest 内核 >= 5.5（CONTINUE）且支持 `SECCOMP_RET_USER_NOTIF`；init 启动时
  在 guest 内探测，不满足则记录告警并退回 off，`egress-audit` 能力不会出现在 pong 里；
- workload 与 init 同 uid 时，init 可用 `process_vm_readv` 读 sockaddr；后续 enforce
  路径需要 `CAP_SYS_PTRACE`/降权模型（PR3 讨论）；
- audit 只覆盖 `connect`；UDP `sendto`、DNS 重定向在 PR4 实现。

### 7.8 Egress mediation（enforce，PR3）

enforce 模式在 audit 的拦截基础上把连接改道到 daemon egress proxy：

- init 用 `pidfd_getfd` 复制 workload 正在 connect 的 socket（同一个 open file
  description，target 的 fd/flags/epoll 注册都不变），由 init 在复制 fd 上完成
  `connect(proxy)` 与 `CONNECT host:port HTTP/1.1` 握手，然后向目标线程 spoof
  connect 结果：200 → 成功，403/407/超时/不可达 → `ECONNREFUSED`（fail closed，绝不回退直连）；
- init 从 `HTTP_PROXY`/`HTTPS_PROXY` 读取 daemon 发放的 credential（`sandbox-id:token`），
  随后把六个 proxy 环境变量从自身环境 unset；workload 由 init fork/exec，因此**不会继承**
  proxy credential。CONNECT 请求带 `X-Agent-Compose-Sandbox/Session/Marker` 身份头
  （daemon 侧消费在 PR6）；
- 不经过 proxy、直接连接的目的地：`AF_UNIX`、loopback、沙箱自身接口地址、proxy 自身地址；
  非 DNS 的 UDP 一律拒绝；DNS（UDP/TCP 53）被重定向到沙箱内策略 DNS（见 7.9）；
- Docker/Kubernetes 的 sandbox 容器在 enforce 时加 `CAP_SYS_PTRACE`（Docker 默认
  seccomp profile 只在该 cap 下允许 `pidfd_getfd`）；trampoline 在 exec 前把
  `SYS_PTRACE/SYS_ADMIN/NET_ADMIN/NET_RAW/BPF/PERFMON/SYS_MODULE` 从 workload 的
  bounding set 去掉，workload 无法用这些能力绕过拦截或读取 init 内存；
- enforce 是 **default-deny**：项目未声明 policy 时 daemon 生成合成的 deny-all 声明并只
  自动放行 engine endpoints（facade/Jupyter/telemetry/proxy），因此不会因为“没写策略”
  而退回无限制；admission 同样把这类 sandbox 报成 default deny + `seccomp_redirect`。
- enforcing policy 已通过 proxy 下发（`MediatedByForwardProxy`）时，daemon 在 ready
  握手里要求 init 广告 `egress-mediate` 与 `egress-hardening`；guest 内核不支持/旧 init/
  未启用代理凭据导致 init 实际 off 时，**sandbox 启动失败**，不会接受一个无法强制的
  sandbox。proxy 不可达属于另一条路径，由 `EGRESS_UNENFORCEABLE_POLICY=warn|refuse`
  决定（strict 部署应设为 `refuse`）；
- 已知限制：需要 `CAP_SYS_PTRACE` 的集群（受限 PodSecurity）不可用；`getpeername`
  会返回 proxy 地址；proxy-aware 客户端如果保留 proxy env 会直连 proxy（init 的 bypass
  路径），enforce 下我们已 unset，因此不会发生。

### 7.8.0 独立 supervisor（Docker，opt-in）

`SANDBOX_SUPERVISOR=enforce` 让 Docker workload 使用 `NetworkMode=none`，只保留 lo；每个
sandbox 由 daemon 启动一个独立的 host 进程 `ac-supervisor`，通过 host UDS 接收 init 的
中继连接。init 用 per-sandbox token 认证，token 经认证的 init 控制通道下发，不进入
container 环境。supervisor 把普通 egress 转发给 daemon forward proxy（策略判定仍在
daemon），engine endpoints 直连。`SANDBOX_SUPERVISOR_UID/GID` 可让 supervisor 以非 root
运行（需把 binary 放到非 root 可执行路径，daemon 在启动时临时给 socket 目录授权，握手
完成后恢复 root-only）。实测 workload 仅 lo、允许 200、外部 blocked、LLM prompt OK，
supervisor 进程以 uid 1000 运行。限制：仅 Docker；Jupyter 与该模式不兼容；K8s/Micro 仍用
CNI/SDK 网络策略。

### 7.8.1 非 root workload（opt-in）

`SANDBOX_INIT_WORKLOAD_UID/GID`（必须同时设置，非 0）让 init 在每次 exec 的
trampoline 中 `setgroups/setgid/setuid` 到该数字身份；PID1/控制 socket/代理握手仍以
root 运行，因此 `pidfd_getfd` 和 seccomp listener 不受影响。init 会在启动和每次 exec 前
把 `/workspace`、`/root`、`/data/{state,runtime,logs}` chown 给该身份；控制目录
`/.agent-compose` 保持 root-only。默认 0/0 保持历史 root workload。Docker 实测
`uid=1000 gid=1000`、workspace 可写、`/.agent-compose` Permission denied、代理放行 200。

### 7.9 Policy DNS（PR4）

enforce 模式下 init 在 `127.0.0.1:53`（以及 `::1:53`，若 guest 支持）同时监听 UDP 与
TCP DNS：

- 发往任意地址 `:53` 的 connect（包括 Docker 内嵌 DNS 127.0.0.11、外部 8.8.8.8）
  都会被改连到 stub；UDP/TCP 均支持，AF_INET6 socket 重定向到 `::1:53`；
- A 查询返回 `198.18.0.0/15` 合成地址（TTL 30s），AAAA 返回空 answer，让应用的
  双栈 resolver 回退 IPv4；非 A/AAAA 查询返回空 NOERROR；
- stub 维护 `hostname ↔ 合成 IP` 映射；connect 到合成 IP 时还原 hostname，CONNECT
  发 `hostname:port`，因此 daemon 的域名 allow/deny、`allowed_ips` 按声明生效；
  未映射的合成地址直接拒绝（不直连）；
- engine 自己的 hostname（`AGENT_COMPOSE_RUNTIME_BASE_URL` 与 proxy host）是
  passthrough：stub 返回真实地址，facade/Jupyter 不被当作普通 egress 处理；
- 不做 `/etc/resolv.conf` 改写：拦截 `:53` 的 connect 已经覆盖系统 resolver，
  同时避免 init 自己的解析被 stub 递归劫持。副作用是明确使用 unconnected UDP
  `sendto` 的 resolver 仍可能绕过 stub，PR5 处理；
- stub 启动失败（例如端口被占用）只记录告警，TCP 改道仍工作，但域名策略退化为
  IP 策略；`egress-dns` 能力不会出现在 pong 里。

### 7.10 Identity、审计与报告（PR6）

- init 在 CONNECT 请求上带 `X-Agent-Compose-Sandbox/Session/Marker/Pid`；daemon
  校验 Sandbox 头与 proxy credential 一致，其余字段作为归因数据进入
  `egress.Request`，不参与策略判定；
- 设置 `EGRESS_AUDIT_LOG=/abs/path.jsonl` 后，forward proxy 的每条 allow/deny 都会
  追加一行 JSON（sandbox、destination、resolved IPs、action、rule、generation、
  session/marker/pid、时间）；写失败只记录错误，不改变决策；
- admission 报告在 `SANDBOX_INIT_EGRESS=enforce` 时新增 `seccomp_redirect` 机制，
  并替换掉“只对遵守 proxy env 的进程生效”的旧限制说明，明确 init 会重定向所有
  TCP connect，同时披露 DNS stub 的 `sendmmsg` 残余缺口；
- per-run 策略（按 scheduler run / interactive session 选择不同策略）仍留到后续：
  当前 session/marker 只用于审计，不改变生效策略。

### 7.11 加固与 per-run 钩子（PR7）

- enforce 模式在 listener 交接完成后，再安装一个**无 listener 的 hardening filter**
  （叠加在通知 filter 之上，只收紧不放松），对 `unshare`、`setns`、`mount`、
  `umount2`、`pivot_root`、`bpf`、`perf_event_open`、`kexec_*`、`userfaultfd`、
  `ptrace`、模块加载、`open_by_handle_at`、`process_vm_*`、`pidfd_getfd` 返回
  `EPERM`；workload 无法移除已叠加的 filter；
- `sendmmsg`/`recvmmsg` 故意不在拒绝列表里：glibc 的 `getaddrinfo` 会使用它们，
  硬拒绝会破坏正常域名解析；因此“使用 sendmmsg 的 resolver 可绕过策略 DNS”仍是
  已披露的残余，后续用第二个通知 listener 收敛；
- per-run 策略钩子：proxy 新增可选 `EgressProxySessionPolicySource`，按
  session/marker 取策略；当前 adapter 返回 sandbox 级策略，per-run 存储落地时
  只需替换这个实现，proxy 与 init 协议不用改；
- workload 非 root 化仍**未实现**：它需要 workspace/chown、Jupyter、provider
  runner 权限矩阵的整体设计，作为后续 hardening 项；
- init 在 pong 里额外广告 `egress-hardening` 能力。

### 7.12 Stage 2 测试

- 单元：profile 校验、filter 编译、Landlock ruleset、cgroup 路径、失败语义；
- 集成：allow/deny syscall probe、文件读写 probe、fork/exec、pids limit、进程退出清理；
- 真机：Docker/K8s/Microsandbox/BoxLite 各自跑 agent/codex/pi/JS runtime 的 smoke；
  - Docker kill-init 用例在 kill 前启动写入 host workspace 心跳的后台 workload，确认 init 死后心跳停止；
  - exec 用例覆盖后台后代持有管道、非交互 stdin EOF、TTY EOT 和 marker kill_tree 的退出帧/进程消失断言。
- 负向：`ptrace` 被拒、读 `/.agent-compose` 被拒、超出 cgroup 被拒、profile 篡改被拒；
- 性能：seccomp/Landlock 对 exec/启动延迟的影响。

---

## 8. 安全与边界

- Stage 1 的 init 与当前 sandbox 一样以容器用户运行；它不是新的权限边界，只是**进程所有权/控制面**。
- init 二进制放在 `/.agent-compose/ac-init`（目录 0700、文件 0755），不在 PATH 中；Stage 2 的 profile 也规划放在 `/.agent-compose/`，避免和普通 workload 文件混在一起。Stage 1 仍以容器用户运行 init，所以这只是目录规整，不是安全边界。
- 直接 `docker exec` / `docker compose exec` 不使用 init 控制协议：Docker 仍能进入容器，但该进程的 PPID 在容器 PID namespace 中显示为 0，不属于 init 的 workload 树；调试可以用，daemon 的正式 exec 路径不会走它。
- 控制 socket 在 sandbox 内可见；agent 本来就能 `exec`/kill 自己的进程，因此不构成额外提权。目录 0700 + socket 0666 是为了让非 root 的 native daemon 能连接 root init 创建的 socket。
- **控制目录加固（已实现）**：init 新建控制目录用 0700，并拒绝 group/world-writable
  的现有目录；`/.agent-compose` 等 init binary 所在目录若 group/world-writable，启动时
  记录告警。这些检查降低 socket/binary 被替换的风险，但不是新的 uid/namespace 边界。
- 控制协议没有额外认证：能访问 socket 等价于能控制该 sandbox 的进程；socket 只在 sandbox 的 bind mount 内。Landlock 也不能阻止 `AF_UNIX` connect，因此控制面隔离仍依赖独立 supervisor（设计见
  [sandbox_supervisor_isolation.zh-CN.md](sandbox_supervisor_isolation.zh-CN.md)）。
- 启用 Landlock enforce 后，init 仍与 workload 同容器、同用户；Landlock 只约束
  workload 的文件系统视角，不改变 init 的权限边界。真正的 boundary 需要独立
  supervisor + 认证的控制通道。
- 后续阶段可在 `Hooks.Prepare` 中注入 seccomp/cgroup/资源限制；那时 init 会成为真正的 boundary，需要重新评估 socket 认证、降权和进程身份。

---

## 9. 构建与镜像

- `cmd/ac-init`：Linux 静态二进制；`task build:ac-init` 生成 `build/ac-init`。
- `guest-images/Dockerfile.agent-compose-guest` 与 Arch 版本新增 `sandbox-init-builder` stage，把二进制装到 `/.agent-compose/ac-init`。
- `SANDBOX_INIT_BINARY` 可覆盖 guest 路径，便于测试镜像/自定义镜像。
- `task build` 不依赖 init binary；只有 guest image 与 init 相关测试需要。

---

## 10. 测试

Stage 1 已包含：

- `pkg/sandboxinit`：
  - exec 的 stdout/stderr/exit；
  - ping/list；
  - TTY；
  - marker `kill_tree`。
- `pkg/config`：默认关闭、环境读取、非法值/相对路径拒绝。
- `pkg/driver`：init label 识别、control dir/socket 路径、mount 创建、默认 binary、VMState 进程模型持久化、exec 取消时的 marker tree 清理。

建议后续补：

- 真实 Docker 的 e2e：启用 flag，确认容器 PID 1 是 init、`docker top` 中 main/exec 都是 init 子进程、`docker stop` 能回收。
  - 已加入一个 opt-in 的 `TestDockerSandboxInitContainerLifecycle`：验证 init label/entrypoint、`Exec`、`Exec` 的 PPid=1、交互式 stdin。设置 `AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE` 运行。
- 交互式 attach 的 stdin/resize/signal e2e。
- 并发 exec、大量子进程、孤儿回收、marker 清理。
- 与现有 `docker exec` 调试路径的对照（同一套 run/命令结果一致；正式 workload 不走它）。

---

## 11. 后续阶段

1. **Seccomp/Landlock hook**：在 `Hooks.Prepare`/`AfterStart` 中接入，先 audit 再 enforce；init 负责把策略传给 child。
2. **进程身份**：init 已知每个 exec 的 leader/进程树，可在后续与 `/proc`/cgroup 结合，提供 binary identity。
3. **资源限制/cgroup**：把 main/exec 放进统一 cgroup；init 负责清理。
4. **文件系统/权限**：降权、只读 rootfs、`no_new_privs`。
5. **网络/凭据**：与 egress gateway/credential broker 集成；init 只保留“进程树 + 本地控制”职责，网络仍由 daemon 代理决定。

---

## 12. 代码地图

| 主题 | 文件 |
| --- | --- |
| init 入口 | `cmd/ac-init/main.go` |
| 协议/客户端 | `pkg/sandboxinit/{protocol,client}.go` |
| Linux 进程/回收/PTY | `pkg/sandboxinit/{process_linux,server_linux}.go` |
| Landlock 模式/策略/安装 | `pkg/sandboxinit/{landlock,landlock_linux}.go` |
| 控制目录加固 | `pkg/sandboxinit/control_dir_linux.go` |
| Docker 集成 | `pkg/driver/docker_sandbox_init.go`、`pkg/driver/docker_runtime.go` |
| K8s 集成 | `pkg/driver/k8s_sandbox_init.go`、`pkg/driver/k8s_runtime.go` |
| BoxLite/Microsandbox 集成 | `pkg/driver/{boxlite,microsandbox}_runtime.go`、`pkg/driver/sandbox_init_runtime.go` |
| Driver capability | `pkg/driver/sandbox_init_capability.go` |
| 通用 exec/interaction | `pkg/driver/sandbox_init_exec.go`、`pkg/driver/sandbox_init_interaction.go` |
| 配置开关 | `pkg/config/config.go`（`SandboxInitEnabled`/`SandboxInitBinary`/`SandboxInitSocketDir`） |
| 镜像 | `guest-images/Dockerfile.agent-compose-guest` |
| 构建 | `Taskfile.yml` `build:ac-init` |

---

## 13. Stage 1 功能验证记录（2026-10-29）

环境：Linux arm64、Docker、guest image 基于 `chaitin/agent-compose-guest:latest` 注入 `/.agent-compose/ac-init`；LLM 使用 `deepseek-flash` 通过 daemon facade 访问外部 OpenAI Responses / Anthropic Messages 兼容端点。

### 13.1 已验证

| 场景 | 结果 |
| --- | --- |
| `SANDBOX_INIT_ENABLED=true` 创建 Docker sandbox | 容器 PID1 为 `ac-init`；普通 sandbox 没有 tail/sleep keepalive；exec 都是其子进程 |
| 普通 command exec | `Exec` 成功，`/proc/$$/status` 的 PPid 为 1 |
| 直接 `docker exec` | 仍可进入容器执行 `/bin/sh`；PPid 显示 0，绕过 init，仅用于调试 |
| 交互式 stdin exec | `agent-compose exec -i --command 'read ...'` 收到 stdin，Node 正常运行 |
| 并发 exec / 大输出 / 立即退出 | `pkg/sandboxinit` 单测通过（含 `-race`） |
| Codex prompt | 成功，facade 请求 200，返回 `OK` |
| Pi prompt | 成功，返回 `OK` |
| Claude / Anthropic Messages prompt | 成功，返回 `ANTHROPIC_OK` |
| Node.js runtime | `node -p process.version` 在 sandbox 内正常 |
| Jupyter main | Jupyter 作为 init 子进程启动，命令执行成功 |
| detached run | 提交、日志 follow、完成清理均正常 |
| daemon 重启后继续 exec | 运行中的 init sandbox 在 daemon 重启后仍可执行命令 |
| stop / rm | init 收到 SIGTERM，exec 会话取消，容器/记录清理正常 |
| kill init | Docker integration 直接 KILL PID1，容器退出；没有残留 workload 树 |
| `SANDBOX_INIT_ENABLED=false` | 回到 `docker-init + sh/tail + docker exec`，行为与旧版本一致 |
| 启用 flag 但镜像无 init binary | Docker 启动立即失败并返回明确错误，无残留容器 |
| K8s 初始化 Pod | `createPod` 单测验证 Command/Args 为 ac-init + control socket，无 tail；exec bridge 编译通过 |
| BoxLite/Microsandbox 初始化 | 启动参数/控制通道代码路径和带 tag 编译通过；本机没有可启动的 VM 环境，未做真机 smoke |

### 13.2 验证中发现并修复的问题

1. **严格 mount 比较把 init control mount 当作 extra stale bind。**
   第二次 `EnsureSandbox` 或 `IsSandboxAlive` 会因此删除运行中的容器，下一次 exec 得到 EOF。修复：init 容器在比较时把 control mount 加入 expected；stale 删除时清理 per-sandbox runtime 目录。
2. **Unix socket 路径过长。**
   原实现把 socket 放在 `<SANDBOX_ROOT>/<date>/<sandbox-id>/init-control/init.sock`，会超过 108 字节；现改为标准 runtime dir `$XDG_RUNTIME_DIR/agent-compose` 或 `/run/agent-compose` 下的 `<sandbox-id>/init.sock`，不再使用 hash 或短 symlink。
3. **PTY `Ctty` 兼容性。**
   `pty.StartWithSize` 在当前 Go 版本下报 `Setctty set but Ctty not valid in child`；改为 `pty.Open` + `Ctty=0` 后 TTY 测试通过。

### 13.3 未在本机做真机验证

- BoxLite/Microsandbox：本机 `task test:runtime-smoke` 的 BoxLite VM 启动失败，Microsandbox 未继续；启动/控制/exec 路径已按 Stage 1 实现并通过带 tag 编译，但发布前必须在有 KVM 的条件跑真机 smoke。
- 真实 K8s 集群：Pod init + `pods/exec` bridge 已实现并通过单测/编译，未在集群内执行。
- 容器/VM 重启、长时间运行、资源压力、网络策略与 init 的组合；
- Jupyter 浏览器端到端（仅验证进程启动和命令执行）。
