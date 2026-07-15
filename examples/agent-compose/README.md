# agent-compose examples

Languages: English | [中文](README.zh-CN.md)

## Locally verified Docker examples

| Example | Capability | Provider auth |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | Minimal project and current sandbox-oriented CLI | No |
| [docker-workspace-lifecycle](docker-workspace-lifecycle/) | Local workspace, exec, stop, resume, and isolation | No |
| [docker-multi-agent](docker-multi-agent/) | Multiple agents sharing a workspace declaration | No for command runs |
| [docker-env-secrets](docker-env-secrets/) | Dotenv, project variables, agent env, and redaction | No |
| [docker-volume-persistence](docker-volume-persistence/) | Managed volume and read-only bind mount | No |
| [docker-build](docker-build/) | Compose-driven guest image build | No |
| [docker-scheduler-cron](docker-scheduler-cron/) | Declarative cron control plane | Only to run the trigger |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | Automatic scheduled provider run | Yes |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | Relative scheduler script URL snapshot | No |
| [docker-scheduler-script-runtime](docker-scheduler-script-runtime/) | State, logs, interval, and scheduler shell | No |

Docker examples require a running agent-compose daemon, Docker daemon, and the
published guest image locally. Their READMEs use stable behavior rather than
dynamic IDs or complete output snapshots.

## Configuration-only KVM templates

- [boxlite-minimal](boxlite-minimal/)
- [microsandbox-minimal](microsandbox-minimal/)

These manifests are parsed by automated tests, but runtime execution requires a
prepared Linux/KVM host and was not verified locally.
