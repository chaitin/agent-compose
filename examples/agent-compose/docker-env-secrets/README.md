# Project environment and secrets

Languages: English | [中文](README.zh-CN.md)

This example uses an explicit dotenv file, project variables, agent-specific
environment, and secret metadata. The committed value is intentionally fake.

## Prerequisites and configuration

Docker and the daemon must be running. `env_file: example.env` supplies
`EXAMPLE_SECRET`; project `variables` apply to every agent, while `agents.*.env`
is agent-scoped. `secret: true` marks values for redaction in rendered config.

## Run the tutorial

```bash
agent-compose config
agent-compose up
agent-compose run inspector --command 'test "$PROJECT_VALUE" = project-level && test "$AGENT_VALUE" = agent-level && test "$PROJECT_SECRET" = safe-example-secret && test "$AGENT_SECRET" = safe-example-secret && echo "environment ok"'
agent-compose down
```

`config` redacts values marked `secret: true`. Project variables are supplied to
runs, while agent env is scoped to that agent. Process environment values passed
to the CLI take precedence over `example.env`.

## What to verify

Before `up`, confirm `agent-compose config` contains `********` and never prints
`safe-example-secret`. The command run must print `environment ok`, proving the
real guest received both scopes. The value is a non-sensitive fixture; do not
commit production secrets or use this pattern as a secret manager.
