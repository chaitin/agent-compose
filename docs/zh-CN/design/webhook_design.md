# agent-compose Webhook / Event Ingress 现状

本文档描述当前代码里的外部事件入口和 topic event 分发模型，并记录需要补齐的目标设计。对应实现主要在：

- HTTP handler：`pkg/events/webhooks/http.go`
- topic event model：`pkg/model/`
- SQLite store：`pkg/storage/configstore/topic_event_store.go`
- dispatcher：`pkg/events/dispatcher.go`
- loader bus：`pkg/bus/`
- loader JS API：`pkg/loaders/engine.go`
- loader run host：`pkg/loaders/run_host.go` 和 daemon adapters `pkg/agentcompose/adapters/loader_host.go`

## 总体链路

当前实现：

```text
HTTP webhook ingress
  -> event
  -> EventDispatcher
  -> LoaderBus
  -> loader scheduler.on(...)
  -> optional scheduler.event.publish(...)
  -> event
```

Webhook handler 和 loader `scheduler.event.publish(...)` 都只写 `event`，初始状态为 `pending`。后台 `EventDispatcher` 按 sequence 扫描 pending 事件，发布到进程内 `LoaderBus`。当前代码由 `LoaderManager` 在事件无匹配 loader，或匹配 loader 的 run 记录已创建后调用 ack，并把事件标记为 `published_to_bus`。

`published_to_bus` 只表示事件已经完成当前进程内的投递确认，不表示存在匹配 loader，也不表示 loader 执行业务已经完成。

## Topic 策略

Topic 只允许：

```text
[a-zA-Z0-9._-]+
```

长度上限为 128，不允许空值、空白字符、`/`。

当前前缀边界：

| 发布方 | 允许 topic |
|---|---|
| 外部 webhook ingress | `webhook.*` |
| loader `scheduler.event.publish` | `runtime.*`、`workflow.*`、`external.*` |
| Go 内部生命周期事件 | `agent-compose.*` |

`scheduler.on(...)` 复用 loader topic 匹配能力，支持精确匹配和前缀通配：

```js
scheduler.on("webhook.github.push", function(event) {});
scheduler.on("webhook.github.*", function(event) {});
```

## HTTP API

### 投递事件

当前实现：

```http
POST /api/webhooks/:topic
```

示例：

```bash
curl -X POST http://127.0.0.1:7410/api/webhooks/webhook.github.push \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: github-delivery-xxx' \
  -H 'X-Correlation-ID: github:push:xxx' \
  -d '{"ref":"refs/heads/main","repository":"agent-compose"}'
```

成功响应：

```json
{
  "accepted": true,
  "topic": "webhook.github.push",
  "event_id": "evt_xxx",
  "sequence": 123,
  "correlation_id": "github:push:xxx"
}
```

没有 loader 订阅该 topic 时仍返回 `202 Accepted`。

补齐 source 配置后仍使用同一个投递入口。`webhook_source` 通过 topic prefix、provider header、token 或 signature 参与匹配，不需要把 `source_id` 放进 URL。

### 查询事件

```http
GET /api/events/:event_id
GET /api/events?correlation_id=some_system:object:123
GET /api/events?topic=runtime.some_adapter.requested&after_sequence=123&limit=100
```

查询约束：

- `event_id` 查询返回单个事件。
- `correlation_id` 查询返回同一业务链路下的事件流，按 `sequence` 升序。
- `topic + after_sequence` 可用于外部 adapter 轮询派生事件。
- `limit` 默认 100，上限 500。
- 列表查询必须至少包含 `correlation_id` 或 `topic`。

Topic polling 不是消息队列语义。Adapter 需要自行保存 last seen sequence，并按 `event_id` 或业务 id 做幂等。

## 鉴权

配置：

```text
WEBHOOK_BODY_LIMIT_BYTES=1048576
```

当前实现：

- `/api/webhooks/*` 绕过 UI session auth，由 handler 按 webhook source 校验 token。
- 每个 webhook source 绑定 `topic_prefix` 和 token hash；请求 topic 必须匹配 enabled source。
- 对外约定请求携带 `Authorization: Bearer <source-token>` 或 `X-WEBHOOK-TOKEN`。
- source token 比较使用 hash + constant-time compare。
- `/api/events` 和 `/api/events/*` 使用普通 API/auth 路径，不再依赖 webhook token。

当前是 per-source token 模型。接公网前需要增加 provider signature 校验。

目标行为：

- webhook handler 必须根据 `:topic` 匹配 enabled source 配置。
- token 鉴权使用 `Authorization: Bearer <source-token>` 或 `X-WEBHOOK-TOKEN: <source-token>`。
- provider signature 鉴权按 source 配置启用，例如 GitHub `X-Hub-Signature-256` 或 GitLab token。
- 不保留 legacy 项目前缀的环境变量、表名或 header 作为目标命名。

### Webhook Source 配置增强

需要增加显式 webhook source 配置，用来约束入口、鉴权和 UI 展示。建议新增 `webhook_source` 表和对应管理 API：

| 字段 | 说明 |
|---|---|
| `id` | source id，例如 `github-main` |
| `name` | UI 展示名 |
| `enabled` | 是否允许接收 |
| `provider` | `github`、`gitlab`、`generic` 等 |
| `topic_prefix` | 允许写入的 topic 前缀，例如 `webhook.github.` |
| `token_hash` | source 级 bearer token 哈希，替代明文存储 |
| `signature_type` | `none`、`github_sha256`、`gitlab_token` |
| `signature_secret` | provider signature secret，按 secret 机制加密或托管 |
| `body_limit_bytes` | source 级 body 限制，默认继承全局限制 |
| `created_at` / `updated_at` | 元数据 |

handler 先用 `:topic` 找到 `enabled=true` 且 `topic_prefix` 匹配的 source，再校验 token 或 signature 和 body limit。若同一 topic 匹配多个 source，请求必须通过且只能通过其中一个 source 的鉴权；否则返回 `401 Unauthorized` 或 `409 Conflict`，避免一个全局 token 拥有所有 topic 的写权限。

## Event Envelope

Webhook body 只接受 JSON object，Content-Type 可以是 `application/json` 或带参数的 JSON media type。数组、字符串、数字、布尔值和 `null` 都返回 `400 Bad Request`。

写入 `event.payload_json` 的 webhook payload 使用 camelCase：

```json
{
  "eventId": "evt_xxx",
  "sequence": 123,
  "source": "webhook",
  "provider": "github",
  "intent": "notification",
  "method": "POST",
  "path": "/api/webhooks/webhook.github.push",
  "topic": "webhook.github.push",
  "correlationId": "github:push:xxx",
  "idempotencyKey": "github-delivery-xxx",
  "deliveryId": "provider-delivery-id",
  "remoteAddr": "127.0.0.1:12345",
  "headers": {
    "content-type": "application/json",
    "user-agent": "GitHub-Hookshot/..."
  },
  "query": {},
  "body": {
    "ref": "refs/heads/main"
  }
}
```

Loader callback 收到的 `LoaderTopicEvent` 形状：

```json
{
  "topic": "webhook.github.push",
  "createdAt": "2026-05-28T10:00:00Z",
  "payload": {
    "eventId": "evt_xxx",
    "sequence": 123,
    "source": "webhook",
    "provider": "github",
    "intent": "notification",
    "correlationId": "github:push:xxx",
    "body": {
      "ref": "refs/heads/main"
    }
  }
}
```

`correlation_id` 来源优先级：

1. `X-Correlation-ID`
2. JSON body 顶层 `correlation_id`
3. JSON body 顶层 `correlationId`
4. 新事件自己的 `event_id`

`provider` 对 `webhook.<provider>.*` 取第二段。Loader 派生事件会读取 payload 顶层 `provider` 字段。

Header 只保留 allowlist：

- `content-type`
- `user-agent`
- `x-request-id`
- `x-correlation-id`
- `x-github-event`
- `x-github-delivery`
- `x-gitlab-event`
- `x-hub-signature-256`

敏感 header 会被过滤，例如 `authorization`、`cookie`、`set-cookie`、`x-webhook-token`。

当前只保存 provider signature 相关 header 作为审计和后续扩展输入，不做 provider signature 验证。

## Event Log

事件表为 `event`，由 `ConfigStore.initSchema` 初始化。Go 类型为 `TopicEventRecord`。

核心字段：

| 字段 | 说明 |
|---|---|
| `sequence` | 全局递增游标，SQLite autoincrement |
| `id` | event id，使用 `evt_<uuid>` |
| `topic` | event topic |
| `source` | `webhook`、`loader`、`system` |
| `provider` | webhook provider 或 loader payload provider |
| `intent` | `notification`、`command` 等元数据 |
| `correlation_id` | 业务链路 id |
| `idempotency_key` | 幂等键 |
| `delivery_id` | provider delivery id |
| `payload_hash` | 原始 payload hash，不包含 sequence |
| `payload_json` | 标准 event payload |
| `dispatch_status` | 当前为 `pending` 或 `published_to_bus`；目标投递状态见下文 |
| `parent_event_id` | 派生事件的上游 event |
| `publisher_type` | `webhook`、`loader`、`system` |
| `publisher_id` | loader id 等 |
| `publisher_run_id` | loader run id |
| `created_at` | Unix milli |
| `dispatched_at` | Unix milli |

目标补齐字段：

| 字段 | 说明 |
|---|---|
| `replay_of_event_id` | 人工重放来源 event id，非 replay 事件为空 |

索引覆盖：

- `correlation_id, sequence`
- `topic, sequence`
- `dispatch_status, sequence`
- `topic, idempotency_key` 唯一索引，忽略空幂等键

幂等规则：

- 优先使用 `Idempotency-Key`。
- 没有 `Idempotency-Key` 时使用 provider delivery id，例如 `X-GitHub-Delivery`。
- 没有可用幂等键时不做平台级去重。
- 同一 `topic + idempotency_key` 且 `payload_hash` 一致时，返回已有事件和 `202 Accepted`。
- 同一 `topic + idempotency_key` 但 `payload_hash` 不一致时，返回 `409 Conflict`。

## Dispatcher 语义

`EventDispatcher` 是单进程内后台 goroutine：

1. 按 `sequence` 升序扫描 `pending` 事件。
2. 将 `payload_json` decode 成 map。
3. 调用 `LoaderBus.Publish(LoaderTopicEvent)`。
4. `LoaderManager` 消费 bus event 后，如果无匹配 loader，或匹配 loader 的 run 已创建，则调用 event ack。
5. ack 成功后标记 `published_to_bus` 和 `dispatched_at`。
6. bus 满或 publish 失败时保留 `pending`，下轮重试。

当前没有跨进程 claim、lease、ack、consumer group 或 durable delivery。多副本部署前需要增加原子 claim 和 lease 机制。

已知非可靠窗口：

- 事件写入 bus 后，如果进程在 loader event loop 消费前退出，该事件可能需要靠 pending 重试；如果已经 ack，则不会自动重放。
- 事件创建 loader run 并 ack 后，如果进程在 loader 业务动作完成前退出，Event log 不感知 loader 侧结果。
- 如果事件已经 publish 到 bus，但进程在更新 `published_to_bus` 前退出，重启后可能再次 publish。

Loader callback、外部 adapter 和业务 callback 都应按 `eventId`、`correlationId` 或业务 id 做幂等。

### Dispatch 状态补齐

需要把事件投递状态和 loader 业务状态拆开。`dispatch_status` 不应该承担 “业务是否完成” 的含义。

建议第一阶段只把当前 event 表的 `dispatch_status` 扩展为投递状态：

| 状态 | 含义 |
|---|---|
| `pending` | 已写入，等待 dispatcher 扫描 |
| `publishing_to_bus` | 事件已被当前 dispatcher claim，正在发布到 bus |
| `published_to_bus` | 已完成当前进程内 bus 投递确认 |
| `no_subscriber` | 没有匹配 loader，事件无需业务处理 |
| `retrying` | 本轮 publish 或 ack 失败，等待下次重试 |
| `dead_letter` | 重试耗尽或 payload 无法解码，进入人工处理 |

投递状态补齐需要增加这些字段，避免多个进程或失败重试时只能靠内存状态判断：

| 字段 | 说明 |
|---|---|
| `claim_id` | 当前 claim token，空表示无人持有 |
| `claim_until` | claim 过期时间，Unix milli |
| `attempt_count` | dispatcher 投递尝试次数 |
| `next_attempt_at` | 下次可重试时间，Unix milli |
| `last_error` | 最近一次投递错误 |
| `dead_letter_at` | 进入 dead letter 的时间，Unix milli |

dispatcher 扫描条件应变为：`dispatch_status IN ('pending', 'retrying') AND next_attempt_at <= now`，并用单条条件更新原子 claim。claim 过期后允许其它进程重新 claim。

新增 `event_delivery` 表来表达一条 event 对多个 loader trigger 的处理结果，避免在 event 主表单行里丢失多订阅者信息：

| 字段 | 说明 |
|---|---|
| `event_id` | 源事件 |
| `loader_id` | 匹配的 loader |
| `trigger_id` | 匹配的 event trigger |
| `run_id` | 创建的 loader run |
| `status` | `matched`、`run_started`、`run_succeeded`、`run_failed`、`skipped` |
| `error` | 失败原因 |
| `created_at` / `updated_at` | 元数据 |

建议 schema：

```sql
CREATE TABLE event_delivery (
  event_id TEXT NOT NULL,
  loader_id TEXT NOT NULL,
  trigger_id TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, loader_id, trigger_id)
);

CREATE INDEX idx_event_delivery_run ON event_delivery(run_id);
CREATE INDEX idx_event_delivery_status ON event_delivery(status, updated_at);
```

delivery 在 loader trigger 匹配时写入 `matched`，run 创建后更新为 `run_started`，run 完成后更新为 `run_succeeded` / `run_failed` / `skipped`。

`published_to_bus` 继续保留为投递层状态，但 UI 和 API 应展示 delivery/run 状态，避免用户误以为 webhook 已经被业务成功处理。

### 观测与运维

建议补齐以下 HTTP API 或 Connect API，用于 UI 和排障：

```http
GET /api/events/:event_id/trace
GET /api/events/:event_id/sandboxes
POST /api/events/:event_id/replay
GET /api/webhook-sources
GET /api/webhook-sources/:source_id/stats
```

`trace` 返回 event、父子 event、delivery、loader run、loader event 和关联 sandbox。`sandboxes` 只返回由该 event 触发链路创建或操作过的 sandbox，适合外部系统拿 `event_id` 反查 sandbox。已实现的 `GET /api/events/:event_id/sessions` 路由是同一 sandbox 查询的兼容别名。

`replay` 不应改写原 event，也不应复用原 event id。建议创建一个新 event，payload 继承原 payload，并写入 `replay_of_event_id`、新的 `event_id`、新的 `sequence` 和可选 replay reason。这样幂等键不会和原始 provider delivery 冲突，trace 也能区分原始投递和人工重放。

需要暴露的指标：

- pending event 数量和最大等待时间。
- dispatcher 最近成功时间、失败次数、bus full 次数。
- 按 topic/source/provider 聚合的接收量、拒绝量、2xx/4xx/5xx。
- signature 校验失败、幂等冲突、body too large。
- event 到 run_started、run_completed 的延迟。
- dead letter 数量和最近错误。

UI 上建议在自动化/运行记录里增加 “Webhook 事件” 视图：按 source/topic/status 筛选，展示 event id、correlation id、delivery id、匹配 loader、run 状态、关联 sandbox 和重放入口。

## Event 到 sandbox 查询

需要提供 “根据 `event_id` 查到 sandbox” 的能力。当前已有部分链路可复用：

- webhook event payload 带 `eventId` / `correlationId`。
- event 触发 loader run 时，run 的 `payload_json` 保存了触发事件 envelope。
- loader 通过 v1-compatible session RPC bridge 创建或操作 sandbox 时，会写入 loader event，字段包含 `linked_sandbox_id`。
- loader 派生 event 会写 `parent_event_id`、`publisher_run_id`。
- sandbox 自身有 `trigger_source=script:<loader_id>`，但缺少直接 event/run 关联。

建议查询语义：

```http
GET /api/events/:event_id/sandboxes
```

返回：

```json
{
  "event_id": "evt_xxx",
  "correlation_id": "github:push:xxx",
  "sandboxes": [
    {
      "sandbox_id": "sandbox_xxx",
      "relation": "created_by_loader_run",
      "loader_id": "loader-1",
      "run_id": "run-1",
      "trigger_id": "on-webhook",
      "loader_event_id": "loader_event_id",
      "created_at": "2026-05-28T10:00:00Z"
    }
  ]
}
```

现有表可用于人工排障，但不适合作为正式 API 的主查询路径：

- `loader_run.payload_json` 里有触发事件 envelope，但没有 event id 索引。
- `loader_event.linked_sandbox_id` 能找到 sandbox，但需要先知道相关 run。
- `correlation_id` 可能覆盖多个派生事件和 run，只能作为 trace 辅助条件，不能单独作为精确关联。
- 因此 `GET /api/events/:event_id/sandboxes` 不应依赖全表 JSON 扫描作为主实现。

正式实现需要增加显式关联表。event 到 run 的关系由 `event_delivery` 保存；event 到 sandbox 的关系由 `event_sandbox_link` 保存：

```sql
CREATE TABLE event_sandbox_link (
  event_id TEXT NOT NULL,
  sandbox_id TEXT NOT NULL,
  relation TEXT NOT NULL,
  loader_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  trigger_id TEXT NOT NULL DEFAULT '',
  loader_event_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, sandbox_id, relation, run_id)
);

CREATE INDEX idx_event_sandbox_link_sandbox ON event_sandbox_link(sandbox_id, created_at);
CREATE INDEX idx_event_sandbox_link_run ON event_sandbox_link(run_id);
```

写入时机：

- loader event trigger 匹配并创建 run 时，写入 `event_delivery(event_id, loader_id, trigger_id, run_id, status=run_started)`。
- `loaderRunHost.CallSessionRPC` 识别到 `linked_sandbox_id` 时，同时写 `event_id -> sandbox_id`。
- loader 派生 event 时，写入 `parent_event_id`。查询 `GET /api/events/:event_id/sandboxes` 时需要先按 `parent_event_id` 展开 descendant event，再汇总这些 event 的 `event_sandbox_link`。
- 如果业务希望原始 webhook event 直接查到派生 event 创建的 sandbox，查询层负责 descendant 展开，不要求每个 descendant 写多份 ancestor link。
- 手动 run 没有 event_id 时不写该表。

## Loader API

Loader runtime 提供：

```js
scheduler.event.publish(topic, payload)
```

语义：

- `topic` 必须符合 topic 策略。
- `payload` 必须是 JSON object。
- 只允许发布 `runtime.*`、`workflow.*`、`external.*`。
- 写入 Event log，`source=loader`，`publisher_type=loader`。
- 从当前触发事件继承 `correlationId` 和 `parent_event_id`。
- 手动运行且没有当前触发事件时，如果 payload 没有 `correlationId`，使用新事件自己的 `eventId`。
- 不直接调用 `LoaderBus`，由 `EventDispatcher` 统一分发。
- JS 调用返回 `{ eventId, sequence, topic, correlationId }`。
- validation 阶段不可用，会返回 `scheduler.event.publish is unavailable during validation`。

Go 内部 `agent-compose.*` 生命周期事件仍走直接 `LoaderBus.Publish` 路径，不进入 `event`。因此不能假设所有 `agent-compose.*` 事件都能通过 `/api/events` 查询到。

## 错误响应

| 条件 | 响应 |
|---|---|
| 没有匹配的 webhook source | `404 Not Found` |
| token 缺失或错误 | `401 Unauthorized` |
| topic 为空、非法或前缀不允许 | `400 Bad Request` |
| `Content-Type` 不是 JSON | `415 Unsupported Media Type` |
| body 不是合法 JSON object | `400 Bad Request` |
| body 超过大小限制 | `413 Payload Too Large` |
| 幂等键重复但 payload hash 不一致 | `409 Conflict` |
| Event log 写入失败 | `500 Internal Server Error` |
| `GET /api/events/:event_id` 未找到事件 | `404 Not Found` |
| 查询参数非法或缺少查询边界 | `400 Bad Request` |
