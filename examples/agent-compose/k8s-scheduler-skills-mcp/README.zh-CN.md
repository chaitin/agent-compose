# agent-compose k8s driver：scheduler、skills、MCP 与 volumes

Languages: [English](README.md) | 中文

一个项目里四个 `driver: k8s` agent，分别验证 k8s driver 在没有共享文件系统情况下
（见 `docs/design/k8s_pod_runtime_driver_design.md` §2.1）的几条 guest 同步路径：

- `scheduled`：cron 触发，验证 scheduler 能按计划创建并运行 k8s sandbox。
- `skilled`：git 来源的 skill，验证 daemon 解析后通过 exec 推送到 guest Pod。
- `mcp-enabled`：项目级 MCP server 引用，验证 managed MCP 配置块被推送到 guest
  Pod 的 `~/.codex/config.toml`。
- `volumed`：`driver: k8s` 的项目 volume，验证会创建 PersistentVolumeClaim 并
  挂载到 guest Pod。

## 前置条件

- 一个运行在 `k8s` driver 上的 `agent-compose` daemon（`charts/agent-compose`
  的 `runtime.driver: k8s`），且集群节点上能拉到 `agent-compose-guest:latest`。
- daemon 所在的 guest 镜像需要 `git`（`skilled` agent 用），以及 Node.js（
  `agent-compose-guest:latest` 已自带，供 MCP server 用）。
- 集群需要有能满足 `volumed` agent 的 `cache` volume PVC 的 StorageClass。

## 应用

```bash
agent-compose --host <daemon-http-endpoint> up
agent-compose --host <daemon-http-endpoint> scheduler trigger scheduled every-minute
agent-compose --host <daemon-http-endpoint> run skilled --command true --keep-running
agent-compose --host <daemon-http-endpoint> run mcp-enabled --command true --keep-running
agent-compose --host <daemon-http-endpoint> run volumed --command "echo hi > /cache/test.txt" --keep-running
```
