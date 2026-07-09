# Driver-Specific Runtime Mount Manifest 设计

本文档描述当前三种 runtime driver 的 mount manifest 行为。核心原则是：保留一套逻辑 runtime mount 清单，再按 driver 使用不同机制应用。Docker 可以使用细粒度目录和文件 bind；BoxLite 和 Microsandbox 只使用目录 source，并通过 guest bootstrap 暴露兼容路径。

## 背景

早期 manifest 同时包含目录 source 和文件 source：

- 目录 source: `workspace`、`state`、`runtime`、`logs`、`home/.codex`、`home/.claude` 等。
- 文件 source: `home/.claude.json`、`home/.gitconfig`。

BoxLite 对 file source 会报错：

```text
[internal] boxlite async operation: configuration error: Volume host path is not a directory: /data/sandboxes/<sandbox_id>/home/.claude.json
```

因此当前实现按 driver 应用同一逻辑清单：

- `docker`: 将逻辑条目转换成细粒度目录和文件 bind。
- `boxlite`: 只挂载 `<sandbox> -> /data`，再通过 guest bootstrap 暴露逻辑条目。
- `microsandbox`: 只挂载 `<sandbox> -> /data`，再通过 guest bootstrap 暴露逻辑条目。

manifest 始终写入：

```text
<sandbox>/vm/mount-manifest.json
```

manifest 包含 `driver` 字段。runtime consumer 会校验 manifest driver 与当前 runtime driver 一致。

## Manifest Model

持久化 manifest 是 driver-specific applied mount 数据：

```json
{
  "version": 1,
  "driver": "boxlite",
  "mounts": [
    {
      "hostPath": "/abs/path/to/sandbox",
      "guestPath": "/data",
      "type": "bind",
      "readOnly": false
    }
  ]
}
```

`loadRuntimeMountManifest` 校验：

- `version` 是当前支持版本。
- `driver` 是合法 runtime driver。
- 如果调用方传入 expected driver，manifest driver 必须匹配。
- mount `type` 必须是 `bind`。
- `hostPath` 和 `guestPath` 必须是绝对路径。

`loadDirectoryRuntimeMountManifest` 在上述校验基础上额外要求所有 `hostPath` 都是目录。BoxLite 和 Microsandbox 使用这个 loader。

## 逻辑 Runtime Mount 清单

逻辑清单是所有 driver 的语义源：

| Sandbox source | Guest path | Type |
| --- | --- | --- |
| `workspace` | `/workspace` | dir |
| `state` | `/data/state` | dir |
| `runtime` | `/data/runtime` | dir |
| `logs` | `/data/logs` | dir |
| `home/.codex` | `/root/.codex` | dir |
| `home/.claude` | `/root/.claude` | dir |
| `home/.opencode` | `/root/.opencode` | dir |
| `home/.claude.json` | `/root/.claude.json` | file |
| `home/.gitconfig` | `/root/.gitconfig` | file |
| `home/.gemini` | `/root/.gemini` | dir |
| `home/.config/claude` | `/root/.config/claude` | dir |
| `home/.config/Claude` | `/root/.config/Claude` | dir |
| `home/.config/gemini` | `/root/.config/gemini` | dir |
| `home/.config/opencode` | `/root/.config/opencode` | dir |
| `home/.local/share/gemini` | `/root/.local/share/gemini` | dir |

未列入清单的 `/root` 子路径，在 directory-only runtime 下不保证持久化。

## Docker Layout

Docker manifest 保持从逻辑清单派生的细粒度 source：

| Host path | Guest path |
| --- | --- |
| `<sandbox>/workspace` | `/workspace` |
| `<sandbox>/state` | `/data/state` |
| `<sandbox>/runtime` | `/data/runtime` |
| `<sandbox>/logs` | `/data/logs` |
| `<sandbox>/home/.codex` | `/root/.codex` |
| `<sandbox>/home/.claude` | `/root/.claude` |
| `<sandbox>/home/.opencode` | `/root/.opencode` |
| `<sandbox>/home/.claude.json` | `/root/.claude.json` |
| `<sandbox>/home/.gitconfig` | `/root/.gitconfig` |
| `<sandbox>/home/.gemini` | `/root/.gemini` |
| `<sandbox>/home/.config/claude` | `/root/.config/claude` |
| `<sandbox>/home/.config/Claude` | `/root/.config/Claude` |
| `<sandbox>/home/.config/gemini` | `/root/.config/gemini` |
| `<sandbox>/home/.config/opencode` | `/root/.config/opencode` |
| `<sandbox>/home/.local/share/gemini` | `/root/.local/share/gemini` |

Docker runtime 对每个 source 应用 `DOCKER_HOST_SANDBOX_ROOT` rebase。`.claude.json` 和 `.gitconfig` 等 file 条目仍作为 file bind source。

## BoxLite Layout

BoxLite manifest 只包含一个目录 source：

| Host path | Guest path |
| --- | --- |
| `<sandbox>` | `/data` |

BoxLite consumer 用 directory-only loader 读取该 manifest，再把 source 传给 `boxlite_options_add_volume`。

guest bootstrap 保持 `/root` 为镜像内真实目录，创建 `/workspace -> /data/workspace`，并只为声明的 home 条目创建 symlink，例如：

```text
/root/.codex -> /data/home/.codex
/root/.gitconfig -> /data/home/.gitconfig
```

默认的 `/data/state`、`/data/runtime`、`/data/logs` 已经位于 sandbox mount 内，不需要再建 symlink。

## Microsandbox Layout

Microsandbox manifest 与 BoxLite 相同，只包含一个目录 source：

| Host path | Guest path |
| --- | --- |
| `<sandbox>` | `/data` |

Microsandbox consumer 用 directory-only loader 读取该 manifest，再构造 `microsandbox.Mount.Bind`。guest bootstrap 使用与 BoxLite 相同的逻辑条目 symlink 行为。

## BoxLite / Microsandbox Host Layout

BoxLite 和 Microsandbox driver 下，fresh sandbox 的 host 侧包含逻辑 source：

```text
<sandbox>/
  workspace/
  state/
  runtime/
  logs/
  home/
    .codex/
    .claude/
    .opencode/
    .claude.json
    .gitconfig
    .gemini/
    .config/
      claude/
      Claude/
      gemini/
      opencode/
    .local/
      share/
        gemini/
  vm/
    mount-manifest.json
```

目录级挂载 `<sandbox> -> /data` 会覆盖 guest image 原生 `/data` 的最终可见内容。`/workspace` 会被重建为 symlink。`/root` 保持镜像内真实目录，只将声明的 `/root` home 条目 symlink 到 `/data/home`。这样不依赖 guest `mount --bind` 权限，也避免整体 `/root -> /data/home` 替换。

## Directory-Only Bootstrap

BoxLite 和 Microsandbox 在 sandbox/box start 或 reconnect 后执行同一段 bootstrap command。该命令以 cwd `/` 运行，发生在 Jupyter readiness 检查之前，也发生在每次 `Exec` / `ExecStream` 用户命令之前。

bootstrap 会验证 `/data/workspace` 和 `/data/home` 存在，重建 `/workspace -> /data/workspace`，确保 `/root` 是真实目录，然后创建或修复声明 home 条目的 symlink。它会拒绝替换 `/root` 下未知的非 symlink target，拒绝 mounted `/root` target，不执行 `mount --bind /data/home /root`，也不创建整体 `/root -> /data/home` symlink。

bootstrap stdout/stderr 不会混入用户命令输出。bootstrap 失败时，driver 返回带 driver、sandbox、runtime id、exit code、stdout、stderr 上下文的诊断错误；原始用户命令不会执行。

## Driver Switch Behavior

start/resume 前 manifest 始终按当前已解析 driver 重写。同一个 sandbox 如果先用 Docker 生成 manifest，再用 BoxLite 或 Microsandbox 启动，最终 manifest 会变为 directory-only layout，不会复用旧的 Docker file source mounts。

## Runtime Image Source Order

mount manifest 只描述 sandbox 数据目录如何挂载，不描述 guest rootfs 来源。BoxLite 和 Microsandbox 的 rootfs/image 解析遵循 Docker-first 策略：

- BoxLite：`BOX_ROOTFS_PATH` 非空时直接使用该目录；否则 Docker daemon 可用时先从本地 Docker image materialize OCI layout；Docker daemon 不可用或 Docker image miss 时使用 OCI cache，cache miss 会通过 go-containerregistry pull，再 materialize 到 `IMAGE_CACHE_ROOT` 同级的 `image-cache/<image-id>/oci` 并传给 BoxLite。
- Microsandbox：Docker daemon 可用时先从本地 Docker image materialize rootfs；Docker daemon 不可用或 Docker image miss 时使用 OCI cache，cache miss 会通过 go-containerregistry pull，再展开到 `IMAGE_CACHE_ROOT` 同级的 `image-cache/<image-id>/rootfs` 并作为绝对路径传给 Microsandbox。绝对 rootfs path 使用 `PullPolicyNever`。
- Docker runtime 仍只使用 Docker daemon image store，不直接消费 OCI cache。

该策略不改变 BoxLite/Microsandbox 的 directory-only mount manifest 和 guest environment contract。

## Test Coverage

当前测试覆盖以下行为：

- Docker manifest 包含 `.claude.json`、`.gitconfig` 等 file source。
- Docker mount rebase 覆盖 file source。
- BoxLite/Microsandbox manifest 不包含 file source。
- BoxLite/Microsandbox manifest 只包含 `<sandbox> -> /data`。
- BoxLite/Microsandbox manifest 中所有 host source 都是目录。
- directory-only loader 拒绝 file source。
- Docker 和 directory-only bootstrap 都派生自同一逻辑 mount 清单。
- Directory-only bootstrap 保持 `/root` 为真实目录，只把声明 home 条目暴露为 symlink。
- driver 切换会重写 manifest。

## Runtime Smoke Tests

真实 runtime 启动 smoke test 是显式 opt-in，默认 `go test` 不启动 sandbox。

开启方式：

```bash
task test:runtime-smoke
```

可以用 `SMOKE_RUNTIME_DRIVERS` 选择 driver：

```bash
SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=boxlite,microsandbox task test:runtime-smoke
```

smoke test 会真实创建并启动对应 runtime，并通过启动期 marker 验证：

- BoxLite/Microsandbox manifest 可被 directory-only loader 消费。
- manifest 不包含 `/root/.claude.json` 或 `/root/.gitconfig` 的独立 file source。
- `<sandbox>` 挂载到 `/data`。
- guest 内 `/root` 是真实目录，不是整体指向 `/data/home` 的 symlink。
- guest 内声明 home 条目，例如 `/root/.claude.json`、`/root/.gitconfig`、`/root/.codex`，解析到 `/data/home/...`。
- guest 对 `/data/state` 和声明 home 条目的写入会持久化到 host `<sandbox>/state` 和 `<sandbox>/home`。
- 设置 `SMOKE_OCI_IMAGE_REF` 时，BoxLite 会使用 OCI cache materialized layout，Microsandbox 会使用 OCI cache rootfs；测试会强制 Docker daemon 不可用，避免回退到本地 Docker materialization。

可选镜像覆盖：

- `SMOKE_DEFAULT_IMAGE`
- `SMOKE_DOCKER_DEFAULT_IMAGE`
- `SMOKE_MICROSANDBOX_DEFAULT_IMAGE`
- `SMOKE_BOX_ROOTFS_PATH`
- `SMOKE_OCI_IMAGE_REF`

`SMOKE_OCI_IMAGE_REF` 必须指向可启动 agent-compose guest 的镜像，至少包含当前 smoke 需要的 shell、Jupyter 启动依赖，以及可由 guest bootstrap 写入 `/data/state/runtime-mount-smoke.txt` 和声明 home 条目的环境。未设置时 OCI image smoke 会 skip，directory-only mount smoke 仍按原逻辑运行。
