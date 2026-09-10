# S24 目录副本冲突资格

当前状态：四阶段与解决后正常 Host 重启检查已实现，源码待独立双轴审查；完整包、真实 S24、fresh CI 均未执行或尚无当前结果。夹具不能代替产品资格。

## 完整意图与来源

本批从远端 main `58032b97043c2bba80a8eb1f65ae2906797e3251` 建立独立分支，迁入旧 S24 工作树基线 `cadf51533ed45c3793c8f75b21025375d4d3f633` 的最终工作树内容。旧工作树的 12 项 staged／unstaged／untracked WIP 原样保留，未 stash、reset、commit 或同步。

新工作树 `build/qa/directory-replica-conflict-s24/migration/` 保留 HEAD→工作树完整 binary/full-index patch、原 index patch、原 HEAD、前后 porcelain 状态和四个未跟踪源文件的副本。三方应用只有性能文档冲突：保留主干 29/29 固定样本并单列新增 S24 gap；能力索引由仓库生成器重建。

计划 A2 要求真实隔离、副本运输、冲突选择和败方恢复证据。旧 S24 manifest 与性能文档明确把恢复声明限定为返回 recoverySnapshotIds 的公开列出及真实 Snapshot UI previewRestore 可达性，本批保留该范围。旧 Workspace 总计划的 advisory 确定性收敛目标仍是更广范围；本场不执行败方恢复，不声称右端最终收敛、云盘 offline/reconnect 或 exclusive writing。

## 实现与拒绝边界

- seed、fork-left、fork-right、resolve 四个核心 Node 阶段保持真实 Workspace Center、Tables、Conflict Center UI；不会通过内部 RPC 制造冲突或直接改本地 authority。
- 两侧固定且独立的 local-data、WebView 数据目录与选择目录；每轮启动独立 logs、readiness、controls、lifecycle。仅双方正常退出成功后复制公开生成的不可变 replica payload，拒绝重叠目录、链接、非空 seed 目标、已有载荷冲突；失败保留部分复制与原始错误，不重开。
- 通过公开 conflict.list 和每个 pending 项的真实 UI inspect 寻找唯一稳定 tableId/path，最后重新选择同一 conflictId；不使用首项作为预期。分页不完整、多个匹配、响应身份漂移和额外 choices 均拒绝。
- preview/apply 绑定同一 planId，query 核对胜方 marker 和稳定行身份；保留 recoverySnapshotIds 并进入实际 Snapshot UI 恢复预览。
- resolve 正常 Host 退出后，以原 runtime 再启动 `verify-resolved` 检查点，验证胜方行与 schema/data revision、同一 conflictId 的 ready 状态，以及同一 recoverySnapshotIds 仍可进入 UI 预览。没有第二轮运输或右端收敛声明。
- 成功阶段缺少完整 roundTrips/failures/pending 数组，或存在未确认失败/pending，均失败。180s 既有 Node 上限保持；超时保留输出、部分结果和原始错误，关闭与报告错误仅附加，不覆盖主因。
- Host 准入复用 main PR320，不复制旧补丁、不放宽 workspace/epoch/lease 或 Go 权威校验。

## 源码验证与失败记录

环境复用锁一致的 shared UV 与 Web 依赖；固定 Node 24.19.0，未下载或修改 lock。所有证据在本工作树 `build/qa/directory-replica-conflict-s24/`。

- `uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -k replica_success_requires_complete_clean_bridge_diagnostics --no-cov -q`：旧实现 **6 FAIL**（全部 DID NOT RAISE），`bridge-red.log`。
- 首次组合 Python 命令错误指定不存在的 `tests/e2e/test_packaged_host_lifecycle.py`，收集前 EXIT4，0 项运行；`fixtures-current.log` 保留。
- 纠正后，`uv run --frozen --no-sync python -m pytest tests/e2e/test_directory_replica_payloads.py tests/e2e/test_packaged_replica_hosts.py tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py --no-cov -q`：218 PASS，9.50s，`fixtures-corrected.log`。后续追加解决/重启失败回归及主干同步的最终结果见下节。
- 固定 Node `--test` 执行 directory_replica_conflict_ui、workspace_v2_method_terminal、table_mutation_receipt_capture、bridge_diagnostics_instrumentation 四个 `.test.mjs`：49 PASS，139.5154ms，`node-current.log`；新相邻测试已接入既有 Python 质量入口的固定 Node 测试名单。
- `uv run --frozen --no-sync python scripts/generate_product_e2e_capability_index.py --write` / `--check` 通过；Web runner `node --check` 通过。

旧 S24 的 155 fixture PASS 与 84 Node PASS 仅为保留的历史结果，不能计入当前完整资格。未执行完整 Python 质量入口、完整构建或真实副本操作；没有新的性能结论。普通启动移除 Python 的 L6–L10 工作不包含在本批。

- 新增 resolve／重启失败夹具后，组合首次为 220 PASS、1 FAIL（9.53s，`fixtures-final-before-sync.log`）：测试错误要求右 Host 关闭失败后，剩余左 Host 仍正常关闭；实际 ExitStack 按既有规则中止清理左 Host。已改为精确验证右侧正常关闭失败、左侧 abort 清理和双方 scope 已关闭，未修改生产生命周期。
- 旧 WIP 保存检查：完整 HEAD→工作树 patch、原 index patch、12 路径状态和四个未跟踪文件逐字节一致；`migration/preservation-result.txt` PASS。

## History 主干同步

首个完整场景提交为 `92c5e14e9e3253f3342251be482bc8cf6076d05c`。随后正常合入 main `a3ca78b9181a529d978f9fba46586fbb924ecada`（PR324），保留 History Go owner、真实 S07 Product 恢复断言及旧报告不覆盖新增 S07 的 changed 状态。冲突仅在两份文档：性能页同时保留 S24 gap 和 S07 changed；能力索引按合并后的 manifest 重生成。未改写或复制生产 History 路径。

关闭失败夹具纠正后，`uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -k 'transports_only_closed_successful_stages or replica_success_requires_complete_clean_bridge_diagnostics' --no-cov -q`：18 PASS，1.55s，`late-close-focused.log`。同步后的最终组合结果见下节；此处不替代真实产品 S24。

## 当前源码最终端点

固定组合源码 `3b5c2a11eeb6d3f79e234080d406b5cb49f45843`，已包含 main `a3ca78b9`。相对该 main 的 backend、desktop/src、desktop/web-grid/src、sidecar 与 ownership inventory 无差异，本批只改变产品场景、测试编排和文档。

`uv run --frozen --no-sync python -m pytest tests/e2e/test_directory_replica_payloads.py tests/e2e/test_packaged_replica_hosts.py tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py --no-cov -q`：**239 PASS，10.53s，EXIT0**，`fixtures-main-final.log`。包含旧失败的6项 bridge 完整性回归、阶段运输与晚期关闭/重启失败、共享生命周期、固定 Node 行为契约（含新冲突选择／重启检查模块）、能力索引与 History owner 接线契约。

Ruff format/check、能力索引生成一致性、Web runner 语法与 `git diff --check` 通过；两次源码／同步提交正常 hooks 均通过。当前结果是定向源码资格，不是完整 Python 质量入口或发布资格。独立 Standards/Spec、当前包完整构建、同包 S24 实际运行与 fresh CI 仍待完成；首次 RED、收集失败及晚期夹具失败日志全部保留，没有用后续通过抹去。
