# Scheduler 脚本 URL 示例

语言：[English](README.md) | 中文

本示例把 QJS 保存在 `scheduler.js`，并在 `agent-compose.yml` 中通过
`scheduler.script.url` 引用。

它同时演示两个相互独立的能力：

- `daily-review` 保留教程中的日历调度
  `scheduler.cron(...) + scheduler.agent(...)` 流程。
- `source-loaded` 是两秒后执行的 `scheduler.timeout(...)` shell 回调，用真实
  daemon 证明外部脚本已被加载并运行。

## 前置条件

- Docker daemon 和 `agent-compose` daemon 已启动。
- Docker 能拉取发布版 guest 镜像，或本地已有该镜像。
- cron agent 回调真正触发时需要已配置的 LLM provider；timeout shell 回调不调用模型。

## Compose 与脚本

compose 文件启用 scheduler，并引用相对于 compose 目录的路径：

```yaml
scheduler:
  enabled: true
  sandbox_policy: new
  script:
    url: ./scheduler.js
```

脚本保留真实 cron agent 示例，同时增加快速验证 trigger：

```js
scheduler.cron("daily-review", "0 9 * * *", function dailyReview() {
  return scheduler.agent("Review the current project state.");
});

scheduler.timeout("source-loaded", function sourceLoaded() {
  return scheduler.shell("printf 'scheduler script URL ok\\n'");
}, 2000);
```

## 运行示例

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer daily-review
agent-compose scheduler inspect reviewer source-loaded
agent-compose down
```

`config` 和 `up` 会解析相对 URL、读取脚本，并把内容快照内联发送给 daemon。
修改 `scheduler.js` 后需要再次执行 `up`；它不是运行时 import，也不会后台刷新。

## 验证要点

- `config` 包含解析后的脚本内容。
- `scheduler ls reviewer` 同时列出 `daily-review` 和 `source-loaded`。
- 约两秒后，检查 `source-loaded` 可看到它已触发，event 中包含
  `scheduler script URL ok`。
- `daily-review` 仍按 `0 9 * * *` 调度，并在日历时间到达时调用 agent。
- `down` 禁用 scheduler 并删除项目 sandbox。

loader ID 和时间戳由环境动态生成。真实 daemon E2E 会断言 event 内容，而不是
写死这些值。
