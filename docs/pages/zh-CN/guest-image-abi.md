# 自定义 Guest Image ABI

本文定义当前 `agent-compose` 与 OCI guest image 之间的最小约定，并说明如何构建、选择和验证自定义镜像，而不必照搬官方
`ghcr.io/chaitin/agent-compose-guest` 镜像中的全部工具。

这是一份按能力分层的约定。只用于直接执行命令的 sandbox 镜像，所需软件远少于同时运行 Codex、Claude、Gemini、OpenCode、Pi、JupyterLab 和所有 notebook cell 类型的镜像。

本文使用 **必须**、**应该** 和 **可以** 描述兼容性要求。

## 1. 兼容模型

目前没有 guest ABI version label，也没有能证明任意 guest image 与某个 daemon release 匹配的启动握手。daemon 选择镜像、挂载 sandbox state、通过 runtime driver 启动镜像，再按照路径和协议约定执行命令。

因此：

- 自定义镜像**必须**针对生产环境使用的每个 `agent-compose` release 和 runtime driver 进行测试。
- 包含 `agent-compose-runtime` 的镜像**应该**从与 daemon 相同的 Git tag 或 commit 构建 runtime。Runtime CLI 及其 stdout 协议是内部 release boundary，不保证任意跨版本兼容。
- 最稳妥的自定义方式是继承与 daemon tag 匹配的不可变官方 guest tag。
- 多架构 tag **必须**包含 runtime host 使用的架构。官方发布矩阵是 `linux/amd64` 和 `linux/arm64`。

本文约定的是镜像文件系统和进程环境。KVM 访问、Docker 可达性、镜像拉取、daemon 权限、网络策略、模型凭据及外部服务健康度属于部署问题，不是 guest image ABI 属性。

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

Docker 和 BoxLite 在启动 sandbox 时会替换镜像 entrypoint 和 command；Microsandbox 也会执行显式的启动后 bootstrap。因此 `CMD`、`ENTRYPOINT`、`EXPOSE`、health check 和 image label 都不是 ABI 要求。它们可以方便手动运行镜像，但必需的初始化内容**必须**预先写入镜像文件系统。

### 3.2 控制面所需命令

对于三个 runtime driver，镜像都**必须**提供支持 `-lc` 的 `sh`。跨 driver 镜像还**必须**在固定 runtime `PATH` 中提供：

```text
mkdir  test  rm  ln  readlink  mountpoint  tail  sleep
```

这些是普通 shell/core utilities。在 Debian 系镜像中通常由 `coreutils`、`util-linux` 等包提供。兼容性还取决于 daemon 实际使用的参数形式：`tail` 必须支持 `-f`，`sleep` 必须支持 `infinity`，`mountpoint` 必须支持 `-q`，`readlink` 必须能读取 symbolic link。

用途如下：

- Docker 使用 `sh -lc 'tail -f /dev/null'` 启动未启用 Jupyter 的 sandbox。
- BoxLite 使用 `sh -lc 'sleep infinity'` 启动 sandbox。
- BoxLite 和 Microsandbox 使用 `sh -lc` bootstrap 准备 `/workspace` 和持久化 home entry symlink，其中会调用 `mountpoint` 和 `readlink`。

仅让基础 Docker sandbox 保持运行不要求 `bash`；但使用 shell cell、runtime `shell` request、runtime SDK `shell` API 或跨 driver Jupyter 时，镜像**必须**安装 `bash`。通用自定义 guest 建议安装它。

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
| `/root` | 镜像提供的真实 home 目录 | 目录本身归镜像所有 |
| `/root/.codex` | Codex config 和 state | 持久化 |
| `/root/.agents` | 投影后的 agent skill | 持久化/投影 |
| `/root/.claude` | Claude config 和 state | 持久化 |
| `/root/.opencode` | OpenCode state | 持久化 |
| `/root/.pi` | Pi config 和 state | 持久化 |
| `/root/.gemini` | Gemini state | 持久化 |
| `/root/.claude.json` | Claude root config | 持久化文件 |
| `/root/.gitconfig` | Git config | 持久化文件 |
| 指定的 `/root/.config/...` 和 `/root/.local/share/gemini` 路径 | Provider state | 持久化 |

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

Prompt mode 的 `stream` session 支持 `codex`、`claude`、`opencode` 和 `pi` provider。其他 provider 会在 guest runtime interaction 打开前被拒绝。

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

`prompt` 和 `exec` 的 stdout payload、stream 分离、artifact 文件以及交互式 NDJSON frame 都属于协议，而不仅是 CLI 展示。自行替换 runtime 时，必须实现对应 release 的完整协议。强烈建议直接复用仓库 runtime，协议详见 [agent-compose 与 runtime 调用约定](https://github.com/chaitin/agent-compose/blob/main/docs/design/agent-compose-runtime_contract.md)。

## 5. 可选能力要求

### 5.1 Agent Provider

只需安装 project 实际选择的 provider。除非已经独立验证过其他组合，否则 runtime adapter 和 provider executable 应使用同一个仓库 tag 下官方 guest Dockerfile 对应的版本。

| Provider | Executable/runtime 要求 |
| --- | --- |
| Codex | Runtime package 中的 `@openai/codex-sdk`，以及通过 `CODEX_BIN`、`AGENT_COMPOSE_CODEX_BIN`、`/usr/bin/codex`、`/usr/local/bin/codex` 或 `PATH` 中 `codex` 选择的可执行文件 |
| Claude | `@anthropic-ai/claude-agent-sdk` 和 Claude Code；通过 `CLAUDE_CODE_EXECUTABLE`/`CLAUDE_CODE_PATH`、`/usr/bin/claude` 或 SDK 支持的默认位置选择 |
| Gemini | `PATH` 中的 `gemini` 可执行文件 |
| OpenCode | `PATH` 中的 `opencode` 可执行文件 |
| Pi | `PATH` 中的 `pi` 可执行文件 |

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

需要编译或测试 Go 代码的 project **必须**选择安装了 Go 的开发镜像或自定义 guest。仓库中的 `guest-images/Dockerfile.devbox-archlinux` 是一个面向 x86_64、包含 Go 和其他构建工具的开发镜像示例，不是默认 guest。生产部署应构建并发布适配目标架构的不可变开发镜像，再在 agent spec 中显式选择它。

## 7. 推荐构建方式：继承官方 Guest

这种方式维护面最小，并保留所有受支持能力：

```dockerfile
ARG AGENT_COMPOSE_VERSION=vX.Y.Z
FROM ghcr.io/chaitin/agent-compose-guest:${AGENT_COMPOSE_VERSION}

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

以下示例从当前仓库 checkout 构建一个仅支持 Codex 的 guest。它有意不安装 Jupyter、Python、Go、compiler、其他 provider CLI 和离线 SDK tarball。Docker build context **必须**是仓库根目录，`CODEX_VERSION` 应与同一个 checkout 的官方 guest Dockerfile 保持一致。

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
      /root/.agents /root/.claude /root/.codex /root/.gemini /root/.opencode /root/.pi \
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
  --build-arg CODEX_VERSION=<version-from-guest-Dockerfile> \
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

每次升级 daemon 时：

1. 将自定义镜像 rebase 到匹配的官方 guest tag，或者从匹配 source tag 重新构建 `runtime/javascript`。
2. 根据 `guest-images/Dockerfile.agent-compose-guest` 对齐 provider CLI version。
3. 重新运行静态检查和 Docker lifecycle test。
4. 对已启用能力重新运行 Jupyter、provider 和所选 driver 验收测试。
5. 发布不可变 tag 或 digest，并显式更新 project/deployment reference；不要静默移动生产兼容 tag。

实现的事实来源包括：

- [`guest-images/Dockerfile.agent-compose-guest`](https://github.com/chaitin/agent-compose/blob/main/guest-images/Dockerfile.agent-compose-guest)
- [`pkg/driver/runtime_mount_manifest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/runtime_mount_manifest.go)
- [`pkg/driver/directory_only_guest_bootstrap.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/directory_only_guest_bootstrap.go)
- [`pkg/driver/docker_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/docker_runtime.go)
- [`pkg/driver/jupyter_guest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/jupyter_guest.go)
- [`pkg/agentcompose/adapters/agent_runner.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/agentcompose/adapters/agent_runner.go)
- [`pkg/execution/command_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/execution/command_runtime.go)
- [`runtime/javascript`](https://github.com/chaitin/agent-compose/tree/main/runtime/javascript)
