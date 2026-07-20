# agent-compose 示例

语言：[English](README.md) | 中文

`agent-compose` Docker runtime driver 的可运行示例，按从简单到完整的顺序排列。

| 示例 | 演示内容 | 是否需要 provider 凭证 |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | 最小的 Docker project：一个 agent，不启用 scheduler。 | `config`/`up`/`ps` 不需要 |
| [docker-scheduler-cron](docker-scheduler-cron/) | managed cron scheduler 的控制面流程。 | `config`/`up`/`ps`/`down` 不需要 |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | 从相对 `file` 来源加载 scheduler 脚本，并给出 HTTP 来源的替代配置。 | `config`/`up`/`ps`/`down` 不需要 |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | 端到端的定时运行：触发、执行 agent 并持久化日志。 | 定时运行需要 |

## 通用前置条件

- Docker daemon 正在运行。
- `agent-compose` daemon 已经启动。
- 本地存在 `agent-compose-guest:latest` 镜像。

如需构建 guest 镜像，在仓库根目录执行：

```bash
task image:agent-compose-guest
```

每个示例都有自己的 `README.md`，包含完整命令和预期输出。
