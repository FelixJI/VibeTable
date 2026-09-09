# mutation 产品 RPC Go owner 迁移资格

此变更将同一 mutation 契约下的 preview/apply 成对迁入 Go，避免预览与应用形成中间 owner 状态。保留已有 mutation Kernel 和 workspace 写门禁，Python import/export/plugin 使用的内部 client 不随产品路由删除。同步 #319 后产品目录为 104 RPC、7 事件，当前 owner 为 22 Go / 80 Python / 2 Host。

前置交付为 Go 权威恢复流、Host 完整消费者和独立 workspace mutation 重放修复。重放与字段计划修复的#302以及标签#303均已各自通过CI，已由#304统一端点通过fresh CI并squash进入main。Host #300已完成合并后main CI/CD闭环；本分支已同步#304实际main结果，基础修复不重复计入owner切换的PR diff。

## 契约

Go 保留闭集根 DTO、Unicode scalar、通用 JSON 深度/数值/secret 与紧凑 UTF-8 1 MiB 限制，参数错误在 dispatcher 层拒绝；领域 DecodeStrict 和既有公共错误在 handler 层处理。apply 使用原 mutation.apply business gate，preview 不进入写门禁。Host 默认路由由生成的能力清单决定，Go 错误不回退 Python。

历史 Python oracle 保留 producer `38098da214a0fb33bb6df1fd0707b1ba0b4ac754` 与 32 条原件。capture/capture_case/--write 已退休；默认与 --check 仅检查历史结构和静态约定。包括完整 domain/transport error.data 与 JSON 类型，11 个破坏变体先失败后通过。原 JSON 相对 `9be34b05896dec4d348105d68216969306b22f21` 无 diff。历史 scripted transport 结果不是实际 mutation 领域验收；Go 直接消费两条完整 typed 结果，其余语义依赖对应领域与实际 handler 测试。

## 本地验证

复用缓存工具链和环境。工作树原有空 .venv 使固定 Pyright 路径无法解析依赖，使用 `uv sync --frozen --group dev --group build --offline` 从缓存补齐，未重建环境或修改 lock。

- sidecar：`go test -race ./internal/app -run '^(TestMutationProduct|TestMutationExistingREST)' -count=1 -timeout=5m` PASS19.544s。真实 PocketBase + workspace Runtime + REST/Product handler 覆盖 preview 无权威写入、insert、完整 receipt、幂等重放、stale CAS、旧 epoch 和关闭门禁拒绝。修复前新 Product 与旧 REST 重放均有 RED 日志。
- Go productcapabilities 与 productrpc 全包 race PASS；旧严格 HTTP fixture 补入两个不得执行的注册，继续验证完整清单而非放宽门禁。
- HostProductRpcCompositionTests：固定 SDK/locked restore，36 PASS、0 skip；两新方法默认 Go 及错误不回退 Python。
- Python 路由/owner 聚焦测试 48 PASS；历史 oracle 26 PASS，包括 11 个 error.data 破坏变体 RED→GREEN。
- `uv run --frozen --no-sync python scripts/automation_project.py python-quality`：Ruff/Pyright/mypy PASS；初次完整 pytest 1799 PASS、1 skip、1 fail，coverage91.36%。失败为旧精确 owner 集合未包含本次两个方法；修正后该契约文件 10 PASS。同一完整入口最终复验 PASS：1800 passed、1 skipped，70.73 秒，coverage91.36%；保留前述失败记录。

证据保留于 build/mutation-product-http-integrated-replay-fix.log、mutation-owner-go-capabilities-corrected.log、mutation-host-composition-tests.log、mutation-owner-python-focused-corrected.log、mutation-error-data-red.log、mutation-error-data-green.log、mutation-owner-python-quality-final.log、mutation-owner-inventory-final.log、mutation-owner-python-quality-verified.log。

## 审查和剩余资格

Standards 未发现确定违例，指出严格 fixture 装配多处重复的维护性建议；本意图保留现有装配，不混入独立重构。Spec 发现历史 error.data 漂移检查不足，已修复并独立增量复审为 0 新问题。新增 owner 集合与本文已完成两轴独立增量复审，均为 0 个确定问题。

当前尚未完成完整 fresh CI、squash merge 及 main CI/CD；以下最新实际包验证已完成，旧 Host 构建证据不作为本 owner 端点的实际运行。
## 最新实际产品证据

冻结生产来源 `9d6af2efe0330cf2823bf0eb21e01b1181e8ce21`，`uv run --frozen --no-sync python scripts/build_next.py` 完整构建 EXIT0。构建包为 `dist/VibeTable.Next`，sidecar build-info commit 为 `9d6af2efe033`，四组件 freshness 全通过；没有复用旧 0.5.0 包。

命令：`uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 02-all-field-schema --scenario 04-json-round-trip --scenario 08-stale-conflict --scenario 11-plugin-mutation`。

运行 `20260908T165809Z`：4/4 PASS、0 failed、0 skipped。S02 普通编辑与撤销 13,217ms/18断言；S04 JSON 编辑、粘贴、导入导出 9,968ms/20断言；S08 过期冲突 4,893ms/8断言；S11 插件授权/拒绝 8,013ms/14断言。全部 pageErrors、异常 bridge failures 与 pending 为空，node/Host exit0，无残留进程，端口释放、owner lease 与最终清理均 PASS。

日志 `build/mutation-owner-product-build.log`、`build/mutation-owner-product-e2e.log`；完整报告 `build/qa/product-e2e/20260908T165809Z/product-e2e-report.json`。该证据覆盖当前产品编辑和保留的 Python 工作流，不扩大为所有取消/崩溃故障注入或完整发布资格。
补充相邻装配验证：`go test -race ./internal/app -run Product -count=1` 完成于271.748秒，但整体 FAIL。唯一报告失败为 `TestLookupListProductHTTPKeepsSemanticBudgetAcrossWireEscaping` 的 `TempDir RemoveAll cleanup: directory is not empty`；未报告 Product 业务断言失败。日志 `build/mutation-owner-product-app-race.log` 保留，不重试、不删除清理检查，也不将该运行写为 PASS。该清理问题与此前本地 Go 阶段同类，完整远端门禁仍负责最终验收。

## Host 修复集成后的实际产品补充

正常合并 Host d17314f24e3ed71f1aa116707074ffdacd9d726a 后，冻结源码 2204a406e00d5e34725881ba980b4ad830496ce7；owner 实现不变，新增生产差异是自动恢复保留响应时最新的兼容 Dashboard 会话筛选。复用既有环境执行 `uv run --frozen --no-sync python scripts/build_next.py`，完整构建退出 0，sidecar build-info commit 为 2204a406e00d，四组件 freshness 全通过。

`uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 02-all-field-schema --scenario 08-stale-conflict --scenario 16-dashboard-lifecycle`：运行 20260908T185856Z，3/3 PASS、0 skip。S02 13.173 秒/18 断言；S08 4.751 秒/8 断言；S16 28.225 秒/17 断言。各场景 Node/Host exit 0，pageErrors、异常 bridge、pending 均为 0；members/descendants 为空，端口释放、owner lease 和最终清理通过。

原始报告为 build/qa/product-e2e/20260908T185856Z/product-e2e-report.json；构建和运行日志为 build/mutation-owner-current-host-build.log、build/mutation-owner-current-host-e2e.log。S04/S11 仍归属于前述 9d6af2ef 运行，没有在本次源码重跑。前置 #300/#302 尚待完整合并闭环；本补充不替代最终 fresh CI。

## 统一候选后的最新实际资格

源码e7eb336274c636aaa839dfa66b0f137716b8838e，正常同步#304候选5e978159。相对候选仅保留mutation.preview/apply成对owner迁移、契约和oracle/测试/资格记录；不重复携带replay基础修复。两轴合并交界审查无新增确定问题；此处为最新运行，上述历史证据保留各自源码归属。

- `go test -race ./internal/app -run '^(TestMutationProduct|TestMutationExistingREST)' -count=1 -timeout=5m`：PASS19.585秒，日志build/mutation-owner-qualified-candidate-race.log。
- Web源码与已验证#304候选完全等价，复用其1523 PASS覆盖检查，不冒称在本分支重复运行。
- `uv run --frozen --no-sync python scripts/build_next.py`：完整构建退出0，四组件fresh，sidecar build-info为0.5.1/e7eb336274c6，日志build/mutation-owner-qualified-product-build.log。
- `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 02-all-field-schema --scenario 04-json-round-trip --scenario 08-stale-conflict --scenario 11-plugin-mutation --scenario 28-relation-delta-preview`：run20260908T210452Z，5/5 PASS、0 skip。

| 场景 | 耗时 | 断言 | 已确认预期bridge失败 |
|---|---:|---:|---:|
| S02普通编辑 | 13.536秒 | 18 | 1 |
| S04 JSON/Data IO | 9.929秒 | 20 | 0 |
| S08过期冲突 | 4.409秒 | 8 | 0 |
| S11插件写入 | 7.965秒 | 14 | 1 |
| S28关系preview/标签 | 5.496秒 | 10 | 0 |

五场景Node/生命周期Host退出0，pageErrors、未确认bridge failures、pending均0；成员/后代为空，端口、lease和最终清理通过。覆盖产品Go owner与保留Python Data IO/Plugin路径的相邻交界，不推断全部取消/崩溃或完整发布资格。

报告build/qa/product-e2e/20260908T210452Z/product-e2e-report.json，日志build/mutation-owner-qualified-product-e2e.log。此源码未重跑S16，先前2204a406上的S16证据保持历史归属。等待#304实际main后完成独立Mutation PR的fresh CI、squash和main CI/CD。

## 实际 main 端点同步

#304 的 required CI 全部成功，2026-09-08 21:43 UTC squash 合并为 `3bf03bc6f47399bacbdb1305a9b110c8d3b11ae5`。本分支通过正常 merge 同步为 `2eb6b2f76ed3f292c2ce7be59b7ad81077fe5362`，与此前 `ba3c8871` Git tree 完全一致；复用上述 `e7eb3362` 构建、定向 race 与五场景资格，无需重复生成可执行文件。#304 合并后的 main CI/CD 仍在跟踪，本 owner PR 的 fresh CI、squash 和合并后闭环仍待完成。

## 首轮 fresh CI 的进程清单修正

CI run `34282769151` 的 core 与 race-b 同在 `TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart` 失败：实际 Go 注册21项，进程测试仍冻结迁移前19项。仅向 RPCMethods 与 Registrations 的精确排序清单加入 `mutation.apply`、`mutation.preview` 和各自 workspace scope，保留全部旧方法与身份/失败关闭/跨重启断言。定向旧测试 FAIL1.349s；`go test ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1` PASS1.708s，同命令加 `-race` PASS10.148s。日志位于 `build/qa/mutation-process-capabilities/`。产品源码未变；更新后的 fresh CI 与合并闭环仍待完成。

## Host owner 清单与不可重试写入回归

新一轮 CI `34285984850` 的 core 已推进到 .NET，暴露4处 Host 精确 owner/scope 期望未同步及1处旧 Python 写入夹具仍使用已迁移的 mutation 方法。仅更新4个测试文件：两 mutation 方法精确加入 Go/workspace 闭集；原 disposed Python 写回归改用仍属 Python 的合法 `field.change.apply`，并验证零传输写入；新增 Go mutation 在首 forwarder disposed 后即使替代端已安装也只调用原端一次、替代端零次、Python零次，并发出唯一 BACKEND_UNAVAILABLE。

聚焦4类测试旧5 FAIL/93 PASS（480ms），修复后99 PASS（482ms）；命令为 `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~ProductRpcCapabilityManifestTests|FullyQualifiedName~ProductRpcRouteSelectorTests|FullyQualifiedName~WebMessageRouterTests|FullyQualifiedName~WorkspaceRequestDispatcherQueryTests'`。去掉filter运行整个Desktop.Tests：1099 PASS、1 skip（27s），skip为原有符号链接权限相关用例，未改其行为。日志 `build/qa/mutation-host-owners/{red,green,desktop-full}.log`；双轴增量审查0问题。生产代码与已有打包资格不变；后续 fresh CI 仍待完成。

已将 #307 的实际 main squash `cadf5153` 正常同步为 `e871bf1c`，无冲突；仅包含已独立审查的 gateway 测试夹具改动，产品源码未变。合并交界 Standards/Spec 复核无确定问题。

`dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter FullyQualifiedName~ProductSidecarHttpGatewayTests`：27 PASS、0 skip，116ms；日志 `build/qa/mutation-main-sync.log`。最新实际 main 端点的 fresh PR CI 尚待取得，不复用旧 head 的通过状态。

## 同步 #312 实际 main 后的资格

原 PR head `91490903cf6adff3c0031b7828f17b29e35e86ba` 的 CI run `34293658250` 保留两次失败：attempt 1 在 Coverlet 6.0.4 汇总 hits 时出现 `EndOfStreamException`（`Unable to read beyond the end of the stream`）；attempt 2 的 core job `102296968911` 在 `WorkspaceSessionEnvelopeFilterTests.LifecycleCloseWaitsForOtherInflightButNotItsOwnEnvelope` 第 209 行等待关闭时超过既有 2 秒预算。旧日志 `ci-306-core-latest.log`、`ci-306-core-attempt2.log` 保留。本次不第三次原样重跑该 run，也不修改超时、覆盖率或生命周期实现；失败根因仍未确定。

正常 merge #312 实际 main `3cd6f83202ea0977d690ad71cf7dff25595e4f02`，固定生产候选为 `689d51f5d4049ba680d22a8c5d48273641b1f1c5`。无冲突，保留 main 的 Formula 编译缓存初始化和本 PR 的 preview/apply 成对注册及 apply 写门禁。上述生命周期测试、filter 与 session manager 在同步前后无差异，因此这次同步没有修复旧超时路径。本轮未修改依赖或 lock，复用既有环境；main 交界两轴审查均为 0 个确定问题。

以下均在该固定生产候选执行一次，日志目录为 `build/qa/mutation-owner-main-312/`：

| 命令 | 结果 | 日志 |
|---|---|---|
| `uv run --frozen --no-sync python contracts/v2/generate_mutation_product_oracle.py --check` | EXIT 0 | `oracle-check.log` |
| `uv run --frozen --no-sync python contracts/v2/product_rpc_capability_policy.py --check` | EXIT 0 | `policy-check.log` |
| `uv run --frozen --no-sync python contracts/v2/product_runtime_inventory.py --check` | EXIT 0 | `inventory-check.log` |
| `uv run --frozen --no-sync python -m pytest tests/contract/test_mutation_product_oracle.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/backend/adapters/test_pocketbase_product_rpc.py -q --no-cov` | 111 PASS，1.81 秒 | `python-related.log` |
| `go test -race ./internal/app -run '^(TestMutationProduct\|TestMutationExistingREST)' -count=1 -timeout=5m` | PASS，29.037 秒 | `go-mutation-race.log` |
| `go test -race ./internal/contracts/productcapabilities ./internal/productrpc -count=1` | PASS，1.261 / 1.693 秒 | `go-routing-race.log` |
| `go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1` | PASS，11.801 秒 | `go-process-race.log` |
| `go vet ./internal/app ./internal/contracts/productcapabilities ./internal/productrpc ./cmd/vibetable-pb` | EXIT 0 | `go-vet.log` |
| `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore` | 1099 PASS、1 既有 skip，26 秒 | `desktop-full.log` |
| `uv run --frozen --no-sync python scripts/build_next.py` | 完整构建 EXIT 0，无跳过步骤 | `product-build.log` |

Go 命令在 `sidecar/` 执行；表格中的正则竖线是 Markdown 转义，实际命令使用 `|`。Desktop 的 skip 为原有 `ActivationPointerLinkIsRejectedAndRetained`，本轮未修改其行为。本轮未运行完整 Python 覆盖率、Web 覆盖率、全部 Go 测试或 .NET solution 覆盖率；上述聚焦与单项目检查不替代完整 fresh CI。

实际包 `dist/VibeTable.Next` 的 sidecar build-info 为 `0.5.1 / 689d51f5d404`。运行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 05-formula-lifecycle --scenario 28-relation-delta-preview`，报告 `build/qa/product-e2e/20260909T032034Z/product-e2e-report.json`，运行日志 `build/qa/mutation-owner-main-312/product-e2e.log`，结果 2/2 PASS、0 failed、0 skipped。

| 场景 | 耗时 | 断言 | bridge roundTrips |
|---|---:|---:|---:|
| S05 Formula 生命周期 | 6775 ms | 7 | 47 |
| S28 Relation preview/标签 | 6134 ms | 10 | 60 |

两场景 Node/Host exit 0，pageErrors、bridge failures、acknowledgedFailures、pending 均为 0；成员和后代为空，端口释放、owner lease 与最终清理通过。四组件 freshness 全通过。已查看报告目录下两张场景截图；`28-relation-delta-preview/28-relation-delta-preview.png` 显示 `AUTHOR-UPDATED`，S05 截图仅记录场景最终界面，生命周期断言以报告为准。

本地全部进程已终态。当前候选仍需新 head 的 fresh required、squash 和合并后 main CI/CD；这次通过不覆盖或解释两次旧 CI 失败，也不扩大为全部故障注入或完整发布资格。


## 同步 #319 实际 main 后的资格

正常 merge `GitHub/main` 的 `25b260394a0a01e8432d23fa3d1a6e8b9b65922f`，固定生产候选为 `d27fc82f7c7185877e8e6c1ce95a9516ac5a90ba`。八处冲突均来自严格 owner/注册清单及 fixture：保留 main 的 `relation.inspectPair` 与本分支的 `mutation.preview/apply`，按排序同步完整清单到 22 Go / 80 Python / 2 Host。权威 inventory 自动合并后运行 `product_rpc_capability_policy.py` 生成目录，未手改派生文件追认结果。两个手工冲突 HTTP fixture 保留双方不得调用的占位注册；mutation fixture 原有目录自动注册机制保持不变。

本次没有修改 mutation Kernel、写门禁或 Host fallback 行为。preview/apply 共用同一 Go Kernel，apply 仍在 `mutation.apply` 业务写门禁中使用 `IdempotencyKey`；Request/PreviewResult 没有 preview 签发的 plan token，不把其他能力的 token 迁移计入本意图。真实 HTTP 契约确认 preview 不改变权威状态，apply 产生一份 gate proof，重放不重复记录、revision、审计、事件或幂等效应。Host composition 与 disposed forwarder 测试确认默认 Go 路由、Go 失败无 Python fallback，写入只调用旧 forwarder 一次，替代端和 Python 均零次。

日志统一保存在 `build/qa/mutation-owner-main-319/`。复用既有 `.venv`、工具和 Node 依赖；后续 Python 命令显式设置当前工作树为 `PYTHONPATH`，`python-source.log` 确认 `backend.__file__` 来自本工作树。没有修改依赖、lock、CI、覆盖率或超时。

| 验证命令 | 结果 | 日志 |
|---|---|---|
| `uv run --frozen --no-sync python contracts/v2/generate_mutation_product_oracle.py --check` | EXIT 0 | `oracle-check.log` |
| `uv run --frozen --no-sync python contracts/v2/product_rpc_capability_policy.py --check` | EXIT 0 | `policy-check.log` |
| `uv run --frozen --no-sync python contracts/v2/product_runtime_inventory.py --check` | EXIT 0 | `inventory-check.log` |
| `uv run --frozen --no-sync python -m pytest tests/contract/test_mutation_product_oracle.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc_coverage.py tests/backend/adapters/test_pocketbase_product_rpc.py -q --no-cov` | 112 PASS，1.36 秒 | `python-related.log` |
| `uv run --frozen --no-sync python scripts/automation_project.py python-quality` | Ruff/Pyright/mypy PASS；1833 PASS、1 既有 skip，73.83 秒，覆盖率 91.42% | `python-quality.log` |
| `go test -race ./internal/app -run '^(TestMutationProduct\|TestMutationExistingREST\|TestRelationInspect)' -count=1 -timeout=5m` | PASS，19.731 秒 | `go-mutation-race-fixed.log` |
| `go test -race ./internal/contracts/productcapabilities ./internal/productrpc -count=1` | PASS，1.241 / 1.645 秒 | `go-routing-race.log` |
| `go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1` | PASS，10.274 秒 | `go-process-race.log` |
| `go test -race ./internal/app -run '^(TestFieldSettingsDescribeProductHTTP\|TestRelationSearchProductHTTP)' -count=1 -timeout=5m` | PASS，15.412 秒 | `go-conflict-fixtures-race.log` |
| `go vet ./internal/app ./internal/contracts/productcapabilities ./internal/productrpc ./cmd/vibetable-pb` | EXIT 0 | `go-vet.log` |
| `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore` | 1105 PASS、1 既有 skip，23 秒 | `desktop-full.log` |
| `uv run --frozen --no-sync python scripts/build_next.py` | 一次完整构建 EXIT 0，无跳过步骤 | `product-build.log` |

Go 命令在 `sidecar/` 执行，使用已有固定 Go 1.27 / w64devkit，命令级 `CGO_ENABLED=1` 和 `CC`；表格竖线为 Markdown 转义。第一次 race 因当前命令未启 CGO 被工具拒绝，日志 `go-mutation-race.log`；随后一轮因本次多补了目录已自动注册的 `relation.inspectPair` 而失败，日志 `go-mutation-race-cgo.log`，并保留其中的 TempDir 清理错误。去除该多余补入后运行上表，不放宽重复注册或清理检查。Desktop skip 仍是 `ActivationPointerLinkIsRejectedAndRetained`。正常 merge 提交的 Ruff format/check、version consistency、package contract hooks 均 PASS，日志 `merge-commit.log`。

实际包为 `dist/VibeTable.Next`，sidecar build-info 为 `0.5.1 / d27fc82f7c71`，四组件 freshness 全通过。对同一包执行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --scenario 02-all-field-schema --scenario 31-relation-pair-inspection`，运行 `20260909T121959Z`，2/2 PASS、0 failed、0 skipped。S02 验证 owner 迁移后的普通编辑/撤销；S31 验证本次 main 新增关系检查的目录和路由交界，不重复扩展其他已取得历史资格的场景。

| 场景 | 耗时 | 断言 | bridge roundTrips | 已确认预期失败 |
|---|---:|---:|---:|---:|
| S02 普通编辑/撤销 | 14062 ms | 18 | 161 | 1 |
| S31 关系完整性检查 | 7544 ms | 12 | 68 | 0 |

两场景 Node/Host exit 0，pageErrors、未确认 bridge failures、pending 均为 0；成员和后代为空，端口释放、owner lease 和最终清理通过。报告 `build/qa/product-e2e/20260909T121959Z/product-e2e-report.json`，日志 `product-e2e.log`。已检查两场景截图；S31 显示双端 101/1 行完成检查及“扫描覆盖完整不代表关系健康”。S02 截图记录最终分组界面，编辑/撤销结果以断言为准。

本轮未重跑完整 Web 覆盖率、全部 Go 测试或 .NET solution 覆盖率；完整门禁仍由当前 head 的 fresh `required` 验证。本地运行均已终态，后续生产源码无改动；新 head 的 push、fresh CI、squash 与 main CI/CD 由主代理继续，本节不把旧 CI 成功复用为新 head 的验收。
