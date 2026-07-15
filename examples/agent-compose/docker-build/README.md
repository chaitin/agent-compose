# Build a Docker guest image

Languages: English | [中文](README.zh-CN.md)

This example builds a guest-derived image and verifies a build argument marker.

```bash
agent-compose build
agent-compose up
agent-compose run worker --command "cat /opt/agent-compose-example.txt"
agent-compose down
agent-compose rmi agent-compose-example-build:latest --force
agent-compose rmi agent-compose-example-build:local --force
```

The expected marker is `built-by-agent-compose`. The build requires Docker and
the published guest base image locally. The example uses fixed local tags for
clarity; automation should copy the example and substitute unique tags.
