# Docker volume 持久化

语言：[English](README.md) | 中文

该 project 把 managed named volume 挂载到 `/cache`，并把只读 bind fixture
挂载到 `/fixtures`。

## 前置条件与配置

Docker 和 daemon 必须已启动。顶层 `cache` volume 由项目管理并以读写方式挂载；
`./fixtures:/fixtures:ro` 相对 compose 目录解析并只读挂载。

## 运行示例
在示例目录中执行：

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

## 预期结果

使用保留 run 返回的 sandbox ID。第一次命令必须读取 fixture 并写入
`/cache/value`；stop/resume 后 `cat` 必须返回 `persistent`。`touch` 检查必须因只读
挂载而失败。最后显式 stop、rm，再执行 `down`。

## 输出示例

sandbox 成功 stop/resume 后，持久化值和只读检查如下：

```console
$ agent-compose exec <sandbox-id> -- cat /cache/value
persistent
$ agent-compose exec <sandbox-id> -- sh -c 'if touch /fixtures/unexpected 2>/dev/null; then exit 1; fi'
# exit status 0：只读挂载拒绝写入
```
