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
