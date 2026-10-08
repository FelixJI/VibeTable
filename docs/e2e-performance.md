# 产品 E2E 性能基线

本页记录真实发布包在 Windows WPF/WebView2 + 按需 Python Worker + PocketBase 进程栈上的可比较基线。它不是浏览器 mock，也不把 20–30 秒的测试等待上限当作性能目标。

## 当前 CI 分片

`data-io` lane 的 `product-e2e-data-io` stage 执行 S23/S24 目录副本、S34/S35 数据互操作、S37–39 公式/计算链与文件组合、S41–44 文件旅程及 S45 常用字段显示旅程；其余场景由 `resilience` lane 的 `product-e2e` stage 执行。两 stage 各保留 1800 秒上限，共用同一候选，并由聚合门禁核对当前 manifest 的完整、不重复覆盖。

分片调整依据为：

- [PR400 的 CI 诊断记录](https://github.com/FelixJI/VibeTable/pull/400)（历史事实，保留）：原标准分片完成 32 场已用约 1779 秒，S23/S24 合计约 356 秒。
- main 成功样本 run 36781713078（完整归属见 [#410 验收记录](https://github.com/FelixJI/VibeTable/issues/410)）：resilience 33 场已包含 S37/S38（两场合计约 217 秒），全程约 1693.9 秒，对 1800 秒预算余量仅约 106 秒。
- resilience 失败样本 run 36792999964（原始失败见 [PR #419](https://github.com/FelixJI/VibeTable/pull/419#issuecomment-5922504924)）：同为 33 场，01–37 已计约 1665.3 秒，机器时长增量累积耗尽余量，S38 执行中被 1800 秒外层预算终止。

因此将 S37/S38 迁入原仅 4 场、约 8 分钟的 `data-io` 分片。以下已发布样本的分片数量与路径保留其原时点事实。

[PR459 的 CI run 37741159454](https://github.com/FelixJI/VibeTable/actions/runs/37741159454)（source `f8606322`）中，`product-e2e` 前 32 场累计约 1715.1 秒，S45 的业务结果与四份截图已完成，但 Host 收尾时被该 stage 的 1800 秒外层预算终止，聚合报告仍为 32/33。相同候选的 `data-io` 11 场全部通过、累计约 1156.4 秒，prepare 中 S45 完整运行约 86.1 秒。因此把完整 S45 分配到既有 `data-io` 分片，当前两分区为 32/12 场；保留 `release.smoke` 预合并旅程、全部断言、截图、零 Worker 门禁与两 stage 的原预算。该分配仍需后续完整 CI 验证，不把本次被截断报告视为通过。

## 当前产品 E2E 证据

- source SHA：`GitHub/main@b17c7a6b7d0c9a22049ae664b7e678a10cc7518c`
- GitHub run：[main CI 36963790989](https://github.com/FelixJI/VibeTable/actions/runs/36963790989)
- 报告契约：`contractVersion=2.0`
- 结果：39/39 passed、0 failed、0 skipped。
- 当前 manifest gap：5（`41-file-document-operations`、`42-file-document-native-operations`、`43-file-revision-leaves`、`44-file-restore-crash`、`45-common-field-display`）。
- 当前 manifest surplus：无。
- 当前 manifest changed：无。

同一 main 候选的 data-io 7 场与 resilience 32 场精确覆盖当时 manifest 的 39 场，两 lane 的原报告全部通过，六 lane 的 commit/source/candidate/资产绑定一致，required 成功。报告内部路径分别为 `lane-evidence/data-io/20261002T043152Z/product-e2e-report.json` 和 `lane-evidence/resilience/20261002T043620Z/product-e2e-report.json`。普通 CD36970691192 成功，正式 Publish 与 attestation 均 skipped；[PR423 合并后证据](https://github.com/FelixJI/VibeTable/pull/423#issuecomment-5946519425)保留核验范围。此样本不代表 #415/#409 全部 AC/GAC 已通过。

覆盖清单：`01-offline-first-start`、`02-all-field-schema`、`03-schema-errors`、`04-json-round-trip`、`05-formula-lifecycle`、`06-relation-fanout`、`07-attachment-history`、`08-stale-conflict`、`09-atomic-import-scale`、`10-sse-reconnect`、`11-plugin-mutation`、`12-backup-consistency`、`13-protection-policy`、`14-document-diff`、`15-workspace-snapshot-package`、`16-dashboard-lifecycle`、`17-interface-lifecycle`、`18-workspace-search`、`19-gallery-lifecycle`、`20-kanban-lane-drag`、`21-calendar-date-move`、`22-timeline-date-move`、`23-directory-replica-recovery`、`24-directory-replica-conflict`、`26-lookup-definition-read`、`27-relation-target-search`、`28-relation-delta-preview`、`29-lookup-source-pagination`、`30-query-snapshot-validation`、`31-relation-pair-inspection`、`32-shared-work-calendar`、`33-host-grid-presentation`、`34-relation-lookup-data-io`、`35-data-io-interoperability`、`36-backend-import-exit`、`37-collection-formula-journey`、`38-calculation-chain-journey`、`39-file-workflow-combination`、`40-file-history-capacity`。

S41–44 尚未取得新最终包的真实 Host 资格：分别补文件排序分页/身份确认/丢失重连、真实 Windows 打开预览拖出、多 autosave/正式分支/非当前叶子恢复激活、真实 Restore 崩溃后的同 UUID 冷恢复。聚焦契约与静态审阅不替代实跑；沿既有 180 秒 driver 和分片 1800 秒预算，能力缺失或证据不足均失败。

## 前一完整样本 2026-09-27（0b，历史）

- 历史 source：main `0bdbc1b`
- 历史 main CI run：36333755042
- 历史报告契约：2.0
- 历史结果：35 场全部通过，失败与跳过均为 0。
- 当时 manifest gap：4（`37-collection-formula-journey`、`38-calculation-chain-journey`、`39-file-workflow-combination`、`40-file-history-capacity`）。
- 当时 manifest surplus：无。
- 当时 manifest changed：3（`05-formula-lifecycle`、`14-document-diff`、`18-workspace-search`）。

在本节历史时点，新增登记的 39、40 尚未取得真实运行资格，不能引用本节历史样本作为其证据；分区随当前 manifest 生效：39 随计算链族进入 data-io lane，40 默认进入 resilience lane（该 lane 提供 Go，test-only producer 可在同一 lane 内运行），两 lane 的 1800 秒上限不变。

本次 S05 扩展的局部候选验证与截图见 [PR #397](https://github.com/FelixJI/VibeTable/pull/397)。局部验证仅覆盖新增断言，不替代完整 PR 门禁，也不计入本节的历史主干样本。

新增 S37 的局部同包验证见 [PR #401](https://github.com/FelixJI/VibeTable/pull/401)：165 行合成来源、三行台账的公式/Lookup/CSV 对照、来源变更、重命名及重开通过；同包 S05/S26 回归通过。这些局部结果不替代本节历史主干样本或当前 manifest 的完整 CI 门禁。

同一 0bdbc1b 主干候选的两份分片报告精确覆盖当时 manifest 的 35 个场景：`resilience` lane 33 场（artifact 内部路径 `lane-evidence/resilience/20260927T165554Z/product-e2e-report.json`）与 `data-io` lane 2 场（`lane-evidence/data-io/20260927T165201Z/product-e2e-report.json`），两分片 lane 报告 commit 均为 0bdbc1b，聚合门禁核对场景选择精确覆盖当前 manifest 且两分片均成功。覆盖清单：`01-offline-first-start`、`02-all-field-schema`、`03-schema-errors`、`04-json-round-trip`、`05-formula-lifecycle`、`06-relation-fanout`、`07-attachment-history`、`08-stale-conflict`、`09-atomic-import-scale`、`10-sse-reconnect`、`11-plugin-mutation`、`12-backup-consistency`、`13-protection-policy`、`14-document-diff`、`15-workspace-snapshot-package`、`16-dashboard-lifecycle`、`17-interface-lifecycle`、`18-workspace-search`、`19-gallery-lifecycle`、`20-kanban-lane-drag`、`21-calendar-date-move`、`22-timeline-date-move`、`23-directory-replica-recovery`、`24-directory-replica-conflict`、`26-lookup-definition-read`、`27-relation-target-search`、`28-relation-delta-preview`、`29-lookup-source-pagination`、`30-query-snapshot-validation`、`31-relation-pair-inspection`、`32-shared-work-calendar`、`33-host-grid-presentation`、`34-relation-lookup-data-io`、`35-data-io-interoperability`、`36-backend-import-exit`。

按实际阶段口径统计共 681 项断言通过：S11 与 S33 的顶层断言是 phases.resume 镜像，只计 seed/resume 两份 phase 报告各一次；S24 计五个真实阶段（14/14/14/29/18，共 89 项）；其余场景按顶层报告计。未确认 bridge failure、pending request 与 pageErrors 均为 0；acknowledgedFailures 共 38 项，全部为场景显式确认的预期失败。36 份唯一归档 lifecycle 证据全部成功；成功 CI 仅归档聚合报告，S24 的 8 份逐 Host 原件仅在失败场景归档，本次未读取，退出清理由已审阅的 fail-closed runner 与成功阶段链证明。

六个普通业务场景（S01/02/03/05/06/08）在真实业务完成后经完整 Job 成员观测核验：Host 与 Go sidecar 各 1、Python backend 与插件 Node 各 0；Job 中同时存在 conhost 等宿主附属进程，不据此声称整个 Job 仅两进程，也不声称全时段事件审计。S24 在声明范围（两端冲突选择、败方恢复预览、重启可达性）的五阶段全部通过；61d 样本中 verify-resolved 阶段暴露的 replica 实际异常（replica.status failed / pendingSync 与新增 corrupt 保护快照项；并非故障注入，旧 b744 样本同样存在）经正式补审确认为稳定 P2（S24-R2-F1，完整公开报告见 [Issue #341 评论 5855979696](https://github.com/FelixJI/VibeTable/issues/341#issuecomment-5855979696)）：纯表 apply 提交非空 filehistory root 时，snapshotFiles 错误使用空 document 集合的 max 版本 0，bundle 严格拒绝，正常关闭因此新增 protection corrupt。该 P2 已由 [PR386](https://github.com/FelixJI/VibeTable/pull/386) 修复（权威 filehistory head 版本与 snapshot 元数据一致，严格 bundle 校验保留，并新增 S24 正常关闭保护点完整性门禁）；后续资格失败诊断输出与 .NET 终止通知同步测试由 [PR388](https://github.com/FelixJI/VibeTable/pull/388) 完善后收敛为本 0b 主干样本，其中两个新增保护快照均 ready/verified/replicated，最终公开 replica 为 replicated、pendingSync=false。S24 已以声明范围由 Task #341 验收关闭（完整独立补审与 AC1–6 见 [Issue #341 评论 5858178011](https://github.com/FelixJI/VibeTable/issues/341#issuecomment-5858178011)）；该范围不含败方实际 restore、二次运输收敛、云盘 offline/reconnect、exclusive writer 或公开手动同步，不据此宣称同步收敛。

四组件 desktop-host、web-grid、python-backend、pocketbase-sidecar freshness 全部通过，packageAudit 通过。启动、静默工作集、包体与 RPC 数值以[真实包运行时基线](quality/packaged-runtime-baseline.md)的当前对照表（含本 0b 最终样本列）为唯一权威；S10 恢复四项计时以[打包产品恢复时延](quality/packaged-recovery-baseline.md)为准，本页不重复第二套数字。关联 CD 36337135634 成功：Stage release 执行，正式 Publish、provenance/SBOM attestation 与 Verify closed E2E 步骤均 skipped，普通合并未发布；`release-state.json` 记录 publish=false、reason=plan-unchanged、source_sha=0bdbc1b，普通合并的 release-candidate 产物仅承载该状态哨兵，真实产品候选在 CI 候选交接（ci-candidate-handoff/prepare）中归档；0b 候选的 release lane 报告记录 269 个文件、ZIP 146888718 bytes。

## 前一固定样本 2026-09-27（61d，历史）

以下为前一固定样本的历史记录，保留真实事实，不作为当前资格。该样本绑定 main 61d4b407、CI run 36316404213（两分片内部路径 `lane-evidence/resilience/20260927T115753Z` 与 `lane-evidence/data-io/20260927T115335Z`）与报告契约 2.0：35 场全部通过、0 failed、0 skipped；按同一阶段口径共 676 项断言（S24 五阶段 14/14/14/26/16，共 84 项）、acknowledgedFailures 34 项、36 份唯一归档 lifecycle 全部成功；六普通场景零 Worker 观测与四组件 freshness 同当前节口径。该样本 verify-resolved 阶段暴露上述 S24-R2-F1 实际异常（时点表述为“问题整改中”），修复与主干复验见当前证据节；其 S23 恢复与 S07/S17/S28/S32 等中间状态叙述见下方 2026-09-09 历史节与各自资格记录，此处不重复。

## 2026-09-09 主干固定样本（历史）

以下为旧主干固定样本的历史记录，保留其真实事实与相对差异，不作为当前资格。该样本绑定 source `25b260394a0a01e8432d23fa3d1a6e8b9b65922f`、main CI run 34309394462（`ci-lane-resilience` lane，内部路径 `lane-evidence/resilience/20260909T042302Z/product-e2e-report.json`，artifact 按 CI 策略短期保留）与报告契约 2.0；同轮 29 场全部通过、0 failed、0 skipped，累计 382 项断言。该处仅声明当时固定样本，不代表后续提交均通过，也不以单次样本宣称性能改善。

- 当时相对该样本的 manifest gap：6（`24-directory-replica-conflict`、`32-shared-work-calendar`、`33-host-grid-presentation`、`34-relation-lookup-data-io`、`35-data-io-interoperability`、`36-backend-import-exit`）。
- 当时相对该样本的 manifest changed：6（`01-offline-first-start`、`07-attachment-history`、`11-plugin-mutation`、`16-dashboard-lifecycle`、`17-interface-lifecycle`、`28-relation-delta-preview`）。该差异后来由 2026-09-27 主干样本收齐。
- 当时 manifest surplus：无。

该样本覆盖当时登记的 29 个场景（01–23、26–31）。S06 两端关系字段的真实 UI 编辑、冻结计划与应用，以及 S26–S31 的 Lookup 描述、关系搜索与预览、来源分页、查询快照与关系完整性检查，均获得该次打包报告。

以下是该历史样本时点的中间状态叙述，保留原貌；其新增断言均已由 2026-09-27 主干样本覆盖（见当前证据节）。

同编号场景 S07 的后继新增段保留历史抽屉的 Workspace V2 恢复，并新增公开 `history.previewRestoreRequested` / `history.applyRestoreRequested` 桥接闭环，独立验证 Go Product owner；该新增段验证预览不改变当前附件、Product 五字段结果不含 `mutationRevision`，以及返回行中的当前存储名与附件权威列表、表/记录/字段身份、原名、既有内容 checksum 和长度一致，恢复会重新生成托管存储名。旧 25b 样本不包含这些 Product 断言，不能用旧报告证明新增恢复入口；[本地资格记录](quality/history-restore-local-validation.md)为其同包局部验证，该等待已由 2026-09-27 主干样本覆盖。

S17 当时新增 sidecar 重启后的 fresh Interface list/load、完整定义及 revision 持久和真实 UI 删除断言，source `65be85ce3d5727dc95cf3723ab0fd4795fb2016e` 的同包 S17 已通过，证据见 [Surface 资格记录](quality/surface-metadata-owner-qualification.md)；该局部资格不替代当时主干完整样本，已由 2026-09-27 样本承接。

S28 当时保留原关系预览断言，并增加真实选择器多值增删、单值替换和清空、新建目标、Lookup 更新与同 UUID 重开；旧主干样本只证明预览语义，新增关系写入资格当时以 #345 对应 PR 的新包证据为准，已由 2026-09-27 样本承接。

S32/S34/S35 当时不在该主干样本中：S32 局部资格见[关系与 Lookup 数据互操作](quality/relation-lookup-data-io.md)，S34/S35 真实包证据见[数据互操作资格矩阵](quality/data-io-interoperability.md)（本地 source-built 与新包单场景，非该 main 样本）；后续 CI 已将 S34/S35 放入独立的 `data-io` lane（`product-e2e-data-io` stage），其余场景由 `resilience` lane 的 `product-e2e` stage 执行，两 stage 各保留 1800 秒上限。

该样本四组件 freshness 全部通过；所有场景附着真实 WPF/WebView2，Node/Host 正常退出，进程组及后代为空、端口与 owner lease 清理通过；诊断为 0 个未确认 bridge failure、0 个 pending request，另有 33 个已确认事件（含预期取消）；性能汇总的 28 次失败使用不同口径，不能称为全程没有拒绝响应。S23 证明目录副本的公开创建、读写、释放活动缓存、同 UUID 重开与 sidecar 替代进程恢复，不扩展为手动同步、跨设备 offline/reconnect、冲突处理或 exclusive-writer 资格。[历史失败记录](quality/product-e2e-failure-notes.md)继续保留原始出处与未归因状态，该次通过不替代历史根因分析。

该时点的 S24 叙述：四个核心阶段为 seed、fork-left、fork-right、resolve；双方正常关闭后只运输产品生成的副本载荷；解决后另一次正常 Host 关闭及重启检查同一 workspace、胜方行、已解决冲突和 recovery Snapshot 的 UI 恢复预览可达性；左右 local-data、WebView 数据目录、选择目录保持独立；成功 Node 阶段必须提供完整 bridge diagnostics 且没有未确认失败或 pending，180s 超时保留原始输出和部分结果。当时 main `58032b9` 已包含限定 provisional 操作准入，但 S24 源码尚待完整包与真实产品执行，不能据夹具通过宣布完成；不执行败方恢复，不声明右端最终收敛、云盘 offline/reconnect 或 exclusive writing，详见[唯一资格记录](quality/directory-replica-conflict-s24-qualification.md)。该等待已由 2026-09-27 主干样本承接（当前证据节）。

### 该样本的 29 场景耗时（2026-09-09）

| 场景 | 耗时 |
|---|---:|
| `01-offline-first-start` | 6.372s |
| `02-all-field-schema` | 44.045s |
| `03-schema-errors` | 18.778s |
| `04-json-round-trip` | 20.407s |
| `05-formula-lifecycle` | 17.019s |
| `06-relation-fanout` | 45.013s |
| `07-attachment-history` | 18.967s |
| `08-stale-conflict` | 10.573s |
| `09-atomic-import-scale` | 17.381s |
| `10-sse-reconnect` | 23.927s |
| `11-plugin-mutation` | 17.492s |
| `12-backup-consistency` | 31.838s |
| `13-protection-policy` | 15.919s |
| `14-document-diff` | 8.890s |
| `15-workspace-snapshot-package` | 64.214s |
| `16-dashboard-lifecycle` | 49.004s |
| `17-interface-lifecycle` | 23.415s |
| `18-workspace-search` | 37.245s |
| `19-gallery-lifecycle` | 26.677s |
| `20-kanban-lane-drag` | 24.571s |
| `21-calendar-date-move` | 23.357s |
| `22-timeline-date-move` | 21.501s |
| `23-directory-replica-recovery` | 39.018s |
| `26-lookup-definition-read` | 14.025s |
| `27-relation-target-search` | 19.262s |
| `28-relation-delta-preview` | 15.967s |
| `29-lookup-source-pagination` | 15.794s |
| `30-query-snapshot-validation` | 10.212s |
| `31-relation-pair-inspection` | 23.119s |

`history.query` 8 次，p50 19.7ms、p95/max 157.3ms；`history.drawer.initialLoad` 2 次，p50 115.34ms、p95/max 247.41ms，均在既有预算内。场景耗时为 6.372s–64.214s，包含启动与 fixture 准备，不能解释为单次用户交互延迟。

S10 恢复测量（历史样本）：sidecar kill→可读表 2273.97ms，backend 退出后关闭工作区 2664.85ms、重开 2590.33ms，backend kill→可写 session 5747.57ms。它们是该次故障注入样本，不是普通 RPC 延迟或新增性能门槛；2026-09-27 同源新样本见[打包产品恢复时延](quality/packaged-recovery-baseline.md)，本页不再维护第二套恢复数字。

## 本次主干 CI/CD 归属

0bdbc1b 主干 CI 36333755042 的 14 个 job 全部 success（含全部严格 `required`），两分片产品 E2E、固定候选构建、package contract、更新恢复与生命周期证据均在该 run 内。关联 CD 36337135634 成功：Stage release 执行，正式 Publish、provenance/SBOM attestation 与 Verify closed E2E 步骤均 skipped，普通合并未发布；`release-state.json` 记录 publish=false、reason=plan-unchanged、source_sha=0bdbc1b，普通合并的 release-candidate 产物仅承载该状态哨兵，真实产品候选在 CI 候选交接（ci-candidate-handoff/prepare）中归档，按既有 checksum 契约核对，不叠加新 hash 层。

中间历史保留：PR386 squash 为 c2e6052b 后，c2 主干 CI36324919070 整体 failure（core/required 失败；其中 PDF qualification 步骤 exit1 且缺少详细输出，诊断输出由 PR388 补齐），CD36328134058 整体 skipped；PR388 旧提交 1b 的 CI36327492700 中 core/required 失败、resilience lane 因新提交取消，其余 job 成功。两者保留为历史失败/取消，不能改写为通过；0bdbc1b 是后继主干的完整补验。先前 main312 的覆盖率收集失败、L9 旧 main 36312515247 的 core 失败同样保留为历史失败。

## 测量口径

- `场景耗时`：Playwright 连接到真实 WebView2 后，到场景断言与证据采集前的业务步骤耗时；包含建表、导入、故障注入等测试准备。
- `bridge 往返`：renderer 发出带 `requestId` 的请求，到对应成功响应或 `operation.failed` 的耗时。
- `历史抽屉首屏`：点击历史按钮，到 `history-timeline` 可见；覆盖 UI 触发、宿主、数据权威查询（历史样本当时经 Python，现行直达 Go）与 Vue 渲染。
- 性能证据由 `tests/e2e/product_e2e_runner.py` 聚合到报告的 `performance` 字段。

## 2026-07-26 历史全场景基线

| 场景 | 耗时 |
|---|---:|
| 01 离线首次启动 | 0.341s |
| 02 全字段结构 | 19.381s |
| 03 结构错误 | 2.012s |
| 04 JSON 往返 | 6.926s |
| 05 公式生命周期 | 7.758s |
| 06 关系联动 | 7.559s |
| 07 附件与历史 | 9.679s |
| 08 过期写冲突 | 1.539s |
| 09 1000 行原子导入故障 | 5.326s |
| 10 SSE/sidecar 重连 | 7.466s |
| 11 插件变更 | 4.650s |
| 12 备份一致性 | 16.999s |

这组 12 场景均通过的历史数据用于发现问题，不作为当前零失败基线：增强诊断后发现了 7 次被旧 runner 忽略的中途 `operation.failed`，其中 6 次来自故障恢复窗口的历史轮询，1 次来自关系搜索发送空字符串。

## 正常操作延迟

排除故障恢复等待后，多数 bridge 操作的 p95 低于 110ms：schema/lookup 描述约 59ms，schema apply 约 66ms，附件列表约 35ms，历史恢复应用约 54ms。场景 07 连续四轮共 12 次普通历史查询的 p50 为 20.0ms，p95/max 为 72.2ms。

因此“小数据历史查询本身普遍很慢”不成立。肉眼可感知的停顿主要来自：

1. sidecar 被杀后的自动恢复，读取会等待新 backend 代际；
2. 场景在打开历史前执行的建表、附件、备份等准备工作；
3. 旧 runner 的 20–30 秒等待上限没有输出真实耗时，容易被误读为实际延迟。

## 预算

| 指标 | 目标 | 告警 | 硬上限 |
|---|---:|---:|---:|
| 普通历史查询 p95 | ≤200ms | >500ms | 单次 >2s |
| 历史抽屉首屏 p95 | ≤300ms | >750ms | 单次 >2s |
| 故障恢复中的幂等读取 | ≤3s | >3s | bridge 超时 10s |
| 单场景防挂死 | 按历史基线比较 | 相对上升 50% | 180s |

最终关键场景回归应同时满足：场景通过、未确认 `operation.failed=0`、pending=0，并在报告中
保留上述耗时；冲突、拒绝和取消等预期失败必须由场景明确确认，不能混入未确认失败。

## 2026-07-26 历史验证结果

最终代码的完整轮次（2026-07-26）覆盖 12 个真实 WPF/WebView2 场景。诊断过程中
08 场景的并发编辑冲突本身正确，但 `table.editRejected` 未携带原始 `requestId`，
被新增诊断误记为 pending。补齐更新、插入、删除响应的请求关联后，最新发布包的
同轮 12 个场景均通过，所有场景均为 `operation.failed=0`、`pending=0`。

- 普通历史查询：p50 20.0ms；此前四轮共 12 次的 p95/max 72.2ms。
- 历史抽屉首屏：2 次的 p50 46.3ms、p95/max 431.0ms，低于 750ms 告警线。
- 故障恢复中的历史查询：1.957s；这是 09 场景杀死 sidecar 后等待新后端代际的时间，
  低于 3s 恢复上限，不应与普通查询混合解释。
- 故障恢复中的 `query.page`：约 2.056s；同样是 10 场景的主动重连成本。
- 除故障恢复外，主要请求的 p95：schema describe 60.9ms、lookup list 66.6ms、
  events reconcile 75.3ms、schema apply 107.4ms。

完整轮次的场景耗时为 0.359s–21.076s。02（21.076s）和 12（16.384s）较长，
是因为场景包含全字段建模、附件/备份准备和一致性校验；它们不是单次用户交互延迟。
## 2026-09-29～30 三表计算链资格

本次本地对照使用固定 PCG(396, 390)、10k/50k 纯合成来源行、67 条汇总、1,000 条主表和100行视窗，覆盖0/1/200条匹配、64个其他条件、1KiB宽文本和日期字段。链路为条件SUM Lookup→Formula×2→下游SUMIF；独立标量oracle核对完整1,000/986条结果，不只检查首屏。优化前预算已冻结在 [#396](https://github.com/FelixJI/VibeTable/issues/396#issuecomment-5891847462)，下表均通过，不能用有限250ms求值保护代替这些延迟目标。

机器为 i9-14900KF（24核/32逻辑处理器）、68,463,378,432B RAM、Windows 11 x64 10.0.26340、Go1.27.0，GOMAXPROCS=32。两组分别独立进程，测量时没有并发构建/测试。各操作首个样本单列，热测20次；application-cold为关闭PB实例后在同一DB重建app/compiler/query的20次样本，OS文件缓存和Go runtime仍热，bootstrap另记，不能称磁盘冷启动。p50/p95使用nearest-rank，单位ms。

| 操作 | 10k 基线 p50/p95 | 10k 本次 p50/p95 | 50k 基线 p50/p95 | 50k 本次 p50/p95 | 冻结 p95 上限 |
|---|---:|---:|---:|---:|---:|
| 原始字段筛选排序，热 | 10.674/11.523 | 17.530/20.435 | 11.908/15.138 | 17.524/18.508 | 30 |
| 原始字段筛选排序，application-cold | 11.786/12.525 | 20.578/23.485 | 12.919/18.050 | 19.050/21.158 | 30 |
| 计算字段筛选排序，热 | 20.027/20.536 | 37.886/40.615 | 21.023/30.935 | 37.415/40.742 | 50 |
| 计算字段筛选排序，application-cold | 21.039/23.255 | 41.541/44.190 | 21.043/24.041 | 39.418/41.314 | 50 |
| 100条重复Lookup条件，热 | 8.525/9.520 | 15.241/16.766 | 62.911/73.337 | 111.869/116.041 | 10k:25；50k:150 |

表中“本次”为9月30日加入上游定义版本水印后的新测量，源值/schema变更后的过期结果拒绝由普通与race回归验证。延迟均满足原预算，但高于此前基线，因此只主张确定性重复查询减少，不主张墙钟加速；不能仅从这两轮耗时把差异归因于某一代码修改。开发阶段不承担历史计算缓存的迁移兼容；新建工作区、当前版本重开与后续schema编辑仍须满足正确性和完整门禁。

确定性计数：100行查询的DBX查询回调从126降为31（摘要批取后的中间候选为29，新增上游定义版本读取增加2次），其中完整PB记录摘要读取从100次降为1次；500行分2批，并保留Original、摘要私有列及原始顺序。100条重复条件仍只发出1次实际匹配查询（总回调13），没有另建缓存；各计时请求execution-plan编译次数为0，创建67条汇总和1,000条主表时各编译1次。实际EXPLAIN是主键范围SEARCH，不能称条件索引命中；未测SQLite visited rows，回调次数不是扫描行数。

每秒读取进程的Windows PeakWorkingSet64，本次10k/50k最后存活样本峰值分别150,896,640B/332,005,376B，基线为150,425,600B/331,010,048B，均低于536,870,912B。该口径包括初始化/造数/所有查询，最后采样与退出间可能漏峰，不是单次查询RSS。每规模保留105条原始样本、5组分位数、编译/查询计数、SQL计划及退出码；绑定的候选快照与日志记录在#396。

原始来源通过validated PocketBase Save准备，汇总/主表与后续变更仍走真实mutation和jobs；本数据集不证明50k导入吞吐。原先mutation造数在49,500行触发30分钟测试超时的失败保留；不同造数方式的耗时不相减为产品收益。无关备注变更的2个全量fanout已降为0，相关源变更在fanout完成前拒绝旧值；表达式/时钟、工作区A→B→A及进行中取消由独立组合回归覆盖。

包含上游定义水印的开发候选 `95532dc1`，其普通构建真实WPF/WebView2 S38于2026-09-30通过，场景75.69008s，33项断言及四组件freshness通过。覆盖可见编辑器创建/编辑三层计算、只读F2、199条来源分页、计算值筛选排序、CSV/XLSX逐值顺序与公式样文本不执行、关闭重开同一workspace身份；无外网/异常/pending，进程、端口及句柄清理通过。该本地场景不替代最终PR全量质量和旧主路径矩阵；此前9月29日中间候选证据保留。相同界面路径截图：[计算值筛选排序](assets/screenshots/vibetable-calculation-chain-filtered.png)、[完整来源分页](assets/screenshots/vibetable-calculation-chain-sources.png)。

## #414 关系生命周期局部候选资格（2026-10-01）

产品提交 `e8949f33f917122e9255849e5505155324954b50` 基于已合入 V2 会话的 main `6db8c6b3`。正式候选构建通过（274 个文件）；真实同包 S38 报告 `20260930T202918Z` 仅选择 S38：执行 1 场、成功 1 场、失败 0 场、跳过 0 场；清单中另外 36 场未在该局部报告执行，wall clock 91.55182s。来源199条、完整三行独立数值、初始关系绑定、引用字段改名、筛选排序、CSV/XLSX与重开均通过；Go integration/scale 另行验证删除拒绝、解绑与重新绑定，不作为 S38 界面覆盖；后台 bridge clean 也通过。截图见 [关系生命周期](quality/screenshots/issue414-relation-lifecycle.png)。此前 `5b897ec3` 的 S38 因7次后台 lookup.query 失败而整场失败，保留原报告，不以39项数值断言通过替代整场结论；共享查询字段映射修正后才通过。

复用 #396 原始 fixture/数据与5组查询预算，10k规模通过。50k首轮 computed warm p95 51.613ms 超过50ms，明确为预算失败；独占复核 warm/cold computed p95 40.154/43.034ms、raw p95 20.173/22.18ms、repeated lookup p95 120.09ms 通过原预算，首轮失败证据仍保留。源修改到完整查询仅为单次观察，不能称p95或提速。

新增关系专属资格复用同一原始10k/50k来源、67条summary与1000条main，实际覆盖 reciprocal WrapValues 与 relationPath AllRecords；不以本来已有全表cursor的conditional lookup冒充这次新增路径。两规模绑定、解绑、重新绑定、来源修改后的全部1000行独立oracle、67条summary新鲜envelope均通过，新path job记录 allRecords=true、processedMainRows=1000。源修改到完整查询单次为41.099048s（10k）与263.082394s（50k），无新增失效延迟预算，不声称性能改善。Windows每1秒观察实际进程 PeakWorkingSet64，10k/50k峰值为142209024/293060608字节；最后未观测区间可能低估，不把累计分配或配置上限当峰值。

这些局部证据不更新本页历史35场样本，不代替当前PR完整required及合并后main CI/CD哨兵。

## 2026-10-01 OOXML 搜索资格（#410）

开发候选 `1ab09aba` 的真实 WPF/WebView2 S18 单场景通过，用时 42.24863s。合成 DOCX、XLSX、PPTX 均命中“合同编号”；未引用字符串无命中，缓存公式值与演讲备注可检索，部分覆盖提示及打开正确文件均通过。报告绑定 `20260930T171742Z`，该结果不替代完整 PR 门禁，也不扩大 PDF 支持范围。

界面截图：[OOXML 搜索与覆盖提示](quality/screenshots/issue410-ooxml-search.png)。

## 2026-10-01 DOCX 生产对比资格（#412）

产品包 `b7f178ee`、截图定位器修复后的 harness `d18065ff` 在真实 WPF/WebView2 S14 中通过：仅执行 1 场、成功 1 场、失败 0 场、跳过 0 场，用时 26.97566s。文本 77 组分页、DOCX 3 组格式/结构变化、已有修订副本的零变化、具体位置与字号、取消重试、源修订及有效指针保留、关闭过期与清理均核对通过。其余场景未在本局部报告执行，本结果不替代最终 PR 门禁或 #415 同包组合验收。

报告 `20260930T222617Z` 对三个经 Job 身份核实的 Worker 记录工作集样本，最高观察值 114585600 字节；名义 50ms 驱动循环会漏掉瞬时或短命进程峰值，不等于内存限制。此前错误截图定位器导致的整场超时失败已保留，未抬高期限。

真实界面：[DOCX 位置、覆盖提示与格式差异](quality/screenshots/issue412-docx-diff.png)。

## 2026-10-01 XLSX 稀疏语义对比资格（#413）

正式候选 `3cdcd92b`（274 个文件，ZIP 147593020 字节）的真实 WPF/WebView2 S14 于 `20260930T231200Z` 单场景通过，27.14228s。许可语料的格式和内容变化完整 oracle 分别为 3 和 11 组；另有 2 sheet、10004 个实际 cell、20万字符共享串及末格 XFD1048576 的稀疏语料，精确 75 组变化。三次首次比较分别为 217.3174ms、105.4656ms、439.8808ms，均为单次样本，未称 p95。原始分页每页 7 组和完整稳定 ID、UI 首屏至多 50 组及完整翻页、缓存未重算/部分覆盖、源与历史只读、过期会话拒绝和关闭清理均实际通过。

同包附加采样场景 `20260930T232240Z` 通过（32.26333s）：复用 Windows Job 已核身份成员，每 50ms 名义间隔观测 working set，共 219 个样本，无未验证成员或采样错误。Host 最大观测值 326569984B，同时全 Job 总 working set 最大观测值 1141510144B，包含 Go、WebView2 等全场景成员；不是 XLSX-only RSS、内核峰值或 Worker 1GiB 上限，可能漏掉短进程和采样间分配。观测不修改生产流程或期限。

两处畸形输入（重复共享公式地址、错误关系 part 位置）的生产 engine 分类随后真实 RED→GREEN，OpenXml 全项目 182 项通过。该代码增量待最终候选与完整远端门禁核实。完整本地 quality 在 Go 阶段因已记录的 Windows 空目录 delete-pending/外部句柄类清理失败而失败，既有三次尝试耗尽；保留 FAIL，不追加重试、不改生产关闭语义。其余最新质量阶段及 PR required 以真实报告为准，本地 S14 不替代全量矩阵或 #415 同包组合验收。

真实界面截图：[格式及位置](quality/screenshots/issue413-xlsx-format.png)、[内容及覆盖提示](quality/screenshots/issue413-xlsx-content.png)。
