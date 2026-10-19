# 旧可视化布局搭建退役与数据保全（#485 / Goal #480）

状态：实现与直接回归已落地；完整发布门禁和真实包证据以本次 PR 的验证记录为准。
基线：#491（PR #491，merge commit `793c45f5496da12efbe199e5fbe4921324589466`）之后。

## 产品行为

- 可视化 Interface/Dashboard 搭建（拖拽编排、属性面板、草稿/保存/发布、独立运行入口）已从正常产品移除；应用入口来自已安装代码插件（#482/#484 插件应用目录）。
- 旧公开写/运行通道拒绝：
  - WPF Host `WebMessageRouter`：`dashboard.*Requested` / `interface.*Requested` 全部移出请求白名单，命中固定的 `RetiredLayoutRequestTypes` 集合，回 `operation.failed`（code `CAPABILITY_RETIRED`）。
  - Go sidecar：`interface.list/load/commit/delete` 与 `insights.*` 7 个方法仍在描述符目录中（owner goSidecar）但注册为显式拒绝，直接请求返回公共错误码 `layout.retired`，并指向 `legacyLayout.snapshot` 保全通道。
  - 生成契约同步：`product-rpc-capability-policy.json` 将上述 11 个方法 audience 降为 `hostOnly`；`legacyLayout.snapshot`（workspace/hostOnly/read）经 catalog→policy→manifest→TS/Python/Go 适配器全链路再生成。
- 插件打印不受影响：`PluginSurfaceApi.showPrint` 直接连接 Host 的 WebView2 `ShowPrintUI`；旧 dashboard 专属 controller 已退役。插件权限、取消和会话生命周期仍由插件宿主管理。
- 保留能力：字段设置、表格视图筛选/排序/列宽、文件管理、查询/聚合（`query.page`/Aggregate）、字段格式化与采样、ECharts option/renderer（`desktop/web-grid/src/dashboard/{types,sampling,format}.ts` 与 `charts/`，含 `DashboardChart.vue`）继续可用且有测试。图表使用最小 `DashboardChartInput`，不再需要旧布局身份、位置和草稿 DSL；无人使用的 gridstack/html-to-image 已通过 npm 卸载并生成 lock。

## 数据保全（Host 拥有、只读）

- 集合不动：`vibetable_interfaces`、`vibetable_dashboards`、`vibetable_panels`、既有 migrations、metadata Service、audit/outbox/idempotency 全部保留；不迁移、不删库、不写入或重建数据；读取 revision 沿用既有 payload 派生算法，不触发 metadata 写入。
- Go 新增只读保全读取器 `sidecar/internal/metadata/legacy_preservation.go`：
  - 直接读原始三个集合记录的 `payload_json` 原文 + 存储记录 ID + `logical_id` + 独立 `parent_id` + 既有 payload 派生 revision（无法重算的损坏项保留原文并逐项诊断）。
  - 明确不经过旧 DTO 闭集校验（该校验恰恰拒绝复杂定义——已被单测证实），逐项诊断：`legacy.payload_unparseable/payload_not_object/identity_mismatch/version_unsupported/element_manual_rewrite/plugin_action_unsupported/dashboard_manual_rewrite`。
  - 产品 RPC `legacyLayout.snapshot`（hostOnly，仅 Host 调用）暴露该快照；Web 桥契约 `legacyLayout.reportRequested/exportRequested`（`desktop/web-grid/src/contracts/surfaceBridgeContracts.ts`）为唯一 renderer 入口：renderer 只能请求检测报告和触发 Host 原生受控 JSON 保存，不能提交内容或路径；无导入/执行入口，不新增 hash。

## 旧能力 → 替代矩阵（与 Issue #485 冻结矩阵一致）

| 旧定义能力 | 结论 |
| --- | --- |
| text、记录列表/分页、表内筛选排序、详情表单 create/update、同页导航 | #484 record-manager 代码插件可代表替代；合成 fixture 保持同一业务 table/field/record ID，经正常插件安装采用；不创建另一 Interface 对象。 |
| dashboard metric/聚合、chart、复杂布局/tabs/跨页编排 | 需人工改写代码插件；公共查询/聚合/图表模块保留，不宣称 record-manager 已复现。 |
| legacy plugin action | 早已明确拒绝执行；保全并诊断（`legacy.plugin_action_unsupported`），最终 Worker 处理属 #486。 |
| 未知元素/字段/版本、任意旧表达式 | 不自动转换；逐项定位、原样保全、不 eval、不静默丢弃。 |

## 保全与验证路径

`LegacyLayoutRequestController` 通过 Go 权威快照检测和导出；PluginCenterView 展示原定义数量、逐项诊断和人工替代说明。Host 使用原生 JSON 保存选择、workspace epoch lease 和文件 broker 结算。取消、退休会话或写入异常不会提交临时文件；renderer 的未知参数、路径和内容在读取快照前拒绝。

旧 Python dashboard 写适配退出，metadata 对三个旧 namespace 的写入在调用 sidecar 前拒绝。Go 的旧公开 RPC 保留明确退役响应，不能借 list/load 或旧 DTO 作为兼容执行器。

scenario16 在 runner 创建的独立工作区中先通过产品建立真实业务数据，待所属 Host 退出后核对绝对路径及 workspace UUID，仅向三个旧集合加入历史定义（包含未知字段和损坏 JSON）。重开产品后核对提示、受控导出和 record-manager 原表展示，并对比原定义原串、记录/字段/表 ID、附件元数据、audit 与 revision；再次重开检验重复处理。scenario17 承接 record-manager 的分页、确认式写入、拒绝、刷新和导航；scenario46 保留导出取消及新插件原生打印的终态、会话退休和清理断言。scenario45 普通字段能力保留。

直接验证包括 Web 全量测试与构建、.NET 全量测试、Go 保全/旧端点回归、Python 相关写入拒绝以及 E2E runner/fixture 契约。真实包场景及项目 quality/pr-e2e/build/smoke 的结果单独记录，不以源码契约测试代替真实 WPF 验收。JSON 仅为保全材料，没有自动导入或执行入口；复杂布局、图表和旧插件动作仍需人工改写。
