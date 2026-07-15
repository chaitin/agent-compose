# 最小 Microsandbox project

语言：[English](README.md) | 中文

这是一个使用 Microsandbox driver 的配置模板。

```bash
agent-compose config
# 在准备好的 Linux/KVM 主机和包含 Microsandbox 的二进制上：
agent-compose up
```

compose 文件会进入常规测试，但本地未验证 runtime 执行。运行需要 Linux、
`/dev/kvm`、Microsandbox runtime artifacts，并且二进制的 `compiled_drivers`
包含 `microsandbox`。
