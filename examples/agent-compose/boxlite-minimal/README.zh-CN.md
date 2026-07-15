# 最小 BoxLite project

语言：[English](README.md) | 中文

这是一个使用 BoxLite driver 的配置模板。

## 环境要求

实际运行要求 Linux、KVM 权限、BoxLite artifacts，以及 `compiled_drivers` 包含
`boxlite` 的二进制。应用前检查：

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## 运行教程

```bash
agent-compose config
# 在准备好的 Linux/KVM 主机和包含 BoxLite 的二进制上：
agent-compose up
```

compose 文件会进入常规测试，但本地未验证 runtime 执行。运行需要 Linux、
`/dev/kvm`、BoxLite runtime artifacts，并且二进制的 `compiled_drivers` 包含
`boxlite`。

`config` 可在任意平台安全执行，应归一化出 `driver.name: boxlite`。在准备好的 host
上继续执行 `run reviewer --command "uname -a"`、检查返回的 sandbox，最后执行
`down`。仓库本地验证不会声称 BoxLite runtime 已成功运行。

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

这里只验证 config，不声称 BoxLite runtime 已运行成功。
