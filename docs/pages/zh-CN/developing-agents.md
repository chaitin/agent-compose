# 使用 agent-compose 开发 Agent

这份手册把 compose 文件、daemon、guest runtime 和运行记录串成一条可执行流程，适合开发 Agent 时使用。

## 三种执行环境

| 接口 | 运行位置 | 主要用途 |
| --- | --- | --- |
| Guest Runtime SDK | sandbox guest 内 | 执行工作区代码、命令、Agent 和 workflow |
| Scheduler API | daemon 的 QuickJS 环境 | 注册触发器，启动 Agent、LLM、命令和事件 |
| Daemon API | 外部客户端，通过 Connect RPC | 管理 project、run、sandbox 和事件 |

三种环境彼此隔离，不能直接互相 `import`。工作区自动化放在 Guest Runtime
SDK，调度策略放在 `scheduler.script`，生命周期控制放在 daemon client。

## 一个最小项目

创建 `agent-compose.yml`：

```yaml
name: review-demo
agents:
  reviewer:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    workspace:
      path: .
    system_prompt: Review the workspace and summarize the result.
```

在该文件所在目录执行：

```bash
agent-compose config
agent-compose up -f agent-compose.yml
agent-compose project ls
agent-compose agent ls review-demo
agent-compose run review-demo/reviewer "Check the README for missing steps."
agent-compose logs review-demo
```

`config` 负责本地配置的解析、校验、规范化和脱敏；`up` 将 revision 保存到
daemon。每次运行都在隔离 sandbox 中执行，不会直接修改源工作区。

## 添加调度

简单任务可以使用声明式 trigger；需要共享流程或组合多种能力时使用脚本：

```yaml
agents:
  reviewer:
    provider: codex
    scheduler:
      script: |
        scheduler.cron("daily-review", "0 9 * * *", async () => {
          return scheduler.agent("Review the latest workspace changes.", {
            sandboxPolicy: "new",
            timeout: "10m",
          });
        }, { timezone: "UTC" });

        function main(payload) {
          return { accepted: true, payload: payload ?? null };
        }
```

使用 `agent-compose scheduler inspect review-demo/reviewer` 查看已保存的调度，
使用 `agent-compose scheduler runs review-demo/reviewer` 查看执行结果。调度脚本
由 daemon 执行，不支持 `import` 或 `require`。触发器 ID、事件、并发、取消和错误
行为见 [Scheduler API 手册](scheduler-api.html)。

## 观察和排查运行

先查看 run 或 scheduler run，再查看事件和日志：

```bash
agent-compose inspect review-demo/reviewer
agent-compose logs review-demo
agent-compose scheduler runs review-demo/reviewer
```

几种状态含义不同：

- `failed`：任务确实执行过，但报告了失败；
- `stopped`：任务被显式停止；
- `skipped`：调度并发策略阻止了新的运行；
- 不支持的能力：具体 handler 拒绝了当前不可用的 driver 或 provider。

daemon 健康或 `config` 成功，并不能证明 provider 和 runtime 镜像可以真正执行；
上线前要用目标 driver 跑一次真实 sandbox 任务。

## 发布前检查

1. 需要可复现时，将移动的 Git source 和 guest image 固定到 release 或 commit。
2. 只提供项目实际使用的 provider 和 guest-image 能力。
3. secret 放在标记为 secret 的环境变量中，不要写入 prompt、源码或日志。
4. 为周期任务设置明确的超时和并发策略。
5. 使用目标 driver 验证真实运行、持久化事件和失败路径。
