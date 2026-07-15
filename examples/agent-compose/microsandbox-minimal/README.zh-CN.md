# 最小 Microsandbox project

语言：[English](README.md) | 中文

这是一个使用 Microsandbox driver 的配置模板。

## 环境要求

实际运行要求 Linux、KVM 权限、Microsandbox artifacts，以及 `compiled_drivers`
包含 `microsandbox` 的二进制。先检查 host：

```bash
agent-compose --json version
test -r /dev/kvm && test -w /dev/kvm
```

## 运行教程

```bash
agent-compose config
# 在准备好的 Linux/KVM 主机和包含 Microsandbox 的二进制上：
agent-compose up
```

compose 文件会进入常规测试，但本地未验证 runtime 执行。运行需要 Linux、
`/dev/kvm`、Microsandbox runtime artifacts，并且二进制的 `compiled_drivers`
包含 `microsandbox`。

`config` 可在任意平台安全执行，应归一化出 `driver.name: microsandbox`。在准备好的
host 上继续执行 `run reviewer --command "uname -a"`、检查返回的 sandbox，最后
执行 `down`。本地不会声称 Microsandbox runtime 已成功运行。
