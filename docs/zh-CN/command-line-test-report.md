# agent-compose 命令行测试报告

本文记录 `feature/cli-optimization` 分支上 agent-compose CLI 的人工/自动化组合测试结果。测试目标是覆盖当前已经实现的命令行体系、关键参数、兼容入口和认证行为，并记录延期命令与测试中发现的问题。

> 状态说明：本文是历史复测报告，不作为当前 CLI 能力权威说明。当前命令语义以 [命令行使用手册](command-line-manual.md) 和 [CLI 当前设计](design/agent-compose-cli-improvement-plan.md) 为准。历史报告中当时尚未覆盖的 `stats` 已在后续 CLI runtime capabilities 工作中实现；`build` 和 `push` 仍未作为稳定 CLI 发布。

## 1. 测试环境与启动方式

测试分支：

```bash
git checkout feature/cli-optimization
```

构建 daemon 镜像：

```bash
rtk task image:agent-compose
```

启动 daemon 服务：

```bash
export AUTH_USERNAME=admin
export AUTH_PASSWORD=change-me
AUTH_USERNAME="$AUTH_USERNAME" AUTH_PASSWORD="$AUTH_PASSWORD" rtk docker compose up -d agent-compose
```

构建本地 CLI 二进制：

```bash
rtk task build
```

验证 daemon：

```bash
AUTH_USERNAME=admin \
AUTH_PASSWORD=change-me \
./build/agent-compose --host http://127.0.0.1:7410 status
```

本轮测试使用的主配置文件：

```bash
./examples/agent-compose/docker-minimal/agent-compose.yml
```

测试版本：

```text
v2606.5.0-232-gee40224
```

测试输出保存在以下临时目录，便于复查 stdout、stderr 和退出码：

```text
/tmp/agent-compose-cli-retest-20260703210649
/tmp/agent-compose-cli-lifecycle-20260703210918
/tmp/agent-compose-cli-image-compat-20260703211025
/tmp/agent-compose-cli-extra-20260703211050
/tmp/agent-compose-cli-pull-project-20260703211138
/tmp/agent-compose-cli-follow-20260703211240
```

## 2. 测试范围

本轮覆盖的顶层命令：

```text
config
daemon
down
exec
image
images
inspect
logs
ls
ps
pull
resume
rm
rmi
run
status
stop
up
version
```

本轮历史报告未作为功能实现验收的命令：

```text
stats
build
push
```

其中 `stats` 是当时尚未发布的能力，当前已经实现为 sandbox 资源统计命令；`build` 和 `push` 仍按当前设计暂缓。

## 3. 服务与认证测试

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 查看版本 | `./build/agent-compose version` | 无 | 输出本地二进制版本 | 通过 |
| daemon 帮助 | `./build/agent-compose daemon --help` | `--help` | 输出 daemon 命令帮助 | 通过 |
| 认证成功 | `AUTH_USERNAME=admin AUTH_PASSWORD=... ./build/agent-compose --host http://127.0.0.1:7410 status` | `--host`、认证环境变量 | 返回 daemon status/version | 通过 |
| 未提供认证 | `env -u AUTH_USERNAME -u AUTH_PASSWORD ./build/agent-compose --host http://127.0.0.1:7410 status` | `--host` | 返回未认证错误，退出码非 0 | 通过 |
| 错误认证 | `AUTH_USERNAME=bad AUTH_PASSWORD=bad ./build/agent-compose --host http://127.0.0.1:7410 status` | `--host`、错误认证环境变量 | 返回未认证错误，退出码非 0 | 通过 |

结论：远程 HTTP daemon 访问会统一读取 `AUTH_USERNAME` 和 `AUTH_PASSWORD`，正确认证时可访问，缺失或错误认证时会拒绝访问。

## 4. 配置与 Project 命令测试

以下命令均使用公共前缀：

```bash
AUTH_USERNAME=admin AUTH_PASSWORD=... \
./build/agent-compose \
  --host http://127.0.0.1:7410 \
  -f ./examples/agent-compose/docker-minimal/agent-compose.yml \
  --project-name <test-project>
```

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 输出 normalized config | `<prefix> --json config` | `--json` | stdout 为 JSON，包含 project/agents | 通过 |
| 静默校验配置 | `<prefix> config --quiet` | `--quiet` | 配置合法时无输出并退出 0 | 通过 |
| 应用 project | `<prefix> up` | `-f`、`--project-name` | project 注册/更新到 daemon 后返回 | 通过 |
| 查看 project 列表 | `<prefix> ls` | 无 | 表格展示 daemon 上的 project | 通过 |
| JSON project 列表 | `<prefix> --json ls` | `--json` | stdout 为 JSON，不混入 warning | 通过 |
| project 分页与详细信息 | `<prefix> ls --verbose --limit 10 --offset 0` | `--verbose`、`--limit`、`--offset` | 展示 project id、project dir、spec hash 等扩展字段 | 通过 |
| 查看 project 详情 | `<prefix> inspect project` | `inspect project` | 展示当前 project 详情 | 通过 |
| 查看 agent 详情 | `<prefix> --json inspect agent reviewer` | `inspect agent`、`--json` | 返回 reviewer agent 详情 | 通过 |
| 关闭 project | `<prefix> down` | 无 | 停止 project 相关运行态 | 通过 |

结论：`-f` 可在任意目录定位 project 配置，`--project-name` 可覆盖 project 名称，`ls` 的分页、详细输出和 JSON 输出均可用。

## 5. Sandbox 运行与生命周期测试

生命周期测试使用 `run reviewer --command 'true' --keep-running` 创建可复现 sandbox。`true` 会立即完成命令执行，`--keep-running` 保留 runtime，便于后续执行 `ps/inspect/exec/logs/stop/resume/rm`。

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 创建并保留 sandbox | `<prefix> run reviewer --command 'true' --keep-running` | `run`、`--command`、`--keep-running` | 命令退出 0，输出 sandbox id | 通过 |
| 运行后自动删除 sandbox | `<prefix> run reviewer --command 'printf run-rm-ok' --rm` | `--command`、`--rm` | 命令输出内容，完成后删除 sandbox | 通过 |
| 查看运行中 sandbox | `<prefix> ps` | 无 | 默认展示 running sandbox | 通过 |
| 查看所有 sandbox JSON | `<prefix> --json ps --all` | `--json`、`--all` | 展示 running/stopped 等所有 sandbox | 通过 |
| 按状态过滤并显示详细列 | `<prefix> ps --all --status running --verbose` | `--status`、`--verbose` | 仅展示匹配状态的 sandbox，并包含 driver/image/workspace 等详细列 | 通过 |
| 查看 sandbox 详情 | `<prefix> --json inspect sandbox <sandbox>` | `inspect sandbox` | 返回 sandbox/session runtime 详情 | 通过 |
| 兼容查看 session | `<prefix> --json inspect session <sandbox>` | `inspect session` | 返回详情，并在 stderr 输出 deprecated warning | 通过 |
| 查看 run 详情 | `<prefix> --json inspect run <run-id>` | `inspect run` | 返回 run 详情 | 通过 |
| 删除 running sandbox 被拒绝 | `<prefix> rm <sandbox>` | `rm` | 返回 `is running`，退出码非 0 | 通过 |
| 停止 sandbox | `<prefix> stop <sandbox>` | `stop` | sandbox 状态变为 stopped | 通过 |
| 恢复 sandbox | `<prefix> resume <sandbox>` | `resume` | sandbox 恢复 running | 通过 |
| 强制删除 sandbox | `<prefix> rm --force <sandbox>` | `--force` | 删除 running sandbox | 通过 |

结论：sandbox 作为 CLI 对外概念已经贯穿 `run/ps/inspect/stop/resume/rm`。`rm` 对 running sandbox 的保护符合预期，只有 `--force` 才允许强制删除。

## 6. Exec 命令测试

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 使用 `--command` 执行命令 | `<prefix> exec <sandbox> --command 'printf cli-exec-ok'` | `exec <sandbox>`、`--command` | 输出 `cli-exec-ok` | 通过 |
| 使用 positional command 执行命令 | `<prefix> exec <sandbox> printf cli-exec-args-ok` | `exec <sandbox> [command] [args...]` | 输出 `cli-exec-args-ok` | 通过 |
| 兼容旧 `--session-id` | `<prefix> --json exec --session-id <sandbox> --command 'printf legacy-exec-ok'` | `--session-id`、`--json` | stdout 为 JSON；stderr 输出 deprecated warning | 通过 |

结论：新的 `exec <sandbox>` 语义可用，旧 `--session-id` 入口仍兼容，且 deprecated warning 不污染 JSON stdout。

## 7. Logs 命令测试

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 查看 agent 日志 | `<prefix> logs reviewer --tail 20` | positional agent、`--tail` | 输出 reviewer 的 run 日志 | 通过 |
| 查看 sandbox 日志 JSON | `<prefix> --json logs --sandbox <sandbox> --tail 20 --timestamp` | `--sandbox`、`--tail`、`--timestamp`、`--json` | 返回指定 sandbox 日志，包含时间信息 | 通过 |
| 兼容旧 `--session-id` | `<prefix> --json logs --session-id <sandbox> --tail 5` | `--session-id`、`--json` | stdout 为 JSON；stderr 输出 deprecated warning | 通过 |
| 跟随日志 | `<prefix> logs reviewer --follow --tail 5` | `--follow`、`--tail` | 输出已有日志；当前 run 已结束时命令可正常返回 | 通过 |

结论：`logs` 可按 agent/sandbox/session 兼容入口读取日志，`--tail`、`--timestamp`、`--follow` 均完成基础验证。

## 8. 镜像命令测试

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 查看镜像列表 | `<prefix> images` | 无 | 表格展示 daemon 可见镜像 | 通过 |
| JSON 镜像列表 | `<prefix> --json images` | `--json` | stdout 为 JSON | 通过 |
| 查看镜像详情 | `<prefix> --json inspect image agent-compose-guest:latest` | `inspect image`、`--json` | 返回镜像详情 | 通过 |
| 拉取单个镜像 | `<prefix> pull busybox:latest` | `pull <image>` | 拉取 busybox 镜像 | 通过 |
| 查看已拉取镜像 | `<prefix> inspect image busybox:latest` | `inspect image` | 返回 busybox 镜像详情 | 通过 |
| 删除镜像 | `<prefix> rmi busybox:latest` | `rmi <image>` | 删除 busybox 镜像 | 通过 |
| 拉取 project 镜像 | `<busybox-project-prefix> pull` | 无 image 参数 | 从 project agents 中读取 image 并拉取 | 通过 |

兼容的旧 `image` 命令树：

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| 旧镜像列表 | `<prefix> image ls` | `image ls` | 功能可用，stderr 输出 deprecated warning | 通过 |
| 旧镜像拉取 | `<prefix> image pull busybox:latest` | `image pull` | 功能可用，stderr 输出 deprecated warning | 通过 |
| 旧镜像详情 | `<prefix> --json image inspect agent-compose-guest:latest` | `image inspect`、`--json` | stdout 为 JSON；stderr 输出 deprecated warning | 通过 |
| 旧镜像删除 | `<prefix> image rm busybox:latest` | `image rm` | 功能可用，stderr 输出 deprecated warning | 通过 |

结论：新镜像命令 `images/pull/rmi/inspect image` 可用；旧 `image` 命令树没有删除，兼容 warning 正确输出到 stderr。

## 9. Run 兼容入口测试

| 测试项 | 命令结构 | 关键参数 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| positional prompt 兼容 warning | `<prefix> run reviewer legacy positional prompt` | 旧 positional prompt | stderr 输出 deprecated warning，并提示使用 `--prompt` | 通过 |

说明：该命令会进入真实 provider 执行路径，测试环境没有把 provider 完成结果作为确定性 E2E 断言。本轮验证重点是旧 positional prompt 入口的兼容 warning 行为，以及 warning 不写入 JSON stdout 的约束。

## 10. 历史延期命令边界测试

| 测试项 | 命令结构 | 当前预期 | 结果 |
| --- | --- | --- | --- |
| stats | `<prefix> stats` | 历史测试时尚未发布，返回未知命令/不可用错误 | 当时符合预期；当前已实现 |
| build | `<prefix> build` | 当前未实现，返回未知命令/不可用错误 | 符合预期 |
| push | `<prefix> push` | 当前未实现，返回未知命令/不可用错误 | 符合预期 |

结论：该表只描述历史测试时点。当前 `stats` 已发布为 sandbox stats 命令；`build` 和 `push` 仍不作为稳定 CLI 能力。

## 11. 测试中发现的问题

### 11.1 `exec --session-id <sandbox> --command` 参数校验问题

问题：旧兼容入口 `exec --session-id <sandbox> --command ... --json` 曾被 Cobra positional args 校验拦截，报 `requires at least 1 arg(s)`。

处理：已修复，提交为：

```text
ee40224 fix(cli): allow legacy exec target with command flag
```

复测结果：通过。旧入口可用，deprecated warning 输出到 stderr，JSON stdout 保持干净。

### 11.2 `pull` 无参数遇到本地镜像时会失败

现象：

```bash
<prefix> pull
```

当 project 中的 agent image 是 `agent-compose-guest:latest` 时，命令会尝试从 registry 拉取该镜像；由于它是本地构建镜像，远端仓库不存在，返回：

```text
pull access denied for agent-compose-guest
```

判断：这是测试示例镜像与 `pull` 语义之间的环境限制，不是 CLI 参数解析失败。使用临时 busybox project 复测 `pull` 无参数已通过。

建议：后续可考虑优化错误信息，提示用户该镜像可能是本地镜像或私有镜像，需要先登录 registry 或改用可拉取镜像。

### 11.3 外部 timeout 中断 `run` 可能留下 runtime 容器

现象：测试脚本曾使用：

```bash
timeout 60s <prefix> run reviewer --command 'sleep 300' --keep-running
```

`timeout` 向 CLI 发送终止信号后，daemon 侧 project/sandbox 元数据最终显示 stopped/removed，但 Docker 层仍出现一个运行中的测试容器，需要手动清理。

判断：这是异常中断路径下的 runtime 清理问题。正常的 `run --command 'true' --keep-running`、`stop`、`resume`、`rm --force` 生命周期测试均通过。

建议：后续单独补一个异常取消场景的测试与修复，确保客户端连接被外部杀掉时，daemon 能够一致地取消 run 并清理 runtime，或明确把 runtime 保留策略写入状态。

## 12. 测试统计

| 类型 | 数量 | 说明 |
| --- | ---: | --- |
| 确定性通过测试项 | 59 | 已实现命令、关键参数、JSON 输出、deprecated warning、认证、sandbox 生命周期、镜像命令。 |
| 历史延期命令边界项 | 3 | 历史测试时 `stats`、`build`、`push` 尚未发布；当前 `stats` 已实现，`build`/`push` 仍暂缓。 |
| 已修复问题 | 1 | `exec --session-id ... --command` 兼容入口参数校验问题。 |
| 新发现待跟进问题 | 2 | 本地镜像 `pull` 报错提示可优化；外部 timeout 中断 `run` 后可能遗留 runtime 容器。 |
| 当前阻塞发布的问题 | 0 | 除已明确延期项和异常中断清理边界外，已实现 CLI 命令通过本轮复测。 |

总体结论：本文所述测试在历史分支和历史版本上完成。当前已经实现的 agent-compose CLI 命令体系以命令行使用手册和 CLI 当前设计为准；兼容入口仍要求 deprecated warning 不污染 JSON stdout。
