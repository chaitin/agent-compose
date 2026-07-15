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
tags in concurrent automation; use a unique tag for each build.

## Example successful output

A successful image build and guest run produces output like:

```console
status=succeeded
run=a023773553771e0be8d51fb1a983c37e66c2712697b37e9119be7ba4ccc04ef8
sandbox=78459590803602e1945bdac9e3c74a1d9a656c29f57b6169bab757d1779b1d7e
built-by-agent-compose
```

Generated run and sandbox IDs differ. Concurrent automation should substitute
a unique image tag.
