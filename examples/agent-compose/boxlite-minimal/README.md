# Minimal BoxLite project

Languages: English | [中文](README.zh-CN.md)

This example defines a single Codex agent backed by the BoxLite driver.

## Requirements

Runtime use requires Linux, KVM access, BoxLite artifacts, and a binary whose
`compiled_drivers` contains `boxlite`. Check the binary before applying:

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## Inspect the configuration

```bash
agent-compose config
```

The normalized output should contain `driver.name: boxlite`.

## Run on a BoxLite host

After the requirements above are satisfied:

```bash
agent-compose up
agent-compose run reviewer --command "uname -a"
agent-compose ps --all
agent-compose down
```

`run` should return `status: succeeded`, a non-empty sandbox ID, and the guest
kernel information. If the binary does not include BoxLite, the command reports
the driver as unsupported. Missing KVM access or runtime artifacts cause
BoxLite initialization to fail.

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

This output confirms that the project selects BoxLite. Runtime output depends
on the BoxLite artifacts and KVM environment on the host.
