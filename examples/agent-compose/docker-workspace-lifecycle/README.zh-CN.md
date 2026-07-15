# Docker workspace 生命周期

语言：[English](README.md) | 中文

该示例把 `workspace/` 复制到每个新 sandbox，并在不需要模型凭证的情况下演示
当前 sandbox 生命周期。

## 前置条件与配置

Docker 和 `agent-compose` daemon 必须已启动，并且 Docker 可获得发布版 guest
镜像。`source` 使用本地 workspace provider，`worker` 按名称引用它。新 sandbox
得到源目录副本，不会把 host 源目录作为可写工作区挂载。

## 运行教程

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

## 验证与清理

使用 `run` 返回的 sandbox ID，在 stop/resume 前后读取文件，并确认 host 上不存在
`workspace/generated.txt`。`rm` 删除已停止 sandbox，`down` 清理剩余项目状态。
