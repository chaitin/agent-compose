# agent-compose 命令行使用手册

agent-compose 命令行用于连接 agent-compose daemon，管理 project、agent、sandbox、日志、资源统计和镜像。它的使用模型接近 Docker Compose：配置文件定义 project，daemon 负责长期状态和运行时生命周期，CLI 负责发起操作和展示结果。

## 核心概念

- `project`：一个 `agent-compose.yml` 或 `agent-compose.yaml` 对应一个 project。配置文件所在目录是 project root。
- `agent`：project 中定义的 agent 配置。一个 project 可以包含多个 agent。
- `sandbox`：agent 的一个运行态隔离环境。一个 agent 可以创建多个 sandbox；无论底层 runtime 是 Docker、BoxLite 还是 Microsandbox，命令行都统一使用 sandbox 概念。
- `daemon`：agent-compose 的服务端进程，负责 project 状态、scheduler、sandbox 生命周期、日志、镜像和 API。

## 命令格式

```bash
agent-compose [global options] <command> [command options] [arguments]
```

全局参数位于 `agent-compose` 和子命令之间，适用于所有 project 相关命令。

| 参数 | 说明 |
| --- | --- |
| `-f, --file <path>` | 指定 project 配置文件。支持 `agent-compose.yml` 和 `agent-compose.yaml`。使用该参数后，可以在任意目录操作该 project，project root 为配置文件所在目录。 |
| `--host <endpoint>` | 指定 daemon 地址。可以连接本机 daemon，也可以连接远程 daemon。 |
| `--project-name <name>` | 覆盖配置文件中的 project 名称。适用于同一份配置在不同环境中以不同 project 名称运行的场景。 |
| `--json` | 使用 JSON 输出，供脚本、AI 和自动化系统解析。 |

示例：

```bash
agent-compose -f /path/to/project/agent-compose.yml up
agent-compose -f /path/to/project/agent-compose.yaml ps --all
agent-compose --host http://10.0.0.12:7410 ls --json
```

使用规则：

- 未指定 `-f` 时，CLI 在当前目录查找 `agent-compose.yml` 或 `agent-compose.yaml`。
- 使用 `-f` 时，不需要切换到 project root。
- `--host` 只决定 CLI 连接哪个 daemon；sandbox 实际运行在 daemon 所在环境中。
- 使用 `--host` 或 `AGENT_COMPOSE_HOST` 连接 HTTP(S) daemon 时，CLI 会从 `AUTH_USERNAME` 和 `AUTH_PASSWORD` 读取 Basic Auth 凭据；Unix socket 本地连接不使用该认证。
- 自动化场景应使用 `--json`，不要解析人类可读表格。

## 常见工作流

本地开发：

```bash
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "Review the current diff"
agent-compose logs reviewer --follow
agent-compose down
```

后台部署：

```bash
agent-compose -f /path/to/project/agent-compose.yml up
agent-compose -f /path/to/project/agent-compose.yml ps --all
agent-compose -f /path/to/project/agent-compose.yml logs --follow
```

远程 daemon：

```bash
agent-compose --host http://10.0.0.12:7410 ls
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml up
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml stats --watch
```

## `ls`：查看 project

查看当前 daemon 管理的所有 project。

```bash
agent-compose ls
agent-compose ls --limit 20 --offset 40
agent-compose ls --verbose
agent-compose ls --json
```

默认输出字段：

- `PROJECT`：project name。
- `CONFIG FILE`：配置文件路径。
- `REVISION`：当前 project revision。
- `AGENTS`：agent 数量。
- `SCHEDULERS`：scheduler 数量。
- `SERVICES`：project 关联服务数量。当前 project spec 尚未定义 service 模型，因此该列显示为 `-`。

`--verbose` 显示更多 daemon 已记录的信息，包括 project id、project root、spec hash、创建时间、更新时间和状态摘要。

选项：

| 参数 | 说明 |
| --- | --- |
| `--limit <n>` | 最多返回 n 个 project。未指定时 CLI 会自动翻页并读取完整列表。 |
| `--offset <n>` | 从指定 offset 开始读取 project。通常与 `--limit` 一起用于分页。 |
| `--verbose` | 显示更多列。 |

## `up`：启动或更新 project

读取配置文件，将 project 应用到 daemon，并启动或更新 project 中的 scheduler 和服务。

```bash
agent-compose up
agent-compose -f /path/to/project/agent-compose.yml up
```

当前 `up` 的行为是将 project 应用到 daemon 后返回，project 后续由 daemon 管理。它不会 attach project 日志，也不提供 `-d/--detach` 参数。

## `down`：关闭 project

关闭当前 project，停止 scheduler、服务和运行中的 sandbox。

```bash
agent-compose down
agent-compose -f /path/to/project/agent-compose.yml down
```

注意事项：

- `down` 只影响当前 project。
- 使用 `-f` 或 `--project-name` 时，应确认定位到的是预期 project。
- 如果部分 sandbox 停止失败，命令返回非零退出码，并在输出中说明失败项。

## `run`：运行 sandbox

为指定 agent 启动一个 sandbox，或在已有 sandbox 中继续运行。

```bash
agent-compose run <agent> <trigger>
agent-compose run <agent> --trigger <trigger>
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --command "..."
agent-compose run <agent> --sandbox <sandbox> --prompt "..."
```

输入模式：

| 模式 | 用法 | 说明 |
| --- | --- | --- |
| trigger | `run <agent> <trigger>` 或 `run <agent> --trigger <trigger>` | 运行配置中定义的 trigger。 |
| prompt | `run <agent> --prompt "..."` | 向 agent provider 发送 prompt。 |
| command | `run <agent> --command "..."` | 在 sandbox 中执行命令。 |
| sandbox 复用 | `run <agent> --sandbox <sandbox> --prompt "..."` | 在指定 sandbox 中继续运行。 |

选项：

| 参数 | 说明 |
| --- | --- |
| `-d, --detach` | 后台运行，立即返回 sandbox/run 信息。 |
| `-i, --interactive` | 交互运行。与 trigger 模式互斥。 |
| `-k, --keep-running` | 运行结束后保留 sandbox runtime。 |
| `--sandbox <sandbox>` | 指定已有 sandbox。 |
| `--jupyter [<port>]` | 在 sandbox 内启用 Jupyter。可选端口，未指定时使用默认端口。 |
| `--jupyter-expose [<addr>:]<port>` | 将 Jupyter 暴露到 daemon host。地址默认 `0.0.0.0`。 |
| `--rm` | 运行结束后删除 sandbox。 |

示例：

```bash
agent-compose run reviewer nightly
agent-compose run reviewer --trigger pr-opened
agent-compose run reviewer --prompt "Review the staged changes"
agent-compose run builder --command "task build"
agent-compose run tester --command "task test" --keep-running
agent-compose run reviewer --sandbox sandbox_123 --prompt "Continue the review"
agent-compose run researcher --prompt "Explore this dataset" -d --jupyter
agent-compose run researcher --prompt "Explore this dataset" --jupyter 8888 --jupyter-expose 127.0.0.1:18888
```

互斥规则：

- trigger、prompt、command 一次只能选择一种。
- trigger 模式与 `--interactive` 互斥。
- `--rm` 与 `--keep-running` 不能同时使用。

## `ps`：查看 sandbox

查看当前 project 下的 sandbox。默认只显示运行中的 sandbox。

```bash
agent-compose ps
agent-compose ps -a
agent-compose ps --all
agent-compose ps --status running
agent-compose ps --status exited,error
agent-compose ps --verbose
agent-compose ps --json
```

选项：

| 参数 | 说明 |
| --- | --- |
| `-a, --all` | 显示全部 sandbox，包括已结束和错误状态。 |
| `--verbose` | 显示更多列。 |
| `--status <status>[,<status>...]` | 按状态过滤。 |

默认输出字段：

- `SANDBOX`
- `AGENT`
- `STATUS`
- `RUN`
- `CREATED`
- `UPDATED`

`--verbose` 增加 project、driver、image、Jupyter、workspace 和错误摘要等信息。

## `stop`：停止 sandbox

停止一个或多个 sandbox。

```bash
agent-compose stop <sandbox>
agent-compose stop <sandbox> [<sandbox N>]
```

示例：

```bash
agent-compose stop sandbox_123
agent-compose stop sandbox_123 sandbox_456
```

## `resume`：恢复 sandbox

恢复一个或多个 sandbox 运行。

```bash
agent-compose resume <sandbox>
agent-compose resume <sandbox> [<sandbox N>]
```

示例：

```bash
agent-compose resume sandbox_123
agent-compose resume sandbox_123 sandbox_456
```

## `rm`：删除 sandbox

删除一个或多个 sandbox。

```bash
agent-compose rm <sandbox>
agent-compose rm <sandbox> [<sandbox N>]
agent-compose rm --force <sandbox>
```

选项：

| 参数 | 说明 |
| --- | --- |
| `--force` | 强制删除 running 状态的 sandbox。 |

行为规则：

- 删除非 running 状态的 sandbox 时，`rm` 会删除 sandbox 记录和相关运行态资源。
- 对 running 状态的 sandbox 执行 `rm` 会失败，并提示 sandbox is running。
- 如果确认要删除 running sandbox，必须显式使用 `--force`。强制删除会先停止 sandbox，再删除相关资源。
- 删除 sandbox 不会删除 project 配置。

示例：

```bash
agent-compose rm sandbox_123
agent-compose rm sandbox_123 sandbox_456
agent-compose rm --force sandbox_789
```

## `exec`：在 sandbox 中执行命令

在运行中的 sandbox 内执行命令，语义类似 `docker compose exec`。

```bash
agent-compose exec <sandbox>
agent-compose exec <sandbox> <command> [args...]
agent-compose exec <sandbox> --prompt "..."
agent-compose exec <sandbox> --command "..."
```

选项：

| 参数 | 说明 |
| --- | --- |
| `-d, --detach` | 后台执行。 |
| `-i, --interactive` | 交互执行。 |
| `--prompt "..."` | 在该 sandbox 中向 provider 发送 prompt。 |
| `--command "..."` | 以 flag 形式传入命令。 |

示例：

```bash
agent-compose exec sandbox_123
agent-compose exec sandbox_123 pwd
agent-compose exec sandbox_123 bash -lc "task test"
agent-compose exec sandbox_123 --prompt "Summarize current workspace state"
agent-compose exec sandbox_123 --command "git status --short"
```

## `logs`：查看日志

查看当前 project 下 agent、sandbox 或 run 的日志。默认展示 project 下所有 agent 日志。

当前 `logs` 基于 agent-compose v2 RunService 返回的 run output/artifacts 展示，也就是 `RunDetail.output` 中持久化的 agent-compose run 输出；不会默认读取 Codex、Claude、Gemini 等 provider 的私有日志文件。

```bash
agent-compose logs
agent-compose logs <agent>
agent-compose logs --agent reviewer
agent-compose logs --run-id <run-id>
agent-compose logs --sandbox <sandbox>
agent-compose logs --follow
agent-compose logs -n 100
agent-compose logs -t
```

选项：

| 参数 | 说明 |
| --- | --- |
| `-n, --tail <n>` | 只显示 run output 的最后 N 行，文本和 JSON 输出一致。 |
| `--follow` | 持续跟随日志输出。 |
| `-t, --timestamp` | 文本输出显示 run 级时间戳。当前没有逐 chunk 时间戳，会使用该 run 的 `completed_at`、`updated_at`、`started_at` 中最合适的可用时间。 |
| `--agent <agent>` | 按 agent 过滤。 |
| `--run-id <run-id>` | 按 run 过滤。 |
| `--sandbox <sandbox>` | 按 sandbox 过滤。 |

示例：

```bash
agent-compose logs
agent-compose logs reviewer
agent-compose logs --agent reviewer --tail 200
agent-compose logs --sandbox sandbox_123 --follow -t
agent-compose logs --run-id run_123 --json
```

## `inspect`：查看资源详情

查看 project 下资源或 daemon image 的详细信息。

```bash
agent-compose inspect project
agent-compose inspect agent <agent>
agent-compose inspect run <run-id>
agent-compose inspect sandbox <sandbox>
agent-compose inspect image <image>
```

说明：

- `inspect project` 查看 project spec、revision、agent、scheduler 等信息。
- `inspect agent <agent>` 查看 agent 配置和运行摘要。
- `inspect run <run-id>` 查看一次 run 的详情。
- `inspect sandbox <sandbox>` 查看 sandbox/runtime 详情。
- `inspect image <image>` 查看镜像详情。

## `stats`：查看资源消耗

查看当前 project 下 running sandbox 的资源消耗，行为类似 `docker compose stats`。默认展示当前采集值后返回。

```bash
agent-compose stats
agent-compose stats -w
agent-compose stats --watch
agent-compose stats --json
```

选项：

| 参数 | 说明 |
| --- | --- |
| `-w, --watch` | 定期刷新资源消耗，直到用户中断。 |

输出字段：

- `SANDBOX`
- `AGENT`
- `CPU %`
- `MEM USAGE / LIMIT`
- `MEM %`
- `NET I/O`
- `BLOCK I/O`

不同 runtime driver 可采集的指标可能不同。不可用指标显示为 `-`，JSON 输出保持字段结构稳定。

## 镜像命令

管理 daemon 或当前 project 相关的镜像。

```bash
agent-compose images
agent-compose pull <image>
agent-compose push <image>
agent-compose rmi <image>
agent-compose inspect image <image>
```

命令说明：

- `images`：列出镜像。
- `pull <image>`：拉取镜像。
- `push <image>`：推送镜像。
- `rmi <image>`：删除镜像。
- `inspect image <image>`：查看镜像详情。

常用选项：

| 命令 | 参数 | 说明 |
| --- | --- | --- |
| `images` | `-a, --all` | 显示全部镜像。 |
| `images` | `--query <text>` | 按镜像引用过滤。 |
| `pull` | `--platform <os/arch[/variant]>` | 指定拉取平台。 |
| `rmi` | `--force` | 强制删除镜像。 |
| `rmi` | `--prune-children` | 删除无 tag 的 child images。 |

## 其他命令

```bash
agent-compose daemon
agent-compose status
agent-compose version
agent-compose config
agent-compose config --quiet
```

- `daemon`：启动 agent-compose daemon。
- `status`：检查 daemon 状态。
- `version`：输出 CLI 构建版本。
- `config`：解析、校验并输出 normalized project 配置。
- `config --quiet`：只校验配置，不输出 normalized config。

## 使用建议

- 使用 `up` 将 project 应用到 daemon 后，通过 `ps`、`logs`、`stats` 观察状态。
- 跨目录操作 project 时使用 `-f /path/to/project/agent-compose.yml` 或 `-f /path/to/project/agent-compose.yaml`。
- 操作远程 daemon 时显式传入 `--host`，并确认目标 daemon 上的 project 名称和配置文件路径符合预期。
- 脚本和自动化系统使用 `--json`，避免依赖表格列宽或文本排版。
