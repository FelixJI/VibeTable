# VibeTable 功能、架构、测试与稳定性成熟度审计（历史基线）

> 历史资料归档（2026-10-13）：本文保留 2026-08-17 审计原稿，正文中的“当前”“本次”、测试数、版本、故障和建议均指当时基线。本轮仅归档，未重跑原审计或重新核验其中的 CI 结论；原稿中的根因判断不作为当前诊断。后续处置见[成熟度收敛计划](../../plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md)，现行验收见[能力闭环矩阵](../capability-matrix.md)与[产品 E2E 证据](../../e2e-performance.md)。

审计日期：2026-08-17\
审计基线：`GitHub/main@0149afe7845491c1a25fb5664212361d3469e556`\
产品版本：`0.5.1`\
审计性质：代码、可执行配置、测试、当前 CI 与本地聚焦验证的综合评估

## 1. 执行摘要

VibeTable 已不是原型，也不是“只有界面和局部代码”的演示项目。它具备可工作的离线多维表格、文件历史、快照恢复、插件受控写入、内容型记录、VibeTable 界面和统一搜索闭环；PocketBase 单一数据权威、workspace UUID、session epoch、path grant、稳定错误 envelope 和恢复协调等设计具有较高工程成熟度。

综合判断：**工程化 Beta（M3），核心数据与恢复内核接近发布候选（M4），但当前构建不具备 release-qualified 状态。**

主要依据：

- 架构方向正确，真实 seam 清楚；SchemaCore、WorkspaceSearch、Surface 等新 module 体现了“小 interface 隐藏复杂 implementation”的深模块设计。
- 测试资产大、门禁真实：当前 Web 1,163/1,163 测试通过，line 87.58%、branch 76.54%；核心 Go line 90.09%、decision-arm 82.67%；当前 CI 的 .NET 六项目共 646 tests 全通过。
- 产品 E2E 已从历史 16 个扩展为 18 个，新增 VibeTable 界面和统一搜索真实发布包场景。
- 当前 `main` CI 仍失败：完整真实 WPF/WebView2 产品 E2E 为 17/18，结构化 modal 按 Escape 后焦点偶发停留在 `BODY`。同一 Git tree 在 PR 全绿、main 红灯，确认是非确定性稳定性缺陷。
- 文档证据明显滞后：能力矩阵仍写 16 场景，性能基线仍是 12 场景，无法代表当前大规模 workbench 改造。
- 四栈架构成本仍高：两个 RPC catalog 合计 159 个 method；生成 DTO 已降低漂移，但 method registry、allowlist、bridge 和错误映射的 interface 面积仍大。

因此，不建议把当前状态描述为“稳定正式版”或“所有功能都已完整覆盖”。更准确的说法是：**数据一致性、恢复和契约工程强；产品交互、性能证据、跨版本兼容和发布稳定性仍需一轮收敛。**

## 2. 审计方法与证据等级

### 2.1 判定口径

功能按以下四层判断：

1. implementation 存在；
2. Web/WPF/Python/Go 跨栈接线完成；
3. 自动化测试验证 interface 行为；
4. 真实发布包中形成用户可见闭环。

只有第四层才称为“产品闭环”。单元测试、组件存在或 capability producer 存在，不自动等于用户可用。

### 2.2 证据优先级

1. 当前提交的 GitHub `required`、真实发布包 E2E 与故障注入；
2. 本次动态测试和覆盖率；
3. 当前可执行配置、代码和契约；
4. 文档与历史报告。

文档与实现冲突时，以实现和当前 CI 为准。

### 2.3 当前关键外部证据

- [当前 main 失败 CI run 32034481212](https://github.com/FelixJI/VibeTable/actions/runs/32034481212)
- [PR #60](https://github.com/FelixJI/VibeTable/pull/60) 的同树代码曾完整通过 required
- PR head 与 squash main 的 Git tree 都是 `73552e4608f932958ffb7bfc458915b5c7ec39c0`

## 3. 综合成熟度评分

分数用于排序，不表示数学精确度。成熟度分级：M1 原型、M2 可用、M3 工程化 Beta、M4 发布候选、M5 稳定生产。

| 维度 | 评分 | 阶段 | 结论 |
|---|---:|---|---|
| 功能有效性 | 8.0/10 | M3–M4 | 核心主链真实闭环；产品高级交互与隐藏能力仍不完整 |
| 架构合理性 | 8.3/10 | M4 | authority、seam、恢复与生成契约设计科学；interface 总面积偏大 |
| 程序可维护性 | 7.2/10 | M3 | 大协调器已拆分，但多个 1,000–2,600 行热点和跨栈扩散仍存在 |
| 自动化测试 | 8.4/10 | M4 | 多层门禁、覆盖 ratchet、race、故障注入和真实 E2E 完整度高 |
| 边缘覆盖 | 7.8/10 | M3–M4 | 一致性/恢复/取消很强；UI 时序、Unicode、真实环境组合偏弱 |
| 稳定性 | 6.4/10 | M2–M3 | 故障恢复强，但当前 main 红灯且同树一绿一红 |
| 交付工程 | 8.0/10 | M4 | 候选绑定、分片门禁、race、release/CD 设计成熟；当前候选未合格 |
| 文档与证据治理 | 5.8/10 | M2 | 16/18、12/18 场景基线漂移，降低决策可信度 |
| 综合 | **7.4/10** | **M3** | 工程化 Beta；解决 P0 后可重新进入 M4 资格评估 |

## 4. 功能有效性与成熟度

| 功能域 | 当前成熟度 | 已验证能力 | 主要缺口 |
|---|---|---|---|
| Workspace 生命周期 | M4 | 离线首启、创建/打开/切换、旧 epoch 拒绝、快照包 open-as-new | mirrored/replica、relink 缺真实产品闭环 |
| Schema、字段、记录 | M4 | Schema V2、全字段家族、计划/应用、迁移回滚、冲突 | 长期跨版本 migration 契约尚未冻结 |
| 查询、筛选、公式、Relation/Lookup | M4 | revision-bound 查询、依赖/新鲜度、fanout、错误/预算测试 | 大规模多跳交互性能与替代视图完整 E2E |
| 粘贴、导入、导出 | M3–M4 | JSON 深往返、1,000 行原子失败回滚 | CSV/XLSX 多字段、多 locale、多错误组合的 packaged E2E |
| 文件、附件、历史、diff | M4 | 文件元数据查询、附件历史、restore、文档 diff、CAS stale | DocumentList/Inspector Web 覆盖偏低；超大文档集合资格不足 |
| 统一搜索与内容型记录 | M4（新） | record/file/attachment、正文/元数据/历史、rebuild、关联修复 | 自研 PDF 提取器长期格式覆盖；超大索引性能基线 |
| Snapshot、恢复、审计、保护 | M4 | 故障注入、快照一致性、ledger 保留、retention 零删除路径 | 真实 retention 非零删除和长期恢复演练 |
| Realtime 与进程恢复 | M4 | SSE 去重/追赶、sidecar kill/reconcile、BFF session 轮换 | 复杂 UI 恢复后的焦点/选择状态仍有时序缺陷 |
| Dashboard | M3 | 创建、保存、刷新、重开；组件覆盖已大幅提高 | 图表、筛选、钻取、导出、双编辑器冲突缺 packaged E2E |
| VibeTable 界面 | M3–M4（新） | 构建、运行、重开、插件动作确认/拒绝/取消 | 复杂 binding、表单失败和多页导航性能仍需更多真实场景 |
| 插件 | M3 | install/enable/action、受控 mutation、越权拒绝 | 成功升级/回滚/卸载生命周期仍缺用户闭环 |
| 看板/日历/时间线/画廊 | M2–M3 | 组件和相邻测试存在 | 创建、配置、拖拽、保存、重开的真实产品 E2E 缺失 |
| 自更新 | M3 | 资产身份、ZIP 安全、复制失败回滚、代理边界测试 | 新版本启动健康确认/自动回退、真实 packaged 更新 E2E |
| 诊断与日志 | M3–M4 | JSON schema、轮转、诊断 bundle、失败证据保留 | 跨进程 correlation/脱敏和 bundle 契约需当前产品场景复核 |

### 4.1 功能上的核心结论

旧审计中“文件只按 displayName 子串搜索”“size/modifiedAt 假接线”“Dashboard/搜索界面覆盖为零”等结论已大幅过时。当前代码已经形成统一搜索、内容记录、文件 AND/OR 查询、VibeTable 界面和更完整 Dashboard 测试。

当前未完成形态主要不再是明显 TODO，而是：

- 真实用户路径未进入 packaged E2E；
- 只验证生命周期，没有验证复杂交互；
- capability producer 存在但产品入口隐藏；
- 单次测试通过但真实时序不稳定；
- 开发期数据格式尚无长期兼容承诺。

## 5. 架构科学性与合理性

### 5.1 科学合理的部分

1. **数据权威单一。** PocketBase/Go 是业务数据 authority，Python BFF 不拥有 SQLite 写路径，Web 不绕过 Host；没有形成隐蔽的第二数据源。
2. **本机能力归属合理。** WPF 持有 WebView2、进程生命周期、原生 picker、绝对路径和 path grant，Web 只得到受控 handle。
3. **跨进程 seam 是真实的。** session epoch、request/operation ID、取消、稳定错误 envelope、capability allowlist 都有 production/test adapter 和分层测试。
4. **恢复模型成熟。** Snapshot、row history、ledger、workspace identity、外部 replica 的语义分离清楚，没有把 CSV 导出冒充备份。
5. **深 module 方向正确。** `SchemaCore` 的 `Describe/Plan/Apply`、WorkspaceSearch engine、Surface Repository/ActionRuntime 都把复杂状态和验证藏在较小 interface 后。
6. **生成契约降低漂移。** Schema V2/Workbench DTO 同时生成 Python/Go/C#/TypeScript，并以闭对象、fixture 与 stale check 约束。
7. **大基数不是靠提高常量硬扛。** 设计采用分页、预算、取消、checkpoint 和持久后台任务。

### 5.2 架构成本与不合理之处

#### A. Interface 总面积过大

product catalog 100 methods，workspace catalog 59 methods。DTO 生成化解决了 shape 漂移，但没有解决调用方需要理解多少 method、错误、capability 和生命周期约束的问题。

建议：按业务能力形成 `SchemaCore`、`ViewQuery`、`WorkspaceSearch`、`SurfaceRepository`、`HistoryRestore` 等 capability interface；由生成器同时生成 method registry、allowlist 和 error mapping。外部仍可共享一个 transport envelope，但调用方不应直接面对 159 个平铺方法。

#### B. 拆文件已发生，但部分 module 仍浅

旧的 `MainWindow.Product.cs`、dispatcher 和 `product_rpc.py` 已明显拆分，这是进步；但 `WorkspaceView.vue` 仍有 1,570 行，若干 controller 的覆盖仅 47%–68%，说明部分状态只是从一个文件平移到多个文件，interface 和测试 seam 尚未完全变深。

判定标准应使用删除测试：删除 module 后，复杂度是否集中消失；若只是在调用方重新出现同样编排，它仍是浅 module。

#### C. Go 权威内核存在大实现热点

`filehistory/service.go` 2,625 行、`replica/production.go` 2,132 行、`attachments/manager.go` 2,080 行、`query/compiler.go` 1,799 行、`mutation/apply.go` 1,790 行。

大 implementation 不自动是坏事；若 external interface 小且内部状态局部化，它可能是深 module。优化重点不是按行数机械拆文件，而是建立内部 seam：revision store、content repository、query planner、commit coordinator、fault adapter，并只通过外部 interface 测行为。

#### D. Python BFF 的长期价值需持续审查

Python 目前承担导入导出、插件、insights、surface/content 编排等真实能力，不是纯 pass-through；因此不能简单删除。但每个新增功能都应做 deletion test：若只是把 Web DTO 转发到 Go，应并入现有 transport adapter，避免第四套业务语义。

## 6. 程序与测试覆盖

### 6.1 代码/测试规模

| 栈 | 生产文件/行 | 测试文件/行 |
|---|---:|---:|
| Python BFF | 79 / 21,571 | 102 / 28,788 |
| Web | 195 / 49,581 | 164 / 32,784 |
| .NET | 157 / 40,269 | 103 / 23,522 |
| Go | 214 / 85,281 | 166 / 45,662 |
| QA/scripts | 36 / 11,267 | 自动化契约混合 |
| JSON contracts | 42 / 31,428 | codegen/fixture 验证 |

静态测试声明约为 Python 858、Web 1,089、.NET 601、Go 698；参数化后当前 CI/本地实际执行数更高。

### 6.2 当前动态结果

| 范围 | 结果 |
|---|---|
| Web test:coverage | 164 files、1,163 tests 全通过；statement 83.88%、branch 76.54%、function 81.95%、line 87.58% |
| Web typecheck/build | 通过；4,732 modules；Vite build 10.95s |
| Go 核心深 module | SchemaCore、RelatedComputation、WorkspaceSearch 全通过 |
| Go core coverage | line 90.09%（882/979）、decision-arm 82.67%（477/577） |
| Go 稳定性聚焦 | filehistory、restore、diagnostics、workspacev2 全通过 |
| .NET 当前 CI | 646 tests、0 failed、0 skipped |
| Python 当前 CI | core lane 成功并通过 backend line 85% 门禁；本机因缺 uv 未重复执行 |
| 真实产品 E2E | main 17/18；PR 同树曾 18/18，确认非确定性焦点缺陷 |

### 6.3 覆盖薄弱区

- Web `DocumentInspector.vue` line 45.45% / function 16.66%。
- Web `DocumentList.vue` line 62.06% / branch 51.56%。
- Web `WorkspaceView.vue` line 70.29% / function 54.18%。
- 部分 workspace controller line/branch 约 47%–68%。
- .NET PreviewHost line 42.13% / branch 51.78%，刚高于 41/50 ratchet。
- .NET Desktop line 59.47% / branch 51.44%，属于中等覆盖。
- Go 量化高门禁目前只覆盖三个新核心 module；filehistory、restore、mutation、query、snapshot 等大模块主要依赖测试通过而非统一覆盖 ratchet。
- Python 只有 line 85% 阈值，没有独立 branch ratchet。

结论：总体覆盖已达到良好水平，但“聚合数字”会掩盖具体 UI 和原生模块盲区。下一步不应追求全仓统一 90%，而应按风险域建立 ratchet。

## 7. 边缘覆盖与稳定性

### 7.1 强项

测试对以下边缘投入明显充分：

- stale revision、CAS conflict、session epoch；
- 幂等、replay、duplicate、事务回滚；
- cancel、timeout、deadline；
- corrupt/malformed/tampered input；
- restart、restore、resume、sidecar kill；
- empty/null/blank 与 PocketBase 零值区分；
- path grant、越权拒绝、ZIP/路径安全；
- 1,000 行导入和 Relation fanout/budget。

故障注入中的 durability faults、Host degraded restart、真实 sidecar kill/reconcile 在当前 main CI 均通过。

### 7.2 薄弱边缘

1. **UI/原生时序。** modal、Tabulator 重渲染、WebView2/OS focus、after-leave 的组合没有稳定 test seam。
2. **Unicode 系统矩阵。** CJK 搜索有专测，但 NFC/NFD、emoji、RTL、大小写 locale、长路径、保留名、导入导出编码未形成跨栈矩阵。
3. **真实规模。** PR qualification 仅 1,000 records、100 files、64MiB；不足以证明大工作区稳定性。
4. **非零破坏路径。** retention 真删除、插件成功升级/回滚/卸载、更新后健康失败回退等仍缺完整产品证据。
5. **性能证据过时。** 文档仍是 12 场景旧基线；当前 18 场景和新搜索/界面没有可比较趋势。
6. **跨版本升级。** 当前开发阶段不把历史 workspace 升级作为门禁，不能对长期数据兼容作生产级承诺。

## 8. 当前发布阻断：结构化 modal 焦点恢复

### 8.1 根因判断

当前 `restoreStructuredDialogFocus` 在 modal 关闭后：

1. Vue `nextTick` 尝试一次；
2. 失败后再用一个 `requestAnimationFrame` 尝试一次；
3. 从 Tabulator `getRows()` 单次快照定位当前 row/cell。

这避免了 `getRow` 未命中 warning，但两个固定时点都可能落在数据提交后的行 DOM 替换窗口。单元测试使用静态 DOM 与静态 row snapshot，无法模拟 `after-leave → 数据提交 → renderComplete → DOM 替换`。

### 8.2 推荐修复

建立 Grid/Tabulator 内部真实 seam：

```ts
interface GridFocusPort {
  focusCell(
    target: { rowKey: string | number; field: string },
    context: { expectedRenderRevision: number; signal: AbortSignal },
  ): Promise<"focused" | "not-found" | "cancelled">;
}
```

- Tabulator adapter 订阅一次 `renderComplete` 或等价稳定事件，再解析当前 row/cell；若当前 revision 已稳定则立即执行。
- modal controller 只表达“关闭后恢复目标”的意图，不理解 Tabulator row snapshot。
- workspace/table 切换、再次打开 modal 时用 AbortSignal/epoch 取消旧请求。
- 目标被过滤或删除返回 `not-found`，不打印 warning，不把焦点强行放到错误行。
- 不新增固定 sleep，也不把无限重试当修复。

### 8.3 验收

- 单测：原节点存活、行重建、目标删除、过滤隐藏、快速开关、切换取消、OS focus 丢失。
- 组件/adapter 测试：使用可触发 `renderComplete` 的 fake Tabulator adapter，而非只测静态函数。
- packaged E2E：场景 04 独立进程连续 3 次通过，随后完整 18/18 通过。
- 主分支 `required` 恢复绿色后才能重新声明 release-qualified。

## 9. 技术债务台账

| ID | 技术债 | 优先级 | 影响 | 修复成本 |
|---|---|---|---|---|
| TD-01 | WebView2/Tabulator modal 焦点非确定性 | P0 | main 红灯、可访问性和键盘流中断 | 中 |
| TD-02 | 能力矩阵/稳定化/性能文档停在 16/12 场景 | P0 | 错误决策、历史绿灯掩盖当前失败 | 小–中 |
| TD-03 | 159 个平铺 RPC method 与多栈 registry/allowlist | P1 | 变更扩散、学习成本、漂移风险 | 大 |
| TD-04 | WorkspaceView/文件 UI/controller 覆盖岛 | P1 | 用户可见回归不能由总覆盖发现 | 中 |
| TD-05 | 替代视图、Dashboard 深交互、更新、mirrored、插件生命周期缺 E2E | P1 | 功能存在但产品成熟度不可证 | 大 |
| TD-06 | Go 高覆盖门禁只覆盖三个新核心 module | P1 | 权威大模块回归量化不足 | 中 |
| TD-07 | PreviewHost/Desktop 覆盖余量有限 | P1 | 原生生命周期与文件能力风险 | 中 |
| TD-08 | 跨版本 workspace 升级不在门禁 | P1（1.0 前 P0） | 长期用户数据兼容不可承诺 | 大 |
| TD-09 | 当前开发机 uv/npm/.NET 工具链不可复现 | P1 | 新开发者与本地复验成本高 | 中 |
| TD-10 | 18 场景性能基线缺失、Web chunk 偏大 | P1 | 冷启动、内存和交互退化不可趋势化 | 中 |
| TD-11 | 更新后无启动健康确认/自动回退 | P1 | 新包可复制成功但启动后失效 | 中 |
| TD-12 | Unicode/locale/路径组合矩阵薄弱 | P2 | 中文/跨区域真实数据边缘风险 | 中 |
| TD-13 | 自研 PDF 文本提取器格式覆盖与维护成本 | P2 | 搜索漏检、异常 PDF 行为漂移 | 中–大 |
| TD-14 | npm 警告 deprecated lucide 包、glob 链及 pending install script | P2 | 供应链维护噪音和未来升级阻断 | 小–中 |

## 10. 详细优化方案

### 10.1 证据与门禁治理

1. 从 `pocketbase_product_scenarios.json` 生成能力矩阵的场景列，禁止手写 16/18 数字。
2. 文档只引用 `source SHA + GitHub run URL + report schema version`；不要引用易被清理的本机 `build/q/...` 路径作为唯一证据。
3. `docs/quality-gates.md` 更新为真实 `pr-e2e`：4 个 release.smoke 场景 + workbench qualification。
4. 建立“变更路径 → capability → scenario”映射：修改 `dialogFocus` 自动选场景 04，修改 search 自动选 18，修改 Surface 自动选 17。
5. 完整 18 场景仍由 resilience lane 执行；不要用 quick corpus 替代完整门禁。

验收：文档生成 check 在旧数字时失败；任一公开 capability 都能追到至少一个当前场景或明确 Hidden/Internal。

### 10.2 Interface 深化与契约收敛

1. 在现有 JSON Schema codegen 上扩展 method registry/error code/capability mapping 生成。
2. 将 product/workspace catalog 按 capability 分组导出，调用方只依赖所需 interface。
3. Web bridge 不再暴露大而平的 1,700 行手写表面；生成 transport glue，手写层只保留语义协调。
4. WPF controller 与 Python adapter 使用同一 capability manifest，减少重复 allowlist。
5. 对每个新 port 执行“两 adapter 规则”：至少有 production adapter 和 test adapter；没有真实变化点时不引入假 seam。

验收：新增一个字段或 method 时，手工同步点数量下降；stale generation test 能列出遗漏的全部产物。

### 10.3 风险导向覆盖 ratchet

建议分阶段提升，不一次性抬全仓阈值：

- Web：为 `DocumentInspector`、`DocumentList`、`WorkspaceView` controller cluster 单独设 line/branch ratchet；先取当前值，禁止回退，再以每轮 +5 个百分点推进。
- .NET：PreviewHost line 41→45→50，Desktop line 51→55→60；新增原生文件/进程逻辑必须在对应 assembly 计入。
- Go：把 `filehistory`、`restore`、`query`、`mutation` 纳入第二组 `-coverpkg` 风险门禁，不与新三个 module 混成一个容易掩盖的总数。
- Python：在保持 line 85 的同时记录 branch baseline，稳定两轮后引入增量 branch ratchet。
- 测试通过 external interface 断言用户可观察结果；避免为提高数字测试私有 helper。

### 10.4 产品 E2E 扩充

按风险而非页面数量新增场景：

1. 替代视图：建视图、配置、拖拽/日期移动、保存、重开、stale conflict。
2. Dashboard：多图表、筛选、钻取、导出、binding drift 修复、双编辑器冲突。
3. 数据互操作：CSV/XLSX 的空白、日期时区、select/relation、Unicode、公式值和失败原子性。
4. 自更新：旧包→新包→启动健康确认→失败回退，使用隔离安装根和 mock Release server。
5. mirrored workspace：离线、重连、同步冲突和不伪造 exclusive writer。
6. 插件：成功升级、失败回滚、卸载、重启后 catalog 一致性。
7. retention：非零删除、阻塞原因、审计与 snapshot 可达性。

### 10.5 性能与规模资格

建立三个 profile：

| Profile | Records | Files | Logical data | 用途 |
|---|---:|---:|---:|---|
| PR quick | 1,000 | 100 | 64MiB | 保持现状，快速回归 |
| Nightly | 50,000 | 5,000 | 2GiB | 查询、搜索 rebuild、恢复、内存趋势 |
| Release | 100,000+ | 10,000+ | 5GiB+ | 发布前容量资格 |

记录 cold start、workspace open、首屏、search p50/p95/p99、rebuild throughput、RSS peak、Snapshot/restore 时间和取消响应时间。性能报告按 commit 保存趋势，连续三次回退才阻断，单次硬上限立即阻断。

Web 侧对 charts、Naive UI、主 index chunk 做 route/capability lazy loading；用真实 WebView2 cold-start 与 RSS 证明收益，不以 chunk 数量本身作为目标。

### 10.6 跨版本兼容与更新健康

1. 在 1.0 前冻结 workspace/schema/snapshot version policy。
2. 至少建立 `N-1 stable → current` 的真实 workspace 升级 fixture；升级前自动 snapshot，失败保持原 workspace 可打开。
3. 新版本首次启动写健康确认；更新 helper 在超时/崩溃时恢复旧包入口。
4. 更新健康协议不触碰用户业务数据，只验证启动、版本、sidecar/BFF ready 和只读 workspace probe。
5. Release E2E 使用真实旧发布包与当前候选执行升级；普通 PR 可使用最小 fixture。

### 10.7 本地工具链可复现性

增加 Windows 只读 doctor/bootstrap 入口：

- 显示 uv、Python、Node/npm、Go、.NET、WebView2 的实际解析路径与版本；
- 检测 npm prefix 覆盖导致的 shim 错误；
- 指向官方/仓库声明安装方式，不静默修改全局配置；
- 缺失工具时明确区分“开发最小集”和“完整 release QA 集”；
- CI 与本地复用同一版本检查逻辑。

验收：全新 Windows 开发环境能在一条文档路径内从 doctor 失败到 `bootstrap` 成功；不会修改 lock、registry 或 `global.json`。

## 11. 30/60/90 天路线图

### 0–30 天：恢复发布可信度

- 修复 TD-01 事件驱动焦点恢复，恢复 main `required` 绿色。
- 场景 04 临时执行 3 次独立进程稳定性资格，根因关闭后回到单次。
- 更新 capability matrix、stabilization ledger、interprocess seams、quality gates 和 18 场景性能基线。
- 为 DocumentList/Inspector、Workspace controller、PreviewHost 建立不回退 ratchet。
- 增加 Windows doctor，解决 uv/npm/.NET 解析可见性。

退出条件：最新 main 18/18、required 绿色；同树重复不再一绿一红；文档只引用当前 SHA 证据。

### 31–60 天：补产品闭环与风险覆盖

- 新增替代视图、Dashboard 深交互、CSV/XLSX、更新事务 E2E。
- 扩大 Go `-coverpkg` 到 filehistory/restore/query/mutation。
- 生成 RPC registry/allowlist，缩小手写 bridge 面积。
- 建立 nightly 50k/5k/2GiB qualification 和趋势报告。
- 实施更新后健康确认与自动包回退。

退出条件：高风险功能均有 packaged journey；关键容量曲线稳定；跨栈新增 method 的手工同步点显著减少。

### 61–90 天：为 1.0 兼容承诺做准备

- 冻结 workspace/schema/snapshot 版本政策与 N-1 upgrade contract。
- 增加真实 retention 删除、mirrored/replica、插件升级/回滚/卸载 E2E。
- 建立 Unicode/locale/长路径组合矩阵。
- 评估 PDF 提取器：保留明确支持子集并 fuzz，或替换为维护成熟且可离线打包的 adapter。
- Release profile 扩展到 100k records/10k files/5GiB，并记录恢复时间和峰值内存。

退出条件：可以对“当前 stable 到下一 stable 的数据升级、失败回退和恢复”作明确承诺，才建议进入 M4/M5 评估。

## 12. 最终验收清单

任何“已达到稳定发布”的结论至少需要：

- `main` 的 `required` 成功，18/18 产品 E2E、0 unexpected bridge failure、0 pending request；
- 当前 SHA 的 capability、稳定化和性能证据已同步；
- 结构化 modal 焦点场景在独立进程中稳定重复通过；
- Python/Web/Go/.NET 覆盖 ratchet 全部通过，风险域无回退；
- nightly/release 容量 profile 没有超出硬预算；
- 更新后启动健康失败能恢复旧包；
- 至少存在一个 N-1 workspace 升级成功和失败回滚证据；
- 所有公开 capability 都有用户入口、闭集契约和产品级证据，或被明确标为 Hidden/Internal。

## 13. 审计限制

- 本机缺少可用 uv 和 .NET SDK，npm shim 也被用户 prefix 配置破坏；Python/.NET 未在本机重复全量运行，采用当前 GitHub core lane 作为权威证据。
- 本机 Go 为 1.26.5，高于仓库 1.25.8；本地 Go 聚焦结果只作补充，锁定工具链结论来自 CI。
- 本次未修改业务代码，也未重跑完整 WPF/WebView2 18 场景；对焦点问题的判断基于当前 main 失败日志、同树 PR 成功证据和实现时序审查。
- 评分用于优先级排序，不应用作对外 SLA 或营销百分比。
