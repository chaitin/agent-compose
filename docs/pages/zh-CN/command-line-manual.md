# agent-compose 命令行使用手册

agent-compose 命令行用于连接 agent-compose daemon，管理 project、agent、sandbox、日志和镜像。它的使用模型接近 Docker Compose：配置文件定义 project，daemon 负责长期状态和运行时生命周期，CLI 负责发起操作和展示结果。

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
| `--project-name <name>` | 按名称选择 daemon 中已部署的 project；它不会修改 compose 文件声明的项目名称。 |
| `--json` | 使用 JSON 输出，供脚本、AI 和自动化系统解析。 |

示例：

```bash
agent-compose -f /path/to/project/agent-compose.yml up
agent-compose -f /path/to/project/agent-compose.yaml ps --all
agent-compose --host http://10.0.0.12:7410 ls --json
```

使用规则：

- 未指定 `-f` 和 `--project-name` 时，project 范围的命令在当前目录查找 `agent-compose.yml` 或 `agent-compose.yaml`。
- 指定 `--project-name` 时，已部署项目命令直接选择 daemon project，不读取 compose 文件；即使同时指定 `-f` 也是如此。
- `config`、`up` 等本地编排命令始终使用 compose 文件声明的项目名称（或根据其目录推导），`--project-name` 不会覆盖该名称。
- 使用 `-f` 时，不需要切换到 project root。
- `--host` 只决定 CLI 连接哪个 daemon；sandbox 实际运行在 daemon 所在环境中。
- daemon 不再消费浏览器登录用的 `AUTH_*` / `OAUTH_*` 配置；UI 浏览器认证由 agent-compose-ui server 处理。
- 自动化场景应使用 `--json`，不要解析人类可读表格。

### Daemon 认证

在 daemon 环境中设置 `AGENT_COMPOSE_AUTH_TOKEN` 后，HTTP(S) 控制面请求必须
携带共享 Bearer Token；配置为空时认证默认关闭。受信任的本地 Unix Socket
连接不走此认证路径。

为 daemon 站点验证并保存 Token：

```bash
agent-compose --host https://compose.example.com auth login --token '<token>'
agent-compose --host https://compose.example.com status
```

第一条命令会先向 daemon 验证 Token，成功后写入
`~/.config/agent-compose/config.yml`（或当前平台的用户配置目录）。后续命令会
根据规范化后的 `--host` 或 `AGENT_COMPOSE_HOST` 自动加载对应 Token。配置文件
仅允许当前用户读写。可通过 `agent-compose auth ls` 查看已保存的站点，通过
`agent-compose --host <site> auth logout` 删除站点凭据。

当前仍支持 HTTP，包括宿主机访问容器回环映射的场景；但明文 HTTP 中的 Bearer
Token 可能被监听并重放。CLI 与 daemon 位于不同机器时，应使用 HTTPS、SSH
隧道、VPN 或其他受保护网络。

Health RPC、runtime LLM facade、Jupyter proxy 和 webhook ingestion 继续使用各自
已有的认证或信任边界，不消费 daemon Token。

该 Token 保护的是 daemon 控制面，并非只识别 CLI 程序。任何调用相同控制面 API
的 UI server 或反向代理，也必须先配置注入 `Authorization: Bearer <token>`，再
开启 daemon 认证。

### Project 环境文件

配置可以显式指定一个或多个 dotenv 文件；相对路径以 project 配置文件所在目录为基准：

```yaml
env_file:
  - .env
  - .env.local
```

未声明 `env_file` 时，CLI 优先自动加载 project 目录下的 `.env`；该文件不存在时，再尝试当前工作目录的 `.env`。显式声明 `env_file` 后不再自动加载这两个文件。

变量冲突时，后声明的 env 文件覆盖先声明的文件，启动 CLI 时已有的进程环境覆盖所有 env 文件。Project 环境文件只参与 `agent-compose.yml` 渲染，不会改变 `--host`、认证等 CLI 连接配置。

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
agent-compose --host http://10.0.0.12:7410 project ls
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml up
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml logs --follow
```

## `project ls`：查看 project

查看当前 daemon 管理的所有 project。

```bash
agent-compose project ls
agent-compose project ls --limit 20 --offset 40
agent-compose project ls --verbose
agent-compose project ls --json
```

默认输出字段：

- `PROJECT`：project name。
- `CONFIG FILE`：配置文件路径。
- `REVISION`：当前 project revision。每次应用新的 spec 变更都会递增；连续重复
  apply 当前 spec 时保持同一个 revision。
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

## `agent ls`：查看当前 project 的 agents

查看当前已应用 project 下的所有 agents。顶级 `ls` 是 `agent ls` 的别名。

```bash
agent-compose agent ls
agent-compose ls
agent-compose agent ls --json
```

## `project up`：启动或更新 project

读取配置文件，将 project 应用到 daemon，并启动或更新 project 中的 scheduler 和服务。

```bash
agent-compose up
agent-compose project up
agent-compose -f /path/to/project/agent-compose.yml up
```

当前 `up` 的行为是将 project 应用到 daemon 后返回，project 后续由 daemon 管理。它不会 attach project 日志，也不提供 `-d/--detach` 参数。

顶级 `up` 是 `project up` 的别名。

## `project down`：关闭 project

关闭当前 project，停止 scheduler、服务和运行中的 sandbox。

```bash
agent-compose down
agent-compose project down
agent-compose -f /path/to/project/agent-compose.yml down
```

顶级 `down` 是 `project down` 的别名。

注意事项：

- `down` 只影响当前 project。
- 使用 `-f` 或 `--project-name` 时，应确认定位到的是预期 project。
- 如果部分 sandbox 停止失败，命令返回非零退出码，并在输出中说明失败项。

## `run`：运行 sandbox

为指定 agent 启动一个 sandbox，或在已有 sandbox 中继续运行。

```bash
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --command "..."
agent-compose run <agent> --sandbox <sandbox> --prompt "..."
```

输入模式：

| 模式 | 用法 | 说明 |
| --- | --- | --- |
| prompt | `run <agent> --prompt "..."` | 向 agent provider 发送 prompt。 |
| command | `run <agent> --command "..."` | 启动或复用该 agent 的 sandbox 后通过 guest `agent-compose-runtime exec` 执行 shell 命令；命令 transcript 会实时输出，并写入该次 run 记录。 |
| prompt REPL | `run <agent> -i --prompt` | 从 stdin 逐行读取 prompt；每条非空输入创建一次 run，并复用同一个 sandbox。 |
| command REPL | `run <agent> -i --command` | 从 stdin 逐行读取 command；每条非空输入创建一次 run，并复用同一个 sandbox。 |
| sandbox 复用 | `run <agent> --sandbox <sandbox> --prompt "..."` | 在指定 sandbox 中继续运行。 |

prompt 输入必须使用 `--prompt`；非交互 run 必须选择 `--prompt` 或 `--command`。不再支持 positional prompt 参数。
不支持额外的位置参数。

选项：

| 参数 | 说明 |
| --- | --- |
| `--keep-running` | 运行结束后保留 sandbox runtime。 |
| `--sandbox <sandbox>` | 指定已有 sandbox。 |
| `--rm` | 运行结束后删除 sandbox。 |
| `--jupyter` | 为本次 run 启用 Jupyter；未设置时使用 agent YAML 默认，YAML 未设置时默认关闭。 |
| `--jupyter-expose` | 标记本次 run 的 Jupyter agent-compose proxy 入口为显式暴露意图；该参数不请求 runtime driver 暴露 host port，并会同时启用 Jupyter。 |
| `-d, --detach` | 将 run 提交给 daemon 后立即返回；输出 run id、初始状态和 `logs --follow` 查看命令。 |
| `-i, --interactive` | 进入 prompt 或 command REPL；必须与 `--prompt` 或 `--command` 组合。 |

示例：

```bash
agent-compose run reviewer --prompt "Review the staged changes"
agent-compose run builder --command "task build"
agent-compose run tester --command "task test" --keep-running
agent-compose run tester --command "task test" -d
agent-compose run reviewer -i --prompt
agent-compose run tester -i --command
agent-compose run reviewer --sandbox sandbox_123 --prompt "Continue the review"
agent-compose run reviewer --jupyter --jupyter-expose --prompt "Inspect the notebook state"
```

互斥规则：

- prompt、command 一次只能选择一种。
- 使用 `--prompt` 或 `--command` 时，不能再传额外位置参数。
- `run -d/--detach` 和 `run -i/--interactive` 互斥。
- `run -i/--interactive` 必须选择 `--prompt` 或 `--command`，不能与 `--json` 组合。
- REPL 中空行不会创建 run；输入 `/exit` 或 Ctrl+D 退出。
- REPL 不是 TTY/PTY 或运行中 stdin 透传；每条输入都是一次独立 `RunAgentStream`，但复用同一个 sandbox。
- detached run 可通过输出的 `agent-compose logs --run <run-id> --follow` 命令观察输出，也可继续使用 `stop`/`logs` 操作该 run。
- `run -i --prompt` 仅支持可复用 provider conversation 的 Codex、Claude/cc 和 OpenCode；Gemini 当前会返回 unsupported。
- `StopRun` 会请求 daemon 内当前活动 run 取消；daemon 重启后遗留的 running/pending run 会在启动 reconcile 中标记为 failed，并带 `daemon interrupted` 错误。

## `scheduler`：调用、查看和操作 project scheduler

```bash
agent-compose scheduler ls [agent]
agent-compose scheduler invoke <scheduler-ref> [--payload <json>]
agent-compose scheduler trigger <scheduler-ref> <trigger-ref> [--payload <json>] [--detach]
agent-compose scheduler runs [scheduler-ref] [--trigger <trigger-ref>] [--status <status>] [--limit <n>]
agent-compose scheduler logs [run-ref] [--run <run-ref>] [--scheduler <scheduler-ref>] [--trigger <trigger-ref>] [--tail <n>]
agent-compose scheduler prune [--scheduler <scheduler-ref>] [--trigger <trigger-ref>] [--status <terminal-statuses>] [--older-than <duration>] [--force]
agent-compose scheduler inspect <scheduler-or-trigger-or-run-ref> [--scheduler <scheduler-ref>]
```

- `scheduler ls` 同时列出声明式 scheduler 配置的 trigger 和 scheduler script 注册到系统中的 trigger。
- `scheduler invoke` 在前台调用显式脚本型 scheduler 的默认执行入口；它不创建 trigger run 历史、持久化外层日志或 artifacts。原有 `scheduler run` 命令已直接移除。
- `scheduler trigger` 手动执行指定 trigger；使用 `--detach` 时会返回一个可继续 inspect 或 stop 的持久化 trigger run。
- `scheduler trigger --payload '{"key":"value"}'` 将 JSON payload 传给 scheduler trigger handler。
- `scheduler runs` 只列出外层 trigger run；`scheduler.agent()` 创建的内层 agent run 仍由普通 run 命令管理。默认返回全部匹配记录，只有显式设置 `--limit` 才限制最终数量；status 可选 `running`、`succeeded`、`failed`、`canceled`、`skipped`。
- `scheduler logs` 默认输出当前所有 scheduler 的全部 trigger run 外层结构化事件。`--tail N` 选择全局最新 N 条匹配事件并按从旧到新输出；`--tail -1` 表示全部，`--tail 0` 表示不输出。这里不包含 Invocation 日志或内层 agent transcript。
- `scheduler runs/logs --trigger` 会优先按当前定义解析名称和短 ID。Trigger 被删除或改名后，只要仍有持久化的 trigger run 历史，就可以继续用其精确 ID 查询；同一历史 ID 属于多个 Scheduler 时，`runs` 需要增加 Scheduler 位置参数，`logs` 需要增加 `--scheduler`。
- `scheduler prune` 清理外层 trigger run 历史及其直属 loader event、event delivery/link 和规范 run artifacts。默认匹配当前 project 中全部 `succeeded`、`failed`、`canceled`、`skipped` 终态 trigger run；可用 `--scheduler`、`--trigger`、`--status`、`--older-than` 缩小范围。命令默认只 dry-run，只有 `--force` 才真正删除。running run、Invocation、内层 agent run、topic event、sandbox、loader state 和 sticky binding 都会保留。历史 Trigger ID 与 `runs`/`logs` 使用相同的“当前定义优先”解析规则。
- daemon 启动时会先把上一次进程中断后遗留的外层 `running` trigger run 收敛为 `failed`，并记录 daemon-interrupted loader event；之后它才能按普通终态历史参与 prune。
- `scheduler inspect` 只接受一个 scheduler 名称/ID、trigger 名称/ID 或外层 trigger run ID。多个 scheduler 存在相同 trigger reference 时，使用 `--scheduler <scheduler-ref>` 消歧；旧双位置参数形式不再支持。
- 当前 `scheduler runs` 和 `scheduler logs` 会收齐 unary cursor pages 后一次性渲染；streaming/follow 能力留到单独修改中实现。

## `ps`：查看 sandbox

查看当前 project 下的 sandbox。默认只显示运行中的 sandbox。使用 `--all` 时仍限定当前 project，但会包含所有状态。
该 project 必须已经存在于 daemon 中；执行 `agent-compose down` 后，需要先重新执行 `agent-compose up`，再使用 `ps`。
使用 `--project-name <name>` 时，会直接选择 daemon 中已有的 project；即使同时指定 `--file`，已部署项目选择也不会读取 compose 文件。本地编排命令仍要求 compose 文件，且绝不会用 `--project-name` 修改其中的项目名称。

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
| `-a, --all` | 显示当前 project 中所有状态的 sandbox。 |
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

## `sandbox`：管理 sandbox

使用 `sandbox` 命令组可以在同一个命名空间下管理当前 project 的 sandbox。兼容入口 `ps`、`stop`、`resume` 和 `rm` 仍然可用。

```bash
agent-compose sandbox ls
agent-compose sandbox ls --all --json
agent-compose sandbox stop <sandbox>
agent-compose sandbox resume <sandbox>
agent-compose sandbox rm <sandbox>
agent-compose sandbox rm --force <sandbox>
agent-compose sandbox prune
agent-compose sandbox prune --older-than 7d
agent-compose sandbox prune --status error --json
agent-compose sandbox prune --agent worker --driver microsandbox --force
agent-compose sandbox prune --include-orphans
```

子命令：

| 命令 | 说明 |
| --- | --- |
| `sandbox ls` | 等价于 `ps`；支持 `--all/-a`、`--status`、`--verbose` 和 `--json`。 |
| `sandbox stop <sandbox...>` | 等价于 `stop`；停止一个或多个 sandbox。 |
| `sandbox resume <sandbox...>` | 等价于 `resume`；恢复一个或多个 stopped sandbox。 |
| `sandbox rm <sandbox...>` | 等价于 `rm`；删除一个或多个 sandbox。仅在确认要删除 running sandbox 时使用 `--force`。 |
| `sandbox prune` | 对当前 project 中 stopped 或 failed sandbox 做 dry-run 清理预览；使用 `--force` 才会删除匹配项。 |

`sandbox prune` 参数：

| 参数 | 说明 |
| --- | --- |
| `--status <status>[,<status>...]` | 覆盖默认的 `stopped,failed` 状态过滤。`running` 和 `pending` 会被拒绝；running sandbox 请使用 `sandbox rm --force <sandbox>`。 |
| `--agent <agent>` | 只匹配指定 agent name 的 sandbox。 |
| `--driver <docker|boxlite|microsandbox>` | 只匹配指定 runtime driver 的 sandbox。 |
| `--older-than <duration>` | 匹配 `updated_at` 早于指定时长的 sandbox；缺少 `updated_at` 时回退使用 `created_at`。时长示例：`7d`、`168h`。 |
| `--include-orphans` | 同时扫描 daemon 全局、在任何 project 中都已无 sandbox 记录的受管 runtime 残留。 |
| `--force` | 实际删除匹配的 sandbox。不带该参数时，`sandbox prune` 只做 dry-run。 |

行为规则：

- 不带 `--include-orphans` 时，`sandbox prune` 只处理当前 compose project 的 stopped/failed sandbox 记录，且不会扫描 driver 残留。
- 带 `--include-orphans` 时，`--driver`、`--older-than` 同时过滤记录和残留；`--status`、`--agent` 只过滤记录。任何仍关联已知 sandbox 记录的 runtime 资源都不会成为 orphan。
- ownership 不完整、manifest 损坏、路径越界、仍 active 或 schema 未知的残留会显示为不可删除；即使 `--force` 也只会 skipped。
- `sandbox prune` 调用 daemon 的 `SandboxService.PruneSandboxes` use case，删除 sandbox 自有 runtime/data，不删除共享 cache artifact；cache inventory 仍由 `cache prune` 或 `cache rm` 管理。
- forced prune 中某个 sandbox 删除失败时，命令会继续处理后续匹配项，输出 skipped 项，并以非零退出码结束。

`sandbox stop` 会保留可恢复的 driver state。`sandbox rm` 在 `<SANDBOX_ROOT>/.lifecycle` 写入持久 deletion journal；running sandbox 必须显式使用 `--force`，删除会按可恢复阶段清理 driver resource、sandbox accessories、sandbox 目录和 metadata。处于 `DELETING` 的 sandbox 不能 resume，也不能接收新的 exec/run；daemon 启动时只继续未完成的 deletion journal，不会猜测或自动删除普通历史残留。

## `stats`：查看 sandbox 资源统计

查看运行中 sandbox 的资源统计快照。未指定 sandbox 参数时，显示当前 compose project 下所有 running sandbox 的统计。
project 级 stats 要求该 project 已存在于 daemon 中；执行 `agent-compose down` 后，需要先重新执行 `agent-compose up`，再使用不带 sandbox 参数的 `stats`。

```bash
agent-compose stats
agent-compose stats --json
agent-compose stats <sandbox>
agent-compose stats <sandbox> --json
```

输出字段包括 CPU 百分比、memory usage/limit/percent、network rx/tx、block read/write、uptime、driver 和 sampled_at。不同 runtime driver 无法提供的字段会在文本表格中显示 `-`，在 JSON 中保留稳定 key，并以 `value: null` 和 `status: unknown` 或 `status: unavailable` 表达。

driver 没有稳定 stats 能力入口时，命令会返回 unsupported，而不是普通 execution failed。

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
agent-compose exec <sandbox> -- <command> [args...]
agent-compose exec <sandbox> --command "..."
agent-compose exec <sandbox> --prompt "..."
```

选项：

| 参数 | 说明 |
| --- | --- |
| `--command "..."` | 以 flag 形式传入 shell 命令，等价于在 sandbox 中执行 `bash -lc "..."`。 |
| `--prompt "..."` | 在已有 sandbox 中执行一次 agent prompt，输出回复后退出；增加 `-i`（以及可选的 `-t`）进入多轮 attach 会话。 |
| `--cwd <path>` | 指定 sandbox 内工作目录。 |
| `--agent <agent>` | 兼容旧目标选择参数，会输出 deprecated warning；新命令应使用 `exec <sandbox>`。 |
| `--run <run-id>` | 兼容旧目标选择参数，会输出 deprecated warning；新命令应使用 `exec <sandbox>`。 |

示例：

```bash
agent-compose exec sandbox_123 -- pwd
agent-compose exec sandbox_123 -- bash -lc "task test"
agent-compose exec sandbox_123 --command "git status --short"
agent-compose exec sandbox_123 --prompt "总结当前 workspace"
agent-compose exec sandbox_123 --cwd /workspace --command "pwd"
```

`exec` 与 `run --command` 使用同一套 guest `agent-compose-runtime exec` command output 路径。文本模式把 command stdout 输出到本机 stdout，把 command stderr 输出到本机 stderr，并经过 host 侧 marker filtering；不会额外 echo host wrapper command。`--json` 不输出流式 output，只输出最终 result。`exec` 不创建 `ProjectRun`；需要 run 审计、`logs` 或 run artifact 时使用 `run --command`。

## `logs`：查看日志

查看当前 project 下 agent、sandbox 或 run 的日志。默认展示 project 下所有 agent 日志。

当前 `logs` 基于 agent-compose v2 RunService 返回的 run log artifact 展示。`--follow` 由服务端按 `logs_path` 指向的日志文件增量读取；普通查看会使用 run 记录中的输出和 artifact 汇总。它不会默认读取 Codex、Claude、Gemini 等 provider 的私有日志文件。

```bash
agent-compose logs
agent-compose logs <agent>
agent-compose logs <project|agent|run|sandbox-id>
agent-compose logs --agent reviewer
agent-compose logs --run <run-id>
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
| `--run <run-id>` | 按 run 过滤。 |
| `--sandbox <sandbox>` | 按 sandbox 过滤。 |

示例：

```bash
agent-compose logs
agent-compose logs reviewer
agent-compose logs --agent reviewer --tail 200
agent-compose logs --sandbox sandbox_123 --follow -t
agent-compose logs --run run_123 --json
```

## `inspect`：查看资源详情

查看 project 下资源、daemon image 或 runtime cache item 的详细信息。
`inspect project` 和 `inspect agent <agent>` 要求该 project 已存在于 daemon 中；执行 `agent-compose down` 后，需要先重新执行 `agent-compose up`，再使用它们。

```bash
agent-compose inspect project
agent-compose inspect <project|agent|run|sandbox|image|cache-id>
agent-compose inspect agent <agent>
agent-compose inspect run <run-id>
agent-compose inspect sandbox <sandbox>
agent-compose inspect image <image>
agent-compose inspect cache <cache-id>
```

当唯一参数是完整 ID 或十六进制短 ID 时，`inspect` 会通过 daemon 自动识别资源类型。名称仍需使用显式类型形式；短 ID 命中多个资源时，命令会报告歧义及候选资源类型。

说明：

- `inspect project` 查看 project spec、revision、agent、scheduler 等信息。
- `inspect agent <agent>` 查看 agent 配置和运行摘要。
- `inspect run <run-id>` 查看一次 run 的详情。
- `inspect sandbox <sandbox>` 查看 sandbox/runtime 详情。
- `inspect image <image>` 查看镜像详情。
- `inspect cache <cache-id>` 查看一个 daemon runtime cache item，包括引用、阻止删除原因和 warnings。

## 镜像命令

管理 daemon 或当前 project 相关的镜像。

```bash
agent-compose image ls
agent-compose image pull
agent-compose image pull <image>
agent-compose image build [agent...]
agent-compose image rm <image>
agent-compose image inspect <image>
```

命令说明：

- `image ls`：列出镜像。
- `image pull`：拉取当前 project 中所有 agent 引用的镜像。
- `image pull <image>`：拉取指定镜像；如果本地 OCI image backend/store 已存在该镜像，会直接成功并输出 skipped/already exists warning，不会再次 pull。
- `image build [agent...]`：构建所有 project agent 配置的镜像；提供 agent 名称时只构建指定 agent 的镜像。
- `image rm <image>`：删除镜像 metadata/store entry。OCI 模式下只删除逻辑 metadata ref；无引用的物理 manifest/blob 由 CacheService 显式回收。该命令不删除 materialized 或 runtime-derived cache。
- `image inspect <image>`：查看镜像详情。

以下顶层命令是对应 `image` 子命令的快捷别名：

| 顶层快捷别名 | Image 命令 |
| --- | --- |
| `images` | `image ls` |
| `pull [image]` | `image pull [image]` |
| `build [agent...]` | `image build [agent...]` |
| `rmi <image>` | `image rm <image>` |
| `inspect image <image>` | `image inspect <image>` |

常用选项：

| 命令 | 参数 | 说明 |
| --- | --- | --- |
| `image ls` | `-a, --all` | 显示全部镜像。 |
| `image ls` | `--query <text>` | 按镜像引用过滤。 |
| `image pull` | `--platform <os/arch[/variant]>` | 指定拉取平台。 |
| `image build` | `-t, --tag <name[:tag]>` | 添加输出镜像 tag。 |
| `image build` | `--dockerfile <path>` | 覆盖配置中的 Dockerfile。 |
| `image build` | `--target <stage>` | 指定 Dockerfile target stage。 |
| `image build` | `--build-arg <key=value>` | 设置构建变量，可重复提供。 |
| `image build` | `--platform <os/arch[/variant]>` | 指定构建平台。 |
| `image build` | `--no-cache` | 禁用构建缓存。 |
| `image build` | `--pull` | 始终尝试拉取更新的基础镜像。 |
| `image rm` | `--force` | 强制删除镜像。 |
| `image rm` | `--prune-children` | 请求 image backend 清理 child images。OCI cache 当前会返回 warning，不删除 blobs，也不删除 runtime/materialized cache。 |

## Cache 命令

查看并显式清理 daemon runtime cache inventory。只有 daemon 会扫描 cache path 并执行删除；CLI 只发送过滤条件并展示结果。

```bash
agent-compose cache ls
agent-compose cache inspect <cache-id>
agent-compose cache prune
agent-compose cache rm <cache-id>
agent-compose inspect cache <cache-id>
```

Cache domain 在 CLI 中用 `--type` 表示：

- `oci`：daemon OCI image store 中的物理 manifest、blob 和中断项。
- `materialized`：从镜像派生出的 runtime 输入，例如 BoxLite OCI layout 或不可变的 Microsandbox qcow2 母盘。
- `runtime`：driver home 下可跨 sandbox 复用的 runtime-derived image。
- `skill`：content-addressed skill artifact 及中断的临时/lock 项。

保护状态：

- `active`：正在被 running/resuming runtime 使用，永不删除。
- `referenced`：存在 `REQUIRED` 引用，例如 OCI metadata 或 running/stopped sandbox dependency。即使带 `--force` 也不可删除。`ADVISORY` 引用只用于展示，不阻止删除。
- `unused`、`expired`、`orphaned`：设置 `--force` 后可删除。
- `unknown`：引用或安全检查不完整，永不删除。

常用选项：

| 命令 | 参数 | 说明 |
| --- | --- | --- |
| `cache ls`, `cache prune` | `--driver <docker|boxlite|microsandbox|all>` | 按 runtime driver 过滤。 |
| `cache ls`, `cache prune` | `--type <oci|materialized|runtime|skill>` | 按 cache type 过滤。 |
| `cache ls`, `cache prune` | `--status <active|referenced|unused|expired|orphaned|unknown>` | 按保护状态过滤。 |
| `cache prune` | `--unused`, `--orphaned`, `--expired` | status 快捷参数；彼此互斥，也不能与 `--status` 同用。 |
| `cache prune` | `--older-than <duration>` | 匹配超过指定时长的 cache，例如 `7d` 或 `168h`。 |
| `cache prune`, `cache rm` | `--force` | 实际删除符合条件的 item。没有 `--force` 时两个命令都是 dry-run。 |

示例：

```bash
agent-compose cache ls --type materialized
agent-compose cache inspect <cache-id>
agent-compose cache prune --driver boxlite --unused
agent-compose cache prune --type skill --orphaned --force
agent-compose cache prune --expired --force
agent-compose cache prune --older-than 7d --force
agent-compose cache rm <cache-id> --force
```

`CACHE_TTL` 默认 `168h`，设为 `0` 会禁用 expired 判定；TTL 不会触发后台或启动时删除，必须显式执行 `cache prune --expired --force`。`--older-than` 仍是独立过滤条件。`cache prune` 和 `cache rm` 默认 dry-run；`--force` 只授权执行，不能绕过 `active`、`referenced` 或 `unknown` 保护。BoxLite v0.9.7 ABI 不提供安全 image remove/prune，因此 runtime image inventory 只读；Microsandbox 共享 image 使用 SDK inventory/remove API。`sandbox prune` 不删除 cache artifact。

Microsandbox 根文件系统使用 `DATA_ROOT/image-cache` 下的不可变 qcow2 母盘，并为每个 sandbox 在 `MICROSANDBOX_HOME/rootfs-disks` 下创建私有 qcow2 子盘。只要任何 rootfs sidecar 仍指向母盘，该母盘就会被标记为 referenced 且不可删除。stop/resume 保留私有子盘；sandbox remove/prune 删除子盘及其 ownership sidecar。母盘与子盘记录的是 daemon 挂载命名空间中的路径，因此备份和迁移必须同时移动两棵目录树，并保持 daemon 可见路径不变。一个 `DATA_ROOT` 只能由一个 daemon 实例独占，不能并发共享。只有当 sidecar 指向该 image cache 内合法的母盘路径时，母盘才会被计入引用；sidecar 无法读取或指向其他位置时会输出 warning，并把所有母盘标记为 `unknown` 直到它被修复或删除——因为此时已无法确认它保护的是哪一块母盘。

Microsandbox 解析 guest 镜像时优先使用可达的 Docker daemon，daemon 不可用时改用 agent-compose 自己的 image cache，顺序与 BoxLite driver 一致；Microsandbox 自身始终不访问任何 registry。两条路径的认证方式不同：Docker daemon 使用它自己的凭据，image cache 使用 daemon 进程的 keychain 以及 `IMAGE_REGISTRY`、`IMAGE_INSECURE_REGISTRIES`，因此没有 Docker daemon 的部署需要配置 image cache 一侧。发生回退时会输出 warning，且来源会写入母盘的 cache identity，`cache ls` 可以看出每块母盘由哪条路径产出。pull policy 失败不触发回退，`pull_policy=never` 不会被另一条路径绕过。两条路径使用不同的解包实现，因此各自持有独立母盘；同一镜像若两条路径都解析过会构建两份。

首次升级到 disk-image rootfs 时需要一次性切换：先排空 Microsandbox workload，删除现有 Microsandbox runtime sandbox，再仅删除各镜像 cache 中旧的 `rootfs/` 目录和 `.rootfs.ready` 标志。不要删除整个镜像目录，因为 BoxLite 的 `oci/` cache 和新的 Microsandbox 母盘共用该目录；`/data` 下的 workspace 与 agent state 必须保留。daemon 镜像会提供 `qemu-img` 和支持 `-d` 的 `mkfs.ext4`；原生部署需要安装这两个工具。该方案不要求支持 reflink 的文件系统、loop device 或特权 mount。

daemon 可以选择启用基于时间的保留清理。`WORKSPACE_CLEANUP_TTL` 只回收符合条件的 stopped sandbox 的 workspace 目录，metadata、logs 和 state 会保留用于审计；workspace 被回收后 sandbox 不能再 resume。`IMAGE_CACHE_CLEANUP_TTL` 清理 `IMAGE_CACHE_ROOT` 自有且未被引用的 OCI 与 materialized 数据，优先使用最后使用时间，没有时回退到拉取时间或文件修改时间。两项默认都是 `0`，即关闭对应 cleaner；`CLEANUP_INTERVAL` 默认 `1h`。自动清理不会处理 workspace source、Docker daemon 镜像、BoxLite home 或 Microsandbox SDK cache，也不实现磁盘空间水位策略。

兼容说明：

- `agent-compose image ls` 已废弃，请使用 `agent-compose images`。
- `agent-compose image pull <image>` 已废弃，请使用 `agent-compose pull <image>`。
- `agent-compose image rm <image>` 已废弃，请使用 `agent-compose rmi <image>`。
- `agent-compose image inspect <image>` 已废弃，请使用 `agent-compose inspect image <image>`。
- 旧 `image` 命令树仍可用，但会在 stderr 输出 deprecated warning，后续版本会评估删除。

## `status`：检查 daemon 状态

检查当前选择的 daemon 状态和版本。

```bash
agent-compose status
agent-compose --host http://127.0.0.1:7410 status
agent-compose status --json
```

默认输出字段：

- `STATUS`：daemon 响应状态。
- `UPTIME`：daemon 返回的时间戳；如果 daemon 返回了时区信息，则按 daemon 时区展示。
- `VERSION`：daemon 构建版本。

自动化场景使用 `--json` 输出 daemon 原始 status 响应。

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

## 暂缓命令

以下命令或能力尚未作为稳定 CLI 发布：

- `push`：image push 暂缓。
- `up -d/--detach`：当前 `up` 本身就是 apply project 后返回，不提供 detach 参数。
- `up` 前台 attach 和 Ctrl+C 停止整个 project：暂缓。

## 使用建议

- 使用 `up` 将 project 应用到 daemon 后，通过 `ps`、`logs` 观察状态。
- 跨目录操作 project 时使用 `-f /path/to/project/agent-compose.yml` 或 `-f /path/to/project/agent-compose.yaml`。
- 操作远程 daemon 时显式传入 `--host`，并确认目标 daemon 上的 project 名称和配置文件路径符合预期。
- 脚本和自动化系统使用 `--json`，避免依赖表格列宽或文本排版。
