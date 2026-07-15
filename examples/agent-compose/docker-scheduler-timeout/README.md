# Declarative timeout scheduler

Languages: English | [中文](README.zh-CN.md)

This example fires once 15 seconds after the scheduler is applied and runs a
Codex prompt.

## Prerequisites

Docker and the published guest image are required. The daemon must also have a
working Codex/OpenAI provider configuration.

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose --json logs reviewer
agent-compose inspect run <run-id>
agent-compose logs --run <run-id>
agent-compose down
```

Poll `logs reviewer` until the run appears; do not assume a fixed completion
time. The JSON log response contains the run ID. A successful detail reports
`source: scheduler`, `status: succeeded`, and Docker as the driver.

The normalized scheduler includes `sandbox_policy: new` and `kind: timeout`.
