# Docker workspace 生命周期

语言：[English](README.md) | 中文

该示例把 `workspace/` 复制到每个新 sandbox，并在不需要模型凭证的情况下演示
当前 sandbox 生命周期。

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

`generated.txt` 在 stop/resume 后仍存在，但不会写回仓库中的 `workspace/` source。
新建第二个 sandbox 时会得到一份全新的 source 副本。
