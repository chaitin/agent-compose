# agent-compose 示例

语言：[English](README.md) | 中文

## 已在本地验证的 Docker 示例

| 示例 | 能力 | Provider 认证 |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | 最小 project 和当前 sandbox CLI | 不需要 |
| [docker-workspace-lifecycle](docker-workspace-lifecycle/) | Local workspace、exec、stop、resume 和隔离 | 不需要 |
| [docker-multi-agent](docker-multi-agent/) | 多个 agent 共享 workspace 声明 | command run 不需要 |
| [docker-env-secrets](docker-env-secrets/) | Dotenv、project variables、agent env 和隐藏 secret | 不需要 |
| [docker-volume-persistence](docker-volume-persistence/) | Managed volume 和只读 bind mount | 不需要 |
| [docker-build](docker-build/) | Compose 驱动的 guest image 构建 | 不需要 |
| [docker-scheduler-cron](docker-scheduler-cron/) | 声明式 cron 控制面 | 运行 trigger 时需要 |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | 自动调度的 provider run | 需要 |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | 相对 scheduler script URL 快照 | 不需要 |
| [docker-scheduler-script-runtime](docker-scheduler-script-runtime/) | State、日志、interval 和 scheduler shell | 不需要 |

Docker 示例需要运行中的 agent-compose daemon、Docker daemon，以及本地已有
发布版 guest image。README 描述稳定行为，不保存动态 ID 或完整输出快照。

## 仅做配置验证的 KVM 模板

- [boxlite-minimal](boxlite-minimal/)
- [microsandbox-minimal](microsandbox-minimal/)

自动化测试会解析这些 manifest，但 runtime 执行需要准备好的 Linux/KVM 主机，
本地未做运行验证。
