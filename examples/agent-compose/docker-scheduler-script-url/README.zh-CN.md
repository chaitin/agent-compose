# Scheduler 脚本 file 来源示例

语言：[English](README.md) | 中文

本示例把 QJS 保存在 `scheduler.js`，并在 `agent-compose.yml` 中通过
`scheduler.script.provider: file` 和 `path` 引用。

```bash
agent-compose config
agent-compose up
agent-compose ps
agent-compose down
```

`config` 会把获取到的脚本以内联形式输出。`up` 再获取一次，基于内容快照计算
hash，并且只把脚本文本发送给 daemon。修改 `scheduler.js` 后需再次执行 `up`
才会生效。相对路径以 `agent-compose.yml` 所在目录为基准。

控制面命令不要求 provider 凭证；实际定时运行仍需要可用的 guest runtime 和
provider 凭证。
