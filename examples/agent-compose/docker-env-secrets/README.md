# Project environment and secrets

Languages: English | [中文](README.zh-CN.md)

This example uses an explicit dotenv file, project variables, agent-specific
environment, and secret metadata. The committed value is intentionally fake.

```bash
agent-compose config
agent-compose up
agent-compose run inspector --command 'test "$PROJECT_VALUE" = project-level && test "$AGENT_VALUE" = agent-level && test "$PROJECT_SECRET" = safe-example-secret && test "$AGENT_SECRET" = safe-example-secret && echo "environment ok"'
agent-compose down
```

`config` redacts values marked `secret: true`. Project variables are supplied to
runs, while agent env is scoped to that agent. Process environment values passed
to the CLI take precedence over `example.env`.
