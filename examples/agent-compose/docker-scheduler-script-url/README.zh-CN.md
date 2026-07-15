# Scheduler 脚本 URL 示例

语言：[English](README.md) | 中文

本示例把 QJS 保存在 `scheduler.js`，并在 `agent-compose.yml` 中通过
`scheduler.script.url` 引用。

脚本保留原来的每日 `scheduler.cron(...)` agent callback，同时增加两秒后执行的
`scheduler.timeout(...)` shell callback。timeout 用于快速验证外部文件已加载，不会
替代 cron 教程。

## 分步运行

```bash
agent-compose config
agent-compose up
agent-compose ps
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer daily-review
agent-compose scheduler inspect reviewer source-loaded
agent-compose down
```

`config` 会把获取到的脚本以内联形式输出。`up` 再获取一次，基于内容快照计算
hash，并且只把脚本文本发送给 daemon。修改 `scheduler.js` 后需再次执行 `up`
才会生效。相对路径以 `agent-compose.yml` 所在目录为基准。

控制面命令不要求 provider 凭证；scheduled agent callback 要求可用的 guest
runtime 和 daemon provider 配置。`source-loaded` shell callback 不调用模型。

预期检查：

1. `config` 包含解析后的 QJS 内容。
2. `scheduler ls reviewer` 同时列出 `daily-review` 和 `source-loaded`。
3. 约两秒后，`source-loaded` 已触发，event 中包含 `scheduler script URL ok`。
4. `daily-review` 仍按 `0 9 * * *` 调度，并在日历时间到达时调用 agent。
5. `down` 禁用 scheduler 并清理项目 sandbox。

## 真实验证输出

以下结果采集自 2026-07-15 的真实 scheduler runtime：

```console
type=loader.command.completed
message="scheduler script URL ok"
payload={"exitCode":0,"mode":"shell","stderrTruncated":false,"stdoutTruncated":false,"success":true}
```

原 event 还包含动态 cell 和 sandbox ID，此处为可读性省略。E2E 会断言 message 和
shell 成功结果。
