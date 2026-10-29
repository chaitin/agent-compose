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
{"type":"pong"}
```

- 一个 exec session 一个连接；连接断开不会杀进程（支持 detach/attach 语义），daemon 在 context 取消时显式 `kill`。
- `kill_tree` 先按 manager 中的 marker 找进程组，再扫描 `/proc/<pid>/environ` 的 marker 环境变量，覆盖已脱离原进程组的后代。
- `marker` 由 daemon 生成/传入；Stage 1 沿用现有 execution marker 字符串。

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
- daemon 的 `Exec`/`ExecStream` 在 context 取消时调用 `kill`（tree 与否取决于 `KillOnlyOnCancel`）；非 `KillOnlyOnCancel` 时还会按 marker 调用 `kill_tree`，清理 `agent-compose-runtime` 放到独立进程组的真实命令及其后代。
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

### 7.4 Landlock 设计

- 要求 Linux Landlock ABI ≥ 3；不满足时按 profile 的 `hard_requirement` 拒绝启动，或 `best_effort` 记录 finding；
- baseline：只读系统路径 + workspace 读写；
- 额外规则：项目声明的只读/读写路径；
- 规则在 child 的 `pre_exec` 中 `restrict_self`；path FDs 在 `Prepare` 中打开；
- 不能访问控制 socket、`/.agent-compose` 下的 init binary/profile（如果它们不在 baseline）；
- 失败语义：`enforce` 拒绝启动；`audit` 记录但不回退到“无规则”。

### 7.5 进程身份与 cgroup

- init 已经知道每个 exec 的 leader、进程组和 marker；Stage 2 可把 `(pid, exe, digest, cgroup)` 作为 `started` 帧的扩展字段；
- 每个 workload session 放入独立 cgroup（或统一 sandbox cgroup 的子组），限制 pids/CPU/memory；init 负责清理；
- 进程退出/孤儿清理时移除 cgroup；容器/VM 停止时兜底。

### 7.6 配置与灰度

建议新增（默认关闭）：

```text
SANDBOX_INIT_SECCOMP=off|audit|enforce
SANDBOX_INIT_LANDLOCK=off|audit|enforce
SANDBOX_INIT_CGROUP=off|on
SANDBOX_INIT_PROFILE=/.agent-compose/security.json
```

- Stage 2 首版默认 `off`，不影响 Stage 1；
- 先在 Docker 上 `audit`，收集 1–2 个版本的真实 syscall/路径数据，再切 `enforce`；
- Microsandbox/BoxLite/K8s 在接入 init 后按同样顺序验证；
- 每次 profile 变更都有 digest/命令审计；失败时 fail closed；
- 与 egress/credential 的集成仍由 daemon 侧负责，init 只做进程边界。

### 7.7 Stage 2 测试

- 单元：profile 校验、filter 编译、Landlock ruleset、cgroup 路径、失败语义；
- 集成：allow/deny syscall probe、文件读写 probe、fork/exec、pids limit、进程退出清理；
- 真机：Docker/K8s/Microsandbox/BoxLite 各自跑 agent/codex/pi/JS runtime 的 smoke；
- 负向：`ptrace` 被拒、读 `/.agent-compose` 被拒、超出 cgroup 被拒、profile 篡改被拒；
- 性能：seccomp/Landlock 对 exec/启动延迟的影响。

---

## 8. 安全与边界

- Stage 1 的 init 与当前 sandbox 一样以容器用户运行；它不是新的权限边界，只是**进程所有权/控制面**。
- init 二进制放在 `/.agent-compose/ac-init`（目录 0700、文件 0755），不在 PATH 中；Stage 2 的 profile 也规划放在 `/.agent-compose/`，避免和普通 workload 文件混在一起。Stage 1 仍以容器用户运行 init，所以这只是目录规整，不是安全边界。
- 直接 `docker exec` / `docker compose exec` 不使用 init 控制协议：Docker 仍能进入容器，但该进程的 PPID 在容器 PID namespace 中显示为 0，不属于 init 的 workload 树；调试可以用，daemon 的正式 exec 路径不会走它。
- 控制 socket 在 sandbox 内可见；agent 本来就能 `exec`/kill 自己的进程，因此不构成额外提权。目录 0700 + socket 0666 是为了让非 root 的 native daemon 能连接 root init 创建的 socket。
- 控制协议没有额外认证：能访问 socket 等价于能控制该 sandbox 的进程；socket 只在 sandbox 的 bind mount 内。
- 后续阶段可在 `Hooks.Prepare` 中注入 seccomp/Landlock/资源限制；那时 init 会成为真正的 boundary，需要重新评估 socket 认证、降权和进程身份。

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
