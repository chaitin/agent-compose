# Docker 多 agent project

语言：[English](README.md) | 中文

两个 agent 引用同一个 workspace 声明，但会获得独立的 sandbox workspace 副本
和 agent definition。

## 前置条件与配置

Docker 和 daemon 必须已启动。两个 agent 引用相同本地 workspace 源，但每次 run
创建独立 sandbox 副本。不同的 `system_prompt` 只作用于模型 prompt，不作用于
shell command。

## 运行教程

```bash
agent-compose up
agent-compose inspect agent reviewer
agent-compose inspect agent tester
agent-compose run reviewer --command "test -f project.txt && printf 'reviewer ok\\n'"
agent-compose run tester --command "test -f project.txt && printf 'tester ok\\n'"
agent-compose logs reviewer
agent-compose logs tester
agent-compose down
```

command 路径不会调用 provider；使用 `--prompt` 时才会应用两个 agent 各自的
system prompt。

## 验证要点

`inspect agent` 应显示两个 definition。两个 command run 都应能读取 workspace
fixture、输出各自 marker，并具有不同 run/sandbox ID。只有 daemon 配置了 provider
后才运行 `--prompt`。`down` 清理两个 agent 的项目 sandbox。
