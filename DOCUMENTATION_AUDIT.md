# agent-compose 文档准确性审计报告

- **审计基准**：`origin/main` @ `2fb4e84771f351a80b86c8be1e0b706b06b8185b`
- **清理基准**：本分支已变基到 `origin/main` @ `435717ae`，下表全部结论在该基线上修复完毕
- **审计范围**：仓库内全部人类可读文档 —— 根级文档（`README.md`、`README.zh-CN.md`、`AGENTS.md`、`CONTRIBUTING.md`、`TESTING.md`、`SECURITY.md`）、`docs/pages/` 及 `docs/pages/zh-CN/` 公开手册、`docs/design/`、`docs/spec/`、`runtime/*/README.md`、`examples/**`、`charts/**`、`deploy/**`、`tools/migrations/**`
- **对照物**：Go 代码、TypeScript runtime、proto 定义、`Taskfile.yml`、`scripts/**`、`guest-images/**`、`Dockerfile*`、`.env.example`、`.github/workflows/**`

## 一、结论摘要

审计共确认 **80 行缺陷，其中 79 项为独立问题**（`I24` 是 `I23`/`O18` 的重复指向），按性质分为四类：

| 类别 | 数量 | 含义 |
| --- | ---: | --- |
| **错误（WRONG）** | 21 | 文档描述与当前代码/配置**直接冲突**，照做会失败或得到相反结论 |
| **过时（OUTDATED）** | 30 | 代码已演进（新功能、新驱动、新工具、版本升级），文档停留在旧状态 |
| **描述不准确（INACCURATE）** | 26 | 结论方向正确但范围不全、遗漏字段/参数/子命令，或中英文版本漂移 |
| **计划未落地（NEVER-IMPLEMENTED-PLAN）** | 3 | 文档把未实现的设计写成现状，或把已实现的能力写成"待做" |

**修复状态：本分支已逐项修复全部 80 行**，共改动 45 个文件（含本审计文件），并拆分为独立的 `docs:` 提交。修复时逐条回到代码复核，因此有三处结论被修正或降级：

- `O18`、`I24` 在审计基线上确实缺失 `dsh`，但**已被同一分支的 Gemini 清理提交（本分支第 1 个提交）顺带修好**，因此无需再次改动。
- `agent-compose-runtime_contract.md` 的"协议 payload 标记只有两个"经复核**是准确的**：宿主侧仅 `pkg/execution/parse.go:12-13` 定义 `__AGENT_RESULT__`/`__COMMAND_RESULT__`，`__WORKFLOW_RESULT__`/`__WORKFLOW_EVENT__` 只由 runtime/SDK 解析，宿主从不检索它们，故保留原文。
- `k8s_pod_runtime_driver_k3d_test_plan.md` 中"与 docker/boxlite 同样的共享挂载"被补全为 docker、boxlite、microsandbox（`runtimeMountSpecsForMicrosandbox` 复用 BoxLite 的挂载集合）。

**仍未修复的是代码侧问题，不属于文档审计范围**，在第九节末尾单独列出。

**问题最集中的三条主线：**

1. **k8s 运行时驱动在文档中几乎不存在。** 代码里 `k8s` 是第四个正式驱动（`pkg/driver/runtime_driver.go:10-13`），Linux 构建产物实际编译了 `docker,boxlite,microsandbox,k8s`，但 README、AGENTS.md、`docs/design/agent-compose_design.md`、`proto_v2_api_contract.md`、两份 mount manifest 设计文档仍写"三个驱动"。README 自身前后矛盾（正文说三个驱动，构建矩阵同一节又列出 k8s）。
2. **`dsh` provider 的文档覆盖系统性缺失。** `dsh` 已是第五个正式 provider，但 `docs/pages/guest-image-abi.md` 的 provider 表、持久化挂载表、stream 会话支持列表，`agent-compose-runtime_contract.md` 的 `--provider` 枚举与 §10 适配器章节，`dynamic-workflow-spec.md` 的 runner/effort 矩阵，均未包含它。
3. **一次性 V2 storage 迁移工具及其测试已被删除（commit `e58ac901`），文档仍在教用户下载和运行它。** 这是最高危的一类：`README.md`/`README.zh-CN.md` 有整节"下载 `agent-compose-v2-storage-migrator-linux-amd64` 并执行"，`TESTING.md` 记录了一个已不存在的 `task test:e2e:docker-v2-storage-cutover` 和一个已不存在的环境变量。仓库中已无任何构建或发布该二进制的路径。

此外，`docs/design/` 中多份设计文档把**已经落地**的能力写成"未来/建议/尚未支持"（webhook 签名校验、投递状态机、乐观并发、可恢复交互会话、`provider/model` 路由退役），阅读者会得到与代码相反的判断。

## 二、审计方法与可信度

- 每个结论都同时给出**文档位置**与**代码证据位置**。`docs/design` 与 `docs/spec` 全部结论由子代理用 `git show HEAD:<file>` / `git grep HEAD` 固定到 `origin/main` 基线，避免受工作树并发编辑影响。
- 公开手册（`docs/pages/**`）结论通过**实际编译 CLI 并 dump 每个子命令的 `--help`** 与文档逐条比对得出；YAML schema 覆盖用模拟 `tools/genpages/schema-coverage.mjs` 的方式复核（73 个字段全部覆盖）。
- 交叉复核：报告作者对其中 **20 余项**高价值结论独立复验（见第四节标注的依据均为复验过的命令）。
- **一处子代理结论已证伪**：有子代理报告"`pkg/driver/types_test.go:92-110` 仍断言 `GOOGLE_API_KEY`/`GEMINI_API_KEY` 被拒绝，该测试当前失败"。实测 `go test -count=1 ./pkg/driver/...` **通过**（`ok ... 1.370s`）。该观察来自 Gemini 清理过程中的中间态竞态，**不是**真实问题，请勿按此修改。
- `docs/spec/core-e2e-test-strategy-spec.md:7` 的数量结论亦做了修正：子代理报"实际 126 个"，实测唯一 `TestE2E*` 函数为 **129 个**（`cmd/` 27、`pkg/` 88、`test/e2e/` 11、`internal/` 3）。

## 三、错误（WRONG）——文档与代码直接冲突

| ID | 位置 | 文档说法 | 实际情况与证据 | 建议 |
| --- | --- | --- | --- | --- |
| W1 | `TESTING.md:252-266`（命令 256、环境变量 259） | 存在一次性 V2 存储迁移的 opt-in E2E：`task test:e2e:docker-v2-storage-cutover`，可用 `AGENT_COMPOSE_E2E_V2_STORAGE_CUTOVER_IMAGE` 指定镜像 | `Taskfile.yml` 中该 task 出现 **0 次**；全仓库仅 `TESTING.md:259` 提到该环境变量；`test/e2e` 下无相关文件。均已被 commit `e58ac901` 删除 | 删除该段，或改为"该 E2E 已随迁移工具一同退役" |
| W2 | `README.md:455-539`、`README.zh-CN.md:366-435` | 一次性迁移器是受支持的、可下载的工具，指引下载 `SHASUMS256.txt` 与 `agent-compose-v2-storage-migrator-linux-amd64/-arm64` 并执行 | 同 commit 删除了 `cmd/agent-compose-migrate/**`、`scripts/build-v2-storage-migrator-binaries.sh`、`.github/workflows/v2-storage-migrator.yml`、`build:migrator` task。全仓库中该名称只剩这两处 README | 标注该节仅适用于移除前的历史 release，写明最后携带该 asset 的 tag，并说明本仓库不再构建它 |
| W3 | `CONTRIBUTING.md:80-82` | 生成的 `*.pb.go` 消息文件"有意被忽略，不要强行提交" | 生成物**已被跟踪且必须提交**：`git ls-files` 列出 `proto/**/*.pb.go` 与 `*connect.go` 共 4 个；`.gitignore` 无相关规则；`AGENTS.md:168` 与 `.github/workflows/ci.yml:40-43` 明确要求提交并保证"重新生成不应产生变化" | 改为"生成的 Go/Connect 源码随 `.proto` 变更一起提交" |
| W4 | `README.zh-CN.md:210-213`、`examples/scheduler-script/README.md:35-43` | `scheduler.script` 可用 `{ url: ... }` 指定来源，示例只写 `url:` 而无 `provider` | `provider` 为必填：`pkg/compose/normalize.go:1249-1296` 的 default 分支返回 `scheduler script provider "" is not supported`；`pkg/compose/spec.go:727-736` 要求 inline 或 provider 二选一。`file` 用 `path`，`http`/`git` 用 `url`。英文 README `:192-195` 是正确的 | 两处示例改为 `{ provider: file, path: ... }`，并说明 `url` 属于 `provider: http`/`git` |
| W5 | `docs/pages/command-line-manual.md:636`、`docs/pages/zh-CN/command-line-manual.md:613` | `exec` 有 `--agent <agent>` 选项（已废弃） | `exec` 只定义 `--run`、`--command`、`--prompt`、`-i/--interactive`、`-t/--tty`、`--cwd`；`cmd/agent-compose/cli_exec_command_test.go:254-262` 的 `TestCLIExecAgentFlagIsRemoved` 断言 `unknown flag: --agent`。仍然生效且会打印废弃警告的只有 `--run` | 删除该行；如需保留历史说明，只描述 `--run` |
| W6 | `docs/pages/command-line-manual.md:873-877`、zh:851-856 | `image ls\|pull\|rm\|inspect` 树已废弃、会向 stderr 打印警告、未来可能移除 | 代码中没有任何 `Deprecated:` 字段或警告（唯一的 `writeDeprecatedWarning` 调用点是 exec `--run` 与 `inspect session`）；`cmd/agent-compose/cli_image_test.go:663-674` 甚至断言 `image --help` 中**不得**出现 "deprecated"。代码中反而把顶层 `pull`/`build`/`rmi` 三个命令视为 legacy 别名（`images` 不在其中） | 改为中性的"两种写法都支持"，并说明哪一种是规范形式 |
| W7 | `docs/pages/octobus-quickstart.md:186`、`docs/pages/zh-CN/octobus-quickstart.md:186` | `agent-compose run coder "Use the calculator capability to add 20 and 22, ..."` | `run` 不接受位置参数形式的提示词：`cmd/agent-compose/cli_run_command.go:246-253` 返回 `run does not accept positional trigger arguments`（只有显式传 `--prompt`/`--command` 且无值时才会消费位置参数）。手册自身 `command-line-manual.md:385-386` 也说明了这一点 | 两处都改为 `run coder --prompt "..."` |
| W8 | `docs/design/agent-compose_design.md:297` | `WatchProject` "目前只有未实现的 handler" | 已实现：`pkg/agentcompose/api/project_handler.go:187` `func (h *ProjectHandler) WatchProject(...)`，委托给 `pkg/agentcompose/app/project_controller.go:194`；且 `cmd/agent-compose/rpc_transport_contract_test.go` 会因任何 RPC 仍由 `Unimplemented*Handler` 提供而失败 | 删除该句，或说明 `WatchProject` 流式返回项目变更 |
| W9 | `docs/design/agent-compose_design.md:22`（`AGENTS.md` 同样过时） | "Project/run owner helpers: `pkg/projects/` and `pkg/runs/`" | `pkg/projects` 不存在（`git ls-tree origin/main pkg/` 无此项）；项目域归属 `internal/projects`（如 `internal/projects/controller.go:512,523`） | 改为 `internal/projects/` |
| W10 | `docs/design/agent-compose_design.md:738-742` | Linux native binary / Linux daemon image 的编译驱动为 `docker, boxlite, microsandbox` | `scripts/build-agent-compose-binary.sh:154-159`：`linux-full` 使用 `tags=netgo,osusergo,boxlitecgo,microsandboxcgo,k8scompose`，`compiled_drivers=docker,boxlite,microsandbox,k8s`；`pkg/driver/runtime_driver_compiled_full_k8s_test.go` 断言四个驱动 | 两个 `linux-full` 行都补上 `k8s` |
| W11 | `docs/design/control_plane_transport_contract.md:35` | `project down`/`down` 调用 `GetProject`、`ListSchedulers`、`SetSchedulerEnabled`、`ListSandboxes`、`StopSandbox` | `cmd/agent-compose/cli_run_command.go:137-150` 只调用一次 `clients.project.RemoveProject(...)`；列举/停止等效果发生在服务端 `internal/projects/controller.go:523` → `DownProject` | RPC 列改为 `ProjectService.RemoveProject` |
| W12 | `docs/design/dsh_agent_provider_design.md:58` | `EnsureDshFacadeConfig`（`pkg/llms/dsh_facade.go`）签发 facade token；模型选择是 `<llm-provider-id>/<model-name>` 引用（`SplitModelReference`，`pkg/llms/model_reference.go`） | 两个文件都不存在，两个符号在全仓库 0 次出现；实际是 `pkg/llms/dialect_writers.go:160` 的 `writeDshGuestConfig`（设置 `DSH_WIRE_API`/`DSH_MODEL`/`GuestModelEnvName`/`DSH_PERMISSION_MODE`，由 `:16-30` 分发）。`provider/model` 形式对所有 agent 均已退役：`pkg/llms/agent_dialect.go:73`、`pkg/llms/agent_model_resolution.go:29-31` | 改写 §4.1，指向 `dialect_writers.go`/`writeDshGuestConfig`，并说明 `DSH_MODEL` 是不透明 model id |
| W13 | `docs/design/pi_agent_provider_design.md:139`、`:256` | `pkg/llms/pi_facade.go` 负责解析模型选择与签发 facade token | 该文件不存在；Pi 配置由 `pkg/llms/pi_runtime_config.go` 与 `pkg/llms/dialect_writers.go:145` 的 `writePiGuestConfig` 负责 | 两处引用改名 |
| W14 | `docs/design/llm_provider_catalog_design.md:14,143,210,215,225,248,250,330` | 使用 `provider/model` 选择 provider 与上游 literal 模型名，并给出 `baizhi/deepseek-v4-flash`、`openai/gpt-5.6-sol` 等示例 | 模型 id 已不透明化，`<connection>/<model>` 现在**直接报错**：`pkg/llms/connection_catalog.go:27` `ErrLegacyQualifiedModel = errors.New("model uses the retired <connection>/<model> syntax")`，由 `:353-374` 经 `Resolve`（`:297`）抛出；`pkg/llms/connection_catalog.go:43-61` 明确"Catalog 从不拆分或读取 model 字符串结构"。唯一例外是 `models.json` 顶层 `default` 键（`pkg/llms/catalog.go:364`） | 改写选择规则为"连接由声明的凭据/默认值决定；`model` 是不透明字面量"，删除 `provider/model` 示例（保留 `default` 键例外） |
| W15 | `docs/design/webhook_design.md:296` | "Provider signature verification is not performed yet." | 已实现：`pkg/events/webhooks/github.go:12` 读取 `X-Hub-Signature-256` 并用 `hmac.Equal` 校验；`pkg/storage/configstore/topic_event_store.go:590,720` 持久化/读取 `signature_type`、`signature_secret`。该文档 `:173-178` 与 `agent-compose_design.md:1068-1069` 都写的是"已校验" | 删除该句，改为"GitHub provider 签名已校验" |
| W16 | `docs/design/webhook_design.md:300` | "The event table is `event`, initialized by `ConfigStore.initSchema`." | `initSchema` 只是测试辅助（`pkg/storage/configstore/migrations_test_support_test.go:12`，仅被 `_test.go` 引用）；生产 schema 来自编号迁移 `pkg/storage/sqlite/migrations/`（`000001_baseline.sql:414` 起） | 指向迁移目录 |
| W17 | `docs/design/k8s_pod_runtime_driver_k3d_test_plan.md:42-43` | `proto/agentcompose/v2/agentcompose.pb.go` "被 gitignore，按需生成" | 生成物已被跟踪（`agentcompose.pb.go`、`agentcomposev2connect/agentcompose.connect.go`），无匹配的 ignore 规则，`AGENTS.md` 要求提交 | 改为"生成物已提交，用 `task generate` 重新生成" |
| W18 | `docs/design/k8s_pod_runtime_driver_k3d_test_plan.md:343` | `SandboxStartTimeout` 未设置时默认 2 分钟 | `pkg/config/config.go:810` `startTimeout := 30 * time.Minute`（env `SANDBOX_START_TIMEOUT`，legacy `SESSION_START_TIMEOUT`） | 改为 30 分钟 |
| W19 | `docs/design/runtime-bidirectional-stream-design.md:3-5` | "BoxLite interaction remains unsupported by the current runtime API." | BoxLite 命令交互**已支持**：`pkg/driver/boxlite_cgo.go:511-518` 返回 `NativeExec/Stdin/StdinEOF/TTY/Resize/Signal` 全 `true`（实现于 `pkg/driver/boxlite_interaction_cgo.go`）。同文档 `:86`、`:92`、`:33` 写的是正确结论。agent attach 不支持的原因是没有驱动设置 `AgentTurns`（`pkg/driver/interaction.go:147,243`），与 BoxLite 无关 | 改为"三个驱动都支持命令 attach；agent turn 走 wrapper-stream 路径" |
| W20 | `docs/pages/connect-transport-matrix.md:3`、zh:3 | bidi 指"`RunAttach`/`ExecAttach` 风格交互调用" | 实际暴露的是 `AttachAgentRun`（`proto/agentcompose/v2/agentcompose.proto:66`）与 `AttachExec`（`:88`）；`proto/agentcompose/v2/execution_rpc_contract_test.go:59-81` 的 `TestLegacyExecutionRPCNamesAreNotExposed` 断言 `RunAttach`/`ExecAttach` **不在** descriptor 中 | 示例名改为 `AttachAgentRun`/`AttachExec` |
| W21 | `docs/pages/guest-image-abi.md:188`、zh:131 | prompt 模式 `stream` 会话支持 `codex`、`claude`、`opencode`、`pi`，"其它 provider 在打开 guest 交互前被拒绝" | `dsh` 也在支持列表中：`pkg/runs/prompt_attach.go:40-47` 的 `promptAttachProviders` 含 `"dsh": true`，`:81` 的报错文本是"…codex, claude, opencode, pi, and dsh providers only"；guest runtime 同样支持（`runtime/javascript/src/provider.ts:3,25`） | 两语言都补上 `dsh`（同时注意 `cmd/agent-compose/cli_resource_reference.go:56-76` 的 CLI 侧白名单仍只有四个 provider，属代码不一致） |

## 四、过时（OUTDATED）——代码已演进，文档停在旧状态

### 4.1 驱动 / 构建 / 部署

| ID | 位置 | 过时内容 | 现状依据 | 建议 |
| --- | --- | --- | --- | --- |
| O1 | `README.md:264`、`README.zh-CN.md:244`、`AGENTS.md:120-121` | 构建矩阵的 Linux 原生二进制行只写 `docker, boxlite, microsandbox` | `scripts/build-agent-compose-binary.sh:154-159`：`linux-full` → `docker,boxlite,microsandbox,k8s`；`Dockerfile:58` 与 `Dockerfile.agent-compose-local:41` 都用 `--profile linux-full`。注意 `README.md:265` 已正确列出 k8s，README 自相矛盾 | 补 `k8s`，并同步 Linux 构建的描述性文字 |
| O2 | `docs/design/agent-compose_design.md:61,225,723` | "当前支持三个运行时驱动：`boxlite`、`docker`、`microsandbox`"（架构图、driver one-of、驱动章节各一处） | `RuntimeDriverK8s` 是正式驱动：`proto/agentcompose/v2/agentcompose.proto:1035-1039`（`K8sDriverSpec k8s = 5`）、`pkg/compose/spec.go:251,276`、`pkg/driver/k8s_runtime.go`、`pkg/volumes/k8s_driver.go`、`charts/agent-compose/`；`pkg/driver/runtime_mount_manifest.go:212-219,272` 显式处理 k8s | 三处枚举都补 `k8s` |
| O3 | `docs/design/proto_v2_api_contract.md:30-31` | "`config` 中 `boxlite`、`docker`、`microsandbox` 三选一" | 同 O2，`DriverSpec` one-of 有四个分支 | 列出四个分支 |
| O4 | `docs/design/runtime_mount_manifest_design.md:113-114` | "`driver` 是解析后的运行时驱动：`docker`、`boxlite` 或 `microsandbox`" | 同上；k8s 分支走 `runtimeMountSpecsForK8s`（`:272`）返回 nil（"k8s 驱动在 daemon 与沙箱 Pod 之间没有共享文件系统"） | 补 `k8s` 并说明其 manifest 为空 |
| O5 | `docs/design/runtime_mount_manifest_driver_specific_design.md:3` | "三种运行时驱动" | 四种（含 k8s） | 改为四种 |
| O6 | `examples/agent-compose/README.md:3-4,8-13`（+ zh-CN） | 示例索引自称"Docker runtime driver 的可运行示例"，表格只列 4 个 docker 示例，前置条件是"Docker daemon 运行中" | `examples/agent-compose/k8s-scheduler-skills-mcp/` 存在，内含 4 个 `driver: k8s` agent 与 README（要求 k8s 驱动 daemon、集群可见的 guest 镜像、StorageClass），但**没有任何索引页链接到它** | 表格补该示例及其前置条件，或把页面标题改为同时覆盖 Docker 与 Kubernetes |

### 4.2 `docs/design` 中"已完成却写成未来/现状相悖"

| ID | 位置 | 过时内容 | 现状依据 | 建议 |
| --- | --- | --- | --- | --- |
| O7 | `docs/design/project-spec-hash-contract.md:27-34` | "## Future concurrency preconditions —— 乐观并发**可以**通过新增可选 `expected_revision` 或 `expected_current_spec_hash` 字段实现" | `expected_current_spec_hash` **已存在并生效**：`proto/agentcompose/v2/agentcompose.proto:441`，注释（`:439-444`）说明不匹配时返回 `ABORTED`（dry run 亦然）；`submitted_spec_hash` 见 `:400`、`:421`；hash 格式 `sha256:`+hex（`pkg/compose/output.go:112`）。仅 `expected_revision` 仍未实现 | 把 `expected_current_spec_hash` 移出"未来"，只保留 `expected_revision` 为未决项 |
| O8 | `docs/design/webhook_design.md:192-208` | "Suggested new `webhook_source` table and management API"，字段表被当作提案 | `pkg/storage/sqlite/migrations/000001_baseline.sql:414-429` 已定义 `webhook_source(id, name, enabled, provider, topic_prefix, token_hash, token_header, signature_type, signature_secret, body_limit_bytes, created_at, updated_at)` 及索引；文档字段表还漏了真实存在的 `token_header` | 改写为描述现状表结构并补 `token_header` |
| O9 | `docs/design/webhook_design.md:326-330,388-412` | 投递状态机被写成"Target field to add: `replay_of_event_id`"、"Suggested first phase…"、"Add `event_delivery` table" | 全部已落地：`000001_baseline.sql:395-401` 已有 `replay_of_event_id`、`claim_id`、`attempt_count`、`next_attempt_at`、`dead_letter_at`（索引 `:409`）；状态词汇见 `pkg/model/topic_event_model.go:19-27`；dispatcher 已使用 `Retrying`/`DeadLetter`/`NextAttemptAt`（`pkg/events/dispatcher.go:117,138,145`）；`event_delivery` 由 `000007_event_scheduler_links.sql:34-57` 重建 | 移入"当前行为"章节 |
| O10 | `docs/design/resumable-interactive-session.md:7-9` | 描述"当前边界"：`AttachAgentRun` 首帧创建新 run、断连即取消、无法按 `run_id` 恢复、后台 run 没有输入通道 | 三个缺口全部关闭：`AttachAgentRunStart.run_id = 6`、`disconnect_policy = 7`（`proto:1132-1141`），`AttachRunMode`（`:307-311`）、`AttachDisconnectPolicy`（含 `ATTACH_DISCONNECT_DETACH`，`:313-317`），`StartAgentRunRequest.interactive = 2`（`:1970-1974`）；Go 侧 `pkg/runs/controller.go:345,355,454,470,494`、`pkg/runs/attach_input.go:24-34`、`pkg/agentcompose/app/run_supervisor.go:88-166`、`pkg/runs/interactive_session.go:29,43,242` | 把该节改写为"此前的边界（现已实现）" |
| O11 | `docs/design/k8s_pod_runtime_driver_k3d_test_plan.md:369-397` | Scenario 8 断言 k8s 拒绝 volume 挂载（期望报错 `k8s driver does not support volume mounts`），并称"named volumes 在 k8s v1 中不在范围内" | 恰好相反：named volume 是 k8s 的**受支持**路径，映射为 PVC（`k8s_pod_runtime_driver_design.md:108-114`；`pkg/volumes/k8s_driver.go:25-26` 默认 `1Gi`/`ReadWriteOnce`）。被拒绝的是 bind mount 与非 k8s driver 的 volume：`pkg/volumes/normalize.go:114-142`，用例见 `pkg/volumes/normalize_test.go:84` | 重写 Scenario 8：named volume 成功（PVC）、bind mount / 非 k8s driver 失败 |
| O12 | `docs/design/workspace-content-reuse.zh-CN.md:270,277` | proto 依赖要求为 `v0.1.4`（"当前 require"） | `go.mod:7` 要求 `github.com/chaitin/agent-compose/proto v0.1.7`（`replace … => ./proto` 见 `:132`） | 作为历史快照标注时点，或更新为当前值 |
| O13 | `docs/design/agent_system_prompt_design.md:17` | "all five runners receive the composed context" | 基线（Gemini 仍在）是六个 runner；本分支移除 Gemini 后为五个（Codex、Claude、OpenCode、Pi、DSH）；`runtime/javascript/src/runners/dsh.ts:82-106` 消费 `systemContext` 并写出 `system-context.txt`、设置 `DSH_SYSTEM_CONTEXT_FILE` | 去掉硬编码数量，并补 dsh 的 file-based 机制 |
| O14 | `docs/design/agent_system_prompt_design.md:179` | `execution.WriteAgentSystemPromptFile(sandbox, systemPrompt string) error` | 实际签名在 `pkg/execution/agent_files.go:72`：`(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, systemPrompt string, writeGuestFile GuestFileWriterFunc) error`（guest writer 参数用于 k8s 经 exec/tar 推送）；已不在 `pkg/execution/model.go` | 更新签名与文件变更表 |
| O15 | `docs/design/pi_agent_provider_design.md:11-12,263` | Pi 固定版本 `v0.81.1` / `@earendil-works/pi-coding-agent@0.81.1` / `ARG PI_AGENT_VERSION=0.81.1` | `guest-images/Dockerfile.agent-compose-guest:13` 为 `0.82.1`（`:14` 的 `PI_MCP_ADAPTER_VERSION=2.11.0`） | 更新版本或直接引用 Dockerfile |
| O16 | `docs/design/octobus_integration.md:286` | "all five current guest runners receive the composed `systemContext`" | 基线六个 runner（含 dsh，见 O13），本分支移除 Gemini 后为五个 | 去掉数量，写"所有 guest runner" |
| O17 | `docs/design/runtime-bidirectional-stream-design.md:277-283` | prompt 交互策略表：Codex 先实现，Claude/**Gemini**/OpenCode "待确认 SDK 会话能力后接入" | prompt attach 实际支持 `codex`、`claude`、`opencode`、`pi`、`dsh`（`pkg/runs/prompt_attach.go:41-45`；`:38` 注释"gemini 缺席因为 GeminiRunner 不持久化 thread"；`:82` 报错文本列出五个 provider） | 用 `prompt_attach.go` 的当前集合替换矩阵 |
| O18 | `docs/design/agent-compose-runtime_contract.md:8,191` | provider 枚举为 "Codex, Claude, Gemini, OpenCode, and Pi" / `codex, claude, gemini, opencode, pi` | `dsh` 是一等 provider（`pkg/model/agent_model.go:90-91`、`runtime/javascript/src/runners/dsh.ts`）。**注**：Gemini 部分已由本次清理修复（见第六节），此处残留的是 dsh 缺失 | 两处补 `dsh` |
| O19 | `docs/design/agent-compose-runtime_contract.md:635-720` | §10 "Provider Adapter Behavior" 只有 `10.1 Codex`、`10.2 Claude`、`10.3 Gemini`、`10.4 OpenCode` | Pi 与 dsh runner 都已存在且可选（`runtime/javascript/src/runners/pi.ts`、`dsh.ts`；`pkg/runs/prompt_attach.go:38-45`），但契约读者无法从中了解 Pi 的 appended system-prompt file 与 dsh 的 `DSH_*` 契约 | 补 §10.5 Pi 与 §10.6 dsh（或明确指向对应 provider 设计文档） |
| O20 | `docs/design/runtime_mount_manifest_design.md:130-143` | Home 初始化表只有 `.codex/.claude/.claude.json/.gitconfig`；logical home 列表为 `.opencode`、`.pi`、`.config/{claude,Claude,opencode}` | `pkg/driver/runtime_mount_manifest.go:352` 迭代 `{".codex", ".claude", ".claude.json", ".gitconfig", ".dsh"}`；logical 列表（`:225-247`）还含 `home/.agents`、`home/.pi`、`home/.dsh` | 补 `.dsh`（及 `.agents`） |
| O21 | `docs/design/runtime_mount_manifest_driver_specific_design.md:74-90,99-115,161-184` | "logical list 是所有 driver 的 source of truth"（15 行），Docker/BoxLite/Microsandbox 布局表同样缺项 | 规范列表（`pkg/driver/runtime_mount_manifest.go:225-247`）额外含 `home/.agents`、`home/.pi`、`home/.dsh` | 依据 `runtimeMountManifestLogicalList` 重新生成表格 |

### 4.3 `docs/spec` 与 runtime 文档

| ID | 位置 | 过时内容 | 现状依据 | 建议 |
| --- | --- | --- | --- | --- |
| O22 | `docs/spec/dynamic-workflow-spec.md:49-54` | "`runtime/javascript` 当前 CLI 暴露两个 host-dependent 子命令：`prompt` / `exec`" | `runtime/javascript/src/cli.ts` 注册**四个**：`prompt`（:29）、`workflow`（:59）、`exec`（:94）、`stream`（:115） | 改为四个 |
| O23 | `docs/spec/dynamic-workflow-spec.md:58-62` | "现有 provider runner" 只列 `CodexRunner`、`ClaudeRunner`、`OpenCodeRunner`（原含 Gemini） | `runtime/javascript/src/prompt.ts:89-94` 还分发 `PiRunner`、`DshRunner`；同一文档 `:410`、`:491` 的 provider 联合类型已包含 `pi`、`dsh` | 补 `PiRunner`（`runners/pi.ts:32`）与 `DshRunner`（`runners/dsh.ts:46`），二者均拒绝 `outputSchema` |
| O24 | `docs/spec/dynamic-workflow-spec.md:64-73` | "`docs/design/agent-compose-runtime_contract.md` 当前明确说明还没有：`workflow` 子命令、`__WORKFLOW_RESULT__` stdout 协议…本方案实现后需要同步更新该设计文档" | 该设计文档现已描述这些能力：`docs/design/agent-compose-runtime_contract.md:792-808`（"provides `prompt`, `exec`, and `workflow` … writes one `__WORKFLOW_RESULT__` payload to stdout, and streams prefixed `__WORKFLOW_EVENT__` records"），SDK 的 `runtime.workflow()/workflowFile()` 也已写入 | 删除过时的"现状"清单与"待更新"动作项，或改为已完成说明 |
| O25 | `docs/spec/dynamic-workflow-spec.md:635` | "package version 从当前 `0.9.0` 升到 `0.10.0`" | 两个包均为 `0.11.0`（`runtime/javascript/package.json:3`、`runtime/agent-compose-runtime-sdk/package.json:3`） | 写实际版本或标注为历史 |
| O26 | `docs/spec/core-e2e-test-strategy-spec.md:7` | "当前约有 77 个 `TestE2E...`" | 实测 **129** 个唯一 `TestE2E*` 函数（`cmd/` 27、`pkg/` 88、`test/e2e/` 11、`internal/` 3），函数名出现次数同为 129 | 更新为约 129 |
| O27 | `docs/spec/core-e2e-test-strategy-spec.md:38` | 引用 `test/e2e/api_smoke_test.go` 作为现有 fixture | 该文件不存在（`test/e2e/` 下 15 个 `.go` 文件中没有它） | 删除该条或替换为现存 fixture |

### 4.4 根级文档与 README 漂移

| ID | 位置 | 过时内容 | 现状依据 | 建议 |
| --- | --- | --- | --- | --- |
| O28 | `README.zh-CN.md:197,199,206` | 中文 CLI 表：`scheduler ls\|runs\|logs\|trigger\|inspect`；`images\|pull\|build\|rmi\|inspect`；全局参数只写 `--project-name` | `cmd/agent-compose/cli_scheduler_definition.go` 还注册 `invoke`（:19）、`prune`（:76）、`stop`（:96）；`cli_image_definition.go:11-69` 注册分组形式 `image ls\|pull\|build\|rm\|inspect`（`rmi` 仍作为 legacy 别名存在）；`cli_root.go:63` 定义 `--project-name, -p`。英文表也漏了 `scheduler stop` | 依据三个定义文件重新生成 zh-CN 表（含 `invoke`/`prune`/`stop`、分组 `image`、`-p`） |
| O29 | `README.md:245-248`（及 `:363-389`） | 英文 README 只提 `AGENT_COMPOSE_AUTH_TOKEN`，CLI 表无 `auth` 行，全文未描述 daemon Bearer-token 工作流 | `agent-compose auth login\|logout\|ls` 已实现（`cmd/agent-compose/cli_auth.go`），凭据存于 `~/.config/agent-compose/config.yml`（`pkg/clientconfig/auth.go:24-38`，可用 `AGENT_COMPOSE_CONFIG` 覆盖）；这些内容在 `README.zh-CN.md:202,215-234` 已有 | 把 zh-CN 的 `auth` CLI 行与"Daemon authentication"整节移植到英文 README |
| O30 | `charts/agent-compose/README.md:84-85` | "see the main project README's 'Daemon `models.json`' section" | 两个 README 都没有 `models.json` 内容；该章节实际位于 `docs/pages/agent-compose-yaml-manual.md:550`（zh:551） | 改为指向手册页面锚点 |

## 五、描述不准确 / 不完整（INACCURATE）

### 5.1 "三个驱动"与实际四个驱动（同一根因，跨文档）

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| I1 | `README.md:38`、`README.md:258`、`README.zh-CN.md:35`、`SECURITY.md:44` | 写"**Three** runtime drivers"、"The **three** names describe product-supported drivers"，但同一节又列出 k8s 列（`README.md:252,265`） | `pkg/driver/runtime_driver.go:10-13` 与 `pkg/config/config.go:30-33` 都声明 `boxlite`、`docker`、`microsandbox`、`k8s` 四个名字 |

**建议**：统一改为"四个驱动"或直接列举 "Docker、BoxLite、Microsandbox、Kubernetes"；凡枚举驱动集合处都补 k8s。

### 5.2 `dsh` provider 文档缺口

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| I2 | `docs/pages/guest-image-abi.md:262-267`、zh:192-197 | §5.1 provider 表只列 Codex、Claude、OpenCode、Pi | 默认 guest 镜像安装 `@deepseek-ai/dsh@${DSH_VERSION}` 与 `/usr/bin/dsh`（`guest-images/Dockerfile.agent-compose-guest:78,106`）；DSH 在其它文档（`agent-compose-yaml-manual.md:507,535`）、`pkg/model/agent_model.go`、`runtime/javascript/src/provider.ts:3` 中都是一等 provider |
| I3 | `docs/pages/guest-image-abi.md:146-153`、zh:101-108 | 持久化的 home 条目表缺 `/root/.dsh`（且同页 `:163`/zh:114 声称"只有声明的 home 条目会被持久化"，使缺失变得实质） | `pkg/driver/runtime_mount_manifest.go:237` 有 `home/.dsh` → `/root/.dsh`，legacy 迁移列表 `:349` 同样包含 |
| I4 | `docs/pages/guest-image-abi.md:412`、zh:291 | guest provider 版本 build arg 表列出 `CODEX_VERSION`、`CLAUDE_CODE_VERSION`、`OPENCODE_VERSION`、`PI_AGENT_VERSION`、`PI_MCP_ADAPTER_VERSION`，缺 `DSH_VERSION` | `guest-images/Dockerfile.agent-compose-guest:16` `ARG DSH_VERSION=0.1.0-rc.6`（archlinux 变体 `:17`）。**附带代码问题**：`DSH_VERSION` 未在 `Taskfile.yml` 全局 env 中声明，`scripts/build-agent-compose-guest.sh` 也不转发，因此 `task image:agent-compose-guest` 无法覆盖它，Dockerfile 默认值总是生效 |
| I5 | `docs/spec/dynamic-workflow-spec.md:448-452`（重复于 `:872-874`） | `effort` 矩阵只把 OpenCode 列为"首版返回 unsupported error" | `runtime/javascript/src/workflow/agent-invocation.ts:34-36` 对**所有非 codex/claude** provider 拒绝：`if (raw.effort && provider !== "codex" && provider !== "claude")`，即 opencode、pi、dsh 都抛 `workflow agent effort is not supported by <provider> runner`。`model` 列表（`:444-446`）同样漏 Pi/DSH |

### 5.3 CLI 手册遗漏与错误

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| I6 | `docs/pages/command-line-manual.md:421`、zh:392 | 写 "Codex, Claude/**cc**, OpenCode, and Pi"，`cc` 别名不存在 | Claude 的别名只有 `claude-code`/`claude_code`（`cmd/agent-compose/cli_resource_reference.go:80-92`、`pkg/model/agent_model.go:82`、`runtime/javascript/src/provider.ts:14-15`） |
| I7 | `docs/pages/command-line-manual.md:707`、zh:686 | `inspect` 类型列表缺 `volume`；`volume`（`ls/create/inspect/rm/prune`）与 `llm provider`（`ls/create/inspect/update/rm`）两个命令组在 CLI 手册中完全没有章节 | `cmd/agent-compose/cli_inspect_command.go:14` 的 `Use` 含 `volume`；`cmd/agent-compose/cli_root.go:90-91` 注册这两个组；YAML 手册仅在 `:643-648` 顺带提到 `llm provider` |
| I8 | `docs/pages/command-line-manual.md:388-396`、zh:358-367 | `run` 选项表缺 `--driver <name>`、`-t, --tty`、`--label key=value`（可重复） | `cmd/agent-compose/cli_run_definition.go:17,24,26`；`run --help` 实测 |
| I9 | `docs/pages/command-line-manual.md:631-637`、zh:608-614 | `exec` 选项表缺 `-i/--interactive`、`-t/--tty`（正文 `:634` 又提到 `-i`/`-t`，表格自相矛盾），且含不存在的 `--agent`（见 W5） | `cmd/agent-compose/cli_exec_definition.go`；`exec --help` 实测 |
| I10 | `docs/pages/command-line-manual.md:466-470,506`、zh:439-443 及 sandbox-ls 行 | `ps` / `sandbox ls` 的过滤参数只写 `--all`、`--status`、`--verbose` | 二者都还接受可重复、按 AND 组合的 `--label key=value`（`cmd/agent-compose/cli_ps_command.go:23`；`ps --help` 实测） |
| I11 | `docs/pages/command-line-manual.md:26-48`（Rules） vs zh:46 | 中文手册写明"daemon 不再消费浏览器登录用的 `AUTH_*`/`OAUTH_*` 配置；UI 浏览器认证由 agent-compose-ui server 处理"，英文手册无对应规则 | daemon 只读 `AGENT_COMPOSE_AUTH_TOKEN`（`pkg/config/config.go`，`pkg/`、`cmd/agent-compose/` 中无 `AUTH_*`/`OAUTH_*` 读取），而 `.env.example:35-40,193-198` 与 installer 仍保留这些名字给 UI → 中文规则正确，英文手册漂移 | 
| I12 | `docs/pages/agent-compose-yaml-manual.md:677`、zh:659 | "`agent-compose build` 在 `image` 与 `build.tags` 都未提供 tag 时失败" | 实际条件是 `len(tags) == 0`，其中 `tags = agent.Image + build.Tags + CLI --tag`（`cmd/agent-compose/cli_image_command.go:310-342`）；只给 `--tag` 的构建会成功 | 
| I13 | `docs/pages/agent-compose-yaml-manual.md:772`、zh:760 | "k8s 驱动要求 daemon 运行在目标集群内" | 驱动用 kubeconfig 构建 client，in-cluster 只是回退：`clientcmd.NewNonInteractiveDeferredLoadingClientConfig` + `K8S_KUBECONFIG`/`KUBECONFIG`/`~/.kube/config`（`pkg/driver/k8s_runtime.go:177-207`、`pkg/config/config.go:610`、`pkg/compose/spec.go:272-276`）；Pod 可达的 daemon URL 单独由 `K8S_RUNTIME_BASE_URL`/`config.RuntimeBaseURL` 配置（`pkg/driver/k8s_runtime.go:573-576`、`pkg/llms/runtime_config.go:412-419`）。Helm chart 是**受支持的安装入口**，不是驱动的硬性要求 |

### 5.4 根级文档不完整

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| I14 | `README.md:173` | 顶层字段列表 `name, env_file, variables, workspaces, agents, mcp_servers, volumes` 漏 `octobus_servers` | `pkg/compose/spec.go:19` 声明 `OctoBusServers map[string]OctoBusServerSpec \`yaml:"octobus_servers"\``，`pkg/compose/spec.go:496` 有校验；手册 `docs/pages/agent-compose-yaml-manual.md:212,437` 与 `octobus-quickstart.md` 均为其正式文档 |
| I15 | `Taskfile.yml:123-131` | `docs:build` 的 `generates` 列表不含 `connect-transport-matrix.html`、`octobus-quickstart.html` 及两者 zh 变体 | `tools/genpages/render-pages.mjs:13-45,69-73` 会额外渲染这四个页面（外加 `:92-93` 的 `index.html`/`manual.css` 拷贝）。`generates` 不全时，页面缺失也可能被 Task 判定为"已是最新" |
| I16 | `README.md:366`、`README.zh-CN.md:316` | "`AUTH_PASSWORD`, `AUTH_SECRET` — UI server login secrets（replace the examples / 务必替换示例值）" | `.env.example:39-40` 中这两个变量**是空值**，没有"示例值"可替换；`README.md:337-338` 正确地要求用 `openssl rand` 生成；`deploy/README.md:92-95` 说明 installer 首次安装时生成 | 

### 5.5 runtime / SDK README 不完整

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| I17 | `runtime/javascript/README.md:33-34` | workflow 脚本可用全局对象列表为 `agent`、`parallel`、`pipeline`、`phase`、`log`、`workflow`、`args`、`budget`，漏 `cwd` 与冻结的 `process` | `runtime/javascript/src/workflow/runtime.ts:124-126`（`cwd: this.options.workspace`、`process: Object.freeze({ cwd })`；沙箱加固见 `:103`）；`docs/spec/dynamic-workflow-spec.md` 也列出了 `cwd`/`process` |
| I18 | `runtime/javascript/README.md:5-13,21` | `prompt` 概览缺 `--model`、可重复的 `--skill`；全文未记录 `exec`、`stream` 子命令 | `runtime/javascript/src/cli.ts:35`（`--model`）、`:37`（`--skill`）、`:94`（`exec`）、`:115`（`stream`） |
| I19 | `runtime/agent-compose-runtime-sdk/README.md:302` | `baseUrl` 回退链写作 `BASE_URL` → `HTTP_URL` → `http://127.0.0.1:7410` | `src/llm.ts:84-90` 实际为 `baseUrl` → `BASE_URL` → `HTTP_URL` → `AGENT_COMPOSE_BASE_URL` → `AGENT_COMPOSE_HTTP_URL` → 默认值 |
| I20 | `runtime/agent-compose-runtime-sdk/README.md:49-54` | `runtime.paths` 表只映射 `workspace`→`WORKSPACE`、`stateRoot`→`STATE_ROOT`、`runtimeRoot`→`RUNTIME_ROOT` | `src/env.ts:16-19,40-46` 在默认值之前还会读取 legacy 别名 `AGENT_COMPOSE_WORKSPACE`、`AGENT_COMPOSE_STATE_ROOT`、`AGENT_COMPOSE_RUNTIME_ROOT` |
| I21 | `runtime/agent-compose-runtime-sdk/README.md`（API 章节） | 完全没有文档化导出的 `ssh` API | `src/index.ts:13-14` 导出 `ssh`、`RuntimeSshConfig`、`RuntimeSshPrepareOptions`；`src/ssh.ts:93` 暴露 `ssh.prepareConfig()`（用于写 `~/.ssh/config`、known_hosts 与密钥） |
| I22 | `runtime/agent-compose-runtime-sdk/README.md:207` | 把不支持 schema 输出写成 OpenCode 独有现象 | 实际有**四个** runner 抛 `structured JSON output is not supported by <provider> runner`：`runners/opencode.ts:298`、`runners/pi.ts:32`、`runners/dsh.ts:46`；README `:183` 已列出 OpenCode/Pi/DSH，`:207` 与之矛盾（Codex/Claude 支持：`runners/codex.ts:385-387`、`runners/claude.ts:118-121`） |
| I23 | `docs/design/agent-compose-runtime_contract.md:175` | "The JavaScript runtime supports two subcommands:" 后接 `prompt` / `exec` | 同一文件 `:792` 说提供 `prompt`、`exec`、`workflow`；`runtime/javascript/src/cli.ts` 实际有四个（含 `:115` 的 `stream`） |
| I24 | `docs/design/agent-compose-runtime_contract.md:8,191` | 见 O18（provider 枚举缺 dsh）；此处同时存在"两个子命令"表述不一致（与 I23 同源） | 同上 |
| I25 | `docs/design/proto-v2-field-layout-freeze.md:37-40` | 称四个 Attach 消息都在 tag 15 **和** 16 保留信封元数据 | `AttachExecRequest`/`AttachAgentRunRequest` 只定义了 tag 15（轻微不准确） |
| I26 | `docs/design/control_plane_transport_contract.md:33` | `project up`/`up` 调用 `ProjectService.ValidateProject` 与 `ApplyProject` | `cmd/agent-compose/cli_run_command.go:87-120` 只调用 `ApplyProject`（带 `Source`、`SubmittedSpecHash`）；`git grep "ValidateProject(" -- cmd/agent-compose` 只命中 `_test.go`。`ValidateProject` 是独立 CLI/API 表面 |

## 六、计划未落地（NEVER-IMPLEMENTED-PLAN）

| ID | 位置 | 问题 | 依据 |
| --- | --- | --- | --- |
| P1 | `docs/design/webhook_design.md:467-469` | 把 `POST /api/events/:event_id/replay` 与 `GET /api/webhook-sources/:source_id/stats` 列入 HTTP API | 两条路由都未注册：`cmd/agent-compose/rpc_transport_contract_test.go:24-45` 的 `TestDaemonHTTPRouteAllowlist` 中不存在，唯一的 webhook-source 路由是 `GET /api/webhook-sources`；文档自身 `:483` 用的是 "should" |
| P2 | `docs/design/project-definition-architecture.md:12-16,29` | 称 `pkg/projectdef` 是"受支持的可复用 API"，拥有 schema、YAML/JSON 加载、归一化、canonical JSON/hash、静态校验；并称 `pkg/compose` 会在迁移中沿该边界拆分 | `pkg/projectdef/projectdef.go` 只有 93 行类型别名与一行转发器（`type ProjectSpec = compose.ProjectSpec`、`Parse → compose.Parse`）；实现仍在 `pkg/compose`（`spec.go`、`normalize.go`、`canonical_json.go`、`output.go:106,115`）。`pkg/projectdef` 另有 15+ 个转发一致性测试，是 façade 而非所有者 |
| P3 | `docs/design/runtime-bidirectional-stream-design.md`（非目标节与实际能力） | 见 W19 与 O17：把已支持的 BoxLite 交互写成不支持，并把已实现的 prompt attach 集合写成"待接入" | 同 W19/O17 |

## 七、经核对确认无误的部分（无需修改）

以下内容经逐条对照代码确认正确，可作为后续修改时的可靠参考：

- **测试与覆盖率**：`TESTING.md` 的形状覆盖率基线 60/60/60/70 与 `scripts/test-coverage.sh:219-224` 一致；Go 覆盖率流水线描述与 `scripts/test-coverage.sh:27-58,120-140,275-288` 一致；`test:deploy` 的工具依赖（readelf、docker compose、jq）与 `scripts/tests/test-installer-binaries.sh:20-21`、`scripts/tests/test-compose-kvm-config.sh:6-11` 一致；E2E fixture 数量（"128 + 3"、"257"、范围 1–100000 / 3–100000）与 `test/e2e/docker_workspace_mount_host_daemon_test.go:293,300-305`、`test/e2e/docker_skills_reuse_host_daemon_test.go:212,216` 一致。
- **配置默认值**：`.env.example` 中 `SQLITE_MAX_OPEN_CONNS=4`、`CACHE_TTL=168h`、`CLEANUP_INTERVAL=1h`、`SANDBOX_CPUS=4`、`SANDBOX_MEMORY_MIB=4096`、`SANDBOX_DISK_SIZE_GB=6`、`AGENT_TIMEOUT=10h`、`SCHEDULER_RUN_TIMEOUT=20m`、`SANDBOX_START_TIMEOUT=30m`、`SANDBOX_STOP_TIMEOUT=30s`、`SANDBOX_GRACEFUL_STOP_TIMEOUT=10s`、`JUPYTER_READY_TIMEOUT=120s`、`WEBHOOK_BODY_LIMIT_BYTES`、`WORKSPACE_UPLOAD_LIMIT_BYTES`、`JUPYTER_GUEST_PORT=8888`、`DEFAULT_IMAGE`、`RUNTIME_DRIVER=docker`、`JUPYTER_PROXY_BASE=/jupyter`，以及 `SANDBOX_*`→`SESSION_*` legacy 别名优先级（`pkg/config/config.go:1150-1164`）与 `AGENT_TELEMETRY_*` 语义（`pkg/config/agent_telemetry.go:19-67`）。
- **daemon 鉴权**：`docs/design/daemon_bearer_auth.md` —— token hash + `subtle.ConstantTimeCompare`、401 形状、豁免清单、客户端配置路径均与 `cmd/agent-compose/daemon_auth.go:17-68`、`pkg/config/config.go:416`、`pkg/clientconfig/auth.go:27,34` 一致。
- **proto 契约**：`proto-v2-string-enum-boundaries.md`（所有 enum 都有零值 `UNSPECIFIED`，字段/枚举形状与 proto 一致）；`llm-provider-rpc.md`（服务/方法名与"`<connection>/<model>` 已退役"的表述一致）。
- **k8s**：`k8s_pod_runtime_driver_design.md`（chart、环境变量、k8s volume driver、guest 文件推送）全部核对通过；`k8s_pod_runtime_driver_k3d_test_plan.md` 的构建 profile 断言、Pod 标签（`pkg/driver/k8s_runtime.go:38-40`）、`pkg/runs/sandbox_preparation.go:343` 亦正确。
- **runtime 环境变量**：`runtime_environment_variables_design.md` —— `GUEST_WORKSPACE`/`GUEST_STATE_ROOT`/`GUEST_RUNTIME_ROOT`/`GUEST_LOG_ROOT` 与"`GUEST_HOME` 不再是公开配置输入"的表述与 `pkg/config/config.go` 一致。
- **LLM 路由**：`llm_model_routing_redesign.md` 准确性很高 —— `pkg/llms` 生产文件数 26、五个 dialect writer 与 `dialect_writers.go` 对应、bridge 伪版本 `v1.1.6-0.20260922130207-cfe67158b4c7` 与 `go.mod:8` 一致、迁移 16/17 说明与迁移目录一致。
- **公开手册**：YAML schema 覆盖（74 个字段在中英文手册中均被覆盖）、`env_file` 解析、capset 路由与校验、`build.platforms`、firecracker 拒绝、并发/超时默认值、`/etc/localtime` 挂载、GUI/MPI catalog + `x-capability-sandbox-token` + `capset` 标签、webhook 路由与 daemon route allowlist 的一致性、guest ready-file/graceful-stop ABI 均正确。
- **其它**：`deploy/README.md` installer 标志/子命令/环境覆盖；`charts/agent-compose/README.md` 的 values/templates/PVC 保留策略；`sdk/go/README.md` 版本表与 API 签名；`tools/migrations/README.md`、`pkg/storage/sqlite/migrations/README.md`、`pkg/compose/testdata/compat/README.md`；`AGENTS.md:125-133` 原生默认值、`AGENTS.md:168` proto 打标流程与 `.github/workflows/proto-tag.yml` 一致；`SECURITY.md:30-32` 的 `HTTP_LISTEN` 警告（`pkg/config/config.go:1045`）；`playground_setup.md` 端口/task/smoke 命令；`resource_identity_cli_design.md` 已实现部分（裸 SHA-256 + `ShortID` 12 字符、`legacyIDPrefix = "sha256:"`、`run_short_id`/`sandbox_short_id`、`--verbose`）。

## 八、Gemini provider 清理（本分支已完成，附带给文档带来的变化）

用户确认为"无人使用"，故按**彻底删除、含 Google 凭据识别**执行。清理后的文档现状（与代码一致）：

- **provider 集合**：`codex`、`claude`、`opencode`、`pi`、`dsh`。`provider: gemini` 现在会命中通用校验错误 `agent definition provider %q is not supported`（`internal/projects/agent_definition_normalize.go`）。这是**有意的非向后兼容**变更；不提供兼容别名。
- **从 guest 移除**：`gemini` 运行时（`runtime/javascript/src/runners/gemini.ts` 及 fixture、导出、CLI 帮助、provider 联合类型、systemContext 分发、telemetry、agent-event 解析注释），guest 镜像中的 `GEMINI_CLI_VERSION` build arg 与 `gemini` CLI 安装，`/root/.gemini`、`/root/.config/gemini`、`/root/.local/share/gemini` 挂载项与镜像目录，以及相关 task/CI 契约测试。
- **凭据模型变化（需安全评审知悉）**：`providerFamilyGoogle` 常量与 `GOOGLE_API_KEY`/`GEMINI_API_KEY` 的"识别但不吸收"条目已从 `pkg/llms/declared_connection.go` 与 `pkg/driver/types.go` 的 `LLMProviderCredentialEnvName` 中删除。**后果：这两个变量名现在属于"不识别"类别，会原样下发进 guest 沙箱。** 用户已明确接受该行为变化。仍保留"识别但不吸收（从 guest 移除）"语义的只剩 `AZURE_OPENAI_API_KEY`。
- **无行为影响的改动**：`pkg/schedulers/engine_options.go` 的 `schedulerSecretEnvName` 中删除这两个名字是**语义等价**的 —— 其上方的 `*_KEY` 后缀判断早已覆盖它们（现有测试 `pkg/schedulers/engine_bindings_coverage_test.go` 通过 `API_KEY`/`LLM_API_KEY` 覆盖该分支）。
- **文档已同步**：`README.md`、`README.zh-CN.md`、`docs/pages/{agent-compose-yaml-manual,command-line-manual,guest-image-abi}.md` 及 zh 变体、`docs/spec/dynamic-workflow-spec.md`、`docs/design/` 下 13 份文档、两个 runtime README、`runtime/agent-compose-runtime-sdk/README.md`。`grep -rn -i gemini` 在生产代码与 `docs/`、`README*.md` 中已无命中；仍有命中的只有 `.github/workflows/notify-dingtalk-release.yml` 的保留项、断言 gemini 被拒绝或被禁止的测试文件（`*_test.go`、`runtime/javascript/test/provider.test.ts`、`scripts/tests/test-image-ci-contract.sh`），以及本审计文件自身。
- **唯一保留项**：`.github/workflows/notify-dingtalk-release.yml:96` 的发布说明翻译提示词把 "Gemini" 列在"需保持原样的技术产品名"中。它不声明支持，且 release notes 里可能出现"移除 Gemini provider"这类文本，故**有意保留**；如需彻底清除可一并删除该词。
- **破坏性标注**：清理提交的 subject 带 `!`（`refactor(providers)!: remove the gemini agent provider`），footer 写明 `BREAKING CHANGE`：`provider: gemini`/`gemini-cli`/`gemini_cli` 一律被拒，且 `GOOGLE_API_KEY`/`GEMINI_API_KEY` 不再被识别或剥离。release notes 因此会把它归入破坏性变更。
- **回归测试**：`internal/projects/agent_definition_normalize_test.go`（四种 gemini 拼写全部被拒）、`pkg/driver/types_test.go` 与 `pkg/agentcompose/api/project_spec_redaction_test.go`（两个 Google 变量名保持可见）、`internal/projects/llm_credential_warnings_test.go`（告警文案是 passed through）、`pkg/execution/agent_resume_trace_coverage_test.go`（gemini 无 thread log roots）、`pkg/driver/runtime_mount_manifest_test.go`（无 gemini 挂载项）、`runtime/javascript/test/provider.test.ts`（运行时拒绝 gemini）、`scripts/tests/test-image-ci-contract.sh`（镜像禁止 gemini 字样）。

## 九、修复顺序与实际落地

原计划的修复优先级如下，本分支按同一顺序落地：

1. **先修高危及误导性**：W1、W2（已删除的迁移工具仍教用户下载运行）；W3（与 CI 策略相反的 proto 提交指引）；W5、W6、W7（照抄即命令失败的 CLI 手册示例）；W8、W11、W15（"未实现/不校验"等与代码相反的结论）。
2. **再做跨文档一致性扫描**：I1/O1–O6（k8s 驱动）；I2–I5/O18–O21（dsh provider）；I23（"两个子命令"）。这几类同根因，一次性批量修正。
3. **然后修"已实现写成未实现"**：O7–O12、P1–P3。
4. **最后补全性缺陷**：第五、六节的遗漏项（CLI 参数表、SDK README API 覆盖、`docs:build` 的 `generates` 列表）。

**原报告留作代码侧问题的两项已在审计完成后由上游修复**，本分支第二次变基时纳入，文档随之更新：

- `cmd/agent-compose/cli_resource_reference.go` 的 `run -i --prompt` provider 白名单已由 PR #726 补齐 `dsh`（`interactivePromptProviders` 同时用于错误信息），交互式 gate 测试改用真正不支持的 `aider`，手册该行已改为列出 DSH 及其别名。
- `DSH_VERSION` 已由 PR #727 接入 `Taskfile.yml` 与 `scripts/build-agent-compose-guest.sh`；guest ABI 文档中关于 task 不转发该变量的说明已删除，否则会与代码相反。
- 建议在 `docs:build` 或 CI 中增加一条"runtime driver / agent provider 枚举一致性"检查，防止这两类漂移再次发生。

## 十、审计与清理过程说明

- 审计基于 `origin/main` @ `2fb4e847`；上游随后两次前进：先合入 10 个提交（至 `435717ae`），再合入 PR #726/#727（至 `8eb9b7a7`）。本分支第一次变基到 `435717ae` 时只与 4 个文件重叠且无冲突；第二次变基到 `8eb9b7a7` 时有 7 个文件冲突，逐个人工合并：保留上游新增的 `dsh` 交互式支持与 `DSH_VERSION` 转发，同时删除 `GEMINI_CLI_VERSION`、`gemini` provider（含测试里的用法），以及关于 task 不转发 `DSH_VERSION` 的过时说明。
- 上游新增的公开文档（事件 payload 提示块、`include_event`、`EVENT_DELIVERY_SCOPE`）已逐条对照 `internal/projects/scheduler_event_prompt.go`、`pkg/compose/normalize.go`、`pkg/config/event_delivery.go` 复核，**未发现新的不准确之处**；`docs:build` 的 schema 覆盖从 73 个字段增至 74 个，两份手册均已覆盖新字段。
- `docs/design` / `docs/spec` 的全部结论在审计阶段固定到 `origin/main`（`2fb4e847`）验证，因此不受工作树并发编辑影响；`docs/pages` 的结论基于工作树当前行号。
- 清理阶段按文件所有权分片并行执行（5 个互斥文件集，共 45 个文件，含本审计文件），避免并发编辑互相覆盖；每片均要求先回代码复核再改，并回报无法验证或拒绝修改的条目。
- 一处子代理结论已证伪（见第二节），请勿据此修改代码。修复过程中另有两条子代理观察被复核后**否决**：runtime contract 的"两个协议标记"表述准确；`O18`/`I24` 已由 Gemini 清理提交修复，无需二次改动。
- 审计本身未修改任何被审计文档；Gemini 清理、审计报告、文档清理分属三个独立提交，便于单独回退。

## 十一、审计之后的补充修正（第二次变基新增）

第二次变基到 `8eb9b7a7` 后，又针对本报告未覆盖的位置做了一轮复核，结果如下（均为文档、测试或注释，无生产行为变化）：

| 位置 | 问题 | 处理 |
| --- | --- | --- |
| `docs/design/llm_model_routing_redesign.md:249-251` | 仍把 Google 列为"识别但不可吸收"，并称其 name 在剥离名单上 | 该类别改为只有 Azure，并说明 `GOOGLE_API_KEY`/`GEMINI_API_KEY` 已归入不可识别类别 |
| `README.md:289`、`README.zh-CN.md:238` | k8s 写成 daemon 必须运行在目标集群内，并只能用 Helm Chart 部署 | 改为按 kubeconfig 构建 client（`~/.kube/config`、集群内配置是最后回退）、Pod 经 `K8S_RUNTIME_BASE_URL` 访问 daemon，Helm Chart 是支持的安装入口 |
| `docs/spec/core-e2e-test-strategy-spec.md:24` | "三种 runtime driver" | 四种（补 `k8s`） |
| `docs/pages/guest-image-abi.md:9-10,84,499`、zh:6,56,363 | 能力描述漏 DSH、"three runtime drivers" 与示例 `mkdir` 漏 `/root/.dsh` | 分别补 DSH、four、`/root/.dsh` |
| `docs/pages/command-line-manual.md:951`、zh:929 | `--auth` 只列 `x-api-key`/`bearer` | 补 `protocol-default`（`cmd/agent-compose/cli_llm_provider.go:298-302` 接受该值） |
| `docs/design/llm_provider_catalog_design.md:7` | provider 列表漏 DSH | 补 DSH |
| `docs/design/agent_system_prompt_design.md:17` | 移除 Gemini 后仍写 "all six runners" | 改为 all five runners（Codex、Claude、OpenCode、Pi、DSH） |
| `docs/pages/index.html:2808-2817` | Runtimes 卡片与 chip 行漏 k8s | 中英属性、可见文本与 chip 四处补齐 |
| 本文件 | 文件数/增删统计、"73 个字段"、`grep gemini` 结论、W6/W8/O2 的行号与表述 | 逐条按实测改正 |

新增测试只用于钉住这次破坏性变更：gemini 的各种拼写被拒、Google 两个变量名不再被识别或脱敏、镜像构建不再包含 gemini CLI 或相关凭据名、guest 不再有 gemini 挂载项与 thread log roots。
