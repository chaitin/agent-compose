# 自定义 Guest Image ABI

本文定义当前 `agent-compose` 与 OCI guest image 之间的最小约定，并说明如何构建、选择和验证自定义镜像，而不必照搬官方
Docker Hub 上 `chaitin/agent-compose-guest` 镜像中的全部工具。

这是一份按能力分层的约定。只用于直接执行命令的 sandbox 镜像，所需软件远少于同时运行 Codex、Claude、OpenCode、Pi、DSH、JupyterLab 和所有 notebook cell 类型的镜像。

本文使用 **必须**、**应该** 和 **可以** 描述兼容性要求。

## 1. 兼容模型

guest image 现在会声明自己是为哪个契约构建的，声明由两处产物承载：

- OCI label `io.agent-compose.guest-abi`，无需启动镜像即可从镜像元数据读取；
- 位于 `/etc/agent-compose/guest-abi.json` 的 manifest，随镜像文件系统一起分发，可在运行中的 sandbox 内读取。

两者携带同一个数字：**guest ABI**。当前 daemon release 只支持一个 guest ABI 值（`pkg/guestabi` 中的 `CurrentGuestAbi`）。声明了其他值的 guest image 与当前 daemon release 不匹配。

guest ABI 与 provider 版本刻意保持独立。升级 Codex、Claude、OpenCode、Pi 或 DSH 改变的是镜像内容，而不是 daemon 与 guest 之间的契约，因此 `guestAbi` 保持不变，仅重建 provider 的镜像可以继续运行在已经部署的 daemon 之下。提高 `guestAbi` 意味着 daemon/guest 契约本身发生变化，这属于 daemon release，而不是 guest 重建。

daemon 仍然负责选择镜像、挂载 sandbox state、通过 runtime driver 启动镜像，并按路径和协议约定执行命令。声明改变的是 daemon 在此之前能知道什么，而不是它启动 sandbox 的方式。

因此：

- 自定义镜像**必须**针对生产环境使用的每个 `agent-compose` release 和 runtime driver 进行测试。声明并不等于内容证明：目前没有会检查任意镜像内容的启动握手，daemon 也不会拒绝声明缺失、不可读或 ABI 不同的镜像，而是回退到自己编译期内置的 provider 声明。
- 包含 `agent-compose-runtime` 的镜像**应该**从与 daemon 相同的 Git tag 或 commit 构建 runtime。Runtime CLI 及其 stdout 协议是内部 release boundary，不保证任意跨版本兼容。
- 最稳妥的自定义方式是继承与 daemon tag 匹配的不可变官方 guest tag。
- 多架构 tag **必须**包含 runtime host 使用的架构。官方发布矩阵是 `linux/amd64` 和 `linux/arm64`。

本文约定的是镜像文件系统和进程环境。KVM 访问、Docker 可达性、镜像拉取、daemon 权限、网络策略、模型凭据及外部服务健康度属于部署问题，不是 guest image ABI 属性。

### 1.1 Guest ABI 产物

`guest-images/providers.lock.json` 是仓库内 guest 镜像内容的唯一编写点。它记录 `guestAbi`、`nodeMajor` 以及一个 `packages` map；每个条目给出 npm 包、固定版本、承载它的 Docker build arg，并可选给出可执行文件名、feature 列表、companion 包或 Node.js 版本要求。`scripts/provider-lock.sh` 据此派生其余所有内容：

- `pairs` 输出 `BUILDARG=version` 行；
- `guest-abi` 渲染 manifest，`sync` 将其写入 `guest-images/guest-abi.json`，`guest-abi-version` 输出 ABI 版本号；
- `check` 在 lock、两个 guest Dockerfile 的 `ARG` 默认值、构建脚本的转发或生成的 manifest 任意一处不一致时失败；
- `bump K=V [K=V ...]` 一次性改写 lock、两个 Dockerfile 和 manifest 中的某个 provider 版本，且不校验新版本。

`guest-images/guest-abi.json` 由 lock 生成并提交到仓库。只有提供 agent 可执行文件的包才会出现在 `providers` 下；companion 包属于构建输入，因此被有意排除。daemon 要求 manifest 描述它认识的每个 provider（`codex`、`claude`、`opencode`、`pi`、`dsh`）；缺少其中任何一个时，daemon 会放弃该 manifest 并改用编译期声明（`pkg/capmatrix/provider_matrix.go` 中的 `DeclaredProvidersFromManifest`）：

```json
{
  "guestAbi": 1,
  "providers": {
    "codex": {
      "version": "x.y.z",
      "features": ["structured_output", "session_resume", "streaming"]
    }
  }
}
```

两个 guest Dockerfile 都会烘焙该 manifest 并发布 ABI：

```dockerfile
ARG GUEST_ABI=1
LABEL io.agent-compose.guest-abi="${GUEST_ABI}"
COPY guest-images/guest-abi.json /etc/agent-compose/guest-abi.json
```

`GUEST_ABI` 在 Dockerfile 中默认为 `1`，由 `scripts/build-agent-compose-guest.sh` 覆盖为 lock 中的取值。因此 manifest 随镜像文件系统一起分发，而 label 让 registry 或运维人员无需启动镜像即可看到契约版本。

在 daemon 侧，`pkg/guestabi` 拥有该契约（`CurrentGuestAbi`、`Parse`、`LoadFile`、`Validate`、`VerifyCompatible`、`ErrUnsupportedGuestAbi`）。可选环境变量 `GUEST_ABI_MANIFEST` 用于指定一个 manifest 文件，它在 `.env.example` 中以注释形式给出示例。当它已设置且有效时，`EngineService.GetCapabilities` 报告的 provider capability matrix 由该 manifest 构建，而不是由 daemon 编译期声明构建；当它未设置、不可读、格式错误或声明了不同的 ABI 时，daemon 记录 warning 并回退到编译期声明，不会拒绝该镜像。读取该文件是这条路径上唯一的 guest 相关 I/O：构建 snapshot 仍然不会访问 Docker、KVM 或 Kubernetes。

### 1.2 从运行中的 sandbox 读取 Guest ABI

由本仓库构建的 runtime 可以自描述其所在镜像：

```bash
agent-compose-runtime abi
agent-compose-runtime abi --json
agent-compose-runtime abi --manifest /etc/agent-compose/guest-abi.json
```

`abi` 默认读取 `/etc/agent-compose/guest-abi.json`，`--manifest` 可覆盖路径，`--json` 输出单行 JSON。报告包含：

| 字段 | 含义 |
| --- | --- |
| `guestAbi` | 镜像声明的契约版本；未找到可用 manifest 时为 `0` |
| `providers` | 镜像声明的每个 provider 到 `{version, features}` 的映射 |
| `manifestPath` | 实际读取的 manifest 路径 |
| `runtimeVersion` | 已安装 `agent-compose-runtime` 包的版本 |

manifest 缺失、不可读、格式错误或结构不符时，命令报告 `guestAbi: 0` 和空的 `providers` map，退出码仍为 `0`。`0` 是文档化的“未知或旧版 guest”取值：它表示镜像没有自描述，而不是镜像损坏。只有 runtime 安装本身损坏（runtime 包 manifest 不可读）时该命令才会失败。

## 2. 约束分层

只有 baseline 层是无条件要求；项目使用哪种能力，就增加对应能力层。

| 层级 | 何时需要 | 镜像最小内容 |
| --- | --- | --- |
| Sandbox baseline | 始终需要 | Linux OCI image、root home 约定、shell/bootstrap 工具、可写挂载目标 |
| Runtime CLI | Agent prompt、scheduler command/shell 或 prompt attach | Node.js 和与 release 匹配的 `agent-compose-runtime` 可执行文件 |
| Provider | Agent 选择该 provider | 所选 provider 的可执行文件和 runtime dependencies |
| Jupyter | 使用 `jupyter.enabled` 或 `run --jupyter` | Python 3 和可 import 的 `jupyterlab`；额外 kernel 可选 |
| Notebook cell | 执行对应语言的 cell | 按 cell 类型提供 `bash`、`python3` 或 `node` |
| Runtime SDK | Workspace 代码需要离线安装/使用 SDK | 约定路径下可选的 SDK tarball |

因此，一个镜像可以兼容直接 `exec`，但有意不兼容 agent prompt 或 Jupyter。缺少可选层时，只会在调用该能力时失败；daemon 不会在 project apply 阶段预检所有工具。

## 3. Baseline ABI

### 3.1 镜像和用户

Guest image **必须**：

- 是目标架构的 Linux OCI/Docker image；
- 默认以 root 运行（`USER root`，或不设置非 root `USER`）；
- 定义 `HOME=/root`，并提供真实、可写的 `/root` 目录；
- sandbox 启动期间提供可写 root filesystem；
- 不依赖 image `ENTRYPOINT` 初始化必需文件。

当前不支持非 root guest image。daemon 将 guest home target 固定为 `/root`，在 `/root` 下挂载或创建 provider state symlink，并执行会替换部分路径的 bootstrap 操作。目前没有公开的 `GUEST_HOME` override。

Docker 和 BoxLite 在启动 sandbox 时会替换镜像 entrypoint 和 command；Microsandbox 也会执行显式的启动后 bootstrap。因此 `CMD`、`ENTRYPOINT`、`EXPOSE` 和 health check 都不是 ABI 要求。它们可以方便手动运行镜像，但必需的初始化内容**必须**预先写入镜像文件系统。唯一带有契约含义的 label 是 `io.agent-compose.guest-abi`（第 1.1 节）；daemon 读取的是烘焙进镜像的 `/etc/agent-compose/guest-abi.json` manifest，而不是该 label。

### 3.2 控制面所需命令

对于四个 runtime driver，镜像都**必须**提供支持 `-lc` 的 `sh`。跨 driver 镜像还**必须**在固定 runtime `PATH` 中提供：

```text
mkdir  test  rm  ln  readlink  mountpoint  tail  sleep
```

这些是普通 shell/core utilities。在 Debian 系镜像中通常由 `coreutils`、`util-linux` 等包提供。兼容性还取决于 daemon 实际使用的参数形式：`tail` 必须支持 `-f`，`sleep` 必须支持 `infinity`，`mountpoint` 必须支持 `-q`，`readlink` 必须能读取 symbolic link。

用途如下：

- Docker 使用 `sh -lc 'tail -f /dev/null'` 启动未启用 Jupyter 的 sandbox。
- BoxLite 使用 `sh -lc 'sleep infinity'` 启动 sandbox。
- BoxLite 和 Microsandbox 使用 `sh -lc` bootstrap 准备 `/workspace` 和持久化 home entry symlink，其中会调用 `mountpoint` 和 `readlink`。

仅让基础 Docker sandbox 保持运行不要求 `bash`；但使用 shell cell、runtime `shell` request、runtime SDK `shell` API 或跨 driver Jupyter 时，镜像**必须**安装 `bash`。通用自定义 guest 建议安装它。

Kubernetes 的 agent skills 功能还**必须**提供 `node`、`sh`、`tar` 和
`flock`（Debian 中由 `util-linux` 提供），以及常规文件工具 `cat`、`mktemp`、
`mv`、`basename`、`mkdir`、`rm`、`ln`、`readlink`。内容比较仅使用 Node 内置的
`fs`、`path`、`crypto` 模块。交付时先准备 sandbox 私有、可写的完整版本目录，
验证归档末尾的随机完成条目，再原子替换 canonical 软链接；`flock` 通过操作系统
进程锁串行化发布。缺少必要工具时，在替换旧目录之前失败。使用 fake API 和真实
Linux shell 的传输测试验证此工具合同，不能替代真实集群部署测试。

### 3.3 固定命令搜索路径

在受管 agent 和 runtime 执行中，`agent-compose` 会注入以下 `PATH`：

```text
/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
```

必需可执行文件**必须**安装在这些目录之一，或者通过受支持的显式环境变量指定。不要依赖镜像 `PATH` 中超出该列表的自定义目录。如果 `agent-compose-runtime` 使用标准 `#!/usr/bin/env node` launcher，则 `/usr/bin/env` 和 `node` 也必须能通过该路径找到。

### 3.4 文件系统和挂载目标

使用 daemon 默认配置时，guest 侧约定如下：

| Guest path | 所有权和用途 | 持久化约定 |
| --- | --- | --- |
| `/workspace` | Agent cwd 和 project workspace | 每个 sandbox 持久化 |
| `/data/state` | Prompt、schema、cell、provider session state、artifact | 每个 sandbox 持久化 |
| `/data/runtime` | Runtime resource、cache、MPI 和 extension | 每个 sandbox 持久化 |
| `/data/logs` | Jupyter 和 runtime log | 每个 sandbox 持久化 |
| `/etc/agent-compose/guest-abi.json` | 官方镜像烘焙的 guest ABI manifest | 归镜像所有；不持久化；未烘焙该文件的镜像中不存在 |
| `/root` | 镜像提供的真实 home 目录 | 目录本身归镜像所有 |
| `/root/.codex` | Codex config 和 state | 持久化 |
| `/root/.agents` | 投影后的 agent skill | 持久化/投影 |
| `/root/.claude` | Claude config 和 state | 持久化 |
| `/root/.opencode` | OpenCode state | 持久化 |
| `/root/.pi` | Pi config 和 state | 持久化 |
| `/root/.dsh` | DSH profile 和 state | 持久化 |
| `/root/.local/share/opencode` | OpenCode 用于续接的 session 数据 | 持久化 |
| `/root/.claude.json` | Claude root config | 持久化文件 |
| `/root/.gitconfig` | Git config | 持久化文件 |
| 指定的 `/root/.config/...` 路径 | Provider state | 持久化 |

daemon 会准备 host 侧 mount source。镜像**应该**预先将 `/workspace`、`/data/state`、`/data/runtime` 和 `/data/logs` 创建为目录，但**不得**在这些路径中存放不可被覆盖的镜像内容：

- Docker 会分别 bind mount 各个逻辑路径。
- BoxLite 和 Microsandbox 会把整个 sandbox 挂载到 `/data`，覆盖镜像原生 `/data` 内容，再将 `/workspace` 替换为指向 `/data/workspace` 的 symlink。
- 只有声明过的 home entry 保证持久化；其他 `/root` 路径保留在镜像 writable layer 中，不属于可移植持久化 ABI。

Compose volume 可以挂载到其他绝对 guest path。这些路径由具体 project 定义，不属于 baseline guest ABI。

daemon 支持 `GUEST_WORKSPACE`、`GUEST_STATE_ROOT`、`GUEST_RUNTIME_ROOT` 和 `GUEST_LOG_ROOT` override。本文定义的是默认的跨 driver ABI。修改这些路径等同于建立自定义 ABI，部署方**必须**针对每个所选 driver 验证；`/root` 仍固定不变。

## 4. Runtime CLI ABI

Agent prompt 和受管 scheduler command 不会由 daemon 直接调用 provider 工具。daemon 会通过 `sh -lc` 调用 `agent-compose-runtime`。

支持这些能力的镜像**必须**提供：

- 满足同版本 `runtime/javascript/package.json` 中 `engines.node` 范围的 Node.js（当前为 Node.js 20 或更高版本）；
- 固定 `PATH` 中名为 `agent-compose-runtime` 的可执行文件；
- 普通 run 使用的 `prompt` 和 `exec` 子命令；
- 需要交互式 prompt attach 时使用的 `stream` 子命令。

Prompt mode 的 `stream` session 支持 `codex`、`claude`、`opencode`、`pi` 和 `dsh` provider。其他 provider 会在 guest runtime interaction 打开前被拒绝。

daemon 会显式传递 workspace、state 和 home 路径，并注入：

| 变量 | 默认值/内容 |
| --- | --- |
| `HOME` | 从镜像继承；必须为 `/root` |
| `WORKSPACE` | `/workspace` |
| `STATE_ROOT` | `/data/state` |
| `RUNTIME_ROOT` | `/data/runtime` |
| `SANDBOX_ID` | 当前 sandbox ID |
| `VERSION` | 当前 daemon version |
| `AGENT_COMPOSE_RUNTIME_BASE_URL` | 配置 runtime facade 时设置 |
| `AGENT_COMPOSE_RESOLVED_MODEL` | 为本次托管 agent 调用解析的完整模型引用 |

Pi 和 DSH runtime 直接使用 `AGENT_COMPOSE_RESOLVED_MODEL`，不再解析其中的斜杠。
分步升级时，若旧 daemon 未提供该变量，新 runtime 仍接受旧的 connection/model 参数。
这个容忍**只有一个方向**：daemon 不再为旧 guest 保留 DSH 的参数前缀，因此旧 DSH guest
被新 daemon 驱动时会拿到字面 model，并在第一个斜杠处截断。请先更新 guest 镜像，或与
daemon 同时更新。该回退方式已弃用，待旧 daemon 版本退出支持后移除；新集成应使用已
解析的值。runtime 协议的其他部分仍应使用与 release 匹配的镜像。

`prompt` 和 `exec` 的 stdout payload、stream 分离、artifact 文件以及交互式 NDJSON frame 都属于协议，而不仅是 CLI 展示。自行替换 runtime 时，必须实现对应 release 的完整协议。强烈建议直接复用仓库 runtime。

### 4.1 Graceful-stop 能力

请求 graceful sandbox stop 时，daemon 只向已跟踪的外层
`agent-compose-runtime` 进程发送信号。daemon 不会发现、持有或发送信号给
runtime 的子进程树。与 daemon release 匹配的 runtime 负责取消任务、清理
子进程、持久化部分结果并最终退出自身进程。

daemon 会为每个受管 runtime execution 注入以下私有变量：

| 变量 | 内容 |
| --- | --- |
| `AGENT_COMPOSE_INTERNAL_EXECUTION_ID` | 唯一的 execution UUID |
| `AGENT_COMPOSE_INTERNAL_EXECUTION_READY_FILE` | `/tmp/agent-compose-runtime-ready/<execution-id>` |

runtime 安装 `SIGTERM` handler 后，**必须**以十进制形式将自身 PID 写入
ready file，并将文件 mode 设为 `0600`；handler dispose 时**应该**删除该
文件。同版本的仓库 runtime 已实现此协议。

daemon 会打开独立的 driver control execution，读取准确的 ready file，
通过 `/proc` 校验 PID 的 execution ID、可执行文件和 command line，并且只向
该 PID 发送 `SIGTERM`。支持此能力的 guest **必须**挂载可读 procfs，并提供
`sh`、`cat`、`tr`、`grep`、`readlink` 和 shell `kill`。当前 runtime
可执行文件必须解析为 `node` 或 `nodejs`，command line 必须能识别出
`agent-compose-runtime`。

忽略这些变量的旧 runtime 在普通执行场景中仍保持兼容。由于 ready file
不会出现，graceful stop 会等待完整 grace period，再安全升级为 sandbox
stop。不完整或已失效的 readiness 最终也会超时；缺少 control utility 则会
被视为信号发送失败。两种结果都会安全升级，daemon 都不会扫描或管理任意
guest 进程。

### 4.2 LLM facade 变量

daemon 托管 agent 的 LLM 连接时，会把这些 facade 变量发布到 sandbox 环境里，因此在容器启动时读取它们的镜像能直接拿到。所有值都是 facade 路由和 facade token，上游凭据不会下发到 guest。

| 变量 | 内容 |
| --- | --- |
| `AGENT_COMPOSE_SANDBOX_TOKEN` | 本次托管调用的 facade token |
| `LLM_API_ENDPOINT` | 已解析连接的 facade endpoint |
| `LLM_API_KEY` | 同一个 facade token，供读取通用契约的镜像使用 |
| `LLM_API_PROTOCOL` | facade 为该连接使用的上游协议 |
| `LLM_MODEL` | facade token 授权的模型引用 |
| `AGENT_COMPOSE_RESOLVED_MODEL` | 同一模型在 agent 侧寻址命名空间中的写法 |

被选中的 agent 还会带上自己 dialect 的变量名。codex、claude、opencode、pi 各自只面向一个 provider family，因此都会写全该家族的整套变量：OpenAI 家族为 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`CODEX_MODEL`、`OPENAI_MODEL`；Anthropic 家族为 `ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_MODEL`、`CLAUDE_MODEL`。只覆盖其中一部分的 dialect 会让其余变量保留启动兼容 facade 的值，从而把本次运行的 token 与 facade 解析出的模型（而不是本次运行解析出的模型）凑在一起。此外 opencode 还有 `OPENCODE_CONFIG`、`OPENCODE_MODEL`，pi 还有 `PI_CODING_AGENT_DIR`；dsh 不写任何 provider 专属变量，启动兼容 facade 的整套值因此原样保留。

早于通用 `LLM_*` 契约的旧镜像由 provider 专属变量名服务。`ANTHROPIC_MODEL` 与 `CLAUDE_MODEL` 是一对，`CODEX_MODEL` 与 `OPENAI_MODEL` 是另一对。daemon **两对都会发布**，取值来自已启用的 provider，外加 catalog 默认模型、声明的上游，或 sandbox / 全局环境里的 provider 凭据。镜像读哪个家族取决于它自己的 entrypoint，而不是 daemon 选中的 agent：agent 是 codex 的 sandbox 完全可能跑一个读 `ANTHROPIC_API_KEY` 的镜像，只发布被选中 agent 的家族会让这种镜像拿不到启动所需的变量。只有当某个家族没有已启用的 provider、或没有可发布的模型时才会跳过它。

这些变量里的模型名就是下发该 token 时使用的模型，但 token 授权的是它命名的**连接**，而不是那一个模型：请求里出现 token 未记录的模型名时会被**原样转发给那条连接**，**由该连接决定自己服务哪些模型**。因此镜像**建议**使用被下发的模型或已解析的引用，而不是镜像内置的模型名 —— 上游不服务的名字会在上游失败，返回一个需要镜像自己去解释的错误，而不是在 facade 处被挡住。

这两对变量对 guest 而言并不等价。被选中 agent 所在家族还会被该 agent 自己的配置写入，从而覆盖启动兼容 facade 为该家族写的名字，并把它们指向本次运行解析出的模型。另一个家族没有这样的归属者：它存在的意义是让只读该家族的镜像也能启动，其 token 不记录模型，因为 daemon 并不配置那个镜像，无从预知它的 entrypoint 会要哪个模型。无论哪个家族，只要请求里的模型名不是 token 记录的那一个，都会被原样转发给 token 命名的连接。

## 5. 可选能力要求

### 5.1 Agent Provider

只需安装 project 实际选择的 provider。除非已经独立验证过其他组合，否则 runtime adapter 和 provider executable 应使用同一个仓库 tag 下官方 guest Dockerfile 对应的版本。

| Provider | Executable/runtime 要求 |
| --- | --- |
| Codex | Runtime package 中的 `@openai/codex-sdk`，以及通过 `CODEX_BIN`、`AGENT_COMPOSE_CODEX_BIN`、`/usr/bin/codex`、`/usr/local/bin/codex` 或 `PATH` 中 `codex` 选择的可执行文件 |
| Claude | `@anthropic-ai/claude-agent-sdk` 和 Claude Code；通过 `CLAUDE_CODE_EXECUTABLE`/`CLAUDE_CODE_PATH`、`/usr/bin/claude` 或 SDK 支持的默认位置选择 |
| OpenCode | `PATH` 中的 `opencode` 可执行文件 |
| Pi | `PATH` 中的 `pi` 可执行文件；Pi 1.x 内置 MCP 支持，不再需要 adapter extension |
| DSH | `PATH` 中的 `dsh` 可执行文件（官方 guest 会全局安装 `@deepseek-ai/dsh`）；`dsh --profile agent-compose` 还需要 `/root/.dsh/profiles/agent-compose` 下的 profile 资源 |

Provider credential 和 endpoint variable 会在执行时注入，**不得**写入镜像。

### 5.2 Jupyter

启用 Jupyter 时，镜像**必须**提供：

- `python3`；
- 可 import 的 `jupyterlab` Python module；
- `sh`，以及目录级挂载 runtime 启动路径需要的 `/bin/bash`；
- 后台启动需要的 `nohup`；
- 可写 workspace 和 log root。

daemon 会自行使用配置的 port、root directory、base URL 和 token 启动 `python3 -m jupyterlab`。镜像不需要 `EXPOSE 8888`、image startup command、预置 token 或 Jupyter password。默认 guest port 是 `8888`，但可以配置。

`bash_kernel` 和仓库内 JavaScript kernelspec 是官方镜像提供的便利能力，只有用户需要这些 kernel 时才必须安装。

### 5.3 Notebook Cell 和 Scheduler Command

直接执行 cell 时使用以下镜像命令：

| Cell/request | 所需命令 |
| --- | --- |
| Shell cell 或 runtime shell request | `bash` |
| Python cell | `python3` |
| JavaScript cell | `node` |
| Runtime exec request | Request 指定的 command |

同样，MCP server command 和任意 `agent-compose exec` command 必须由自定义镜像安装或由 workspace 提供，不属于 baseline ABI。

### 5.4 离线 Runtime SDK

官方 guest 包含：

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

控制面和 runtime CLI **不要求**该 tarball。只有 workspace Node.js 代码需要在无法访问 registry 时安装 `@chaitin-ai/agent-compose-runtime-sdk`，才需要放入与 release 匹配的 tarball。

## 6. 不属于最小约束的内容

官方默认 guest 是一个覆盖面较广的运行环境，但有意不包含 Go 工具链，也不包含 `protoc-gen-go` 和 `protoc-gen-go-grpc` 代码生成器。镜像保留独立的 `grpcurl` 用于 gRPC 诊断；`grpcurl` 虽由 Go 编写，最终镜像中并不存在 Go 工具链。Baseline ABI 不要求特定 Linux 发行版、`apt`、Go、C/C++ compiler、protobuf Go 代码生成器、Git、curl、`tini`、所有 provider CLI、Jupyter、额外 kernel 或离线 runtime SDK。

其中部分工具可能成为 workload 要求。例如 coding agent 通常需要 Git，正常 TLS 访问需要 CA certificate，仓库构建可能需要编译工具。Git workspace provisioning 由控制面在首次 runtime 启动前完成，因此它本身不会让 Git 成为 baseline image 要求。

需要编译或测试 Go 代码的 project **必须**选择安装了 Go 的开发镜像或自定义 guest。仓库中的 `guest-images/Dockerfile.devbox-archlinux` 会继续作为面向 x86_64、包含 Go 和其他构建工具的开发镜像示例；它不是默认 guest。生产部署应构建并发布适配目标架构的不可变开发镜像，再在 agent spec 中显式选择它。

### 6.1 可选 Arch Linux Guest

仓库还提供 `guest-images/Dockerfile.agent-compose-guest-archlinux`。它使用 Arch Linux 和 `pacman`，同时保留默认 guest 的 provider CLI、Jupyter 支持、JavaScript runtime、离线 runtime SDK bundle、root 用户、目录、entrypoint 和启动命令。与默认 guest 一样，其最终镜像不包含 Go 工具链；Go 只在隔离的 builder stage 中用于构建 `grpcurl`。镜像安装 Node.js 22 LTS，并且只安装 npm 原生依赖所需的编译器软件包，而不是完整的 Arch `base-devel` 软件包组。

在仓库根目录执行以下命令进行本地构建：

```bash
task image:agent-compose-guest-archlinux
```

生成的 tag 为 `agent-compose-guest:archlinux`。该 task 会显式构建 `linux/amd64`，因此也可以在 Arm 开发主机上通过模拟执行。构建默认使用上游 Arch mirrorlist 以及 Go、PyPI 和 npm 官方 registry。可通过 `IMAGE_TAG` 覆盖 tag；如果部署需要固定 base tag 或使用其他软件包镜像，可设置 `ARCHLINUX_TAG` 或 `ARCHLINUX_MIRROR`：

```bash
IMAGE_TAG=registry.example.com/team/agent-compose-guest:vX.Y.Z-archlinux \
ARCHLINUX_TAG=<pinned-tag> \
task image:agent-compose-guest-archlinux
```

镜像 CI 会在 pull request 中构建该变体，并在推送到 `main` 或任意 Git tag 时将其发布到 Docker Hub 的 `chaitin/agent-compose-guest`。它的分支、Git tag 和 SHA tag 使用 `-archlinux` 后缀，`v*` release tag 还会更新稳定 tag `archlinux`。例如，release `vX.Y.Z` 会同时发布 `vX.Y.Z-archlinux` 和 `archlinux`。manifest 仅包含 `linux/amd64`。默认构建使用的上游 `library/archlinux` 镜像仅支持 x86_64；如果团队为其他架构提供了兼容的 Arch Linux base，必须单独构建并验证该变体。Arch Linux guest 不是部署默认镜像，使用时需要在 agent spec 中显式选择，并在投入使用前执行第 10 节中的验证。

### 6.2 仓库镜像构建参数

仓库镜像任务按标准生态变量的原名传递参数。Taskfile 只负责编排，不定义 `BUILD_*` 别名；变量为空时不会传给 Docker，从而保留 Dockerfile 中面向公网环境的默认值。

| 变量 | 作用范围 | 适用任务 |
| --- | --- | --- |
| `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`NO_PROXY` | Docker 构建网络访问 | daemon、两种 guest、native runtime artifact 导出 |
| `REGISTRY_MIRROR` | 仅 Docker 基础镜像地址 | daemon、两种 guest、native runtime artifact 导出 |
| `GOPROXY` | Go module 下载 | daemon 和两种 guest |
| `GITHUB_MIRROR` | BoxLite、Microsandbox 的 GitHub Release 下载 | daemon native runtime artifact 导出 |
| `NPM_CONFIG_REGISTRY` | npm 包下载 | 两种 guest |
| `PIP_INDEX_URL`、`PIP_TRUSTED_HOST` | Python 包下载 | 两种 guest |
| `ARCHLINUX_MIRROR` | pacman 包下载 | 仅 Arch Linux guest |

同时接受小写的 `http_proxy`、`https_proxy`、`all_proxy` 和 `no_proxy`，并将其规范化为对应的 Docker 代理构建参数。其余构建控制变量如下：

| 变量 | 作用范围 |
| --- | --- |
| `DOCKER_DEFAULT_PLATFORM` | Docker 目标平台；daemon task 从 `GOARCH` 推导，Arch Linux guest 默认为 `linux/amd64` |
| `IMAGE_TAG` | daemon task 与两种 guest task 的本地镜像 tag |
| `NO_CACHE=1` | 传递 Docker `--no-cache` 参数 |
| `GO_VERSION`、`GRPCURL_VERSION`、`NODE_MAJOR` | 默认 guest 工具链参数；`NODE_MAJOR` 仅适用于 Debian guest |
| `ARCHLINUX_TAG` | Arch Linux guest 基础镜像 tag |
| `GUEST_ABI` | 由 label 和烘焙 manifest 声明的 guest ABI 契约版本；默认取 lock 中的 `guestAbi` |
| `CODEX_VERSION`、`CLAUDE_CODE_VERSION`、`OPENCODE_VERSION`、`PI_AGENT_VERSION`、`DSH_VERSION` | guest provider 包版本；各自默认取 `guest-images/providers.lock.json` 中对应的固定版本 |

`guest-images/providers.lock.json` 是 provider 版本的唯一编写点。`scripts/build-agent-compose-guest.sh` 会从 lock 读取每个 provider 版本和 `GUEST_ABI`，因此即使不设置这些变量，构建仍然是固定且可复现的；显式设置环境变量可为一次性本地构建覆盖 lock。当 lock、两个 guest Dockerfile 的 `ARG` 默认值、构建脚本的转发或生成的 `guest-images/guest-abi.json` 任意一处不一致时，`scripts/provider-lock.sh check` 会失败。

`REGISTRY_MIRROR`、`GITHUB_MIRROR` 和 `ARCHLINUX_MIRROR` 没有跨工具通用的标准环境变量，因此保留为作用域明确的项目参数。`REGISTRY_MIRROR` 不是 dockerd mirror 配置，只负责改写仓库 Dockerfile 中的基础镜像地址。

例如：

```bash
task image:agent-compose-guest \
  HTTPS_PROXY=http://proxy.example.com:8080 \
  REGISTRY_MIRROR=mirror.example.com \
  GOPROXY=https://goproxy.example.com,direct \
  NPM_CONFIG_REGISTRY=https://npm.example.com \
  PIP_INDEX_URL=https://pypi.example.com/simple
```

写在 task 名称后的参数与导出同名环境变量效果一致。不要把凭据写入提交文件或构建日志。daemon 镜像任务从 `GOARCH` 推导 `DOCKER_DEFAULT_PLATFORM`，保证 binary 与 BoxLite/Microsandbox artifact 使用同一架构。

## 7. 推荐构建方式：继承官方 Guest

这种方式维护面最小，并保留所有受支持能力：

```dockerfile
ARG AGENT_COMPOSE_VERSION=vX.Y.Z
FROM chaitin/agent-compose-guest:${AGENT_COMPOSE_VERSION}

# 只添加项目特有工具，保留默认 root user 和路径。
RUN apt-get update \
    && apt-get install -y --no-install-recommends jq rsync \
    && rm -rf /var/lib/apt/lists/*

COPY ./company-ca.crt /usr/local/share/ca-certificates/company-ca.crt
RUN update-ca-certificates
```

将 `vX.Y.Z` 替换为与 daemon release 匹配的不可变 tag 或 digest。如果部署同时使用两个架构，应构建和发布多架构镜像：

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg AGENT_COMPOSE_VERSION=vX.Y.Z \
  -f Dockerfile.guest \
  -t registry.example.com/team/agent-compose-guest:vX.Y.Z-custom.1 \
  --push .
```

不要把 secret、model API key、runtime token 或可变 project state 写入 image layer。

## 8. 从零构建示例

以下示例从当前仓库 checkout 构建一个仅支持 Codex 的 guest。它有意不安装 Jupyter、Python、Go、compiler、其他 provider CLI 和离线 SDK tarball。Docker build context **必须**是仓库根目录，`CODEX_VERSION` **应该**与同一个 checkout 的 `guest-images/providers.lock.json` 中的固定版本一致（`scripts/provider-lock.sh` 会保持 guest Dockerfile 的 `ARG` 默认值与其一致）。

```dockerfile
FROM node:22-bookworm-slim

ARG CODEX_VERSION

RUN test -n "${CODEX_VERSION}" \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
         bash ca-certificates coreutils git util-linux \
    && npm install -g "@openai/codex@${CODEX_VERSION}" \
    && rm -rf /var/lib/apt/lists/* /root/.npm

COPY runtime/javascript /tmp/agent-compose-runtime

RUN cd /tmp/agent-compose-runtime \
    && npm ci \
    && runtime_tarball="$(npm pack --silent | tail -n 1)" \
    && npm install -g "./${runtime_tarball}" \
    && rm -rf /tmp/agent-compose-runtime /root/.npm

RUN mkdir -p \
      /root/.agents /root/.claude /root/.codex /root/.opencode /root/.pi /root/.dsh \
      /workspace /data/state /data/runtime /data/logs

ENV HOME=/root
USER root

# 方便直接执行 `docker run`；agent-compose driver 会使用自己的 lifecycle
# command 替换镜像启动命令。
CMD ["sleep", "infinity"]
```

从匹配的 checkout 构建：

```bash
docker build \
  --build-arg CODEX_VERSION=<codex-version-from-providers.lock.json> \
  -f Dockerfile.guest \
  -t registry.example.com/team/agent-compose-guest:custom .
```

该示例复制 runtime source，避免将任意 npm runtime version 与 daemon 混用。按需增加第 5 节中的可选能力依赖。

## 9. 选择镜像

在 agent 上指定镜像：

```yaml
agents:
  reviewer:
    provider: codex
    image: registry.example.com/team/agent-compose-guest:vX.Y.Z-custom.1
    driver:
      docker: {}
```

在准备好的 Linux/KVM 部署上，同一个 OCI image reference 也可以由 BoxLite 或 Microsandbox 使用。仅通过 Docker 测试不能证明跨 driver 兼容。

如果要将镜像设为部署默认值，可在 daemon 配置中设置 `DEFAULT_IMAGE` 或对应的 driver-specific default。Agent 显式指定的 image 优先于默认值。

## 10. 验证

### 10.1 静态约定检查

根据镜像计划支持的能力调整可选检查：

```bash
image=registry.example.com/team/agent-compose-guest:custom

docker run --rm --entrypoint sh "$image" -lc '
  set -eu
  test "$(id -u)" = 0
  test "${HOME}" = /root
  test -d /root
  for command in sh mkdir test rm ln readlink mountpoint tail sleep; do
    command -v "$command" >/dev/null
  done
  for path in /workspace /data/state /data/runtime /data/logs; do
    test -d "$path"
  done

  # Runtime/Codex 能力检查：
  command -v node >/dev/null
  command -v agent-compose-runtime >/dev/null
  command -v codex >/dev/null
  agent-compose-runtime --help >/dev/null
'
```

如果包含 Jupyter，再执行：

```bash
docker run --rm --entrypoint sh "$image" -lc '
  command -v bash >/dev/null
  command -v nohup >/dev/null
  python3 -c "import jupyterlab"
'
```

### 10.2 仓库生命周期测试

Docker image lifecycle smoke 会使用真实 daemon image 验证 sandbox 创建、直接 exec、workspace 持久化、stop/resume 和删除：

```bash
task image:agent-compose

AGENT_COMPOSE_E2E_DAEMON_IMAGE=agent-compose:latest \
AGENT_COMPOSE_E2E_GUEST_IMAGE=registry.example.com/team/agent-compose-guest:custom \
task test:e2e:image-docker
```

该 smoke 不会调用 model provider。它只证明 baseline Docker lifecycle，不证明 runtime/provider 层。

如果镜像包含 Jupyter，执行：

```bash
AGENT_COMPOSE_E2E_DOCKER_JUPYTER_IMAGE=registry.example.com/team/agent-compose-guest:custom \
task test:e2e:docker-jupyter
```

对于 BoxLite 或 Microsandbox，只能在准备好的 Linux/KVM host 上运行 `task test:runtime-smoke`，之后再使用自定义镜像执行真实 project run。Provider 验收应至少为每个已安装 provider 执行一次真实 prompt；如果依赖 resume/session state，还应验证对应状态保持行为。

## 11. 升级检查清单

daemon release 与 guest 镜像构建现在是两条独立的轨道。`guestAbi` 决定两条轨道的产物能否组合，`scripts/provider-lock.sh check` 则保证仓库自身的 guest 构建保持一致。仍然把两条轨道绑在一起的只有两点：刻意提高 `guestAbi`，以及第 11.1 节中的 DSH profile。

升级 daemon 时：

1. 确认新 daemon 支持的 guest ABI（`pkg/guestabi` 中的 `CurrentGuestAbi`；`scripts/provider-lock.sh guest-abi-version` 输出本 checkout 构建时使用的值）。它只在 daemon/guest 契约变化时才会改变。
2. 如果该值发生变化，需要在新的值上重建并重新发布自定义 guest 镜像（第 11.2 节），并更新 project/deployment 中的镜像引用。如果没有变化，仅包含 provider 的现有镜像可以继续使用，不必因为这次 daemon 升级而重建。
3. 只要 daemon release 改变 runtime 协议，就将自定义镜像 rebase 到匹配的官方 guest tag，或从匹配的 source tag 重新构建 `runtime/javascript`。Runtime CLI 及其 stdout 协议仍是 release boundary（第 1 节）。
4. 对已启用能力重新运行静态检查、Docker lifecycle test，以及 Jupyter、provider 和所选 driver 的验收测试。
5. 发布不可变 tag 或 digest，并显式更新 project/deployment reference；不要静默移动生产兼容 tag。

### 11.1 DSH profile 来自 daemon，且按 sandbox 冻结

`dsh` 是唯一行为依赖 daemon 产物而不只依赖镜像的 provider：daemon 内嵌 `agent-compose` profile，并在 sandbox home 首次初始化时把它复制进该 sandbox 的 `home/.dsh`。因此两者必须匹配。

- daemon 只在目标不存在时才写入 `home/.dsh`（[`pkg/driver/runtime_mount_manifest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/runtime_mount_manifest.go) 中的 `initializeSandboxHomeDefaults` 会跳过已存在的条目），而 home 目录是按 sandbox 持久化的。
- 因此 sandbox 运行的是**首次初始化其 home 的那个 daemon** 的 profile，而不是当前部署的 daemon 的 profile。升级 daemon 不会刷新已有 sandbox。
- k8s driver 按需把同一份宿主 home 快照推送到 guest，所以这条规则对三种 driver 都成立。

后果：

- 请**同批**安装或升级 daemon 与 guest 镜像。基于旧 DSH API 构建的 guest 镜像无法激活新 daemon 内嵌的 profile，反之亦然。
- 升级前创建的 sandbox 会继续使用旧 profile。daemon 升级后请重建长期存活的 sandbox，不要指望它们自动采用新 profile。
- 当 profile 无法激活时，runtime 现在会报出可操作的错误并终止 run，而不是无限等待。

实现的事实来源包括：

- [`guest-images/Dockerfile.agent-compose-guest`](https://github.com/chaitin/agent-compose/blob/main/guest-images/Dockerfile.agent-compose-guest)
- [`guest-images/Dockerfile.agent-compose-guest-archlinux`](https://github.com/chaitin/agent-compose/blob/main/guest-images/Dockerfile.agent-compose-guest-archlinux)
- [`pkg/driver/runtime_mount_manifest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/runtime_mount_manifest.go)
- [`pkg/driver/directory_only_guest_bootstrap.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/directory_only_guest_bootstrap.go)
- [`pkg/driver/docker_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/docker_runtime.go)
- [`pkg/driver/jupyter_guest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/jupyter_guest.go)
- [`pkg/agentcompose/adapters/agent_runner.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/agentcompose/adapters/agent_runner.go)
- [`pkg/execution/command_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/execution/command_runtime.go)
- [`runtime/javascript`](https://github.com/chaitin/agent-compose/tree/main/runtime/javascript)

### 11.2 独立于 daemon 升级 guest 镜像

只要重建后 `guestAbi` 不变，仅更新 provider 的 guest 镜像就可以安全发布，并部署到已经部署好的 daemon 之下：

- `guest-images/providers.lock.json` 中的 provider 版本发生变化，`scripts/provider-lock.sh bump` 会一次性改写 lock、两个 guest Dockerfile 和 `guest-images/guest-abi.json`。
- `guestAbi` 不变，因为 daemon/guest 契约和 runtime CLI 协议都没有变化。`scripts/tests/test-provider-lock.sh` 会守住这一性质：它在 lock 的副本中提升某个 provider 版本，如果渲染出的 `guestAbi` 跟着变化就判定失败。
- 重建后的镜像仍需要针对被改动的 provider 执行 guest 镜像验收测试。仅凭这个数字无法证明 provider CLI 的行为仍符合 runtime adapter 的预期；这也是每日检查只做报告、绝不改写固定版本的原因。

上述安全性成立的前提是这次重建唯一变化的输入是 provider 固定版本。同一次镜像构建也会打包其 checkout 中的 `runtime/javascript`，而 runtime CLI 协议是 `guestAbi` 未覆盖的 release boundary（第 1 节）。如果重建同时带入了 runtime 源码变化，仍应按常规将该镜像与 daemon release 匹配。

任何会提高 `guestAbi` 的改动都不是纯 guest 改动。提高它意味着 daemon/guest 契约发生变化，因此必须先发布带有匹配 `CurrentGuestAbi` 的 daemon，新取值的镜像才能被采用。除此之外没有更宽的版本矩阵：当 manifest 的 `guestAbi` 等于 daemon 的 `CurrentGuestAbi` 时即为兼容，比较逻辑就是这一条（`pkg/guestabi/compat.go` 中的 `VerifyCompatible`）。

伴生包可以记录「某个 provider 的后续版本会让它变得多余」，从而让「升级时记得摘掉它」由升级动作本身强制，而不是靠人记。`pi-mcp-adapter` 曾带有：

```json
"retiredBy": {
  "package": "pi",
  "version": "0.99.0",
  "reason": "Pi 0.99 and later ship a built-in MCP extension, so the adapter only exists to add MCP to older Pi releases"
}
```

当被退役的包仍与不低于 `retiredBy.version` 的版本一起固定时，`scripts/provider-lock.sh check` 会失败，并在消息中点名该包与原因。修复属于这次升级的一部分：从 lock、两个 guest Dockerfile 和构建脚本中移除该包。`pi` 升级到 1.3.0 时该门禁触发，adapter 因此被移除；当前 lock 已不再固定伴生包，该机制保留给下一个伴生包。

[`.github/workflows/guest-image-release.yml`](https://github.com/chaitin/agent-compose/blob/main/.github/workflows/guest-image-release.yml) 是独立的发布路径。手动 `workflow_dispatch` 时，它会校验 lock、构建可加载的 `linux/amd64` 镜像、运行 `scripts/verify-guest-image-abi.sh`、探测 baseline guest ABI、打印镜像内 `agent-compose-runtime abi --json` 报告，然后才可选地发布多架构镜像。它的 tag 规则为：

- 不可变 tag `guest-abi<N>-<YYYYMMDD>-<shortsha>`；可选的 Arch Linux 变体再加 `-archlinux` 后缀；
- 移动别名 `guest-abi<N>`，仅在设置 `promote_alias` 时才会更新。

它刻意从不移动 `latest`：部署通过 `DEFAULT_IMAGE` 默认使用 `chaitin/agent-compose-guest:latest`，静默改指该 tag 会替换所有现有安装的 guest。

在选择镜像之前，先用构建它的 checkout 校验已发布的镜像：

```bash
scripts/verify-guest-image-abi.sh \
  registry.example.com/team/agent-compose-guest:guest-abi1-20260101-abc1234
```

该脚本会把镜像的 `io.agent-compose.guest-abi` label、镜像内烘焙的 manifest 以及每个 agent provider 版本与本 checkout 的 `guest-images/providers.lock.json` 逐一比对，打印发现的 provider，并在任何一处不一致时以非零状态退出。请针对 digest 或不可变 tag 执行，不要针对移动别名；provider 版本是与执行该脚本的 checkout 的 lock 比较的，因此要使用构建该镜像的 checkout。

### 11.3 每日上游依赖检查

[`.github/workflows/upstream-check.yml`](https://github.com/chaitin/agent-compose/blob/main/.github/workflows/upstream-check.yml) 每天 UTC `17 1 * * *` 运行 `scripts/check-upstreams.sh`，并支持手动 `workflow_dispatch`。它会比较两类固定的上游依赖，并把结果汇报到团队已经关注的位置：

- 将 `guest-images/providers.lock.json` 中固定的每个包与 npm 当前发布的版本比较；
- 将 `drivers.lock.json` 中固定的每个 native runtime driver 与其仓库最新的稳定 GitHub Release 比较。该 lock 目前覆盖 BoxLite 与 Microsandbox。Docker 与 Kubernetes 刻意不在扫描范围内：它们是宿主机提供的部署依赖，而不是本仓库按固定 SDK 构建的产物，因此这里没有需要跟随的版本。

当 driver lock 条目与实际构建它的 `Dockerfile` ARG 不一致，或与必须保持同步的 Go SDK module 不一致时，扫描会拒绝运行。提交的契约测试也会检查现有固定版本，因此只改其中一处的变更会在 CI 失败。

- 配置了 `DINGTALK_DEV_TARGETS_JSON` secret 时，通过 `scripts/notify-dingtalk.sh` 把一份简报推送到研发钉钉群。这份简报就是为群消息写的：使用中文，且只列出需要关注的内容——有哪些固定的 provider 或 driver 版本落后于上游（含当前版本与最新版本）、哪些包已被其他 provider 的版本取代、以及有多少个 npm 或 GitHub Release 查询失败。这是独立于发版通知的渠道：发版公告由 release workflow 读取的 `DINGTALK_TARGETS_JSON` secret 发送。该 webhook 就是通知渠道：没有需要盯的 tracking issue。若未配置该 secret，运行仍然成功，但会输出 warning 并在 job summary 中说明——否则「静默无通知」和「全部是最新」看起来完全一样；
- 同一份简报写入 job summary，并作为 draft bump pull request 的正文。

该 workflow 不会自行修改任何 provider 或 driver 固定版本。若某个包的 lock 条目记录了 `retiredBy` 版本，简报会同时给出该计划，且不会把它放进 bump 列表：预期动作是在使其退役的那次升级里删除它，而不是跟随它自己的版本。`workflow_dispatch` 输入 `bump` 会额外打开一个 draft pull request，其中只对过期的 provider 版本执行 `provider-lock.sh bump`，再运行 `scripts/provider-lock.sh check`。driver 固定版本只报告、不自动提升：它们的 Dockerfile 产物、lock 条目和 Go SDK module 必须一起移动，因此由人工升级。之所以是 draft：provider 版本提升本身是机械操作，而判断某个 release 是否安全不是。check 失败不会阻止该 draft —— 失败内容会写进 PR 正文，等 draft 标记为 ready 后由 CI 强制。两个可选 repository variable 可以细化扫描：`PROVIDER_UPGRADE_MENTIONS` 会追加到推送简报用于 @ 提醒，`NPM_CONFIG_REGISTRY` 用于 npm 查询。

GitHub 的 scheduler 是尽力而为的，因此应把 job summary 视为持久记录，把钉钉推送视为通知渠道：

- `cron` 可能被延迟，且定时 workflow 只在默认分支上运行；
- 仓库连续 60 天无活动后，定时 workflow 会被禁用；
- 定时 workflow 不会在 fork 中运行。

也可以随时手动执行同一扫描：

```bash
scripts/check-upstreams.sh
```
