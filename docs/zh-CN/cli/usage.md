# agent-compose CLI 操作手册

本文面向使用 `agent-compose` CLI 的用户，说明常用命令、远程访问 daemon 的推荐写法，以及容易混淆的使用点。

## 使用模型

推荐部署模型是：

- `agent-compose daemon` 运行在一台本地或远程容器宿主机上，负责创建 runtime、管理 project、运行 agent/service、保存状态。
- `agent-compose` CLI 运行在用户当前机器上，通过 `--host` 访问 daemon HTTP 地址。
- compose 文件可以在 CLI 当前机器上读取，`up` 时会把标准化后的 project spec 和 bundle 文件提交给 daemon。

本文每个命令章节先给出“不带 `--host` 的默认用法”，再给出“通过 `--host` 访问远程 daemon 的示例”。实际跨机器使用时，请优先采用带 `--host` 的形式。

## 全局参数

所有子命令都支持这些全局参数：

```bash
agent-compose [flags] [command]

-f, --file string           Path to agent-compose.yml
    --host string           Daemon HTTP endpoint
    --json                  Print machine-readable JSON
    --project-name string   Override compose project name
```

常用参数说明：

- `--host`：daemon HTTP 地址，例如 `http://127.0.0.1:7410`、`http://server.example.com:7410`。
- `--file` / `-f`：指定 compose manifest。默认会在当前目录查找 `agent-compose.yml`、`agent-compose.yaml` 或 `agent-compose.json`。
- `--project-name`：覆盖 manifest 里的 `name`。适合测试、并行运行同一份配置、避免覆盖已有 project。
- `--json`：输出机器可读 JSON。排查 run、service、image 时很有用。

常见误区：

- 在非 compose 目录执行命令时忘记加 `--file`。
- CLI 和 daemon 不在同一台机器时忘记加 `--host`。
- 临时测试时没有加 `--project-name`，导致覆盖同名 project。

## 快速开始

以 `examples/agent-compose/docker-minimal/agent-compose.yml` 为例，manifest 中只有一个 agent：`reviewer`。

默认本地写法：

```bash
cd examples/agent-compose/docker-minimal

agent-compose config
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "hi"
```

远程访问 daemon 的推荐写法：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  config

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  up

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  ps

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi"
```

最重要的规则：`run <agent> [prompt...]` 的第一个参数是 agent 名称，不是 prompt。

错误：

```bash
agent-compose run "hi"
```

正确：

```bash
agent-compose run reviewer "hi"
agent-compose run reviewer --prompt "hi"
```

## daemon

### 作用

启动 agent-compose 后端服务。daemon 负责接收 CLI/API 请求、管理 project、调度运行、创建 runtime session、代理 Jupyter 和保存状态。

### 默认用法

```bash
agent-compose daemon
```

### 远程访问说明

`daemon` 自身不使用 `--host` 访问远端。`--host` 是给 CLI 客户端连接 daemon 用的。

常见部署方式是在容器宿主机启动 daemon：

```bash
go run ./cmd/agent-compose daemon
```

或通过 compose/systemd/容器方式启动服务，然后在用户机器上执行：

```bash
agent-compose --host http://127.0.0.1:7410 status
```

常见误区：

- 把 `--host` 当成 daemon listen 地址。daemon 监听地址由运行环境配置控制，例如 `HTTP_LISTEN`。
- daemon 没启动就执行 `up`、`run`、`ps`、`invoke` 等需要服务端的命令。

## version

### 作用

打印当前 CLI 二进制版本。用于确认本地 CLI 版本。

### 默认用法

```bash
agent-compose version
```

### 远程访问示例

`version` 是本地命令，不访问 daemon，因此不需要 `--host`。

建议同时检查 CLI 和 daemon：

```bash
agent-compose version
agent-compose --host http://127.0.0.1:7410 status
```

常见误区：

- 只看 CLI 版本，不看 daemon 版本。排查异常时两者都要确认。

## status

### 作用

查询 daemon 是否可用，并查看 daemon 返回的版本和状态。

### 默认用法

```bash
agent-compose status
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 status
```

常见误区：

- daemon 在远端时直接执行 `agent-compose status`，CLI 会按默认地址连接，可能查到错误的本地服务或连接失败。

## config

### 作用

读取本地 compose manifest，做标准化并输出规范化后的配置。适合在 `up` 前确认 agent、driver、image、network 等最终值。

### 默认用法

```bash
agent-compose config
agent-compose config --quiet
```

### 远程访问示例

`config` 主要是本地 manifest 处理，不需要 daemon。为了保持脚本形态一致，也可以带 `--host`，但它不会改变本地解析行为。

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  config
```

常见误区：

- 以为 `config` 会把配置提交到 daemon。它只负责验证和打印；真正提交使用 `up`。
- 在错误目录执行导致 CLI 找不到 manifest。跨目录时加 `--file`。

## validate

### 作用

验证本地 compose manifest。可输出内置 JSON Schema，也可只做 dry run 校验。

### 默认用法

```bash
agent-compose validate
agent-compose validate --dry-run
agent-compose validate --schema
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  validate --dry-run
```

常见误区：

- 需要快速看 schema 时去翻源码。直接使用 `agent-compose validate --schema`。
- 把 `validate --dry-run` 当作部署。它不会创建或更新 daemon 里的 project。

## bundle

### 作用

验证或查看一个 compose bundle 目录。bundle 目录需要包含 `agent-compose.yml`、`agent-compose.yaml` 或 `agent-compose.json`，并可包含 service entry、schema 等相对路径文件。

### 默认用法

```bash
agent-compose bundle inspect [dir]
agent-compose bundle validate [dir]
agent-compose bundle validate [dir] --dry-run
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  bundle inspect examples/service-entry

agent-compose --host http://127.0.0.1:7410 \
  bundle validate examples/service-entry --dry-run
```

常见误区：

- 在仓库根目录执行 `agent-compose bundle inspect`，但根目录没有 compose manifest。此时应显式传入 bundle 目录。
- 把 `--file` 和 `[dir]` 混用后以为 bundle 目录来自 `--file`。bundle 子命令的目录参数才是要检查的 bundle 根目录。

## up

### 作用

把本地 compose project 应用到 daemon。它会提交标准化 project spec 和 bundle 文件，创建或更新 project、agent definition、service、scheduler 等 daemon 侧资源。

### 默认用法

```bash
agent-compose up
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  up
```

测试同一份 manifest 时推荐加项目名覆盖：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --project-name my-docker-minimal-test \
  up
```

常见误区：

- 修改了 manifest 后只执行 `config`，忘记执行 `up`，daemon 仍使用旧 project。
- 不使用 `--project-name` 做临时测试，覆盖了 manifest 中同名 project。
- 认为 `up` 会立即运行 agent。`up` 只应用 project；手动运行使用 `run` 或 `invoke`。

## down

### 作用

停止 project 相关 scheduler 和运行中的 sessions。适合停止定时任务、清理测试运行环境。

### 默认用法

```bash
agent-compose down
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  down
```

常见误区：

- 以为 `down` 会删除 project 记录。它主要停止 scheduler/session，不等价于删除历史。
- 在错误 project 上执行 `down`。建议用 `--file` 和 `--project-name` 明确目标。

## ls / list

### 作用

列出 daemon 中已应用的 projects。`list` 是 `ls` 的别名。

### 默认用法

```bash
agent-compose ls
agent-compose ls --query docker-minimal
agent-compose ls --verbose
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 ls
agent-compose --host http://127.0.0.1:7410 ls --query docker-minimal --verbose
agent-compose --host http://127.0.0.1:7410 --json ls --query docker-minimal
```

常见误区：

- 用 `ps` 查所有 project。`ps` 是当前 project 视角；列 daemon 中所有 project 用 `ls`。
- `--query` 只能过滤项目名、ID 或 source path，不是通用 SQL 查询。

## ps

### 作用

查看当前 project 的 agent、scheduler、latest run、session、driver、image 状态。

### 默认用法

```bash
agent-compose ps
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  ps

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --json ps
```

常见误区：

- 没执行 `up` 就执行 `ps`。daemon 中还没有 project 时会查不到。
- 不看 `SESSION` 列就执行 `exec`。`exec` 需要可选中的运行中 session。

## run

### 作用

手动运行当前 project 中的一个 agent，并向它传入 prompt。

### 默认用法

```bash
agent-compose run <agent> [prompt...]
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --prompt "..." --keep-running
agent-compose run <agent> --session-id <session-id> --prompt "..."
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi"
```

保留 runtime session，方便后续 `exec`：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi" --keep-running
```

输出 JSON：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --json run reviewer --prompt "hi"
```

常见误区：

- 把 prompt 当作第一个参数。`run "hi"` 会把 `hi` 当 agent 名称。
- 不知道有哪些 agent。先执行 `config`、`ps` 或 `inspect project`。
- 以为 `--keep-running` 会让 agent 一直执行。它的作用是运行结束后保留 runtime session，便于 `exec` 或复用 session。
- agent provider 需要凭据或网络访问。`run` 参数正确并不代表 provider 一定能完成任务。

## invoke

### 作用

调用当前 project 中定义的 service entry。适合结构化输入输出、自动化任务和非对话式工作流。

### 默认用法

```bash
agent-compose invoke <service> --input-json '{"key":"value"}'
agent-compose invoke <service> --input-file input.json
agent-compose invoke <service> --input-json '{"key":"value"}' --keep-running
agent-compose invoke <service> --session-id <session-id> --input-json '{"key":"value"}'
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  invoke risk-review --input-json '{"scope":"daily"}'
```

推荐排查时使用 JSON 输出：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json invoke risk-review --input-json '{"scope":"daily"}'
```

常见误区：

- 同时传 `--input-json` 和 `--input-file`。应二选一。
- 输入 JSON 与 service 的 input schema 不匹配。
- 忘记执行 `up`，daemon 中还没有 service definition。
- 以为 service 名称是 agent 名称。`invoke <service>` 需要传 `services:` 下的 key。

## logs

### 作用

查看当前 project 的 run 输出。可以按 agent、run id、session id 过滤，也可以 follow 正在运行的输出。

### 默认用法

```bash
agent-compose logs
agent-compose logs --agent reviewer
agent-compose logs --run-id <run-id>
agent-compose logs --session-id <session-id>
agent-compose logs --follow
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  logs --agent reviewer

agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  logs --run-id <run-id>
```

JSON 输出：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json logs --run-id <run-id>
```

常见误区：

- `--json` 不能和 `--follow` 一起使用。需要结构化输出用 `--json`，需要实时追踪用 `--follow`。
- 没有 run 时日志为空是正常情况。
- 使用 `--agent` 查 service run。service run 的 target 是 service，优先用 `--run-id`。

## exec

### 作用

在一个运行中的 project session 内执行命令。适合调试 guest 环境、检查 workspace、读取环境变量。

### 默认用法

```bash
agent-compose exec --agent <agent> -- <command> [args...]
agent-compose exec --session-id <session-id> -- <command> [args...]
agent-compose exec --run-id <run-id> -- <command> [args...]
agent-compose exec --agent <agent> --cwd /workspace -- pwd
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  exec --agent reviewer -- pwd
```

指定 session：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  exec --session-id <session-id> -- env
```

常见误区：

- 没有运行中的 session 就执行 `exec`。先用 `ps` 看 `SESSION` 列，或通过 `run --keep-running` 保留 session。
- 忘记 `--`。当要执行的命令或参数可能被 CLI 当成 flag 时，用 `--` 分隔更稳妥。
- `--agent` 只用于按 agent 选择运行中 session。若有多个候选 session，建议改用 `--session-id`。

## inspect

### 作用

查看 project、agent、run 或 session 的详细信息。默认也以 JSON 格式打印，适合排查状态。

### 默认用法

```bash
agent-compose inspect project
agent-compose inspect agent <agent-name>
agent-compose inspect run <run-id>
agent-compose inspect session <session-id>
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  inspect project

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  inspect agent reviewer
```

查看 run 和 session：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  inspect run <run-id>

agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  inspect session <session-id>
```

常见误区：

- `inspect agent` 忘记传 agent 名称。
- `inspect run` 使用了其它 project 的 run id。run 查询带 project 上下文，确保 `--file` / `--project-name` 指向同一个 project。
- `inspect session` 使用的是 session id，不是 run id。

## images / image ls

### 作用

列出 daemon 所在宿主机可见的镜像。`images` 等价于 `image ls`。

### 默认用法

```bash
agent-compose images
agent-compose image ls
agent-compose image ls --query agent-compose-guest
agent-compose image ls --all
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 --json image ls --query agent-compose-guest
```

常见误区：

- 以为查询的是 CLI 本机镜像。通过 `--host` 访问时，查询的是 daemon 宿主机的镜像。
- 镜像很多时不加 `--query`，输出难以阅读。

## image inspect

### 作用

查看 daemon 宿主机上的镜像详情，包括 image id、tag、平台、大小、可用状态、container count 等。

### 默认用法

```bash
agent-compose image inspect <image>
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  image inspect agent-compose-guest:latest
```

常见误区：

- 使用 CLI 本机存在但 daemon 宿主机不存在的镜像名。runtime 使用的是 daemon 宿主机镜像。

## pull / image pull

### 作用

让 daemon 宿主机拉取镜像。`pull` 是顶层快捷命令，等价于 `image pull`。

### 默认用法

```bash
agent-compose pull <image>
agent-compose image pull <image>
agent-compose pull <image> --platform linux/arm64
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  pull ghcr.io/chaitin/agent-compose-guest:latest --platform linux/arm64
```

常见误区：

- 在 CLI 本机执行 `docker pull` 后以为 daemon 能用。远程 daemon 使用 daemon 宿主机的镜像仓库和本地镜像缓存。
- 平台不匹配。ARM64 宿主机应明确关注 `linux/arm64` 镜像是否可用。

## rmi / image rm

### 作用

从 daemon 宿主机移除镜像。`rmi` 是顶层快捷命令，等价于 `image rm`。

### 默认用法

```bash
agent-compose rmi <image>
agent-compose image rm <image>
agent-compose image rm <image> --force
agent-compose image rm <image> --prune-children
```

### 远程访问示例

```bash
agent-compose --host http://127.0.0.1:7410 \
  image rm old-image:latest
```

常见误区：

- 删除仍被 session/container 使用的镜像。先用 `image inspect` 看 `container_count`，必要时先 `down` 相关 project。
- 随意使用 `--force`。它可能影响仍在使用的镜像，应只在确认后使用。

## JSON 输出建议

适合加 `--json` 的场景：

```bash
agent-compose --json ls
agent-compose --json ps
agent-compose --json run reviewer --prompt "hi"
agent-compose --json invoke risk-review --input-json '{"scope":"daily"}'
agent-compose --json logs --run-id <run-id>
agent-compose --json image ls --query agent-compose-guest
```

不适合或不支持的场景：

- `logs --json --follow`：不支持组合使用。
- 需要人类实时观察输出时，直接用默认文本输出或 `--follow`。

## 常见问题

### `run project ... agent hi: ... project agent .../hi not found`

原因：把 prompt 写在了 `<agent>` 位置。

错误：

```bash
agent-compose run "hi"
```

正确：

```bash
agent-compose run reviewer "hi"
agent-compose run reviewer --prompt "hi"
```

先确认 agent 名称：

```bash
agent-compose config
agent-compose ps
agent-compose inspect project
```

### 找不到 project 或 project 状态不是预期

检查当前命令指向的 compose 文件和项目名：

```bash
agent-compose --file /path/to/agent-compose.yml config
agent-compose --host http://127.0.0.1:7410 ls --query <project-name>
```

如果用过 `--project-name`，后续 `ps`、`run`、`logs`、`down` 也要带同一个 `--project-name`。

### `exec` 报没有 running session

先看 session：

```bash
agent-compose ps
```

如果 `SESSION` 为空，先保留 session：

```bash
agent-compose run reviewer --prompt "hi" --keep-running
```

再执行：

```bash
agent-compose exec --agent reviewer -- pwd
```

### service 调用成功但不知道输出在哪里

使用 `--json invoke`，关注这些字段：

- `run_id`
- `session_id`
- `status`
- `output_json`
- `logs_path`
- `artifacts`

示例：

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json invoke risk-review --input-json '{"scope":"daily"}'
```

### daemon 在远端但镜像不可用

通过 CLI 查询的是 daemon 宿主机：

```bash
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 image inspect agent-compose-guest:latest
```

如果不存在，在 daemon 宿主机侧拉取或用 CLI 远程拉取：

```bash
agent-compose --host http://127.0.0.1:7410 pull agent-compose-guest:latest
```

## 推荐排查顺序

1. 检查 daemon：`agent-compose --host <url> status`
2. 检查本地配置：`agent-compose --file <manifest> config`
3. 检查 daemon 中 project：`agent-compose --host <url> ls --query <name>`
4. 应用配置：`agent-compose --host <url> --file <manifest> up`
5. 看 project 状态：`agent-compose --host <url> --file <manifest> ps`
6. 运行 agent 或 service：`run` / `invoke`
7. 查看详情：`logs` / `inspect run` / `inspect session`
8. 清理：`down`

## 命令速查

```bash
# daemon 状态
agent-compose --host http://127.0.0.1:7410 status
agent-compose version

# 本地配置
agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml validate --dry-run
agent-compose validate --schema

# bundle
agent-compose bundle inspect examples/service-entry
agent-compose bundle validate examples/service-entry --dry-run

# project
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml up
agent-compose --host http://127.0.0.1:7410 ls --query docker-minimal --verbose
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml ps
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml down

# agent
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml run reviewer --prompt "hi"
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml run reviewer --prompt "hi" --keep-running

# service
agent-compose --host http://127.0.0.1:7410 --file examples/service-entry/agent-compose.yml invoke risk-review --input-json '{"scope":"daily"}'
agent-compose --host http://127.0.0.1:7410 --file examples/service-entry/agent-compose.yml --json invoke risk-review --input-json '{"scope":"daily"}'

# logs / inspect
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml logs --agent reviewer
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml inspect project
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml inspect agent reviewer

# image
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 image inspect agent-compose-guest:latest
agent-compose --host http://127.0.0.1:7410 pull agent-compose-guest:latest
```
