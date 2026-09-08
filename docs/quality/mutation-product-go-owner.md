# mutation 产品 RPC Go owner 迁移资格

此变更将同一 mutation 契约下的 preview/apply 成对迁入 Go，避免预览与应用形成中间 owner 状态。保留已有 mutation Kernel 和 workspace 写门禁，Python import/export/plugin 使用的内部 client 不随产品路由删除。产品目录仍为 103 RPC、7 事件，当前 owner 为 21 Go / 80 Python / 2 Host。

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
