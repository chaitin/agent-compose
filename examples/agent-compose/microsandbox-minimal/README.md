# Minimal Microsandbox project

Languages: English | [中文](README.zh-CN.md)

This is a configuration template for a Microsandbox-backed agent.

```bash
agent-compose config
# On a prepared Linux/KVM host with a Microsandbox-enabled binary:
agent-compose up
```

The compose file is validated in normal tests. Runtime execution was not
verified locally and requires Linux, `/dev/kvm`, Microsandbox runtime artifacts,
and a binary whose `compiled_drivers` includes `microsandbox`.
