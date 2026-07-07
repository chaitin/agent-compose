# 资源标识与 CLI 展示设计

英文版本：[../../design/resource_identity_cli_design.md](../../design/resource_identity_cli_design.md)

状态：设计方案。本文描述 agent-compose 面向 CLI 的资源标识模型和输出形态，本文本身不代表已经调整代码。

示例使用脱敏后的项目相对路径：

- `agent-compose/examples/docker-minimal/agent-compose.yml`
- `agent-compose/examples/docker-scheduler-timeout/agent-compose.yml`

## 摘要

agent-compose CLI 输出应对齐 Docker/Compose 的使用体验：

- 预定义资源使用用户写下的 name 作为标识；如果 `agent-compose.yml` 未声明 `name`，使用配置文件所在目录名作为 project name。
- 运行时资源使用不透明 ID 标识。默认 CLI 输出展示 12 位短 ID；`--verbose` 和 `--json` 可以展示完整值。
- Image 保持 Docker/OCI 语义，包括 image ID、tag、repo digest 和 `sha256:`。
- CLI 不再把资源类型、name 和 hash 片段自动拼接成对外 ID。
- CLI、UI 和对外交互接口统一使用 Sandbox 概念。

核心是区分“人写的名字”和“机器生成的不透明身份”：

- 有 name 的资源，`name` 是其作用域内的公开唯一标识。
- 不透明身份使用 `id` 表示完整 `sha256:` 值，使用 `short_id` 表示 `id` 中 hash 的前 12 位。
- 如果有 name 的资源内部也需要不透明身份用于存储、冲突处理或排障，可以在 `--verbose` 或 `--json` 中展示，但默认 CLI 优先展示 name。

## 资源分类

### 预定义资源

预定义资源来自 `agent-compose.yml` 或 project 目录。

| 资源 | 公开标识 | 默认展示 | 其他可用标识 |
| --- | --- | --- | --- |
| Project | `name`；缺省时使用配置目录名 | project name | verbose/JSON 中的 `id`、`short_id` |
| Agent | 当前 project 下的 `agents.<name>` | agent name | verbose/JSON 中的 `id`、`short_id` |
| Scheduler | project 下的调度器资源 | 默认不作为独立命名资源展示 | verbose/JSON 中的 scheduler `id`、`short_id` |
| Trigger | 声明式 trigger 的 `name`；脚本注册 trigger 使用内部 ID 或序号 | trigger name 或 trigger short ID | verbose/JSON 中的 `id` |

Project 示例：

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
```

在这个例子中：

- Project 标识：`docker-minimal`
- 当前 project 下的 Agent 标识：`reviewer`
- 二者都不应以自动拼接 ID 作为默认展示或默认输入。

### Scheduler 与 Trigger 作用域

`agent-compose scheduler ls` 的作用域是当前 project。当前 CLI 语义下，一个 project 只有一个 scheduler 资源，但这个 scheduler 可以有多个 trigger。

Trigger 来源有两类：

- `agent-compose.yml` 中声明式配置的 trigger，通常带 `name`。
- loader script 运行时注册的 trigger，通常没有用户定义 name，紧凑展示时使用内部 trigger ID 或稳定序号。

因此，`scheduler ls` 默认应展示 trigger 列表，而不是强调 scheduler ID。Scheduler ID 仍然可用于排障和机器输出，应放在 `--verbose` 和 `--json` 中。

### 运行时资源

运行时资源由执行过程产生，不再生成可读拼接 ID。

| 资源 | 公开标识 | 默认展示 | 其他可用标识 |
| --- | --- | --- | --- |
| Run | 不透明 run `id` 或 `short_id` | 短 run ID + project/agent/status | JSON 中的 `id` |
| Sandbox | 不透明 sandbox `id` 或 `short_id` | 短 sandbox ID + project/agent/status | JSON 中的 `id` |
| Cache | 不透明 cache `id` 或 `short_id` | 短 cache ID + type/ref/path | JSON 中的 `id` |

示例：

```text
Run ID:     103f88fea811
Sandbox ID: c5582b466ada
Cache ID:   8b42ac739d10
```

不要把运行时资源展示为由资源类型、project name、agent name 和 hash 片段拼接出的自动生成 ID。

### Image 资源

Image 使用 Docker/OCI 身份模型。CLI 不包装、不重命名 image ID。

紧凑文本输出：

```text
IMAGE ID      REF                         STATUS
e67e6413b80b  agent-compose-guest:latest  available
```

JSON 输出保留完整值：

```json
{
  "image_id": "sha256:e67e6413b80b665a4ca89279a67d709e77ee50640b3d267b568d379d211c9a8b",
  "short_id": "e67e6413b80b",
  "image_ref": "agent-compose-guest:latest"
}
```

## 展示规则

默认 CLI 输出应紧凑，并服务于下一步操作。

| 输出模式 | 规则 |
| --- | --- |
| 默认文本 | 预定义资源展示 name；运行时资源展示 12 位短 ID。 |
| `--verbose` | 增加完整路径、scheduler ID、完整 hash、时间戳和排障元数据。 |
| `--json` | 当前资源对象内统一使用 `id`、`name`、`short_id` 字段；不截断 `sha256:`。 |

除非有明确的版本化 API 变更，否则不要删除现有 JSON 字段。但 `project-<name>-<hash12>` 这类自动拼接 ID 不具备公开标识价值，应彻底移除，不作为兼容字段保留。Revision 属于内部实现细节，默认文本输出不展示；`--verbose` 和 `--json` 可以展示 revision 字段用于排障和自动化。

## CLI 参数解析规则

每个命令只在自己的命令作用域中解析参数。

| 命令 | 作用域 | 推荐输入 |
| --- | --- | --- |
| `agent-compose ls` | daemon 全局 | 无目标 |
| `agent-compose up/down` | 当前 compose project | 配置路径或 project name |
| `agent-compose inspect project` | daemon 全局或当前配置 | project name；`id`/`short_id` 作为补充 |
| `agent-compose inspect agent` | 当前 project | agent name；`id`/`short_id` 作为补充 |
| `agent-compose scheduler ls` | 当前 project | 无目标，可选 agent 过滤 |
| `agent-compose scheduler inspect` | 当前 project | agent name + trigger name/ID |
| `agent-compose run` | 当前 project | agent name |
| `agent-compose ps/logs` | 默认当前 project | agent name、run ID、sandbox ID |
| `agent-compose inspect run` | 默认当前 project | run `id`/`short_id` |
| `agent-compose inspect sandbox` | 默认当前 project | sandbox `id`/`short_id` |
| `agent-compose images/cache` | daemon 级资源 | image ref/ID 或 cache `id`/`short_id` |

解析顺序：

1. 对有 name 的资源，先在作用域内精确匹配 name。
2. 精确匹配 `id`。
3. 唯一匹配短 ID 或 ID 前缀。
4. 必要时使用作用域名称，例如 project + agent 或 agent + trigger。
5. 如果匹配多个候选，返回 ambiguous 错误，并列出候选项。

## 兼容策略

CLI 应改善展示和解析体验，同时不破坏已有客户端。

| 现有字段 | 建议 |
| --- | --- |
| 带可读拼接文本的 `project_id` | 移除。Project 对象使用 `name`、`id`、`short_id`，不再需要 `project-<name>-<hash12>` 形式。 |
| `managed_agent_id` | 新 JSON 输出中不作为推荐字段暴露。Agent 对象使用 `name`、`id`、`short_id`。 |
| `scheduler_id` | 仅在其他资源对象引用 scheduler，或 verbose/debug 输出中保留。Scheduler 对象使用 `id`、`short_id`。 |
| `trigger_id` | 仅在其他资源对象引用 trigger 时保留。Trigger 对象使用 `id`，有声明式名称时使用 `name`，并提供 `short_id`。 |
| `run_id` | 仅在其他资源对象引用 run 时保留。Run 对象使用 `id`、`short_id`。 |
| `sandbox_id` | 仅在其他资源对象引用 sandbox 时保留。Sandbox 对象使用 `id`、`short_id`。 |
| `cache_id` | 仅在其他资源对象引用 cache 时保留。Cache 对象使用 `id`、`short_id`。 |
| `image_id` | 保持 Docker/OCI 语义。 |
| `spec_hash` | JSON 和 verbose 中保留完整 `sha256:`。 |

## 命令输出示例

下面展示建议输出形态。当前输出示例已简化，只保留和标识相关的字段。

### `agent-compose ls`

`ls` 是 daemon 全局命令。参考 Docker Compose，默认应展示 config file path。

建议形态：

```text
ID            NAME                      CONFIG FILE                                         AGENTS  SCHEDULERS
55521f60a3e9  docker-minimal            agent-compose/examples/docker-minimal/agent-compose.yml       1  0
92f42e13d913  docker-scheduler-timeout  agent-compose/examples/docker-scheduler-timeout/agent-compose.yml  1  1
```

Verbose 可以增加不透明 ID 和完整 hash：

```text
ID            NAME            CONFIG FILE                                      REVISION  SPEC HASH
55521f60a3e9  docker-minimal  agent-compose/examples/docker-minimal/agent-compose.yml  1  sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd
```

### `agent-compose up`

默认文本只输出动作表格。`ID` 放在第一列并展示 `short_id`，动作放在最后一列。

建议形态：

```text
ID            NAME            TYPE              ACTION
55521f60a3e9  docker-minimal  project           created
6a3d03099bc3  reviewer        agent             created
```

### `agent-compose inspect project docker-minimal --json`

JSON 在每个资源对象内使用 `id`、`name`、`short_id`。对象类型已经限定字段含义时，不再增加 `project_id`、`agent_id`、`agent_short_id` 等冗余字段。

```json
{
  "project": {
    "name": "docker-minimal",
    "id": "sha256:55521f60a3e9...",
    "short_id": "55521f60a3e9",
    "source_path": "agent-compose/examples/docker-minimal/agent-compose.yml",
    "current_revision": 1,
    "spec_hash": "sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd"
  },
  "agents": [
    {
      "name": "reviewer",
      "id": "sha256:6a3d03099bc3...",
      "short_id": "6a3d03099bc3",
      "provider": "codex",
      "image": "agent-compose-guest:latest",
      "driver": "docker"
    }
  ],
  "schedulers": []
}
```

这里的 `id` 是不透明值，不是可读拼接 ID。

### `agent-compose scheduler ls`

作用域：当前 project。默认输出关注 trigger。

建议紧凑形态：

```text
AGENT     TRIGGER                    KIND     SOURCE       ENABLED
reviewer  run-once-after-15-seconds  timeout  declarative  true
```

如果 trigger ID 可用且稳定：

```text
TRIGGER ID    AGENT     TRIGGER                    KIND     SOURCE       ENABLED
8f52c930d7a4  reviewer  run-once-after-15-seconds  timeout  declarative  true
```

Verbose：

```text
TRIGGER ID    AGENT     TRIGGER                    KIND     SOURCE       SCHEDULER ID   ENABLED
8f52c930d7a4  reviewer  run-once-after-15-seconds  timeout  declarative  cd228d46fd7d  true
```

### `agent-compose run reviewer --command 'echo ok' --keep-running`

前台输出保持命令输出：

```text
ok
```

Detached 输出应返回下一步可用的句柄：

```text
Run: 103f88fea811
Sandbox: c5582b466ada
Status: running
Logs: agent-compose logs --run 103f88fea811 --follow
```

### `agent-compose ps`

```text
SANDBOX ID    PROJECT         AGENT     STATUS   RUN ID        DRIVER  IMAGE
c5582b466ada  docker-minimal  reviewer  running  103f88fea811  docker  agent-compose-guest:latest
```

### `agent-compose logs --run 103f88fea811`

```text
reviewer-run-103f88fea811 [2026-07-07T10:15:30Z]| ok
```

`logs --json` 不使用这种展示拼接，字段应拆开表达，例如 `agent_name`、`run_id`、`run_short_id`、`time`、`content`。

### `agent-compose inspect run 103f88fea811 --json`

```json
{
  "id": "sha256:103f88fea811...",
  "short_id": "103f88fea811",
  "project_name": "docker-minimal",
  "agent_name": "reviewer",
  "status": "succeeded",
  "sandbox_id": "sha256:c5582b466ada...",
  "sandbox_short_id": "c5582b466ada",
  "exit_code": 0,
  "output": "ok\n",
  "driver": "docker",
  "image_ref": "agent-compose-guest:latest"
}
```

### `agent-compose inspect sandbox c5582b466ada --json`

```json
{
  "id": "sha256:c5582b466ada...",
  "short_id": "c5582b466ada",
  "project_name": "docker-minimal",
  "agent_name": "reviewer",
  "run_id": "sha256:103f88fea811...",
  "run_short_id": "103f88fea811",
  "status": "running",
  "driver": "docker",
  "image_ref": "agent-compose-guest:latest",
  "workspace_path": "agent-compose/data/sandboxes/c5582b466ada/workspace"
}
```

### `agent-compose images --query agent-compose-guest`

语义不变：

```text
IMAGE ID      REF                         STATUS     SIZE
e67e6413b80b  agent-compose-guest:latest  available  3277198228
```

### `agent-compose cache ls`

```text
CACHE ID      DRIVER  TYPE       STATUS      REMOVABLE  SIZE   REF
8b42ac739d10  docker  sandbox    referenced  false      789    c5582b466ada
4a19e01f64ca  docker  rootfs      unused      true       123M   agent-compose-guest:latest
```

## 迁移建议

1. 增加从不透明 ID 和 hash 提取 `short_id` 的统一工具。
2. 默认文本输出停止渲染自动拼接 ID。
3. JSON 输出对当前资源对象统一使用 `id`、`name`、`short_id`。
4. CLI 参数解析支持 name、精确 ID、唯一 ID 前缀。
5. Scheduler ID 移到 verbose/JSON，`scheduler ls` 默认展示 trigger。
6. CLI/UI/API 对外统一使用 Sandbox。
7. Image 输出继续对齐 Docker/OCI 行为。

## 风险

- 短 ID 匹配必须拒绝有歧义的前缀。
- 有 name 的资源只在作用域内唯一；agent 和 trigger 解析必须结合 project 上下文。
- 删除已有 JSON 字段需要单独的版本计划，优先做新增字段迁移。
- 运行时 ID 必须保持不透明，并且足够稳定以支持后续命令。
