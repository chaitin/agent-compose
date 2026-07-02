# Webhook 队列与工作额度设计

## 背景

当前 webhook 链路已经有持久化事件队列：

```text
POST /api/webhooks/:topic
  -> event 表 pending/retrying/publishing_to_bus
  -> EventDispatcher claim
  -> LoaderBus
  -> LoaderManager 匹配 scheduler.on(...)
  -> runLoader
```

这能避免 HTTP 请求直接同步启动大量任务。HTTP handler 只写入 SQLite `event` 表并返回 `202 Accepted`。

现有保护能力：

- `event` 表持久化 pending 事件，支持 claim、retry、dead letter。
- `LoaderBus` 有固定 256 缓冲，满时 dispatcher 会把事件释放为 retrying。
- loader 有 `concurrency_policy`，默认同一 loader 运行中时跳过新 run，`parallel` 则允许并发。

现有缺口：

- 没有按项目、仓库、租户等 webhook payload 特征限制工作并发。
- 自动化开发任务中多个项目可能共用同一个 topic，仅按 topic 限流不足。
- 当前同一 loader 默认 `skip` 会在繁忙时跳过事件，不适合作为 webhook 排队机制。
- `LoaderBus` 是进程内缓冲，不应该承担可靠队列职责。

## 目标

1. webhook 请求高峰到来时，事件先可靠落库，不因同步启动过多 loader run 压垮服务。
2. 后台分发按可配置规则匹配事件特征，并限制对应工作队列的最大并发。
3. 同 topic 可以按项目等 payload 特征分到不同工作额度。
4. 成本最小：复用现有 `event` 表、dispatcher、loader manager，不引入外部队列组件。

非目标：

- 不做跨进程分布式并发锁。本服务当前 control plane 是单进程部署，先覆盖这一运行形态。
- 不新增 UI 配置。先用环境变量/部署配置完成运维侧控制。
- 不改变 webhook HTTP API 的成功响应语义。

## 配置

新增环境变量：

```text
WEBHOOK_QUEUE_RULES_JSON='[
  {
    "name": "repo-agent-compose",
    "workers": 2,
    "match": {
      "topic": "webhook.github.push",
      "payload": {
        "body.repository.full_name": "chaitin/agent-compose"
      }
    }
  },
  {
    "name": "default-github",
    "workers": 4,
    "match": {
      "topic": "webhook.github.*"
    }
  }
]'
```

字段：

| 字段 | 说明 |
|---|---|
| `name` | 队列名。用于日志、测试和内部运行计数。为空或重复视为配置错误。 |
| `workers` | 该队列允许同时运行的 webhook-triggered loader run 数，必须大于 0。 |
| `match.topic` | topic 精确匹配或已有 `*` 后缀前缀匹配。 |
| `match.provider` | 可选，匹配 `event.provider`。 |
| `match.payload` | 可选，按 `event.payload_json` 的点号路径匹配字符串、数字、布尔值。 |

匹配顺序：

1. 按配置数组顺序匹配。
2. 第一个匹配规则决定队列。
3. 没有匹配规则时使用内置队列 `default`。

默认行为：

```text
WEBHOOK_QUEUE_DEFAULT_WORKERS=8
```

- 未配置 `WEBHOOK_QUEUE_RULES_JSON` 时，所有 webhook-triggered loader run 走 `default` 队列，默认并发 8。
- `WEBHOOK_QUEUE_DEFAULT_WORKERS=0` 表示不限制默认队列，用于兼容旧部署。

## 分发语义

队列不是新的存储表，可靠队列仍然是 `event` 表：

```text
pending/retrying event
  -> dispatcher claim
  -> loader manager 找到匹配 loader/trigger
  -> 队列规则匹配 payload，尝试占用 worker
  -> 有 worker：创建 loader run，ack event
  -> 无 worker：释放 claim，status=retrying，next_attempt_at=now+短退避
```

关键点：

- 只有实际拿到 worker 后才启动 loader run。
- 无 worker 时不 ack，事件留在 `event` 表等待下次扫描。
- run 完成后释放 worker。
- 无订阅者事件仍按现有逻辑标记 `no_subscriber`，不占 worker。
- 手动运行、定时运行、loader 派生事件和系统事件不受 webhook 队列限制，继续沿用 loader 自身并发策略。

## 多订阅者处理

一个事件可能匹配多个 loader/trigger。为了避免部分订阅者已 ack、部分订阅者因队列满丢失，处理顺序为：

1. 先收集所有匹配目标。
2. 对 webhook 事件按 `loader_id` 去重；同一个 loader 同时命中精确 topic 和 wildcard trigger 时，只保留第一个匹配 trigger。
3. 为这些目标逐一尝试占用对应队列 worker。
4. 继续占用所有目标 loader 的执行槽位，避免默认 `skip` 并发策略把 webhook 事件直接记为 skipped。
5. 任一队列 worker 或 loader 槽位无法占用时，释放已占用资源，并把事件 release 为 retrying。
6. 全部占用成功后再创建并启动对应 run。

这样可以保证事件在“全部目标可启动且 run 已创建”之前不会从数据库队列中移除，也避免部分目标已创建 run、部分目标失败后留下额外失败记录。

## 与 loader concurrency_policy 的关系

队列额度控制“事件可以启动多少个 run”。loader `concurrency_policy` 继续控制同一个 loader 内部是否允许并发。

当 webhook 事件命中默认 `skip` 策略的 loader，且该 loader 已有 run 在运行时，事件不会被创建为 `skipped` run，也不会 ack；dispatcher 会把事件释放回 `retrying`，等待下一轮重试。这样默认配置下仍然是排队语义，不会把 webhook 任务消费掉。

如果某个自动化开发 loader 本身可以安全并发处理多个项目，可以显式使用：

```js
// 对应 loader 配置
concurrency_policy: "parallel"
```

此时并发上限由匹配到的 webhook queue `workers` 控制。

## 复杂度取舍

选择环境变量 JSON，而不是新建 DB 配置表和 UI：

- 能满足部署侧根据项目/仓库调整 worker 大小。
- 不影响现有 API/前端/proto。
- 避免配置热更新复杂度。规则在服务启动时加载，改配置后重启生效。

选择 `event` 表作为唯一可靠队列，而不是新增任务表：

- 避免双写和迁移风险。
- 已有 claim/retry/idempotency 能直接复用。
- 容量控制逻辑集中在 loader event 分发入口。

## 验证计划

单元测试：

- 队列规则按 topic/provider/payload 匹配。
- 同 topic 不同 payload 分到不同队列。
- worker 满时事件 release 为 retrying，不创建 loader run。
- worker 释放后下一次分发能创建 run。
- 未配置规则时走默认队列。

集成/真实验证：

- `go test ./pkg/events ./pkg/loaders ./pkg/storage -run 'Webhook|EventDispatcher|Loader'`
- `task test` 或至少相关 Go package 全量测试。
- 本地启动 daemon，创建一个 webhook source 和 event trigger loader，快速投递多个同 topic、不同 repository 的 webhook，确认：
  - HTTP 全部返回 202。
  - 超出 worker 的事件保持 retrying/pending，后续继续执行。
  - 不出现大量并发 run 超过配置。
