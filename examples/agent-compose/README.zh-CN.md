# agent-compose 示例

语言：[English](README.md) | 中文

`agent-compose` Docker runtime driver 的可运行示例，按从简单到完整的顺序排列。

| 示例 | 演示内容 | 是否需要 provider 凭证 |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | 最小的 Docker project：一个 agent，不启用 scheduler。 | `config`/`up`/`ps` 不需要 |
| [docker-scheduler-cron](docker-scheduler-cron/) | managed cron scheduler 的控制面流程。 | `config`/`up`/`ps`/`down` 不需要 |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | 从相对文件 URL 来源加载 scheduler 脚本。 | `config`/`up`/`ps`/`down` 不需要 |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | 端到端的定时运行：触发、执行 agent 并持久化日志。 | 定时运行需要 |
| [docker-workspace-lifecycle](docker-workspace-lifecycle/) | 本地 workspace 副本及 sandbox stop、resume、exec、rm。 | 不需要 |
| [docker-multi-agent](docker-multi-agent/) | 两个独立 agent 使用同一 workspace source。 | command 不需要；prompt 需要 |
| [docker-env-secrets](docker-env-secrets/) | Dotenv、project/agent variables 和 secret 隐藏。 | 不需要 |
| [docker-volume-persistence](docker-volume-persistence/) | 托管 volume 和只读 bind mount。 | 不需要 |
| [docker-build](docker-build/) | 构建并运行基于 guest 的 Docker 镜像。 | 不需要 |
| [docker-scheduler-script-runtime](docker-scheduler-script-runtime/) | Inline QJS、持久 scheduler state 和 shell callback。 | 不需要 |
| [boxlite-minimal](boxlite-minimal/) | 最小 BoxLite 配置模板。 | prompt run 需要 |
| [microsandbox-minimal](microsandbox-minimal/) | 最小 Microsandbox 配置模板。 | prompt run 需要 |

## 通用前置条件

- Docker daemon 正在运行。
- `agent-compose` daemon 已经启动。
- Docker 能访问 `ghcr.io/chaitin/agent-compose-guest:latest`。

如需获取示例使用的镜像，执行：

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

每个示例都有自己的 `README.md`，包含完整命令和预期输出。

BoxLite 和 Microsandbox 还要求 Linux、KVM 权限、对应 runtime artifacts，以及包含
所选 compiled driver 的二进制。未在准备好的 host 上运行时，它们只是配置模板。
