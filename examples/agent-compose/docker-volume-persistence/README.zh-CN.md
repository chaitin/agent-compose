# Docker volume 持久化

语言：[English](README.md) | 中文

该 project 把 managed named volume 挂载到 `/cache`，并把只读 bind fixture
挂载到 `/fixtures`。

```bash
agent-compose up
agent-compose run worker --command "cat /fixtures/readonly.txt && printf 'persistent\\n' > /cache/value" --keep-running
agent-compose stop <sandbox-id>
agent-compose resume <sandbox-id>
agent-compose exec <sandbox-id> -- cat /cache/value
agent-compose exec <sandbox-id> -- sh -c 'if touch /fixtures/unexpected 2>/dev/null; then exit 1; fi'
agent-compose stop <sandbox-id>
agent-compose rm <sandbox-id>
agent-compose down
```

cache 值在 sandbox stop/resume 后仍存在。`down` 会移除 project-managed volume
的归属，不应把该示例当作备份机制。
