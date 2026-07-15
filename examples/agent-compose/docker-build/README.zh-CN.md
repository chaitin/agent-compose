# 构建 Docker guest image

语言：[English](README.md) | 中文

该示例构建一个基于 guest image 的本地镜像，并验证 build argument marker。

## 前置条件与配置

Docker 和 daemon 必须已启动，Docker 还需能获得发布版 guest 基础镜像。
`build.context` 指向本目录，`dockerfile` 选择 Dockerfile，`args` 提供 marker，
`tags` 为主 `image` 引用增加第二个本地 tag。

## 运行教程

```bash
agent-compose build
agent-compose up
agent-compose run worker --command "cat /opt/agent-compose-example.txt"
agent-compose down
agent-compose rmi agent-compose-example-build:latest --force
agent-compose rmi agent-compose-example-build:local --force
```

预期 marker 为 `built-by-agent-compose`。构建需要 Docker 能访问发布版 guest base
image。示例使用固定 tag 以便阅读；自动化测试应复制示例并替换成唯一 tag。

## 验证要点

`build` 必须完成并创建两个本地 tag。worker 命令读取镜像构建阶段写入的文件，输出
必须为 `built-by-agent-compose`。`down` 后两个 `rmi` 命令删除教程镜像。并发自动化
不要复用这些固定 tag；E2E 会生成唯一 tag。

## 真实验证输出

以下结果采集自 2026-07-15 的真实 Docker build 和 guest run：

```console
status=succeeded
run=a023773553771e0be8d51fb1a983c37e66c2712697b37e9119be7ba4ccc04ef8
sandbox=78459590803602e1945bdac9e3c74a1d9a656c29f57b6169bab757d1779b1d7e
built-by-agent-compose
```

E2E 使用唯一 image tag；动态 run 和 sandbox ID 会不同。
