# Runtime cache lifecycle spec

## 背景与目标

`agent-compose run <agent> --command ...` 在 BoxLite driver 下出现明显冷启动放大：命令本身只需要数百毫秒，但每次 run 默认创建新的 session/sandbox，并在完成后停止 runtime；此前 BoxLite 启动路径还会清理自身可复用的 runtime image cache，导致下一次启动重新构建 image disk/rootfs 派生物。

本文定义 runtime cache 生命周期的首版方案。BoxLite 启动热路径中的直接缓存清理已经完成第一步修复：`pkg/driver/boxlite_cgo.go` 的 `EnsureSession` 成功路径不再调用 `cleanupLegacyBoxliteCaches()`。后续目标是把这类 runtime derived cache 的清理能力收敛到显式、可观测、带保护条件的 cache lifecycle 命令。

目标是：

- 保持 BoxLite session 启动只创建或复用 runtime 资源，不执行破坏性全局 cache GC。
- 为 BoxLite 和 Microsandbox 提供统一、显式、可观测、带保护条件的手动 cache 生命周期管理能力。
- 保持 image domain 和 runtime cache domain 分离：`pull`/`rmi` 仍面向 OCI image reference 和 image backend/store；BoxLite/Microsandbox 的派生 runtime cache 不回流为 runtime driver 的 pull/remove 语义。
- 让 operator 能安全释放磁盘空间，并在 dry-run 中看到会删除什么、为什么可删、为什么不可删。

本文覆盖：

- BoxLite runtime derived cache 的启动热路径修复和显式清理入口。
- Microsandbox materialized rootfs、per-session docker disk 和 stale sandbox state 的生命周期语义。
- v2 Connect API、CLI、数据模型、失败语义和测试验收口径。

本文不覆盖：

- `run --command` 跳过 Jupyter readiness 的优化。该问题会减少 command-only run 的固定启动等待，但不属于 cache 生命周期第一层修复。
- Docker daemon 自身 image/container/volume GC 的完整管理。Docker driver 仍由 Docker daemon image store 和 container lifecycle 负责。
- 自动后台周期性 GC。首版只提供显式命令和必要的当前 session rollback/stop 清理。

## 现状和 harness 约束

项目入口和服务边界：

- `AGENTS.md` 指定 `cmd/agent-compose/main.go` 是 daemon 和 CLI 入口，`pkg/agentcompose/` 负责 session lifecycle、runtime drivers、Jupyter proxy、loader scheduling、config persistence、LLM client 和 service setup。
- `AGENTS.md` 指定支持的 runtime drivers 为 `docker`、`boxlite`、`microsandbox`，默认 driver 为 `docker`。
- `docs/design/agent-compose_design.md` 明确 daemon 是状态权威，CLI 只读取本地 compose 并调用 daemon v2 APIs；因此 cache lifecycle 的真实判断和删除必须在 daemon service 层执行，CLI 只做参数校验和展示。
- `docs/design/agent-compose_design.md` 已有 `images`、`pull`、`rmi`、`image inspect` 命令调用 `ImageService` 管理 daemon image store，默认 store 由 `IMAGE_STORE_MODE` 选择。
- `docs/zh-CN/design/agent-compose-cli-improvement-plan.md` 明确 `pull` 不属于 runtime driver 能力，BoxLite/Microsandbox 只在启动 runtime 时从 OCI image 派生自身可运行 artifact。

runtime image 和 mount 现状：

- `docs/design/runtime_mount_manifest_driver_specific_design.md` 明确 Docker driver 使用 Docker daemon image store；BoxLite/Microsandbox 在 Docker daemon 可用时优先从本地 Docker image materialize，在 Docker daemon 不可用或 image 缺失时使用 daemon OCI cache。
- BoxLite 的 Dockerless 路径 materialize 到 `image-cache/<image-id>/oci` 并传给 BoxLite。
- Microsandbox 的 Dockerless 路径 materialize 到 `image-cache/<image-id>/rootfs`，并把 absolute rootfs path 传给 Microsandbox；absolute rootfs path 使用 `PullPolicyNever`。
- `pkg/imagecache.Cache` 的 OCI store root 是 `IMAGE_CACHE_ROOT`，默认由 service 初始化为 `<DATA_ROOT>/images`；materialized cache root 是 `filepath.Join(filepath.Dir(IMAGE_CACHE_ROOT), "image-cache")`，默认 `<DATA_ROOT>/image-cache`。

当前实现边界：

- `pkg/driver/boxlite_cgo.go` 的 `EnsureSession` 已移除启动成功后的 `cleanupLegacyBoxliteCaches()` 调用，启动热路径不再清理 BoxLite runtime derived cache。
- `pkg/driver/boxlite_cache_gc.go` 仍保留 `cleanupLegacyBoxliteCaches` 和 `cleanupLegacyBoxliteImageCaches` helper；其中 `cleanupLegacyBoxliteImageCaches` 会删除 `BoxliteHome/images/local` 和 `BoxliteHome/images/disk-images` 下所有子项。这些 helper 不应由启动热路径调用。后续如继续使用，必须只通过显式 cache 命令触发，并补齐引用检查和保护规则。
- `pkg/driver/microsandbox_runtime.go` 不存在同类全局 image cache 清理。它在 `StopSession` 和创建失败 rollback 中删除 `MicrosandboxHome/docker-disks/<session-id>.raw`，这是 per-session docker 数据盘；但 `prepareEnvironment()` 当前调用 `gcDockerDisks()`，会在 runtime 首次初始化时删除所有 `docker-disks/*.raw`，该行为需要纳入显式 cache/state lifecycle，而不是隐藏 startup sweep。
- `pkg/imagecache.Remove` 当前只更新 OCI metadata，并提示 blob cleanup deferred；它不是 runtime derived cache GC。

harness 质量约束：

- `TESTING.md` 规定测试分为 unit、integration、E2E，跨 service boundary、persistence boundary、runtime-driver behavior 或 user-facing workflow 的变更应增加更宽测试。
- `Taskfile.yml` 的主质量门禁是 `task lint`、`task build`、`task test`。
- `Taskfile.yml` 提供 opt-in real runtime smoke：`task test:runtime-smoke`，覆盖 BoxLite/Microsandbox 真实启动和 OCI cache consumption；该任务不属于默认 `task test`，但 runtime cache 变更完成后应按 driver 运行。
- 涉及 proto 变更时必须重新生成 Go proto、Connect Go 和 `proto-client` TypeScript 产物，并验证生成结果。

## 核心概念或领域模型

### Cache domain

`Cache domain` 表示 cache 项的所有权和删除语义。首版定义四类：

- `oci-image-store`：OCI image metadata、layout 和 blob store。当前由 `pkg/imagecache` 和 `ImageService` 管理，路径为 `IMAGE_CACHE_ROOT`。
- `materialized-image-cache`：从 OCI/Docker image 派生出的 runtime 输入形态。BoxLite 使用 `<DATA_ROOT>/image-cache/<image-id>/oci`，Microsandbox 使用 `<DATA_ROOT>/image-cache/<image-id>/rootfs`。
- `runtime-derived-cache`：runtime 自身为了快速创建 VM/container rootfs 维护的派生缓存。BoxLite 包含 `BoxliteHome/images/local`、`BoxliteHome/images/disk-images`、`BoxliteHome/bases` 等。Microsandbox 如 SDK 在 `MicrosandboxHome` 下维护 image/rootfs cache，也归入此类。
- `session-ephemeral-state`：某个 session/sandbox 专属、可在 session 停止或移除后删除的运行状态。Microsandbox 的 `docker-disks/<session-id>.raw` 和 stopped/orphaned `sandboxes/<name>` 属于此类；BoxLite box 目录、session VM state 也可按同一视图展示。

### Cache item

`Cache item` 是 CLI/API 展示和删除的最小对象。字段必须足以支持安全删除：

- `cache_id`：daemon 生成的稳定 ID，建议格式为 `<domain>:<driver>:<digest-or-name-or-path-hash>`。
- `domain`：四类 cache domain 之一。
- `driver`：`docker`、`boxlite`、`microsandbox` 或 `all`/空值。
- `kind`：更细粒度类型，例如 `oci-layout`、`materialized-oci-layout`、`materialized-rootfs`、`boxlite-disk-image`、`boxlite-base`、`microsandbox-docker-disk`、`microsandbox-sandbox-state`。
- `path`：daemon 本机路径。JSON 输出可包含；文本输出默认显示可读缩略路径。
- `size_bytes`：递归估算大小；无法读取时为 0 并附 warning。
- `image_id`、`image_ref`、`resolved_ref`：能关联 image metadata 时填写。
- `session_id`、`sandbox_id`：能关联 session/sandbox 时填写。
- `status`：`active`、`referenced`、`unused`、`expired`、`orphaned`、`unknown`。
- `removable`：是否允许当前请求删除。
- `blocked_reasons`：不可删原因列表。
- `last_used_at`：优先来自 runtime metadata；缺失时可使用 mtime 并标记 `last_used_source=mtime`。

### Reference

`Reference` 是阻止删除或解释删除安全性的证据来源：

- running/resuming session 的 `vm/runtime.json`、store session metadata、dashboard/session streams。
- stopped session 中仍可被 `resume` 复用的 VM/runtime state。
- current project/agent definitions 中的 image ref。
- OCI image metadata 中的 `ConfigDigest`、`ManifestDigest`、`LayoutCachePath`、`RootFSCachePath`。
- BoxLite DB 中 active box state。
- Microsandbox SDK/daemon 中 running/draining sandbox state。
- filesystem orphan detection：磁盘路径存在但 metadata/session/runtime DB 已无对应记录。

### Protection status

删除保护状态按保守规则计算：

- `active`：正在被 running/draining/resuming runtime 使用。绝不删除，即使 `--force`。
- `referenced`：没有 active runtime 使用，但仍被 stopped session、current project/agent image 或 image metadata 引用。默认不删除；只有明确参数允许时才可删除，且必须展示引用。
- `unused`：没有 active runtime、没有 session 引用、没有 current project/agent 引用，可删除。
- `expired`：超过用户指定 TTL，且不是 active。若仍 referenced，默认仍不删除。
- `orphaned`：磁盘存在但 metadata 不存在，可删除；首版仍应先 dry-run 展示。
- `unknown`：无法完成引用检查，默认不可删除。

## 架构和组件边界

### CLI

`cmd/agent-compose/main.go` 新增 `cache` 命令组。CLI 负责：

- 参数互斥和默认 dry-run 展示。
- 文本/JSON 输出格式。
- 将 `--driver`、`--type`、`--status`、`--older-than`、`--force` 等参数传给 daemon。
- 退出码映射：无可删项不是错误；删除被保护项返回非 0，除非请求是 dry-run。

CLI 不直接读取或删除 daemon 本地路径。

### Connect service

v2 新增 `CacheService`，由 `pkg/agentcompose/service` 实现。service 负责：

- 构建 cache inventory。
- 统一引用检查。
- 执行 dry-run 和 force 删除。
- 记录 warnings、blocked reasons 和删除结果。
- 使用 store/config/runtime provider/image cache 作为事实来源。

`ImageService` 保持 image store 管理边界，不承载 runtime cache 和 session ephemeral state。`ImageService.RemoveImage` 可以继续只删除 metadata；如后续需要和 runtime cache 联动，必须通过显式参数和 `CacheService` 的保护逻辑实现。

### Runtime driver

driver 层提供 driver-specific inventory/protection/removal adapter，但不在 `BoxRuntime` 基础接口上增加启动期 GC 语义。建议定义新的内部接口，例如：

```go
type RuntimeCacheInspector interface {
    ListRuntimeCaches(context.Context, RuntimeCacheListRequest) ([]RuntimeCacheItem, error)
    RemoveRuntimeCache(context.Context, RuntimeCacheRemoveRequest) (RuntimeCacheRemoveResult, error)
}
```

首版 adapter 可由 service 直接组合 helper 实现，不要求所有 driver 立即支持完整字段；缺失字段用 `unknown` 和 warning 表达。

BoxLite driver 的 `EnsureSession` 热路径已不再调用 `cleanupLegacyBoxliteCaches()`。如保留 legacy cleanup helper，只能由 cache command 显式触发，且不得按固定目录无条件清空当前有效 cache。

Microsandbox driver 必须保留当前 session 创建失败 rollback 和 `StopSession` 对本 session docker disk 的清理；但 startup 全量 `gcDockerDisks()` 应退出热路径，改由 `cache prune --type session-ephemeral` 的 inventory-aware 逻辑执行。

### Persistence

首版不新增持久化表。cache inventory 从现有事实源计算：

- `DATA_ROOT/data.db` 中的 current project/agent definitions 和 managed agent image/driver 信息。
- `SESSION_ROOT` 下 session metadata、`vm/runtime.json`、`proxy/jupyter.json`。
- `IMAGE_CACHE_ROOT/metadata.json` 和 materialized cache ready flags。
- `BoxliteHome/db/boxlite.db`、`BoxliteHome/images/*`、`BoxliteHome/bases/*`。
- `MicrosandboxHome/config.json`、`MicrosandboxHome/sandboxes/*`、`MicrosandboxHome/docker-disks/*`。

如果后续需要 last-used 精度或跨节点协调，可新增 cache metadata 表；首版不引入。

## API、CLI、配置、数据模型或协议变化

### CLI

新增命令：

```bash
agent-compose cache ls
agent-compose cache inspect <cache-id>
agent-compose cache prune
agent-compose cache rm <cache-id>
```

通用参数：

```bash
--driver <boxlite|microsandbox|docker|all>
--type <oci,materialized,runtime,session>
--status <active|referenced|unused|expired|orphaned|unknown>
--json
```

`cache ls`：

```bash
agent-compose cache ls --driver boxlite
agent-compose cache ls --driver microsandbox --type session
agent-compose cache ls --type materialized --json
```

文本输出列建议为：

```text
CACHE ID  DRIVER  TYPE  STATUS  REMOVABLE  SIZE  REF/SESSION  PATH
```

`cache inspect <cache-id>` 输出完整 item、引用证据、blocked reasons 和 warnings。

`cache prune` 默认 dry-run。以下命令只展示计划，不删除：

```bash
agent-compose cache prune --driver boxlite --unused
agent-compose cache prune --driver microsandbox --type session --orphaned
agent-compose cache prune --older-than 7d
```

真正删除必须显式：

```bash
agent-compose cache prune --driver boxlite --unused --force
agent-compose cache prune --driver microsandbox --type session --orphaned --force
agent-compose cache rm <cache-id> --force
```

`cache prune` 参数：

- `--unused`：只选择 `unused`。
- `--orphaned`：只选择 `orphaned`。
- `--expired` 或 `--older-than <duration>`：选择超过 TTL 的项。
- `--include-referenced`：允许删除 `referenced`，但仍不允许删除 `active` 或 `unknown`。
- `--force`：执行删除；没有 `--force` 时为 dry-run。

`cache rm <cache-id>` 默认 dry-run，实际删除也需要 `--force`。如果目标是 `active` 或 `unknown`，即使 `--force` 也失败。

### Connect API

在 `proto/agentcompose/v2/agentcompose.proto` 新增 `CacheService`：

```proto
service CacheService {
  rpc ListCaches(ListCachesRequest) returns (ListCachesResponse);
  rpc InspectCache(InspectCacheRequest) returns (InspectCacheResponse);
  rpc PruneCaches(PruneCachesRequest) returns (PruneCachesResponse);
  rpc RemoveCache(RemoveCacheRequest) returns (RemoveCacheResponse);
}
```

核心 message：

```proto
message CacheItem {
  string cache_id = 1;
  CacheDomain domain = 2;
  string driver = 3;
  string kind = 4;
  string path = 5;
  uint64 size_bytes = 6;
  string image_id = 7;
  string image_ref = 8;
  string resolved_ref = 9;
  string session_id = 10;
  string sandbox_id = 11;
  CacheStatus status = 12;
  bool removable = 13;
  repeated string blocked_reasons = 14;
  string last_used_at = 15;
  string last_used_source = 16;
  repeated CacheReference references = 17;
  repeated string warnings = 18;
}
```

枚举：

- `CacheDomain`: `OCI_IMAGE_STORE`、`MATERIALIZED_IMAGE_CACHE`、`RUNTIME_DERIVED_CACHE`、`SESSION_EPHEMERAL_STATE`。
- `CacheStatus`: `ACTIVE`、`REFERENCED`、`UNUSED`、`EXPIRED`、`ORPHANED`、`UNKNOWN`。

`PruneCachesRequest` 包含 filter、`older_than_seconds`、`include_referenced`、`force`。`force=false` 表示 dry-run。

`PruneCachesResponse` 必须包含：

- `dry_run`。
- `matched`：匹配的 cache items。
- `removed`：实际删除成功的 cache ids。
- `skipped`：因保护规则跳过的 items。
- `warnings`。

`RemoveCacheRequest` 包含 `cache_id` 和 `force`。

### 配置

首版不新增必需配置。

现有配置语义保持：

- `IMAGE_CACHE_ROOT` 仍是 OCI image store root。
- materialized cache root 继续由 `pkg/imagecache.Cache.MaterializationRoot()` 推导。
- `BOXLITE_HOME` 仍是 BoxLite runtime home。
- `MICROSANDBOX_HOME` 仍是 Microsandbox runtime home。
- `BOX_CACHE_TTL` 不再驱动 BoxLite 启动热路径 GC。若保留 TTL 行为，只能由 `cache prune --older-than` 或后续显式 scheduled maintenance 使用。

`.env.example` 仅在新增 operator-facing 配置时更新。首版新增 CLI/API，不要求新增环境变量。

### `rmi` 关系

`agent-compose rmi <image>` 继续只面向 image store/backend，不默认删除 materialized/runtime cache。

后续可增加显式参数：

```bash
agent-compose rmi <image> --prune-runtime-cache
```

该参数不属于首版；即使未来加入，也必须调用 `CacheService` 的 dry-run/protection 逻辑，不得在 `ImageService` 内直接删除 runtime cache。

## 工作流和失败语义

### BoxLite session start

目标语义：

1. `PrepareSessionStart` 解析 guest image，并在需要时 materialize 到 OCI layout。
2. `EnsureSession` 创建或复用 BoxLite box。
3. BoxLite 使用已有 `BoxliteHome/images/*`、`BoxliteHome/bases/*` 等 runtime derived cache。
4. 启动成功后不调用全局 cache cleanup。该语义已通过移除 `EnsureSession` 中的 `cleanupLegacyBoxliteCaches()` 调用落地。
5. 只保存本 session 的 VM/proxy state。

失败语义：

- materialize 失败表现为 session start failure。
- runtime derived cache 缺失时由 BoxLite 自行重建；但 agent-compose 不主动删除可用 cache。
- 如果 cache inventory 判断失败，不影响 session start；cache 命令中显示 `unknown`。

### Microsandbox session start/stop

目标语义：

1. `EnsureSession` 初始化 Microsandbox runtime 环境，但不执行隐藏的全量 `docker-disks` sweep。
2. `createSandbox` 为当前 session 创建 `docker-disks/<session-id>.raw`。
3. 如果当前 sandbox 创建失败，rollback 删除刚创建的当前 session disk。
4. `StopSession` 删除当前 session docker disk，并移除 stopped sandbox state。
5. crash 后遗留的 `.raw` 或 sandbox state 由 `cache ls/prune --type session` 显示和清理。

失败语义：

- 当前 session disk 删除失败记录 warning，不掩盖原始 stop error，保持现有 best-effort 行为。
- startup 不再删除其他 session 的 `.raw`；需要释放 orphaned disk 时由 operator 显式执行 prune。

### Cache list/inspect

`cache ls` 必须是只读操作。任何路径 stat、size walk、metadata read 失败都转为 item warning 或 top-level warning；除非根路径不可访问导致无法构建 inventory，才返回 RPC error。

`cache inspect` 找不到 `cache_id` 返回 `NotFound`。如果 item 存在但引用检查不完整，返回 item 且 `status=UNKNOWN`、`removable=false`。

### Cache prune/rm

删除流程：

1. 获取对应 cache domain 的互斥锁。OCI/materialized cache 使用 `imagecache.Lock` 或同级 lock；runtime home 使用 driver-specific lock file；session state 删除使用 session store/runtime lock。
2. 重新构建目标 item 的最新 inventory，避免使用 stale dry-run 结果。
3. 重新计算 `status` 和 `blocked_reasons`。
4. 若 `status=ACTIVE` 或 `UNKNOWN`，直接跳过或失败，不受 `--force` 影响。
5. 若 `status=REFERENCED` 且未设置 `include_referenced`，跳过。
6. 执行删除。
7. 返回 removed/skipped/warnings；删除失败返回 item-level error，批量 prune 继续处理其他 item。

删除必须只删除预期路径：

- 使用 canonical path 检查，目标必须位于对应 root 下。
- 禁止 follow symlink 删除 root 外路径。
- 删除 directory 前后都要验证 parent root。
- `cache rm` 的 `cache_id` 不允许直接作为 filesystem path 使用。

## 测试、质量门禁和验收标准

### Unit tests

覆盖：

- BoxLite `EnsureSession` 保持不调用 `cleanupLegacyBoxliteCaches()`；后续如重构 cache helper，应补测试证明启动成功后不删除 `images/local`、`images/disk-images`。
- BoxLite cache inventory 能识别 `runtime-derived-cache`、materialized OCI layout、active/referenced/unused/orphaned 状态。
- Microsandbox `prepareEnvironment` 不再全量删除 `docker-disks/*.raw`；当前 session 创建失败 rollback 仍删除本 session disk。
- Microsandbox session-ephemeral inventory 能识别 `.raw` 对应 session、orphaned disk 和 active sandbox。
- `cache_id` 生成稳定，且 path traversal/symlink escape 被拒绝。
- prune protection：`active`、`unknown` 永不删除；`referenced` 需要 `include_referenced`；`unused/orphaned` 可在 `force` 下删除。

### Integration tests

覆盖：

- v2 `CacheService` list/inspect/prune/remove handler，使用临时 `DATA_ROOT`、`SESSION_ROOT`、fake runtime homes 和 fake/stub references。
- CLI `cache ls`、`cache prune`、`cache rm` 文本和 JSON 输出。
- `cache prune` 默认 dry-run，不删除文件；加 `--force` 后才删除。
- `ImageService.RemoveImage` 和 `agent-compose rmi` 不默认删除 runtime/materialized cache。
- run/session store 中 running session 引用会阻止 cache 删除。

### Runtime smoke

涉及真实 BoxLite/Microsandbox 的变更完成后，按范围运行：

```bash
SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke
```

验收重点：

- BoxLite 连续启动同一 guest image 时保持不因 agent-compose 启动成功后的 cleanup 导致 `images/disk-images` 被清空。
- BoxLite `cache ls --driver boxlite` 能展示 runtime derived cache。
- Microsandbox `cache ls --driver microsandbox --type session` 能展示 stopped/orphaned docker disk 状态。
- materialized OCI cache consumption 仍符合 `docs/design/runtime_mount_manifest_driver_specific_design.md` 中的 Dockerless 路径。

### 质量门禁

局部开发阶段至少运行：

```bash
go test ./cmd/agent-compose ./pkg/agentcompose/service ./pkg/driver ./pkg/imagecache
task build
```

合并前按 harness 运行：

```bash
task lint
task build
task test
```

如果触达 proto-client：

```bash
cd proto-client && npm ci && npm run gen && npm run build
```

## 首版不做事项

- 不实现自动后台周期性 GC。
- 不实现跨节点/多 daemon cache 协调。
- 不提供 UI 页面；首版只提供 v2 API 和 CLI。
- 不让 `rmi` 默认删除 runtime derived cache。
- 不删除 Docker daemon image/container/volume cache。
- 不把 BoxLite/Microsandbox cache 清理挂进 session start/resume 热路径。
- 不支持按任意 filesystem path 删除 cache；所有删除都必须通过 inventory 生成的 `cache_id`。
- 不要求首版精确记录 last-used；缺失时可用 mtime 并显式标记。

## 关键假设和已确认决策

- 已确认首要修复范围只关注第一层：避免不必要 cache 清理，并设计手动生命周期管理机制；command-only 跳过 Jupyter readiness 另行处理。
- BoxLite 启动热路径清理全局 runtime derived cache 的直接调用已移除；后续工作集中在显式 cache lifecycle API/CLI 和保护规则。
- 已确认 BoxLite 和 Microsandbox 需要统一设计，但现状不同：BoxLite 后续重点是 runtime derived cache 的显式清理入口；Microsandbox 主要是 session-ephemeral state 的隐藏 startup sweep，需要可观测并显式化。
- cache lifecycle 是 daemon 权威能力，CLI 不直接读写 daemon 本地 cache 路径。
- `pull`/`rmi` image domain 与 runtime cache domain 保持分离，符合 `docs/zh-CN/design/agent-compose-cli-improvement-plan.md` 的 driver-independent OCI image 设计。
- 删除策略保守优先：无法证明可删时视为 `unknown`，默认不可删除。
