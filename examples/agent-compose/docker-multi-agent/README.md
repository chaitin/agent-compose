# Docker multi-agent project

Languages: English | [中文](README.zh-CN.md)

Two agents share one workspace declaration while receiving independent sandbox
copies and agent definitions.

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

The command path does not invoke the configured provider. The distinct system
prompts apply when the agents are run with `--prompt`.
