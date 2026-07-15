# Build a Docker guest image

Languages: English | [中文](README.zh-CN.md)

This example builds a guest-derived image and verifies a build argument marker.

## Prerequisites and configuration

Docker and the daemon must be running, and Docker must be able to obtain the
published guest base image. `build.context` is this directory, `dockerfile`
selects `Dockerfile`, `args` supplies the marker, and `tags` adds a second local
tag to the primary `image` reference.

## Run the tutorial

```bash
agent-compose build
agent-compose up
agent-compose run worker --command "cat /opt/agent-compose-example.txt"
agent-compose down
agent-compose rmi agent-compose-example-build:latest --force
agent-compose rmi agent-compose-example-build:local --force
```

The expected marker is `built-by-agent-compose`. The build requires Docker and
access to the published guest base image. The example uses fixed local tags for
clarity; automation should copy the example and substitute unique tags.

## What to verify

`build` must complete and create both local tags. The worker command reads the
file written during the image build and must print `built-by-agent-compose`.
After `down`, both `rmi` commands remove the tutorial images. Avoid these fixed
tags in concurrent automation; the E2E creates unique tags for that reason.
