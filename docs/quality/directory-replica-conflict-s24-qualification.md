# S24 目录副本冲突资格

当前状态：c045 源码已独立双轴通过并完成完整构建；04ab 导航修复的增量双轴及完整 Python 通过。同包第二次 S24 在 resolve 因合法空文件历史被生产冲突 reader 拒绝而失败。本轮修复 Go 两个候选入口，真实 FilesystemRemote／Manager 回归及相关 Go 包通过；待新的独立双轴、完整新包与 S24 验证。S24 尚未取得产品通过资格，fresh CI 尚无本批结果。

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

旧 S24 的 155 fixture PASS 与 84 Node PASS 仅为保留的历史结果，不能计入当前完整资格。在首次 c045 源码交接时未执行完整 Python 质量入口、完整构建或真实副本操作；后续构建和首轮真实失败见下节，没有新的性能结论。普通启动移除 Python 的 L6–L10 工作不包含在本批。

- 新增 resolve／重启失败夹具后，组合首次为 220 PASS、1 FAIL（9.53s，`fixtures-final-before-sync.log`）：测试错误要求右 Host 关闭失败后，剩余左 Host 仍正常关闭；实际 ExitStack 按既有规则中止清理左 Host。已改为精确验证右侧正常关闭失败、左侧 abort 清理和双方 scope 已关闭，未修改生产生命周期。
- 旧 WIP 保存检查：完整 HEAD→工作树 patch、原 index patch、12 路径状态和四个未跟踪文件逐字节一致；`migration/preservation-result.txt` PASS。

## History 主干同步

首个完整场景提交为 `92c5e14e9e3253f3342251be482bc8cf6076d05c`。随后正常合入 main `a3ca78b9181a529d978f9fba46586fbb924ecada`（PR324），保留 History Go owner、真实 S07 Product 恢复断言及旧报告不覆盖新增 S07 的 changed 状态。冲突仅在两份文档：性能页同时保留 S24 gap 和 S07 changed；能力索引按合并后的 manifest 重生成。未改写或复制生产 History 路径。

关闭失败夹具纠正后，`uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -k 'transports_only_closed_successful_stages or replica_success_requires_complete_clean_bridge_diagnostics' --no-cov -q`：18 PASS，1.55s，`late-close-focused.log`。同步后的最终组合结果见下节；此处不替代真实产品 S24。

## 首次场景源码端点（生产修复前）

固定组合源码 `3b5c2a11eeb6d3f79e234080d406b5cb49f45843`，已包含 main `a3ca78b9`。相对该 main 的 backend、desktop/src、desktop/web-grid/src、sidecar 与 ownership inventory 无差异，当时仅改变产品场景、测试编排和文档；后续真实产品运行发现的 Go 修复见最后一节。

`uv run --frozen --no-sync python -m pytest tests/e2e/test_directory_replica_payloads.py tests/e2e/test_packaged_replica_hosts.py tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py --no-cov -q`：**239 PASS，10.53s，EXIT0**，`fixtures-main-final.log`。包含旧失败的6项 bridge 完整性回归、阶段运输与晚期关闭/重启失败、共享生命周期、固定 Node 行为契约（含新冲突选择／重启检查模块）、能力索引与 History owner 接线契约。

Ruff format/check、能力索引生成一致性、Web runner 语法与 `git diff --check` 通过；两次源码／同步提交正常 hooks 均通过。当前结果是定向源码资格，不是完整 Python 质量入口或发布资格。该源码端点交接时独立 Standards/Spec、完整构建、同包 S24 与 fresh CI 尚待完成；后续状态见下节。首次 RED、收集失败及晚期夹具失败日志全部保留，没有用后续通过抹去。

## 首轮完整包与真实 S24 失败、场景导航修复

固定源码 `c0452afd9b9038ca9a5c8bf839da25b240b9c299` 的 Standards／Spec 独立两轴均为 0。root 执行一次完整 `uv run --frozen --no-sync python scripts/build_next.py --release`，session60338 EXIT0，`build-release.log`。随后同包原 runner 执行 S24，session2497 **EXIT1，0/1 passed、0 skipped**；报告为 `build/qa/directory-replica-conflict-s24/product-e2e/20260910T040328Z/product-e2e-report.json`，package audit passed。

seed 完成 14 项断言；fork-left 在 `scenario24` 原7802行等待 workspace-center 60s 超时，0项断言、0 bridge failure、0 pending，后续阶段未运行。原 Host 正常退出：exit0、members/descendants空、ports released、owner lease closed，lifecycle passed。失败报告、截图、trace、stdout/stderr 与原运行现场全部保留。

现场 `fork/hosts/left/24-directory-replica-conflict.png` 显示当前已在首页，顶部为 E2E Forked Replica／临时写入，并有种子表卡。根因不是恢复到了 Tables：

1. `uiStore.ts` 默认 lastWorkspace，`WorkspaceView.vue` 启动决策打开上次工作区；其 workspace-center 渲染条件是显式 showWorkspaceCenter，或无活动工作区且在首页。原 S24 只等待 nav-home 可见，不执行进入中心的动作，因而永久等待隐藏中心。
2. `workspaceSessionUiController.ts:66` 对当前 workspace 卡仅设置 showCenter=false 后返回 true，不发送 workspace.open。即使只修中心导航，原 S24 的公开 open 捕获也不会收到终态。

修复仅在 S24 场景：使用现有工作区切换器打开中心；若“关闭当前工作区”按钮可见，先通过真实 UI workspace.close 并验证 seed workspaceId、closed结果和请求 sessionEpoch，再执行原公开 workspace.open 捕获及准入断言。没有修改生产启动行为、内部 open、超时或重试。

回归执行实际 `scenario24` 与既有中心导航函数，页面夹具模拟截图中的“活动工作区首页”，并保留当前卡片不发 open 的行为；没有启动真实产品或副本运输：

- 固定 Node24.19 `node --test tests/e2e/directory_replica_conflict_ui.test.mjs`：`navigation-red.log` **13 PASS／4 FAIL**，原路径仍等隐藏中心；只补中心导航时 `current-card-red.log` **14 PASS／3 FAIL**，明确因当前卡不发 open。
- 同命令完整修复后 `navigation-final-green.log`：**17 PASS，83.1218ms**，覆盖 seed、fork-left、fork-right、resolve、verify-resolved 的真实导航控制流。
- `uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py -q --no-cov`：**142 PASS，8.47s，EXIT0**，`navigation-python-final.log`，包含固定 Node 契约入口。
- Web runner `node --check` 与 `git diff --check` 通过。仅 E2E 源码与资格记录变化，不需要替换包内产品组件；未重建、未重跑真实 S24。root 后续增量双轴与原包复验仍 pending，不能据本轮夹具通过改写首轮真实 FAIL。


## 同包导航复验失败与合法空文件历史修复

导航修复 `04ab60cdd2d9869734e104f9d62a27cfe635183e` 的增量 Standards／Spec 均为 0；root 完整 Python 质量入口报告 **1916 PASS、1 skip，coverage 91.42%**。这些结果属于生产修复前源码。root 用原 c045 包复验，shell66965 **EXIT1**；`product-e2e-navigation/20260910T041637Z/product-e2e-report.json` 保留。seed、fork-left、fork-right 各14项断言通过；resolve 的关闭／打开身份3项通过后，在 `waitForPublishedReplicaUi` 原7679行等待60s超时，bridge failure／pending 均0；verify-resolved 未执行。

resolve 截图 `product-e2e-navigation/20260910T041637Z/24-directory-replica-conflict/resolve/hosts/left/24-directory-replica-conflict.png` 显示存储页“需要处理”且 release-cache 禁用。只读检查关闭后数据库（确认不存在 WAL 后用 SQLite `mode=ro&immutable=1`）发现两侧公开状态的持久投影均为 advisory／failed／pendingSync=true，冲突表0项；`replica-pending.json` 明确 `replica.verification_invalid`。所以这不是已有冲突造成正常 publish 等待不适用，不能改成“有冲突即可跳过”或放宽超时。

运输后的公开不可变载荷具有完整 `file-state-root` 及所引用 `file-state-head`：files／attachments 都为空、fileRevision=0、明确 historyRoot=""，未生成 filehistory-root。这是只创建表的合法工作区。`frozen_source.go` 正常生产该空 head，普通恢复路径允许它；但 `filesystemConflictCandidate` 原先必须找到任意 filehistory-root，`Manager.snapshotConflictCandidate` 原先也直接拒绝空 historyRoot，导致远端发现及本地 base／local 候选读取都失败。

本轮只修改这两个生产入口并增加小型共享 `conflictFileHistoryReference`：从 file-state-root 的精确 sourceRoot 读取 head；核对名称、格式、workspace、historyRoot／fileRevision 的显式存在性及 snapshot revision 一致。只有 historyRoot 明确为空、revision为0且 files／attachments 都为空时才创建空文件候选；非空历史仍必须读取所引用的正确 history manifest 并验证格式与 workspace。Manager 将同一 head 及必要 history 交给共享候选投影，不再形成两种空历史判定。没有伪造文档、修改运输逻辑、放宽 UI 等待／bridge／workspace 准入、改变公共协调器或生成 oracle。

固定本机 Go1.27.0，在 `sidecar` 执行：

- 首次新夹具调用 `Engine.List` 时遗漏分页参数／第三返回值，编译 EXIT1，`table-only-red.log`；纠正夹具签名后才取得真实 RED，不将编译失败当回归证据。
- `go test ./internal/replica -run '^TestTableOnlyFilesystemRemoteAndManagerConflictCandidates$' -count=1 -v`：旧生产代码的真实远端发现、OpenManager 本地候选、Manager 完整发现三路径均 `replica.verification_invalid`，**EXIT1，0.931s**，`table-only-red-runtime.log`。
- 同命令修复后 **EXIT0，0.910s**，`table-only-green.log`。通过真实 FilesystemRemote 生成并重读 seed→left／seed→right 分叉；Manager 持久保存三份不同表候选及保护 pin，并保持未安装依赖 scanner 时 graph.Complete=false。
- 增补17个缺失／null／不一致 head、revision、history、非空 files／attachments 拒绝子例，以及既有真实非空文件历史的两个入口回归。首次后者把 Manager 断言放在既有夹具删除本地仓库之后，返回 repository.not_found，**EXIT1，1.043s**，`history-boundaries.log`；已将本地断言移到既有删除之前，远端断言仍在独立恢复之后，未修改生产行为来掩盖夹具错误。
- `go test ./internal/replica -count=1`：**EXIT0，1.431s**，`replica-final.log`，包含上述完整边界与既有副本契约。
- `go vet ./internal/replica`：**EXIT0**，`replica-vet.log`。
- `go test ./internal/workspacev2 -run 'Replica|Conflict' -count=1`：**EXIT0，14.961s**，session2008，`workspace-replica-conflict.log`。

所有原运输／GUI失败、包和运行现场保留；本轮未重跑完整 Python、完整 Go、完整构建或真实 S24。由于 Go runtime 已变化，c045 包及前述 Python 结果不能替代新源码的发布资格。交回 root 完整增量双轴后，必须以新包运行同一完整 S24；胜方持久状态、同一冲突、败方恢复预览可达性与 fresh／bridge／cleanup 仍待真实验证。普通 hooks 的本轮结果随提交记录保留。
