# 最小 BoxLite project

语言：[English](README.md) | 中文

本示例定义一个使用 BoxLite driver 的 Codex agent。

## 环境要求

实际运行要求 Linux、KVM 权限、BoxLite artifacts，以及 `compiled_drivers` 包含
`boxlite` 的二进制。应用前检查：

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## 查看配置

```bash
agent-compose config
```

归一化输出应包含 `driver.name: boxlite`。

## 在 BoxLite host 上运行

满足上述环境要求后执行：

```bash
agent-compose up
agent-compose run reviewer --command "uname -a"
agent-compose ps --all
agent-compose down
```

`run` 应返回 `status: succeeded`、非空 sandbox ID 和 guest kernel 信息。如果二进制
未包含 BoxLite，命令会报告 driver 不受支持；缺少 KVM 权限或 runtime artifacts
会导致 BoxLite 初始化失败。

## Config 归一化输出

`agent-compose config` 输出如下：

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

该输出表明 project 已选择 BoxLite；runtime 输出取决于 host 上的 BoxLite artifacts
和 KVM 环境。
