# `agent-compose.yml` 配置手册

本文档说明当前代码实际接受的 `agent-compose.yml` / `agent-compose.yaml` 字段、默认值、约束和使用方式。解析器采用严格字段校验：未知字段、重复字段以及类型不符都会报错，因此字段名必须与本文一致。

> 重要：项目级工作区只支持顶层复数键 `workspaces`。顶层 `workspace` 已不受支持，会被当作未知字段拒绝。Agent 选择某个工作区时仍使用单数键 `agents.<agent>.workspace`。

可以在应用配置前进行本地校验：

```bash
agent-compose config --quiet
agent-compose -f ./path/to/agent-compose.yml config
```

第一条命令只校验；第二条还会输出归一化后的配置，并对标记为 secret 的值进行脱敏。默认查找当前目录下的 `agent-compose.yml`，其次查找 `agent-compose.yaml`；两者同时存在时应使用 `-f/--file` 明确选择。

## 完整结构速览

下面的配置用于展示字段所在位置。它刻意包含了较多能力，实际项目只需保留需要的部分。

```yaml
name: review-pipeline

env_file:
  - .env
  - .env.local

variables:
  DISPLAY_NAME: review-pipeline
  CONTROL_TOKEN:
    value: ${CONTROL_TOKEN}
    secret: true

workspaces:
  source:
    provider: file
    path: .
  upstream:
    provider: git
    url: https://github.com/example/project.git
    ref: main
    target: .

mcp_servers:
  local-tools:
    type: local
    command: npx
    args: ["-y", "@example/mcp-server"]
    env:
      API_TOKEN:
        value: ${MCP_API_TOKEN}
        secret: true
  issue-tracker:
    type: remote
    transport: http
    url: ${ISSUE_TRACKER_MCP_URL}
    headers:
      Authorization:
        value: Bearer ${ISSUE_TRACKER_TOKEN}
        secret: true

volumes:
  cache:
    name: review-cache
    driver: local
    labels:
      purpose: agent-cache
    options: {}

agents:
  reviewer:
    enabled: true
    provider: codex
    model: ${REVIEW_MODEL}
    system_prompt: |
      Review changes carefully and report concrete evidence.
    image: ghcr.io/chaitin/agent-compose-guest:latest
    build:
      context: .
      dockerfile: guest-images/Dockerfile.agent-compose-guest
      target: runtime
      args:
        CHANNEL: stable
      platforms: [linux/amd64]
      tags: [review-agent:latest]
      no_cache: false
      pull: true
    driver:
      docker: {}
    env:
      LOG_LEVEL: info
      SERVICE_TOKEN:
        value: ${SERVICE_TOKEN}
        secret: true
    mcp_servers:
      - local-tools
      - name: audit-api
        type: remote
        transport: sse
        url: https://mcp.example.com/sse
        headers:
          Authorization:
            value: Bearer ${AUDIT_TOKEN}
            secret: true
    capset_ids:
      - engineering
    skills:
      - ./skills/review
      - name: release-check
        provider: git
        url: https://github.com/example/agent-skills.git
        path: skills/release-check
        ref: main
    volumes:
      - cache:/cache
      - type: bind
        source: ./reports
        target: /workspace/reports
        read_only: false
    workspace:
      name: source
    scheduler:
      enabled: true
      sandbox_policy: sticky
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: Review the current workspace.
          sandbox_policy: new
    jupyter:
      enabled: false
      guest_port: 8888

```

## 通用规则

### 严格解析

- 顶层和各对象中的未知字段会报 `unknown field`，不会被静默忽略。
- 同一映射中的重复字段会报 `duplicate field`。
- 布尔字段必须是布尔值，列表字段必须是 YAML sequence，映射字段必须是 YAML mapping。
- 项目名、Agent 名、顶层 Workspace 键、MCP 名、Volume 键和最终 Skill 名使用稳定标识符格式：`^[a-z][a-z0-9_-]*$`。
- 路径和 URL 的相对基准因字段而异，具体见对应章节。

### 环境变量来源和优先级

`env_file` 决定 `${NAME}` 插值时可读取哪些 dotenv 文件。加载顺序如下：

1. 按 `env_file` 列表顺序加载，后面的文件覆盖前面的同名值。
2. 运行 `agent-compose` CLI 的进程环境最后覆盖 dotenv 文件。

若未配置 `env_file`，CLI 先查配置文件所在目录的 `.env`，不存在时再查当前工作目录的 `.env`。显式写 `env_file: []` 表示不自动加载任何 dotenv 文件。显式文件不存在、不可读或列表中有空路径都会失败。

`${NAME}` 只支持简单形式，不支持 shell 的 `${NAME:-default}`、命令替换或递归展开。引用的变量不存在时配置校验失败。

当前支持插值的位置包括：

- `variables.*.value`
- `agents.*.model`
- `agents.*.env.*.value`
- 项目级和 Agent 内联 MCP 的 `url`、`env.*.value`、`headers.*.value`
- Skill 的 `name`、`source`、`url`、`path`、`ref`、`username`

其他字符串字段不会自动插值，例如 `name`、`provider`、`image`、`system_prompt`、Workspace 字段、Build 字段和 Scheduler 字段。

### 环境值的两种写法

`variables`、`agents.*.env`、MCP `env` 和 MCP `headers` 都使用同一个值结构：

```yaml
PLAIN_VALUE: hello
SECRET_VALUE:
  value: ${SECRET_VALUE}
  secret: true
```

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `value` | string | `""` | 实际值；在受支持的位置执行 `${NAME}` 插值。 |
| `secret` | bool | `false` | 标记敏感值。规范化配置输出会显示 `********`，运行时仍使用真实值。 |

`secret` 是脱敏元数据，不会自行从环境读取值；仍需在 `value` 中写 `${NAME}`。

## 顶层字段

| 字段 | 类型 | 必填 | 作用 |
| --- | --- | --- | --- |
| `name` | string | 条件必填 | 项目标识。省略时尝试使用配置文件所在目录名；最终必须符合稳定标识符格式。也可由 CLI `--project-name` 覆盖。 |
| `env_file` | string 或 string[] | 否 | 指定插值使用的 dotenv 文件。相对路径以配置文件目录为基准。 |
| `variables` | map | 否 | 项目级命名变量及 secret 元数据，写入规范化项目配置。它们不会自动继承到 Agent 的 `env`，也不会成为其他 `${NAME}` 的变量来源。 |
| `workspaces` | map | 否 | 可复用的项目级 Workspace 定义。只能使用复数形式。 |
| `mcp_servers` | map | 否 | 可由 Agent 按名称引用的 MCP Server 定义。 |
| `volumes` | map | 否 | 项目管理或引用的持久 Volume。 |
| `agents` | map | 否 | Agent 定义；map key 是 Agent 名。 |

### `name`

```yaml
name: code-review
```

允许小写字母开头，后续可包含小写字母、数字、`_` 和 `-`。例如 `review-v2` 合法，`Review`、`2-review` 和包含空格的名称不合法。项目身份同时受规范化的配置文件路径影响，因此同名但来自不同路径的项目可被区分。

### `env_file`

单文件可以写标量：

```yaml
env_file: .env.production
```

多文件写列表：

```yaml
env_file:
  - .env
  - .env.production
```

### `variables`

```yaml
variables:
  REGION: cn-hangzhou
  RELEASE_TOKEN:
    value: ${RELEASE_TOKEN}
    secret: true
```

`variables` 当前用于保存项目级配置值和脱敏语义。若某个值要传入 sandbox，仍需在对应 Agent 的 `env` 中声明。

## `workspaces`：项目级工作区

顶层必须使用 `workspaces`：

```yaml
workspaces:
  source:
    provider: file
    path: .
```

以下旧写法无效：

```yaml
# 错误：顶层 workspace 会被严格解析器拒绝
workspace:
  provider: file
  path: .
```

每个 `workspaces.<key>` 支持：

| 字段 | 类型 | 适用范围 | 作用 |
| --- | --- | --- | --- |
| `name` | string | 兼容字段 | 顶层条目的实际名称由 map key 决定；通常不要重复填写。 |
| `provider` | string | 必填 | `file` 或 `git`。 |
| `url` | string | `git` 必填 | Git clone URL；`file` 不允许设置。 |
| `ref` | string | `git` 可选 | Git branch、tag 或 commit。 |
| `path` | string | `file` 必填 | 相对于 compose 文件目录的来源路径，不可逃逸项目根目录；Git Workspace 不支持仓库内子目录。 |
| `target` | string | 可选 | sandbox workspace 根目录下的目标目录，默认 `.`。 |
| `username` | string | `git` 可选 | Git HTTP 用户名。 |
| `password` | string | `git` 可选 | Git 密码，只允许完整环境引用 `${NAME}`。 |
| `token` | string | `git` 可选 | Git token，只允许完整环境引用 `${NAME}`。 |

本地 Workspace 在每次项目 run 创建时被复制为隔离快照，Agent 对快照的修改不会写回源目录。

```yaml
workspaces:
  source:
    provider: file
    path: ./src
  release-branch:
    provider: git
    url: https://github.com/example/service.git
    ref: release
    target: .
```

Workspace 选择规则：

- 顶层 `workspaces` 只是命名定义，不会自动分配给任何 Agent。
- Agent 省略 `workspace` 时，无论顶层定义了多少个 Workspace，该次 run 都不配置 Workspace。
- Agent 需要使用顶层 Workspace 时，必须通过 `workspace.name` 显式引用，或定义内联 Workspace。
- 显式的空对象 `workspace: {}` 非法；不使用 Workspace 时应省略该 key。

## `mcp_servers`：项目级 MCP

项目级 MCP 是命名映射，名称须符合稳定标识符格式。支持 `local` 和 `remote` 两类。

### Local MCP

```yaml
mcp_servers:
  filesystem:
    type: local
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    env:
      NODE_ENV: production
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string | 是 | 必须为 `local`。 |
| `command` | string | 是 | sandbox 内启动 MCP Server 的命令。 |
| `args` | string[] | 否 | 命令参数；空项和重复项会在规范化时去除。 |
| `env` | map | 否 | 进程环境，支持值对象和 `${NAME}` 插值。 |
| `transport` | string | 禁止 | Local MCP 不接受该字段的非空值。 |
| `url` | string | 禁止 | Local MCP 不接受 URL。 |
| `headers` | map | 禁止 | Local MCP 不接受 HTTP headers。 |

### Remote MCP

```yaml
mcp_servers:
  docs:
    type: remote
    transport: sse
    url: https://mcp.example.com/sse
    headers:
      Authorization:
        value: Bearer ${MCP_TOKEN}
        secret: true
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string | 是 | 必须为 `remote`。 |
| `transport` | string | 是 | `sse` 或 `http`。 |
| `url` | string | 是 | 远程 MCP 地址，支持 `${NAME}` 插值。 |
| `headers` | map | 否 | 请求头，支持值对象、secret 和插值。 |
| `command` | string | 禁止 | Remote MCP 不执行本地命令。 |
| `args` | string[] | 禁止 | Remote MCP 不接受命令参数。 |
| `env` | map | 禁止 | Remote MCP 不接受进程环境。 |

项目级 MCP 不会自动注入所有 Agent。Agent 必须在自己的 `mcp_servers` 中引用或定义需要的 Server。

## `volumes`：项目级 Volume

```yaml
volumes:
  cache: {}
  shared-data:
    name: existing-data
    driver: local
    external: true
    labels:
      owner: platform
    options:
      tier: fast
```

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `name` | string | 由项目和 key 派生 | 指定底层 Volume 名。 |
| `driver` | string | `local` | Volume driver；当前只支持 `local`。 |
| `external` | bool | `false` | 为 `true` 时引用已经存在的 Volume，而不是由项目创建。 |
| `labels` | map[string]string | 空 | 附加标签。key/value 会去除首尾空白。 |
| `options` | map[string]string | 空 | 传给 local Volume driver 的选项。 |

Volume map key 必须符合稳定标识符格式。Agent 通过 `agents.<name>.volumes` 挂载项目 Volume。

## `agents.<name>`

`agents` 是以 Agent 名为 key 的映射：

```yaml
agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
```

支持的字段如下：

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 是否启用 Agent。禁用后定义保留但不可按正常流程运行，Scheduler 也不会启用。 |
| `display_name` | string | 空 | Agent 的可读显示名称。 |
| `description` | string | 空 | Agent 职责的可读说明。 |
| `provider` | string | `codex` | Agent CLI/provider：`codex`、`claude`、`gemini` 或 `opencode`。兼容别名会在持久化边界归一化。 |
| `model` | string | provider/daemon 默认 | 模型名；支持 `${NAME}` 插值。 |
| `system_prompt` | string | 空 | 附加的系统提示，适合使用 YAML `|` 多行标量。 |
| `image` | string | daemon 默认镜像 | Guest 镜像引用，也会作为 `build` 的一个输出 tag。 |
| `build` | string/object | 无 | `agent-compose build` 使用的镜像构建配置。 |
| `driver` | object | Docker | 运行时 driver，必须且只能选择一个 key。 |
| `env` | map | 空 | 注入 sandbox 的环境变量。 |
| `mcp_servers` | scalar/object/list | 空 | 引用项目级 MCP，或声明 Agent 专属 MCP。 |
| `capset_ids` | string[] | 空 | 允许该 Agent sandbox 使用的 OctoBus capability set ID。 |
| `skills` | list | 空 | 注入 Agent 的 Skill 来源。 |
| `volumes` | list | 空 | Volume 或 bind mount 列表。 |
| `workspace` | object | 无 | 显式引用一个顶层 `workspaces` 条目，或定义 Agent 内联 Workspace。 |
| `scheduler` | object | 无 | 自动触发 Agent 的 Scheduler。 |
| `jupyter` | object | disabled | Agent run 的 Jupyter 默认配置。 |

### `enabled`、`provider`、`model` 和 `system_prompt`

```yaml
agents:
  reviewer:
    enabled: true
    provider: claude
    model: ${CLAUDE_MODEL}
    system_prompt: |
      Focus on correctness, security, and regression risk.
```

Provider 支持 `codex`、`claude`、`gemini` 和 `opencode`。当前兼容归一化还接受 `claude-code` / `claude_code`、`gemini-cli` / `gemini_cli`、`open-code` / `open_code`，新配置建议使用规范名称。

### `image`

```yaml
agents:
  reviewer:
    image: ghcr.io/chaitin/agent-compose-guest:latest
```

运行时会确保所选 driver 能使用该镜像。若同时配置 `build`，`image` 也会加入构建 tag；若两者均未提供 tag，执行 `agent-compose build` 会失败。

GitHub CI 当前只向 GHCR 发布以下两个多平台镜像：

| 镜像 | 用途 | Dockerfile | 平台 |
| --- | --- | --- | --- |
| `ghcr.io/chaitin/agent-compose` | 控制面 daemon | `Dockerfile` | `linux/amd64`、`linux/arm64` |
| `ghcr.io/chaitin/agent-compose-guest` | Sandbox guest runtime | `guest-images/Dockerfile.agent-compose-guest` | `linux/amd64`、`linux/arm64` |

Agent 的 `image` 应使用 `ghcr.io/chaitin/agent-compose-guest:<tag>`。daemon 镜像用于部署控制面，不能作为 guest 镜像使用。CI 不发布 BoxLite-only、Microsandbox-only 或其他 driver 专用 guest 镜像。

### `build`

短写法只指定 context：

```yaml
build: ./guest
```

完整写法：

```yaml
build:
  context: ./guest
  dockerfile: Dockerfile
  target: runtime
  args:
    VERSION: "1.2.3"
  platforms:
    - linux/amd64
  tags:
    - example/guest:latest
  no_cache: false
  pull: true
```

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `context` | string | `.` | Build context；相对路径以 compose 文件目录为基准。 |
| `dockerfile` | string | `Dockerfile` | Dockerfile 路径，由镜像构建后端解释。 |
| `target` | string | 空 | 多阶段构建 target。 |
| `args` | map[string]string | 空 | Build args；key 去除首尾空白且不能为空。 |
| `platforms` | string[] | 空 | 目标平台，格式为 `os/arch`；当前最多一个。 |
| `tags` | string[] | 空 | 输出镜像 tag，可与 Agent `image` 以及 CLI `--tag` 合并。 |
| `no_cache` | bool | `false` | 禁用构建缓存。 |
| `pull` | bool | `false` | 构建时拉取较新的基础镜像。 |

当前 `agent-compose build` 使用 Docker daemon image store。CLI 同名选项可以覆盖或追加 YAML 配置。

### `driver`

省略 `driver` 时默认为：

```yaml
driver:
  docker: {}
```

必须且只能选择一个 runtime：

```yaml
driver:
  docker:
    host: ""
```

或：

```yaml
driver:
  boxlite:
    kernel: ""
    rootfs: ""
```

或：

```yaml
driver:
  microsandbox:
    profile: secure
```

| Driver | 子字段 | 当前状态 |
| --- | --- | --- |
| `docker` | `host` | 支持的稳定 driver。`host` 被解析和保留；daemon 的 Docker 边界仍由部署配置决定。 |
| `boxlite` | `kernel`, `rootfs` | Linux 构建可编译支持；运行时初始化是惰性的。子字段会去除首尾空白。 |
| `microsandbox` | `profile` | Linux 构建可编译支持；运行时初始化是惰性的。`profile` 会去除首尾空白。 |
| `firecracker` | `kernel`, `rootfs` | 仅保留在解析 schema 中；当前规范化会明确报 `unsupported runtime driver firecracker`，不可使用。 |

为完整说明 schema，下面的结构可以被解析器识别，但当前会在规范化阶段失败：

```yaml
# 当前实现中无效。
driver:
  firecracker:
    kernel: /path/to/kernel
    rootfs: /path/to/rootfs
```

即使 schema 支持某个 driver，当前二进制也必须编译了该 driver。可通过 `agent-compose --json version` 的 `compiled_drivers` 查看；这不代表 KVM、Docker daemon 或运行时制品健康。

### `env`

```yaml
env:
  LOG_LEVEL: debug
  API_TOKEN:
    value: ${API_TOKEN}
    secret: true
```

这些值进入 Agent sandbox。相同名称的空项会在后续边界归一化；`secret: true` 控制展示脱敏。

### `mcp_servers`

引用一个项目级 MCP：

```yaml
mcp_servers: filesystem
```

引用多个：

```yaml
mcp_servers:
  - filesystem
  - issue-tracker
```

定义 Agent 专属 MCP：

```yaml
mcp_servers:
  - name: private-tools
    type: local
    command: private-mcp
    args: ["serve"]
```

内联对象支持 `name`、`type`、`transport`、`command`、`args`、`env`、`url` 和 `headers`，其 local/remote 约束与项目级 MCP 相同。内联 MCP 必须提供 `name`；同一 Agent 内不能重复声明同名内联 MCP。重复引用同一个项目级 MCP 会去重。

### `capset_ids`

```yaml
capset_ids:
  - engineering
  - ticketing
```

列表会去除空值和重复值。配置了 capability gateway 时，这些 ID 用于将 sandbox 限制到对应 OctoBus capability set，并注入访问所需的环境和能力说明；gateway 未配置或能力说明获取失败时采用 best-effort 行为并产生 warning。

### `skills`

Skill 目录必须包含有效的 `SKILL.md`。provider 支持 `file`、`http` 和 `git`；同一 Agent 的最终 Skill 名不可重复。ZIP 是内容格式，不是 provider。

本地目录：

```yaml
skills:
  - name: review
    provider: file
    path: ./skills/review
```

Git 来源：

```yaml
skills:
  - name: review
    provider: git
    url: https://github.com/example/skills.git
    path: review
    ref: v1.0.0
    username: ${GIT_USERNAME}
    token: ${GIT_TOKEN}
```

远程 ZIP：

```yaml
skills:
  - name: review
    provider: http
    url: https://downloads.example.com/review.zip
    format: zip
    path: review
```

| 字段 | 类型 | 作用 |
| --- | --- | --- |
| `name` | string | Skill 名；省略时从 path/URL 推导，最终必须符合稳定标识符格式。 |
| `provider` | string | 必填，支持 `file`、`http` 或 `git`。 |
| `url` | string | `http` 和 `git` 必填。 |
| `path` | string | `file` 的本地路径；Git 或 ZIP 内容内的子目录。相对 file 路径以 compose 文件目录为基准。 |
| `ref` | string | Git branch、tag 或 commit。 |
| `format` | string | 可选内容格式，目前只支持 `zip`；HTTP Skill 必须设置。 |
| `username` | string | HTTP/Git 用户名，可插值。 |
| `password` | string | HTTP/Git 密码，只允许完整环境引用 `${NAME}`。 |
| `token` | string | HTTP/Git token，只允许完整环境引用 `${NAME}`。 |

`password` 和 `token` 不允许明文。它们在 Skill 解析阶段通过 daemon 环境解析，避免把凭证展开后写入项目规范。远程 ZIP 下载限制为 HTTP(S)，并执行大小、压缩包和网络地址安全检查。

Git ref 会在各自业务生命周期中解析：Skill 在 Agent run 时解析，Workspace 在 sandbox provisioning 时解析，Scheduler 来源在 `config`/`up` 时解析并保存脚本快照。因此 moving branch 在三处可能得到不同 commit；需要严格一致时，应在 `ref` 中直接填写 commit SHA。

### `volumes`

短写格式是 `source:target[:ro|rw]`：

```yaml
volumes:
  - cache:/cache
  - ./reports:/workspace/reports:ro
```

长写格式：

```yaml
volumes:
  - type: volume
    source: cache
    target: /cache
    read_only: false
  - type: bind
    source: ./reports
    target: /workspace/reports
    read_only: true
```

| 字段 | 类型 | 必填 | 作用 |
| --- | --- | --- | --- |
| `type` | string | 否 | `volume` 或 `bind`。省略时：source 命中顶层 Volume、是绝对路径或以 `.` 开头时可推断，否则按 Volume。 |
| `source` | string | 是 | 顶层 Volume key/有效 Volume 名，或 bind 的 host source。 |
| `target` | string | 是 | Guest 内绝对路径。 |
| `read_only` | bool | 否 | 只读挂载，默认 `false`。 |

同一 Agent 不能将多个条目挂到相同 `target`。短写使用 `:` 分隔，因此不适合包含冒号的 source/target，此时应使用长写。

### `workspace`：Agent 选择或内联工作区

这里是 Agent 内部的单数 `workspace`，与顶层复数 `workspaces` 不是同一个层级。

引用顶层条目：

```yaml
workspace:
  name: source
```

内联本地 Workspace：

```yaml
workspace:
  provider: file
  path: ./src
```

内联 Git Workspace：

```yaml
workspace:
  provider: git
  url: https://github.com/example/project.git
  ref: main
  target: .
```

若 `name` 与任一来源字段或 `target` 同时出现，该对象按内联 Workspace 处理，而不是从顶层继承后局部覆盖。需要复用时只写 `name`。

### `scheduler`

Scheduler 可以使用声明式 `triggers`，也可以使用 JavaScript `script`；两者互斥。

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 是否启用该 Agent 的 Scheduler。禁用 Agent 也会使其 Scheduler 无效。 |
| `sandbox_policy` | string | `new` | Scheduler 默认 sandbox 策略：`new` 或 `sticky`。 |
| `concurrency_policy` | string | `skip` | 整个 Agent Scheduler 的重叠运行策略：`skip` 或 `parallel`。 |
| `triggers` | list | 空 | 声明式触发器。 |
| `script` | string/object | 空 | 内联 JavaScript，或扁平的 `file`/`http`/`git` 来源配置。不能和 `triggers` 同时使用。 |

`new` 为每次调用创建新 sandbox；`sticky` 允许 Scheduler 绑定并复用 sandbox。单个 Trigger 的 `sandbox_policy` 可覆盖执行 Agent 时的策略。

`concurrency_policy` 作用于整个 Agent Scheduler，包括全部声明式或脚本注册的 Trigger，以及手动 Scheduler 调用。`skip` 会把与同一 Scheduler 既有运行重叠的新 run 记录为 `skipped`，且不会排队补跑；`parallel` 允许重叠 run 并行执行。它不是 Trigger 级策略。

#### 声明式 Trigger

每个 Trigger 必须且只能写一个 kind 字段：`cron`、`interval`、`timeout` 或 `event`。

```yaml
scheduler:
  enabled: true
  sandbox_policy: sticky
  concurrency_policy: parallel
  triggers:
    - name: nightly
      cron: "0 2 * * *"
      prompt: Run the nightly review.
    - name: heartbeat
      interval: 30m
      prompt: Check service health.
    - name: startup-once
      timeout: 15s
      prompt: Perform the startup check.
      sandbox_policy: new
    - name: webhook-review
      event:
        topic: webhook.github.push
      prompt: Review the pushed changes.
```

| 字段 | 类型 | 必填 | 作用 |
| --- | --- | --- | --- |
| `name` | string | 否 | 可读的稳定名称；同一 Scheduler 中非空名称不可重复。省略时 ID 会结合列表位置稳定派生。 |
| `cron` | string | 四选一 | 5 字段 cron，支持可选秒字段和 robfig/cron descriptor。声明式 cron trigger 使用 UTC。 |
| `interval` | duration | 四选一 | 周期，例如 `30s`、`5m`、`2h`；必须大于 0，实际注册精度至少 1ms。 |
| `timeout` | duration | 四选一 | 一次性延迟，例如 `15s`；必须大于 0，实际注册精度至少 1ms。 |
| `event.topic` | string | 四选一 | 内层 `topic` 是订阅的非空 topic，可使用如 `webhook.github.push`。 |
| `prompt` | string | 否 | 触发后发送给 Agent 的 prompt；空值默认为 `Run agent <name>.`。 |
| `sandbox_policy` | string | 否 | 本次 Agent 调用使用 `sticky` 或 `new`；省略时不在生成的调用中显式覆盖。 |

#### 内联脚本

```yaml
scheduler:
  enabled: true
  script: |
    scheduler.interval("review", async function () {
      return scheduler.agent("Review the workspace.");
    }, 60000);
```

脚本由 Scheduler runtime 验证，并从脚本注册结果获取触发器。

#### 外部脚本

本地文件：

```yaml
scheduler:
  enabled: true
  script:
    provider: file
    path: ./scheduler.js
```

HTTP URL：

```yaml
scheduler:
  enabled: true
  script:
    provider: http
    url: https://example.com/scheduler.js
```

外部脚本 mapping 使用与 Skill、Workspace 相同的来源字段：

- File：`provider: file` 配合 `path`，相对路径以 compose 文件目录为基准。
- HTTP：`provider: http` 配合 `url`，可选认证字段。
- Git：`provider: git` 配合 `url`、可选 `ref` 和必填的仓库内 `path`。

应用项目时 CLI 会读取脚本并将内容快照保存到项目规范，而不是让 daemon 以后重新读取来源。HTTP 读取限制包括 10 秒超时、最大 1 MiB、最多 5 次 redirect、UTF-8 校验；HTTPS 不允许降级 redirect 到 HTTP，URL userinfo 不允许使用。

### `jupyter`

```yaml
jupyter:
  enabled: true
  guest_port: 8888
```

| 字段 | 类型 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Agent run 未用 CLI 显式覆盖时，是否启用 Jupyter。 |
| `guest_port` | int | daemon `JUPYTER_GUEST_PORT` | Guest 内监听端口。`0` 使用 daemon 默认；显式值必须在 1–65535。 |

设置 `guest_port` 但保持 `enabled: false` 会保留端口配置，但默认不启动 Jupyter。CLI `run --jupyter` 可以为单次运行启用。

## 常见错误与迁移提示

### 顶层误写 `workspace`

错误：

```yaml
workspace:
  provider: file
  path: .
```

正确：

```yaml
workspaces:
  source:
    provider: file
    path: .

agents:
  reviewer:
    workspace:
      name: source
```

### 同时配置 Scheduler script 和 triggers

二者互斥。把所有注册逻辑放入 `script`，或完全使用声明式 `triggers`。

### 以为 `variables` 会自动进入 sandbox

`variables` 不会继承到 Agent。需要显式写：

```yaml
variables:
  API_URL: https://api.example.com

agents:
  reviewer:
    env:
      API_URL: https://api.example.com
```

如果目标是复用部署环境值，可在两个位置都写 `${API_URL}`，并通过 `env_file` 或 CLI 进程环境提供。

### 以为项目 Workspace 会被自动选择

顶层 `workspaces` 不会被自动选择。Agent 省略 `workspace` 时，即使项目只定义了一个条目，也不会配置 Workspace。需要使用时应设置 `workspace.name` 或内联 Workspace；不使用时应省略该 key，而不是写 `workspace: {}`。

### 选择未编译或运行条件不满足的 driver

配置 schema 通过不代表 runtime 一定可启动。BoxLite/Microsandbox 只在 Linux 完整构建中编译，并依赖 KVM 和对应运行时制品；Docker 需要可达的 Docker daemon。

## 最小配置示例

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

验证并应用：

```bash
agent-compose config --quiet
agent-compose up
agent-compose run reviewer --prompt "Review this project."
```
