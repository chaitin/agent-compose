# Workspace、skills 与 sandbox 内容复用

本方案回应 [#674](https://github.com/chaitin/agent-compose/issues/674)、[PR #686 的评审](https://github.com/chaitin/agent-compose/pull/686#issuecomment-5614654801)及其[静态分析附件](https://github.com/chaitin/agent-compose/pull/686#issuecomment-5614767211)。附件的基线是 main `e6857fdf` / PR `34f9f8dc`。下文区分已核实的问题、实现约定和验证范围，避免把静态推断当成运行结论。

附件中的 daemon 指守护进程。相关讨论落在输入快照、容器路径换算和 K8s 文件交付，而不是单独的示例项目。

## 1. 结论与范围

仅增加 `workspace.mode: mount` 不足以解决 #674。需要同时处理临时副本的生命周期、重复分发 skills、隔离副本的复制方式，以及不同 driver 的交付机制。

本次采用四项互补措施：

1. 默认 `copy` 保留隔离语义；临时 run 快照在完成交付后回收。
2. skills 保持每个 sandbox 私有可写，未变化的内容跳过复制；发生变化时先准备完整目录，再发布。
3. 文件复制优先使用文件系统 clone/reflink，不支持时完整复制。可写副本禁止使用共享 inode 的硬链接。
4. Docker 的 `mount` 保留为明确选择的实时源目录映射，并与已有 bind volume 共用宿主路径解析。

这些措施的收益不同：**回收快照减少长期文件数量，跳过 skills 更新减少重复建删文件，reflink 减少数据块复制，mount 才会取消该工作区的私有文件副本**。reflink 并不消除每个可写副本的 inode。

`.codex` 也必须区分位置：项目源码里的 `.codex` 属于 workspace，遵循 workspace 的 copy/mount 选择；sandbox 的 `home/.codex` 则可能包含凭据、配置和历史，保持私有状态所有权。

| 附件条目 | 核实结论与处理 | 主要开发入口 |
| --- | --- | --- |
| H1：run 快照增长 | 已确认；补所有权、lease、Ready 后及恢复回收 | `pkg/workspaces/transient_snapshot*`、`provisioner_snapshot.go`，以及 runs / scheduler / sandboxstore 接入 |
| H2：K8s 复用丢修改 | 原文“每次全量覆盖”不符合已有门控；保留门控，修实际 skills / Pi 交付缺口 | `pkg/agentcompose/adapters/agent_runner*`、`pkg/driver/k8s_runtime_guest*` |
| H3：skills 每次全删全拷 | 已确认；完整内容指纹、私有 staging、一致发布及恢复 | `pkg/execution/agent_skill*`、`pkg/skills/resolve.go` |
| H4：文件复制方式 | 补安全 clone / fallback，保持双向写隔离 | `pkg/workspaces/copy_file*`、`remove_owned_directory.go` |
| H5：宿主路径信任边界 | 明确 daemon API 的管理权限，不把项目根校验解释成多租户授权 | YAML manual 及本文第 7 节 |
| H6：路径换算重复 | 共用 bind source resolver，保留不同来源合同及 fail-closed 行为 | `pkg/driver/docker_bind_source*` |

## 2. 复制与挂载清单

| 环节 | 内容流向 | 原行为及必要性 | 本次处理 |
| --- | --- | --- | --- |
| C1 | 项目 file 来源 → run 临时快照 | 冻结本次准备的输入；原来随 run 增长且缺少回收 | 独占 generation、显式所有权、交付后回收 |
| C2 | 快照 → sandbox workspace staging → workspace | 提供可隔离修改的工作区；Ready 后不再复制 | 保留私有副本，使用安全 clone/copy；Ready 持久化后释放 C1 |
| C3 | Git → sandbox workspace | 每个 sandbox 获取独立 checkout | 保留；共享 Git object cache 属于后续独立优化 |
| C4a | skill 来源 → 内容缓存 | 按内容解析 file/git/zip 等来源 | 完整内容及可执行权限指纹；保护正在交付的缓存不被 prune |
| C4b | skill 缓存 → `home/.agents/skills` | 原来每次 run 全删全拷 | 比较来源和实际目标，未变不重建；变更完整 staging 后发布 |
| C5 | `.agents/skills` → `.claude/skills` | 相对链接；不能链接时为受管理的私有副本 | 正确链接不重建；受管理 fallback 按内容更新 |
| C6 | 内嵌默认文件 → provider home | 仅初始化缺失文件；与凭据/历史混合 | 继续逐 sandbox 隔离，避免整体共享 home |
| C7 | daemon workspace/home/skills → K8s Pod | 没有共享宿主文件系统，需要传输 | 保留初始化门控；skills 内容比较后仅传 canonical tree，并维护 alias；补 Pi 配置同步 |
| C8 | sandbox → retention archive | 留存日志等审计材料，跳过 workspace/volume | 不改变归档与用户数据所有权 |
| C9 | OCI layer → image materialization | 不可变镜像缓存已有复用策略 | 不把其硬链接策略套用到可写 workspace/skills |

Docker 将 sandbox 自有 workspace、state/runtime/logs 和 provider home 条目分别 bind 到 guest；BoxLite/Microsandbox 以 sandbox 数据目录共享及 guest 链接提供相应布局。K8s 使用 exec/tar 传输，named volume 由 PVC 承载。用户声明的外部 bind volume 与这些自有副本有不同的所有权。

## 3. 临时快照的所有权与回收

不能简单按 workspace ID、目录名或“看起来像 run”推测哪些路径可删。Settings workspace 预设和 run 输入有不同的生命周期。新临时快照使用独立目录、独占 generation ID 及版本化 ownership marker；逻辑 workspace ID 与物理 generation 分离。

```text
准备 run / scheduler inline workspace
    |
    +-- 创建独占 generation + ownership marker
    |      |
    |      +-- 准备失败 / 选择复用已有 sandbox -> 创建方回收
    |      |
    |      +-- 新 sandbox 持久化引用 -> sandbox 接管
    |              |
    |              +-- Pending / Failed -> 保留输入供重试
    |              |
    |              +-- workspace Ready 成功写入存储 -> 回收临时输入
    |
启动及周期恢复（获取 generation 的文件锁之后）
    +-- 活跃准备的 lease 正被持有 -> 跳过
    +-- 保留待处理 / 失败 / 未知状态的引用
    +-- 回收已 Ready 或无引用的自有 generation
    +-- 不根据名称猜测或删除旧预设、旧无 marker 目录
```

物理输入放在 `data/workspace-snapshots/<uuid>/content`。`SnapshotID` 只由内部准备产生并持久化到 sandbox；临时 `SnapshotLease` 只在内存中传递，不进入 API、JSON 或 sticky hash。生成协议和旧 sandbox JSON RPC 都显式投影公共字段，RPC 不会直接序列化带内部字段的持久化模型；全部旧字段及空值行为有完整合同回归。每次准备持有独占 OS 文件锁；Store 成功保存 sandbox metadata 后，持久 Pending 引用接管输入，准备方释放锁。准备失败或复用已有 sandbox 也会释放自己的 lease。回收方非阻塞取得同一个锁后重新读取 ownership 和 `GetSandbox`，因此另一个 daemon 不会把仍在准备的输入当成旧孤儿；部分创建在 metadata 已保存、索引尚未更新时仍保留重试输入。

极早的强制中断（lease 已创建、ownership marker 尚未发布）可能只留下空锁元数据，此时还未开始内容复制。未知或半写 marker 保守保留并报告，不能把“交付后回收输入”解读为所有强制终止点都完全不留元数据。

必须覆盖以下对称情况：

- Ready 写入存储失败时不能先删除输入，否则重试失去数据。
- 新 run 最后复用 sticky/显式 sandbox 时，其刚准备但未使用的输入也要释放。
- inline scheduler 的逻辑 workspace ID 可以稳定，但不同消费者不能共享被反复 reset 的物理目录。
- generation ID 不参与 sticky 配置 hash；源码配置的语义字段仍须全部参与。
- 删除 sandbox 时清理它仍持有的自有输入；中断清理在下次启动或相应生命周期调用中可重试。
- 历史版本遗留的无标记目录需要单独审计，不能在升级时启发式删除。sandbox 工作区保留期仍由现有策略控制。

## 4. Skills：私有内容复用与一致发布

### 4.1 内容合同

目标是相同内容不重建文件，同时保留原有“下一次准备恢复所声明 skills”的行为。只记录来源 fingerprint 不够：guest 可以修改私有技能目录，所以还要检查实际目标。

比较合同覆盖完整相对路径集合、文件/目录类型、空目录、可执行权限、文件大小和 SHA-256 内容；时间戳不作为内容身份。普通写权限不被提升为共享只读策略。符号链接与特殊文件继续遵循明确的拒绝约定，不隐式跟随到缓存或项目之外。

来源解析与投影之间通过 `Resolver.WithResolved` 持有缓存根的共享文件锁；cache prune 获取同一文件的排他锁，所以不会在消费回调完成前删除来源。锁只持续到当前准备操作结束，没有永久 GC pin。根锁及单 artifact 锁的等待都响应取消。

file/ZIP 缓存改为独立的 `content` 子目录，`.artifact.json`、`.ready` 留在外层，避免 LastUsedAt 元数据触发无意义的内容更新。新 file/ZIP cache key 带版本区分，旧 artifact 不原地迁移，仍可通过已有 cache 管理清理；Git 已有的 `entry/content` 布局和 key 保持兼容。

### 4.2 宿主共享文件系统的 driver

Docker、BoxLite、Microsandbox 都能看到宿主上的私有 skills 目录。未变内容保留 inode；变化内容先复制到私有 staging、校验完整内容，再发布。Linux 和 macOS 使用支持目录交换的文件系统操作替换已有目录，失败不能退回“先删旧目录、边拷边暴露新目录”。

同一 sandbox 的准备通过可取消文件锁串行化。每个 staging 带明确 ownership marker，发布记录保留原 inode 身份及原 manifest；正常失败/取消回滚已发布目录和 manifest，进程中断后下次准备按记录恢复。已经提交的旧副本及未发布的自有 staging 会被清理，包含只读目录权限的副本也有清理路径。无正确标记的同名前缀目录保守保留；进程若恰好中断于 mkdir 与 marker 写入之间，该未知目录也不会被启发式删除。

目标内容读取失败（例如 EACCES）会返回错误，而不是当成变化后覆盖。Claude alias 仅接受正确的相对 symlink，或 regular marker 内容明确匹配的受管理 fallback；正确链接与不变 fallback 保留 inode。发生 fallback 更新时，技能各复制一次，额外的用户 canonical 条目单独保留，不再先全拷旧树后再全拷所有技能。

目录发布保证单个公共路径不会指向尚未准备完的树；它不提供跨多个文件打开操作的数据库式读取事务。不同 sandbox 的可写私有目录仍然相互隔离。

### 4.3 K8s 的传输

K8s 的宿主目录未变化，不代表 Pod 中的目录未变化。每次 skills 准备检查 guest 实际内容：一致时跳过 tar；缺失、修改或新 Pod 时重新交付。探针位于 AgentRunner 的适配层，使用该 runner 本来就依赖的 Node 和其内置 `fs`/`crypto`，不增加模型或网络依赖。无法执行、权限错误和损坏响应不能被当成成功。Node 与 Go 按同一合同比较完整条目：相对路径、文件/目录类型、执行权限位、大小、内容 SHA-256；只排除 canonical 根的内部 manifest。guest canonical 可以是本机制的软链接，其内部软链接与特殊文件不能被当作有效内容。

只传 canonical skills tree。`.claude/skills` 保持指向 canonical tree 的相对链接，避免同一批内容传两份。完整传输在私有 generation 中完成，后续通过原子替换 canonical 链接发布；旧 generation 及中断 staging 有明确回收路径。

兼容旧版本真实目录时，首次迁移存在有界的 rename → link 窗口；不能把它称为零窗口的原子目录交换。普通失败恢复旧目录，中断后下次操作先恢复可用状态。该专用路径不改变整 workspace/home 的通用传输语义，也不能绕过 PVC 所有权保护。父目录、控制目录及恢复路径不能通过非预期软链接跳出私有 home。

该交付合同明确依赖 Node、sh、tar、flock 和常规文件工具。锁由 `flock` 的文件描述符持有，进程退出即释放，残留锁文件不会形成永久占用。归档只有在源树完整读取成功后才追加随机完成条目；即使失败的生产者已传出可解包的 tar 前缀，guest 也不能发布半份内容。Claude alias 先检查/建立，再更新 canonical，避免 alias 冲突时已经替换 canonical。

canonical 与 alias 的旧目录迁移使用各自有所有者标记的 `previous` 备份，下次操作恢复中断的 rename → link 窗口，并回收未发布版本。控制目录首次创建、尚未写入所有者标记时若被强制终止，后续会保守拒绝该未标记目录，不会把未知目录当作本任务垃圾删除。这不是宿主与 guest 间的分布式原子事务：guest 已发布但宿主确认失败时，下一次仍按实际内容比较并校正。

## 5. Provider home 与 K8s 生命周期核查

附件 H2 声称“普通复用 sandbox 的每次 run 都推送完整 workspace/home”。这不符合现有调用门控：

| 入口 | 已有门控 | 推论 |
| --- | --- | --- |
| 普通 run | `runs.prepareFreshStartAgentEnvironment` 跳过已运行/已启动且未主动释放的 runtime | 不会在普通复用时整体覆盖 |
| scheduler | `SchedulerSessionRunner.loadOrResumeLocked` 对 Running 直接复用，并区分首次启动/主动释放 | 不能只看 prepare 函数的存在推断每次都调用 |
| RPC / session | `session_rpc_bridge` 的 fresh-start 准备同样区分已有 runtime | 保留其生命周期合同并补回归测试 |

附件“没有 mount，所以 K8s 不具备相同相对布局、必须复制两份 skills”的推论也不成立：guest 的 `.agents` 和 `.claude` 相对布局仍然可用，tar 本身也支持 symlink。

实际需修的是 skills 重复传输，以及 retained K8s runtime 中新生成的 Pi `models.json` 只更新了 daemon 文件而没有推送 guest 的缺口。初次 workspace/home 同步放在每 run 的 skills、system prompt、facade 配置和 MCP 文件生成之前，因此后来的整 home 归档不会覆盖刚发布的 skills。生成内容通过下列专门路径送达：

| 生成内容 | guest 交付方式 |
| --- | --- |
| 已有 home 私有文件、内嵌默认文件（仅缺失时播种） | fresh start 的 home 归档 |
| Codex `.codex/config.toml`（模型、facade、策略、MCP） | `WriteCodexMCPConfig` 的单文件同步，包括无 MCP 时已有模型配置 |
| OpenCode `.config/opencode/opencode.json` | `WriteOpenCodeMCPConfig` 的单文件同步 |
| Pi `.pi/agent/models.json` | 每次生成 facade 配置后同步；失败阻止本次 agent 执行 |
| 系统提示、MCP 通用配置、提示和 schema 等 state 文件 | 原有 `GuestFileWriter` 专门写入 |
| Claude、DSH facade；startup OpenAI/Anthropic token 和 endpoint；所有 provider 的运行时环境 | 运行时环境合并与命令注入，不依赖 home 归档 |

六种 provider 的 fresh-home 回归枚举全部宿主生成文件，逐字节比较 guest 对应文件，并固定生成路径清单；新增生成文件若没有单独交付会直接失败。Pi 连续执行回归同时检查模型变化、私有历史不被重播种、推送失败阻止执行和重试成功。startup facade 只写 token store 并返回环境，不会生成隐藏的 home 文件。

PVC 数据继续归用户所有。当前 workspace/home 初始传输对覆盖 PVC 的路径有保护并跳过；本次不声称自动完成已有 PVC 的源目录播种。K8s 实际集群验证需要可用集群，本地脚本及 fake API 测试只能验证协议、路径和文件行为。

## 6. Copy 的实现与隔离

Linux 优先 FICLONE；macOS 优先文件系统 clone。跨文件系统或不支持 clone 时回退普通字节复制；磁盘满、I/O 损坏等真正错误应返回，不能笼统吞掉所有 clone 错误。

必须验证两个方向：修改副本不能影响来源，之后修改来源也不能改变已经交付的副本。两个 sandbox 从同一来源准备，也不能通过共享 inode 串写。特殊文件不能当普通文件打开并无限阻塞；现有 symlink 拒绝策略继续保留。

复制请求沿调用链传递 context，fallback 每次读取检查取消，clone 在系统调用前后检查取消。文件以非阻塞、不跟随最终 symlink 的方式打开并再次检查类型，静态与竞态出现的 FIFO 不会被当作普通文件持续读取。文件和目录保留普通权限位。清理先使用当前权限执行 `os.Root.RemoveAll`，不为可直接删除的 guest 目录额外 chmod；只有权限不足时，才对明确自有的 generation/staging 目录尝试补齐 owner 所需权限，不跟随 symlink。普通用户 daemon 可以回收自己拥有的只读目录，也可以删除其当前权限允许删除的其他 UID 目录；实际无权删除的非空目录会明确失败并保留受保护内容。

reflink 的实现与快照 GC 分开：即使文件系统不支持 reflink，快照回收和未变 skills 跳过仍然有效。不同文件系统的性能结果不能混为一组承诺。

## 7. Docker 路径解析与信任边界

工作区、sandbox 自有路径和声明的 bind volume 共用解析实现：先处理明确的 `DOCKER_HOST_SANDBOX_ROOT` 映射，再根据 daemon 容器最深的包含挂载换算。更深的 tmpfs 或无法分享的挂载会遮住父 bind；不能穿过它继续用父路径。

| 来源合同 | 显式 SandboxRoot 映射 | 容器共享路径映射 | 未证明共享的路径 |
| --- | --- | --- | --- |
| 已有自有路径 / bind volume | 保留已有配置行为 | 共用最深挂载算法 | 保留已有显式宿主路径合同 |
| workspace `mode: mount` | 来源在 SandboxRoot 下时使用；外部来源另行解析 | 共用同一算法 | 需要可验证本地 Engine，否则拒绝 |

二者共用路径机制，但 `workspace.mount` 额外承诺挂入 daemon 校验过的项目来源，因此不能把远端另一个同名路径当成该目录。原生 daemon 先验证本地 Engine，不会因为 Engine 中恰有一个与宿主同名的容器，就误用那个容器的挂载。容器内 daemon 则必须通过自身共享挂载映射。原生 mount 支持规则、递归只读、镜像 VOLUME 重叠检查和来源失效后的 inspect/stop/remove 能力继续保留。

**daemon API 属于受信任的管理边界。** 管理客户端可以提供项目 `source_path`，已有 bind volume 也允许声明宿主路径。项目根包含校验是输入一致性约束，不是对任意 API 管理者的多租户路径授权。运维方应按管理接口的权限配置监听地址和认证；不要把接口开放给只能访问某个项目目录的低权限租户并期待此校验形成隔离。

## 8. 验收与证据要求

| 机制 | 必须断言的结果 | 验证层次 |
| --- | --- | --- |
| 临时快照 | Ready 后无自有临时输入；失败持久化保留；复用不泄漏；重启回收孤儿；预设不受影响 | 状态矩阵、真实存储集成、Docker E2E 文件计数 |
| 默认 copy | 来源与副本双向隔离；稳态只保留 sandbox 所需副本 | clone/fallback 测试、E2E 全树 SHA/文件数 |
| workspace mount | 零源文件副本、根/子目录、RW/RO、双向可见、生命周期不删来源 | Docker 真实运行；Linux 实际 tmpfs 子挂载验证递归 RO |
| skills 宿主投影 | 未变 inode 不变；增删改/可执行位触发更新；客体改写恢复；失败/取消不留半目录 | 文件系统测试、并发/race、跨 sandbox 隔离 |
| skills K8s 投影 | 未变不推送；guest 修改/缺失/新 Pod 重推；canonical 与 alias 一致；失败可恢复 | 真实 Node/shell + fake API；集群 E2E 单独报告 |
| provider home | 各 sandbox 独立；已有状态不被普通复用覆盖；Pi 生成配置同步到 guest | 适配器及生命周期回归测试 |
| 配置/API | 默认兼容；物理代次不影响 sticky；不新增内部所有权响应字段；未知字段漂移迫使更新测试 | 完整模型/投影/hash 合同测试 |

仓库提供 `guest-images/Dockerfile.workspace-test` 和 `task image:workspace-test`。镜像从当前源码及 lockfile 构建真实 JavaScript runtime，适合无模型的文件交付测试；不替代完整 provider/Jupyter 镜像验证。执行方式与文件计数参数见 [TESTING.md](../../TESTING.md)。

### 基线与新实现不得混用

`34f9f8dc` 的远程 Linux 基线已实测：Docker 10,003 个源文件在 copy 模式留下 20,006 个副本，四个 mount 组合均为 0；实际 tmpfs 子挂载的 root/subdir × RW/RO 四个组合通过。它们是扩展修改前的基线，不证明本次快照/skills 修改已通过。

本机 BoxLite/Microsandbox CLI 的独立读写探针也不是 agent-compose Go driver 的正向集成结论。远程 full build 编入四个 driver，但测试机没有 `/dev/kvm`；VM 启动受环境阻挡。非 Docker workspace mount 的拒绝合同可以验证，VM 客体正向运行不能据此标为通过。没有 K8s 集群时也不声称集群 E2E 通过。

### 本次修改的实测结果

验证使用 Go 1.26.7；远程为 Ubuntu 24.04 / Linux 6.17 / Docker 29.1.5 API 1.52 / linux/amd64。冻结的非报告源码指纹为 `89e3cd2721877042aca179f9161ad5cfaa66e6e2a1001ecbf047965d12f533d6`；该指纹按源码清单中除 `docs/design/` 外的每个路径与 SHA-256 排序计算。该镜像对应随后推送的 `adecc5b9` 生产代码。后续 `81a43e7b` 及本轮编译标签、协议 golden 的补充只修改测试/报告，执行代码保持一致。

本次 full Linux 二进制 SHA-256 为 `01b2c9114e61ae1e80fc0bc4a403dbe0cee99b130a6547ef10c88e1d1aa38cae`，版本标记 `pr686-review-worktree`，编入 Docker、BoxLite、Microsandbox、K8s。Daemon 测试镜像复用既有官方构建的 runtime 依赖，仅替换本次 `task build` 产物；它不是重新下载所有依赖的干净官方镜像构建。Guest 镜像按当前 `Dockerfile.workspace-test` 及 lockfile 构建，并实际运行 runtime `--help`。

| 镜像 | 本次 Image ID |
| --- | --- |
| daemon 测试镜像 | `sha256:4eddcfa5041e5464c88c7262ab6c92d2ebf524f89d52f3ee8f83eaab1561ba62` |
| workspace-test guest | `sha256:7242fb970a7f49659708f10dba14c570c91cef17a90922186fd88a54f07871cd` |

两镜像的 revision label 均指向上面的源码指纹。

| Docker workspace 用例 | 源文件数 | 稳态源文件副本数 | 观测准备耗时 |
| --- | ---: | ---: | ---: |
| 默认 copy / 根目录 | 10,003 | 10,003 | 10,582 ms |
| mount RW / 根目录 | 10,003 | 0 | 2,348 ms |
| mount RW / 子目录 | 10,003 | 0 | 2,932 ms |
| mount RO / 根目录 | 10,003 | 0 | 2,264 ms |
| mount RO / 子目录 | 10,003 | 0 | 2,420 ms |

五项 E2E 全部通过，包括完整内容哈希、读写可见性/隔离及删除后源目录保留。该轮同时运行其他验证，耗时只作为观测值，不能与前一轮空闲基线作性能倍率比较。文件副本计数是确定性断言：copy 从旧版稳态 `2N` 变为 `N`；初始准备仍可能短暂同时持有输入快照和目标副本。

容器内 daemon 的实际 tmpfs 子挂载验证也为 **4/4 PASS**（根/子目录 × RW/RO，34.067 秒）。每个 RO 场景在父目录与子挂载上共尝试 8 次创建、覆盖、重命名和删除，全部返回 `EROFS`；RW 场景全部成功。host 修改对两种模式均可见，guest state/logs 保持可写，host 原始 tmpfs 仍为 RW。公共删除保留来源和 host 子挂载；测试结束后四个临时挂载及测试容器均清理成功。

专用镜像的 skills E2E 使用 257 个源文件、两个 sandbox，完整场景 **PASS（21.37 秒）**。三次未变 Prompt 的 canonical 262 个条目（含 258 个文件及 manifest）、全部 inode、内容哈希和 Claude alias 均保持不变；运行观测为 962 / 757 / 1,057 ms。随后验证 guest 增删改及执行位修复、源内容更新、两个 sandbox 双向隔离、删除后源数据保留。两次 Command bootstrap 没有调用测试 CLI；六次真实 Prompt 经 runtime/Codex SDK 调用本地 NDJSON fixture，第一 sandbox 5 次、第二 1 次，无模型调用。

缓存和私有副本分开计数：初始 cache 257、两个 sandbox 合计 514；来源改变后 cache 两代合计 514、sandbox 私有副本仍为 514。旧 cache 代次交给既有缓存 GC，不声称跨 sandbox 零副本共享。

**身份与权限也属于验证条件。** 完整 skills 场景以 UID 0 daemon / UID 0 guest 执行，与发出的 daemon 镜像身份一致。UID 1000 daemon / UID 0 guest 的同一场景已完成上述六次 Prompt 和内容断言，但最终整个 sandbox 删除失败于 `state/agents/providers/codex.json`。旧版 `34f9f8dc` 在一个没有 skills 的单 Prompt 场景也返回相同 HTTP 500：runtime 的 `writeStoredThread` 创建 root-owned `providers` 0755 / `codex.json` 0644，而 store 使用宿主 `os.RemoveAll` 删除；关键创建、挂载、清理代码与本次版本字节相同。因此它是既有混合 UID 的状态目录清理限制，不能计作完整非 root E2E 通过，也没有用放宽 provider 文件权限掩盖问题。

此次实跑发现并修复的 skills 清理问题有独立 **8/8 Linux 跨 UID 回归**：root 创建夹具后，由真实 UID 1000 子进程清理。覆盖 foreign 空目录 0755 / 0555 / 0000、foreign 可写非空目录 0777 / 0577、自有只读非空目录 0555、foreign 受保护非空目录的正确错误与保留，以及外部软链接目标的权限/内容不变。

可复现的主要入口：

```bash
GOTOOLCHAIN=go1.26.7 task build
task image:workspace-test
AGENT_COMPOSE_E2E_BINARY="$PWD/build/agent-compose" \
AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE=agent-compose-guest:workspace-test \
AGENT_COMPOSE_E2E_WORKSPACE_FILES=10000 \
  go test ./test/e2e -run '^TestE2EDockerWorkspaceMount$' -count=1 -timeout=15m -v
# skills 完整删除场景应使用具备 guest 状态目录清理权限的 daemon 身份。
AGENT_COMPOSE_E2E_BINARY="$PWD/build/agent-compose" \
AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE=agent-compose-guest:workspace-test \
AGENT_COMPOSE_E2E_SKILL_FILES=257 \
  go test ./test/e2e -run '^TestE2EDockerSkillsReuse$' -count=1 -timeout=15m -v
```

镜像任务强制 `--platform linux/amd64`，并实际运行 runtime。Linux 的常规 Compose 配置测试需要工作目录有 `.env`（隔离验证目录使用空文件即可）；完整 `task test` 不要求创建真实 sandbox，它的覆盖率 E2E 与上面的显式 Docker 测试分别报告。

### 最终测试与覆盖率

本机 `task build`、8 个改动相关包的完整测试、对应 lint/格式检查、文档构建均通过。远程 Linux full build、完整 `task test` 和额外四 driver 标签的九包测试已完成：

| 检查 | 结果 |
| --- | --- |
| 完整 `task test` | PASS；Unit 76.77%、Integration 60.51%、E2E 60.39%、Combined 81.06% |
| 额外九包四 driver 标签测试 | 2,417 PASS / 50 SKIP / 0 FAIL，含子测试；跳过不等于运行通过 |
| 三／四 driver 编译清单 fixture | 各 1 RUN / 1 PASS / 0 SKIP，编译条件互斥 |
| `scripts/tests/test-coverage-contract.sh` | 本机与 Linux 均 PASS，完整 15 项 E2E 名称 golden 与实际清单一致 |
| Linux proto 模块 | 28 RUN / 28 PASS / 0 SKIP / 0 FAIL，包含 Sandbox 全 25 字段 golden |
| workspace / execution / skills race | 271 PASS / 1 SKIP / 0 FAIL；跨 UID 清理回归通过 |
| 文档构建 | 本机与 Linux PASS；73 个 YAML schema 字段、12 个公开文件校验通过 |
| `task lint` | Linux 全部 scope 完成，0 issues；格式检查通过 |
| 追加 qemu 镜像行为测试 | 安装工具后精确重跑 6 项：6 RUN / 6 PASS / 0 SKIP / 0 FAIL |

四 driver 标签测试的 50 项跳过逐项分类为：30 项是当前 driver 已编译而不适用的“未编译拒绝”分支，13 项是未显式启用的 runtime/OCI smoke，6 项缺少 `qemu-img`，1 项是 root 会绕过普通用户的不可读权限夹具。race 的唯一跳过也是最后这一权限条件，专门跨 UID 的清理测试实际通过。

随后仅在独立临时测试容器安装 `qemu-img/qemu-io 7.2.22`，六项缺工具的用例实际全部运行通过，追加结果与原 50 项跳过分开记录。它们验证 base cache 命中不重新物化、拒绝既有 backing、两个 qcow2 overlay 经真实 qemu-io 写入后相互隔离且 base 不变、并发创建收敛、不完整文件对修复，以及 ownership/backing 不一致拒绝。该验证不需要 KVM，也没有启动 VM 客体；不会据此声称 BoxLite/Microsandbox 的 Go driver 客体 E2E 已通过。

此前 59.17% 的 E2E 覆盖门禁失败已经通过实际公开服务用例补齐。新增用例使用真实 HTTP 路由、应用依赖、SQLite、控制器、Provisioner、SandboxDriver 和 AgentRunner；仅替换外部 runtime 执行及 Docker image-inspect 协议端点。两次 ApplyProject、八次 RunAgent（七成功、一次别名冲突预期失败）及两次 RemoveSandbox 逐次验证快照回收、Ready、无残留 stage、未变 inode、guest 漂移修复、source 内容/执行位更新、声明删除和双向隔离。原 Go E2E 覆盖为 23,759 / 39,819，新结果为 24,281 / 39,819，增加 522 条语句；合并 JS/SDK 后是 25,796 / 42,716 = 60.39%。没有降低阈值、调整覆盖范围或改名凑分类。

`test/e2e` 的清单问题也已定位到真实 Actions：main 原有 13 项，`34f9f8dc` 增加 workspace mount 变成 14，`adecc5b9` 增加 skills reuse 变成 15，而旧断言始终是 13。[GitHub Linux 日志](https://github.com/chaitin/agent-compose/actions/runs/34461231052/job/102821965553) 实际报告 `got 15, want 13`。`81a43e7b` 用完整名称 golden 修复它；公开服务 E2E 位于 app 包，不计入这 15 项。三 driver fixture 另补 `!k8scompose`，与原四 driver fixture 互斥，不改变真实 driver 清单。

### Published proto 编译与发布顺序

[Published proto version 失败日志](https://github.com/chaitin/agent-compose/actions/runs/34437803053/job/102817765843) 暴露了本地构建未覆盖的依赖路径：根模块仍 require `proto v0.1.0`，但新增 `WorkspaceMode`、`WorkspaceSpec.mode/read_only` 及 `SandboxWorkspaceDelivery` 尚未发布；本地 `replace => ./proto` 隐藏了差异。

已创建最小前置草稿 [PR #691](https://github.com/chaitin/agent-compose/pull/691)，提交 `59d2474b`，只包含 schema、重新生成的 Go 文件和完整合同测试，没有主程序、依赖或 CI 改动。构建、28 项测试、全部 18 个 proto 文件的生成一致性、Buf breaking 检查均通过；在隔离目录原样执行「移除 replace → 下载 v0.1.0 → go build ./...」也通过，因为前置 PR 的主程序仍来自 main，尚不消费新增字段。

```text
#691 合并（仅新增协议）
    -> 上游发布 proto/v0.1.1
    -> #686 更新真实 require / go.sum
    -> 原样运行 Published proto 检查
```

`vt128` 只有上游读取权限，不能发布上游 tag；`proto/v0.1.1` 目前不存在。主 PR 因此仍有这个明确的发布前置依赖，不能宣称 CI 全绿。没有填写虚构校验和、改用 fork replace 或跳过兼容性检查。Actions 的 fork 审批与执行结果必须逐个 HEAD 查看，旧提交的通过不能替代最新提交。

2026-09-10 UTC 09:53 核对已获批的 `adecc5b9`：CI [34461231052](https://github.com/chaitin/agent-compose/actions/runs/34461231052) 的其他 10 个 job 全部通过，仅 Published proto 和尚未修复的旧计数断言失败；包括实际 lint、生成一致性、Go tests/driver race 及平台二进制检查。Images [34461230998](https://github.com/chaitin/agent-compose/actions/runs/34461230998) 全部通过，完整 daemon 镜像及默认、Arch Linux 两种 guest 生命周期均通过。该轮是本次生产代码的实际 Actions 证据；后续测试修复的 HEAD 仍应等待自己的审批与结果。

## 9. 后续扩展边界

跨 sandbox 的只读 skills 缓存挂载需要独立的显式交付合同：技能能否写自己的目录、缓存 lease/GC、Pi/Dsh realpath 约束、BoxLite 单共享挂载限制及 K8s 内容物化都需要统一设计。不能把现有可写技能目录悄悄变为只读，也不能把包含凭据和历史的整个 provider home 指向共享缓存。

后续可在本次指纹、所有权和验证基础上增加只读内容共享；Git object cache、项目内安全 symlink 支持、已有 PVC 的显式初始化策略也应按各自所有权和兼容约定推进。PR 的描述应逐项说明已经实现的机制，使用 `Refs #674`，避免以 Docker mount 的零副本数字声称整个 issue 已被彻底关闭。
