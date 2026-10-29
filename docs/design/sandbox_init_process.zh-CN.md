# Sandbox Init 进程（Stage 1）设计

> 状态：Stage 1 实现草案
>
> 关联：`docs/design/openshell-sandbox-network-internals.zh-CN.md`（OpenShell 的边界/PID1 调研）
>
> 目标：先给 agent-compose 的 Docker sandbox 一个真正的 PID 1，统一管理 agent 的 JS runtime、codex/pi 等 harness 以及它们启动的所有子进程；暂不改变网络代理/密钥模型。该 init 预留 seccomp/Landlock 等后续能力的接入点。

---

## 1. 背景与问题

当前 Docker sandbox 的进程模型是：

- 容器配置 `Entrypoint: ["sh", "-lc"]`、`Cmd: ["tail -f /dev/null"]`（或 Jupyter 启动命令），并设置 `HostConfig.Init = true`。
- DAEMON 通过 `docker exec` 启动 agent、codex/pi harness、scheduler 命令和交互式 attach。
- `docker exec` 进程由 Docker/runc 创建，不在一个受 sandbox 控制的进程树里；没有统一的父进程、回收器、信号转发、资源统计或后续 seccomp 注入点。
- agent 的子进程、后台进程、孤儿进程散落在容器 PID namespace 中；daemon 只能靠 marker/`docker exec` 做 best-effort 清理。

Stage 1 的目标不是立刻复制 OpenShell 的完整 boundary，而是先建立**进程所有权**：

1. 容器 PID 1 是一个专门的 init 进程；
2. 所有 workload 进程（main、exec、interactive attach）都由 init `fork/exec`；
3. init 负责回收孤儿、转发终止信号、维护进程表；
4. daemon 不再用 `docker exec` 启动 workload 进程，而是通过 Unix socket 控制 init；
5. 通过 daemon feature flag 灰度：关闭时完全保留现有 `docker exec` + `docker-init` 行为。

---

## 2. 非目标（Stage 1）

- 不改变网络/egress 模型（仍走现有 daemon forward proxy）。
- 不实现 seccomp/Landlock/namespace/cgroup 的强制；只保留 `Hooks` 扩展点。
- 不实现 OpenShell 的 boundary TLS/gRPC、policy DNS、TLS MITM、credential broker。
- 不改变 Docker 之外的 driver（BoxLite/Microsandbox/k8s）。init 二进制目前只在 Docker 路径使用。
- 不改变 sandbox 的 root 用户/能力；Stage 1 的 init 与当前容器一样以容器用户运行。后续阶段再讨论降权/seccomp。

---

## 3. 总体结构

```text
daemon
  │  Unix socket（host bind mount）
  ▼
container PID 1: agent-compose-init
  ├─ accept loop / JSON protocol
  ├─ process manager / reaper（wait4）
  ├─ main process（tail/Jupyter，由 --main-shell 指定）
  └─ exec sessions（agent、codex/pi、scheduler、attach）
        ├─ stdout/stderr frames
        ├─ stdin / resize / signal
        └─ exit status
```

- init 先创建控制 socket，再启动 main；daemon 在 `ContainerStart` 之后 `Ping` 到 socket 才算 sandbox ready。
- daemon 的 `Exec`/`ExecStream`/`OpenInteraction` 在容器带 init label 时走 init 协议；否则走原 `docker exec`。
- `TerminateProcessTree` 在 init 容器上走 init 的 `kill_tree`；`SignalGuestRuntime` 也通过 init exec，保证控制进程也在 init 树内。

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

- init 启动时先监听 socket，再启动 `--main-shell` 指定的 `/bin/sh -lc <command>`。
- main 的 stdout/stderr 直接继承 init 的 stdout/stderr，所以 `docker logs` 仍然可见。
- main 退出后 init 执行 graceful shutdown，并以 main 的 exit code 退出；容器生命周期与旧行为一致。
- 如果 main 是 Jupyter，init 只是父进程，Jupyter 的端口绑定/健康检查不变。

### 5.2 exec/attach

- `exec` 由 init 直接 `fork/exec`；非 TTY 使用 stdin/stdout/stderr pipes，TTY 使用 PTY。
- init 用 `wait4(-1)` 统一回收：包括 main、exec、以及被 reparent 到 PID 1 的孤儿。若 init 不是 PID 1（测试/包装进程），会设置 `PR_SET_CHILD_SUBREAPER`，让孙进程仍被 reparent 到 init。
- 子进程设置独立进程组（非 TTY `Setpgid`，TTY `Setsid`），终止时按进程组发信号。
- 信号：TERM/INT/HUP/KILL；`kill` 默认杀进程组，`kill` tree=false 只杀 leader（对应 `KillOnlyOnCancel`）。
- `list` 返回 init 当前管理的进程；用于诊断，不参与策略。

### 5.3 断连

- exec 连接断开时，init 不杀进程；输出被 drain 到 `io.Discard`，避免子进程因 pipe 满阻塞。
- daemon 的 `Exec`/`ExecStream` 在 context 取消时调用 `kill`（tree 与否取决于 `KillOnlyOnCancel`）。
- 交互式 `OpenInteraction` 的 cancel 先发 TERM，再关闭 session；最终由 init/容器停止兜底。
- `docker stop` 给 PID 1 发 SIGTERM；init 把 TERM 转发给所有进程组，并在 PID 1 身份下向整个 PID namespace 发 TERM（回收未登记的孤儿），等待 5s，再 SIGKILL。

---

## 6. Daemon 集成与灰度

### 6.1 Feature flag

```text
SANDBOX_INIT_ENABLED=false            # 默认关闭
SANDBOX_INIT_BINARY=/usr/local/bin/agent-compose-init
```

- `SANDBOX_INIT_ENABLED=true` 只影响**新创建**的 Docker 容器；已存在的容器按 label 决定控制方式。
- 容器带 `agent-compose.sandbox_init=true` label 时，即使 daemon 后来关闭 flag，`Exec`/`OpenInteraction`/`TerminateProcessTree` 仍走 init（否则运行中的 sandbox 会被破坏）。
- 关闭 flag 后新建容器回到 `docker-init + sh/tail + docker exec`，行为与旧版本一致。
- 启用 flag 但镜像没有 init binary 时，容器会启动失败；`EnsureSandbox` 在 `SandboxStartTimeout` 内等待 socket 后报错。部署必须使用包含 `/usr/local/bin/agent-compose-init` 的 guest image。

### 6.2 容器创建

启用时：

```text
Entrypoint = /usr/local/bin/agent-compose-init
Cmd        = --control-socket /run/agent-compose/init.sock
             --main-shell <tail 或 jupyter 命令>
HostConfig.Init = false
Labels[agent-compose.sandbox_init] = true
Mount <sandbox>/init-control -> /run/agent-compose (rw)
```

host 侧 control dir 由 daemon 创建（0700），socket 由 init 创建（0666，目录是访问门禁）。daemon 通过 `bindRuntimeMountSource` 处理 containerized daemon 的 host path 映射。

Unix socket 路径上限约 108 字节；如果 `<SANDBOX_ROOT>/<id>/init-control/init.sock` 过长，daemon 会在自己的临时目录创建一个短 symlink（`/tmp/agent-compose-init-<uid>/<hash>.sock`）指向真实 socket，并用短路径连接。guest 侧路径始终是 `/run/agent-compose/init.sock`。

### 6.3 Ready 与错误

- `EnsureSandbox` 在容器启动后 `Ping` init socket，直到 `SANDBOX_START_TIMEOUT`。
- socket 未出现/协议不匹配 → 清理本次 attempt（新建容器）并返回错误；不会静默回退到 `docker exec`，因为那会把“已启用 init”变成“实际上没有 init”。
- 旧容器（无 label）不等待，不受影响。

### 6.4 Exec 路径

| daemon 方法 | init 容器 | legacy 容器 |
| --- | --- | --- |
| `Exec` / `ExecStream` | `init client Exec`，帧转换为 `ExecChunk` | `docker exec` |
| `OpenInteraction` | PTY/pipes over init | `docker exec` attach |
| `TerminateProcessTree` | `kill_tree` by marker | marker control exec |
| `SignalGuestRuntime` | init exec of signal script | docker exec signal script |

`ExecResult` 的 `Stdout`/`Stderr`/`Output`/`SecurityFacts` 语义保持不变；`dockerExecCollector` 复用，UTF-8 解码和 security fact 过滤不重复实现。

---

## 7. 安全与边界

- Stage 1 的 init 与当前 sandbox 一样以容器用户运行；它不是新的权限边界，只是**进程所有权/控制面**。
- 控制 socket 在 sandbox 内可见；agent 本来就能 `exec`/kill 自己的进程，因此不构成额外提权。目录 0700 + socket 0666 是为了让非 root 的 native daemon 能连接 root init 创建的 socket。
- 控制协议没有额外认证：能访问 socket 等价于能控制该 sandbox 的进程；socket 只在 sandbox 的 bind mount 内。
- 后续阶段可在 `Hooks.Prepare` 中注入 seccomp/Landlock/资源限制；那时 init 会成为与 OpenShell 类似的 boundary，需要重新评估 socket 认证、降权和进程身份。

---

## 8. 构建与镜像

- `cmd/agent-compose-init`：Linux 静态二进制；`task build:sandbox-init` 生成 `build/agent-compose-init`。
- `guest-images/Dockerfile.agent-compose-guest` 与 Arch 版本新增 `sandbox-init-builder` stage，把二进制装到 `/usr/local/bin/agent-compose-init`。
- `SANDBOX_INIT_BINARY` 可覆盖 guest 路径，便于测试镜像/自定义镜像。
- `task build` 不依赖 init binary；只有 guest image 与 init 相关测试需要。

---

## 9. 测试

Stage 1 已包含：

- `pkg/sandboxinit`：
  - exec 的 stdout/stderr/exit；
  - ping/list；
  - TTY；
  - marker `kill_tree`。
- `pkg/config`：默认关闭、环境读取、非法值/相对路径拒绝。
- `pkg/driver`：init label 识别、control dir/socket 路径、mount 创建、默认 binary。

建议后续补：

- 真实 Docker 的 e2e：启用 flag，确认容器 PID 1 是 init、`docker top` 中 main/exec 都是 init 子进程、`docker stop` 能回收。
  - 已加入一个 opt-in 的 `TestDockerSandboxInitContainerLifecycle`：验证 init label/entrypoint、`Exec`、`Exec` 的 PPid=1、交互式 stdin。设置 `AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE` 运行。
- 交互式 attach 的 stdin/resize/signal e2e。
- 并发 exec、大量子进程、孤儿回收、marker 清理。
- 与现有 `docker exec` 路径的对照（同一套 run/命令结果一致）。

---

## 10. 后续阶段

1. **Seccomp/Landlock hook**：在 `Hooks.Prepare`/`AfterStart` 中接入，先 audit 再 enforce；init 负责把策略传给 child。
2. **进程身份**：init 已知每个 exec 的 leader/进程树，可在后续与 `/proc`/cgroup 结合，提供 binary identity。
3. **资源限制/cgroup**：把 main/exec 放进统一 cgroup；init 负责清理。
4. **文件系统/权限**：降权、只读 rootfs、`no_new_privs`。
5. **网络/凭据**：与 egress gateway/credential broker 集成；init 只保留“进程树 + 本地控制”职责，网络仍由 daemon 代理决定。

---

## 11. 代码地图

| 主题 | 文件 |
| --- | --- |
| init 入口 | `cmd/agent-compose-init/main.go` |
| 协议/客户端 | `pkg/sandboxinit/{protocol,client}.go` |
| Linux 进程/回收/PTY | `pkg/sandboxinit/{process_linux,server_linux}.go` |
| Docker 集成 | `pkg/driver/docker_sandbox_init.go`、`pkg/driver/docker_runtime.go` |
| 配置开关 | `pkg/config/config.go`（`SandboxInitEnabled`/`SandboxInitBinary`） |
| 镜像 | `guest-images/Dockerfile.agent-compose-guest` |
| 构建 | `Taskfile.yml` `build:sandbox-init` |
