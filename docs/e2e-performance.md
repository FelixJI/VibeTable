# 产品 E2E 性能基线

本页记录真实发布包在 Windows WPF/WebView2 + 按需 Python Worker + PocketBase 进程栈上的可比较基线。它不是浏览器 mock，也不把 20–30 秒的测试等待上限当作性能目标。

## 当前产品 E2E 证据

- source SHA：`GitHub/main@61d4b40759489c751a03bb595a27422e9ecd4f5b`
- GitHub run：[main CI 36316404213](https://github.com/FelixJI/VibeTable/actions/runs/36316404213)
- 报告契约：`contractVersion=2.0`
- 结果：35/35 passed、0 failed、0 skipped。
- 当前 manifest gap：无。
- 当前 manifest surplus：无。
- 当前 manifest changed：1（`24-directory-replica-conflict`）。

同一 61d 主干候选的两份分片报告精确覆盖该源码 manifest 的 35 个场景；当前 S24 新增正常关闭保护点完整性门禁，61d 旧样本不覆盖该增量：`resilience` lane 33 场（artifact 内部路径 `lane-evidence/resilience/20260927T115753Z/product-e2e-report.json`）与 `data-io` lane 2 场（`lane-evidence/data-io/20260927T115335Z/product-e2e-report.json`），两分片 lane 报告 commit 均为 61d4b407，聚合门禁核对场景选择精确覆盖当前 manifest 且两分片均成功。覆盖清单：`01-offline-first-start`、`02-all-field-schema`、`03-schema-errors`、`04-json-round-trip`、`05-formula-lifecycle`、`06-relation-fanout`、`07-attachment-history`、`08-stale-conflict`、`09-atomic-import-scale`、`10-sse-reconnect`、`11-plugin-mutation`、`12-backup-consistency`、`13-protection-policy`、`14-document-diff`、`15-workspace-snapshot-package`、`16-dashboard-lifecycle`、`17-interface-lifecycle`、`18-workspace-search`、`19-gallery-lifecycle`、`20-kanban-lane-drag`、`21-calendar-date-move`、`22-timeline-date-move`、`23-directory-replica-recovery`、`24-directory-replica-conflict`、`26-lookup-definition-read`、`27-relation-target-search`、`28-relation-delta-preview`、`29-lookup-source-pagination`、`30-query-snapshot-validation`、`31-relation-pair-inspection`、`32-shared-work-calendar`、`33-host-grid-presentation`、`34-relation-lookup-data-io`、`35-data-io-interoperability`、`36-backend-import-exit`。

按实际阶段口径统计共 676 项断言通过：S11 与 S33 的顶层断言是 phases.resume 镜像，只计 seed/resume 两份 phase 报告各一次；S24 计五个真实阶段（14/14/14/26/16，共 84 项）；其余场景按顶层报告计。未确认 bridge failure、pending request 与 pageErrors 均为 0；acknowledgedFailures 共 34 项，全部为场景显式确认的预期失败。36 份唯一归档 lifecycle 证据全部成功；成功 CI 仅归档聚合报告，S24 的 8 份逐 Host 原件仅在失败场景归档，本次未读取，退出清理由已审阅的 fail-closed runner 与成功阶段链证明。

六个普通业务场景（S01/02/03/05/06/08）在真实业务完成后经完整 Job 成员观测核验：Host 与 Go sidecar 各 1、Python backend 与插件 Node 各 0；Job 中同时存在 conhost 等宿主附属进程，不据此声称整个 Job 仅两进程，也不声称全时段事件审计。S24 在声明范围（两端冲突选择、败方恢复预览、重启可达性）的五阶段全部通过；verify-resolved 阶段额外出现的 replica 实际异常（replica.status failed / pendingSync 与新增 corrupt 保护快照项；并非本次故障注入，旧 b744 样本同样存在）经正式补审确认为稳定 P2（S24-R2-F1，完整公开报告见 [Issue #341 评论 5855979696](https://github.com/FelixJI/VibeTable/issues/341#issuecomment-5855979696)）：纯表 apply 提交非空 filehistory root 时，snapshotFiles 错误使用空 document 集合的 max 版本 0，bundle 严格拒绝，正常关闭因此新增 protection corrupt；两个恢复副本本身健康。问题整改中：本样本保留为 61d 固定实测样本，不宣称 P2 已修复，不据此宣称同步收敛，S24 母任务与 Goal 均未完成验收。

四组件 desktop-host、web-grid、python-backend、pocketbase-sidecar freshness 全部通过，packageAudit 通过。启动、静默工作集、包体与 RPC 数值以[真实包运行时基线](quality/packaged-runtime-baseline.md)的 2026-09-27 样本为唯一权威表；S10 恢复四项计时以[打包产品恢复时延](quality/packaged-recovery-baseline.md)为准，本页不重复第二套数字。关联 CD 36319246538 成功：Stage release 执行，正式 Publish/provenance/SBOM attestation 步骤均 skipped，普通合并未发布。

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

61d 主干 CI 36316404213 的 14 个 job 全部 success（含全部严格 `required`），两分片产品 E2E、固定候选构建、package contract、更新恢复与生命周期证据均在该 run 内。关联 CD 36319246538 成功：Stage release 执行，正式 Publish、provenance/SBOM attestation 与 closed-evidence 步骤均 skipped，普通合并未发布；候选交接归档的四项资产、build identity 与 SPDX 2.3 SBOM 已按既有 checksum 契约核对，不叠加新 hash 层。先前 main312 的覆盖率收集失败、L9 旧 main 36312515247 的 core 失败均保留为历史失败，不能改写为对应 SHA 通过；61d 是后继主干的完整补验。

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
