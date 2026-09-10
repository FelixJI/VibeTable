# Dashboard/Panel Product owner 本地资格

## 固定 Python oracle（生产迁移前）

基于远端 main `a3ca78b9181a529d978f9fba46586fbb924ecada`，七个公开 `insights.*` 方法作为完整 Dashboard/Panel 聚合迁移；不迁移命名 Version 或 SharedSettings，不提前声明 L5 或 L6–L10 完成。

`contracts/v2/dashboard-python-oracle.json` 固定原 Python producer 的 49 个样本。捕获器经原 dispatcher、Pydantic DTO、InsightsService 和 PocketBaseInternalMetadataPort；仅以受控底层 authority responses 替代 I/O，并固定测试 UUID 来源。覆盖公开七方法、列表投影/排序、workspace revision、原子草稿/配置引用、内置 panel manifest/options、records/aggregate 查询转换与限额、DTO/error。生成器从固定可达 Git commit 归档 backend，在独立 `build/contract-oracles/dashboard-python/replay-*` 目录重放；子进程 `-I` 排除继承 Python 路径，不依赖迁移后的 Go/Python owner。UUID 是既有 ID 分配 seam；workspace revision 沿旧规范 JSON 算法，不添加额外摘要身份层。

- `uv run --frozen --no-sync python contracts/v2/generate_dashboard_python_oracle.py --write` 首次生成；随后 `--check` EXIT 0。
- `uv run --frozen --no-sync pytest tests/contract/test_dashboard_python_oracle.py -q --no-cov`：4 PASS、0.79s（`build/dashboard-oracle-tests.log`），含真实旧 producer 重放与篡改投影拒绝。相关 Ruff format/check 通过。首次 lint 曾发现两个循环闭包绑定警告，已显式绑定后复验通过。

## 有意修复与实现边界

旧 Python 创建会在 receipt 之前随机分配 Dashboard/Panel ID、读取当前状态；删除只删除 Dashboard 而不级联 Panel。这些不是应复制的兼容语义：新 Product owner 必须通过真实 Runtime 普通写 gate，在已验证请求摘要后恢复完整原结果/ID 映射，优先于状态读取，并在同事务内级联删除。原错误样本保留；新语义由独立回归覆盖同/异 payload、删除后重放、Runtime/PB 重启、epoch/取消、CAS/事务回滚。七方法、合法 DTO/投影、查询限额及既有 workspace revision 保持，关闭 dashboards/panels generic 写且保留必要读。

上述 oracle 提交时仅完成冻结，Go owner、Host Product 接线、generic 写关闭及 S16 新重启段尚未实施。没有发布构建、GUI、新包、远端 CI 或独立双轴资格；此文后续只追加实际完成和失败证据。

## Dashboard/Panel 七方法完整实现（待独立审查与新包资格）

基线仍为 `a3ca78b9181a529d978f9fba46586fbb924ecada`；冻结 oracle 先提交于 `ae8bd6f6`，原 JSON 样本未随实现修改。新增 Go metadata Dashboard 深模块及 Product 注册完整拥有 `insights.listDashboards`、`insights.readDashboardWorkspace`、`insights.saveDashboardDraft`、`insights.deleteDashboardWorkspace`、`insights.executeDashboardQuery`、`insights.dashboardQueryLimits`、`insights.panelManifest`。当前 Go owner 31 / Python 71 / WPF 2；独立 Go/Host/Python 字面量清单与 scope/effect 契约同时更新，14 个 app fixture 各七个 Dashboard 注册仅一次，既有 History/Mutation 注册保留。Python 七 handler 删除；InsightsService 中未迁移的 Preset/Version 等服务与 generic reads 保留，不宣称其他尚未合入分支已在本基线完成。

Host 的 JsonRpcDashboardGateway 经既有 JsonRpcProductDataGateway/HostProductRpcInvoker 共用实际 workspace Product binding，MainWindow 不再直连 Python Dashboard RPC。ProductSidecarHttpGateway 只接收七方法中封闭的 -32080 Insights error shape/code，DashboardErrorMapper 映射稳定提示；未知 code、额外私有字段仍拒绝。保存和删除使用真实 Runtime 普通 BusinessWrite gate，完整 receipt 校验/恢复后以精确 ErrBusinessReplay 信号结束 prepared intent，不重复推进 workspace mutationRevision。共享 metadata.executeIdempotent 新增可选 replay callback；旧调用无 callback 的语义保持。receipt 使用 UseNumber 解码，避免动态 JSON 大整数/小数重放时丢失精度。存储均用结构化 json.Marshal；只沿用既有请求 receipt 摘要与 workspace revision 算法，没有再叠摘要身份层。receipt 仍遵循既有 24 小时有效期，不声称无限期重放。

完整草稿以真实 UI 的 deletedPanelIds 契约为准：旧 panel 必须保留或显式删除，省略时事务拒绝；不静默留下与返回 workspace 不一致的 panel。配置的 clientPanelIds 引用在事务中映射，未知引用拒绝；删除 Dashboard 同事务级联 Panel。普通 gate 保留 workspace 准入、epoch/fence/claim、取消与 CAS。关闭 dashboards/panels 的 generic upsert/delete 及旧 dashboards/commit HTTP 入口，GET reads 保留。三个无参公开方法收窄为闭合空 DTO；真实 Host 都发送 `{}`，未知额外字段被拒绝。旧 missing binding 的内部错误样本不改，新 owner 以 dashboard_panel_membership_invalid 表达同一拒绝。

S16 保留原完整旅程及截图，追加受控真实 sidecar 终止/恢复；通过 fresh 公开 list/read 及 UI 重新打开，比较完整 workspace（含 panels/config/bindings/revision）与源 table 的全部两行数据。使用既有恢复窗口、终止 ACK 和清理 seam，不改共享 automation。历史 29 场景文档相对旧样本的 changed 项保持 S07 并增加 S16；本轮未运行这个新 GUI 段。

### 实际本地验证与失败保留

Go 使用仓库固定 Go 1.27.0；下列 Go 命令 cwd 为 sidecar。Python 使用现有锁定 uv 环境（`UV_NO_SYNC=1`），未改 lock。前端依赖从已核对所有非 optional 包版本匹配的本机 worktree 缓存复制到本 worktree，未用主 clone 中陈旧版本替代。

- `go test ./internal/metadata -run '^TestDashboardFrozenPythonOracle$' -count=1` 首次失败于图表 minHeight 与非对象 envelope 的预期层级；修正后 49 样本 PASS 4.731s（`build/dashboard-go-oracle-first.log` / `dashboard-go-oracle-correction.log`）。冻结 JSON 未改。
- `go test ./internal/metadata -run '^TestDashboardScalarCompatibilityAndStoredJSON$' -count=1` 在修正前 RED：整数文本接受 `3e0`/`3.`/`3.00_0`，存储 JSON 接受尾随内容（`build/dashboard-scalars-red.log`）。修正保留合法整数转换、引号/反斜杠/Unicode 和数值语义；最终三包 `go test ./internal/metadata ./internal/productrpc ./internal/app -run '^TestDashboard' -count=1` PASS（4.826s / 0.706s / 2.054s，`build/dashboard-final-focused.log`）。
- 真实 Runtime durable replay 首次涵盖完整原结果/ID、删除后重放、PB+Runtime 重开、异 payload、epoch/workspace、generic 写拒绝，PASS 1.430s（`build/dashboard-runtime-first.log`）；CAS 并发一成一败、取消与 audit 故障事务回滚、reads 保留 PASS 1.337s（`build/dashboard-runtime-cas.log`）。实际 Go queryschema/QueryPort 的 records 投影及 aggregate 也通过 HTTP 回归。
- 随后在上述真实 Runtime 测试加入 `defaultValue` 的 `9007199254740993` 与 `1.0`，修正前重放分别变为 `9007199254740992` 和 `1`，确定 RED（`build/dashboard-numeric-replay-red.log`）。UseNumber 修正后 `go test ./internal/metadata ./internal/app -run 'TestDashboard|TestMetadata|TestCommitDashboard' -count=1` PASS 4.972s / 2.131s（`build/dashboard-numeric-replay-green.log`），含重启后/删除后原结果字节一致与 revision/receipt 不重复增加。
- 完整影响包 `go test ./internal/app ./internal/metadata ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb -count=1` **EXIT 1**（`build/dashboard-go-packages.log`）：app 仅 HistoryRestore commit 与 SchemaGetTable 两项 TempDir 清理目录非空；metadata 6.035s、productrpc 0.751s、capabilities 0.223s、真实 cmd 进程 5.983s PASS。没有 duplicate registration 或业务断言失败，但整体不能记 PASS。此前聚焦合并命令也有一次 Dashboard CAS TempDir coordination 非空，保留 `build/dashboard-complete-first.log`；不以之后聚焦 GREEN 撤销该次 EXIT 1。
- `go vet ./internal/app ./internal/metadata ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb` PASS（`build/dashboard-go-vet.log`）；所有修改 Go 文件 gofmt。
- `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~Dashboard|FullyQualifiedName~HostProductRpcCompositionTests|FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductSidecarHttpGatewayTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests' --logger 'trx;LogFileName=dashboard-host-complete.trx' --results-directory build/qa/dashboard-host`：144 PASS / 0 skip / 16s；日志 `build/dashboard-host-complete.log`。首次正常 locked restore 从已有缓存完成。新真实 controller 测试曾因缺 Raw 参数编译失败、以及误用 schema-only 测试 policy 导致 3 FAIL；改用现成完整生产 policy 后 3 PASS，保留 `dashboard-controller-first.log`、`dashboard-controller-correction.log`、`dashboard-controller-policy.log`，未弱化 production policy。
- `uv run --frozen --no-sync pytest tests/contract/test_product_runtime_inventory.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_dashboard_python_oracle.py -q --no-cov`：22 PASS 1.82s（`build/dashboard-contracts.log`）。
- `uv run --frozen --no-sync pytest tests/backend/application/test_insights_service.py tests/backend/application/test_insights_port_service.py tests/backend/rpc/test_dispatcher.py tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q --no-cov`：首次 235 PASS / 2 FAIL 28.15s，缺少 node_modules 的 compiler-sfc/playwright-core（`build/dashboard-python-adjacent.log`）；复制版本匹配缓存后，仅原两个失败项 `test_node_runner_inventory_matches_the_product_scenario_manifest` 与 `test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime` 复验 2 PASS 16.90s（`build/dashboard-runner-correction.log`）。未把拆分补跑描述为完整 Python suite PASS。
- Product capability policy、Product E2E capability index 均经原脚本生成并 `--check` 通过；相关 Python 文件 Ruff format/check、S16 Node `--check` 通过。首轮 policy 生成曾拒绝不合法 cancellation 值及 group 非规范排序，按真实 cooperative 与排序契约修正后生成通过，未放宽生成器。

上述仅是源码与相关本地契约证据。没有 full release build、新 Dashboard 包、真实 S16 新段、完整 Python/多栈质量入口、远端 CI 或独立双轴通过结论；后续由 root 安排独立审查和必要新包资格。其他 L5 余项及 L6–L10 目标范围未缩减。

### 完整质量、新包与真实 S16 后续

原完整提交6988c96d经独立Standards/Spec0；正常合入main27e511至e12bf68b，无冲突，增量两轴0。首次全质量因日志目录缺失未执行；实际运行又因本工作树.venv缺依赖导致Pyright50项导入失败。`uv sync --frozen --group dev --group build --offline`补齐锁定依赖后，全Python入口1863 PASS、1 SKIP、2 FAIL，coverage91.42%，Ruff/type阶段通过；两FAIL为退出Python的七个Dashboard方法未保留catalog参数模型。

修正生成输入显式保留七方法typed参数；两个无参方法使用既有空闭合模型，匹配Go已实现的空DTO。catalog仍按所有owner穷尽校验，不删公开方法。原generator生成golden，仅两个paramsModel名称改变，冻结Python oracle不改。`uv run --frozen --no-sync python -m pytest tests/contract/test_product_contracts.py -q --no-cov`：12 PASS/1.16s，Ruff format/check通过。

`uv run --frozen --no-sync python scripts/build_next.py --release`：source e12bf68b，EXIT0，日志build/qa/dashboard-metadata/build-release.log；此前未通过的完整Python结果不由build通过覆盖。

同包S16首运行EXIT1：build/qa/dashboard-metadata/product-e2e/20260910T051543Z。15项业务断言通过至CAS冲突重载；设置抽屉仍打开，nav-tables被n-drawer-mask拦截并在原30s失败，尚未进入sidecar重启段。修正仅通过包含当前settings表单的抽屉原关闭按钮正常关闭并等待隐藏；不force点击/加sleep/延长超时/改业务断言。Node语法与diff检查通过，待同包复验；运行生产路径未改，不重复构建相同产品。

### 真实抽屉关闭修复
第二次同包 S16 仍 EXIT1（build/qa/dashboard-metadata/product-e2e-drawer-close/20260910T052107Z）：正常点击关闭按钮后表单未隐藏，15项业务断言通过，重启段尚未到达。锁定 Naive UI 的 DrawerContent 通过父 Drawer 的 update:show 请求关闭；受控 Drawer 未监听该事件，原 DrawerContent @close 无效。改由 Drawer 的 update:show 统一转发关闭，取消按钮原行为保留，不通过遮罩绕过按钮问题。
真实组件（未替换 Drawer/DrawerContent）点击标题关闭按钮：旧实现 1 FAIL/11 PASS，close 未发出（drawer-close-red-actual.log）；修复后抽屉与 Workspace 相邻两文件 15 PASS（drawer-workspace-green.log），npm run typecheck PASS（drawer-typecheck.log）。早先错误测试路径只运行11旧测试，不算RED；随后一次相邻路径错误只运行12抽屉测试，不算两个文件通过。上述15项来自正确的 src/views 路径。
运行时 Web 已变更，必须重新完整构建并重跑 S16；当前尚未宣称通过重启旅程。
