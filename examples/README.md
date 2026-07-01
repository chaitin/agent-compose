# agent-compose Examples

Languages: English | [中文](README.zh-CN.md)

This directory contains example bundles for engine-level agent-compose
contracts.

| Example | What it shows | Validation |
| --- | --- | --- |
| [agent-compose](agent-compose/) | Runnable Docker runtime driver examples for agent projects and schedulers. | `agent-compose bundle validate ./examples/agent-compose/<example>` |
| [service-entry](service-entry/) | Minimal reusable service entry with input/output schemas, trigger, and artifact output. | `agent-compose bundle validate ./examples/service-entry` |

All example bundles are covered by `go test ./pkg/compose`.
