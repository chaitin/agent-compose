# Minimal BoxLite project

Languages: English | [中文](README.zh-CN.md)

This is a configuration template for a BoxLite-backed agent.

## Requirements

Runtime use requires Linux, KVM access, BoxLite artifacts, and a binary whose
`compiled_drivers` contains `boxlite`. Check the binary before applying:

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## Run the tutorial

```bash
agent-compose config
# On a prepared Linux/KVM host with a BoxLite-enabled binary:
agent-compose up
```

The compose file is validated in normal tests. Runtime execution was not
verified locally and requires Linux, `/dev/kvm`, BoxLite runtime artifacts, and
a binary whose `compiled_drivers` includes `boxlite`.

`config` is safe on any platform and should normalize `driver.name: boxlite`.
On a prepared host, continue with `run reviewer --command "uname -a"`, inspect
the returned sandbox, and finish with `down`. Runtime success is deliberately
not claimed by this repository-local validation.

## Normalized config output

`agent-compose config` produces:

```yaml
name: boxlite-minimal
agents:
    - name: reviewer
      provider: codex
      image: ghcr.io/chaitin/agent-compose-guest:latest
      driver:
        name: boxlite
        boxlite: {}
network:
    mode: default
```

This is config validation only; no BoxLite runtime output is claimed.
