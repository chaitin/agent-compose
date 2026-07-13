# Workspace Resume 状态保持 Progress

本文档把 workspace resume 状态保持方案拆成可独立执行、独立验证的任务清单。任务按依赖顺序排列；标记为“可并行”的任务或子任务可以在依赖满足后并行推进，subagent 并发度最高不超过 5。

当前位置：项目根目录 `PROGRESS.md`。

## 文档索引

- 技术规格：[docs/spec/workspace-resume-preservation-spec.md](docs/spec/workspace-resume-preservation-spec.md)
- 实施计划：[docs/plan/workspace-resume-preservation-implementation-plan.md](docs/plan/workspace-resume-preservation-implementation-plan.md)
- Agent harness：[AGENTS.md](AGENTS.md)
- 测试标准：[TESTING.md](TESTING.md)
- 任务入口：[Taskfile.yml](Taskfile.yml)
- CI 门禁：[.github/workflows/ci.yml](.github/workflows/ci.yml)
- 英文设计文档：[docs/design/agent-compose_design.md](docs/design/agent-compose_design.md)
- 中文设计文档：[docs/zh-CN/design/agent-compose_design.md](docs/zh-CN/design/agent-compose_design.md)
- 核心 E2E 规格：[docs/spec/core-e2e-test-strategy-spec.md](docs/spec/core-e2e-test-strategy-spec.md)

## 当前状态

- 已确认：resume 严格保持 sandbox workspace；旧 sandbox 原样迁移；首版无 reset API；真实 runtime 使用 Docker E2E。
- 已完成文档：技术规格、实施计划。
- 代码任务：2/20 完成。
- 当前下一目标：2.1 实现 Provisioner 状态编排和并发控制。

## 执行规则

- [ ] 每轮只选择依赖已完成的第一个未完成父任务；父任务测试和验收未通过前不得勾选完成。
- [ ] 不跨阶段提前接入依赖未稳定的生产路径；同阶段明确标记“可并行”的任务除外。
- [ ] 任何 `ready` 路径触碰、覆盖、clone、copy 或 staging-promote 正式 workspace，立即停止并修复。
- [ ] production code 不得绕过 Provisioner 直接 materialize；静态审计发现双轨路径时不得进入下一阶段。
- [ ] Provisioner 返回错误时 runtime driver start 必须为零次；`ready` 持久化完成前不得启动 runtime。
- [ ] legacy 缺省状态必须原样迁移为 `ready`，不得按目录内容、VM status 或 config 可用性重建。
- [ ] 每个行为变更任务必须运行其最小测试；阶段收口任务运行对应 package/shape 门禁。
- [ ] integration 测试名包含 `Integration`；真实 E2E 只放在 `test/e2e` 并通过正式 API 驱动。
- [ ] 不通过 skip、降低断言、修改 coverage exclusion 或测试专用产品后门处理失败。
- [ ] 不修改 proto、生成客户端、SQLite schema、公开 CLI、环境变量、compose 或 runtime mount，除非先暂停并更新 spec。
- [ ] 保留用户已有改动；提交或推送不属于本账本默认动作，除非用户另行授权。
- [ ] 每个父任务完成后更新五段式完成总结，并把 `下一目标` 指向依赖已满足的下一个父任务。

## 1. Provisioning 持久化状态

参考：[实施计划阶段 1](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-1建立-provisioning-持久化状态)

- [x] 1.1 定义 provisioning 领域状态和转换规则
  - 依赖：无。
  - 工作内容：
    - 在 `pkg/model` 增加 `SandboxWorkspaceProvisioning`、version 1 和 `pending`、`ready`、`failed` 常量。
    - 在 `Sandbox` 增加可选 `workspace_provisioning` JSON 字段。
    - 实现集中校验/转换 helper，只允许 `pending -> ready|failed`、`failed -> pending`，拒绝 ready 回退、未知 version/status 和 nil 输入。
    - 每次合法转换更新 UTC `updated_at`。
  - 可并行子任务：
    - [x] 可并行：领域类型与 JSON round-trip 测试。
    - [x] 可并行：状态转换表测试和非法输入测试。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/model -count=1`。
  - 验收标准：合法转换表全部通过；非法转换不修改原状态；旧 JSON 缺少字段时正常解码为 nil。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 在 `pkg/model` 增加 version 1、`pending`、`ready`、`failed` 常量和 `SandboxWorkspaceProvisioning` 领域类型。
      - 在 `Sandbox` 增加可选 `workspace_provisioning` JSON 字段，并实现集中校验与状态转换 helper。
      - 合法转换使用 copy-on-success 更新 UTC `updated_at`；拒绝路径不修改原状态。
      - 增加 JSON round-trip、旧 JSON 兼容、完整转换表和非法输入测试。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test ./pkg/model -count=1`：通过。
      - `./scripts/with-go-toolchain.sh golangci-lint fmt --no-config --diff ./pkg/model`：通过。
      - `./scripts/with-go-toolchain.sh golangci-lint run --no-config --allow-parallel-runners ./pkg/model`：通过，`0 issues`。
      - `git diff --check`：通过。
    - 审计与例外：
      - 旧 metadata 缺少字段时解码为 nil，无 provisioning 时 JSON 省略该字段。
      - 未修改 proto、SQLite、公开 CLI、环境变量、compose、runtime mount 或生命周期生产路径。
      - 本任务仅建立领域状态；创建 sandbox 时初始化 `pending` 留给依赖任务 1.2。
      - 无例外或未运行的任务内门禁。
    - 下一目标：1.2 在 sessionstore 创建和持久化 pending 状态。

- [x] 1.2 在 sessionstore 创建和持久化 pending 状态
  - 依赖：1.1。
  - 工作内容：
    - 修改 `CreateSandboxWithOptions`：workspace snapshot、snapshot ID 或 workspace ID 存在时首次保存 `pending`。
    - 无 workspace 时保持字段缺省；Store load 不做 legacy 猜测或迁移。
    - 验证 save/load、Store 重建和 RemoveSandbox 对新状态的兼容。
  - 可并行子任务：
    - [x] 可并行：workspace snapshot/ID/empty 三种创建分支测试。
    - [x] 可并行：旧 metadata、Store 重建和 remove 测试。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/storage/sessionstore ./pkg/model -count=1`。
  - 验收标准：带 workspace 的 metadata 在任何 runtime 操作前已是 pending；旧数据无自动改写；无 workspace JSON 无新增字段。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - `CreateSandbox`/`CreateSandboxWithOptions` 在 workspace snapshot 指针存在或 workspace ID 非空时，于首次 metadata 保存前初始化 version 1 `pending` 状态和 UTC `updated_at`。
      - 无 workspace 的 sandbox 保持 provisioning 字段缺省；`loadSandbox` 不执行 legacy 猜测、迁移或写回。
      - 增加创建分支、原始 JSON、Save/Get、Store 重建、legacy 不改写和 remove/staging 清理测试。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test ./pkg/model ./pkg/storage/sessionstore -count=1`：通过。
      - `./scripts/with-go-toolchain.sh go test ./pkg/... -run 'Test.*Workspace.*Provision|Test.*Sandbox.*Persistence' -count=1`：通过。
      - `./scripts/with-go-toolchain.sh golangci-lint fmt --no-config --diff ./pkg/model ./pkg/storage/sessionstore`：通过。
      - `./scripts/with-go-toolchain.sh golangci-lint run --no-config --allow-parallel-runners ./pkg/model ./pkg/storage/sessionstore`：通过，`0 issues`。
      - `git diff --check`：通过。
    - 审计与例外：
      - 非 nil 空 snapshot、带 snapshot ID、仅 workspace ID 和无 workspace 分支均有直接 metadata 断言。
      - legacy metadata 经 `GetSandbox`/`LoadSandbox` 后字段仍为 nil，文件字节和 mtime 均不变。
      - `RemoveSandbox` 继续递归删除 sandbox root，测试包含未来 provisioning staging sentinel。
      - 未修改 proto、SQLite、公开 CLI、环境变量、compose、runtime mount 或 workspace materialization 路径；无例外或未运行的任务内门禁。
    - 下一目标：2.1 实现 Provisioner 状态编排和并发控制。

## 2. 一次性 Workspace Provisioner

参考：[实施计划阶段 2](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-2实现一次性-workspace-provisioner)

- [ ] 2.1 实现 Provisioner 状态编排和并发控制
  - 依赖：1.2。
  - 工作内容：
    - 在 `pkg/workspaces` 增加 Provisioner 及 workspace config/sandbox store/materializer 窄接口。
    - 实现 `Ensure(ctx, sandbox)`，按 sandbox ID 使用 `singleflight.Group` 串行化，并在共享执行前后重新加载 metadata。
    - 无 workspace 直接返回；legacy nil 直接持久化 ready；ready 无副作用返回；pending/failed 进入初始化；未知状态 fail closed。
    - 同步最终持久化对象回调用方并保留 transient env。
    - 把 `golang.org/x/sync` 提升为 direct dependency。
  - 可并行子任务：
    - [ ] 可并行：fake store/materializer 测试夹具。
    - [ ] 可并行：legacy、ready、unknown/no-workspace 分支测试。
    - [ ] 可并行：同 sandbox singleflight 与不同 sandbox 并发测试。
  - 测试方案：`./scripts/with-go-toolchain.sh go test -race ./pkg/workspaces -run 'Test.*Provisioner' -count=1`。
  - 验收标准：ready 分支在 config store/materializer 被调用即 panic 的测试中通过；同 sandbox 只执行一次共享操作；所有调用方获得最终 metadata。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：2.2。

- [ ] 2.2 实现 staging、提升和失败重试
  - 依赖：2.1。
  - 工作内容：
    - 在 `<sandbox>/state/workspace-provisioning/attempt-<id>` 创建同文件系统 staging。
    - 清理遗留 attempt；克隆 sandbox 并仅把 staging 作为 provider WorkspacePath。
    - materialization 成功后把 staging 提升为正式 workspace，再持久化 ready。
    - materialize/提升失败写 failed；failed 重试先持久化 pending；双重错误使用 `errors.Join`。
    - ready 保存失败时阻止 runtime 后续启动，并保留可重试状态。
  - 可并行子任务：
    - [ ] 可并行：staging path/cleanup/promotion focused tests。
    - [ ] 可并行：failed/pending/ready 持久化失败和 `errors.Join` 测试。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/workspaces ./pkg/storage/sessionstore -run 'Test.*(Staging|Retry|Failure|Promotion)' -count=1`。
  - 验收标准：provider 永远只写 staging；pending/failed 半成品可重试；ready workspace 不参与 staging 清理；错误不被清理错误覆盖。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：2.3。

- [ ] 2.3 收紧 file/Git materializer 边界并完成 Provisioner 单元收口
  - 依赖：2.2。
  - 工作内容：
    - 将原 session preparation 降为 Provisioner 内部 materialization，禁止生命周期层直接调用。
    - 移除 `HostWorkspaceInitialized` 对正式 workspace 生命周期的判断；Git 在空 staging 中完成 clone/checkout。
    - 为 file、Git、legacy、source 删除、用户清空、symlink 和远端更新补齐 unit tests。
    - 运行 `go mod tidy` 并审计依赖只产生预期变化。
  - 可并行子任务：
    - [ ] 可并行：file workspace 状态保持用例。
    - [ ] 可并行：本地 Git fixture 和不 re-clone 用例。
    - [ ] 可并行：依赖 diff 与 provider 调用点审计。
  - 测试方案：
    - `./scripts/with-go-toolchain.sh go test ./pkg/workspaces ./pkg/model ./pkg/storage/sessionstore -count=1`
    - `./scripts/with-go-toolchain.sh go test -race ./pkg/workspaces -count=1`
  - 验收标准：file/Git 首次初始化一次；ready 后 source 变化、删除或不可达均不影响 Ensure；race test 无数据竞争。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：3.1。

## 3. Sandbox 生命周期接入

参考：[实施计划阶段 3](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-3接入所有-sandbox-生命周期入口)

- [ ] 3.1 注册 Provisioner 单例并建立调用层接口
  - 依赖：2.3。
  - 工作内容：
    - 在 `pkg/agentcompose/app` 注册单例 Provisioner，顺序早于 session bridge、loader runner 和 run controller。
    - 定义仅暴露 Ensure 的 `WorkspaceEnsurer` 调用层接口，并加入相关 constructor/dependencies。
    - 更新 DI 和构造器测试；生产 graph 不允许缺失 Provisioner。
  - 可并行子任务：
    - [ ] 可并行：app DI provider/registration。
    - [ ] 可并行：fake Ensurer 和 constructor fixture 更新。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/agentcompose/app ./pkg/agentcompose/adapters ./pkg/runs -run 'Test.*(App|Constructor|Dependencies|Provisioner)' -count=1`。
  - 验收标准：生产 service graph 解析成功且所有 owner 获得同一 Provisioner；无公开 API 变化。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：3.2 与 3.3 可并行。

- [ ] 3.2 接入 v1 Session 和 Jupyter 自动恢复
  - 依赖：3.1。
  - 可并行关系：可与 3.3 并行，避免同时修改公共 constructor/DI 文件。
  - 工作内容：
    - v1 create 在 runtime start 前调用 Ensure。
    - `Lifecycle.ResumeLoaded` 和 `EnsureProxyReady` 统一调用 Ensurer，删除各自直接 prepare 分支。
    - 保持 capability/runtime/home 准备、event、token、dashboard 和错误映射顺序。
    - Ensurer 失败时标记现有 failed 状态且 driver start 为零；runtime start 失败不回退 workspace ready。
  - 可并行子任务：
    - [ ] 可并行：`pkg/sessions` lifecycle 接入和 focused tests。
    - [ ] 可并行：v1 bridge create/stop/resume fixture 更新。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/sessions ./pkg/agentcompose/adapters -run 'Test.*(Lifecycle|Session|Proxy).*' -count=1`。
  - 验收标准：Session create/resume 和 Jupyter 自动恢复只有 Ensurer 入口；ready resume 对 workspace 无文件副作用。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：3.4（等待 3.3）。

- [ ] 3.3 接入 Loader 和 Project Run 复用路径
  - 依赖：3.1。
  - 可并行关系：可与 3.2 并行，避免同时修改公共 constructor/DI 文件。
  - 工作内容：
    - Loader 新 sandbox create 与 `LoadOrResume` 使用同一 Ensurer，保留 running sticky 快速返回。
    - `startProjectRunSandbox` 使用 Ensurer；新 sandbox 初始化，显式 sandbox ID 和 sticky binding 复用 ready workspace。
    - 确保本次 run 新解析的 workspace snapshot 不覆盖已有 sandbox snapshot。
    - 保持 cleanup policy、event、dashboard、run status 和 driver error 行为。
  - 可并行子任务：
    - [ ] 可并行：Loader 接入和 sticky tests。
    - [ ] 可并行：Run controller 接入、existing/new sandbox tests。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/agentcompose/adapters ./pkg/runs -run 'Test.*(Loader|Run|Sandbox).*' -count=1`。
  - 验收标准：Loader/Run 的创建和复用都经过 Ensurer；复用已有 sandbox 不重新 materialize；cleanup 非回归。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：3.4（等待 3.2）。

- [ ] 3.4 审计双轨调用并完成生命周期阶段门禁
  - 依赖：3.2、3.3。
  - 工作内容：
    - 用 `rg` 审计 `PrepareSessionWorkspace`、`HostWorkspaceInitialized`、provider materializer 和全部 resume/start 调用点。
    - 删除 adapters、sessions、runs 的直接 materialization；测试中的 focused provider 调用需有明确用途。
    - 跑完整受影响 Go packages，修复构造器/fake/coverage shape 回归。
  - 可并行子任务：
    - [ ] 可并行：生产调用点静态审计。
    - [ ] 可并行：测试 fixture 和 coverage-shape 编译审计。
  - 测试方案：
    - `rg -n 'PrepareSessionWorkspace\(|HostWorkspaceInitialized\(' pkg`
    - `./scripts/with-go-toolchain.sh go test ./pkg/sessions ./pkg/agentcompose/adapters ./pkg/runs ./pkg/agentcompose/app ./pkg/workspaces -count=1`
    - `./scripts/with-go-toolchain.sh go test ./pkg/... -count=1`
  - 验收标准：生产路径只有 Provisioner materialize；所有受影响 package 通过；不存在 ready 内容启发式或双轨 prepare。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：4.1。

## 4. 跨组件 Integration 回归

参考：[实施计划阶段 4](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-4补齐跨组件-integration-回归)

- [ ] 4.1 建立 workspace manifest helper 和 Session/Jupyter integration
  - 依赖：3.4。
  - 工作内容：
    - 实现不跟随 symlink 的 manifest helper，记录 path、type、mode、content hash 和 symlink target。
    - 增加 v1 file workspace create→修改/删除/新增→stop→模板变化→resume 测试。
    - 增加 `EnsureProxyReady` 自动恢复测试，并在 driver start 前断言 manifest 未变。
  - 可并行子任务：
    - [ ] 可并行：manifest helper 和 focused unit tests。
    - [ ] 可并行：Session integration。
    - [ ] 可并行：Jupyter lifecycle integration。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/sessions ./pkg/agentcompose/adapters -run 'TestIntegration.*(Workspace|Session|Proxy)' -count=1`。
  - 验收标准：manifest 能发现当前 bug 的覆盖/复活行为；修复后普通 resume 和自动恢复均完全保持。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：4.2 与 4.3 可并行。

- [ ] 4.2 增加 Loader sticky 和 Project local integration
  - 依赖：4.1。
  - 可并行关系：可与 4.3 并行。
  - 工作内容：
    - 连续两次 loader sticky run 复用同一 sandbox，保留第一次生成文件。
    - run A 使用 local source v1 并修改 workspace；source 更新 v2 后复用 A 保持；新建 run B 获取 v2 且无 A 的文件。
    - 同时断言 sandbox ID、workspace snapshot 和 cleanup policy 行为。
  - 可并行子任务：
    - [ ] 可并行：Loader sticky integration。
    - [ ] 可并行：Project local existing/new sandbox integration。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/agentcompose/adapters ./pkg/runs -run 'TestIntegration.*(Loader|Project|Workspace)' -count=1`。
  - 验收标准：已有 sandbox 保持、新 sandbox 获取最新 source 在测试中同时成立；Loader/Run 不互相污染。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：4.4（等待 4.3）。

- [ ] 4.3 增加 Store 重建、legacy 和首次失败重试 integration
  - 依赖：4.1。
  - 可并行关系：可与 4.2 并行。
  - 工作内容：
    - 使用相同 DataRoot/SandboxRoot 重建 Store/Provisioner 后 resume ready sandbox。
    - 手工写旧 metadata，删除关联 config 后首次 resume 只补 ready、manifest 不变。
    - 构造 file/Git source 错误，断言 driver start 为零；修复后重试成功且只启动一次。
    - 覆盖 runtime start 失败后 workspace 仍 ready、后续 resume 不 materialize。
  - 可并行子任务：
    - [ ] 可并行：Store/Provisioner 重建测试。
    - [ ] 可并行：legacy migration 测试。
    - [ ] 可并行：provision/runtime failure ordering 测试。
  - 测试方案：`./scripts/with-go-toolchain.sh go test ./pkg/workspaces ./pkg/sessions ./pkg/agentcompose/adapters -run 'TestIntegration.*(Persistence|Legacy|Retry|Failure)' -count=1`。
  - 验收标准：持久化而非进程内缓存决定 resume；legacy 不访问 config；首次失败和 runtime 失败具有不同重试语义。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：4.4（等待 4.2）。

- [ ] 4.4 运行默认 Unit/Integration 阶段门禁
  - 依赖：4.2、4.3。
  - 工作内容：
    - 审计测试命名符合 shape selector，不新增复用同一 helper 的伪 E2E wrapper。
    - 运行 unit/integration task 和受影响 package 全量测试。
    - 修复 flaky、共享目录、网络或执行顺序依赖。
  - 可并行子任务：
    - [ ] 可并行：测试命名/shape 审计。
    - [ ] 可并行：受影响 package 覆盖率缺口审计。
  - 测试方案：
    - `task test:unit`
    - `task test:integration`
    - `./scripts/with-go-toolchain.sh go test ./pkg/workspaces ./pkg/sessions ./pkg/agentcompose/adapters ./pkg/runs -count=1`
  - 验收标准：默认门禁内的 unit/integration 全部通过；所有核心行为已有确定性、非 Docker 回归。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：5.1。

## 5. 真实 Docker E2E 和文档

参考：[实施计划阶段 5](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-5增加真实-docker-e2e-和运维文档)

- [ ] 5.1 提取 host-daemon Docker E2E 公共 helper
  - 依赖：4.4。
  - 工作内容：
    - 从现有 Docker Jupyter E2E 提取 binary、daemon、端口、client、环境、日志和 cleanup helper。
    - 保持 package-local、测试专用，不引入产品后门。
    - 现有 Jupyter E2E 编译和行为不变。
  - 可并行子任务：无；该任务集中修改共享 E2E helper，避免并行冲突。
  - 测试方案：
    - `./scripts/with-go-toolchain.sh go test ./test/e2e -run '^TestE2EDockerJupyterHostDaemonStopResume$' -count=1`
    - 有前置镜像时运行 `task test:e2e:docker-jupyter`。
  - 验收标准：公共 helper 无业务实现复制；Jupyter E2E 可编译，具备环境时真实通过；清理和日志诊断不回归。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：5.2 与 5.3 可并行。

- [ ] 5.2 实现 Docker workspace restart/resume E2E
  - 依赖：5.1。
  - 可并行关系：可与 5.3 并行，避免修改 Taskfile/文档。
  - 工作内容：
    - 通过 ConfigService 和 upload API 创建 file source，通过 SessionService 创建 Docker sandbox。
    - 通过 ExecService 修改、删除、新增文件；stop 后更新 source 模板。
    - 停止并以相同 data roots 重启 daemon，resume 原 sandbox 并断言状态与 runtime handle 保持。
    - 创建第二个 sandbox，断言获取新 source 且无旧 sandbox 生成文件。
    - 通过 workspace list/download 验证无反向同步，并注册公开 remove 与 Docker fallback cleanup。
  - 可并行子任务：
    - [ ] 可并行：workspace upload/download client helper。
    - [ ] 可并行：sandbox manifest/exec assertion helper。
    - [ ] 可并行：daemon restart 与 leak cleanup 断言。
  - 测试方案：`AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE=<local-image> ./scripts/with-go-toolchain.sh go test ./test/e2e -run '^TestE2EDockerFileWorkspaceResumePreservesState$' -count=1 -v`。
  - 验收标准：真实 daemon restart 后原 sandbox 保持修改/删除/新增，新 sandbox 获取最新模板；无残留容器、进程、socket、端口或临时目录。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：5.4（等待 5.3）。

- [ ] 5.3 更新 Task、Testing 和设计文档
  - 依赖：5.1。
  - 可并行关系：可与 5.2 并行，E2E test name 和 env contract 以 plan 为准。
  - 工作内容：
    - 新增 `task test:e2e:docker-workspace-resume`，依赖 daemon build，默认本地 `agent-compose-guest:latest`，支持 `AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE`，不隐式 pull。
    - 更新 `TESTING.md` 的前置条件、命令、环境变量和行为说明。
    - 更新中英文设计文档的一次性 seed/ready resume/new sandbox/remove 合同。
    - 更新核心 E2E 规格对应 workspace 场景，不把 Docker 结果描述为三 driver 等价证明。
    - 明确 `.github/workflows/ci.yml` 保持不变。
  - 可并行子任务：
    - [ ] 可并行：Taskfile 和 TESTING.md。
    - [ ] 可并行：中英文设计文档同步。
    - [ ] 可并行：核心 E2E 规格审计与更新。
  - 测试方案：`task --list-all`；检查 task 展示和 shell 语法；运行文档/Task 相关 focused 命令。
  - 验收标准：文档与真实命令一致；无 daemon 公共配置、compose、CI 或 image 默认变化；Docker focused task 可独立运行。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：5.4（等待 5.2）。

- [ ] 5.4 运行 E2E 阶段门禁并审计资源清理
  - 依赖：5.2、5.3。
  - 工作内容：
    - 运行默认 E2E shape，确认真实 Docker test 在无显式环境时按约定 skip、focused task 强制真实执行。
    - 运行 workspace focused Docker E2E；具备 Jupyter 前置条件时同时运行现有 Jupyter focused E2E。
    - 失败后审计 daemon log、Docker label、临时目录和清理路径；不得泄露 secret/token。
  - 可并行子任务：
    - [ ] 可并行：默认 `task test:e2e`。
    - [ ] 可并行：Docker workspace focused E2E。
    - [ ] 可并行：Jupyter focused E2E（环境具备时）。
  - 测试方案：
    - `task test:e2e`
    - `task test:e2e:docker-workspace-resume`
    - `task test:e2e:docker-jupyter`（具备前置条件时）
  - 验收标准：workspace focused E2E 必须真实通过，不能以 skip 代替；无资源泄漏；Jupyter 不能运行时完成编译验证并记录原因。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：6.1。

## 6. 全量门禁和交付收口

参考：[实施计划阶段 6](docs/plan/workspace-resume-preservation-implementation-plan.md#阶段-6全量门禁和交付收口)

- [ ] 6.1 审计依赖、调用点和范围边界
  - 依赖：5.4。
  - 工作内容：
    - 运行 `go mod tidy` 并审计 go.mod/go.sum，只接受 singleflight 所需的直接依赖排序变化。
    - 审计全部 workspace preparation、resume、constructor 和 DI 调用点。
    - 审计 diff 不含 proto/generated client、SQLite、compose、公开 CLI、环境变量、runtime mount 或三 driver E2E 扩张。
    - 对所有已审阅但保留的命中写入完成总结“审计与例外”。
  - 可并行子任务：
    - [ ] 可并行：依赖 diff 审计。
    - [ ] 可并行：生产调用点审计。
    - [ ] 可并行：公开接口/部署范围审计。
  - 测试方案：`go mod tidy` 后运行 `git diff --check`、相关 `rg` 查询和 `./scripts/with-go-toolchain.sh go test ./pkg/workspaces ./pkg/agentcompose/app -count=1`。
  - 验收标准：无双轨 materialization、无范围外文件、无无法解释的依赖变化；所有例外有证据和理由。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：6.2。

- [ ] 6.2 执行完整 harness 质量门禁
  - 依赖：6.1。
  - 工作内容：
    - 依次运行 race test、真实 Docker workspace E2E、lint、coverage test 和 build。
    - 记录 unit、integration、E2E、combined 四项 coverage；失败回到所属任务修复。
    - 不修改 coverage exclusion、测试选择器或门禁阈值规避失败。
  - 可并行子任务：无；最终门禁按顺序执行，避免 cache/资源互相干扰。
  - 测试方案：
    - `./scripts/with-go-toolchain.sh go test -race ./pkg/workspaces -count=1`
    - `task test:e2e:docker-workspace-resume`
    - `task lint`
    - `task test`
    - `task build`
  - 验收标准：所有命令通过；coverage 为 unit ≥ 60%、integration ≥ 60%、E2E ≥ 60%、combined ≥ 70%；Docker E2E 非 skip。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成，覆盖率使用表格记录。
    - 审计与例外：待完成。
    - 下一目标：6.3。

- [ ] 6.3 完成交付审阅和账本总结
  - 依赖：6.2。
  - 工作内容：
    - 将 spec 验收标准逐项映射到实际 unit/integration/E2E 证据。
    - 检查设计文档使用当前态语气，spec/plan 保持设计与计划语气。
    - 更新本文件“当前状态”、所有父任务完成总结和最终残余风险。
    - 确认工作区仅包含本变更需要的代码、测试、Taskfile 和文档。
  - 可并行子任务：
    - [ ] 可并行：验收标准到测试证据映射。
    - [ ] 可并行：文档一致性审阅。
    - [ ] 可并行：最终 diff 和残余风险审阅。
  - 测试方案：复核 6.2 的完整门禁结果；运行 `git diff --check` 和最终静态审计。
  - 验收标准：20/20 父任务完成；每项有五段式证据；无未说明的兼容、部署、测试或数据风险；下一目标为“无”。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：无。

## 全局停止条件和范围外事项

- 若 ready workspace 在任何路径被改写，立即停止，回到 2.x/3.x 修复后重新运行所有状态保持测试。
- 若 legacy migration 需要访问 source 或按内容判断，立即停止；已确认策略是不触碰原 workspace。
- 若 Provisioner 失败后 driver 仍启动，立即停止；错误顺序是 release blocker。
- 若 Docker workspace E2E 无法真实运行，最终任务保持未完成并记录环境阻塞，不得标记全局完成。
- 若实现需要 reset/refresh API、proto、SQLite、CI runtime、compose、runtime mount 或 BoxLite/Microsandbox E2E，先更新 spec/plan 并获得确认。
- 首版不做反向同步、Git 自动操作、公开 provisioning 状态、分布式锁或无关重构。
