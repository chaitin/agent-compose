# Coding Agent 订阅账号认证设计

## 1. 结论

本文对应 [Issue #562](https://github.com/chaitin/agent-compose/issues/562)。目标是让 Coding Agent 使用订阅账号或 API Key，并且所有模型请求仍经过 agent-compose daemon。

第一阶段支持两种订阅账号：

- OpenAI Codex：使用 ChatGPT Plus/Pro 登录；
- Anthropic Claude：使用 Claude Pro/Max 登录。

订阅账号使用厂商专用 OAuth 和请求约定，不等同于通用 Platform API：

- Sandbox 只持有 daemon 签发的 Facade token；
- OAuth access/refresh token 只保存在 daemon；
- daemon 通过对应 Connector 构造 endpoint、认证头和请求；
- `scheduler.llm` 和 `LLMService.Generate` 继续使用通用 API Key provider，不使用订阅账号。

现有系统只从 `.env` 启动一个默认 LLM 配置。新设计引入用户命名的 `Account`，使一个 daemon 可以保存多个订阅账号和 API Key，并由每个 Agent 显式选择。

## 2. 用户怎么使用

### 2.1 登录

默认入口只有一个：

```bash
agent-compose login
```

CLI 首先选择认证方式：

```text
How do you want to authenticate?
> Subscription account (browser login)
  API key
```

选择订阅账号后：

```text
Select subscription service:
> OpenAI Codex (ChatGPT Plus/Pro)
  Anthropic Claude (Pro/Max)
Account name (used in YAML): personal-codex

Open https://auth.openai.com/codex/device
Enter code: ABCD-EFGH
Waiting for authorization... done

Account "personal-codex" is ready.
```

`Account name` 是创建账号时的必填输入，由用户命名并在当前 daemon 内唯一，例如 `personal-codex`、`work-claude`。它使用稳定资源 ID 格式 `^[a-z][a-z0-9_-]*$`，之后直接写入 YAML，例如 `account: personal-codex`。用户名、密码和 MFA 只在上游浏览器页面输入，不经过 agent-compose。

选择 Anthropic 时，CLI 必须在授权前提示：第三方工具调用可能使用 Anthropic extra usage 并按 token 计费，不一定计入 Pro/Max 套餐额度。用户明确确认后才打开授权页面。

选择 API Key 后：

```text
Select API type:
> OpenAI Platform
  Anthropic API
  OpenAI-compatible API

Account name (used in YAML): company-openai
Credential source:
> Paste API key
  Daemon environment variable
API key: ********
```

API Key 使用无回显输入。选择 OpenAI-compatible 时额外填写 Base URL；选择环境变量时只填写 daemon 环境中的变量名。

常用管理命令：

```text
agent-compose login <account-id>       # 重新认证已有 Account
agent-compose logout <account-id>
agent-compose account list
agent-compose account show <account-id>
agent-compose account remove <account-id>
```

`logout` 适用于订阅账号和 API Key Account：它使 daemon 中的 credential 失效，但保留 Account 及 YAML 绑定，状态变为 `reauth_required`。OAuth Connector 在厂商支持时 best-effort 调用 revoke；不支持时明确提示“仅已退出本地 daemon”。环境变量仍存在也不能让已 logout 的 Account 自动恢复。再次执行 `login <account-id>` 可重新认证；`account remove` 才会删除 Account 配置。

`agent-compose auth` 仍表示 CLI 到 daemon 的认证，不改变原有含义。

交互向导只在 TTY 中启用。脚本可以显式提供 `account-id`、`--type` 和其他必要参数；managed API Key 只能通过无回显 prompt 或 stdin 提交，不能放在命令参数中。

### 2.2 登录过程

Codex 支持浏览器登录，并为 headless 环境提供 Device Code。Anthropic 使用浏览器 PKCE；本地 callback 不可达时允许用户粘贴最终 redirect URL。通常登录在 30 秒到 2 分钟内完成，最长不超过上游有效期，并设置 15 分钟上限。

登录 session 由 daemon 管理。CLI 退出后 Device Code session 可以继续到过期；Ctrl-C 会尝试取消。daemon 重启后未完成的 session 失效，用户重新登录即可。

## 3. YAML 怎么写

项目 YAML 只引用 Account 名称，不写认证类型、API Key 或 OAuth token。下面是本设计实施后的完整示例；当前版本尚不能使用 `account` 字段：

```yaml
name: multi-agent-project

agents:
  subscription-coder:
    provider: codex
    model: gpt-5.4
    account: personal-codex
    image: agent-compose-guest:latest
    driver:
      docker: {}

  api-coder:
    provider: codex
    model: gpt-5.4-mini
    account: company-openai
    image: agent-compose-guest:latest
    driver:
      docker: {}

  claude-reviewer:
    provider: claude
    model: claude-sonnet-4-5
    account: personal-claude
    image: agent-compose-guest:latest
    driver:
      docker: {}
```

三个字段的职责不同：

- `provider`：Sandbox 中运行哪个 Coding Agent CLI，例如 `codex`、`claude` 或 `pi`；
- `model`：该 Agent 请求的模型；
- `account`：daemon 使用哪个上游账号和凭证。

多个 Agent 可以共享同一个 Account，也可以分别使用不同账号。daemon 会拒绝已知的不兼容组合，例如 `provider: claude` 绑定 `codex-subscription`，或 `provider: codex` 绑定 `anthropic-subscription`。

兼容规则：

- 不写 `account` 时，继续使用当前 `.env`/默认 provider 行为；
- 当前 `.env` 配置对外表现为保留 Account `default`；
- 显式 Account 不存在、失效或不兼容时直接失败，不回退到其他账号或 `.env`；
- Account ID 由用户命名，项目可以在不同 daemon 上复用，只需提前创建同名 Account。

Compose schema 只增加一个标量：

```go
type AgentSpec struct {
    // existing fields...
    Account string `yaml:"account,omitempty" json:"account,omitempty"`
}
```

当前 `agents` 仍是以 Agent ID 为 key 的 map，不改成 `- name:` 列表。

## 4. 请求怎么走

```text
agent-compose login
  -> daemon 完成 OAuth/API Key 验证
  -> daemon 加密保存 credential

Sandbox Coding Agent
  -> 使用 Facade token 请求 daemon
  -> daemon 根据 Agent 的 account 解析 credential
  -> 必要时刷新 OAuth token
  -> daemon 请求上游模型服务
  -> 将响应流返回 Sandbox
```

核心边界：

1. Sandbox 永远拿不到上游 credential。
2. Facade token 只用于 Sandbox 到 daemon，OAuth token 只用于 daemon 到上游。
3. Facade token 绑定 Account 对应的内部 provider；token 刷新不需要重启 Sandbox。
4. Account 失效时请求明确返回 `reauth_required`，不能静默换账号。

Codex 和 Anthropic 订阅 Connector 只支持 Coding Agent Runtime Facade。它们不声明通用 Generate 能力，因此不会被 `scheduler.llm` 选中，也不会从 Agent 的 `account` 自动继承到 Scheduler。

## 5. Account 与数据兼容

### 5.1 Account 类型

Account 是面向用户的统一概念，ID 由用户定义，例如 `personal-codex`、`company-openai`。

| Account type | 用途 | Credential |
| --- | --- | --- |
| `codex-subscription` | ChatGPT Plus/Pro 的 Codex 服务 | OAuth |
| `anthropic-subscription` | Claude Pro/Max 的 Claude Code 服务 | OAuth |
| `openai-api` | OpenAI Platform API | API Key |
| `openai-compatible` | 企业或第三方 OpenAI-compatible API | API Key |
| `anthropic-api` | Anthropic Messages API | API Key |

Account type 由 CLI 选择结果确定，普通用户不需要在 YAML 中填写。内部 Connector 负责 endpoint、协议、认证头和能力判断，不暴露给普通用户。

### 5.2 数据库调整

首版让 Account ID 与现有 `llm_provider.id` 一对一，复用当前 provider routing 和 Facade token 的 `provider_id`，不额外创建只做转发的 Account 表。

新增 forward-only migration `000013_llm_accounts.sql`，不修改已经发布的 baseline：

```sql
ALTER TABLE llm_provider
ADD COLUMN account_type TEXT NOT NULL DEFAULT '';

ALTER TABLE llm_provider
ADD COLUMN connector_type TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_definition
ADD COLUMN llm_account_id TEXT NOT NULL DEFAULT '';

ALTER TABLE project_agent
ADD COLUMN llm_account_id TEXT NOT NULL DEFAULT '';

CREATE TABLE llm_credential (
    id TEXT PRIMARY KEY,
    connector_type TEXT NOT NULL,
    auth_type TEXT NOT NULL,
    source_type TEXT NOT NULL,
    env_name TEXT NOT NULL DEFAULT '',
    display_label TEXT NOT NULL DEFAULT '',
    upstream_account_id TEXT NOT NULL DEFAULT '',
    secret_version INTEGER NOT NULL DEFAULT 1,
    encryption_key_id TEXT NOT NULL DEFAULT '',
    secret_nonce BLOB NOT NULL DEFAULT X'',
    secret_ciphertext BLOB NOT NULL DEFAULT X'',
    expires_at INTEGER NOT NULL DEFAULT 0,
    invalidated_at INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_llm_credential_status
ON llm_credential(connector_type, invalidated_at, expires_at);

CREATE TABLE llm_provider_credential (
    provider_id TEXT PRIMARY KEY,
    credential_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY(provider_id) REFERENCES llm_provider(id) ON DELETE CASCADE,
    FOREIGN KEY(credential_id) REFERENCES llm_credential(id) ON DELETE RESTRICT
);

CREATE INDEX idx_llm_provider_credential_credential
ON llm_provider_credential(credential_id);
```

`auth_type` 首版使用 `api_key` 或 `oauth`；`source_type` 使用 `env_api_key`、`managed_api_key` 或 `oauth`。这些值由 domain 校验，不在 SQLite 中写死枚举，便于以后增加 workload identity。所有新增时间字段使用 Unix milliseconds，`0` 表示尚未发生。

`agent_definition.llm_account_id` 和 `project_agent.llm_account_id` 不加外键，因为项目 YAML 需要可移植，允许先声明 Account ID、再在目标 daemon 配置同名 Account。`project_revision.spec_json` 和 `project_agent.spec_json` 自然包含 YAML 的 `account` 字段，不需要重写历史 JSON。

现有 `llm_provider.api_key` 保留，旧数据不自动迁移。没有 credential binding 的 provider 完全沿用当前逻辑；有 binding 的 provider 必须使用绑定 credential，失败时不能回退到 legacy API Key。

首次登录成功后，credential、provider 和 binding 在一个事务中创建。重新登录先写入新 credential，再原子替换 binding 并清理无引用的旧 credential。logout 更新 `invalidated_at`；remove 删除 provider、级联删除 binding，再清理无引用 credential。登录失败不能留下 enabled 但没有凭证的 Account。

### 5.3 Credential 存储

支持三种来源：

- `env_api_key`：引用 daemon 环境变量，不在数据库保存 secret；
- `managed_api_key`：CLI/UI 提交，由 daemon 加密保存；
- `oauth`：daemon 保存并刷新 access/refresh token。

managed credential 使用 AEAD 加密。master key 由 operator 注入，或首次使用时生成在 data root 的受限文件中；不能与 ciphertext 一起存进 SQLite。密钥不可用时 Account 进入 `reauth_required`，不得降级为明文存储。

## 6. OAuth 与刷新

OAuth exchange、Device Code polling、PKCE state 校验和 token refresh 都由 daemon 执行。CLI/Web UI 只展示 URL、一次性 code 和脱敏状态。

daemon 必须能够通过出站 HTTPS 访问各厂商的 authorization、token 和模型 API endpoint；登录、刷新和模型调用都依赖该网络。Device Code 不要求 daemon 暴露公网入站端口。Anthropic 的浏览器 PKCE callback 监听在运行 CLI 的本机，CLI 将 authorization code 转交 daemon；callback 不可达时允许粘贴最终 redirect URL。Sandbox 只需要访问 daemon，不需要直接访问厂商网络。

每个 Account 同时只允许一个登录 session。session 是短期内存状态，不写入数据库；daemon shutdown 时统一取消并等待退出。

请求到达时，如果 token 即将过期，credential service 按 credential ID 合并并发刷新。网络请求期间不持有数据库锁；新 access/refresh token 和 expiry 在短事务中原子更新。

- 临时网络错误返回可重试错误，并保留旧 credential；
- `invalid_grant`、撤销或持续 401 将 Account 标记为 `reauth_required`；
- 上游 401 最多强制刷新并重试一次；响应流开始后不再重放请求；
- logout 原子失效 credential，并在上游支持时 best-effort revoke；已签发的 Facade token 在下一次模型请求时自然失败；
- logout 是幂等操作，重复执行返回 Account 已退出，不暴露旧 credential 状态。

## 7. 安全约束

- OAuth endpoint、client ID、scope 和允许访问的上游 origin 由各自受信 Connector 配置提供，项目 YAML 不能覆盖；
- Codex 和 Anthropic OAuth token 只能发送到各自的 allowlist，跨 origin redirect 默认拒绝；
- Sandbox 请求中的 Authorization 只视为 Facade token，不能转发到上游；
- token、authorization code、API Key 和上游原始错误体不能进入日志、事件、响应或 Sandbox 文件；
- managed secret 只允许通过 Unix socket 或受保护的 HTTPS 控制面提交；
- 当前 daemon 没有用户级 RBAC，因此 Account 属于整个 daemon，不宣称是个人私有凭证。

实现前必须分别确认 OpenAI 和 Anthropic 的 OAuth client registration、scope、token audience、请求 headers 和代理使用方式获得厂商允许，不能直接复制其他 CLI 的 client ID。

## 8. 实施与验收

建议分三步交付：

1. Account/credential 存储、加密、登录 API 和 CLI；
2. `codex-subscription`、`anthropic-subscription` Connector，OAuth refresh 和 Runtime Facade 转发；
3. YAML `account`、多 Account 路由、Web UI 和其他厂商类型。

实现需要覆盖以下关键测试：

- 旧数据库、旧 `.env` 和无 `account` YAML 的行为不变；
- credential 密文、错误 key、重新登录和原子替换；
- OAuth 成功、超时、取消、logout/revoke 和并发 refresh；
- Sandbox -> daemon -> fake Codex/Anthropic upstream 的完整请求链；
- 显式 Account 失败不回退，所有订阅 Account 都不被 Scheduler/Generate 选中；
- TTY 向导、non-TTY 参数、无回显输入和 Ctrl-C。

完成标准：用户运行 `agent-compose login` 创建命名 Account，在 YAML 中通过 `account` 选择后，Coding Agent 请求始终经过 daemon；OAuth token 不出现在数据库明文、日志、YAML 或 Sandbox 中；现有 API Key 和 Scheduler 行为保持不变。

实施前还需确认：

- 首版支持的订阅计划、模型和 Agent provider 范围；
- OpenAI 和 Anthropic OAuth client registration 的发布授权；
- Anthropic extra usage 的计费提示和用户确认文案；
- 部署是否必须显式提供 credential encryption key；
- daemon 未启用 Bearer auth 却监听 TCP 时，credential API 的拒绝策略。
