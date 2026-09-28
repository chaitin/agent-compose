# agent-compose k8s driver: scheduler, skills, MCP, and volumes

Languages: English | [中文](README.zh-CN.md)

Four `driver: k8s` agents in one project, each exercising a different
guest-sync path that has no shared filesystem to fall back on (see
`docs/design/k8s_pod_runtime_driver_design.md` §2.1):

- `scheduled`: a cron trigger, verifying the scheduler creates and runs a k8s
  sandbox on schedule.
- `skilled`: a git-sourced skill, verifying the daemon resolves it and pushes
  it into the guest Pod via exec.
- `mcp-enabled`: a project-level MCP server reference, verifying the managed
  MCP config block is pushed into the guest Pod's `~/.codex/config.toml`.
- `volumed`: a `driver: k8s` project volume, verifying it provisions a
  PersistentVolumeClaim and mounts it into the guest Pod.

## Prerequisites

- An `agent-compose` daemon running on the `k8s` driver
  (`runtime.driver: k8s` in `charts/agent-compose`), pointed at a cluster where
  `agent-compose-guest:latest` is available to the nodes.
- The daemon's guest image needs `git` for the `skilled` agent, and Node.js
  (already in `agent-compose-guest:latest`) for the MCP server.
- A StorageClass able to satisfy the `cache` volume's PVC for the `volumed`
  agent.

## Apply

```bash
agent-compose --host <daemon-http-endpoint> up
agent-compose --host <daemon-http-endpoint> scheduler trigger scheduled every-minute
agent-compose --host <daemon-http-endpoint> run skilled --command true --keep-running
agent-compose --host <daemon-http-endpoint> run mcp-enabled --command true --keep-running
agent-compose --host <daemon-http-endpoint> run volumed --command "echo hi > /cache/test.txt" --keep-running
```
