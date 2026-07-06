# Runtime cache lifecycle Progress

本文档把 runtime cache lifecycle 设计和实施计划拆成可独立执行、独立验收的任务清单。任务按依赖顺序排列；标记为“可并行”的子任务可以在同一父任务内并行推进，但 subagent 并发度最高不超过 5。

## 文档索引

- 技术方案：[docs/spec/runtime-cache-lifecycle-spec.md](docs/spec/runtime-cache-lifecycle-spec.md)
- 实施计划：[docs/plan/runtime-cache-lifecycle-implementation-plan.md](docs/plan/runtime-cache-lifecycle-implementation-plan.md)
- Harness：[AGENTS.md](AGENTS.md)
- 测试标准：[TESTING.md](TESTING.md)
- Task runner：[Taskfile.yml](Taskfile.yml)
- 当前架构设计：[docs/design/agent-compose_design.md](docs/design/agent-compose_design.md)
- CLI 当前设计：[docs/zh-CN/design/agent-compose-cli-improvement-plan.md](docs/zh-CN/design/agent-compose-cli-improvement-plan.md)
- Runtime mount 设计：[docs/design/runtime_mount_manifest_driver_specific_design.md](docs/design/runtime_mount_manifest_driver_specific_design.md)

## 执行规则

- [ ] 每个任务完成时必须同时完成对应测试方案和验收标准。
- [ ] 不跨阶段提前合并依赖未满足的功能；可以先做可并行调研或测试设计，但不能绕过父任务依赖。
- [ ] 涉及 proto、生成代码、CLI 参数、质量门禁、coverage 范围、package 脚本或文档时，必须同步更新相关生成物、测试和文档。
- [ ] 每个新增功能、命令和参数都必须有明确测试覆盖；缺少测试覆盖时任务不得标记完成。
- [ ] 每个任务合并前至少运行该任务要求的最小测试；阶段收口和最终收口必须运行 harness 定义的 `task lint`、`task build`、`task test`。
- [ ] 真实 BoxLite/Microsandbox smoke 属于 opt-in 验证；无法运行时必须在完成总结中写明原因和补跑命令。
- [ ] 每个任务完成后必须把完成总结写成 `状态`、`变更`、`验证`、`审计与例外`、`下一目标` 五组；无内容写“无”。

## 阶段 1：协议模型和生成物

参考文档：[实施计划 阶段 1](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-1协议模型和生成物)

- [x] 1.1 新增 v2 CacheService proto contract
  - 依赖：无。
  - 工作内容：在 `proto/agentcompose/v2/agentcompose.proto` 新增 `CacheService`、`CacheDomain`、`CacheStatus`、`CacheReference`、`CacheItem`、filter/request/response messages；保持 `ImageService` 和 `RemoveImageRequest.prune_children` 兼容不变。
  - 可并行子任务：
    - [x] 可并行：审计现有 v2 service/message 命名、字段编号和 enum 风格。
    - [x] 可并行：设计 request/response 测试样例，覆盖 list/inspect/prune/remove。
  - 测试方案：运行 `go test ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect`；后续任务补充 API handler 行为测试。
  - 验收标准：proto 中存在四个 RPC；request 覆盖 `driver`、`domain/type`、`status`、`older_than_seconds`、`include_referenced`、`force`、`cache_id`；response 覆盖 matched/removed/skipped/warnings。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 在 `proto/agentcompose/v2/agentcompose.proto` 新增 `CacheService`，包含 `ListCaches`、`InspectCache`、`PruneCaches`、`RemoveCache` 四个 RPC。
      - 新增 `CacheDomain`、`CacheStatus`、`CacheFilter`、`CacheReference`、`CacheItem` 和 list/inspect/prune/remove request/response messages。
      - `CacheFilter` 覆盖 `driver`、`domain`、`type`、`status`、`older_than_seconds`、`cache_id`；prune request 覆盖 `include_referenced` 和 `force`；remove request 覆盖 `cache_id` 和 `force`。
      - prune/remove response 覆盖 `dry_run`、`matched`、`removed`、`skipped`、`warnings`。
    - 验证：
      - `protoc -I proto --descriptor_set_out=/tmp/agentcompose-v2-cache.pb proto/agentcompose/v2/agentcompose.proto`
      - `./scripts/with-go-toolchain.sh go test ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect`
    - 审计与例外：
      - `ImageService` 和 `RemoveImageRequest.prune_children` 未修改。
      - Go proto、Connect Go 和 TypeScript client 生成物按任务 1.2 更新，本任务只落 proto contract。
    - 下一目标：1.2 生成 Go 和 TypeScript client 产物。

- [x] 1.2 同步生成 Go/Connect Go/TypeScript client
  - 依赖：1.1。
  - 工作内容：更新 `proto/agentcompose/v2/agentcompose.pb.go`、`proto/agentcompose/v2/agentcomposev2connect/agentcompose.connect.go`、`proto-client/src/**`；记录实际使用的 Go proto 生成命令和 `proto-client` npm 命令。
  - 可并行子任务：
    - [x] 可并行：准备本地 protoc/protoc-gen-go/protoc-gen-connect-go/proto-client npm 依赖。
    - [x] 可并行：检查 CI `proto-client` workflow 期望的生成和构建命令。
  - 测试方案：运行 `go test ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect`、`cd proto-client && npm ci && npm run gen && npm run build`、`task build`。
  - 验收标准：`agentcomposev2connect` 暴露 `CacheServiceClient` 和 `CacheServiceHandler`；`proto-client` 构建通过；`task build` 能编译新增 proto 包。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 重新生成 `proto/agentcompose/v2/agentcompose.pb.go`，包含 `CacheDomain`、`CacheStatus`、cache request/response messages 和 `CacheService` descriptor。
      - 重新生成 `proto/agentcompose/v2/agentcomposev2connect/agentcompose.connect.go`，暴露 `CacheServiceClient`、`CacheServiceHandler`、`NewCacheServiceClient`、`NewCacheServiceHandler`、四个 procedure 常量和 unimplemented handler。
      - 按 `proto-client` 现有脚本生成并构建本地 TypeScript client，确认 `proto-client/src/agentcompose/v2/` 中存在 `CacheService` TS definitions。
    - 验证：
      - `protoc -I . --go_out=. --go_opt=paths=source_relative --connect-go_out=. --connect-go_opt=paths=source_relative proto/agentcompose/v2/agentcompose.proto`
      - `cd proto-client && npm ci && npm run gen && npm run build`
      - `./scripts/with-go-toolchain.sh go test ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect`
      - `task build`
    - 审计与例外：
      - CI `proto-client` job 使用 `npm ci`、`npm run gen`、`npm run build`，本地已按同等命令验证。
      - `proto-client/src/` 和 `proto-client/dist/` 按 `proto-client/.gitignore` 与 README 策略不提交；本任务提交 tracked Go/Connect Go 生成物，并记录 TypeScript client 已生成和构建通过。
    - 下一目标：2.1 建立 runtimecache 核心模型。

## 阶段 2：runtime cache 领域包

参考文档：[实施计划 阶段 2](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-2runtime-cache-领域包)

- [x] 2.1 建立 `pkg/runtimecache` 核心模型和 filter
  - 依赖：1.2。
  - 工作内容：新增 `pkg/runtimecache`，定义 domain/status/item/reference/filter/list/prune/remove/result；实现 driver、domain/type、status、older-than、cache-id filter。
  - 可并行子任务：
    - [x] 可并行：梳理 `pkg/images`、`pkg/sessions` 现有 owner package 风格。
    - [x] 可并行：为 filter 和 enum 设计 table-driven test cases。
  - 测试方案：`go test ./pkg/runtimecache`，覆盖每个 filter 参数、组合 filter、空 filter、无匹配项、非法枚举或非法 duration。
  - 验收标准：核心包不导入 `connectrpc.com/connect`；filter 结果稳定；未知值不会导致误删。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `pkg/runtimecache` owner package，定义 driver、domain、type、status 常量，以及 `Item`、`Reference`、`Filter`、`ListRequest`、`ListResult`、`PruneRequest`、`RemoveRequest`、`Result`。
      - 实现 `NormalizeDriver`、`NormalizeDomain`、`NormalizeType`、`NormalizeStatus`、domain/type 映射和 `FilterItems`。
      - `FilterItems` 覆盖 driver、domain、type、status、older-than、cache-id 和组合 filter，保持输入顺序稳定。
      - 增加 table-driven tests，覆盖空 filter、无匹配项、非法 driver/domain/type/status/duration，以及未知 item 值不匹配 typed filters。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - `pkg/runtimecache` 不导入 `connectrpc.com/connect`。
      - 本任务只实现模型和 filter；稳定 `cache_id` 生成/解析、path safety、size/warning 机制按任务 2.2 实现。
      - dry-run/prune/remove 保护语义按任务 2.3 实现，本任务仅预留 request/result 类型。
    - 下一目标：2.2 实现 cache_id 和安全删除基础。

- [x] 2.2 实现 `cache_id`、path safety 和 size/warning 机制
  - 依赖：2.1。
  - 工作内容：实现稳定 `cache_id` 生成/解析、canonical root 检查、symlink escape 防护、root deletion 防护、size walk、warning 聚合。
  - 可并行子任务：
    - [x] 可并行：实现并审计 path traversal、symlink、broken symlink 测试夹具。
    - [x] 可并行：实现 size walk 和 permission/stat failure warning 测试夹具。
  - 测试方案：`go test ./pkg/runtimecache`，覆盖 ID 稳定性、非法 ID、path 注入、symlink escape、root deletion、stat/read/walk 失败 warning。
  - 验收标准：删除逻辑只能作用于 inventory 生成出的 item path；无法证明安全时返回 `unknown` 或不可删。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增稳定 `cache_id` 生成和解析 helper，ID 由 domain、driver、kind 和 path/digest/name/session identity hash 组成，不把原始路径暴露为可执行输入。
      - 新增 `ValidateCachePath`，执行 absolute path、canonical root/target、root deletion、path traversal、symlink escape、broken symlink 和 canonical parent 检查。
      - 新增 `EstimateSize` 和 `AppendWarnings`，递归估算文件大小，并把 missing/stat/walk failure 聚合成 warnings。
      - 增加 tests 覆盖 ID 稳定性、不同 identity 不冲突、非法 ID/输入、path traversal、symlink escape、broken symlink、root deletion、size walk 成功和 warning 聚合。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - `ValidateCachePath` 只提供删除前安全校验；实际删除和重新构建 inventory 后再校验的流程按任务 2.3/后续 adapter 接入实现。
      - 读取失败对保护状态的 `unknown/removable=false` 聚合将在 2.3 的 prune/remove 结果计算中接入；本任务已提供 warning helper。
    - 下一目标：2.3 实现保护状态和 dry-run/prune。

- [x] 2.3 实现保护规则、dry-run 和 prune/remove 核心
  - 依赖：2.2。
  - 工作内容：实现 active、referenced、unused、expired、orphaned、unknown 的 removable 计算；实现 `force=false` dry-run、`force=true` 删除、`include_referenced` 语义。
  - 可并行子任务：
    - [x] 可并行：构建 fake reference source，模拟 running/resuming/stopped session、project image refs、image metadata refs、driver active state。
    - [x] 可并行：构建 prune/remove table tests，覆盖 matched/removed/skipped/warnings。
  - 测试方案：`go test ./pkg/runtimecache`，覆盖所有 status、force/dry-run、include-referenced、active/unknown force 下仍不删除。
  - 验收标准：保护规则保守优先；unknown 永不可删；dry-run 不改文件系统。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `EvaluateProtection`，按 active、referenced、unused、expired、orphaned、unknown 计算 `Removable` 和 `BlockedReasons`。
      - 新增 `PruneItems`，基于 inventory items、filter、`force`、`include_referenced` 和 caller-provided remover 生成 `dry_run`、`matched`、`removed`、`skipped`、`warnings`。
      - 新增 `RemoveItem`，要求合法 `cache_id`，只在 inventory 精确命中后复用 prune 核心执行 dry-run/force。
      - 增加 tests 覆盖所有保护状态、referenced include 语义、dry-run 不调用 remover、force 删除、active/unknown force 下仍跳过、remove error warning 和继续处理。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - tests 通过 `Item.References` 和 status fixtures 模拟 running/resuming/stopped session、project image refs、image metadata refs、driver active/unknown state；真实事实源接入按后续 materialized/driver/app 任务实现。
      - `PruneItems` 和 `RemoveItem` 只操作传入 inventory items 并调用注入 remover，不接受任意 filesystem path。
      - 实际 materialized image cache scanner 和 driver adapters 从阶段 3/4 开始接入。
    - 下一目标：3.1 materialized image cache inventory。

## 阶段 3：materialized image cache inventory 和 prune

参考文档：[实施计划 阶段 3](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-3materialized-image-cache-inventory-和-prune)

- [x] 3.1 实现 materialized cache scanner
  - 依赖：2.3。
  - 工作内容：扫描 `pkg/imagecache.Cache.MaterializationRoot()`；读取 `IMAGE_CACHE_ROOT/metadata.json`；关联 `layout_cache_path`、`rootfs_cache_path`、digest、repo tags/digests；识别 layout/rootfs/ready flag/temp dir。
  - 可并行子任务：
    - [x] 可并行：构造 metadata 和磁盘目录测试 fixture。
    - [x] 可并行：审计 `pkg/imagecache` ready flag、lock、materialize helper 的当前行为。
  - 测试方案：`go test ./pkg/runtimecache ./pkg/imagecache`，覆盖 metadata 存在/缺失、layout/rootfs 存在/缺失、orphaned image dir、ready flag 和 temp dir。
  - 验收标准：每个 materialized item 都能解释 image id/ref 或 orphaned 原因；读取失败时 warning 清晰。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `runtimecache.MaterializedScanner`，扫描 `imagecache.Cache.MaterializationRoot()` 下的 materialized image directories。
      - 读取 `IMAGE_CACHE_ROOT/metadata.json`，将 metadata 中的 `layout_cache_path`、`rootfs_cache_path`、config/manifest digest、repo tag/digest 关联到 materialized layout/rootfs/ready flag items。
      - 识别 `materialized-oci-layout`、`materialized-rootfs`、`materialized-ready-flag`、`materialized-temp-dir`，并为 orphaned disk items 生成 stable `cache_id`、mtime last-used、size 和 removable/protection 状态。
      - metadata 读取失败或显式 materialized path 缺失时返回 warnings，不阻塞磁盘 inventory。
      - 增加 tests 覆盖 metadata 存在/缺失/损坏、layout/rootfs 存在/缺失、orphaned image dir、`.ready`、`.rootfs.ready`、`rootfs.tmp` 和 metadata warning。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache ./pkg/imagecache`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - 本任务只实现 inventory scanner；materialized cache 删除、ready flag 同步删除和 `pkg/imagecache.Cache.Remove` 非回归按任务 3.2 实现。
      - scanner 使用 `pkg/imagecache` 当前 ready flag 和 temp dir 命名：`.ready`、`.rootfs.ready`、`oci.tmp`、`rootfs.tmp`。
    - 下一目标：3.2 materialized cache 删除和 image remove 非回归。

- [x] 3.2 实现 materialized cache prune/remove 和非回归测试
  - 依赖：3.1。
  - 工作内容：使用 `imagecache.Lock` 或同级 lock 保护删除；删除 layout/rootfs 时同步移除对应 ready flag；删除 temp dir 不影响完整 cache；保持 `pkg/imagecache.Cache.Remove` 和 `agent-compose rmi` 不删除 materialized/runtime cache。
  - 可并行子任务：
    - [x] 可并行：实现删除一致性测试。
    - [x] 可并行：实现 `Cache.Remove(PruneChildren=true)` 非回归测试。
  - 测试方案：`go test ./pkg/runtimecache ./pkg/imagecache`，覆盖 referenced 默认不可删、include-referenced 可删、orphaned/temp/expired force 删除、ready flag 同步删除。
  - 验收标准：不会误删 OCI image store root 或其他 image 的完整 cache；image domain 与 runtime cache domain 分离。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `runtimecache.MaterializedRemover`，使用 `imagecache.Cache.Lock()` 保护 materialized cache 删除。
      - 删除 `materialized-oci-layout` 时同步删除同 image dir 下 `.ready`；删除 `materialized-rootfs` 时同步删除 `.rootfs.ready`。
      - 删除 `materialized-temp-dir` 或 ready flag 时只删除目标 item，不删除 sibling layout/rootfs。
      - remover 校验 materialized domain、合法 `cache_id`、cache id 与 inventory item 一致，并用 `ValidateCachePath` 限制目标位于 `MaterializationRoot()` 下。
      - 增加 `pkg/imagecache.Cache.Remove(PruneChildren=true)` 非回归测试，证明 image metadata 删除不删除 materialized layout/rootfs。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache ./pkg/imagecache`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - 覆盖 referenced 默认不可删、`include_referenced` 可删、orphaned/temp/expired force 删除、ready flag 同步删除、temp dir 不影响完整 cache。
      - `ImageService`/CLI `rmi` 路径尚未接入 runtime cache；本任务通过 `pkg/imagecache.Cache.Remove` owner regression 保持 image domain 与 materialized/runtime cache domain 分离。
    - 下一目标：4.1 BoxLite cache adapter。

## 阶段 4：BoxLite 和 Microsandbox driver cache adapter

参考文档：[实施计划 阶段 4](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-4boxlite-和-microsandbox-driver-cache-adapter)

- [x] 4.1 实现 BoxLite runtime-derived inventory 和安全删除
  - 依赖：3.2。
  - 工作内容：识别 `BOXLITE_HOME/images/local`、`BOXLITE_HOME/images/disk-images` 和可安全发现的 box/runtime state；将 legacy cleanup 纳入 inventory-aware removal；BoxLite DB/schema 不确定时标记 unknown。
  - 可并行子任务：
    - [x] 可并行：审计 `pkg/driver/boxlite_cache_gc.go` 和现有 boxlite cgo tests。
    - [x] 可并行：构造 BoxLite home/DB fixture，覆盖 active/stopped/orphaned/unknown。
  - 测试方案：`go test -tags boxlitecgo ./pkg/driver` 和相关 `pkg/runtimecache` tests，覆盖 active 引用阻止删除、stopped/orphaned dry-run/force、DB 表缺失或查询失败为 unknown。
  - 验收标准：BoxLite cache 删除不再按固定目录无条件清空；无法证明安全时不可删。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 在 `pkg/driver/boxlite_cache_gc.go` 新增 BoxLite runtime-derived inventory，扫描 `BOXLITE_HOME/images/local/*` 和 `BOXLITE_HOME/images/disk-images/*`，生成 `runtime-derived-cache` / `boxlite` / `boxlite-local-image`、`boxlite-disk-image` items。
      - 新增 BoxLite DB active state 检查，只有 `box_state.status` 查询成功且无 active box 时才把 legacy runtime cache 标记为 `orphaned/removable`；active box 标记为 `active` 并附带 DB reference。
      - DB 缺失、表缺失、status 列缺失、corrupt/query failure、空或相对 `BOXLITE_HOME` 均转为 warning 或 unknown；unknown item 即使 `force=true` 也不可删。
      - 将 `cleanupLegacyBoxliteImageCaches` 改为调用 inventory-aware prune，不再使用固定目录 `removeAllChildren` 无条件清空。
      - 新增 BoxLite runtime cache remover，校验 domain/driver/kind/cache_id，持有 `BOXLITE_HOME/.agent-compose-runtime-cache.lock`，并通过 `runtimecache.ValidateCachePath` 限制删除目标位于对应 root 下。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - 初次 `./scripts/with-go-toolchain.sh go test -count=1 -tags boxlitecgo ./pkg/driver` 因本地缺少 `build/boxlite/include/boxlite.h` 失败。
      - `./scripts/export-boxlite-dev-artifact.sh ./build/boxlite`
      - `./scripts/with-go-toolchain.sh go test -count=1 -tags boxlitecgo ./pkg/driver`
      - `git diff --check`
    - 审计与例外：
      - 覆盖 inventory 扫描、active 引用阻止删除、stopped/orphaned dry-run、force 单项删除、DB missing/table missing/status column missing/corrupt DB unknown 不可删、相对 `BOXLITE_HOME` 不删除当前目录、symlink escape 删除失败且不影响外部目标。
      - `resolveRootfsPath()` 中 `maybeRunCacheGC()` 仍按任务 4.2 处理，本任务未改动 materialized cache TTL 热路径。
      - 真实 BoxLite smoke 为 opt-in，本任务未运行；补跑命令：`SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke`。
    - 下一目标：4.2 移除 BoxLite 启动热路径隐式清理。

- [x] 4.2 移除 BoxLite 启动热路径 materialized cache GC
  - 依赖：4.1。
  - 工作内容：从 `resolveRootfsPath()` 或等价 image resolution 热路径迁出 `maybeRunCacheGC()`；保留过期 materialized cache 判断能力给显式 prune 使用。
  - 可并行子任务：
    - [x] 可并行：补充回归测试证明 `EnsureSession` 和 image resolution 不触发 cleanup。
    - [x] 可并行：审计 `BOX_CACHE_TTL` 相关文档和配置引用。
  - 测试方案：`go test -tags boxlitecgo ./pkg/driver`，覆盖 `EnsureSession` 不调用 legacy cleanup、`resolveRootfsPath()` 不触发 `maybeRunCacheGC()`、当前 image cache 不被启动路径删除。
  - 验收标准：BoxLite session start/resume 不删除其他 image 或全局 cache 项。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 从 `pkg/driver/boxlite_cgo.go` 的 `resolveRootfsPath()` 移除所有 `maybeRunCacheGC()` 调用，BoxLite image resolution 不再按 `BOX_CACHE_TTL` 清理 `<DATA_ROOT>/image-cache`。
      - 保留 `pkg/driver/boxlite_cache_gc.go` 中的 `maybeRunCacheGC()`、`cleanupExpiredCacheDirs()` 和 artifact 过期判断 helper，供后续显式 prune/maintenance 接入。
      - 在 `pkg/driver/boxlite_cache_gc_test.go` 增加 AST 回归测试，证明 `EnsureSession` 不调用 `cleanupLegacyBoxliteCaches()`，`resolveRootfsPath()` 不调用 `maybeRunCacheGC()`。
      - 增加 filesystem 回归测试，构造已过期的 materialized `rootfs` 和 `oci.tmp`，验证 `resolveRootfsPath()` 在 `BoxCacheTTL` 已过期时也不会删除当前或其他 materialized cache。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `./scripts/with-go-toolchain.sh go test -count=1 -tags boxlitecgo ./pkg/driver`
      - `git diff --check`
      - `rg -n "maybeRunCacheGC\\(" pkg/driver docs PROGRESS.md`
    - 审计与例外：
      - `maybeRunCacheGC(` 在 `pkg/driver` 中仅剩 helper 定义；启动和 image resolution 路径不再调用。
      - `BOX_CACHE_TTL` 仍存在于配置、`.env.example` 和 `README.md` 的简短配置列表中；按阶段 8.1 的文档任务统一更新为“不再驱动启动热路径 GC，用于显式 prune 或后续维护”。
      - 真实 BoxLite smoke 为 opt-in，本任务未运行；补跑命令：`SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke`。
    - 下一目标：4.3 Microsandbox session-ephemeral adapter。

- [x] 4.3 实现 Microsandbox session-ephemeral inventory 和移除 startup sweep
  - 依赖：3.2。
  - 工作内容：识别 `MICROSANDBOX_HOME/docker-disks/*.raw`、`MICROSANDBOX_HOME/sandboxes/*`；从 `prepareEnvironment()` 移除 `gcDockerDisks()`；保留 `createSandbox` 失败 rollback 和 `StopSession` 当前 session 清理。
  - 可并行子任务：
    - [x] 可并行：构造 Microsandbox home fixture，覆盖 running/stopped/orphaned/unknown。
    - [x] 可并行：补充 session 创建失败和 StopSession cleanup 回归测试。
  - 测试方案：`go test ./pkg/driver` 和相关 `pkg/runtimecache` tests，覆盖 prepareEnvironment 不删除已有 `.raw`、创建失败只删除当前 disk、StopSession 删除当前 disk、orphaned disk 可显式 prune。
  - 验收标准：Microsandbox startup 不再全量删除其他 session `.raw`；显式 prune 才清理 orphaned/stopped state。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 从 `pkg/driver/microsandbox_runtime.go` 的 `prepareEnvironment()` 移除 startup `docker-disks/*.raw` sweep，并删除 `gcDockerDisks()`。
      - 保留 `createSandbox` 失败 rollback 的 `removeDockerDisk(sessionID)`，并保留 `StopSession` 对当前 session disk 和 stopped/stale sandbox state 的 best-effort cleanup。
      - 新增 `pkg/driver/microsandbox_cache.go`，扫描 `MICROSANDBOX_HOME/docker-disks/*.raw` 和 `MICROSANDBOX_HOME/sandboxes/*`，生成 `session-ephemeral-state` / `microsandbox` items。
      - 支持 active、referenced、orphaned、unknown reference state；active/unknown 不可删，referenced 需 `include_referenced`，orphaned 可 dry-run/force prune。
      - 新增 Microsandbox session cache remover，校验 domain/driver/kind/cache_id，持有 `MICROSANDBOX_HOME/.agent-compose-session-cache.lock`，并通过 `runtimecache.ValidateCachePath` 限制删除目标位于 `docker-disks` 或 `sandboxes` root 下。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/driver`
      - `./scripts/with-go-toolchain.sh go test -count=1 -tags boxlitecgo ./pkg/driver`
      - `git diff --check`
      - `rg -n "gcDockerDisks|removed stale disk images on startup|prepareEnvironment\\(\\).*docker-disks" pkg/driver docs PROGRESS.md`
    - 审计与例外：
      - 覆盖 `prepareEnvironment()` 保留既有 `.raw`、非 `.raw` 文件和子目录；`removeDockerDisk` 只删除当前 session disk；inventory 扫描 active/referenced/orphaned；dry-run 不删除；force 删除 orphaned；referenced 默认跳过且 `include_referenced` 后删除；unknown 和 symlink escape 均不可删。
      - grep 中 `gcDockerDisks` 只剩 spec/plan/progress 描述，不再存在于 `pkg/driver` 代码。
      - 显式 prune 当前删除 inventory state path；后续 API/app 集成可在 controller 层继续组合 SDK-level `microsandbox.RemoveSandbox`。
      - 真实 Microsandbox smoke 为 opt-in，本任务未运行；补跑命令：`SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke`。
    - 下一目标：5.1 API handler。

## 阶段 5：daemon CacheService 和 route 注册

参考文档：[实施计划 阶段 5](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-5daemon-cacheservice-和-route-注册)

- [x] 5.1 实现 `pkg/agentcompose/api` CacheService handler
  - 依赖：1.2、2.3、3.2、4.1、4.3。
  - 工作内容：新增 `pkg/agentcompose/api/cache.go`；实现 proto/domain 映射、参数校验、Connect code 映射、List/Inspect/Prune/Remove RPC。
  - 可并行子任务：
    - [x] 可并行：实现 enum 和 filter 映射 table tests。
    - [x] 可并行：实现 fake controller，覆盖四个 RPC 的成功和错误路径。
  - 测试方案：`go test ./pkg/agentcompose/api ./pkg/runtimecache`，覆盖四个 RPC、invalid argument、not found、active/unknown protected、referenced without include、dry-run、force delete。
  - 验收标准：handler 返回 warnings/blocked reasons；错误码稳定；unknown enum 不导致误删。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `pkg/agentcompose/api/cache.go`，实现 `CacheHandler` 和 `CacheController` 接口，提供 `ListCaches`、`InspectCache`、`PruneCaches`、`RemoveCache` 四个 RPC。
      - 实现 proto/domain mapping：`CacheDomain`、`CacheStatus`、driver/type/status/cache_id/older_than_seconds filter、`CacheItem`、`CacheReference`、prune/remove result。
      - 实现参数校验：缺失或非法 `cache_id`、未知 enum、未知 driver/type、过大的 `older_than_seconds` 均返回 `InvalidArgument`。
      - 实现 runtimecache error 到 Connect code 映射：not found、invalid argument、failed precondition、unavailable、internal。
      - 新增 `pkg/agentcompose/api/cache_test.go`，使用 fake controller 覆盖四个 RPC 的成功和错误路径。
    - 验证：
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/agentcompose/api`
      - `git diff --check`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
    - 审计与例外：
      - 覆盖 list filter 映射、inspect not found、prune dry-run/force response、remove active protected skipped response、unknown enum 不进入 controller、runtimecache error code 映射。
      - 本任务只实现 API handler 和 fake-controller tests；实际 controller 事实源注入、app route 注册和 generated Connect client integration 按 5.2 实现。
      - `pkg/runtimecache` 仍不导入 Connect。
    - 下一目标：5.2 app 注册和集成。

- [x] 5.2 注册 daemon CacheService 并接入事实源
  - 依赖：5.1。
  - 工作内容：在 `pkg/agentcompose/app/app.go` 构造 runtimecache controller，注入 config、sessionstore、configstore、imagecache、driver adapters；注册 `agentcomposev2connect.NewCacheServiceHandler`。
  - 可并行子任务：
    - [x] 可并行：更新 app route test 期望路径。
    - [x] 可并行：准备临时 DATA_ROOT/SESSION_ROOT/fake runtime homes 集成 fixture。
  - 测试方案：`go test ./pkg/agentcompose/app ./pkg/agentcompose/api ./pkg/runtimecache`，覆盖 `/agentcompose.v2.CacheService/*` route 注册和 generated Connect client 调用四个 RPC。
  - 验收标准：daemon 是唯一删除执行者；CLI 无需本地路径权限；`ImageService.RemoveImage` 相关测试仍通过。
  - 完成总结：
    - 状态：已完成。
    - 变更：
      - 新增 `pkg/runtimecache.Controller` 和 `runtimecache.Source` 组合层，统一 list/inspect/prune/remove inventory，删除只通过 inventory cache id 映射回 source remover。
      - 新增 `runtimecache.MaterializedSource`，组合阶段 3 的 materialized scanner/remover，daemon 可直接管理 `IMAGE_CACHE_ROOT` 同级 `image-cache` 下的 materialized items。
      - 新增 `pkg/driver.NewRuntimeCacheSources`，按 build tags 接入 BoxLite runtime-derived source 和 Microsandbox session-ephemeral source，并补齐 non-BoxLite/non-cgo no-op source 文件。
      - 在 `pkg/agentcompose/app/app.go` 注册 `NewCacheController` DI，构造 imagecache、接入 driver adapters，并注册 `agentcomposev2connect.NewCacheServiceHandler` 到 `/agentcompose.v2.CacheService/*`。
      - 更新 `pkg/agentcompose/app/app_test.go` route 断言，并新增 generated Connect client 集成测试，使用临时 `DATA_ROOT`、`SESSION_ROOT`、`IMAGE_CACHE_ROOT` 和 materialized rootfs fixture 覆盖 `ListCaches`、`InspectCache`、`PruneCaches` dry-run、`RemoveCache` dry-run 和 `RemoveCache` force 删除。
    - 验证：
      - `./scripts/with-go-toolchain.sh gofmt -w pkg/agentcompose/app/app.go pkg/agentcompose/app/app_test.go pkg/runtimecache/controller.go pkg/driver/runtime_cache_sources.go pkg/driver/runtime_cache_sources_boxlite.go pkg/driver/runtime_cache_sources_microsandbox.go pkg/driver/runtime_cache_sources_noboxlite.go pkg/driver/runtime_cache_sources_nomicrosandbox.go`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/runtimecache`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/agentcompose/api`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/agentcompose/app`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/driver`
      - `./scripts/with-go-toolchain.sh go test -count=1 -tags boxlitecgo ./pkg/driver`
      - `CGO_ENABLED=0 ./scripts/with-go-toolchain.sh go test -count=1 ./pkg/agentcompose/app`
      - `CGO_ENABLED=0 ./scripts/with-go-toolchain.sh go test -count=1 ./pkg/driver`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/imagecache`
      - `./scripts/with-go-toolchain.sh go test -count=1 ./pkg/images`
      - `rg -n "connectrpc|connect\\." pkg/runtimecache || true`
      - `git diff --check`
    - 审计与例外：
      - `pkg/runtimecache` 仍不导入 Connect；Connect code 映射只在 `pkg/agentcompose/api/cache.go`。
      - 真实删除路径仍来自 inventory-generated items：集成测试中 force remove 只删除临时 materialized rootfs fixture，dry-run 不修改文件系统。
      - BoxLite source 复用阶段 4.1 的 inventory-aware scanner/remover；non-`boxlitecgo` 构建不注册 BoxLite source。
      - Microsandbox app-level source 复用阶段 4.3 scanner/remover，但当前 session reference state 在 app composition 中保守标记为 unknown，避免引用未完全解析时误删；后续可在 controller 层补充 sessionstore/configstore 到 Microsandbox reference maps 或 SDK-level sandbox removal。
      - `ImageService.RemoveImage` 边界未接入 CacheService；`pkg/imagecache` 和 `pkg/images` 回归测试已通过。
      - 真实 BoxLite/Microsandbox smoke 为 opt-in，本任务未运行；补跑命令：`SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke`、`SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke`。
    - 下一目标：6.1 CLI cache ls/inspect。

## 阶段 6：CLI cache 命令组

参考文档：[实施计划 阶段 6](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-6cli-cache-命令组)

- [ ] 6.1 实现 CLI cache client、`cache ls` 和 `cache inspect`
  - 依赖：5.2。
  - 工作内容：`newCLIServiceClients` 增加 CacheService client；新增 `cache` 命令组；实现 `cache ls`、`cache inspect <cache-id>`；实现通用 `--driver`、`--type`、`--status`、`--json`。
  - 可并行子任务：
    - [ ] 可并行：扩展 CLI stub server 和 cache service stub。
    - [ ] 可并行：实现文本输出和 JSON output structs。
  - 测试方案：`go test ./cmd/agent-compose`，覆盖 `cache ls` 文本/JSON/空结果，`--driver` 所有值和非法值，`--type` 所有值和非法值，`--status` 所有值和非法值，`cache inspect` 文本/JSON/NotFound/missing arg/extra arg。
  - 验收标准：每个命令和每个参数均有测试；JSON stdout 可 `json.Unmarshal`；CLI 不读写 daemon cache path。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：6.2 CLI cache prune/rm。

- [ ] 6.2 实现 CLI `cache prune` 和 `cache rm`
  - 依赖：6.1。
  - 工作内容：实现 `cache prune`、`cache rm <cache-id>`；实现 `--unused`、`--orphaned`、`--expired`、`--older-than`、`--include-referenced`、`--force`；定义互斥/组合规则和退出码。
  - 可并行子任务：
    - [ ] 可并行：实现 prune/rm request mapping tests。
    - [ ] 可并行：实现文本输出和 JSON output tests。
  - 测试方案：`go test ./cmd/agent-compose`，覆盖默认 dry-run、`--force`、`--unused`、`--orphaned`、`--expired`、`--older-than 7d`、`--include-referenced`、非法 duration、负数/零 duration、active/unknown protected error、missing/extra args。
  - 验收标准：无可删项返回 0；usage error 和 Connect error 映射符合现有 CLI；dry-run 中 protected skipped 不失败；JSON 不被 warning/deprecated 文案污染。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：7.1 端到端 workflow。

## 阶段 7：端到端集成和非回归

参考文档：[实施计划 阶段 7](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-7端到端集成和非回归)

- [ ] 7.1 覆盖完整 cache lifecycle workflow
  - 依赖：6.2。
  - 工作内容：增加 in-process daemon/Connect/CLI integration tests，使用临时 `DATA_ROOT`、`SESSION_ROOT`、`IMAGE_CACHE_ROOT`、`BOXLITE_HOME`、`MICROSANDBOX_HOME` 构造完整文件树。
  - 可并行子任务：
    - [ ] 可并行：构造 image metadata + materialized layout/rootfs fixture。
    - [ ] 可并行：构造 BoxLite legacy dirs fixture。
    - [ ] 可并行：构造 Microsandbox docker disk/sandbox state fixture。
  - 测试方案：`go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runtimecache ./pkg/driver ./pkg/imagecache`，覆盖 dry-run 不删除、force 仅删除目标、running session active 不可删、stopped referenced 默认不可删但 include-referenced 可删、unknown warning。
  - 验收标准：完整 workflow 能证明 dry-run、force、保护、warnings 和删除边界。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：7.2 image/session 非回归。

- [ ] 7.2 覆盖 `rmi` 和 session lifecycle 非回归
  - 依赖：7.1。
  - 工作内容：验证 `agent-compose rmi` 不删除 materialized/runtime cache；验证 Microsandbox startup 不删除其他 `.raw`；验证 BoxLite image resolution 不触发 TTL prune。
  - 可并行子任务：
    - [ ] 可并行：实现 `rmi` 非回归集成测试。
    - [ ] 可并行：实现 BoxLite/Microsandbox startup 非回归测试。
  - 测试方案：`go test ./cmd/agent-compose ./pkg/driver ./pkg/imagecache ./pkg/runtimecache`，断言 `image-cache/<image-id>/oci`、`rootfs`、`BOXLITE_HOME/images/*`、`MICROSANDBOX_HOME/docker-disks/*.raw` 未被非 cache 命令删除。
  - 验收标准：image domain 与 runtime cache domain 分离；启动路径不做隐藏全局 GC。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：8.1 文档和质量门禁。

## 阶段 8：文档、质量门禁和真实 runtime smoke

参考文档：[实施计划 阶段 8](docs/plan/runtime-cache-lifecycle-implementation-plan.md#阶段-8文档质量门禁和真实-runtime-smoke)

- [ ] 8.1 更新用户和设计文档
  - 依赖：7.2。
  - 工作内容：更新 `docs/command-line-manual.md`、`docs/zh-CN/command-line-manual.md`、`docs/design/agent-compose_design.md`、中文对应设计文档；说明 `CacheService`、`cache` 命令、dry-run 默认、`--force`、保护状态、`BOX_CACHE_TTL` 新语义。
  - 可并行子任务：
    - [ ] 可并行：更新 CLI 使用文档。
    - [ ] 可并行：更新架构/API 设计文档。
    - [ ] 可并行：审计 `.env.example` 和部署文档是否提到 `BOX_CACHE_TTL`。
  - 测试方案：运行 `task docs` 如仍为 placeholder 则记录；运行 focused grep 检查旧语义；最终运行 `task lint`、`task build`、`task test`。
  - 验收标准：文档与实现一致；不新增无必要环境变量；dry-run/force 行为清晰。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：8.2 全量门禁和 smoke。

- [ ] 8.2 运行全量质量门禁和 runtime smoke
  - 依赖：8.1。
  - 工作内容：检查所有生成物已提交；运行局部和全量质量门禁；具备依赖时运行 BoxLite/Microsandbox runtime smoke。
  - 可并行子任务：
    - [ ] 可并行：执行 proto-client 生成和 build 验证。
    - [ ] 可并行：执行 Go focused test 和 full task gates。
    - [ ] 可并行：在具备 runtime 依赖环境执行 smoke。
  - 测试方案：
    - `go test ./cmd/agent-compose ./pkg/agentcompose/api ./pkg/agentcompose/app ./pkg/runtimecache ./pkg/driver ./pkg/imagecache`
    - `cd proto-client && npm ci && npm run gen && npm run build`
    - `task build`
    - `task lint`
    - `task test`
    - `SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke`
    - `SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke`
  - 验收标准：`task lint`、`task build`、`task test` 通过；coverage 满足 `TESTING.md` baseline；无法运行的 smoke 有明确原因和补跑命令。
  - 完成总结：
    - 状态：待完成。
    - 变更：待完成。
    - 验证：待完成。
    - 审计与例外：待完成。
    - 下一目标：无。

## 停止条件

- 新增 proto 后无法稳定生成 Go Connect 或 TS client：停止在阶段 1。
- 删除路径无法证明 canonical path 位于对应 root 下：停止实现删除，只允许 list/inspect dry-run。
- BoxLite DB schema 与假设不一致：对应 item 标记 `unknown`，不得删除。
- Microsandbox SDK/daemon 无法可靠区分 running/draining/stopped：对应 sandbox state 标记 `unknown` 或 `active`，不得删除。
- 任一 CLI 命令或参数缺少测试覆盖：对应任务不得完成。
- `task test` coverage 低于 baseline：必须补测试，不得只调整 coverage 排除规则。

## 首版不做事项

- 不实现自动后台周期性 GC。
- 不实现跨节点或多 daemon cache 协调。
- 不提供 UI 页面。
- 不让 `rmi` 默认删除 runtime/materialized cache。
- 不删除 Docker daemon image/container/volume cache。
- 不支持按任意 filesystem path 删除 cache。
- 不优化 `run --command` 跳过 Jupyter readiness。
