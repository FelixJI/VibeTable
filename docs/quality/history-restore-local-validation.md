# 历史恢复 Product owner 的本地候选资格（2026-09-09）

候选 source 为 `2fd495d78bf4cd5fc021d8a1abff150b6705eed5`，执行一次 `uv run --frozen --no-sync python scripts/build_next.py --release`，全部构建阶段及桌面自更新 smoke 通过。包为 `dist/VibeTable.Next`，Sidecar 元数据 commit 为 `2fd495d78bf4`。随后以同包运行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/history-restore-product --scenario 07-attachment-history`。

首轮 `20260909T134137Z/product-e2e-report.json` 失败：Product 恢复已成功，但新增断言把单附件字段值误当数组。默认 `maxFiles=1` 的字段返回单个存储名字符串；只修正 E2E 的类型断言及权威列表比对取值，未改生产代码或重建包。复验报告 `build/qa/history-restore-product/20260909T134336Z/product-e2e-report.json` 为 1/1 passed、0 failed、0 skipped，场景耗时 9.152s。预览与应用的四项 Product 断言通过，未预期 bridge failure 和 pending 均为 0；Host 正常退出码 0，进程组与后代为空，端口已释放。构建及两轮场景日志保留在 `build/history-release-build.log`、`build/history-s07.log` 和 `build/history-s07-correction.log`。

这份局部证据同时覆盖保留的 Workspace V2 UI 恢复与新增 Go Product 公开桥接恢复，不关闭正式 main 的 manifest gap/changed，也不替代当前提交的完整 PR CI。

源码验证另保留原失败边界：`build/history-go-tests.log` 的完整五包运行中，app 的 `TestReconcileProductHTTPRejectsClosedParamsAndPreservesSourceFailure` 在 TempDir 清理时报告目录非空，未修改这个无关 fixture；productcapabilities 的旧 owner 数期望也失败，随后按生成 catalog 的真实 22 项 Go owner 更新，最终 app/productrpc/productcapabilities 聚焦测试通过（`build/history-final-focused-tests.log`）。原 workspacev2、audit 和 productrpc 包通过；这不表示原完整五包运行成功。Go vet 及 31 项冻结 Python parity oracle 均通过。

Python 完整质量日志 `build/history-python-quality.log` 中 Ruff format/check、backend Pyright/mypy 和覆盖率 91.43% 通过，pytest 为 1831 passed、1 skipped、2 failed。失败的 `test_node_runner_inventory_matches_the_product_scenario_manifest` 和 `test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime` 均因本 worktree 缺少 Web 的 node_modules。核对 lock 一致并复用现有依赖后，以 `uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -k "node_runner_inventory_matches_the_product_scenario_manifest or bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime"` 精确复验，2 passed、109 deselected、7.15s，记录于 `build/history-python-node-correction.log`；未重新宣称原完整质量入口成功。
