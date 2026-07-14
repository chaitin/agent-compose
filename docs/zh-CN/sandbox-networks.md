# Sandbox 网络

Agent-compose 通过 TCP 端口映射连接 sandbox。Docker、BoxLite 和
Microsandbox 消费相同的标准化 binding，再由各 runtime 使用自己的端口发布实现。

## Project 配置

在 project 顶层声明逻辑 network，并按名称把 agent 加入 network：

```yaml
name: network-demo

networks:
  frontend: {}
  backend:
    driver: port_mapping

agents:
  api:
    networks: [frontend, backend]
    expose:
      - "8080"
      - target: 9090
        host_port: 19090
        protocol: tcp

  worker:
    networks: [backend]

  dashboard:
    ports:
      - "127.0.0.1:18080:8080"
```

当前只支持 `port_mapping` network driver。启用内部网络后，没有显式声明
network 的 agent 会加入隐式的 `default` network。

## `expose`

`expose` 创建内部 listener，并把连接转发到目标 sandbox 内的服务端口。

- `"8080"` 转发到 sandbox 的 8080 端口，listener 端口由系统动态分配。
- 长写法支持 `target`、可选的 `host_port` 和 `protocol`。
- `host_port` 用来固定 listener 端口。发生端口冲突时 sandbox 启动失败，
  agent-compose 不会悄悄改用其他端口。
- 当前只支持 TCP。

Listener 地址由目标 runtime 选择，不需要在每个 agent 中重复配置。

## `ports`

`ports` 使用明确的 host 地址发布 sandbox 服务。支持以下短写法：

```yaml
ports:
  - "8080"                       # 动态 host 端口 -> sandbox 8080
  - "18080:8080"                # 127.0.0.1:18080 -> sandbox 8080
  - "0.0.0.0:18080:8080"        # 所有 host 接口 -> sandbox 8080
```

默认 host IP 是 `127.0.0.1`。当前 host 地址只支持 IPv4，协议只支持 TCP。

## 发布地址

内部 `expose` listener 使用两个可选的 daemon 配置：

```text
NETWORK_DOCKER_PUBLISH_ADDRESS
NETWORK_RUNTIME_PUBLISH_ADDRESS
```

Docker target 使用 Docker 地址；BoxLite 和 Microsandbox target 使用 runtime
地址。没有配置某个地址时，agent-compose 会读取 Docker 默认 `bridge` network
的 IPv4 gateway。

Daemon 作为宿主机进程运行或使用 host network 时通常无需覆盖默认值。Daemon
运行在普通 Docker bridge network 中时，可能需要显式配置两个地址，确保所有
source sandbox 都能路由到生成的 listener。

## 当前能力边界

命名 network 用于记录逻辑归属并标注内部 binding。`port_mapping` driver 不创建
Docker network、不安装防火墙规则、不提供 DNS 名称，也不会透明改写
`api:8080`。因此，属于不同逻辑 network 的 sandbox 在底层仍可能访问同一个
listener。

应用通过 sandbox network state 中记录的实际 `host_ip:host_port` 访问服务。
服务发现和访问策略强制执行不属于当前 network driver 的能力。
