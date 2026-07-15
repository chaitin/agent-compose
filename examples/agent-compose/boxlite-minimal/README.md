# Minimal BoxLite project

Languages: English | [中文](README.zh-CN.md)

This is a configuration template for a BoxLite-backed agent.

```bash
agent-compose config
# On a prepared Linux/KVM host with a BoxLite-enabled binary:
agent-compose up
```

The compose file is validated in normal tests. Runtime execution was not
verified locally and requires Linux, `/dev/kvm`, BoxLite runtime artifacts, and
a binary whose `compiled_drivers` includes `boxlite`.
