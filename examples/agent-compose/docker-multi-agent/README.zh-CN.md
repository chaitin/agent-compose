# Docker 多 agent project

语言：[English](README.md) | 中文

两个 agent 引用同一个 workspace 声明，但会获得独立的 sandbox workspace 副本
和 agent definition。

```bash
agent-compose up
agent-compose inspect agent reviewer
agent-compose inspect agent tester
agent-compose run reviewer --command "printf 'reviewer ok\\n'"
agent-compose run tester --command "printf 'tester ok\\n'"
agent-compose logs reviewer
agent-compose logs tester
agent-compose down
```

command 路径不会调用 provider；使用 `--prompt` 时才会应用两个 agent 各自的
system prompt。
