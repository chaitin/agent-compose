# agent-compose 核心业务 E2E 测试体系技术规格

## 背景与目标

agent-compose 是负责 project、run、sandbox、runtime driver、workspace、scheduler、loader、事件、镜像、缓存、Jupyter 和 LLM facade 的控制面。核心用户工作流跨越 CLI、Connect/HTTP API、SQLite 与文件持久化、后台 manager、guest runtime 和外部依赖边界，单包测试或使用 fake 的多组件测试无法证明部署后的完整流程可用。

仓库当前通过测试函数名称中的 `Integration` 和 `E2E` 区分测试形态，并在 `scripts/test-coverage.sh` 中分别计算 unit、integration、E2E 和 combined statement coverage。当前约有 77 个 `TestE2E...` 分散在 `cmd/`、`pkg/` 和 `test/e2e`：其中多数是复用 unit/integration helper 的 coverage-shape wrapper，只有少量测试真正启动 daemon、Docker sandbox 或后台 scheduler。现有 E2E statement coverage 因此主要反映代码执行数量，而不是用户可观察业务流程的完整性。

本规格定义一套真实、可重复、可诊断的核心业务 E2E 测试体系。目标状态：

- 真实 E2E 只表示通过生产形态 daemon 和公开 CLI、Connect、HTTP 边界完成的用户工作流。
- 使用 fake、stub、单 handler 或直接调用内部 controller 的测试归入 unit 或 integration，不再以 E2E 名称重复执行。
- Docker、BoxLite、Microsandbox 对 runtime 相关核心业务场景提供等价覆盖；driver 无关的控制面流程不做无意义的三倍重复。
- 主要用户流程通过编译后的 CLI 驱动；流式协议、兼容 API、代理、状态与错误语义通过 Connect/HTTP、持久化文件和 runtime inspect 精确断言。
- 完整确定性 E2E 仅手动运行，不作为 pull request 阻塞门禁；在运行环境准备完成后，目标总时长不超过 30 分钟。
- Git、registry、LLM、webhook 和 capability 等外部边界使用本地协议级 mock；真实模型服务只进入独立 live canary。
- 测试失败必须保留足够的脱敏诊断信息，测试结束必须确认没有遗留 daemon、sandbox runtime、网络、socket、端口或挂载。

## 现状和 harness 约束

### 项目 harness

- `AGENTS.md` 规定主入口为 `cmd/agent-compose/main.go`、`pkg/agentcompose/app/`、`pkg/agentcompose/api/`、`pkg/agentcompose/adapters/`、`pkg/agentcompose/proxy/` 和 owner packages；测试方案必须覆盖这些边界的实际协作，而不是绕过 service graph。
- `AGENTS.md` 规定支持 `docker`、`boxlite`、`microsandbox` 三种 runtime driver，默认 driver 是 Docker；完整 E2E 必须显式记录被测 driver，不得把 Docker 结果视作其他 driver 的替代证明。
- `TESTING.md` 将 unit、integration、E2E 定义为三种互补测试形态，并要求跨 API、持久化、runtime driver 或用户工作流的变更具有更宽的测试覆盖。
- `Taskfile.yml` 的主门禁为 `task lint`、`task build`、`task test`；现有 runtime 真实 smoke 通过 `task test:runtime-smoke` 和 `SMOKE_RUNTIME_DRIVERS` 显式启用。
- `.github/workflows/ci.yml` 当前在 GitHub-hosted runner 上执行 lint、Go tests、coverage、runtime SDK、scheduler runtime 和 proto-client 构建，不准备 KVM runtime 产物或完整 guest image，因此不具备稳定运行三 driver 真实 E2E 的前提。
- `docs/design/agent-compose_design.md` 定义 daemon、v1/v2 API、project/run pipeline、sandbox/runtime、loader、LLM、image/cache 和持久化边界；本规格中的业务场景以这些已实现能力为准。
- `docs/design/agent-compose-runtime_contract.md` 定义 guest runtime 的 workspace、state、runtime、home、stdio、provider 和 resume 合同；driver 等价场景必须验证该合同，而不只验证 runtime 进程启动。

### 当前测试事实

- `scripts/run-go-test-shape.sh` 通过函数名包含 `Integration` 或 `E2E` 选择测试；同一个 helper 被不同 shape wrapper 重复调用时仍会被分别计入覆盖率。
- `scripts/test-coverage.sh` 当前要求 unit、integration、E2E statement coverage 均不低于 60%，combined 不低于 70%。真实外部 daemon 子进程默认不会把覆盖数据写回父 `go test` 的 coverprofile。
- `cmd/agent-compose/e2e_docker_scheduler_test.go` 能启动完整 service graph、使用真实 Docker guest、通过 CLI 应用项目，并等待 scheduler run 完成，是当前接近真实 E2E 的基线。
- `test/e2e/docker_jupyter_host_daemon_test.go` 能启动外部宿主机 daemon，通过 Connect API 创建 Docker Jupyter sandbox，并验证 stale port 在 stop/resume 后由 Docker inspect 修复。
- `test/e2e/docker_workspace_resume_host_daemon_test.go` 中的 `TestE2EDockerFileWorkspaceResumePreservesState` 已通过正式 Connect/HTTP API 和真实 Docker guest 验证 file workspace 一次性 provisioning、宿主机 daemon 重启、原 runtime handle 复用、ready workspace 状态保持、新 sandbox 获取最新 source、无反向同步以及资源泄漏清理。该证据仅适用于宿主机 daemon + Docker，不代表 BoxLite/Microsandbox 等价。
- `test/e2e/api_smoke_test.go` 只注册了一个临时 Echo `/api/version` handler，不代表真实 daemon E2E，应重新归类或由真实 daemon health 场景替代。
- `pkg/driver` 下的 BoxLite、Microsandbox 和 Docker smoke 已覆盖部分启动、挂载和 writable layer 行为，但没有通过 project/run/CLI/API 控制面执行完整业务流程。

### 约束结论

- 真实 E2E 的主要质量指标必须是业务场景矩阵，而不是依靠 coverage wrapper 达成的 statement 百分比。
- unit、integration 和 combined coverage 仍是默认质量门禁；真实 E2E statement coverage 单独采集和报告，首版不设置百分比阻塞阈值。
- 完整 E2E 所需的 guest image、BoxLite/Microsandbox runtime 产物和 KVM 环境由显式 prepare 阶段提供，不得在普通 `task test` 中隐式构建或下载。
- 前端工程、浏览器登录和跨仓库 UI 工作流不属于本仓库 E2E；`runtime/javascript` 是 guest runtime 组成部分，继续纳入本仓库测试。

## 核心概念

### 真实 E2E

真实 E2E 是从用户可见入口驱动生产形态系统的测试。一个测试必须至少满足：

- 启动真实 daemon service graph 和 background managers，或连接由测试创建的容器化 daemon。
- 使用编译后的 CLI、生成的 Connect client 或正式 HTTP 路由，不直接调用内部 controller 完成被测操作。
- 当场景涉及 runtime 时，启动真实 Docker、BoxLite 或 Microsandbox guest。
- 断言用户可观察结果，并在需要时用持久化文件、SQLite 或 runtime inspect 验证跨边界一致性。

仅使用 fake runtime、fake store、`httptest` 单 handler、纯映射函数或直接 controller 调用的测试不是 E2E。

### 场景

场景是一个具有稳定 ID、前置条件、执行入口、预期结果、适用 driver 和适用 topology 的用户工作流。场景是 E2E 覆盖率的最小单位。每个场景必须声明：

- 场景 ID 和业务领域。
- P0 核心或 P1 扩展等级。
- 适用的 `docker`、`boxlite`、`microsandbox` driver。
- 适用的宿主机 daemon 或容器化 daemon topology。
- 所需本地 mock 和 runtime capability。
- 最大执行时间和清理责任。

### Driver 等价合同

Driver 等价合同表示同一 project/run/sandbox/Jupyter/workspace 工作流在三个 runtime driver 上产生相同的控制面结果和 guest 可观察行为。底层容器、microVM、端口转发和镜像准备方式可以不同，但以下结果必须一致：

- run、sandbox、cell 和 event 状态。
- workspace、state、runtime 与 home 挂载合同。
- stdout、stderr、exit code、artifact 和日志语义。
- stop、resume、remove、daemon restart 与 runtime missing 行为。
- Jupyter readiness 和统一 proxy 入口。

只有设计文档明确声明为 driver-specific 或 unsupported 的能力可以采用差异化断言；差异必须在场景清单中显式记录，不能通过 skip 隐藏。

### 部署拓扑

- 宿主机 daemon：daemon 进程与 runtime 端口转发位于宿主机网络命名空间。
- 容器化 daemon：daemon 运行于 agent-compose 容器，并通过 Docker socket 和 `/dev/kvm` 管理 guest runtime。

完整业务矩阵主要在宿主机 daemon 上运行。容器化 daemon 对每个 driver 执行部署合同子集：health、command run、Jupyter、stop/resume 和 daemon restart，避免把所有 driver 无关控制面场景与 topology 做笛卡尔积。

### 确定性 Mock

确定性 mock 是由 E2E harness 启动、实现真实协议但不访问公网的本地服务：

- Git fixture repository。
- OCI/Docker registry。
- OpenAI Responses、Chat Completions 和 Anthropic Messages endpoint。
- webhook receiver 和 event publisher。
- capability gateway/catalog endpoint。

Mock 必须对 host daemon、容器化 daemon 和三个 guest runtime 可达，并记录经过脱敏的请求以供断言。

### Live Canary

Live canary 是使用真实 provider 凭证运行的独立手动测试。它只验证 provider 连接、基本 prompt、流式输出和 thread resume，不承担确定性回归门禁，也不影响 `task test:e2e` 的结果。

### 测试现场

测试现场是单个场景拥有的隔离资源集合：data root、sandbox root、socket、TCP port、Docker network、daemon process/container、project、sandbox、runtime、mock 服务和 coverage 目录。现场默认在测试结束时清理；设置 keep-tmp 时只在失败后保留。

## 架构和组件边界

### E2E Harness

`test/e2e` 是真实 E2E 的唯一 owner。公共 harness 负责：

- 构建或定位 instrumented daemon binary。
- 启动宿主机 daemon 或容器化 daemon。
- 创建隔离配置、端口、socket、data root 和 Docker network。
- 调用编译后的 CLI，并解析 text、JSON、stream 和 exit code。
- 创建 v1/v2 Connect、health、HTTP、Jupyter 和 WebSocket client。
- 启动本地 Git、registry、LLM、webhook 和 capability mock。
- 按 driver 准备环境并执行统一场景函数。
- 注入 daemon stop、kill、restart、持久化 state 篡改和 runtime missing 故障。
- 收集日志、状态、inspect、coverage 和场景报告。
- 逆序清理所有资源，并执行泄漏审计。

Harness 不复制业务实现，不直接修改 SQLite 以构造正常业务状态。只有验证恢复语义的故障注入场景可以在 daemon 停止后修改持久化文件或数据库，并必须记录修改前后的状态。

### CLI 驱动层

CLI 是项目、run、sandbox、exec、logs、image、cache 和 volume 用户流程的主驱动入口。Harness 以独立进程调用测试构建的 `agent-compose` binary，并显式设置 `--host`、`--file`、`--json` 或测试 Unix socket。

CLI 断言覆盖：

- exit code、stdout、stderr 和 JSON wire shape。
- command help 中承诺的主流程选项。
- deprecated alias 和稳定错误提示。
- CLI 与 Connect 查询结果的一致性。

### API 断言层

Connect/HTTP 用于 CLI 不适合表达的精确断言：

- v1 compatibility service。
- v2 streaming、attach、watch 和 follow logs。
- Jupyter HTTP/WebSocket proxy。
- workspace upload/download。
- runtime LLM facade。
- webhook/event ingress。
- 稳定 Connect code 和 response field。

API 断言不能替代对应的 CLI happy path；两者用于验证不同边界。

### Runtime Driver 层

每个 driver 在独立测试进程中运行相同的核心场景集合，避免进程级环境变量、cgo runtime、socket 和 KVM 状态互相污染。每个 driver adapter 负责：

- 返回 driver 名称和 capability。
- 提供 guest image、runtime home、动态库和必要环境变量。
- 检查 runtime readiness。
- 查询实际 runtime handle、端口、挂载和状态。
- 在测试结束时移除 runtime 资源。

Driver adapter 只处理基础设施差异，不改变业务场景断言。

### Coverage 与报告

E2E daemon 使用 `go build -cover -coverpkg=./cmd/...,./pkg/...` 构建。每个 daemon process 设置独立 `GOCOVERDIR`；测试结束后使用 `go tool covdata merge` 和 `go tool covdata textfmt` 生成分 driver、分 topology 和合并 coverprofile。

场景 runner 同时生成：

- 人类可读汇总。
- JSON 场景矩阵，包含 pass、fail、duration、driver、topology 和 artifact path。
- JUnit 报告，便于未来接入自动执行环境。
- statement coverage 报告，首版仅用于观察真实工作流触达范围。

`runtime/javascript` 的 TypeScript coverage 继续由现有 Vitest 流程负责；guest 内真实 provider 行为通过场景结果验证，不把浏览器前端 coverage 混入本仓库指标。

## API、CLI、配置和数据模型

### 产品接口

本规格不修改：

- `proto/agentcompose/v1` 和 `proto/agentcompose/v2` wire contract。
- daemon 的公开 HTTP 路由。
- CLI 用户命令或 flag。
- SQLite schema、sandbox 文件布局或 runtime protocol。
- 应用部署环境变量默认值。

如果 E2E 暴露现有接口不一致，应作为产品缺陷单独修复，不能在测试 harness 中加入兼容分支掩盖。

### Task 接口

目标态完整 harness 的测试任务定义为：

- `task e2e:prepare`：构建 daemon、guest image、BoxLite 和 Microsandbox runtime 产物。该任务不计入 30 分钟执行预算。
- `task test:e2e`：运行全部确定性真实 E2E，默认包括三个 driver 和规定的两种 topology。
- `task test:e2e:canary`：运行显式选择的 live provider canary。

当前已存在的 Docker 聚焦入口独立于上述未来完整矩阵：

- `task test:e2e:docker-jupyter` 保留为 Jupyter lifecycle 聚焦诊断入口。
- `task test:e2e:docker-workspace-resume` 运行 `TestE2EDockerFileWorkspaceResumePreservesState`，使用已存在的本地 guest image 完成宿主机 daemon + Docker workspace provisioning 验收；该 task 不执行 BoxLite、Microsandbox、容器化 daemon topology 或其他尚未实现的核心 E2E 场景。

`task test` 不运行真实 runtime E2E，继续作为 PR 的 unit、integration、runtime JavaScript 和 combined coverage 门禁。

### 测试配置

测试专用环境变量不属于 daemon 公共配置：

| 变量 | 默认 | 语义 |
| --- | --- | --- |
| `E2E_DRIVERS` | `docker,boxlite,microsandbox` | 选择被测 driver |
| `E2E_TOPOLOGIES` | `host,container` | 选择 daemon topology |
| `E2E_GUEST_IMAGE` | `agent-compose-guest:latest` | 兼容三 driver 的 guest image |
| `E2E_KEEP_TMP` | 空 | 失败时保留现场和诊断数据 |
| `E2E_CANARY_PROVIDERS` | 空 | live canary provider 列表 |
| `E2E_TIMEOUT` | `30m` | 完整确定性套件硬超时 |

BoxLite、Microsandbox 的 runtime path、library path 和 image registry 继续使用现有项目环境变量，由 `e2e:prepare` 产物和 driver adapter 注入。

### 场景清单

场景清单与测试代码共同维护。每个清单项必须映射到唯一测试函数或 table case；不存在实现的 P0 清单项使完整 E2E 失败。清单不新增产品持久化数据，只属于测试报告输入。

## 工作流和失败语义

### 环境准备

`e2e:prepare` 负责：

- 构建普通与 instrumented daemon binary。
- 构建或确认 `agent-compose-guest:latest`。
- 准备 BoxLite cgo headers、library 和 runtime artifact。
- 准备 Microsandbox binary、library 和 runtime artifact。
- 检查 Docker Engine、KVM、磁盘空间和本地端口能力。

完整 E2E 开始后不隐式拉取公网镜像或构建大型 runtime 产物。

### 确定性套件

1. Runner 执行全局预检；缺少已选择 driver 的依赖时立即失败。
2. 为 driver 和 topology 创建独立 worker，最多并行运行三个 driver worker。
3. 每个 worker 启动隔离 daemon 和本地 mock，并等待 health/version ready。
4. CLI 应用 fixture project，执行场景清单中的用户流程。
5. Connect/HTTP、持久化 state 和 runtime inspect 验证跨边界一致性。
6. 故障场景在明确的持久化边界停止或杀死 daemon/runtime，再从相同 data root 恢复。
7. Worker 输出场景结果与 coverage，清理资源并执行泄漏审计。
8. Runner 合并报告；任何 P0/P1 场景失败、超时、未实现或意外 skip 都使任务失败。

### 外部依赖 Mock

- Mock 只绑定测试可控地址，不访问公网。
- Guest 通过 harness 创建的共享网络或明确 guest-reachable URL 访问 mock。
- Mock 验证 method、path、headers、schema、stream 和 retry，不依赖固定请求顺序以外的偶然实现细节。
- Mock 响应支持成功、限流、超时、断流、非法 payload 和上游错误。
- 请求日志必须删除 Authorization、API key、facade token、Jupyter token 和 secret env 值。

### Live Canary

- 仅在 `E2E_CANARY_PROVIDERS` 非空且对应凭证存在时执行。
- 每个 provider 只执行最小 prompt、流式输出和一次 resume。
- Canary 不运行破坏性故障注入，不读取用户仓库或上传 artifact。
- 缺少凭证时 canary 明确报告未选择，不影响确定性套件。

### 超时和取消

- 完整确定性套件硬超时 30 分钟。
- 普通场景默认不超过 2 分钟；更短操作使用更小 deadline。
- scheduler 场景使用 timeout、event 或手动 trigger，不等待真实整分钟 cron 边界。
- 超时必须同时取消 CLI/API request、停止 daemon operation 并进入清理流程。

### 失败诊断

失败场景收集：

- CLI stdout、stderr 和 exit code。
- daemon structured log。
- sandbox metadata、VM state、proxy state、Jupyter log 和 run transcript。
- Docker inspect 或对应 microVM runtime 状态。
- SQLite 中相关 project/run/loader/event 摘要。
- mock 收到的脱敏请求。
- goroutine/process/container/socket/network 泄漏清单。

默认只在失败时保留摘要；`E2E_KEEP_TMP=1` 时保留完整现场路径。

### 清理与泄漏

清理按创建顺序逆序执行：run、sandbox、runtime、project、daemon、mock、network、临时目录。正常 API 清理失败时，harness 使用已记录的 runtime ID 做兜底删除，但仍将清理失败记入测试结果。

测试结束必须确认：

- daemon process/container 已退出。
- Docker container、BoxLite/Microsandbox handle 已删除。
- 测试 Docker network、Unix socket 和监听端口已释放。
- 没有测试创建的挂载或临时目录留在非 keep-tmp 路径。

## 核心业务场景矩阵

### Daemon、传输和安全

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `DAE-001` | 宿主机 daemon 通过 Unix socket 和 TCP 返回 status、version、health | 控制面一次 |
| `DAE-002` | TCP BasicAuth 拒绝缺失/错误凭证并接受正确凭证；本地 Unix socket 保持可信访问语义 | 控制面一次 |
| `DAE-003` | daemon 优雅退出后从相同 data root 重启，project/run/sandbox 查询保持一致 | 三 driver |
| `DAE-004` | 容器化 daemon 完成 health、command、Jupyter、stop/resume 和 restart | 三 driver |

### Project 和配置

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `PRJ-001` | `config` 和 `ValidateProject` 对合法 compose 给出一致规范化结果 | 控制面一次 |
| `PRJ-002` | 非法变量、driver、workspace、scheduler 配置返回稳定问题路径且不产生持久化副作用 | 控制面一次 |
| `PRJ-003` | 首次 `up` 创建 project/agent/scheduler，重复 `up` 幂等，spec 修改产生新 revision | 控制面一次 |
| `PRJ-004` | `ls`、`inspect project/agent` 与 API 状态一致；`down` 停止相关 sandbox 并禁用 scheduler | 三 driver |
| `PRJ-005` | global/project/agent/run env 按优先级合并，secret 不出现在 CLI、日志或持久化明文视图 | 三 driver |

### Run、Exec 和日志

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `RUN-001` | 同步 command run 返回 stdout、stderr、exit code、result、artifact 和 succeeded 状态 | 三 driver |
| `RUN-002` | stream run 返回 started/output/completed，stdout/stderr 顺序和最终持久化结果一致 | 三 driver |
| `RUN-003` | detach/start/get/follow logs/stop 覆盖运行中、取消和终态查询 | 三 driver |
| `RUN-004` | 默认 stop、keep-running、remove-on-completion 三种 cleanup policy 行为正确 | 三 driver |
| `RUN-005` | `--sandbox` 复用已有 sandbox，workspace 和 writable layer 保留，run 关联更新 | 三 driver |
| `RUN-006` | command 失败、workspace 失败、runtime 启动失败、执行超时均持久化稳定失败状态 | 三 driver |
| `EXE-001` | 按 sandbox、run 和 project/agent selector 执行命令，歧义 selector 返回稳定错误 | 三 driver |
| `EXE-002` | cwd、env、stdin EOF、stdout/stderr 和非零退出码合同一致 | 三 driver |
| `EXE-003` | attach 支持 stdin、TTY、resize、cancel；不支持的 capability 返回明确 unsupported | 三 driver，按 capability 断言 |
| `LOG-001` | `logs` 的 agent/run/sandbox 过滤、tail、follow 和 JSON 输出与 transcript 一致 | 三 driver |

### Sandbox 和 runtime 生命周期

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `SBX-001` | 创建 sandbox 后 metadata、VM state、mount manifest 和 runtime 状态一致 | 三 driver |
| `SBX-002` | stop/resume 复用底层 runtime，并保持 workspace、state、home 和 writable layer | 三 driver |
| `SBX-003` | remove 和 prune 删除匹配资源；dry-run 不产生副作用；活动资源受保护 | 三 driver |
| `SBX-004` | daemon restart 后运行中和停止 sandbox 可查询、可恢复、可代理 | 三 driver |
| `SBX-005` | 已停止 sandbox 的底层 runtime 丢失时拒绝无状态重建并返回可诊断错误 | 三 driver |
| `SBX-006` | 同时创建至少两个 sandbox，ID、runtime ref、端口、workspace 和输出互不串扰 | 三 driver |
| `SBX-007` | stats 返回稳定指标或设计声明的 unsupported，不返回模糊内部错误 | 三 driver，按 capability 断言 |

`TestE2EDockerFileWorkspaceResumePreservesState` 通过 `task test:e2e:docker-workspace-resume` 为 `SBX-002`、`SBX-004` 和 `SBX-006` 提供宿主机 daemon + Docker 的聚焦证据：它跨 daemon 重启复用原 sandbox ID 和精确 Docker container ID，并证明第二个 sandbox 使用独立 runtime/workspace。这不使上表的“三 driver”条目成为已完成；BoxLite、Microsandbox 和完整 topology 证据仍属未来核心 E2E 矩阵。

### Jupyter 和 Kernel

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `JUP-001` | 启用 Jupyter 后 host/guest port、token、proxy path 和 readiness 正确 | 三 driver |
| `JUP-002` | 稳定入口 redirect、kernelspec HTTP 和 kernel WebSocket proxy 可用 | 三 driver |
| `JUP-003` | Shell、Python、JavaScript cell 执行、stream、cell list 和 artifact 持久化正确 | 三 driver |
| `JUP-004` | stop/resume、daemon restart、stale host port 和不可达 target 能通过统一入口恢复 | 三 driver |
| `JUP-005` | 两个并发 Jupyter sandbox 使用不同映射并访问各自实例 | 三 driver |

### Workspace 和 Volume

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `WKS-001` | local workspace 在 project/run/sandbox 中准备到正确 guest cwd | 三 driver |
| `WKS-002` | 本地 Git fixture 的 branch/commit checkout 正确，失败 clone 不留下错误运行状态 | 三 driver |
| `WKS-003` | file workspace 的 list/upload/download 保持内容和权限，拒绝路径穿越与超限请求 | 控制面一次，并在三 driver 挂载验证 |
| `WKS-004` | Workspace Source 只在创建 sandbox 时 seed 一次；ready workspace 在 stop/resume 和 daemon restart 后保留修改、删除与新增文件，新 sandbox 获取最新 source 且 sandbox 变更不反向同步 | 三 driver 目标；Docker/宿主机聚焦验收已实现 |
| `VOL-001` | volume create/list/inspect/remove/prune 和引用保护语义正确 | 控制面一次 |
| `VOL-002` | volume 挂载可读写，并跨 run、stop/resume 和 sandbox reuse 保持数据 | 三 driver |

`WKS-004` 的当前 Docker 聚焦验收由 `TestE2EDockerFileWorkspaceResumePreservesState` 实现，并由 `task test:e2e:docker-workspace-resume` 运行。它通过两个使用相同 data root/sandbox root 的真实宿主机 daemon 进程和真实 Docker guest 证明：

- sandbox A 在 stop 和 daemon restart 后保持原 sandbox ID、精确 Docker container ID 和 workspace path，resume 没有重建 runtime。
- sandbox A 的已修改文件保持 agent 版本、已删除模板文件不复活、新生成文件仍存在，直接验证 ready workspace 未被重新 seed。
- 在 source 模板更新后创建的 sandbox B 获取最新 source 版本和模板中的已删文件，但不包含 sandbox A 的 generated artifact。
- 在 sandbox A 变更后以及 sandbox B 创建后，workspace list/download API 仍返回 source 版本，证明没有 sandbox 到 Workspace Source 的反向同步。
- 测试在第二个 daemon 存活时通过公开 API 删除两个 sandbox 和 workspace config，使用精确 Docker label 作为兜底，并断言 container、daemon process、Unix socket、TCP port 和临时 root 无泄漏。

这一实现是 workspace provisioning 状态保持的 Docker-only 必需验收，不建立 BoxLite/Microsandbox 等价性，也不代替未来完整的三 driver/两 topology 核心 E2E 矩阵。它对 `WKS-003` 只证明 list/download 内容与无反向同步这一子集，不单独证明权限、路径穿越或超限请求的完整合同。

### Scheduler、Loader、Event 和 Webhook

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `SCH-001` | declarative timeout、interval、event 和手动 trigger 创建正确 managed loader/run | 三 driver |
| `SCH-002` | scheduler script 的 shell、exec、state 和 event publish 行为正确 | 三 driver |
| `SCH-003` | sticky/new sandbox policy 分别复用或创建 sandbox，binding 与 run link 正确 | 三 driver |
| `SCH-004` | scheduler command/agent/LLM 的成功、失败、timeout 和 structured output 正确 | 三 driver，使用本地 mock |
| `EVT-001` | webhook ingress 创建 topic event 并触发 event scheduler，重复事件按合同处理 | 控制面一次，run 在三 driver 验证 |
| `EVT-002` | webhook dispatcher 对 2xx、5xx、timeout 执行 ack/retry，并持久化可查询事件 | 控制面一次 |
| `LOD-001` | loader validate/CRUD/enable/run-now/list runs/events 与 managed loader 隔离 | 控制面一次 |

### Agent、LLM 和 Capability

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `AGT-001` | canonical agent 通过本地 OpenAI mock 完成 prompt、stream、artifact 和 thread resume | 三 driver |
| `AGT-002` | system prompt、capability guide 和 per-turn prompt 按 runtime contract 分层注入 | 三 driver |
| `LLM-001` | `LLMService.Generate` 与 scheduler LLM 支持 Responses 和 Chat Completions | 控制面一次 |
| `LLM-002` | structured output 成功、schema 不匹配、非法 JSON、upstream timeout/error 语义稳定 | 控制面一次 |
| `LLM-003` | runtime facade 支持 OpenAI responses/chat 和 Anthropic messages；provider-bound token 可透传请求模型和上游模型错误，providerless 兼容 token 保持原解析行为，provider 与入口 wire scope 仍受 token 约束 | 三 driver |
| `LLM-004` | facade token 不能跨 sandbox 使用，并在 stop/remove 后撤销 | 三 driver |
| `CAP-001` | capability catalog/status、sandbox guide 和 capability proxy 使用本地 mock 正确工作 | 三 driver |

### Image、Cache 和兼容 API

| ID | 场景 | Driver/Topology |
| --- | --- | --- |
| `IMG-001` | 本地 registry 的 pull/list/inspect/remove、query、platform 和冲突错误正确 | Docker/OCI store 各一次 |
| `IMG-002` | CLI build 生成可运行 guest image，并能被 project run 使用 | Docker 一次，产物供三 driver 消费验证 |
| `IMG-003` | Docker/BoxLite/Microsandbox 按各自解析规则消费 Docker 或 OCI image | 三 driver |
| `CAC-001` | cache list/inspect/prune/remove 的 dry-run、force、referenced/active/unknown 保护正确 | 各 cache domain |
| `V1-001` | v1 Session create/get/list/stop/resume/proxy 与内部 sandbox 状态一致 | 三 driver |
| `V1-002` | v1 Kernel、AgentDefinition、Config、Dashboard watch/stream 保持兼容字段和事件 | 控制面一次，runtime 流程三 driver |
| `ERR-001` | v1/v2/HTTP 对 invalid、not found、conflict、failed precondition、unsupported、unavailable 返回稳定分类 | 控制面一次 |

## 测试、质量门禁和验收标准

### 测试重新分类

- `cmd/` 和 `pkg/` 中仅复用 helper 或 fake 的 `TestE2E...` 重命名为 `TestIntegration...` 或普通 unit test。
- 删除只为覆盖率重复调用同一 helper 的 E2E wrapper；同一行为不在三个 shape 中机械重复。
- 真正 E2E 统一位于 `test/e2e` 并使用 `e2e` build tag，`go test ./...` 不隐式启动真实 runtime。
- runtime driver 单层 smoke 保留在 `pkg/driver`，用于更快定位 runtime 合同问题；业务 E2E 不复制其内部实现断言。

### 默认质量门禁

`task test` 继续阻塞：

- unit tests。
- integration tests。
- runtime JavaScript tests。
- unit、integration 和 combined statement coverage。

严格重新分类后，必须重新记录 unit/integration/combined baseline，再调整 `scripts/test-coverage.sh`。首版保持现有最低目标：unit 60%、integration 60%、combined 70%；不再要求通过伪 E2E 达成 60% statement coverage。

### 手动 E2E 门禁

`task test:e2e` 的通过条件：

- 场景清单中的所有 P0 和 P1 确定性场景均有实现并通过。
- 所有声明适用三 driver 的场景在 Docker、BoxLite、Microsandbox 上均通过。
- 所有声明适用 topology 的场景均通过。
- 没有意外 skip、超时或清理失败。
- 完整执行时间不超过 30 分钟，不包含 `e2e:prepare`。
- 场景报告、JUnit 和 coverage artifacts 成功生成。
- 泄漏审计通过。

### 重复性验收

- 在同一开发机连续执行两次 `task test:e2e`，第二次不得依赖第一次遗留状态，也不得因固定端口、容器名、socket 或缓存冲突失败。
- 使用 `E2E_DRIVERS` 或 `E2E_TOPOLOGIES` 过滤运行时，未选择场景标记为 filtered，不标记为 skip 或 pass。
- 开启 `E2E_KEEP_TMP=1` 只改变失败现场保留，不改变业务结果。

### 安全验收

- 报告、CLI 输出、daemon log 和 mock 请求记录中不得包含 API key、Authorization、facade token、Jupyter token 或 secret env 明文。
- 确定性套件不访问公网；任何意外外连使场景失败。
- Live canary 凭证仅通过环境注入，不写入 compose fixture、data root 或测试报告。

## 首版不做事项

- 不测试 `agent-compose-ui`、浏览器静态资源、OAuth、cookie session 或跨仓库前端部署；前端相关内容后续将从本仓库移除。
- 不把真实 E2E 加入 pull request 阻塞 CI，也不要求 GitHub-hosted runner 提供 KVM 和三 driver runtime。
- 不在确定性套件调用真实 OpenAI、Anthropic、Google、GitHub 或公共 registry。
- 不为每个 driver 重复纯控制面 CRUD 和映射场景；只重复 runtime 相关核心业务流程。
- 不把全部业务场景与全部 topology 做无差别笛卡尔积；容器化 daemon 使用明确的部署合同子集。
- 不在首版为真实 E2E statement coverage 设置百分比门禁；先建立可信场景和基线。
- 不通过 E2E 方案修改产品 API、数据 schema、runtime protocol 或产品配置默认值。

## 关键假设和已确认决策

- 完整 E2E 仅手动运行，不阻塞 PR。
- 当前伪 E2E 严格重新分类，不保留仅为满足 coverage 数字的 wrapper。
- Docker、BoxLite、Microsandbox 对核心 runtime 业务场景提供等价覆盖。
- CLI 是主用户入口，Connect/HTTP 用于精确断言流式、兼容、代理和错误语义。
- 完整确定性套件目标时长为 30 分钟，且不包含 guest image、BoxLite/Microsandbox 产物的一次性准备。
- 运行环境是 Linux amd64，使用本机 Docker Engine，并能访问 `/dev/kvm`。
- 阻塞场景使用本地协议 mock；真实 provider 只进入独立 live canary。
- 前端完全不在范围内，`runtime/javascript` 作为 guest runtime 继续在范围内。
- 测试专用任务和环境变量不是产品公开接口，不承担 daemon 配置兼容责任，但其命名和行为必须记录在 `TESTING.md`。
- 远程 `DOCKER_HOST`、非 Linux runtime 和无 KVM 环境不属于首版完整 E2E 支持范围。
