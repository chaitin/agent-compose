# Runtime Mount Manifest 设计

本文档描述当前 runtime mount manifest 的代码事实。agent-compose 在启动或恢复 runtime 前，会从同一套逻辑 runtime mount 清单生成 driver-specific manifest。Docker 将这套清单应用为细粒度 bind mounts；BoxLite 和 Microsandbox 使用 directory-only manifest，只把整个 sandbox 挂到 `/data`，再通过 guest bootstrap 暴露 `/workspace` 和声明的 `/root/...` home 条目等兼容路径。

## 设计目标

runtime 内工具应继续使用镜像默认的目录语义：

- workspace 位于 `/workspace`。
- `$HOME` 使用镜像默认值，当前约定为 `/root`。
- agent-compose 内部交换目录位于 `/data/state`、`/data/runtime`、`/data/logs`。

host 侧 sandbox 状态仍保存在 `<sandbox>` 下。Docker 不暴露 `context`、`vm`、`proxy`、`metadata.json` 等 host 控制面状态。directory-only runtime 会把 `<sandbox>` 整体暴露为 `/data`，但产品代码只依赖下文定义的逻辑路径，不依赖任意 sandbox root 内容。

## Sandbox Host Layout

`Store.CreateSandbox` 创建的 host sandbox 目录结构包括：

```text
<sandbox>/
  context/
  home/
  runtime/
  workspace/
  state/
  logs/
  vm/
  proxy/
  metadata.json
  vm/runtime.json
  proxy/jupyter.json
  state/cells.json
  state/events.json
```

guest/runtime 实际使用：

| Host path | Guest path | 用途 |
| --- | --- | --- |
| `<sandbox>/workspace` | `/workspace` | Jupyter root、cell cwd、loader command cwd、agent working directory |
| `<sandbox>/state` | `/data/state` | cell artifacts、loader request/result、agent prompt/schema/provider state |
| `<sandbox>/runtime` | `/data/runtime` | runtime JS MPI/resource/cache |
| `<sandbox>/logs` | `/data/logs` | Jupyter log |
| `<sandbox>/home` 子路径 | `/root/...` | 声明 home 条目的 sandbox-local tool config/state |

Docker 细粒度挂载不暴露：

- `<sandbox>/context`
- `<sandbox>/vm`
- `<sandbox>/proxy`
- `<sandbox>/metadata.json`

## Guest Path Defaults

默认 guest 路径：

| Config field | Default |
| --- | --- |
| `GuestWorkspacePath` | `/workspace` |
| `GuestHomePath` | `/root` |
| `GuestStateRoot` | `/data/state` |
| `GuestRuntimeRoot` | `/data/runtime` |
| `GuestLogRoot` | `/data/logs` |

`GuestHomePath` 是 manifest 目标路径，不表示 agent-compose 会覆盖 `HOME`。运行时不显式注入 `HOME`，guest 内工具继承镜像默认 home。

## Manifest File

启动或恢复 sandbox 前，agent-compose 写入：

```text
<sandbox>/vm/mount-manifest.json
```

manifest 结构：

```json
{
  "version": 1,
  "driver": "docker",
  "mounts": [
    {
      "hostPath": "/abs/path/to/sandbox/workspace",
      "guestPath": "/workspace",
      "type": "bind",
      "readOnly": false
    }
  ]
}
```

约束：

- `version` 当前为 `1`。
- `driver` 是已解析 runtime driver，取值为 `docker`、`boxlite` 或 `microsandbox`。
- `type` 当前只支持 `bind`。
- `hostPath` 和 `guestPath` 都必须是绝对路径。
- 生成 manifest 前会创建所有需要的 host source。
- runtime consumer 会按 expected driver 校验 manifest，避免旧 manifest 被错误复用。

## Home 初始化

生成 manifest 前，agent-compose 会初始化 `<sandbox>/home` 的默认配置，并且不覆盖已存在的目标：

| Asset | Host target |
| --- | --- |
| `assets/.codex` | `<sandbox>/home/.codex` |
| `assets/.claude` | `<sandbox>/home/.claude` |
| `assets/.claude.json` | `<sandbox>/home/.claude.json` |
| `assets/.gitconfig` | `<sandbox>/home/.gitconfig` |

guest 侧不再运行 `.codex` copy 同步逻辑。工具看到的 `$HOME` 仍是 `/root`，但相关配置和状态由 host sandbox home 提供持久化。

逻辑 mount 清单还会创建当前 provider 使用的声明 home 目录，包括 `.opencode`、`.gemini`、`.config/{claude,Claude,gemini,opencode}` 和 `.local/share/gemini`。

## Driver 差异

Docker 支持 file bind mount，因此 Docker manifest 保留细粒度 home 子路径挂载，包括 `.claude.json` 和 `.gitconfig` 两个 file source。

BoxLite 和 Microsandbox 不依赖 file source mount。它们只挂一个目录 source：`<sandbox> -> /data`。默认配置下 `/data/state`、`/data/runtime`、`/data/logs` 直接来自这个挂载。guest bootstrap 创建 `/workspace -> /data/workspace`，保持 `/root` 为镜像内真实目录，并只为声明的 home 条目创建 symlink，例如 `/root/.codex -> /data/home/.codex`、`/root/.claude.json -> /data/home/.claude.json` 和 `/root/.gitconfig -> /data/home/.gitconfig`。未声明的 `/root` 子路径在 directory-only runtime 下不保证持久化。

更详细的 driver-specific layout 见 `runtime_mount_manifest_driver_specific_design.md`。

## Runtime Consumers

各 runtime driver 均读取 `<sandbox>/vm/mount-manifest.json`：

- Docker 使用 `loadRuntimeMountManifest(sandbox, RuntimeDriverDocker)`，并对每个 source 应用 `DOCKER_HOST_SANDBOX_ROOT` rebase。
- BoxLite 使用 `loadDirectoryRuntimeMountManifest(sandbox, RuntimeDriverBoxlite)`，进入 `boxlite_options_add_volume` 前校验所有 source 都是目录。
- Microsandbox 使用 `loadDirectoryRuntimeMountManifest(sandbox, RuntimeDriverMicrosandbox)`，构造 `microsandbox.Mount.Bind` map 前校验所有 source 都是目录。

BoxLite 和 Microsandbox 会在 sandbox 启动或重连后、Jupyter readiness 检查前执行 `directoryOnlyGuestSandboxBootstrapCommand`，并在 `Exec` / `ExecStream` 用户命令前再次执行。bootstrap 失败时 sandbox 或 command 不会被视为 ready，返回的诊断错误会包含 driver、sandbox/runtime 上下文和 stdout/stderr 摘要。

## Runtime Paths

启动后的 guest 路径：

- Jupyter root: `/workspace`
- Jupyter log: `/data/logs/jupyter.log`
- cell/loader command artifacts: `/data/state/cells/...`
- agent prompt/schema/provider state: `/data/state/agents/...`
- runtime JS resources/cache/MPI: `/data/runtime/...`

runtime command 和 agent env 注入：

- `WORKSPACE=/workspace`
- `STATE_ROOT=/data/state`
- `RUNTIME_ROOT=/data/runtime`

不再注入：

- `HOME`
- `SESSION_WORKSPACE`
- guest-side `SESSION_ROOT`
