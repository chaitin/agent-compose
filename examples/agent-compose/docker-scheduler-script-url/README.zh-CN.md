# Scheduler 脚本 URL

语言：[English](README.md) | 中文

QJS scheduler 保存在 `scheduler.js`，通过 `scheduler.script.url` 引用。

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer source-loaded
# 等待 scheduler inspect 显示 source-loaded 已触发。
agent-compose down
```

`config` 和 `up` 以 compose 目录为基准解析相对路径，并把内容快照内联发送给
daemon。修改 `scheduler.js` 后需要再次 `up`；它不是运行时 import，也不会后台刷新。

两秒 timeout trigger 执行本地 shell 命令，因此不需要 provider 认证。它由
scheduler runtime 驱动；`scheduler trigger` 的 project-run 路径用于 agent prompt。
