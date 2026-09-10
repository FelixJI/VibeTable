# 历史恢复 Product owner 的本地候选资格（2026-09-09）

候选 source 为 `2fd495d78bf4cd5fc021d8a1abff150b6705eed5`，执行一次 `uv run --frozen --no-sync python scripts/build_next.py --release`，全部构建阶段及桌面自更新 smoke 通过。包为 `dist/VibeTable.Next`，Sidecar 元数据 commit 为 `2fd495d78bf4`。随后以同包运行 `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/history-restore-product --scenario 07-attachment-history`。

首轮 `20260909T134137Z/product-e2e-report.json` 失败：Product 恢复已成功，但新增断言把单附件字段值误当数组。默认 `maxFiles=1` 的字段返回单个存储名字符串；只修正 E2E 的类型断言及权威列表比对取值，未改生产代码或重建包。复验报告 `build/qa/history-restore-product/20260909T134336Z/product-e2e-report.json` 为 1/1 passed、0 failed、0 skipped，场景耗时 9.152s。预览与应用的四项 Product 断言通过，未预期 bridge failure 和 pending 均为 0；Host 正常退出码 0，进程组与后代为空，端口已释放。构建及两轮场景日志保留在 `build/history-release-build.log`、`build/history-s07.log` 和 `build/history-s07-correction.log`。

这份局部证据同时覆盖保留的 Workspace V2 UI 恢复与新增 Go Product 公开桥接恢复，不关闭正式 main 的 manifest gap/changed，也不替代当前提交的完整 PR CI。

源码验证另保留原失败边界：`build/history-go-tests.log` 的完整五包运行中，app 的 `TestReconcileProductHTTPRejectsClosedParamsAndPreservesSourceFailure` 在 TempDir 清理时报告目录非空，未修改这个无关 fixture；productcapabilities 的旧 owner 数期望也失败，随后按生成 catalog 的真实 22 项 Go owner 更新，最终 app/productrpc/productcapabilities 聚焦测试通过（`build/history-final-focused-tests.log`）。原 workspacev2、audit 和 productrpc 包通过；这不表示原完整五包运行成功。Go vet 及 31 项冻结 Python parity oracle 均通过。

Python 完整质量日志 `build/history-python-quality.log` 中 Ruff format/check、backend Pyright/mypy 和覆盖率 91.43% 通过，pytest 为 1831 passed、1 skipped、2 failed。失败的 `test_node_runner_inventory_matches_the_product_scenario_manifest` 和 `test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime` 均因本 worktree 缺少 Web 的 node_modules。核对 lock 一致并复用现有依赖后，以 `uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -k 'node_runner_inventory_matches or bridge_recovery_and_workspace_wire_contracts' -q --no-cov *> build/history-python-node-correction.log` 精确复验，2 passed、109 deselected、7.15s，记录于 `build/history-python-node-correction.log`；未重新宣称原完整质量入口成功。

## 进程能力清单的 CI 修正

PR #324 的 head `c4c0db56` 在 CI run `34361869988` 的 core job `102507477291`
失败：`TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart` 仍使用旧的 20 方法
预期，而真实进程已声明包含 `history.previewRestore` 与 `history.applyRestore` 的 22 方法。
这是迁移后遗漏更新的独立进程契约，原 CI 失败保留。

测试现在显式列出 22 个预期方法，并分别严格核对 RPC 方法和 registration 的数量、顺序、
名称，以及每个 registration 的 workspace scope；不从生产生成清单推导预期。
使用既有 Go 1.27.0 执行 `go test ./cmd/vibetable-pb -count=1`（sidecar 目录）通过，
包耗时 4.711s。只有测试和本记录改变，不重建此前已验证的产品包；新 head 仍须 fresh CI。

## 主干同步与身份解码的后续验证

head `7db3498b` 的 CI run `34367022021` 在 core job `102524859501` 失败：
Go authority diff coverage 为 87.50%（7/8），低于既有 90% 门禁。
缺失分支来自严格 wire 解码成功之后的第二次 JSON 解码错误返回，该路径不可达。
提交 `18cabc2e` 让私有 `validateWireIdentity` 返回已验证的 operationId，保留 scope、
workspaceId/sessionEpoch 与 DTO 严格校验；Dispatcher 直接使用这一结果，不重复解析。
回归验证 workspace/global 身份传入 handler，且调用方预置 context 身份不能覆盖 wire 身份。

随后正常合并 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`，History 与 Mutation
共同注册 24 个 Go 方法；由仓库生成脚本更新派生映射，独立进程、descriptor 和 dispatcher
清单严格保留两组入口。Python owner 数量的语义合并遗漏最初造成 1 failed、78 passed：
双方各迁走两个方法，预期应为 78 而非 80。修正独立预期后运行：

```text
uv run --frozen --no-sync python -m pytest tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_workspace_rpc_capability_manifest.py tests/backend/contracts/test_history_contract.py -q --no-cov
```

结果 79 passed（2.14s），原失败和复验分别保留于
`build/history-main-merge-contracts.log` 与 `build/history-main-merge-contracts-correction.log`。
`go vet ./internal/productrpc ./internal/contracts/productcapabilities ./cmd/vibetable-pb`
在 sidecar 目录使用既有 Go 1.27.0 执行通过。身份与恢复聚焦测试命令为：

```text
go test ./internal/productrpc ./internal/app -run 'TestDispatch|TestHistoryRestore|TestHistoryPreview|TestHistoryApply' -count=1 -coverprofile=<本地绝对输出路径>
```

两包通过（0.738s / 3.535s），日志 `build/history-wire-refined.log`。
完整 `uv run --frozen --no-sync python qa/go_coverage.py --go <既有 Go 1.27.0 可执行文件>`
退出 1，记录在 `build/history-go-coverage-final.log`：六个测试在 TempDir 清理时报告目录非空，
涉及 app、workspacesearch、workspacev2，未吞掉错误、加入重试或降低门禁。
失败后单独读取已有 authority profile 的报告为 line 76.66%、branch 63.87%、
diff 94.12%（16/17）；这是诊断指标，不能代替完整入口通过。

上述 runtime 变更及主干同步不由早先 source `2fd495d7` 的包证据覆盖。
当前提交仍需 fresh CI 的完整构建、smoke 与 E2E 门禁，尚未具备合并资格。
