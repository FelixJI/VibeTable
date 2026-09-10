# S24 目录副本冲突资格

当前状态：固定源码 `c2b7c2eaedf11b0be5b58a08ce2704c0228e64b0` 完整发布构建与同包 S24 均 EXIT0，84 条断言通过，8 个 Host 生命周期正常退出并清理。源码修复均已有作者外 Standards／Spec 审查；远端 fresh CI、严格同步、squash merge 及合并后 CI/CD 尚未完成，不声明已进入可信 main。历史失败及其修复证据完整保留于下文。

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


## 第三次新包 S24 失败与完整业务冲突修复

固定源码 `081fa4edf3431e0a97ce7dbea31a7b617975def3` 的独立 Standards／Spec 均为 0，root 完整构建 session15966 EXIT0。同包真实 S24 session90860 **EXIT1**，报告 `product-e2e-empty-history/20260910T045044Z/product-e2e-report.json`。resolve 已到 replicated／pendingSync=false，9项断言通过（含公开 conflict.list、两次真实 inspect 及行绑定），随后唯一目标选择报 found0；旧空历史问题已越过，不能把这次失败归为等待或运输问题。

真实 inspect 中业务表为 `pbc_2168654062`／`t_b91c8638dc04562dbb9b`，而 schema 和场景身份为 `tbl_b91c8638dc04562dbb9b`／`E2E Forked Records`。关闭后只读冲突库显示两组相同 base／replica，local 仅 snapshot ID 与 revision 不同，内容相同。每组原27项包含 users 和执行账本；未变表也因 DatabaseObjectID 指向另一整库快照而进入选择。原截图、完整 inspect 和关闭后的持久状态均保留，不改 selector 为首项、不任选多项。

本修复以 081fa 为父提交，范围为现有 Go 冲突投影、Manager 发现及 whole-table appender：

- `SQLiteProjection.Tables` 保留物理恢复验证，`Candidates` 根据 `vibetable_tables` 的逻辑／物理／显示名映射选出业务表。共享 metadata 从现有 `collectionByNamespace` 经只读 `NamespaceForCollection` 识别；schema／计算依赖／附件定义复用集中 typed dependency 描述，作为现有 `kind=settings` 整表选择。jobs 保留依赖扫描但不作为用户设置；认证、outbox、审计、执行回执、未知内部集合不冒充用户表。
- 公开 itemId/path 为业务逻辑 ID／显示名，metadata 为 `metadata:<namespace>`，schema 为 `schema:<definition>`。公共 DTO shape 不变；公开依赖投影领域 ID，私有完整依赖图仍执行闭包校验。公共 choice 映射回原物理恢复地址；错误 kind、物理地址绕过、非法 both、缺失／重复映射和非法 metadata 引用均失败。
- 表相等复用已有 schema／records／views／attachments 组件，不新增 hash；只移除作为恢复来源的 DatabaseObjectID 对相等性的影响，附件 map 按内容比较，删除与身份仍参与。选择后仍保留原不可变数据库对象和附件来源。
- 同 workspace、相同 base／replica 身份及内容、语义相等 local 的发现，在新 pin 创建前复用原 set；prepared plan、状态、revision、pins、receipt 不改写。不同源或实际 schema／records／views／attachments／settings／files／delete 变化不折叠；stale replan 不复用。
- 真实公共 apply 回归还暴露两项原 staging 缺陷：空附件 map 经持久化变 nil 后被 reflect 拒绝；PocketBase ImportCollections 自动改写 `_collections.updated`，导致导入后的 schema component 与来源不符。四处 staging/CAS/mixed 检查统一使用完整内容比较；原导入事务恢复已验证 config 的 created/updated，保留选中来源及未选择本地 schema，不忽略 schema 字段、不改组件摘要算法。

源码验证均使用固定 Go1.27.0，在 `sidecar` 运行，日志目录仍为本页既有证据根：

- 表内容回归旧实现 RED：`table-content-red.log`；真实 Manager 同内容新 snapshot 重复发现 RED：`duplicate-discovery-red.log`。初次修复因持久化空 map／nil 差异仍 FAIL，`content-and-discovery-green.log`；语义修正后 `content-and-discovery-green-final.log` 通过。
- 三方夹具改为真实 PocketBase 完整 migrations、标准业务 schema 映射、正常 OnTerminate→Reset 和生产等价 VACUUM INTO 快照。原裸表夹具、错误复制 WAL 模式 DB、漏建生产 mutation receipt 表的失败分别保留在 `business-projection-first.log`、`business-pb-first.log`、`business-pb-snapshot.log`，不当作产品 RED；最终真实 FilesystemRemote／Manager 定向 PASS1.527s，`business-pb-current.log`。
- 新公共链夹具的 wire sequence／错误方法名首轮失败保留于 `business-public-apply-first.log`、`business-public-apply-sequence.log`、`business-public-apply-method.log`；修正夹具后真实 apply_unproven RED 在 `business-public-apply-current.log`。`business-public-stage-diagnosis.log` 精确定位空 map 持久化；`business-public-stage-map-fix.log` 和 `business-public-schema-diagnosis.log` 定位导入 updated。恢复 ID 断言首次未按原排序，`business-public-apply-preserved-schema.log` 保留；后续断言独立排序精确集合。
- `go test ./internal/conflict ./internal/replica -count=1`：**EXIT0**，分别0.836s／1.983s，`business-conflict-replica-suite.log`。包含实际表内容变更负控、缺失／重复／错误 schema 映射、共享 metadata 非法 JSON／未知引用、正常空／非空文件历史、真实三方冲突及同内容重复发现。
- `go test ./internal/replica -run '^TestEquivalentDiscoveryPreservesPreparedPlanAndRootPins$' -count=1`：**EXIT0，0.749s**，`business-plan-lifecycle.log`。真实 Engine prepared plan、原 set 与 pins 精确保留；不同状态及来源负控另在 conflict 包覆盖。
- `go test ./internal/workspacev2 -run 'Test.*(Conflict|Replica)' -count=1`：**EXIT1，17.500s**，session93550，`business-workspace-contracts.log`。唯一失败为既有 `TestConflictExternalExpectedSettingsCASRejectsPostPreviewEdit` 的 TempDir RemoveAll“目录非空”，其他契约无失败；早期 `business-projection-first.log` 同样保留既有附件 fault 测试 TempDir 清理失败。不重跑整组求绿，不更改清理 fixture／重试。
- `go test ./internal/workspacev2 -run '^TestConflictBusinessAndSharedSettingsPublicApplyPreservesRecovery$' -count=1`：最终 **EXIT0，3.138s**，`business-public-retained-settings.log`。通过真实 normalized schema／PB 快照→公开 inspect 精确业务表+共享 settings 两项→三类非法 preview 拒绝→合法 preview/apply；胜方两值均恢复、未选 schema/settings 和本地执行账本原样保留。返回双方 recoverySnapshotIds 的精确集合，败方公开 snapshot restore preview 成功，原不可变数据库仍有败方业务值和 settings。中间通过日志一并保留。
- `go vet ./internal/conflict ./internal/replica ./internal/workspacev2 ./internal/metadata`：**EXIT0**，`business-vet.log`；gofmt 和 `git diff --check` 通过。

本轮未修改 Web／Python／公开 DTO 生成器，未重复完整 Python／Node、完整 Go、发布构建或真实 GUI；原 1916 Python／1 skip 仍只属于04ab。旧 WIP、三次真实失败及各包现场不动；本轮 Go runtime 修复必须新包资格，081fa 包不能覆盖。交回 root 独立双轴后，再以原 runner 完整 S24 证明唯一冲突、胜方重开持久值、败方恢复预览、fresh／bridge／cleanup；本页不把源码通过写成产品通过。


## 业务投影修复后新包失败与恢复快照公开发布修复

root 将业务投影修复正常同步到 main `9fa626a13840830037bcb82eaddc0adf8617075b`，固定源码 `3cfafc674f287fcee2d17b9addf634a964d06a1e`，完整构建 session28465 **EXIT0**。同包原 runner 的 S24 session11828 **EXIT1**，报告保留在 `build/qa/s24-business-projection/20260910T055137Z/product-e2e-report.json`。resolve 的 22 项断言中，业务 tableId／唯一 choice、preview／apply／胜方值，以及第一个恢复来源的公开 verified 列表和真实 UI preview 均通过；第二个返回的 `ba668aaf-86b7-4676-a8ae-77761d8561b7` 未公开列出，verify-resolved 未取得通过资格。前三轮真实失败、包、截图、trace、stage-result 与本轮现场均未覆盖或重跑。

只读重放 `24-directory-replica-conflict/resolve/hosts/left/stage-result.json` 精确核对两个返回 ID：公开 snapshot.list 的 nextCursor 为 null、共5项，只有 `80488238-9907-4db1-abd7-93e99f1d323b` 存在且 verified。关闭现场确认无 WAL 后，以 SQLite `mode=ro&immutable=1` 读取 catalog／conflicts：远端 ba668 来源未登记到本地 catalog；已应用冲突仍指向该远端来源，但临时 RootPinIDs 已清空。这不是分页、integrity 过滤或 UI 选择器错误。

生产根因是 `Manager.installRecoveryBundle` 只导入远端对象和 manifest；发现过程没有向本地 snapshot catalog 发布远端恢复来源，apply 却直接返回 plan 中的原远端 SnapshotID。远端 snapshotSequence=3 还与本地 sequence=3 碰撞，因此不能直接把原 Record 插入有本地序号唯一约束的目录，或篡改原 manifest／seal。

本轮实现保持公开 DTO 和场景门禁：

- `RecoveryPublisher` 具名依赖接入实际 Runtime。在候选验证后、冲突临时 pin／记录建立前，保护本地及远端两个来源。通过既有 `snapshot.Coordinator`、协调 capture 与导入冻结源发布新的本地 pinned protection snapshot；原远端身份／内容不改，本地 catalog 保存 SourceWorkspaceID／SourceSnapshotID 以及原 source ManifestID 的精确绑定，不增加摘要算法或第二目录权威。
- 同一 catalog 持久化 `localRecovery`。`Record.IsLocalHead`／`LatestLocalRecord` 是统一判定，应用到 Durable／Memory Last、自动捕获及启动高水位、Manager 发现／排队／队列消费、selected-files 基线和 one-shot 空目录判断；公开 List、verified、previewRestore 与 retention 保留这些恢复副本。恢复副本不成为新的本地分叉或同步 publication。
- apply 从持久目录解析实际可见、verified 且有无到期时间 pin 的恢复 ID。未发布、已删除、未保护或无效来源均拒绝，不再返回任意远端 ID。相同来源重启后复用实际本地 ID，不依赖内存映射；不同来源不折叠。永久保护由已发布的目录记录拥有，冲突临时 pin 释放不影响恢复。发布失败沿既有 Coordinator 释放未发布 pin；若恢复记录已经发布而后续冲突存储失败，该可公开恢复记录仍是可重用的持久状态。
- package import 与目录恢复共用验证后 bundle→importedSnapshotSource 转换；现有 package 输入校验、workspace 重写与 receipt 行为不放宽。没有改 UI selector、等待时间、bridge／cleanup、transport 或恢复验收范围。

新增回归的传输端仅提供已经物化的外部 checkpoint；实际独立 DurableCatalog 捕获与本地 sequence 碰撞、Manager、PocketBase 表／设置选择与应用、公开 Dispatcher snapshot.list／previewRestore、Runtime 重开均运行真实实现。既有真实 FilesystemRemote 三方运输回归同时保留，增加 publication 失败不写冲突／不泄露临时 pin 的边界。已有 PB receipt 重启夹具原用字符串 local／replica 充当快照 ID，现改为真实受保护快照，原进程丢失、PB receipt、同 revision 恢复断言全部保留。

固定 Go1.27.0，在 `sidecar` 执行；本轮证据目录为 `build/qa/s24-recovery-publication/`：

- `go test ./internal/workspacev2 -run '^TestConflictBusinessAndSharedSettingsPublicApplyPreservesRecovery/foreign-catalog$' -count=1 -timeout 120s`：旧生产代码真实 **EXIT1，3.844s**，唯一功能失败为 returned recovery snapshot 未公开列出且 verified，`red.log`。
- 首次修复已使全部公开 list／preview 通过，但旧测试仍要求原远端 ID，**EXIT1，6.815s**，`first-fix.log`。测试随后改为精确验证两个实际公开 ID、源 workspace／snapshot／manifest／database 绑定及本地 ID 唯一性，未改成数量或任选项检查。
- `go test ./internal/workspacev2 -run '^TestConflictBusinessAndSharedSettingsPublicApplyPreservesRecovery' -count=1 -timeout 120s`：首次双 ID／重启完整回归 **EXIT0，7.266s**，`restart.log`。后续补不同 source、自动捕获、selected-files 和失败 pin 负控，以最终组合为准。
- 中间 `snapshot-replica-initial.log`／`workspace-initial.log` 保留 Trigger 枚举与 string 比较的编译失败；`snapshot-replica-second.log` 保留工作目录写错导致修正未生效的失败。修正后 `snapshot-replica-third.log` 暴露两个 Manager 夹具缺新 publisher 接线；`workspace-second.log` 暴露旧公开 apply 重启夹具伪 snapshot ID，均已按真实接口修正，没有将这些失败当作根因 RED。
- `go test ./internal/snapshot ./internal/replica -count=1 -timeout 120s`：**EXIT0**，snapshot **0.757s**、replica **2.061s**，`snapshot-replica-final.log`。包含原真实 FilesystemRemote、恢复发布失败 pin 清理及既有副本契约。
- `go test ./internal/workspacev2 -run 'Conflict|Replica|Snapshot.*(Import|Package)|Import.*Snapshot' -count=1 -timeout 180s`：**EXIT1，25.895s**，`workspace-final.log`。所有功能断言通过；失败仅为新 foreign-catalog 回归及既有 `TestRuntimeReopensAndResumesConflictAtPocketBaseReceiptRevision` 的 TempDir RemoveAll 报 coordination 目录非空。两者均保留正常关闭路径；没有加 retry、删除检查或重复该组合求绿，也不把该入口写成 PASS。
- `go vet ./internal/snapshot ./internal/replica ./internal/workspacev2`：**EXIT0**，`vet.log`；gofmt 与 `git diff --check` 通过。除随后纯排版外未更改生产行为。

本轮未执行完整 Python／Go、完整构建、GUI 或真实目录副本操作，未 push／建 PR。以上源码结果不能覆盖原包真实 FAIL，也不证明败方实际 restore 执行或双端最终收敛；本场要求的双恢复来源公开列表、真实预览与 resolve 重启证据，仍须后续新包 S24 完整验证。此次普通 hooks 结果由提交日志记录，原 TempDir 失败边界保留供独立审查。

独立增量审查（3cfafc67..58c74742）：Spec 0；Standards 指出测试失败路径缺少关闭保障。现恢复 defer 清理，成功路径提前关闭后置 nil，业务断言不变；复核 Standards 0。相关双来源／重启回归再次执行 EXIT0，8.031s，日志 build/qa/s24-recovery-publication/review-cleanup.log。该新修复不用于抹除此前组合 TempDir FAIL；新包 S24 仍待验证。

## 关闭期查询修复与最终候选资格

`c2bf90d8` 新包的 S24 曾在 fork-left 关闭工作区期间出现无 requestId 的 `operation.failed`，messageLength 为 50；包与业务断言通过不覆盖此失败。真实 Host 链用可控时钟复现：通知式 query 未持 workspace 租约，250 ms 防抖跨过关闭边界后触发已关闭会话拒绝。该路径 RED，相关联 requestId 路径通过。

`c2b7c2eaedf11b0be5b58a08ce2704c0228e64b0` 将 query/cursor 统一纳入 scope 租约与取消，过代结果不发布，当前成功、真实失败及追加页通知保留。`dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release --no-restore --filter 'FullyQualifiedName~HostProductRpcInvokerTests|FullyQualifiedName~GridRequestControllerTests|FullyQualifiedName~GridStateCoordinatorTests|FullyQualifiedName~ProductWorkspaceControllerTests'` 对应作者记录的相关 Host/Grid/ProductWorkspace 组 76/76 PASS；正常 hooks 通过。独立 Standards、Spec 分别审查 `c2bf90d8...c2b7c2ea`，均 0 项确定发现。

在该固定源码复用锁一致的环境执行一次：

- `uv run --frozen --no-sync python scripts/build_next.py --release`：EXIT0；日志 `build/qa/s24-query-lifetime-build-release.log`。日志中的预期 updated-crash `0x80131623` 不改变完整命令成功结论。
- `uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/s24-query-lifetime --scenario 24-directory-replica-conflict`：EXIT0；报告 `build/qa/s24-query-lifetime/20260910T083912Z/product-e2e-report.json`，1/1 PASS、0 FAIL、0 SKIP。
- seed、fork-left、fork-right、resolve、verify-resolved 共 84 条断言通过；四组件 freshness 均通过，bridge failures/pending/acknowledged 均 0、pageErrors 0，无缺失的阶段诊断。
- seed/fork/resolve/reopen 的左右 Host 共 8 项生命周期均 exitCode 0，owner lease cleanup passed、errors 0、stableHandleClosed true。

该候选证明本节声明的真实目录副本冲突、公开恢复预览和 Host 重启可达性范围；不扩大为右端再次同步收敛、云盘断网恢复或实际执行败方恢复。最终远端资格仍待 fresh CI 及合并后 CI/CD。

## PR #333 CI 失败与 Preset 关闭代际修复

source `af7442d8` 的 CI run `34457594963` 中，S24 seed 通过，fork-left 已通过9项业务断言，包括关闭保护和 epoch 5 重新打开。实际终止点是 `waitForPublishedReplicaUi` 等待释放缓存预览按钮启用60秒超时；这不是之前无 requestId 的查询失败。证据为 resilience lane 的 `20260910T091449Z/24-directory-replica-conflict/24-directory-replica-conflict-result.json` 和同轮 `product-e2e-report.json`。

该次 CI 的 lane 归档只保留场景汇总结果，未保留 S24 seed/fork/resolve/reopen 的左右 Host 阶段诊断。现仅为 `24-directory-replica-conflict` 的 QA 失败证据保留既有文本日志白名单，以及每个已有阶段 Host 的 `stage-result.json`、`lifecycle.json`、`readiness.json`、场景结果、trace 和截图；同时保留 `_runtime/24/{left,right}/host` 的 trace 与既有 backend/PocketBase 日志白名单。其他场景即使有同形目录也不扩展归档。不会递归复制数据库、controls、用户数据或包。该改动只补齐未来失败的可审计证据，不能据此判断或解决按钮 60 秒超时根因。

另有一个在途 `preset.list`：09:33:49.851Z 选表，49.852Z 发出请求，51.186Z 开始 `workspace.close`，51.224Z 返回 `workspace.session_stale`（requestId `rmtvbye8s-33-971e53b0-9f08-4b2a-ad85-fa13ca5936e2`）。该时序符合 Host 会话退役取消请求的路径，不能据此认定它造成复制超时。按钮是否启用取决于 busy、isTransitioning、pendingSync 和 replicaVerified；当前下载的结果没有最终四项状态、重开后的 replica.changed 载荷及 worker 发布日志，尚不能区分发布失败和 UI 投影问题。Preset 迁移 Go 的 PR #327 合并本身也不能证明这次失败已修复。

独立复现发现 `presetViewController` 清空 currentTable 时没有使请求代际失效，旧 list 拒绝会继续修改已清空的呈现错误状态。本次只在表选择变化时推进代际，让关闭、切表和重开同表遵循相同生命周期；Host 取消终结、bridge 错误诊断与 S24 门禁均保持原语义，当前请求的真实错误仍显示。

固定 Node 24.19.0、仓库锁一致依赖执行：

- RED：仓库根运行 `node desktop/web-grid/node_modules/vitest/vitest.mjs run --root desktop/web-grid src/workspace/presetViewController.test.ts`。旧生产实现 **EXIT1，1 failed / 9 passed**；关闭到 null 后旧请求拒绝仍调用 presets.fail，明确在新断言失败。
- GREEN：仓库根运行 `node desktop/web-grid/node_modules/vitest/vitest.mjs run --root desktop/web-grid src/workspace/presetViewController.test.ts src/services/presetVersionService.test.ts src/bridge/presetVersionBridgeContract.test.ts`，**EXIT0，3 files / 15 tests passed**。覆盖关闭后的旧拒绝、切换其他表、重开同表、当前列表错误和正常当前列表成功。
- 类型检查：在 `desktop/web-grid` 运行 `node node_modules/vue-tsc/bin/vue-tsc.js --noEmit`，**EXIT0**。

本次未执行构建、GUI 或重跑 CI；这项呈现代际修复不消除原 CI 的 bridge 取消记录，也不证明复制按钮超时已解决。该超时及 fresh CI 资格继续保持未解决状态。
