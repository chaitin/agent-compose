# 最小 Docker project

语言：[English](README.md) | 中文

这是最小的 Docker project：一个 agent、显式 guest image，不配置 workspace
和 scheduler。

## 前置条件

- agent-compose daemon 和 Docker daemon 正在运行。
- 本地已有 `ghcr.io/chaitin/agent-compose-guest:latest`。

## 运行

```bash
agent-compose config
agent-compose up
agent-compose inspect agent reviewer
agent-compose run reviewer --command "printf 'docker minimal ok\\n'" --keep-running
agent-compose ps
```

`ps` 列出的是 sandbox，不是 project agent。需要继续执行命令时，从 `run`
或 `ps` 复制 sandbox ID：

```bash
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
agent-compose logs reviewer
agent-compose down
```

预期行为：`config` 显示 `driver.name: docker`，`up` 应用一个 agent，command
输出 `docker minimal ok`，`down` 停止 sandbox 并移除已应用的 project。
