# Project 环境变量和 secret

语言：[English](README.md) | 中文

该示例使用显式 dotenv 文件、project variables、agent 专属环境变量和 secret
元数据。仓库中提交的值是刻意设置的假值。

## 前置条件与配置

Docker 和 daemon 必须已启动。`env_file: example.env` 提供 `EXAMPLE_SECRET`；
project `variables` 作用于所有 agent，`agents.*.env` 只作用于对应 agent。
`secret: true` 要求渲染配置时隐藏该值。

## 运行教程

```bash
agent-compose config
agent-compose up
agent-compose run inspector --command 'test "$PROJECT_VALUE" = project-level && test "$AGENT_VALUE" = agent-level && test "$PROJECT_SECRET" = safe-example-secret && test "$AGENT_SECRET" = safe-example-secret && echo "environment ok"'
agent-compose down
```

`config` 会隐藏标记为 `secret: true` 的值。Project variables 会传给 run，agent
env 只属于该 agent。启动 CLI 时的进程环境变量优先于 `example.env`。

## 验证要点

执行 `up` 前，确认 `agent-compose config` 包含 `********`，且不输出
`safe-example-secret`。command run 必须输出 `environment ok`，证明真实 guest 收到
两个作用域的变量。该值只是非敏感 fixture；不要提交生产 secret。

## 真实验证输出

以下结果采集自 2026-07-15 的真实 daemon Docker E2E：

```console
status=succeeded
run=f1d22000463b950c2251f72c77477d42fbe9a39b2663bbce39b0b20c04be05e8
sandbox=9fffd2978773c87708dc46facb8bf5ba8b1edf275382afb56380b0294a497939
environment ok
```

E2E 同时断言渲染配置包含 `********` 且不包含 fixture secret。动态 ID 会不同。
