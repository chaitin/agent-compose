# Microsandbox 根文件系统隔离技术规格

# 第一部分 · 问题与方案

## 1. 结论先行

当前实现把同一份镜像展开成宿主机上的一个目录，然后把**这一个目录同时、可写地**交给所有使用该镜像的 sandbox 当根文件系统。

本次改造改为：镜像转成一份**只读的 qcow2 母盘**，每个 sandbox 得到一个**以母盘为 backing file 的私有子盘**。子盘创建时只写一张元数据表（约 200 KiB），不复制任何镜像数据；guest 写入时才按簇分配新空间。

一句话对比：

| | 当前 | 改造后 |
| --- | --- | --- |
| 每个镜像 | 1 个共享可写目录 | 1 个只读 qcow2 母盘 |
| 每个 sandbox | 指向那个共享目录 | 1 个私有子盘（backing 到母盘） |
| sandbox 写 `/tmp/x` | 所有 sandbox 都看得见 | 只有自己看得见 |
| 根盘传输层 | virtiofs（共享目录） | **virtio-blk 块设备** |

最后一行是附带收益：根盘从 virtiofs 改为块设备后，guest 内核直接驱动本地 ext4，不再为每次文件操作跨越 VM 边界。元数据密集型负载（包安装、`git`、编译）因此有数量级的提速——详见 §6。

根盘隔离落地后的存储布局进一步简化：新建 sandbox 不再为 `/var/lib/docker` 创建第二块 raw/ext4 盘；Docker 数据直接写入 sandbox 独占的 qcow2/ext4 根盘。`SANDBOX_DISK_SIZE_GB` 决定这块根盘的逻辑容量（默认 6 GiB），系统文件与 Docker 数据共享该容量。

## 2. 故障现象

最近一次表现为 guest 初始化 Git 全局配置时失败（运行时观测，该路径由 guest 镜像内的工具产生，不在本仓库代码中）：

```text
error: could not lock config file /tmp/project-agent-gitconfig: File exists
```

这**不是** Docker 登录失败、镜像拉取失败，也**不是**同一个 sandbox 重复跑了两次 Git。

## 3. 根因：三个事实叠加

单看每一条都合理，叠在一起就出事。三条都可在代码中直接核对：

**事实 A — 镜像目录只展开一次，缓存 key 里没有 sandbox。**
`pkg/driver/local_docker_oci.go:88` 把镜像展开到 `<DATA_ROOT>/image-cache/<image-id>/rootfs`，并用 `.rootfs.ready` 标志位实现跨会话复用。缓存身份**只有 image ID**——同一镜像的所有 sandbox 拿到的是同一个路径。

**事实 B — 这个目录被直接当根盘交给 guest，且可写。**
`pkg/driver/microsandbox_runtime.go:1025` 把该绝对目录路径直接传给 `microsandbox.WithImage(...)`，经 virtiofs 挂成 guest 的 `/`。Microsandbox 不像 Docker 那样自动为每个实例叠一层 overlay 写入层——你给它什么目录，写入就直接落到那个宿主目录。

代码注释本身就是这么描述的（`pkg/driver/microsandbox_etc.go:14`）：

> Microsandbox bind rootfs directories are **shared by every sandbox** using the same materialized image.

**事实 C — guest 启动过程会写根盘。**
`git config --global` 要写 `/tmp/project-agent-gitconfig`。

于是 A + B 意味着：**两个 sandbox 里的 `/tmp` 是宿主机上的同一个目录**。加上 C：

```text
sandbox A: git config --global  ──┐
                                  ├──► 都去创建 /tmp/project-agent-gitconfig.lock
sandbox B: git config --global  ──┘         │
                                            └─► 后到的那个：File exists
```

Git 在改配置前先创建同名 `.lock` 文件做互斥（这是 Git 正确的防并发设计）。它假设"同一个文件系统上的并发进程"才需要互斥——而这里两个本该互相隔离的 VM，恰好落在了同一个文件系统上。

**还有一个更隐蔽的后果**：这些锁文件、配置文件、临时数据会**永久留在镜像缓存目录里**。即使 A 和 B 从不同时运行，A 退出后留下的垃圾也会被 B 继承。缓存目录会随时间不断被污染，且没有任何东西会清理它。

根因压缩成一行：

```text
一份共享镜像目录  +  多个 sandbox 都能写  =  文件互相覆盖 + 并发冲突 + 缓存永久污染
```

## 4. 为什么不能只修 `/tmp`

最省事的修法是给 `/tmp` 单独挂个 tmpfs。这确实能消掉这一次 Git 报错，但它修的是**症状的位置**，不是症状的原因。

### 代码里已经有两个这样的补丁

`/tmp` 不是第一个需要特殊处理的路径。当前 driver 的挂载配置里，已经有两处专门为绕开共享根盘而存在的针对性处理：

| 路径 | 现有处理 | 位置 |
| --- | --- | --- |
| `/run` | 挂 per-VM tmpfs 遮蔽掉共享根盘上的同名目录 | `microsandbox_runtime.go:975` |
| `/etc` | 每个 sandbox **递归复制**一份镜像的 `/etc` 到自己的 state 目录再 bind | `microsandbox_runtime.go:995`、`microsandbox_etc.go` |

两处的代码注释都说明了同一件事：写入落在共享目录上会跨 sandbox 泄漏。`/run` 那条明确写了后果——残留的运行时状态"leaks into every later sandbox of the same image"。

也就是说，**这个方案已经被试过两次了**，`/tmp` 会是第三次。

### 为什么第三次不该再打补丁

逐个目录打补丁有两个不可接受的性质：

- **你无法证明补全了。** 已有的两个补丁分布在两个毫无关联的目录，都是撞上之后才加的。哪个路径会是下一个，从设计上无法提前推导，只能等它出问题。
- **补丁自身有持续成本。** `/etc` 的做法是每创建一个 sandbox 就递归复制一整个目录——这既是运行时开销，也是一份需要跟着镜像变化维护的复制逻辑（哪些文件要复制、权限怎么保留）。

共享可写根盘上剩下的风险面：

| 路径 | 谁会写 | 冲突后果 |
| --- | --- | --- |
| `/var` | 包管理器、日志、各种 state | 状态错乱、磁盘被日志撑爆 |
| `/root` | shell 历史、工具缓存、SSH 配置 | 凭据与历史跨 sandbox 泄漏 |
| `/usr/local`、`/opt` | 运行期安装的工具 | 一个 sandbox 装的东西出现在另一个里 |
| `/home` | 非 root 用户的工具缓存与凭据 | 跨 sandbox 泄漏 |

需要隔离的从来不是某几个目录，而是**整个可写根文件系统**。

### 根盘已经提供所需机制

根盘隔离最初借鉴了当时 `/var/lib/docker` 的实现：后者曾经使用每 sandbox 一个独立 raw/ext4 disk。现在每个 sandbox 已拥有自己的 qcow2/ext4 根盘，同一套块设备语义已经覆盖 `/var/lib/docker`，guest Docker 的 overlay storage 不再需要第二块盘。

因此新建流程只装配根盘和现有 bind/tmpfs mounts，不创建、迁移或改写 `docker-disks/*.raw`。升级前已经存在的 raw 盘只作为 legacy 资源读取和回收，详见 §11.5、§12。

## 5. 方案：只读母盘 + 每 sandbox 的写时复制子盘

改造后的数据流：

```text
                     镜像名称
                        │
                        ▼
        Docker daemon 准备镜像（认证/拉取不变）
                        │
                        ▼
          只读 qcow2 母盘 (base.qcow2)      ← 每个镜像一份，发布后不可变
                        ▲
        ┌───────────────┼───────────────┐   backing file 引用
        │               │               │
   sandbox A 子盘   sandbox B 子盘   sandbox C 子盘   ← 每个 sandbox 一份，可写
```

注意箭头方向：子盘**引用**母盘，而不是从母盘复制而来。这个依赖关系是持久的，母盘的生命周期必须长于所有引用它的子盘（§12）。

对照改造前：

```text
Docker daemon 准备镜像 ──► 共享且可写的镜像目录 ──┬──► sandbox A 直接读写
                                                └──► sandbox B 直接读写
```

三个关键性质：

1. **母盘永远不直接挂给 sandbox。** 它只作为 backing file 被读取。发布后置为只读，任何原地修改都是违规——母盘一旦被污染，所有引用它的子盘同时受影响，且因为母盘不可变，污染会永久固化。
2. **每个 sandbox 只挂自己的子盘。** 因此对 `/tmp`、`/etc`、`/root` 或任何路径的写入，都不可能影响母盘或兄弟 sandbox。不再需要针对具体路径打补丁。
3. **镜像数据仍然只存一份。** 隔离没有以"每个 sandbox 复制一遍镜像"为代价——这靠 qcow2 的 backing file 实现，见下节。

镜像的**拉取和认证仍然完全由 Docker daemon 负责**。Microsandbox 不连接任何 registry，也不需要单独配置仓库凭据。这条是刻意的：它保持了现有的认证边界和部署要求不变。

## 6. qcow2 backing file：为什么隔离不等于复制

这是整个方案在成本上成立的原因，值得讲透。

### 机制

qcow2 文件内部把数据切成**簇（cluster）**，并用两级映射表（L1/L2）记录"逻辑地址 → 文件内偏移"。**backing file** 是这套结构里的一条规则：

**某个簇在自己的映射表里没有条目时，就去 backing file 里读同一位置。**

```text
子盘创建瞬间：
  母盘.qcow2  [簇1][簇2][簇3][簇4]     ← 完整镜像数据
       ▲
       │ backing
  子盘A.qcow2 （空映射表）              新增占用 ≈ 200 KiB
                                        读任何簇都穿透到母盘

子盘 A 写了簇 2：
  母盘.qcow2  [簇1][簇2][簇3][簇4]     ← 不变
       ▲
  子盘A.qcow2      [簇2']              新增占用 = 1 簇
                    ↑ 只有被写过的簇才落在子盘里；其余仍穿透
```

所以：**创建是 O(1) 的，空间增长按各 sandbox 实际写入量计费。**

这里有两个容易混淆的"大小"，必须分清：

| | 含义 | 由什么决定 |
| --- | --- | --- |
| **逻辑大小** | 磁盘对 guest 声明的容量 | `SANDBOX_DISK_SIZE_GB`，通常远大于镜像 |
| **实际占用** | 文件在宿主上真正占的字节数 | 母盘＝镜像内容；子盘＝guest 已写入量 |

同一镜像被 N 个 sandbox 使用，总占用是"一份母盘 + 各自写入量"，既不是 N 倍镜像大小，更不是 N 倍逻辑大小。

#### 子盘占用只增不减

准确的说法是**按历史累计写入量**计费，而非按当前数据量。guest 删除文件不会让子盘缩小——被写过的簇已经分配给子盘，删除只是在其内层 ext4 里标记为空闲。

这意味着长期运行、反复写删的 sandbox 会**单调膨胀**，上界是 `SANDBOX_DISK_SIZE_GB`。要让空间可回收，需要打通一条完整的 discard 链路（guest 内层 ext4 挂载启用 discard → 虚拟块设备透传 → qcow2 释放簇），本次**不实现**；对使用寿命较长的 sandbox，应以逻辑大小作为容量规划依据，而不是以当前实际占用推算。

这一点在容量估算中必须计入，也应作为后续独立评估项记录。

### 三条约束

**约束一：backing 路径必须在 daemon 的挂载命名空间内可解析。**

backing file 的路径**被写进子盘文件里**。agent-compose daemon 运行在容器中（`/data/agent-compose:/data`），因此记录的必须是 **daemon 视角的路径**，而不是宿主视角的路径。两者不一致时，子盘会以"backing file 不存在"启动失败。

这也意味着数据目录不能在母盘与子盘之间被重新挂载到不同位置——迁移、备份、跨环境恢复都必须保持相对布局一致。详见 §11.4。

**约束二：母盘是硬依赖，不能删除。**

子盘只存自己写过的簇，其余全部依赖母盘。删除或损坏母盘 = **同时损坏所有引用它的子盘**。这与文件系统级 CoW 不同（那里删除源文件后引用计数会保留数据块），qcow2 没有这种兜底。母盘生命周期管理见 §12。

**约束三：母盘只能由 agent-compose 自己构建。**

不得接受外部提供的 qcow2 作为母盘。qcow2 是一个由运行时解析的复杂格式，来源不可信的镜像文件等于把解析器暴露给不可信输入。母盘必须由 agent-compose 在宿主侧从 Docker 导出的层构建（§10.3）。

### 为什么不是 overlay2 或 reflink

这两个方案都被认真评估过，且都实测可行，最终落选的原因如下。

**overlay2（宿主侧叠加，merged 目录经 virtiofs 给 guest）——因性能与运维成本落选。**

| | overlay2 | 块设备（本方案） |
| --- | --- | --- |
| 元数据密集操作 | **慢约一个数量级** | 基准 |
| 每 sandbox 产出物 | **一个内核挂载**＋散文件 | 一个文件 |
| daemon 重启 | **须重建全部挂载再 resume** | 无操作 |
| 崩溃残留 | 孤儿挂载，umount 可能 busy | 孤儿文件，`rm` 即可 |
| `/var/lib/docker` 独立盘 | **必须永久保留** | 不需要，直接使用私有根盘 |

性能差距的根源是：overlay 方案下根盘仍是 virtiofs，guest 的**每一次文件操作都要跨越 VM 边界**做一轮往返；块设备方案下 guest 内核直接驱动本地 ext4。顺序吞吐差异很小，但创建/打开/stat 大量小文件的场景差一个数量级——而包安装、`git` 操作、编译恰恰全是这类负载。

此外，文件共享设备（virtio-fs）本身也是一层需要 VMM 解析 guest 请求的仿真代码，去掉它同时缩小了攻击面。

**reflink（文件系统级写时复制 + raw 磁盘）——因部署约束落选。**

reflink 需要宿主文件系统支持 `FICLONE`，且母盘与子盘必须位于**同一个**文件系统：

| 文件系统 | reflink |
| --- | --- |
| XFS（`reflink=1`）、Btrfs | ✅ |
| **ext4** | ❌ 上游从未合入，返回 `EOPNOTSUPP` |
| overlayfs / NFS / tmpfs | ❌ |

ext4 是多数发行版的默认根文件系统，因此 reflink 路线会引入一条**硬性部署前置条件**（`DATA_ROOT` 必须建在 XFS/Btrfs 上），并且必须实现能力探测和降级路径——不支持时退化为稀疏复制，每个 sandbox 实占一份完整镜像数据。

qcow2 的写时复制发生在**镜像格式层**而非文件系统层，因此不依赖宿主文件系统类型，上述前置条件、能力探测和降级路径**全部不需要**。这也是本方案相对 reflink 的主要简化。

代价是失去了 reflink 的一项能力：reflink 允许删除母盘（文件系统按引用计数保留数据块），qcow2 不允许。但 §10.4 本就要求母盘不可变且长期保留，这项能力对本方案没有实际价值。

**hardlink 不是候选。** 两个名字指向同一 inode，写一边另一边跟着变，正是要消灭的共享写。**禁止使用。**

---

# 第二部分 · 实现规格

## 7. 目标与非目标

### 目标

- 同一镜像的不同 sandbox 拥有相互独立的可写根文件系统。
- 镜像认证、拉取和 pull policy 由 Docker daemon 负责；daemon 不可用时由 agent-compose 自己的 image cache 承担（§9）。两种情况下 Microsandbox 自身都不连接 registry。
- 共享镜像产物只读复用；创建 sandbox 不复制镜像数据。
- **不引入任何宿主文件系统类型的部署前置条件**——方案在 ext4 上与在 XFS/Btrfs 上行为一致。
- **不要求容器特权或设备透传**：母盘构建不使用 loop mount（§10.3）。
- 删除以下现有代码：
  - `microsandbox_etc.go` 及其调用——`/etc` 的隔离由子盘天然提供（§11.5）。
  - `local_docker_oci.go` 中**仅供 Microsandbox 使用**的目录展开路径：`materializeLocalDockerImageRootfs` 系列函数，及其产出的 `rootfs/` 子目录与 `.rootfs.ready` 标志。其唯一调用方是 `microsandbox_runtime.go:1066`，改造后无人使用；保留会持续生成无人读取的目录白占磁盘。

    > **删除必须精确限定到上述范围。** 同一文件中的 `materializeLocalDockerImageLayout` 系列由 **BoxLite driver** 使用（`boxlite_cgo.go:627`），产出 `oci/` 子目录与 `.ready` 标志；两条路径还共用 `cacheDir`、`.lock` 和 image ENV 缓存文件。BoxLite 不在本次范围内（见非目标），这些部分**一律保留**。
- stop/start 保留子盘上的修改；remove/prune 安全回收子盘及其归属元数据。
- 创建失败、daemon 重启和并发创建，都不会留下可被误用的半成品母盘或无主子盘。
- 母盘在仍被任何子盘引用时不可被删除。
- 保持现有 `/data`、`/run` 的 guest 行为；`/var/lib/docker` 改为直接使用私有根盘。
- `SANDBOX_DISK_SIZE_GB` 继续决定 Microsandbox 根盘逻辑容量，系统文件与 Docker 数据共享该容量。
- 保留升级前独立 Docker raw 盘的读取与安全清理能力，但不迁移或改写其内容。

### 非目标

- 不修改 Docker driver 和 BoxLite driver 的镜像或 rootfs 实现。
- 不引入 Microsandbox registry credential 或 msb SDK 原生 OCI pull（§9 允许 agent-compose 自己的 image cache 作为镜像来源，那是 agent-compose 在拉取，不是 msb）。
- 不新增 protobuf、HTTP、Connect、CLI 或 compose schema。
- **不实现旧目录 rootfs 的兼容、检测或迁移逻辑**；发布时一次性清理现有 Microsandbox runtime sandbox（见 §13）。
- 不把旧 `/var/lib/docker` raw 盘的数据迁移到 qcow2 根盘，也不在启动时批量清扫无法可靠归属的旧文件。
- 不通过继续枚举根盘子目录来实现隔离。

## 8. 术语

| 术语 | 含义 |
| --- | --- |
| 根文件系统（rootfs） | sandbox 中从 `/` 开始的整套系统文件，含 `/tmp`、`/etc`、`/root`、`/var` |
| 母盘（base qcow2） | 由 Docker 镜像生成的 qcow2 磁盘文件，内含 ext4；所有 sandbox 的**只读**模板 |
| 子盘（sandbox root disk） | 以母盘为 backing file 的 qcow2 文件，归某个 sandbox 独占、可写 |
| backing file | qcow2 的写时复制机制：本文件缺失的簇去 backing 里读，见 §6 |
| 簇（cluster） | qcow2 的分配单位，写入时以簇为粒度从 backing 复制 |
| sidecar | 与磁盘放在一起的小 JSON 文件，记录该磁盘归谁所有 |

## 9. 镜像解析与拉取策略（复用现有机制，行为不变）

Microsandbox driver 必须继续通过现有 Docker daemon 解析 guest 镜像：

| pull policy | 行为 |
| --- | --- |
| 空值 / `missing` | 本地镜像存在则复用，不存在则由 Docker daemon 拉取 |
| `always` | 请求 daemon 刷新；保持现有"拉取失败但本地镜像可用时回退本地"的行为 |
| `never` | 只接受 daemon 中已存在的镜像，否则失败 |

解析成功后：用 **Docker image ID** 标识镜像内容，从 daemon inspect 获取解析后的镜像引用和镜像环境变量。传给 Microsandbox 的创建参数使用**本地磁盘文件**，并设置 `PullPolicyNever`——它不得再根据 guest 镜像名访问任何镜像仓库。

Docker daemon 不可达、或镜像不在 daemon 中时，改用 **agent-compose 自己的 image cache** 解析（`pkg/imagecache`，凭据来自 daemon 进程的 keychain 与 `IMAGE_REGISTRY` / `IMAGE_INSECURE_REGISTRIES`）。这与 BoxLite driver 的解析顺序一致（`boxlite_image_resolver.go:32`）：纯二进制部署可能根本没有 Docker daemon，若只有 Microsandbox 硬失败，切换 `RUNTIME_DRIVER` 就会撞上没有道理的行为差异。

**仍然禁止静默回退到 Microsandbox 原生 pull**——让 msb SDK 自行连接 registry 会改变认证边界和部署要求。image cache 路径不属于此列：镜像仍由 agent-compose 拉取，msb 永不接触任何 registry。

回退必须**可见且可追溯**，因为它确实改变了由谁的凭据解析镜像：

- 回退发生时打 WARN，说明当前使用的是 daemon 进程自身的凭据而非 Docker daemon 的。
- 所选来源写入母盘缓存身份与 manifest（§10.1）。日志说明"发生过切换"，缓存身份说明"磁盘上这块母盘由哪一侧产出"，后者在事后排查时更有用。
- **pull policy 失败不触发回退。** `never` 且镜像不存在是所配置来源给出的真实答案，不是换一个凭据主体重问一次的理由；在此回退等于让 pull policy 被静默绕过。Docker 侧 inspect 报错同理。

## 10. 母盘缓存

### 10.1 缓存身份与布局

母盘的缓存身份**至少**包含以下五项，任一项变化都必须产生新的缓存条目：

1. cache format version
2. **镜像来源**（`docker` 或 `oci`）
3. image ID
4. 目标架构
5. `SANDBOX_DISK_SIZE_GB`（见 `pkg/config/config.go:290`）

来源必须进入身份，理由有两条，缺一不可：

- **两条路径的 image ID 会相同。** image cache 的 ImageID 取自 config digest（`imagecache/materialize.go:34`），而 Docker 对 schema2/OCI 镜像的 image ID 同样是 config blob 的 digest。同一镜像两条路径解析会落到同一个 `<image-id>/` 目录。
- **两条路径产出的 rootfs 不保证逐字节相同。** image cache 自行逐层解 `.wh.` 白障（`imagecache/rootfs.go:168`），而 `docker export` 拿到的是 daemon 已合并好的容器文件系统。

两者叠加的后果是：若身份不含来源，一侧会把另一侧产出的母盘判定为 cache hit 直接复用。母盘发布后不可变且被子盘引用（§10.4、§12），这类错误不可恢复。代价是同一镜像若两条路径都解析过会各存一份母盘，需在容量规划中计入。

建议布局：

```text
<DATA_ROOT>/image-cache/<image-id>/microsandbox/
  base-v1-<source>-<image-id>-<arch>-<disk-size-gib>.qcow2
  base-v1-<source>-<image-id>-<arch>-<disk-size-gib>.json
```

清单文件（`.json`）记录：格式版本、来源、镜像 ID、解析后的镜像引用、架构、磁盘大小、创建时间、母盘路径。校验缓存命中时必须比对来源，来源不符即视为不同缓存条目。

`cache format version` 在**任何会改变母盘字节内容的实现变更**时 bump——包括 mkfs 参数、qcow2 转换参数、镜像展开方式的调整。它的作用是保证升级后不会静默复用按旧逻辑构建的母盘。

#### 两个根目录的关系

母盘位于 `DATA_ROOT` 下，子盘位于 `MICROSANDBOX_HOME` 下，是两棵独立的目录树：

```text
DATA_ROOT/image-cache/<image-id>/microsandbox/   ← 母盘
MICROSANDBOX_HOME/rootfs-disks/                  ← 子盘
```

本方案**不要求**两者同盘或同根（这正是相对 reflink 路线的简化，§6）。但 §11.4 的约束仍然成立：子盘中记录的 backing 路径必须持续可解析，因此**两者的相对位置在子盘创建后不可变更**。若部署将它们置于不同挂载点，迁移时必须一同处理。

所有路径必须经过受控根目录、非符号链接和归属校验（可复用 `pkg/driver/microsandbox_owned_path.go:13` 的既有校验模式）。

### 10.2 母盘的输入必须干净

> **这是本方案最容易出错的一条。**

母盘必须从**镜像的原始只读层**，或 **Docker daemon 新导出的文件系统**构建。

**绝对不能**拿"当前已经给 sandbox 写过的那个共享目录"去制作母盘。那个目录里残留着 §3 描述的锁文件、配置和临时数据；一旦它成为母盘，污染会被复制进此后创建的**每一个** sandbox，而且因为母盘不可变，污染会永久固化。

### 10.3 构建状态机

母盘构建分两段：先在临时 raw 上落地文件系统，再转换为 qcow2 发布。

1. 校验已解析的 image ID、磁盘大小和目标路径。
2. 从 Docker daemon 导出镜像文件系统到临时目录（§10.2 约束其来源）。
3. 在**同一缓存目录**创建唯一临时文件（同目录是为了后续 rename 是原子的）。
4. 用 **`mkfs.ext4 -d <导出目录>`** 一步生成指定逻辑大小的 ext4 稀疏镜像，**不挂载**。
5. 校验根文件系统包含预期基础目录，并确认镜像内容未超出磁盘容量。
6. 用 `qemu-img convert` 将临时 raw 转换为 qcow2。转换后的文件**不得带 backing file**——母盘必须是自包含的。
7. 写入临时 manifest，关闭并清理所有文件、stream 和导出目录，删除中间 raw。
8. 先以禁止覆盖的原子 rename 发布 qcow2，再以同样方式发布 manifest。两个文件无法通过一次 rename 组成文件系统事务，因此 **manifest 本身就是提交标志**：只有只读、自包含且格式为 qcow2 的母盘与内容匹配的 manifest 同时存在，缓存才算完整发布；不再使用单独的 `.ready` 文件。

#### 不得使用 loop mount

第 4 步**必须**用 `mkfs.ext4 -d` 把目录内容直接写入镜像，**不得**走"创建空 ext4 → loop mount → 拷贝 → umount"的路线。

理由是部署约束：loop mount 需要 `/dev/loop-control`、`CAP_SYS_ADMIN`，在容器化部署下意味着特权容器或额外设备透传。agent-compose daemon 运行在容器中，引入这条要求会抵消本方案"无部署前置条件"的核心收益（§7），而且失败形态是部署后首次构建母盘时才暴露的 `mount: permission denied`。

`mkfs.ext4 -d` 不需要任何特权，且不涉及内核挂载，同时消除了 §14 中"mount 必须在所有退出路径释放"对本流程的适用性。

#### 外部命令与依赖

`mkfs.ext4`（e2fsprogs）与 `qemu-img` 必须以参数数组调用、不经过 shell，并传播 context cancellation（§14）。两者都是本方案新增的运行时依赖，必须随 agent-compose 容器镜像一并提供；**启动阶段就要检查其存在与 `-d` 支持**，而不是等到首次创建 sandbox 才失败。

#### 失败与并发

- 构建失败必须清理导出目录、中间 raw 和目标临时文件。
- 空间不足的错误必须明确提示调整 `SANDBOX_DISK_SIZE_GB`。构建期峰值占用约为"导出目录 + raw 实占 + qcow2 实占"，需在容量估算中计入。
- 并发请求同一 cache key 时，允许等待同一次构建的结果，但**不得读取未发布文件**，也**不得持有进程内 mutex 执行无界网络 I/O**（导出镜像属于无界 I/O）。
- qcow2 已存在但 manifest 缺失，表示进程在两次 rename 之间退出。该校验与清理只能在同一 cache key 的进程内 singleflight 协调范围内执行：并发请求必须先加入正在进行的构建，缓存 inventory / prune 不得独立删除发布窗口中的文件。确认没有活跃发布者后，删除未提交的 qcow2 并允许后续重建。
- manifest 存在但 qcow2 缺失时，manifest 不包含 guest 数据且母盘可从 Docker 镜像重建，因此删除孤儿 manifest 并允许后续重建。两者存在但身份、路径、格式、只读属性不匹配时，状态来源无法安全判定，必须保守报错，不得自动接管或复用。
- **已发布的母盘不得被重复发布覆盖。** 发布前需确认目标路径不存在；目标已存在时，只有完整校验母盘与 manifest 后才能复用。若允许覆盖，一个已被子盘引用的母盘会被换成新 inode，导致既有子盘的 backing 指向失效。
- 单个 `DATA_ROOT` **假定由单个 agent-compose 实例独占**。多实例共享同一数据根不在支持范围内——进程内的并发收敛机制对跨进程无效。这一点必须写进部署文档。

### 10.4 发布后不可变

母盘发布后视为不可变：发布前必须完成中间 raw 的写入与卸载、完成 qcow2 转换，发布后把文件权限改为只读。

实现**不得**以可写方式打开它、直接传给 guest、或原地修补其内容。母盘内容需要变化时，唯一正确的做法是**生成新的缓存身份和新文件**。

不可变性在本方案中比在文件系统级 CoW 中更关键：母盘是所有子盘的 backing，对它的任何原地修改会**立即改变所有子盘中未被覆盖的簇的内容**，且不产生任何错误提示。

## 11. Sandbox 专属子盘

### 11.1 布局与归属

```text
<MICROSANDBOX_HOME>/rootfs-disks/<sandbox-id-hash>.qcow2
<MICROSANDBOX_HOME>/rootfs-disks/<sandbox-id-hash>.qcow2.owner.json
```

这套布局最初与当时的 `<MICROSANDBOX_HOME>/docker-disks/` 对称。后者现在只用于识别和清理升级前的 legacy raw 盘；新建流程不会创建该目录。rootfs 文件名继续使用稳定的 sandbox ID hash 规则，**禁止直接拼接不受信任的 ID**。

sidecar 可复用 `pkg/driver/microsandbox_disk_ownership.go:14` 的写入模式（临时文件 + `chmod 0600` + fsync + 原子 rename），但字段需要扩展：

| 字段 | legacy Docker sidecar | rootfs sidecar |
| --- | --- | --- |
| format version | 有 | 保留 |
| sandbox ID | 有 | 保留 |
| disk path | 有 | 保留 |
| created at | 有 | 保留 |
| **resource kind** | 无 | **必须新增** |
| **base cache identity** | 无 | **必须新增** |
| **backing file path** | 无 | **必须新增** |

新增三个字段各自防一类事故：

- `resource_kind` 防止 legacy Docker disk 与 rootfs disk 被互相误认并误删。
- `base cache identity` 保证复用既有子盘时，它确实 backing 到当前这个母盘。
- `backing file path` 记录子盘创建时写入的 backing 路径，用于校验它与当前母盘位置一致（§11.4），并让 remove/prune 能在不解析 qcow2 的情况下判断引用关系（§12）。

### 11.2 复用既有子盘的校验

读取已存在的 disk 时必须**同时**满足以下全部条件：

| 校验项 | 不校验会怎样 |
| --- | --- |
| disk 与 sidecar 都位于 `rootfs-disks` 受控目录内 | 可被诱导读写目录外的任意文件 |
| 路径及其父级都不是逃逸 symlink | 同上，symlink 是绕过前缀检查的经典手法 |
| sidecar 中 sandbox ID 与当前 sandbox 一致 | 接管别的 sandbox 的根盘 |
| resource kind 是 Microsandbox rootfs disk | 把 guest Docker disk 当根盘挂载 |
| base identity 和磁盘大小与首次创建记录一致 | 镜像已变但仍复用旧内容，行为静默漂移 |

任一条不满足**必须失败**。不得覆盖或接管未知文件。

#### 磁盘存在但 sidecar 缺失

这个状态由创建流程的固有窗口产生：§11.3 要求先发布磁盘、再发布 sidecar，进程在两步之间退出即落入此状态。因此它**必须有明确的处置规则**，否则会同时不可用（§11.2 拒绝接管）且不可回收（§12 要求 sidecar 完整才可删），使该 sandbox 永久卡死。

规则：**位于 `rootfs-disks` 受控目录内、且没有对应 sidecar 的磁盘，一律判定为未完成创建的残留，必须删除后重新创建。**

这是安全的，因为 sidecar 是**唯一的归属声明**——没有 sidecar 就意味着从未有任何 sandbox 成功宣告拥有该文件，其中不可能有需要保留的数据。反过来，sidecar 存在但磁盘缺失，则是 sidecar 残留，同样删除。

两种半成品状态都必须记录 warning 日志（含路径与判定依据），不得静默处理——它们指示上一次创建异常终止。

### 11.3 子盘创建

#### 并发前提

**整个子盘创建流程必须在 per-sandbox 互斥下执行**，互斥键为 sandbox ID。

这不是可选的加固。现有 driver 只有一把保护 handle map 的全局锁（`microsandbox_runtime.go:33` 的 `lifecycleMu`），**创建路径本身没有按 sandbox 串行化**。若不补上，两次并发创建会各自建临时文件、先后 rename 到**同一目标路径**，后者静默覆盖前者；先启动的 VM 继续写一个已被 unlink 的 inode，后启动的 VM 写新文件——这正是 §14 禁止的"写入分裂"。

即在互斥之下，第 4 步的发布仍**必须**要求目标路径不存在（见下）。互斥防的是同进程并发，路径检查防的是状态不一致。

#### 创建顺序

1. 校验母盘已发布、可读、且缓存身份与请求一致。
2. 在 `rootfs-disks` 目录内以唯一临时名创建 qcow2，指定母盘为 backing file、格式显式声明为 qcow2。
3. 校验新建子盘的 backing 引用可解析且指向预期母盘（防止路径拼接错误在首次启动时才暴露）。
4. 发布磁盘：目标路径**必须不存在**。已存在则不得覆盖，转入 §11.2 的复用校验路径；校验不通过则失败。
5. 发布 ownership sidecar。顺序不能反：sidecar 先出现意味着一个不存在的磁盘被声明为已归属。第 4、5 步之间的崩溃窗口由 §11.2 的"磁盘存在但 sidecar 缺失"规则兜底。
6. 任何 I/O、权限或路径错误**直接失败**，不得静默重试或改用其他机制创建。

#### 三条硬约束

- **必须显式指定 backing 格式**，不得依赖对 backing file 的格式探测。格式探测是已知的安全弱点，且会让一个被替换的 backing 文件改变解释方式。
- **禁止 hardlink**——它会重新引入跨 sandbox 写共享，见 §6。
- **不得复制母盘数据**。子盘创建后的实际占用应仅为 qcow2 元数据量级；若观察到接近母盘实占，说明退化成了完整复制，属于缺陷而非降级。

### 11.4 backing 路径解析

backing file 的路径被**写在子盘文件内部**，因此路径怎么记录是一项正确性约束，不是实现细节。

#### 判据：以打开该文件的进程为准

路径必须在**实际 open backing file 的那个进程**的挂载命名空间内可解析。注意判据是"**谁读**"，不是"谁写"——写子盘的是 agent-compose，读 backing 的是虚拟机监视器。

本方案下两者是同一个进程：**Microsandbox Go SDK 通过 dlopen 加载 FFI 库、在 agent-compose 进程内驱动 VM**，不 fork 独立的 msb 进程。因此 backing 路径应记录为 **agent-compose daemon 视角的路径**。

```text
宿主视角     /data/agent-compose/image-cache/<id>/microsandbox/base-...qcow2
daemon 视角  /data/image-cache/<id>/microsandbox/base-...qcow2   ← 记录这个
```

> **这条结论依赖"VM 在 daemon 进程内运行"这一前提。** 若将来 Microsandbox 改为独立进程或独立容器执行，本节结论必须重新推导——届时应记录的是那个进程视角的路径。实现时应把该前提写进代码注释，使其在变更时可被发现。

记错的症状是子盘启动时报 backing file 不存在，而磁盘文件本身完好——排查时容易误判为磁盘损坏。

#### 复用既有子盘时的校验

除 §11.2 的各项外，还须确认子盘中的 backing 路径与当前母盘位置一致。不一致意味着数据目录被重新挂载或母盘被移动，**必须失败**而不是尝试改写子盘的 backing 指向。

#### 对部署的影响

母盘与子盘的相对布局在子盘创建后**不可变更**。迁移、备份恢复、更换挂载点时，两者必须一同移动且保持 daemon 视角路径不变；否则所有既有子盘失效。这一点必须写进运维手册。

与此相对，本方案**不对宿主文件系统类型提出任何要求**——ext4、XFS、Btrfs 上行为一致，无需能力探测，无降级路径。

### 11.5 传给 Microsandbox 的参数

根文件系统使用 **sandbox 独占的 qcow2 disk**，内层 filesystem hint 为 `ext4`。

具体到 SDK 调用：根盘由 **`WithImageDisk(<子盘路径>, "ext4")`** 装配，取代当前的 `WithImage(<目录>)`（`microsandbox_runtime.go:1025`）。同时**不得**再使用 `WithBindRootfs`，也不配置 OCI upper（`WithOCIUpperSize`）——后者只对 OCI reference 生效，而本方案不向 Microsandbox 传递 OCI reference（§9）。

关于格式推断：该接口按路径扩展名推断磁盘格式，因此子盘文件名**必须**保留 `.qcow2` 后缀。这不构成 §11.3 所禁止的"格式探测"——两者的区别是：

| | 依据 | 是否可接受 |
| --- | --- | --- |
| 子盘自身格式 | 我们自己生成的**文件名** | ✅ 由本实现完全控制 |
| backing 文件格式 | 被引用文件的**内容** | ❌ 必须显式声明（§11.3） |

风险不在"推断"本身，而在推断依据是否受我们控制。文件名是我们写的；被引用文件的内容不是。

`/data` 与根盘隔离**无关**，不在本次改造范围内：它是宿主↔guest 的数据通道（workspace、state、runtime、logs 和声明的 home entries），通过通用 mount manifest 进入，行为完全不变。

`/etc`、`/tmp`、`/root` 及其他根盘目录**天然位于 sandbox 子盘内**，driver 不再需要为这些路径制作共享 rootfs 的补丁或副本——这正是本方案相对"逐目录打补丁"的核心收益（§4）。

#### §4 中两处现有补丁的处理

| 补丁 | 处理 |
| --- | --- |
| `/etc` 递归复制 + bind（`microsandbox_etc.go`、`microsandbox_runtime.go:995`） | **删除** |
| `/run` tmpfs（`microsandbox_runtime.go:975`） | **保留** |

**`/etc` 可以删**，是因为它存在的唯一理由就是根盘共享。子盘私有后 `/etc` 天然隔离，继续保留只是每创建一个 sandbox 多做一次整目录递归复制。

**`/run` 必须留**，理由不是"标准 Linux 语义"这类原则，而是两个具体事实叠加：

1. **msb guest init 自己不挂载 `/run`**（见 `microsandbox_runtime.go:967` 的注释）。没有 tmpfs 遮蔽，`/run` 的写入就落在根盘上。
2. **子盘跨 stop/start 持久化**（§12 明确要求保留 guest 修改）。

所以改造后 `/run` 残留依然会存活，只是**污染对象变了**：

| | 改造前 | 改造后 |
| --- | --- | --- |
| `/run` 写入落到 | 共享镜像目录 | sandbox 私有子盘 |
| 残留会影响 | 同镜像的所有后续 sandbox | **同一 sandbox 的下次启动** |

陈旧的 `/run/docker/containerd/containerd.pid` 让 dockerd 杀掉自己 containerd 并拒绝启动——这个故障在改造后**依然会发生**，只是从"别的 sandbox 留下的"变成"自己上次启动留下的"。因此 `/run` tmpfs 不是顺带保留，是仍然必需。

这两条的区别给出了判断补丁该不该删的通用标准：**问它除了"根盘是共享的"之外还有没有别的存在理由。** 有则保留，没有则随根因一起删除。

#### `/var/lib/docker`：并入 sandbox 根盘

私有根盘本身是 ext4 块设备，guest Docker 可以直接在其中使用 overlay storage。因此新建 sandbox 不再创建 `docker-disks/*.raw`，mount 配置中也不再包含 `/var/lib/docker`；该目录与其他系统目录一样落在 sandbox 独占的 qcow2 根盘中。

这会把容量边界从“根盘一份容量 + Docker 盘一份容量”改为**单一共享容量**：`SANDBOX_DISK_SIZE_GB` 仍决定 Microsandbox 根盘的逻辑大小，默认仍为 6 GiB，但系统文件、软件安装、镜像层和容器层共同消耗它。不自动扩为两倍，也不新增 Microsandbox 专属容量变量。stop/start 继续保留这些数据；remove 删除 qcow2 根盘时一并回收。

升级兼容只覆盖已有资源，不改变新建路径：

- Microsandbox 已持久化的旧 sandbox 配置仍可按原配置挂载旧 raw 盘，Docker 数据保留在其中直至 sandbox 被 remove/prune。
- remove 同时识别最终的 sandbox-ID-hash 文件名和更早的未 hash 文件名，并删除目标 raw 盘及 sidecar。
- managed-resource inventory 继续读取已有 Docker disk ownership；只有路径、resource kind 和归属均完整合法时才允许 prune。
- 兼容代码不得创建、迁移、重命名或改写旧盘；无法可靠归属的文件保守保留，也不在启动时自动清扫。

当约定的兼容窗口结束，并确认受支持部署的 `docker-disks` inventory 已清空后，可以在后续变更中删除 legacy 路径、ownership reader 和清理分支。在此之前它们必须保持只读识别与定向删除语义。

#### 环境变量

现有 image ENV 仍作为 baseline 合并；session/runtime ENV 和 agent-compose 默认值保持更高优先级，优先级顺序不变。镜像 ENV 从 Docker daemon inspect 获取（§9），不从母盘中读取。

#### guest 侧引导不受影响

agent-compose **不向 rootfs 注入任何自有二进制**，因此根盘形态的变化不影响 guest 引导：

- Microsandbox 自带的 guest agent 由其 SDK 提供并注入，不经过本方案管理的磁盘。
- agent-compose 自己的 guest 准备工作通过**启动后在 guest 内执行 bootstrap 命令**完成，从 `/data` 建立符号链接（见 `directory_only_guest_bootstrap.go`）。`/data` 是独立 bind mount，行为不变。

这意味着母盘内容**纯粹是镜像内容**，其缓存身份（§10.1）不需要包含 agent-compose 自身的版本。

## 12. 生命周期

### 创建

- 顺序：先完成 Docker pull-policy 解析 → 准备母盘 → 再创建子盘。
- Microsandbox 创建成功后，子盘与 runtime handle 共同归当前 sandbox 所有。
- **本次新建的 rootfs disk** + Microsandbox 创建失败 → 删除 disk 与 sidecar。
- **已验证的既有 rootfs disk** + 创建失败 → **保留**，以免破坏 stop/start 的恢复状态。
- 新建路径不探测、创建或挂载 legacy Docker raw 盘；`/var/lib/docker` 直接使用 rootfs disk。

这两条分支的区别是刻意的：新建失败留下的是垃圾，既有资源失败留下的是用户数据。

### 停止、启动与 daemon 重启

- stop 只停止 VM，**不删除**子盘。
- start/resume 使用 Microsandbox 持久化配置中的**同一个磁盘路径**，不重新创建子盘。
- daemon 重启后通过 sidecar 重新验证 ownership；无需恢复宿主 mount。
- guest 在子盘中的合法修改，跨 stop/start 保持。
- 升级前持久化配置中已有的独立 `/var/lib/docker` mount 由 Microsandbox 原样恢复；agent-compose 不迁移或重写它。

### 删除与清理

- remove 顺序：先停止并移除 Microsandbox runtime state → 再删除 legacy Docker raw 盘（如有）以及 rootfs disk 和 sidecar。
- managed-resource inventory 必须继续识别 `rootfs-disks`，并把 `docker-disks` 明确视为 legacy inventory。
- orphan rootfs disk **只有在**sidecar 完整、路径校验通过、且不存在活动 runtime ownership 时才可删除。
- 磁盘与 sidecar 只存其一时按 §11.2 的半成品规则删除——该状态没有归属声明，不适用上一条的保守规则。
- legacy Docker raw 盘没有完整 ownership、路径逃逸或经过 symlink 逃逸时不得 prune；没有 sidecar 的未知 raw 文件继续保守保留。

#### 母盘不可在被引用时删除

> **这是相对文件系统级写时复制最重要的语义差异。**

子盘只存自己写过的簇，其余数据全部依赖母盘。删除母盘 = **同时损坏所有引用它的子盘**，且损坏在下次启动前不可见。因此：

- 母盘缓存的清理（prune / GC）**必须**先确认没有任何子盘的 sidecar 记录指向它。
- 引用关系从 sidecar 的 `backing file path` 与 `base cache identity` 判定，**不需要**逐个解析 qcow2 文件。
- sidecar 位于 driver 自有目录，其中的 backing 路径对缓存层而言是**外部输入**。缓存层只在该路径落在 image cache materialization root 之下、且形如 `<root>/<image id>/microsandbox/<base cache identity>.qcow2` 时才承认这条引用。校验必须是**纯词法**的：inventory 用 ReadDir 拼出的路径作为引用键，在此处解析符号链接会让 `DATA_ROOT` 自身含符号链接时引用悄悄对不上。
- 无法读取或无法通过上述校验的 sidecar **不得**让整个 cache inventory 失败——一个损坏文件不该拖垮其他 domain 的 `cache ls`。这类 sidecar 记录为 warning，并被视为**归属未知**：它保护的是哪块母盘已不可知，因此在它被修复或删除前，所有母盘一律呈现为 `unknown` 且删除请求必须失败。
- 上一条的取舍是刻意的：删除仍被子盘依赖的母盘不可恢复，而拒绝删除一块暂时无法核算归属的母盘只是延迟回收。
- 存在引用时删除请求**必须失败并说明原因**，不得以"稍后重试"或 warning 略过。
- 反向也要成立：子盘被删除后，其对母盘的引用随之消失；母盘只有在引用计数归零后才可回收。

镜像更新不构成删除理由——新镜像会产生新的缓存身份和新母盘（§10.4），旧母盘应保留至其全部子盘消失为止。

## 13. 发布切换（一次性，无迁移代码）

本次改造采用一次性切换：**运行时代码中不保留任何旧目录 rootfs 的识别、拒绝、恢复或迁移分支。**

发布步骤：

1. 停止或排空正在运行的 Microsandbox workload。
2. 删除现有 Microsandbox runtime sandbox，使旧的 persisted config 不会被新版本恢复。
3. 删除曾经作为可写 bind rootfs 使用的 materialized rootfs cache——它可能已被 guest 污染，**不能**作为母盘的可信输入（§10.2）。

   删除范围**严格限定**为各镜像目录下的 `rootfs/` 子目录及其 `.rootfs.ready` 标志。

   **不要删除整个 `image-cache/<image-id>/`**：该目录下同时存放着新方案的母盘（§10.1）、BoxLite driver 在用的 `oci/` 子目录与 `.ready` 标志，以及两者共用的 `.lock` 和 image ENV 缓存。误删会破坏 BoxLite 的镜像缓存，而 BoxLite 不在本次改造范围内。
4. 删除各 sandbox state 目录下 `/etc` 补丁遗留的副本（`microsandbox-rootfs/etc`，见 `microsandbox_etc.go`）。该补丁已随本次改造删除（§11.5），副本不再被读取。
5. **保留** `/data` 中独立持久化的 workspace 和 agent state；不要把它们纳入 runtime sandbox 清理范围。注意上一步的 `/etc` 副本虽然也位于 state 目录下，但属于要清理的范围。
6. 确认部署环境中存在 `qemu-img` 与支持 `-d` 的 `mkfs.ext4`（§10.3 新增依赖，应随容器镜像提供）。**无需**对宿主文件系统类型做要求，**也不需要**容器特权或 loop device。
7. 部署新版本，由 Docker daemon 中的镜像重新构建不可变母盘，并为每个新 sandbox 创建独立子盘。

发布后核查：

- 新建 sandbox 使用 disk-image rootfs，不存在旧的 directory-rootfs runtime state。
- 子盘初始实际占用为元数据量级，未发生完整复制。
- 子盘记录的 backing 路径在 daemon 视角可解析（§11.4）。

若清理未执行，残留 runtime 的行为**不在兼容范围内**。部署脚本或操作手册必须把清理列为升级前置条件——为一次性的历史状态引入长期代码复杂度不划算。

上述一次性切换仅描述旧 directory-rootfs 到 qcow2 rootfs 的边界。后续移除独立 `/var/lib/docker` raw 盘时采用不同的兼容策略：不要求删除已存在的 sandbox，不迁移数据；已有 Microsandbox 持久化配置继续引用原 raw 盘，remove/prune 再按 ownership 安全回收。新建 sandbox 从切换版本起只使用 qcow2 根盘。未被可靠归属的旧文件不做启动时清扫。

## 14. 横切约束

### 安全

| 约束 | 违反后果 |
| --- | --- |
| 所有 cache / disk / sidecar 路径使用绝对受控根目录，拒绝 symlink escape | 目录外任意文件被读写 |
| 所有外部命令使用参数数组，不经过 shell，并传播 context cancellation | 命令注入；取消后进程泄漏 |
| 错误信息不得包含 registry credential、Docker authorization 或完整环境变量 | 凭据写进日志 |

### 可靠性

| 约束 | 违反后果 |
| --- | --- |
| mount、loop device、文件、Docker response body、临时目录在**所有**退出路径释放 | fd / loop device 耗尽，mount 泄漏 |
| 母盘与子盘的每个文件必须原子发布；ownership/manifest 提交标志不得先于对应磁盘出现，且读取方必须校验完整文件对 | 其他进程读到半成品并当作可用 |
| 同一 sandbox 的并发 create 必须收敛到同一 ownership | 产生两个可用根盘，写入分裂 |
| **母盘在被任何子盘引用时不得被删除**（§12） | 所有引用它的子盘同时损坏，且下次启动前不可见 |
| backing 格式必须显式声明，不得依赖运行时探测 | backing 文件被替换后解释方式改变 |
| 文件系统损坏、ownership 冲突、清理失败**不得**降级为普通 warning | 数据损坏被静默吞掉 |

### 可观察性

结构化日志至少包含：

- image reference、resolved reference、image ID、母盘 cache identity
- 母盘 cache hit/miss、build duration、转换后实际占用
- sandbox ID、子盘 path、backing path、创建耗时、创建后实际占用
- 子盘复用、失败创建的清理结果、remove 与 prune 结果
- **母盘删除被引用计数阻止的事件**（§12），含阻止它的子盘数量

日志只记录路径和非敏感身份，**不记录 registry auth**。缓存与 managed-resource 查询必须区分不可变母盘、sandbox rootfs 子盘和 legacy guest Docker disk；最后一类不得再被报告成新建流程的常规产物。

---

# 第三部分 · 验收

## 15. 测试

### 15.1 单元测试

**缓存身份与母盘构建**
- cache identity 对 image source、image ID、architecture、format version、disk size 五项都敏感；仅来源不同时必须得到不同身份（§10.1）。
- 母盘构建的输入：docker 来源用**本次新导出的私有目录**，构建后释放；oci 来源用 image cache 的共享 rootfs，**释放动作必须为空操作**——那个目录 BoxLite 也在读，删除它会留下悬空的 ready 标志。
- 断言 docker 来源的构建流程不引用任何既有的共享 rootfs 缓存路径（§10.2）。
  > 这条测的是"代码从不读那个路径"，**不是**"代码识别并拒绝那个路径"——后者属于 §13 禁止的旧 rootfs 识别分支。
- 镜像来源选择：daemon 不可达或镜像不在 daemon 中时回退到 image cache 并打 WARN；pull policy 失败与 Docker inspect 失败**不回退**（§9）。
- 母盘只在完整发布后可见；失败和 cancellation 会清理临时资源。
- manifest 作为提交标志：singleflight 协调内确认无活跃发布者后，disk-only 或 manifest-only 半成品被删除并允许后续重建；内容不匹配的状态保守失败。
- 已存在的母盘不被重复发布覆盖（§10.3）。
- 构建流程不调用 mount 系统调用（可通过断言使用 `mkfs.ext4 -d` 参数形态实现）。

**子盘创建**
- 子盘正确 backing 到母盘，且 backing 格式被显式声明。
- 子盘创建后实际占用为元数据量级，**未发生完整复制**。
- backing 路径记录为 daemon 视角路径（§11.4）。
- I/O、权限、路径错误正确向上传播，不被静默处理（§11.3 第 5 条）。

**归属校验**
- 子盘 path 校验、symlink escape、ownership mismatch、unknown resource kind 各自被拒绝。
- backing 路径与当前母盘位置不一致时**拒绝复用**，且不尝试改写子盘的 backing 指向。
- 新建 disk 的失败回收 vs 既有 disk 的失败保留，两条分支都覆盖。
- **半成品状态**：磁盘无 sidecar、sidecar 无磁盘，两种情况都被删除并记录 warning（§11.2）。
- 发布时目标路径已存在则不覆盖，转入复用校验（§11.3 第 4 步）。

**并发**
- 同一 sandbox 的并发创建在 per-sandbox 互斥下收敛到单一磁盘与单一 ownership，不产生第二个可用根盘（§11.3、§14）。

**母盘生命周期**
- 存在引用时删除母盘的请求被拒绝，并给出可定位的原因。
- 引用计数归零后母盘可回收。
- 引用判定只依赖 sidecar，不解析 qcow2 文件。
- backing 路径指向 materialization root 之外、路径形状不符、或与 `base cache identity` 不一致的 sidecar，不产生引用，也不能借此把无关缓存项钉成 referenced。
- 无法读取或无法通过校验的 sidecar 只产生 warning，不使 inventory 失败；同时该状态下母盘为 `unknown` 且删除被拒绝。

**接口契约**
- pull policy 在所选来源上执行：docker 来源交给 Docker daemon，image cache 来源由 `pkg/imagecache` 按同一套语义实现，避免回退改变 policy 的含义。
- Microsandbox options 使用 qcow2 disk image + `ext4` 内层提示，而非 bind rootfs 或 OCI reference。
- 新建 options 的 mounts 不包含 `/var/lib/docker`，创建路径不产生 `docker-disks` 目录或 raw 盘。
- image ENV 合并优先级保持不变。
- remove 只删除目标 sandbox 对应的 hashed/未 hash legacy raw 盘及 sidecar；其他 sandbox 的文件不受影响。
- managed-resource inventory/prune 仍识别 ownership 合法的 legacy Docker disk，并拒绝非法路径、symlink escape 与不完整 ownership。

### 15.2 Linux 集成测试

- 从小型 OCI fixture 构建母盘，验证其只读基线内容，且母盘自身不带 backing file。
- **隔离性核心用例**：从同一母盘创建两个子盘，在**相同路径**写入**不同内容**，验证母盘和兄弟子盘均不变。
- 验证子盘初始占用与写入后占用的增量，与写入量同量级。
- 在 **ext4** 上执行以上全部用例，确认不依赖任何特殊文件系统能力。
- 并发请求同一 cache key 时不暴露半成品。

### 15.3 Runtime Smoke

**隔离性用例**（覆盖 §4 中已被打过补丁的路径，以及尚未被覆盖的路径）：

| 用例 | 通过标准 |
| --- | --- |
| 两个并发 sandbox 使用同一 image，在根盘**相同路径**创建互斥锁和不同配置 | 均成功且互不可见 |
| 两个并发 sandbox 各自写入不同的 `/etc/hosts` 与 resolver 配置 | 内容互不可见，且**在 `/etc` 补丁已删除的前提下**成立 |
| 一个 sandbox 把 `/root` 替换成符号链接后销毁，再用同一镜像创建新 sandbox | 新 sandbox 的 `/root` 是镜像原始状态 |
| 一个 sandbox 在 `/var` 下写入大量状态后销毁，再用同一镜像创建新 sandbox | 新 sandbox 看不到任何残留 |

后两条验证的是同一个关键性质：**污染不再具有持久性**。当前实现下，sandbox 对根盘的修改会留在跨会话复用的镜像缓存里，只能靠人工清理；新实现下它随子盘一同销毁，母盘从未被触碰。

**常规 smoke**：
- stop/resume 后各自子盘内容保持。
- `/var/lib/docker` 与 `/` 位于同一根文件系统，dockerd 正常使用 overlay storage，且 Docker 状态跨 stop/start 保持。
- remove 后对应 rootfs disk、sidecar 和 runtime state 全部被清理；新建至删除的整个过程中从未产生独立 raw 盘。
- Docker daemon 已有镜像且 `pull_policy=never` 时，可在**不提供 Microsandbox registry auth** 的情况下创建 sandbox。
- 所有 sandbox 操作结束后，母盘的内容与实际占用均保持不变。

### 15.4 性能与空间

真实 runtime smoke **记录**以下指标，但不设置脆弱的固定时延门槛：

- 冷启动的母盘 build duration（含 raw → qcow2 转换）
- 热缓存下的子盘创建耗时
- sandbox boot duration
- 母盘、初始子盘、guest 写入后各自的 allocated bytes

**硬性验收**（三条，全部无条件适用——本方案不依赖宿主文件系统能力，因此没有需要跳过的分支）：

| 验收项 | 标准 |
| --- | --- |
| 热路径不得展开完整镜像 | 命中母盘缓存时不得重新解包 layers |
| 初始子盘实际占用极小 | 仅为 qcow2 元数据量级，**不得接近母盘实际占用** |
| 子盘增长与写入量同量级 | 写入 N 字节后增量不应显著超过 N |

第二条直接对应 §6 的成本论证——不成立即说明退化成了"每个 sandbox 复制一份镜像"。

另建议记录一项对比基线（不设门槛，仅供决策留痕）：**元数据密集操作的耗时**（例如在 guest 内创建数千个小文件）。这是根盘从 virtiofs 改为块设备的主要收益所在（§1、§6），值得在改造前后各测一次以确认收益兑现。

## 16. 实施边界与质量门禁

### 代码组织

母盘构建、子盘 ownership/clone、Microsandbox option 组装应拆分为**各自单一职责**的单元，避免继续扩大已经混合了 runtime 生命周期的大文件（`microsandbox_runtime.go` 已接近这个边界）。新接口默认 package-private，只暴露真实跨文件或跨 package 消费所需的最小能力。

### 门禁

开发期间运行受影响 package 的 focused tests；完成后运行：

```bash
task lint
task build
task test
```

在准备完成的 Linux/KVM 主机上额外运行：

```bash
SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke
```

**无法在当前环境执行的门禁，必须在交付说明中逐项列出原因，不得暗示已经通过。**
