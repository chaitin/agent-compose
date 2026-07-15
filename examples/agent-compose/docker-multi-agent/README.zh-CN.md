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

## 成功输出示例

两个 agent 都成功运行后，输出示例如下：

```console
reviewer status=succeeded sandbox=56dc449f3f6c47169bda2ca943a7681b847e0005c5b24aca3294aa5a5cb1a78e
reviewer ok
tester status=succeeded sandbox=4151fb772c909e76d9b07a6d2d86045037eece1548ea6e83380609c73ce01d4b
tester ok
```

两个动态 sandbox ID 不同，证明 run 相互隔离。
