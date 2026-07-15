# Docker multi-agent project

Languages: English | [中文](README.zh-CN.md)

Two agents share one workspace declaration while receiving independent sandbox
copies and agent definitions.

## Prerequisites and configuration

Docker and the daemon must be running. Both agents refer to the same local
workspace source, but each run creates an independent sandbox copy. Their
different `system_prompt` values apply to model prompts, not shell commands.

## Run the tutorial

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

The command path does not invoke the configured provider. The distinct system
prompts apply when the agents are run with `--prompt`.

## What to verify

`inspect agent` should show two definitions. Both command runs should read
`workspace/project.txt`, return their respective marker, and have different
run/sandbox IDs. Use `--prompt` only when the daemon has a configured provider.
`down` cleans both agents' project sandboxes.
