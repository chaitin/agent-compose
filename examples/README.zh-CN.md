# agent-compose 示例

语言：[English](README.md) | 中文

本目录包含 agent-compose 引擎级契约示例 bundle。

| 示例 | 演示内容 | 校验方式 |
| --- | --- | --- |
| [agent-compose](agent-compose/) | Docker runtime driver 下的 agent project 和 scheduler 可运行示例。 | `agent-compose bundle validate ./examples/agent-compose/<example>` |
| [service-entry](service-entry/) | 最小可复用 service entry，包含 input/output schema、trigger 和 artifact 输出。 | `agent-compose bundle validate ./examples/service-entry` |

所有示例 bundle 都由 `go test ./pkg/compose` 覆盖校验。
