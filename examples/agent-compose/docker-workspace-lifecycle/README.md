# Docker workspace lifecycle

Languages: English | [中文](README.zh-CN.md)

This example copies `workspace/` into each new sandbox and demonstrates the
current sandbox lifecycle without model credentials.

```bash
agent-compose up
agent-compose run worker --command "printf 'sandbox-only\\n' > generated.txt" --keep-running
agent-compose ps
agent-compose exec <sandbox-id> -- cat generated.txt
agent-compose stop <sandbox-id>
agent-compose resume <sandbox-id>
agent-compose exec <sandbox-id> -- cat generated.txt
agent-compose stop <sandbox-id>
agent-compose rm <sandbox-id>
agent-compose down
```

`generated.txt` survives stop/resume but is not written into the committed
`workspace/` source. A second new sandbox receives a fresh source copy.
