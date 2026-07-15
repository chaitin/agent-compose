# 构建 Docker guest image

语言：[English](README.md) | 中文

该示例构建一个基于 guest image 的本地镜像，并验证 build argument marker。

```bash
agent-compose build
agent-compose up
agent-compose run worker --command "cat /opt/agent-compose-example.txt"
agent-compose down
agent-compose rmi agent-compose-example-build:latest --force
agent-compose rmi agent-compose-example-build:local --force
```

预期 marker 为 `built-by-agent-compose`。构建需要 Docker 和本地已有的发布版
guest base image。示例使用固定 tag 以便阅读；自动化测试应复制示例并替换成唯一 tag。
