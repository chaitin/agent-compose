# agent-compose 项目级策略库示例

语言：[English](README.md) | 中文

本示例演示项目级 `policies` 策略库：按用途定义策略块，Agent 通过名称选择，另有一个 Agent 直接内联声明策略。运行时使用 Docker 驱动。

`config`、`up` 和 `ls` 只涉及控制面，不需要模型或 API key。

## 前置条件

- `agent-compose` daemon 正在运行。
- 只有真正启动 Agent sandbox 时才需要 Docker。
- 真正运行前需要本地已有 `agent-compose-guest:latest`。

## 配置文件

```yaml
name: docker-policy-library

policies:
  locked-down:
    network:
      default: deny-all
      allow:
        - host: api.github.com
          port: 443
  retained-runtime:
    stopped_runtime_policy: retain

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    policy: locked-down
    driver:
      docker: {}

  builder:
    provider: codex
    image: agent-compose-guest:latest
    policy: retained-runtime
    driver:
      docker: {}

  adhoc:
    provider: codex
    image: agent-compose-guest:latest
    policy:
      network:
        default: allow-all
    driver:
      docker: {}
```

## 引用规则

- `policy: <名称>` 引用 `policies` 中的条目。名称在项目规范化时解析，每个 Agent 最终携带自己的有效策略块。
- 把策略块原样内联，会得到完全相同的规范化和 spec hash，因此把共享块搬进 `policies` 不改变引擎实际执行的内容。
- 引用不存在的名称会报错，绝不会被当成"没有策略"——未声明的策略意味着出网不受限制。
- 即使没有 Agent 引用，每个条目也会被校验；条目本身不能再引用其他策略。
- 策略库只是编写便利，不属于规范化配置：`config` 展示的是每个 Agent 的有效 `policy` 块。

## 策略库与 Agent 自有策略的关系

策略库是目录，Agent 的 `policy` 是从中做一次选择，或写自己的块。二者不叠加：

| Agent 声明 | 生效策略 |
| --- | --- |
| `policy: locked-down` | 就是 `locked-down` 这一条。 |
| `policy: {…}` 内联 | 就是内联的这个块。 |
| 不写 `policy` | 没有策略：出网不受限，停止时 `remove`。 |

- 同一个 key 只有一种形状：名称是 scalar，块是 mapping，所以一个 Agent 不可能同时写两者；每个 Agent 恰好只有一个生效策略。
- 库条目不会合并进自行声明块的 Agent。只有按名称引用它的 Agent 才受其约束，因此上面的 `adhoc` 不受 `locked-down` 影响。
- 没有任何隐式继承。目前不存在项目级默认策略：不写 `policy` 的 Agent 就是没有策略，这与"未声明策略"一贯的含义一致。
- "大部分 Agent 用同一条策略、个别例外"不需要组合规则：例外自己写块，其余引用条目即可。

如果真要合并两层，就会出现一个文件里根本没写过的第三种块，读者也就无法只读一处声明就知道 sandbox 到底执行什么。将来若确实需要统一默认值，应当是一个明确的 key 加明确的规则（Agent 覆盖默认，或两者取更严），而不是隐式合并。

## 校验与应用

在示例目录下：

```bash
agent-compose config
agent-compose up
agent-compose ls
```

在仓库根目录下，无需安装二进制：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-policy-library/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-policy-library/agent-compose.yml up
```

规范化输出中 `policies` 已被解析掉，每个 Agent 直接携带它选中的块（完整输出见英文文档）。

## Docker 驱动与 `default: deny-all`

`reviewer` 声明了 `default: deny-all`。Docker 驱动自身无法安装该过滤，但 daemon 的正向代理位于 sandbox 的流量路径上：sandbox 会带上代理环境变量，`api.github.com:443` 被放行，其他目标一律拒绝。`config` 和 `up` 仍然不需要模型或 API key 即可成功；真正 `run` `reviewer` 时是过滤出网，而不是 fail closed。

如果 guest 无法通过代理连通 daemon，该声明会被报告为未执行，sandbox 以更宽的出网运行，除非部署设置了 `EGRESS_UNENFORCEABLE_POLICY=refuse`。若需要访问声明未点名的目标，请使用 `builder` 或 `adhoc`，或把 `locked-down` 改成 `default: allow-all`（声明仍会被校验、哈希和保存，只是不会拒绝任何流量）。

## 清理

```bash
agent-compose down
```
