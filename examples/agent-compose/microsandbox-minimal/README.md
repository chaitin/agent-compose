# Minimal Microsandbox project

Languages: English | [中文](README.zh-CN.md)

This is a configuration template for a Microsandbox-backed agent.

## Requirements

Runtime use requires Linux, KVM access, Microsandbox artifacts, and a binary
whose `compiled_drivers` contains `microsandbox`. Check the host first:

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## Run the tutorial

```bash
agent-compose config
# On a prepared Linux/KVM host with a Microsandbox-enabled binary:
agent-compose up
```

The compose file is validated in normal tests. Runtime execution was not
verified locally and requires Linux, `/dev/kvm`, Microsandbox runtime artifacts,
and a binary whose `compiled_drivers` includes `microsandbox`.

`config` is safe on any platform and should normalize
`driver.name: microsandbox`. On a prepared host, continue with
`run reviewer --command "uname -a"`, inspect the returned sandbox, and finish
with `down`. Runtime success is deliberately not claimed locally.

## Real local config output

Captured with the current CLI on 2026-07-15:

```yaml
name: microsandbox-minimal
agents:
    - name: reviewer
      provider: codex
      image: ghcr.io/chaitin/agent-compose-guest:latest
      driver:
        name: microsandbox
        microsandbox: {}
network:
    mode: default
```

This is config validation only; no Microsandbox runtime output is claimed.
