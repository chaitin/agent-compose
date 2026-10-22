# 升级 daemon 不中断沙箱和正在运行的任务

数据截至 2026-10-19，代码基于 `35b2e551`，microsandbox Go SDK `v0.7.3`、BoxLite `v0.10.4`。

## 1. 背景

### 1.1 现状：每次升级 daemon 都会打断工作

daemon 常用 Docker Compose 部署，升级就是换镜像、重建容器，每次发版都会发生。

一个 run 在沙箱里就是一次长时间运行的 exec：daemon 在沙箱里执行 `agent-compose-runtime prompt`（命令类任务是 `agent-compose-runtime exec`），再从这次 exec 的输出流里读结果、写 run 日志。agent run、交互会话、调度器任务、notebook cell 都是这样。

重建 daemon 容器的后果：

| | Docker driver | VM driver（microsandbox / BoxLite） |
| --- | --- | --- |
| 沙箱 | 不受影响：沙箱容器由宿主机 dockerd 通过挂进来的 `docker.sock` 创建，不是 daemon 的子进程 | **全部停掉**，包括空闲的。下次使用时冷启动，rootfs 盘在 volume 上，文件不丢 |
| 正在跑的 run | **全部判为中断**，要手动重试 | 同左 |

### 1.2 VM 为什么跟着 daemon 一起停

使用 VM driver 时要叠加 `docker-compose.kvm.yml`。VM 进程由 daemon 通过 SDK（FFI，进程内调用）直接 fork 出来，和 daemon 在同一个容器里。

两个 SDK 都有 detach 选项，让 VM 进程不随父进程退出：BoxLite 已经在用（`boxlite_options_set_detach`）；microsandbox 的 `WithDetached()` 还没用上，`msb` 进程带着 `--parent-watch-fd`，父进程一退出它也退出。

但 detach 只能解决"父进程退出"，容器停止时还有三层边界会把 VM 一起收掉：

- **PID namespace**：容器 PID 1 退出，内核 SIGKILL 整个 namespace 里的进程。
- **cgroup**：`docker stop` 按容器 cgroup 枚举进程并杀掉。
- **mount / net namespace**：VM 依赖的 libkrun 等二进制在旧容器的 rootfs 上，TAP 和端口转发在旧容器的 netns 里。

所以根本约束是：**fork VM 的进程不能在会被升级的容器里。** SDK 只有进程内（`local`）和 `cloud` 两种 backend，没有可以自托管的 server，这个进程只能我们自己提供。

### 1.3 任务为什么保不住

不分 driver，有三个原因：

- **daemon 退出时主动 kill exec。** 每个 run 的 ctx 都从 daemon 的根 ctx 派生（`run_supervisor.go`），根 ctx 在收到 SIGTERM 时取消（`main.go` 的 `signal.NotifyContext`），driver 看到 ctx 结束就 kill 掉 exec。
- **输出接不回来。** exec 的输出由 daemon 进程内的 goroutine 接收，daemon 一退出就断了；driver 层也没法重新 attach：microsandbox 的 `ExecHandle` 不能按 ID 重新获取，Docker 的 `ExecStart` 只能调用一次，输出也不进容器日志。
- **重启后的对账只会判失败。** `startBackgroundManagers` 对重启前没跑完的 run：调度器 run 一律标为失败，project run 一律走中断收尾。沙箱则是去运行时确认死活（BoxLite 还没实现这个检查，PR #789 补上）。

### 1.4 目标

- 重建 daemon 容器时，VM 不停；正在跑的 run 不中断，重启后接回来，输出不丢、不重复，终态正确。
- 旧 guest 镜像（不支持新机制）行为不变。

不在范围内：

- 持有 VM 的进程（下文的 sandbox-host）自身升级：仍要 drain。
- 第一次升级到本设计版本的那一次：现有 VM 仍会中断一次。
- daemon 被硬杀时的优雅处理：只保证重启后能接回。

## 2. 测试结果

在 ops（x86_64，KVM）上实测，三个 driver 都通过 `agent-compose exec --command` 执行，走 `agent-compose-runtime exec` 路径。场景 A、B 在 `d5dfcc57`（microsandbox v0.6.14 / BoxLite v0.9.7）上测，guest agent 在这方面两个版本之间没有改动；其余在 release `v2610.1.0`（与 main 版本一致）上测。

| 场景 | microsandbox | BoxLite | Docker |
| --- | --- | --- | --- |
| A：`setsid` 拉起子进程，stdio 断开，exec 结束 | exec 立即返回，子进程存活 | 同左 | 同左 |
| B：后台子进程继承 exec 的 stdout | 子进程存活，但 exec 被拖住，直到子进程退出（约 25 秒） | 同左 | 子进程存活，exec 立即返回 |

本设计依赖的结论：

1. **guest 里脱离 exec 的进程能继续运行**：三个 driver 都不会在 exec 结束时清理 guest 子进程（场景 A）。这是 3.3 的前提。
2. **脱离出去的进程必须断开 stdio**，否则 VM driver 的 exec 不会返回（场景 B）。
3. **取消 exec 会在 guest 里留下孤儿进程**：microsandbox 和 BoxLite 的 Kill 都到不了整棵进程树，命令类 exec 和 BoxLite 上的 agent run 被取消后，进程还在 guest 里跑。PR #789 已修复，详情见 PR。
4. **guest 访问 facade 靠 VM 进程所在容器的 DNS**：guest 拿到的地址是 `http://agent-compose:7410`，由 daemon 容器的 Docker 内置 DNS（`127.0.0.11`）解析，网络出口也是这个容器的 netns。
5. **BoxLite 的 exec 会自动拉起已停止的 box**：`execWithStream` 发现 box 没在运行，就先 `startBox` 再执行（`boxlite_cgo.go:614`）。所以在 BoxLite 上不能用"执行一次 exec"来判断沙箱是否还活着。
6. **换 guest 镜像后，首次启动沙箱要几十分钟**（构建基础盘）：microsandbox 约 26 分钟，BoxLite 超过了 30 分钟的 `SANDBOX_START_TIMEOUT`，daemon 放弃后 BoxLite 仍在后台建完、拉起，留下孤儿 box（issue #791）。之后用缓存冷启动不慢。

未覆盖：supervisor 拉起 runner 后 exec 能否立即返回（第 1 步开工前的原型验证项）；daemon 被硬杀的情况。

## 3. 设计

### 3.1 总体

问题分两层，分别解决：

| 问题 | 方案 | 范围 |
| --- | --- | --- |
| 沙箱（VM）撑过 daemon 升级 | **sandbox-host**：持有 VM 进程的长期运行进程，daemon 通过 RPC 使用它 | 只有 VM driver 需要；Docker / k8s 的沙箱本来就由 dockerd / kubelet 持有 |
| 正在跑的任务撑过 daemon 升级 | **guest 侧 detach**：任务在 guest 里脱离 exec 运行，输出写入 journal（guest 内的输出日志文件），daemon 重启后用新的 exec 重新 attach | 所有 driver 统一 |

接住输出放在 guest 里做，是因为 driver 层无法重新 attach（1.3），而不论哪种 driver，能撑过 daemon 重启的只有 guest 里的进程本身。这样 sandbox-host 只需透传 exec，不用持有 exec 句柄、不用缓存输出，接口更小，也很少需要升级。

```text
agent-compose（频繁升级）
  API / 调度 / DB / run 状态机 / facade
  remoteSandboxRuntime ──┐ unix socket（gRPC）
                         ▼
sandbox-host（很少升级，只用于 VM driver）
  microsandboxRuntime / cgoSandboxRuntime（现有实现原样搬入）
  VM 进程是它的子进程
                         │ exec 透传
                         ▼
guest
  agent-compose-runtime：supervisor + journal + attach
```

拆分后，每种 driver 的沙箱都由 daemon 之外的进程持有：Docker 由 dockerd，k8s 由 kubelet，VM 由 sandbox-host。

### 3.2 取消的分流

| 触发 | 对正在跑的任务 | 适用 |
| --- | --- | --- |
| 用户停止 run、run 超时 | 优雅终止：SIGTERM → 宽限期 → SIGKILL，清理整棵进程树 | 所有 guest |
| daemon 收到 SIGTERM（升级、重启） | 不终止，只断开 attach；任务继续在 guest 里跑 | 支持 detach 的 guest |
| daemon 收到 SIGTERM（升级、重启） | 同"用户停止 run" | 旧 guest |
| daemon 被硬杀 | 无法处理；启动对账时重新 attach | 支持 detach 的 guest |

**两个 ctx**：现在根 ctx 被取消是唯一的取消入口，拆成两个：

- **run ctx**：用 `context.WithoutCancel` 从根 ctx 解绑（保留 values，`detachedRunContext` 带的请求元数据和可信 header 不会丢，不能用 `context.Background()` 代替）。只由 `StopActiveRun` 和超时取消，带上取消原因（`run_supervisor.go` 已经用 `context.WithCancelCause`）。被取消 = 终止任务。
- **attach ctx**：从 run ctx 派生，再挂一个 daemon shutdown 信号。只控制 attach exec，被取消 = 断开连接，不终止任务。

**断开 attach 不做进程树清理。** PR #789 之后，取消 VM exec 要先起一个控制 exec 清理进程树，最多约 5 秒。attach 是单个进程，任务也不在它的进程树里，用不着这一步。`ExecSpec` 加一个选项，让 driver 取消这类 exec 时只调 Kill。

**shutdown 顺序**：取消所有 attach ctx → 各 run 的 `Execute` 以"已断开"返回（不写终态，run 在库里保持 running）→ `RunSupervisor.Shutdown` 的 `wg.Wait()` 清零 → 退出。要在 `cli_daemon.go` 的 10 秒 shutdown 超时内完成：支持 detach 的 run 只是断开连接，耗时可以忽略；旧 guest 的 run 要走进程树清理，最坏约 5 秒，各 run 并发执行。

**同步 `Run` 路径**（`StreamAgentRun`）用的是请求 ctx，客户端断开就取消 run。这个语义保留，只有 daemon shutdown 导致的断开才走 detach：请求 ctx 结束时检查 daemon 是否正在 shutdown，是就只断开 attach，否则取消 run ctx。`startInteractiveRun` 和 `Attach` 同理。

### 3.3 guest 侧 detach（任务保活）

**guest runtime（`runtime/javascript`）新增的子命令：**

- `capabilities --json`：输出支持的能力，例如 `{"detach": true}`，供 daemon 探测。
- `start --detach --execution-id <id> [--task-timeout-ms <ms>] [--stdin] [--raw-command] -- <prompt|exec|stream ...>`：拉起一个 supervisor 进程（`setsid`，stdio 不继承 exec 的管道），由它运行现有的 `prompt` / `exec` / `stream` 逻辑；`--raw-command` 时任务参数是一条命令行（交互式 command run 用）。supervisor 写好 readiness 文件后，这次 exec 立即返回；任务在 ready 之前就结束时立即报错，不等超时。readiness 记录 supervisor 的 pid、进程启动时间（`/proc/<pid>/stat` 第 22 字段）和 guest 的 boot id，判断 supervisor 存活时三者都要对上：VM 用同一块盘冷启动后 state root 还在，单看 pid 会被复用的 pid 骗过。没有 `/proc` 时退化为只看 pid。
- `attach --execution-id <id> --from <offset> [--stdin]`：从 offset 开始回放 journal，追上后持续跟随，读到 `exit` 文件就带着退出码结束；遇到缺口记录原样下发。supervisor 已不在、又没有 `exit` 文件时，报告任务异常死亡，不挂起。journal 没变化（inode、大小、mtime 都没变）时不重读。
- **交互式任务的 stdin**：`start --stdin` 时 supervisor 在 `executions/<id>/stdin.sock` 上监听，把收到的输入写给任务；`attach --stdin` 把自己的 stdin 原样转发过去。输入按帧传输（1 字节类型 + 4 字节长度 + 内容）：`D` 数据、`E` 输入结束、`S` 信号。输入结束必须是显式的帧：daemon 死掉时 attach 也死，连接随之断开，这种情况下任务的 stdin 要保持打开，留给下一个 daemon。同一时间只服务一个连接，新连接顶替旧的。socket 起不来时 supervisor 杀掉任务并记异常退出，不留无人管的任务。
- `cleanup --execution-id <id>`：删除已结束执行的状态目录；任务还在跑时拒绝。
- `stop --execution-id <id> --reason <user|timeout>`：把停止请求交给 supervisor。

**supervisor 的职责：**

- **journal**：把任务的 stdout、stderr 和结构化事件，按单调递增的 offset 追加写入 `<state-root>/executions/<id>/journal`。有大小上限，超限时丢弃最旧的数据，并写一条缺口记录（丢弃的 offset 范围）。
- **停止**：supervisor 是任务的父进程，停止请求都由它转发给任务。转发前先把停止原因写进状态目录，所以结束时能区分三种情况：任务自己退出、supervisor 主动终止（含宽限期后的 SIGKILL）、被别的信号杀死（guest OOM、SIGSEGV 等）。
- **exit 文件**：任务结束时原子写入退出码和结束原因。

**daemon 侧：**

- **启动**：先执行 `start --detach`，再用 `attach` 跟随输出。对 driver 来说都是普通的 ExecStream / Interaction。
- **run 记录新增字段**：
  - `execution_id`：daemon 生成（UUID），在执行 `start --detach` **之前**写入。
  - `detach`：这个 run 是否走 detach 模式，启动时由能力探测决定，shutdown 时据此决定断开还是终止。
  - `deadline`：run 超时的绝对截止时间（或剩余超时）。启动时持久化，重启后对账 attach 时据此重新挂上超时，否则超时计时器随重启丢失、detach 的 run 永远到不了超时终态。
  - `detach_kind`：detach 的是哪类任务（`command`、`agent`、`interactive_prompt`、`interactive_command`），重启后据此选收尾方式。
  - 已确认的 offset 记在 run 产物目录旁的 `journal-cursor` 文件里（agent run 记在它的 cell 目录），不进数据库：每条输出都写一次库代价太大。每个 attach 输出块写一次，先写 cursor 再转发输出，所以 daemon 被硬杀时最多丢一个块，不会重复。
- **兜底清理**：supervisor 自己异常退出时，任务可能没人管。所以 `start --detach` 那次 exec 用 `execution_id` 作为 marker，任务的整棵进程树都带着它；下面两处触发按 marker 清理进程树：停止时 supervisor 没在宽限期内结束任务；attach 报任务异常死亡（supervisor 已不在、且没有 `exit` 文件）——supervisor 已退出、停止请求送不到，只能靠 marker 直接清理。这需要 driver 新增两点（PR #789 的 marker 是 driver 在 exec 内部生成的，调用方传不进去，也只在取消那次 exec 时才清理）：
  - `ExecSpec` 允许调用方指定 marker；
  - 一个不依赖任何 exec、单独"按 marker 清理进程树"的方法。
- **能力探测**：daemon 对任意镜像没有启动握手，也不会因为声明缺失而拒绝（`docs/pages/guest-image-abi.md` 第 1 节），所以不读镜像里的 ABI 声明，而是在沙箱里执行一次 `agent-compose-runtime capabilities --json`，结果按沙箱缓存。输出里有 `detach: true` 就支持；命令不存在或解析失败就走现有路径。不用"`start --detach` 失败就回退"：那样分不清是旧 runtime 不认识这个命令，还是任务启动本身出错，后者回退会把任务再跑一遍。
- **去重**：daemon 写 run 日志时记录每条输出的 offset，重连时从最后一条的 offset 续读，收到不大于它的直接丢弃。所以 run 日志里的输出不重复，即使 daemon 被硬杀。客户端在 daemon 重启后是新连接，续读游标统一用 journal offset，不再沿用 `InteractiveSession` 的进程内事件序号：事件序号随 daemon 重启丢失，而 offset 是 guest 磁盘上唯一跨重启自洽的坐标，统一到 offset 也免去了两套坐标之间的映射。`InteractiveSession` 的回放数据改从 journal 来，不再自己维护一份；`resumable-interactive-session.md` 中「daemon 重启后 session not found、事件序号是进程内状态」的限制随之更新（见该文档）。
- **对账**：对 running 且 `detach` 为真的 run，**先用 `IsSandboxAlive` 确认沙箱活着，再执行任何 exec**（BoxLite 的 exec 会自动拉起已停止的 box，见第 2 节结论 5）。活着就 `attach --execution-id <id> --from <offset>` 接回来，并按持久化的 `deadline` 重新挂上 run 超时；沙箱不在或接不上，走中断收尾。
- **超时**：剩余的 run 超时随 `start` 交给 supervisor（相对时长，避免 guest 与 host 时钟不一致），没有 daemon 在时 supervisor 自己以 `timeout` 原因停掉任务；daemon 侧仍按持久化的 `deadline` 重新挂上超时，两边谁先到都一样收尾。
- **覆盖的 run**：
  - command run 和 agent run：daemon 关闭时只断开；agent run 的 cell 保持 running，这个 run 的 facade token 保留。重启后 agent run 从 run 的日志路径（`cells/<cell-id>/output.txt`）找回 cell，从 journal 开头回放：cursor 之前的输出不再转发，只用来解析结果，这样 daemon 停在结果行和退出之间时也拿得到结果；run 结束后按 run id 删掉保留的 token。
  - 交互式 run：只有本来就不跟随客户端的（断开策略为 detach，如 `run -i -d`）才走 detach；跟随客户端连接的仍随客户端结束。TTY 会话不走 detach，supervisor 没有终端可给。重启后重建 run 的交互会话，客户端可以按 run_id 重新 attach。
  - 调度器 run **接不回来**：编排脚本在 daemon 进程内的 QuickJS 里执行，脚本执行到哪一步、在等哪个 promise 只在 daemon 内存里，guest 侧 detach 保不住。调度器 run 里发起的 project run 本身是 project run，能活过重启；调度器 run 自己仍按现在的方式判失败。要接回需要把调度器步骤持久化并可重放，不在本设计范围。

**终态映射**：退出码由 `attach` 读到的 `exit` 文件提供，结果仍经由现有的 run 状态机和 `CompletionManager` 写回。

| attach 的结果 | run 终态 | ExecResult |
| --- | --- | --- |
| 退出码 0 | succeeded | ExitCode 0 |
| 非 0 退出码 | failed | 对应退出码 |
| supervisor 因超时终止 | failed（超时） | 与现有超时行为一致 |
| supervisor 因用户停止终止 | canceled | 与现有取消行为一致 |
| 被别的信号杀死 | failed，错误信息带上信号 | 128 + 信号值 |
| 任务异常死亡（supervisor 已不在，且没有 `exit`） | failed，标明 guest 任务异常终止；同时按 marker 清理进程树（见兜底清理） | ExitCode -1 |
| 沙箱已不在 | 走现有的中断收尾 | — |

**不改 guest ABI。** 按 `docs/pages/guest-image-abi.md`，runtime CLI 属于 release boundary，不归 `guestAbi` 管，新增子命令是 boundary 之内的正常演进。版本不匹配的旧镜像本来就不保证兼容，`capabilities` 探测只是让它优雅降级：不支持 detach 的 guest 走现有路径，daemon 收到 SIGTERM 时仍然终止任务。

**清理**：run 进入终态、daemon 确认读完后，执行 `cleanup` 删除 `executions/<id>` 目录；断开时不删，留给下一个 daemon。daemon 没回来收尾的执行，由 guest 在下次 `start` 时清掉结束超过 7 天的。沙箱停止或删除时一并清理。

**能力探测的缓存**：只缓存 guest 给出的答案（包括旧 runtime 没有这个子命令时的非 0 退出）；探测超时或 exec 本身出错不缓存，下一个 run 重新探测。VM 沙箱寿命长，一次偶发超时不该让它之后的 run 全走旧路径。

### 3.4 sandbox-host（VM 保活）

**接口**：在 `driver.SandboxRuntime`（`pkg/driver/types.go`）这一层切开：EnsureSandbox、StopSandbox、RemoveSandbox、Exec、ExecStream；可选接口 Stats、IsSandboxAlive、SignalGuestRuntime、InteractionCapabilities + OpenInteraction、ProcessTreeTerminator（按 marker 清理进程树，detach 的兜底终止靠它）；以及残留资源和运行时缓存的列出与清理。`ExecSpec` 的 `Marker`、`KillOnlyOnCancel` 也要原样经过 RPC。Exec、ExecStream、OpenInteraction 是流式透传，其余一问一答。调度、凭据签发、策略判断都留在 daemon。

**协议版本**：单调递增的整数，host 在握手时报告。daemon 支持当前版本和前一个版本（N 和 N-1），测试矩阵覆盖这两种组合。遇到不兼容的 host，daemon 拒绝使用 VM driver 并报错。daemon 不能自己重启 host，那会中断所有 VM。

**拆分时必须守住的约束：**

- **文件路径一致**：microsandbox 和 BoxLite 是直接读写宿主机目录再挂进 VM 的。沙箱目录、volume 源目录、`MSB_HOME` / `BOXLITE_HOME`、镜像缓存，两边必须是同一份数据、同一个绝对路径。BoxLite volume bridge 的 bind mount 要在 host 的 mount namespace 里做。
- **网络**：guest 经由 VM 进程所在容器的 DNS 访问 `agent-compose:7410`（第 2 节结论 4），Jupyter 的端口转发也开在 host 的 netns 里。所以 sandbox-host 要带网络别名 `agent-compose`，daemon 用 `network_mode: service:sandbox-host` 共享它的 netns。
- **只在新建时签发凭据**：`Sandbox.ProxyEnv` 是闭包，不能跨 RPC 传。Ensure 拆成两步：host 先回答需不需要新建；需要的话，daemon 解析出 env，再调用 Create 并带上 env。daemon 用现有的 `LifecycleLocks` 串行化同一个沙箱的 ensure；host 以自己的 VM 状态为准做幂等，收到 Create 时 VM 已存在就丢弃这份 env。
- **"正在创建"由 host 负责**：首次构建基础盘可能要几十分钟（第 2 节结论 6），daemon 可能在这期间重启，而 `LifecycleLocks` 会随之丢失。所以 Create 不跟着发起它的 RPC 一起取消，host 把它做完；新 daemon 再次 Ensure 时，如果创建还在进行就等它完成，不另起一个。只有 daemon 明确要求删除时，host 才在创建完成后回收这个 box。
- **RPC 断开时 host 主动取消 exec**：guest 里的进程不会因为 exec 的另一端没人了就退出。daemon 被硬杀时，host 要把它发起的流式 exec 按取消处理（attach 只调 Kill，其他 exec 清理进程树），否则每重启一次，guest 里就多一批 attach 进程。
- **敏感字段**：`RuntimeEnvItems`（里面有 facade token）在 RPC 里显式传，不进日志；socket 权限 0600。host 只在创建 VM 时把 env 交给 SDK，之后不缓存；facade token 存在数据库里，daemon 重启不会作废。信任边界没有扩大。

同一个 box 在 host 里同时有多个句柄是安全的：`boxlite_get` 每次返回独立句柄，各自持有底层 box 的引用（PR #789 已对照上游源码确认）。

**代码路径只有一条**：VM driver 一律通过 `remoteSandboxRuntime` 走 RPC，不保留进程内直连。daemon 先连 socket，连不上就自己拉起：

- **Compose + KVM**：sandbox-host 由 compose 预先启动。
- **宿主机部署 / 本地开发**：daemon 拉起 `agent-compose sandbox-host`，脱离 daemon 的进程组和会话；用文件锁防止重复拉起，日志写到文件。

**Compose 部署**：只改 `docker-compose.kvm.yml`。installer 检测到 KVM 时本来就会叠加这个文件，用户的命令不变。

```yaml
services:
  sandbox-host:
    image: ${AGENT_COMPOSE_SANDBOX_HOST_IMAGE}   # 与 daemon 同镜像，版本单独配置
    command: ["agent-compose", "sandbox-host"]
    privileged: true
    devices: ["/dev/kvm:/dev/kvm"]
    volumes: [<与 daemon 相同的数据目录和 .env>, sandbox-host-run:/run/agent-compose]
    ports: ["127.0.0.1:7410:7410"]               # 从 daemon 挪过来
    networks: { default: { aliases: [agent-compose] } }
  agent-compose:
    network_mode: "service:sandbox-host"         # daemon 不再需要 privileged 和 /dev/kvm
    ports: !reset []                             # 需要 Compose v2.24.4 及以上
    volumes: [sandbox-host-run:/run/agent-compose]
```

- sandbox-host 的镜像版本由 Release manifest 单独推进，只在 VM driver 或 RPC 协议变化时才变。平时 `docker compose up -d` 只重建 daemon。
- installer 要增加 Compose 最低版本检查（2.24.4）；回滚时的 `down --remove-orphans` 改成只停 daemon，避免停掉 host 或出现版本错配。

**需要 drain 的两种情况**：第一次升级到拆分版本时，以及 sandbox-host 自身升级时，VM 和其中的任务都会丢失。做法是先 drain（停止派发新任务，等正在跑的 run 结束或超时），再升级，之后用同一块 rootfs 盘冷启动。如果同时换了 guest 镜像，drain 窗口要把构建基础盘的时间算进去。第一次升级的情况要写进 release note。

## 4. 实施顺序

| 步骤 | 内容 | 受益范围 |
| --- | --- | --- |
| 0 | PR #789：VM exec 取消时清理整棵进程树；BoxLite `IsSandboxAlive` | 所有 driver |
| 1 | 3.2 取消分流 + 3.3 guest 侧 detach（含 driver 支持指定 marker 和按 marker 清理）。开工前先做原型验证：supervisor 拉起 runner 后 exec 能立即返回 | Docker driver 直接受益；VM driver 还要等第 2 步 |
| 2 | 3.4 sandbox-host | VM driver |

先做 detach：改动小、覆盖所有 driver，做完 Docker driver 就能受益；sandbox-host 改动大，而且要等 detach 也做完，VM driver 上"任务不中断"才完整。第 1 步的对账依赖第 0 步的 `IsSandboxAlive`。

第 1 步验收：

- Docker driver 下 run 运行中重启 daemon，任务不中断，退出码和终态正确。
- run 日志里的输出无重复（daemon 被硬杀后重连也一样）；有缺口时显式标出。
- agent run、不跟随客户端的交互式 run 在 daemon 重启后能被接回；交互式 run 接回后客户端能按 run_id 重新 attach。
- 调度器 run 发起的 project run 能活过重启；调度器 run 本身仍判失败（见 3.3「覆盖的 run」）。
- 对账时沙箱已死（含 BoxLite），run 走中断收尾，VM 不会被 exec 重新拉起。
- supervisor 异常退出后，run 判失败，且 guest 里按 marker 清掉孤儿进程树。
- run 超时跨越 daemon 重启仍按持久化的 `deadline` 进入超时终态。
- 多个 run 并发时，daemon 正常关闭不超过 10 秒（detach 和旧 guest 两种情况都测）。
- 旧 guest 镜像行为不变。

第 2 步验收：

- Compose 下重建 daemon，VM 不重启，正在跑的 run 能接回，guest 仍能访问 `agent-compose:7410`。
- 宿主机部署下 daemon 重启后复用已有的 host，不重复拉起。
- 沙箱创建进行中重启 daemon：host 把创建做完，新 daemon 接着用，不另建、不留孤儿 box。
- daemon 被硬杀后，guest 里不残留 attach 进程。
- installer：Compose 版本低于 2.24.4 时拒绝安装；回滚只停 daemon，不停 sandbox-host。

## 5. 风险与待确认

- **journal 的开销和上限**：agent run 的输出量可能很大，上限取值和各 driver 下 `--state-root` 的磁盘配额要基于真实 run 的输出量分布来定。
- **交互输入多一跳**（attach → guest 内 socket → supervisor），延迟需要实测。
- **上游行为可能变化**：guest agent 不清理 exec 子进程是实测结论，不是上游承诺。升级 microsandbox 或 BoxLite 时，要把场景 A 纳入回归测试。
- **启动超时**：首次构建基础盘可能超过 `SANDBOX_START_TIMEOUT`，超时值是否要调，跟 issue #791（BoxLite 超时后留下孤儿 box）一起评估。
- **单 host 还是每个沙箱一个 shim**：先实现单 host，接口上不排除以后改成每个沙箱一个 shim。

参考：containerd-shim 与 Docker live-restore（持有进程的组件比控制面活得久）、conmon（日志文件加 exit file）、kubelet 通过 CRI 对账、tmux attach。
