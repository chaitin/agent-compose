# Runtime Environment Variables 设计

本文档描述当前 agent-compose 在 guest/container/sandbox 内注入和读取的 runtime environment variables。

## 设计原则

runtime mount manifest 已经把 sandbox 子目录挂到 guest 惯用路径，因此 guest 内不再需要 agent-compose 自定义的 sandbox root，也不需要通过 agent-compose 覆盖 `HOME`。

当前原则：

- workspace 用 `WORKSPACE` 表达。
- state/runtime 用 `STATE_ROOT` 和 `RUNTIME_ROOT` 表达。
- `HOME` 继承镜像默认值，当前 guest image 约定为 `/root`。
- artifact dir 是单次 command request / CLI 输入，不作为全局 env 注入。

## Guest Runtime Variables

agent-compose 注入的 guest runtime 变量：

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `WORKSPACE` | `/workspace` | workspace 位置 |
| `STATE_ROOT` | `/data/state` | cell artifacts、agent prompt/schema/provider state |
| `RUNTIME_ROOT` | `/data/runtime` | runtime JS resource/cache/MPI 等 |
| `SANDBOX_ID` | 当前 sandbox id | 日志、调试、工具上下文 |
| `VERSION` | 当前 agent-compose version | 调试和兼容判断 |
| `JUPYTER_TOKEN` | 当前 proxy token | Jupyter 启动和代理 |

不再由 agent-compose 注入或读取的历史变量：

| 变量 | 当前处理 |
| --- | --- |
| `HOME` | agent-compose 不显式注入，继承镜像默认值 |
| 独立 home override 变量 | 删除 |
| `SESSION_WORKSPACE` | 替换为 `WORKSPACE` |
| `SESSION_ROOT` | 删除或废弃，guest 侧不再存在 session root 语义 |
| `ARTIFACT_DIR` | 不作为全局 env 使用 |

## Home 约定

guest image 负责提供默认用户和默认 home：

```text
HOME=/root
```

Go 配置里的 `GuestHomePath` 固定为 `/root`，仅用于 manifest 目标路径。`NewConfig` 不读取 `GUEST_HOME`。

Home 持久化由 mount manifest 完成：

| Host path | Docker guest path | BoxLite/Microsandbox guest path |
| --- | --- | --- |
| `<sandbox>/home/.codex` | `/root/.codex` | Symlink `/root/.codex -> /data/home/.codex` |
| `<sandbox>/home/.claude` | `/root/.claude` | Symlink `/root/.claude -> /data/home/.claude` |
| `<sandbox>/home/.opencode` | `/root/.opencode` | Symlink `/root/.opencode -> /data/home/.opencode` |
| `<sandbox>/home/.claude.json` | `/root/.claude.json` | Symlink `/root/.claude.json -> /data/home/.claude.json` |
| `<sandbox>/home/.gitconfig` | `/root/.gitconfig` | Symlink `/root/.gitconfig -> /data/home/.gitconfig` |
| `<sandbox>/home/.gemini` | `/root/.gemini` | Symlink `/root/.gemini -> /data/home/.gemini` |
| `<sandbox>/home/.config/claude` | `/root/.config/claude` | Symlink |
| `<sandbox>/home/.config/Claude` | `/root/.config/Claude` | Symlink |
| `<sandbox>/home/.config/gemini` | `/root/.config/gemini` | Symlink |
| `<sandbox>/home/.config/opencode` | `/root/.config/opencode` | Symlink |
| `<sandbox>/home/.local/share/gemini` | `/root/.local/share/gemini` | Symlink |

Docker 会直接细粒度挂载这些 home 子路径。BoxLite/Microsandbox 只把整个 `<sandbox>` 目录挂到 `/data`；guest bootstrap 保持 `/root` 为真实目录，只为上表声明的 home 条目创建 symlink。其他 `/root` 子路径不保证持久化。

## Host Configuration Variables

host/control-plane 配置继续支持：

| 变量 | 默认值 |
| --- | --- |
| `GUEST_WORKSPACE` | `/workspace` |
| `GUEST_STATE_ROOT` | `/data/state` |
| `GUEST_RUNTIME_ROOT` | `/data/runtime` |
| `GUEST_LOG_ROOT` | `/data/logs` |

`GUEST_HOME` 不再作为公开配置输入使用。未来如果要支持非-root guest image，应通过 image metadata 或 runtime driver 能力确定默认 home，而不是重新让 agent-compose 覆盖 `HOME`。

## Runtime JS / SDK Defaults

`agent-compose-runtime exec` 路径默认值：

- `cwd` 默认顺序：
  1. request `cwd`
  2. CLI/default `workspace`
  3. `process.env.WORKSPACE`
  4. `/workspace`

- `home` 默认顺序：
  1. request `home`
  2. CLI/default `home`
  3. `process.env.HOME`
  4. `/root`

- `stateRoot` 默认顺序：
  1. request `stateRoot`
  2. CLI/default `stateRoot`
  3. `process.env.STATE_ROOT`
  4. `/data/state`

- `runtimeRoot` 默认顺序：
  1. request `runtimeRoot`
  2. `process.env.RUNTIME_ROOT`
  3. 从 `stateRoot` 推导，或 fallback `/data/runtime`

runtime JS 启动子进程时注入：

- `WORKSPACE`
- `STATE_ROOT`
- `RUNTIME_ROOT`

runtime JS 不注入：

- `HOME`
- `SESSION_WORKSPACE`
- `ARTIFACT_DIR`

子进程继承 runtime 进程自身的原生 `HOME`。

## Artifact Directory

artifact dir 是 command/request 范围内的路径：

- host 侧 cell 和 loader command artifacts 位于 `<sandbox>/state/cells/...`。
- guest 侧对应路径位于 `/data/state/cells/...`。
- runtime JS 仍可通过 request 或 CLI 参数接收 artifact dir。
- `ARTIFACT_DIR` 不作为全局 env 暴露。

## Current Invariants

- guest/container/sandbox 启动时，agent-compose 不显式设置 `HOME`。
- guest 内工具看到镜像默认 `HOME=/root`。
- workspace 变量统一为 `WORKSPACE`。
- runtime state 变量统一为 `STATE_ROOT` 和 `RUNTIME_ROOT`。
- `HOME`、`SESSION_WORKSPACE`、guest-side `SESSION_ROOT`、global `ARTIFACT_DIR` 不再作为当前 runtime contract 的一部分。
- 声明的 home 持久化路径在 guest 内可通过 `/root/...` 访问；对 directory-only runtime，它们是指向 `/data/home/...` 的 symlink。
