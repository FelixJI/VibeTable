# VibeTable 技术债治理与架构稳定化实施方案

> 状态：Implemented；本地产品、升级/恢复、打包 Host 生命周期与候选构建证据已完成，冻结退出仍待
> GitHub `required` 与第二个 Windows 版本的正式候选 smoke
>
> 基线：2026-08-08 最新 `GitHub/main`，已包含开机自启自愈与静默启动改动
>
> 适用窗口：暂停其他产品工作的稳定化周期

## 1. 决策摘要

本轮不重写架构，也不追求“技术债清零”。目标是在恢复功能开发前完成以下六件事：

1. 把当前实现、历史计划、能力接线状态和真实缺陷对齐为一个可信基线；
2. 先固定四语言 wire contract 门禁，防止清债期间发生无意协议漂移；
3. 修正 Python application 对具体 adapter/infrastructure 的反向依赖，保持现有 wire 行为；
4. 对所有声明能力执行“完整接通、明确隐藏、内部专用”三选一，优先完成已确认的 Document Diff；
5. 在不扩大 public interface 的前提下改善 WPF、Web、Go 和 contract 热点的 locality；
6. 用真实数据升级、恢复、打包进程生命周期和 WebView2 证据完成稳定化验收。

按单一实施主线估算为 12–17 个可独立回滚 PR、约 6–10 周；阶段 0 完成后按能力矩阵重新估算。
这是容量范围，不是发布日期承诺，也不作为降低验收标准的理由。

## 2. 当前事实与风险判断

### 2.1 保留的架构不变量

- PocketBase/Go sidecar 继续是唯一业务数据权威；Python BFF 不获得 SQLite 写路径。
- workspace UUID 是身份；路径、显示名和 activity root 不是身份。
- WPF 继续拥有进程生命周期、本机文件能力、WebView2 bridge 和 path grant。
- Web 只使用 closed contract，不获取数据库、对象仓库或本机绝对路径。
- `WorkspaceRepository`、ViewQuery、FieldChange、FileHistory 等现有深模块不因文件较大而被
  机械拆成浅模块。
- v1 contract 保持冻结；v2 继续通过 schema、RPC catalog、fixtures 和 consumer tests 演进。

### 2.2 已确认的技术债与验收空白

下表的 G1/G2 是治理顺序，不是 bug 严重度。只有可复现缺陷才使用 S0–S3；“缺少产品验收”
不得写成“已发现 bug”。

| 风险面 | 当前证据 | 治理顺序 |
|---|---|---|
| 能力闭环 | 多组 workspace/snapshot/fileHistory/conflict/retention/repository/replica RPC 已存在，但静态证据不足以证明 producer→Host→Web→capability→产品 E2E 全闭环 | G1，阶段 0 逐项判定“接通/隐藏/内部专用” |
| Python 依赖方向 | `backend/application/product_data_service.py` 与 `relation_io_adapters.py` 直接导入 PocketBase adapter；plugin application 直接导入 infrastructure package | G1，先固定 contract 再处理 |
| Python product interface | 单个 `PocketBaseProductDataService` 覆盖 Schema、Query、Formula、Relation、Lookup、History、Attachment 等约 41 个 async 方法 | G1，移动 adapter 职责并收缩调用 interface |
| 未接线 Diff | `VibeTable.Workspace` 仅有 Diff implementation；`VibeTable.DocumentDiff.OpenXml` 未进 solution、无生产消费者、无 bridge operation | G1，用户确认本轮接入 |
| Diff kernel | 同步文件 I/O、无算法预算、OpenXml 按原始 XML 物理换行比较、解析错误被无差别降级 | G1，接线前必须收口 |
| 发布验收 | 现有升级 smoke 侧重原子激活，缺少“上一可用候选真实数据→当前候选”；打包宿主进程、WebView2 no-skip 和长期健康回退仍有验收空白 | G1，冻结退出前补齐 |
| WPF/Web 热点 | `MainWindow.Product.cs`、`WorkspaceRequestDispatcher.cs`、`WorkspaceView.vue` 大且高频变化，但多数 interface 仍较小 | G2，只改善内部 locality |
| Contract locality | Web/C#/Python/Go 有大量 wire 类型和 parser；现有 semantic fixtures 已有效，全面 codegen 收益不确定 | G2，选择性收敛 |
| Go 大文件 | `workspacev2`、`schemaapi/catalog.go`、`filehistory/service.go` implementation 大，但 exported interface 小且测试丰富 | G2，保护深模块，不按 LOC 拆分 |
| 仓库与文档 | vendored Node 约 100 MB/1931 files；计划状态、质量门禁说明与本地架构扫描存在漂移 | G2，后置治理 |

### 2.3 明确不做

- 不替换 PocketBase、WPF、WebView2、Python 或 Go 技术栈。
- 不新增第二数据 authority，不让 WPF/Web 读取 sidecar 内部 SQLite 或 object repository。
- 不为了减少行数创建一请求一 class、一方法一 port 或只有生产实现的假 adapter。
- 不全面生成四种语言的所有内部模型；只治理跨进程 wire contract。
- 不在本轮引入永久 feature flag、兼容双写或旧/新实现长期并存。
- 不将每次正常故障都升级为重试、hash、identity 或人工 gate。
- SHA-256 只保留在发布资产、外部下载和历史对象身份等已有真实字节契约；普通本地比较、内部
  handoff 和运行时文件不新增逐文件、多层重复校验。

## 3. 阶段依赖与交付顺序

```mermaid
flowchart LR
    P0["阶段 0：事实基线与能力矩阵"] --> P1["阶段 1：四语言 wire contract 门禁"]
    P1 --> P2["阶段 2：Python seam 与依赖方向"]
    P2 --> P3["阶段 3：Desktop/Web 目标 locality"]
    P3 --> P4A["阶段 4A：Document Diff kernel"]
    P4A --> P4B["阶段 4B：revision content contract"]
    P4B --> P4C["阶段 4C：WPF/Web 产品纵切"]
    P4C --> P5["阶段 5：数据、恢复与进程验收"]
    P5 --> P6["阶段 6：运行时与文档治理"]
    P6 --> P7["阶段 7：冻结退出审计"]
```

设计审查可并行，代码默认按上图串行合并。阶段 3 只整理 Diff 将经过的目标 module，不做全量
搬家；这样 Diff 接线建立在稳定 interface 上，也避免在同一热点并行重构和新增行为。

### 3.1 容量与决策点

| 阶段 | 预计 PR | 单一主线参考工作日 | 进入下一阶段的决策证据 |
|---|---:|---:|---|
| 0 基线/矩阵 | 1–2 | 2–5 | ledger、能力矩阵、首版固定范围和 active plan 经评审，无未处置 S0/S1 |
| 1 contract 门禁 | 1 | 2–3 | 四语言 consumer 对同一 fixtures/negative corpus 一致 |
| 2 Python seam | 2–3 | 4–7 | application 依赖方向通过，wire 行为无漂移 |
| 3 目标 locality | 1–2 | 3–5 | Diff 所经 interface 稳定，行为 characterization 通过 |
| 4 Diff 纵切 | 2 | 6–10 | kernel 与完整产品纵切分别可回滚，代表性 E2E 通过 |
| 5 数据/生命周期 | 2–4 | 7–12 | 当前候选的数据恢复、打包进程和 WebView2 证据成立 |
| 6 runtime/docs | 2 | 3–5 | Node consumer 决策和 active 文档与实现一致 |
| 7 冻结退出 | 0–1 | 2–4 | 最终 HEAD 的本地资格报告与 GitHub `required` 通过 |

参考工日不含 CI 排队和新发现 S0/S1 的修复时间。阶段 0 结束时由架构 owner、各栈 owner 与 QA
reviewer 共同确认范围；阶段 4B 开始前确认 revision content spike；阶段 7 只审证据，不再塞入新重构。

## 4. 阶段 0：事实基线、能力矩阵、缺陷台账与守护测试

### 4.1 目标

建立后续重构的行为基线，清除“文档写已完成、代码未接线”或“代码已完成、计划仍未开始”
的歧义。WP0.1–WP0.4 不改变产品行为；能力矩阵批准后可立即执行一个只撤销入口/capability 的
WP0.5 小 PR。冻结期间只接受：可复现 S0/S1、能力闭环、相关回归、门禁与文档降噪；其他新功能
进入恢复开发后的 backlog。

### 4.2 工作包

#### WP0.1：实现与计划完成度对账

- 逐项核对以下计划的代码、测试、Git 历史和最终验收：
  - `2026-07-28-field-settings-overhaul.md`
  - `2026-07-28-workspaces-snapshots-file-history.md`
  - `2026-08-05-view-query-relation-formula-completion.md`
- 计划顶部只记录 `Implemented`、`Partially implemented`、`Superseded` 或
  `Ready for implementation`，并链接证据；不伪造历史 checkbox。
- 把确实未实现但仍需要的能力纳入本方案或缺陷台账；已被后续设计取代的内容明确标记。

#### WP0.2：建立单一缺陷与技术债台账

新增 `docs/quality/stabilization-ledger.md`，每项至少记录：

- 可复现现象与最小复现步骤；
- 用户影响和严重度；
- 所属 module/interface，而不是只写文件名；
- 现有或建议测试 seam；
- 状态、负责人、目标阶段和处置结论。

bug 严重度只按可复现的真实影响划分：

- S0：数据丢失、跨 workspace 写入、不可恢复损坏，或恢复后形成混合状态；
- S1：首次启动、workspace 打开/切换、表编辑、导入导出、Snapshot/恢复、sidecar/BFF 生命周期
  等关键路径不可用、挂死或显示成功但实际未提交；
- S2：非破坏性结果错误、状态不一致、性能退化或重要能力不可用，但有明确绕行；
- S3：低影响 UX、可访问性、文案、视觉或维护噪音。

当前只读证据不足以宣布已有开放 S0。验收空白和无法复现的观察进入“待证实”，不伪装为已知
bug；但任何已确认 S0/S1 都阻断后续合并，直至修复或隐藏相应入口。

#### WP0.3：建立能力闭环矩阵

为所有可能对用户广告的能力记录五段证据：

`producer → WPF Host/allowlist → Web consumer → capability 条件 → 产品 E2E`

每项只能落入以下状态之一：

- `Closed`：五段齐全，入口可见；
- `Implemented, unverified`：实现存在但缺产品证据，冻结期补验收后才能广告；
- `Hidden`：provider 或关键 method 集不完整，入口与 capability 同步隐藏；
- `Internal only`：明确无用户入口，记录消费者与保留理由；
- `Remove`：无消费者且无确认需求，另开可回滚 PR 删除。

首轮必须覆盖 workspace lifecycle、Snapshot package、FileHistory/Conflict、Retention/Repository/
Replica、plugin lifecycle/preset/version/dashboard 以及 Document Diff。静态搜索未命中 E2E 只能说明
“待验证”，不能直接判定能力未接线。阶段 0 结束前必须做硬决策并冻结范围：

- 首版必需：列出固定用户任务、固定 PR 数和目标阶段；本轮确认的 Document Diff 属于此类；
- 非首版：紧接阶段 0 用小 PR 同步撤销 UI 入口与 capability 广告；
- Internal-only：记录唯一 consumer，并增加 renderer/raw Web 请求拒绝测试；
- Remove：仅限用户未确认保留且无 consumer 的能力，单独可回滚。

只有批准为首版必需的能力进入后续闭环；不允许到阶段 5 再开放式加入任意跨语言 PR。每条纵向
修复 PR 同时处理 capability、Host、Web 和 E2E，不得按技术层拆出长期半接线状态。

#### WP0.4：固定当前候选验收基线

记录当前候选的 source SHA、application version、schema version 和生成命令；数据恢复只验证当前
契约与当前打包产物。开发阶段不构建历史候选，也不维护跨版本 workspace 升级兼容门禁。

#### WP0.5：执行非首版隐藏决策，并修复守护测试和文档漂移

- 对矩阵判为非首版的能力，同一小 PR 同步撤销 UI 入口与 capability 广告；Internal-only operation
  增加 raw renderer 拒绝测试。该 PR 不删除 provider/data，不引入替代 authority。
- 将 `tests/test_architecture.py` 的 retired-provider 扫描限制到明确的受控源码/配置根，避免
  扫描 ignored 本地笔记；不通过继续扩张 ignored 名单修补。
- 修正 `docs/quality-gates.md` 关于普通 PR 是否运行 Go race 的矛盾，以当前
  `.ci/project.json` 和 CI workflow 为准。
- 为现行跨进程 seam 建一页索引：Web bridge、WPF dispatcher、Python JSON-RPC、sidecar v2 RPC，
  标出 authority、session/epoch、错误和取消语义。

### 4.3 验收

```powershell
uv run pytest tests/test_architecture.py tests/contract -q --no-cov
uv run python scripts/automation.py ci --phase plan
git diff --check
```

- 缺陷台账中的 S0/S1 全部具有复现或“当前无已知项”的证据来源；不能只写结论。
- 能力矩阵中不存在“已广告但关键 method/provider/consumer 不完整”的无处置项。
- 首版能力清单和纵向 PR 数已经冻结；非首版入口已隐藏或有紧随其后的明确小 PR。
- 三份历史计划状态与实现事实一致。
- 本地 ignored 文件不再使架构测试误报。

### 4.4 建议 PR

1. `docs(architecture): 建立稳定化基线与技术债台账`：只包含文档、守护测试和必要 fixture。
2. `fix(capability): 隐藏非首版未闭环入口`：仅在矩阵确认有此类入口时创建，同步改 UI、capability
   和 raw renderer 拒绝测试，不混入 provider 重构。

## 5. 阶段 1：固定四语言 wire contract 门禁

### 5.1 目标

在结构迁移和新能力接线前，先把跨进程 wire 行为锁定为一个稳定 seam。权威来源继续是
`contracts/v2/contracts.schema.json`、生成的 RPC catalog 和共享 fixtures；Python/C#/TypeScript/Go
保留各自手写强类型与语义校验，不启动全语言 DTO 生成器重写。

### 5.2 工作包

- 增加一个项目级稳定 contract 检查入口，由 `.ci/project.json`/项目 adapter 调用现有生成检查和
  四语言 consumer tests；workflow 不重复实现命令。
- 盘点 positive fixtures、negative corpus 在四种 consumer 中的覆盖，补真正遗漏的 reader/error
  语义，不复制已经等价的断言。
- 锁定 method 名、params/result shape、稳定 error envelope、session/epoch、取消和 capability 语义。
- contract PR 只补门禁与 fixture，不改变 wire 行为、持久化或 runtime capability。

### 5.3 最小验证

```powershell
uv run python contracts/v2/generate_rpc_catalog.py --check
uv run pytest tests/contract/test_v2_contracts.py tests/backend/contracts/test_workspace_v2_models.py -q --no-cov
Push-Location desktop/web-grid
npm run test -- src/contracts/workspaceV2.test.ts src/contracts/workspaceV2Bridge.test.ts
Pop-Location
dotnet test desktop/tests/VibeTable.Contracts.Tests/VibeTable.Contracts.Tests.csproj --configuration Release
Push-Location sidecar
go test ./internal/contracts/v2 ./internal/protocolv2
Pop-Location
```

新增稳定入口后，以该入口替换上面手工组合；GitHub `required` 仍是 PR 合并门禁。

### 5.4 建议 PR 与回滚

`test(contracts): 统一四语言 wire contract 门禁`

这是纯门禁 PR，可独立 revert。若补 fixture 暴露真实 consumer 分歧，先登记并单独修复，不在门禁
PR 中悄悄选择一个新 wire 语义。

## 6. 阶段 2：修正 Python seam 与依赖方向

### 6.1 目标 interface

PocketBase 是自有远端进程，属于 remote-but-owned dependency。application module 只在确有业务
编排消费者时拥有窄 port；PocketBase HTTP/JSON implementation 位于
`backend/adapters/pocketbase/`；`backend/__main__.py` 作为 composition root 可以同时依赖
application、adapter 和 infrastructure。

不把当前 41 个 pass-through 方法翻译成 5 个新 façade/port。product RPC adapter 对 composition
root 暴露一个小而深的 interface，例如 `invoke(method, params)`；`method` 必须来自闭合集合，不能是
任意 HTTP path。Relation/Lookup、plugin package 等确有 application 编排的消费者，才定义
application-owned result/port，并由 production + test adapter 实现。

### 6.2 工作包

#### WP2.1：依赖方向红灯

- 使用逐项收紧的 architecture ratchet，不能先加入一个会被尚未迁移代码持续触发的全局红灯：
  - PR1 先为 relation/plugin 目标 import 写红灯，完成迁移后只禁止这些已迁走依赖；
  - PR2 完成 product RPC 迁移后，才启用 `backend/application/**` 不得导入
    `backend.adapters`/`backend.infrastructure` 的全局规则。
- `backend/__main__.py` 明确排除在该规则之外，因为它是 composition root。
- 每个 PR 在自身目标上先红后绿，且合并时完整 `required` 必须为绿；不允许主线保留“等待下个 PR
  才恢复”的失败门禁。

#### WP2.2：移动 PocketBase-specific implementation

- 将 product data 的 closed method registry、route、transport error translation、PocketBase response
  translation 移入 `backend/adapters/pocketbase/`，收敛成一个 `PocketBaseProductRpcModule` 深模块。
- Pydantic wire params 移入 `backend/contracts/` 或 adapter-private contract；application 不理解
  HTTP path、header 或 PocketBase response shape。
- 将 `relation_io_adapters.py` 中具体 PocketBase 实现移到 adapter 层；application 只保留 port
  与业务编排。
- plugin package/file interaction 按依赖分类处理：纯规则留在 application module；本机文件/包
  I/O 通过窄 `inspect/pack` port 注入，并用 production + test adapter 证明该 seam 有真实价值。

#### WP2.3：深化 product modules

- 删除 41 方法总 façade，而不是在外面再包一层 façade；method/params 注册表只保留一个事实来源。
- `backend/__main__.py` 通过 `invoke` 注册既有 RPC method，wire method 名、参数、错误和事件保持不变。
- 测试穿过 dispatcher/`invoke` 或真实 consumer port 观察行为；迁移完成后删除只验证旧 façade
  内部调用顺序的测试。

### 6.3 兼容与回滚

- 不改 Web/WPF/Python JSON-RPC method，不改 sidecar route，不做数据迁移。
- 以现有 RPC catalog、contract fixtures 和 product data 行为测试作为字符化基线。
- PR 可整体 revert；不保留长期 compatibility façade。

### 6.4 验收

```powershell
uv run ruff format --check backend tests/backend tests/test_architecture.py
uv run ruff check backend tests/backend tests/test_architecture.py
uv run pyright backend
uv run mypy backend
uv run pytest tests/backend tests/test_architecture.py tests/contract -q --no-cov
```

GitHub PR CI 继续承担完整 Python coverage、四栈质量和 release candidate smoke。

### 6.5 建议 PR

1. `refactor(backend): 归正 relation 与 plugin 的 application ports`
2. `refactor(backend): 深化 PocketBase product RPC adapter`

两个 PR 都必须保持 wire tree 与产品行为不变；第一 PR 不留下无法运行的半迁移状态。

## 7. 阶段 3：稳定 Diff 接线经过的 Desktop/Web interface

### 7.1 目标

只整理 Diff 纵切即将经过的 module，锁定外部 interface 后再新增行为。此阶段不改变 bridge message、
UI 可见性或数据语义，也不把大文件行数本身当作债务完成指标。

### 7.2 工作包

- `WorkspaceRequestDispatcher` 保留单一 `Dispatch` interface；按完整业务流形成少量内部深模块，
  优先整体移动 document flow，不创建一请求一 handler/class。
- `MainWindow` 继续是 WPF composition root；先收拢 runtime bind/unbind 与 workspace session/epoch
  生命周期，使 Document Diff 复用同一 capability/path-grant/session seam。
- `WorkspaceView.vue` 只为 document/history flow 抽取 composable；最终 Diff UI 放在
  `FileWorkspaceView`/`FileRevisionTree`，不把新状态机重新堆回页面根。
- characterization tests 锁定现有 WebMessageRouter、错误映射、workspace switch、epoch rotation 和
  cancellation 行为；移动后删除只断言 private 调用顺序的浅测试。

### 7.3 建议 PR 与停止条件

1. `refactor(desktop): 深化文档请求与会话协调 module`
2. `refactor(web): 收敛文件历史交互状态 module`

两个 PR 均应保持用户行为不变并可独立 revert。若整理需要新增 public port、wire 双轨或数据迁移，
说明 module 边界判断错误，停止并缩小范围。Go `workspacev2` 此时不按 LOC 拆包。

最小验证：

```powershell
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --filter "FullyQualifiedName~WorkspaceRequestDispatcher"
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --filter "FullyQualifiedName~WorkspaceSession"
Push-Location desktop/web-grid
npm run typecheck
npm run test -- src/views/WorkspaceView.test.ts src/views/FileWorkspaceView.test.ts
npm run build
Pop-Location
```

## 8. 阶段 4：接入 Document Diff 能力

### 8.1 产品范围

首个可交付范围是：在文件修订树中选择一个历史 revision，与请求发起时明确记录的 current
effective revision 比较，显示 `unchanged`、`changed` 或带文本计数的 `changedWithDetails`。比较期间
effective leaf 变化必须返回稳定 stale error，不能悄悄改为 live 工作目录文件。本轮不做：

- 任意两个历史 revision 的自由组合；
- DOCX/XLSX/PPTX 完整结构、样式、批注或公式语义 diff；
- PDF 结构化 diff；
- 合并、冲突自动裁决或把 diff 当恢复前置 gate。

### 8.2 WP4A：先硬化 .NET Diff kernel

#### 目标 seam

- 外部 module interface 统一为可取消的 `CompareAsync(request, cancellationToken)`；
- request 接受 host-owned、可重放的 content source/stream 与文件名/MIME，不把任意绝对路径作为
  Diff core interface；production 与内存 test adapter 形成真实 seam；
- result 是 closed union：相同、变化摘要、带文本计数的变化、明确失败；不把英文 `Summary`
  穿过 bridge，显示文案由 Web 本地化；
- adapter registry 和具体格式 adapter 是 implementation 内部 seam，不暴露给 Web。

#### 实施

- 将 TextDiff 的纯序列算法从文件 I/O 中抽出，OpenXml adapter 直接复用序列 interface，不创建
  临时文本文件。
- 用 BCL XML reader 提取可见文本序列；若 package 内容已变但提取文本相同，返回无文本细节的
  `changed`，不能误报 `unchanged`。产品文案只承诺“可见文本变化摘要”。
- 为 LCS/替代算法设置命名的算法预算；超预算降级为 `changed` summary，不让 WPF UI 线程执行
  无界 O(m*n) 工作。预算依据写入注释/测试，不用易抖动的毫秒断言代替。
- 区分 unsupported、invalid/corrupt、I/O failure 和 cancellation；不再无差别 catch 后伪装成
  binary diff。
- Binary adapter 使用已知长度与流式字节比较，不计算或展示新的 SHA-256；若 revision metadata 已有
  权威 `contentHash`，只允许把它作为快速判等提示，最终语义仍由 content source 契约决定。
- 将 `VibeTable.DocumentDiff.OpenXml` 和对应测试纳入 solution、Release build 和覆盖率门禁。

#### 验收

- 文本相同、增加/删除、超预算降级；
- 代表性 DOCX 可见文本变化；
- 非法 OpenXml 得到稳定失败，不伪装成普通 binary changed；
- unknown binary 仍只报告 changed/unchanged；
- cancellation 不留下临时文件或后台任务。

### 8.3 WP4B：新增 revision content contract

文档 revision 内容仍由 sidecar/FileHistory/ObjectRepository 权威持有。先做一个限时聚焦 spike，
确认现有 `filehistory.Service` 能以最小新增 interface 读取指定 revision；随后新增只读 v2 operation
（暂名 `fileHistory.materializeRevision`，以现有命名规范和 spike 结论为准）：

1. Web 只发送 closed `document.diffRequested`：`entryHandle + targetRevisionId +
   expectedEffectiveRevisionId`；不发送或接收本机路径。
2. WPF 通过 capability store 校验 workspace/session/document/revision 归属与 epoch。
3. WPF Host 为 target/effective 两端创建 app-owned、session-scoped 临时目标与既有 path grant，
   只从 Host-only gateway 调用 materialize operation。
4. sidecar 以 revision ID/CAS 校验两个 revision 同属当前 workspace/document，且 current effective
   仍等于 `expectedEffectiveRevisionId`；随后 materialize 两端内容。变化时返回稳定 stale error，
   不读取 live `files/` 冒充 effective，也不暴露 object repository path。
5. WPF 只比较这两个权威 content source；完成、取消、stale 或失败均走同一临时内容清理生命周期。

`fileHistory.materializeRevision` 是 Host-only/Internal-only operation：不进入 renderer allowlist，不
广告用户 capability。用户可见 `document diff` capability 只有在 kernel、Host operation 和 sidecar
provider 全部可用时才广告。

contract 同步更新：

- `contracts/v2/generate_rpc_catalog.py` 与生成 fixture；
- Go dispatcher/FileHistory handler；
- C# Workspace v2 gateway/strict parser；
- TypeScript 只增加 `document.diffRequested` 的 closed request/result parser，不获得 raw materialize
  request 类型；
- negative fixture、session epoch、read-only、path-grant、stale CAS，以及 raw Web materialize 请求
  被拒绝的 contract tests。

不得把大文件 base64 塞入 JSON，也不得恢复旧 C# `RevisionStore`/`RefStore`/`DocumentCatalogStore`。
Go `workspacev2` 只增加窄 Runtime method 和同 package implementation，不拆出一批浅 package。

该 operation 是只读 materialization，不改变 effective revision、文件树或 Snapshot 状态。

### 8.4 WP4C：WPF/Web 产品纵切

- 在 `WorkspaceDocumentOsAdapter` 建立 native DocumentDiff module；只允许具有 `history`/`diff`
  capability 的当前 entry handle 发起。
- 新增 closed host bridge operation，例如 `document.diffRequested`；错误映射沿用 document operation
  稳定错误路径。
- `FileRevisionTree.vue` 增加“与当前版本比较”，结果显示在现有文件历史/保护界面；忙碌、取消、
  workspace switch 和 epoch rotation 会使旧结果失效。
- `WorkspaceView.vue` 只负责装配，异步 generation/epoch 逻辑放入 document diff composable/module。
- 产品 E2E 覆盖一条真实 TXT 或 DOCX 历史比较路径；不机械复制所有格式组合。
- 只有纵切所有 implementation 与 tests 就绪后才广告 diff capability；缺任一端时入口隐藏。

### 8.5 回滚

- WP4A 是未暴露 kernel，可独立 revert。
- WP4B 与 WP4C 作为一个纵向 PR 合并，避免主线长期存在“新增 RPC 但无 consumer”或“入口已显示
  但 provider 不完整”。回滚该 PR 时同步撤销 UI、capability 广告、Host route 和 additive RPC。
- 无持久 schema/data migration，不需要双写或迁移回滚。
- materialized 临时内容不进入 workspace manifest、Snapshot、audit 或发布包。

### 8.6 建议 PR

1. `refactor(desktop): 深化文档 Diff kernel`
2. `feat(document): 接入文件历史版本比较`（同一 PR 含 contract、Go provider、WPF Host、Web UI、
   capability 与产品 E2E；可分内部 commit，但不分批合入主线）

最小验证：

```powershell
uv run python contracts/v2/generate_rpc_catalog.py --check
uv run python qa/go_format_check.py
Push-Location sidecar
go test ./internal/workspacev2 ./internal/app ./internal/filehistory
go vet ./...
Pop-Location
dotnet test desktop/tests/VibeTable.Workspace.Tests/VibeTable.Workspace.Tests.csproj --configuration Release
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release
Push-Location desktop/web-grid
npm run typecheck
npm run test -- src/views/FileWorkspaceView.test.ts
npm run build
Pop-Location
dotnet test desktop/VibeTable.Desktop.sln --configuration Release
```

纵向 PR 还必须由 GitHub `required` 执行 release build/smoke；可见改动附截图。

## 9. 可选工作包：其余热点 locality 与 contract 可导航性

该工作包只在 Diff 纵切完成、ledger 有变更/缺陷证据且不影响冻结主线时启动。若唯一理由是 LOC，
直接转入恢复开发后的 backlog，不阻断冻结退出。

### 9.1 WPF

- `MainWindow.Product.cs` 保持 composition root 小 interface；按 workspace lifecycle、documents、
  plugins、update/host lifecycle 将 private partial implementation 移到同一 partial class 的命名文件。
- `WorkspaceRequestDispatcher` 保留单一 dispatch interface；按 domain 整理 private handlers/注册表，
  不创建一请求一 class。
- 只有存在 production + test 或两个真实行为实现时才提取新 port。
- 先补充会在移动前锁定行为的 characterization tests，再移动 implementation；不在同一 PR 改产品语义。

### 9.2 Web

- `WorkspaceView.vue` 收敛为页面装配和模板；workspace lifecycle、document/history、view query、
  plugin surface 等状态机进入现有或新的 composable/module。
- composable interface 返回状态与命令；Web bridge 调用留在 service/adapter，不回流到 Vue template。
- 以已有 Vitest 行为测试替换对内部调用顺序的脆弱测试。

### 9.3 Go

- 保持 `schemaapi.Catalog`、`filehistory.Service` 等现有 exported interface。
- 仅当一组 private implementation 有清楚职责和现有行为测试时，在同 package 内移动到
  `*_validation.go`、`*_persistence.go`、`*_materialize.go` 等文件。
- 如果审计只能得出“文件很长”，则不实施该移动；大 implementation 本身不是债务。
- 不为测试暴露 internal seam，不新增旁路 repository interface。

### 9.4 Contract

- 在 Diff contract 合并后，再按 domain 拆分过大的 TypeScript parser/类型文件，保留稳定 barrel export。
- 生成器继续生成 RPC catalog/fixtures；不扩展成全语言 DTO 生成器，除非一次独立 spike 证明
  能减少维护面且不会丢失各语言严格解析语义。
- contract 测试观察 wire 输入/输出和稳定错误，不断言 generator/private parser 的调用顺序。

### 9.5 建议 PR

1. `refactor(desktop): 按运行时职责整理产品 composition implementation`
2. `refactor(web): 深化工作区页面状态 modules`
3. `refactor(contracts): 按 domain 整理 Web v2 parsers`
4. `refactor(sidecar): 整理 FileHistory 与 Schema implementation`（证据满足时才创建）

每个 PR 只处理一个技术栈/一个 module cluster，禁止跨四栈“大搬家”。

## 10. 阶段 5：数据、恢复、进程生命周期与产品验收

### 10.1 打包宿主进程生命周期

用真实打包宿主验证：普通退出、关闭到托盘、托盘退出、静默开机启动、BFF/sidecar 任一异常退出、
workspace 再次打开和 session epoch 轮换。验收后不得遗留子进程、端口占用或旧 epoch 响应写入
新会话。组件级 supervisor 测试保留，但不能替代打包产品证据。

若修改 updater，必须验证“替换完成但新程序未达健康状态”的恢复路径；只回滚 updater code，
不触碰 workspace 数据。幂等读取可沿用已有窄恢复等待，写入与 restore apply 禁止自动重试。

### 10.2 WebView2 与能力产品证据

- required/release evidence 中 WebView2 unavailable 必须 fail，不能以 skip 生成
  `releaseEligible=true`；具体实现为 CI/release 设置 `VIBETABLE_REQUIRE_WEBVIEW2=1`，smoke 在该模式
  把 unavailable 转为失败，QA stage/aggregate contract 证明任何 skip 都不能产生发布资格。本地
  非 release 模式才允许明确标记环境不支持的诊断性 skip。
- 能力矩阵中 G1 用户路径至少有一条真实 happy path 和一条最可能的稳定失败路径。
- 优先复用现有 12 条产品 E2E；新增场景只覆盖矩阵缺口，不复制所有格式/状态组合。
- UI 可见但 capability 未广告、capability 已广告但 method 集不完整，均阻断该入口发布；回滚时
  同时撤销入口和 capability 广告。

### 10.3 建议 PR

1. `test(desktop): 验证打包宿主进程生命周期`
2. `test(e2e): 禁止发布门禁跳过 WebView2 smoke`
3. `fix(update): 补齐候选启动健康失败恢复`（仅在可复现验收缺口确认后创建）

阶段 0 已固定首版能力及 PR 数；本阶段只能执行其中已批准的验收/修复，不得按矩阵临时新增任意
用户能力，也不得创建“补完所有 RPC”的大扫除 PR。

## 11. 阶段 6：vendored runtime、计划与仓库治理

### 11.1 Node runtime

- 先用 consumer test 证明 `runtime/node` 的真实用途。当前已知非归档消费者是 plugin 开发 CLI fallback，
  发布包 contract 明确排除 Node/npm/node_modules；不要把“离线产品”误写成“必须提交开发工具链”。
- 默认方案：让项目 bootstrap 恢复锁定 Node 到声明的 `.tools`/缓存位置，plugin CLI 复用该 resolver，
  然后在同一 PR 删除 committed portable Node/npm，并修正 `.gitignore`/`.gitattributes` 的过时说明。
- 若阶段 0 证明“完全离线开发”是硬契约，则保留方案必须改为单一、可更新、带来源/版本/许可证的
  工具制品，而不是继续提交近两千个 npm 文件并将 JS/JSON/文档全标为 binary。
- 外部官方制品导入时验证一次上游完整性；提交后依赖 Git 与现有 package contract，不建立逐文件
  hash 清单，不引入 Git LFS，也不让正式发布包携带 Node。
- 删除方案的回滚只恢复该 PR 中的 runtime tree 与 resolver；不得改变产品数据或发布包语义。

### 11.2 文档与计划

- 为历史计划补准确状态与替代关系，保留历史内容，不重写 checkbox 伪造实施过程。
- 更新 `docs/README.md`，索引当前 ADR、现行计划、历史/已替代计划和质量门禁。
- 修正 contract README 中系统 `python` 命令为仓库约定的 `uv run python`。
- 删除或改正 `VibeTable.Workspace`/OpenXml 注释中与真实依赖和产品承诺不符的描述。

### 11.3 建议 PR

1. `build(runtime): 移除无发布消费者的内置 Node`；若离线开发契约经证据确认，则改为
   `build(runtime): 规范离线 Node 工具制品`
2. `docs(architecture): 对齐现行计划与模块职责`

## 12. 阶段 7：稳定化、缺陷收口与恢复开发

### 12.1 缺陷处理规则

- S0/S1 发现后阻断当前阶段并先修复根因；补充与可复现故障直接相关的回归测试。
- S2 必须在恢复开发前有明确处置：已修、接受并排期、或证明不再适用。
- S3 不要求清零；只处理会影响首个外部版本理解和主要路径的项目。
- 不用删除校验、增加无依据重试、放宽 coverage 或跳过相关 E2E 换取绿灯。

### 12.2 关键用户路径

在最终 release candidate 上验证以下路径，优先复用现有 E2E/故障注入：

1. 首次启动、普通启动、静默启动到托盘与正常退出；
2. 创建、打开、切换、离线和重新定位 workspace；
3. 建表、字段变更、Query/Relation/Lookup/Formula；
4. paste/import/export 与 plugin mutation；
5. 文档导入、打开、历史比较、restore/activate；
6. Snapshot 创建、导出、预览、恢复和打开为新 workspace；
7. sidecar/BFF 异常退出后的受支持恢复路径；
8. 从上一内部候选升级并保持用户数据目录不变。

能力矩阵若把 Conflict、Retention、Repository、Replica 或 plugin 二级路径判为对首版用户可见，
则相应路径必须在此列表增加证据；若明确隐藏或 internal-only，则不为凑矩阵创建无价值 E2E。

不为“连续绿灯数量”设置机械门槛。每个 PR 由 GitHub 完整 CI 验证；最终聚合快照运行一次完整
本地发布资格门禁。若某项此前存在 flake 或竞态，只重复对应 lane/场景直到证据足以说明根因已消除。

### 12.3 最终门禁

```powershell
uv sync --frozen --group dev --group build
uv run python qa/next.py --ci --json-report build/qa/report.json
```

并确认最终 PR 的 GitHub `required` check 通过。最终方案不在本地重复运行与 PR CI 完全相同的
无关矩阵；本地证据聚焦 release candidate、恢复/升级和本机 GUI 路径。

### 12.4 分层门禁

| 改动 | PR 前最小反馈 | 合并阻断证据 |
|---|---|---|
| Python/BFF | Ruff、类型检查、目标 pytest、相关 contract | GitHub `required` |
| Web/UI | 目标 Vitest、typecheck、build | `required`、相关真实 WebView2 场景、可见改动截图 |
| .NET bridge/lifecycle | 目标 .NET tests | `required`、打包宿主生命周期/产品 E2E |
| Go/data/migration | gofmt、目标 Go tests、vet；并发改动跑相关 race | `required`、packaged sidecar、当前 schema 恢复 |
| 发布/恢复/package | 定向 package/restore contract | 完整 `qa/next.py --ci` 与候选 smoke |

最小反馈不是放宽 PR 门禁。性能 p95 只有在同规格样本量达到现行质量规则要求后才升级为阻断，
不因样本不足制造冻结死锁。

## 13. PR、分支与实施纪律

- 每个 PR 从最新 `GitHub/main` 建立独立 `codex/<slug>` branch/worktree。
- 上表 PR 默认串行合并；只有文件所有权互斥且依赖已满足时才并行。
- bug/feature PR 先写会在旧实现失败的回归/行为契约；行为保持型 refactor 先补充在旧实现通过的
  characterization test；只有依赖方向等架构目标使用预期红灯。完成后删除已被新 interface 替代的
  浅层测试。
- PR 正文记录：背景与根因、interface/seam、影响与回滚、精确验证、GitHub checks。
- 只允许 squash merge；普通技术债不得伪装为 `fix`/`feat` 操纵 changelog。
- 不在一个 PR 同时做 wire 变更、模块搬迁、格式化全仓和用户行为改变。

## 14. 风险、缓解与停止条件

| 风险 | 直接后果 | 缓解/停止条件 |
|---|---|---|
| Python 迁移改变 RPC 行为 | Web/WPF 调用失败或错误码漂移 | wire fixture 先锁定；发现 method/shape/error 变化立即停止重构 |
| Diff materialize 越过 authority | WPF 依赖对象仓库内部布局 | 只用 v2 RPC + path grant；不得读取 sidecar repository path |
| Diff 算法无界 | 大文本阻塞 UI/占用 CPU | 可取消 interface + 算法预算 + summary 降级；不靠超时重试 |
| OpenXml 结果夸大语义 | 用户把文本摘要误解为结构化审阅 | 产品和 contract 只承诺可见文本摘要；不宣称样式/公式/批注 diff |
| 热点整理形成浅模块 | interface 数量和测试 setup 增长 | 一个 adapter 不提 port；保持外部 interface，内部同 package/partial 整理 |
| 全语言 codegen 扩大维护面 | 生成器成为新单点复杂度 | 只保留 semantic schema/catalog/fixture；独立 spike 有证据后再扩大 |
| runtime 治理演变成 hash 工程 | CI 变慢、重复校验、维护困难 | 仅外部导入时验证一次；发布资产沿用既有 checksum，不做逐文件清单 |
| 清债范围持续膨胀 | 冻结无法结束 | 新发现按 S0–S3 入 ledger；不属于退出标准的 S2/S3 转后续 backlog |

若任一行为保持型 PR 需要数据迁移、wire 双轨或永久 feature flag，说明工作包边界错误，应停止并
重新设计，不在原 PR 继续扩张。

## 15. 冻结退出标准

恢复普通产品开发前必须满足：

- 已知 S0/S1 为零；所有 S2 有明确处置与 owner；
- 所有广告 capability 都具有 producer→Host→Web consumer→产品验收证据；否则已同步隐藏或标为
  internal-only；
- Python application 不再直接依赖具体 PocketBase adapter/infrastructure；
- Document Diff 已通过 kernel、materialize、WPF bridge、Web UI 和代表性产品 E2E 完整纵切；
- 阶段 3 的 WPF/Web 目标 locality 未扩大 public interface，旧行为测试继续通过；其余可选整理
  不作为 LOC 指标阻断退出；
- contract/schema/catalog/consumer tests 全部通过，无本地 ignored 文件误报；
- 当前候选的 Snapshot 恢复和异常启动恢复通过；
- 打包宿主退出后无 BFF/sidecar 孤儿，required WebView2 证据零 skip；
- Windows 10 与 Windows 11 x64 各至少一次真实客户端候选 smoke；`windows-latest` 不自动等同于两者；
- vendored Node 已按 consumer 证据移除或形成明确保留契约，发布包仍为完整离线产品包；
- 最终 `qa/next.py --ci` 与 GitHub `required` check 通过；
- stabilization ledger、历史计划状态和现行架构文档与代码一致；
- 最终审查确认没有通过降低覆盖、跳过相关 E2E、吞错、重试或重复 hash 层换取通过。

允许留到恢复开发后的项目：没有真实变更/故障证据支撑的 Go 文件移动、全面 DTO codegen、Git
历史瘦身、任意两 revision 自由比较、PDF/Office 结构化 diff，以及全部 S3 清零。

## 16. 独立审查清单

- [x] 每个 port 都有至少 production + test 两个真实 adapter，或明确为何它只是 internal seam。
- [x] 每个阶段有可运行状态、独立 PR、精确门禁和可逆边界。
- [x] Diff 接入不把大文件放进 JSON，不暴露绝对路径或 repository layout。
- [x] 行为保持型重构没有顺手改变 wire、错误码、取消或 session epoch 语义。
- [x] Go/WPF/Web 热点按 locality 整理，而不是按 LOC 创建浅模块。
- [x] contract 治理没有扩展为未经验证的全语言生成工程。
- [x] runtime 完整性只落在外部导入/发布边界，没有逐层重复 SHA-256。
- [x] 最终验收覆盖真实数据、恢复和进程生命周期，但不机械枚举低概率组合。
- [x] 恢复开发的退出标准可由仓库证据验证，不依赖口头判断。

## 17. 独立审查与处置记录

### 17.1 架构顺序审计

- 接受：先固定四语言 wire contract，再纠正 Python 依赖方向和目标 locality，最后硬化/接入 Diff。
- 接受：`PocketBaseProductDataService` 移为一个小 interface、深 implementation 的 adapter module，
  不制造多个 façade；Go `workspacev2` 不按 LOC 拆包。
- 接受：Diff core 使用可取消 stream/content-source seam、结构化 result、算法预算和显式错误；纵向
  接线以一个跨语言 PR 合入。

### 17.2 可靠性与最终红队审计

- 接受：区分已知 bug 与验收空白；能力必须“接通、隐藏、internal-only”三选一。
- 接受：Python architecture rule 采用逐 PR ratchet，保证每个 PR 合并时完整门禁可绿。
- 接受：Diff 以 revision ID/CAS 固定 target/effective 两端；materialize 为 Host-only，raw Web 请求被拒绝。
- 接受：为 WebView2 no-skip 增加明确 CI mode、QA aggregate contract 和独立 PR。
- 接受：阶段 0 固定首版能力和 PR 数，阶段 5 不再开放式扩张范围。
- 接受：bug/feature 使用红灯回归；行为保持型 refactor 使用先通过的 characterization test。

### 17.3 本地方案校验

- 实施基线为 `GitHub/main@bd06158e`，在独立分支/worktree 完成；历史 checkbox 保留原样，
  不反向伪造串行 PR 过程。
- 四语言 contract、Python application seam、Desktop/Web locality、Document Diff 完整纵切、打包
  Host 生命周期、WebView2 no-skip、Node runtime 治理与文档治理均已落地。
- 最终打包产品报告 `build/q/f2/20260809T152446Z/product-e2e-report.json` 为 16/16 passed、
  0 failed、0 skipped；每个场景均正常关闭 Host，残留后代进程为零且端口释放。
- 打包 Host 生命周期报告 `build/qa/packaged-host-lifecycle-20260809-03/report.json` 为 `ok=true`；
  关闭到托盘、托盘退出和静默启动均由真实 `VibeTable.Next.exe` 驱动，exit code 为 0，且无残留后代
  进程或监听端口。
- 公开 `release_build` 已生成 159 文件候选及 ZIP/checksum/build identity/SPDX SBOM；包契约、
  1065 个 Python tests（90.55% coverage）、四个 Node 工程、Go format/vet 与聚焦 Go tests、
  Desktop solution/Web 全量门禁分别通过。
- 本机完整 `automation.py ci --phase full` 与 `release_smoke` 均在同一 Go 全包阶段被 Windows
  `t.TempDir` delete-pending 基线提前阻断：三轮 fresh-process 仅出现随机空目录清理错误，业务断言
  均通过。没有通过扩大重试、sleep、skip 或改生产关闭语义伪造绿灯；GitHub `required` 仍是最终
  合并门禁。

### 17.4 实施后的范围边界

- Workspace create/open/switch、Snapshot timeline/package/open-as-new、FileHistory/Conflict、
  Dashboard 生命周期与 Document Diff 已有最终产品证据；Retention 与 plugin 只按能力矩阵中已实际
  运行的子路径计为 Closed。
- workspace relink 与 plugin 成功升级/回滚/卸载已同步隐藏，mirrored `replica.synchronize` 已收口为
  Host internal-only；Retention cleanup apply 已进入零删除产品场景。Dashboard 双编辑器 revision
  conflict 仍明确标为未验证，Preset/version 保持 Hidden，不借相邻场景扩大结论。
- 普通退出、关闭到托盘、托盘退出和静默启动已有真实打包 Host 证据；场景 10 分别精确终止
  `vibetable-pb.exe` 与 `vibetable-backend.exe`，证明 sidecar 自动恢复，以及 BFF 异常后通过
  workspace 关闭/重开的受支持恢复路径、session epoch 轮换、旧 epoch 写入拒绝与新 epoch 可写。
  组件 supervisor 测试只作相邻支撑。
- “Windows 10 与 Windows 11 各一次”和 GitHub `required` 属远端/设备矩阵证据，本地实现不能代替；
  因此本文件状态为 Implemented，但不能宣称冻结退出条件已经全部满足。
