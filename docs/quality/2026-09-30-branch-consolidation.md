# 2026-09-30 旧分支价值归并记录

基线：`GitHub/main@f10d377b03197540a4cbd48b96ae84b6a4d46482`；保留开放 PR [#404](https://github.com/FelixJI/VibeTable/pull/404)。本记录是分支内容去向，不把“迁入草稿 PR”写成“已合并”。

## 原则与恢复

- 先提取独有价值，再删除旧分支引用；已覆盖、已替代、明确暂停的实现不重新引入。
- 删除前已为全部 refs 和 detached HEAD 建立并验证 Git bundle，11 个脏工作树另保存二进制 diff 与未跟踪文件。
- 本次不删除工作树文件/目录，也不清理 ignored 文件；解除旧分支占用时仍停在原 HEAD，原未提交内容保留。
- 代码迁移和分支清理授权不包含合并本 PR，也不构成产品/发布资格。

## 逐分支去向

| 原分支 | 位置 | 原 tip | 判断与内容去向 |
| --- | --- | --- | --- |
| `automation/release` | 远端 | `79d71127` | 关闭的 #61 遗留版本候选，不搬旧版本变更；下次发布由 release prepare 重新生成。 |
| `chore/python-3.13` | 本地 | `d7b1566b` | Python 3.13 已在 main；旧 descendants 方案被 Job scope 替代；脏选择投影测试被 TestSelectionProjectionCannotMixSchemaAndCursorRevisions 覆盖；早期审计由成熟度计划接续。 |
| `codex/accept-applied-query-reloads` | 本地 | `0c2a2fce` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/acknowledge-correlated-grid-queries` | 本地 | `f916f58f` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/add-a5-falsy-corpus` | 本地 | `1fb590b2` | main data-io a5-falsy-container-corpus 与互操作资格已承接。 |
| `codex/add-atomic-job-process-scope` | 本地 | `89a8459d` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/add-directory-replica-conflict-s24` | 本地 | `13ea679a` | main 已承接目录恢复、准入及 S24 测试基础；S24 完整产品资格仍待验收，不重复搬旧代码。 |
| `codex/add-packaged-replica-hosts` | 本地 | `83843513` | main 已承接目录恢复、准入及 S24 测试基础；S24 完整产品资格仍待验收，不重复搬旧代码。 |
| `codex/add-packaged-runtime-baseline` | 本地 | `e9a0c420` | main 已有 packaged runtime baseline / 恢复资格或新版 crash oracle，成熟度计划列明继承。 |
| `codex/add-pdf-support-corpus` | 本地、远端 | `3f7667c9` | 不迁旧红测试；页面可达性/Filter 缺口已由 PDF 资格计划及 qa/pdf-adapter 保留，候选未采用，不声称缺陷已修复。 |
| `codex/add-sidecar-generation-authority` | 本地、远端 | `77a0dbdd` | main 的 PocketBaseGenerationContext/生命周期与 detached authority 演进版已承接。 |
| `codex/add-updated-process-crash-oracle` | 本地 | `6adcd661` | main 已有 packaged runtime baseline / 恢复资格或新版 crash oracle，成熟度计划列明继承。 |
| `codex/admit-provisional-replica-operations` | 本地 | `d2c8c918` | main 已承接目录恢复、准入及 S24 测试基础；S24 完整产品资格仍待验收，不重复搬旧代码。 |
| `codex/calculation-chain-ui-qualification` | 本地、远端 | `a3af5f73` | 保留；开放 PR #404，不修改其 head。 |
| `codex/calculation-scale-qualification` | 本地 | `3e7590b0` | 由 PR #404 承接；25 个脏路径中 24 个内容一致，剩余 HasComputedInputs 已在 PR #404 有意删除。 |
| `codex/checkpoint-search-close` | 本地、远端 | `4a2a5b0e` | 迁入本 PR：Close 的 WAL checkpoint、并发操作保护及三项回归。 |
| `codex/conditional-lookup-ui` | 本地 | `14f21565` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/detach-recovery-authority` | 本地、远端 | `374ff1de` | main 的 PocketBaseGenerationContext/生命周期与 detached authority 演进版已承接。 |
| `codex/diagnose-grid-query-cancellation` | 本地、远端 | `ba66ac2a` | main 的 RequestQuery_Cancels_SupersededRead 已采用确定性时间和取消断言。 |
| `codex/diagnose-lookup-viewport-phase` | 本地、远端 | `cfd109ef` | main lookup_sources_viewport.test.mjs 已含 phase 与 abort 诊断。 |
| `codex/diagnose-pending-job-drain` | 本地、远端 | `11995950` | 一次性诊断/性能探针，不作为正式回归迁入；原稿保存在恢复 bundle。 |
| `codex/diagnose-restored-search` | 本地、远端 | `5164f0de` | main restoredSearchDiagnostics 改为仅失败收集，替代旧无条件诊断。 |
| `codex/diagnose-sqlite-directory-cleanup` | 本地、远端 | `6088cf66` | 一次性诊断/性能探针，不作为正式回归迁入；原稿保存在恢复 bundle。 |
| `codex/diagnose-workbench-cold-query` | 本地、远端 | `0c11a501` | 一次性诊断/性能探针，不作为正式回归迁入；原稿保存在恢复 bundle。 |
| `codex/document-diff-docx-provider` | 本地 | `6cfbbfdd` | 迁入本 PR：DOCX 有预算读取、Unicode 差异、归一化 worker、测试及 ADR；尚未接入产品，不能视为完整 provider。 |
| `codex/evaluate-incremental-updates` | 本地、远端 | `ccd9095d` | 迁入本 PR：两份 2026-08-29 调研，标明历史基线；不执行生产迁移。 |
| `codex/exchange-replica-test-payloads` | 本地 | `6e378736` | main 已承接目录恢复、准入及 S24 测试基础；S24 完整产品资格仍待验收，不重复搬旧代码。 |
| `codex/fix-atomic-selection-projection` | 本地 | `de1c00c4` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-cdp-listener-owner` | 本地 | `7fda69b6` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-correlated-attachment-actions` | 本地 | `9c8c2b1a` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-correlated-table-admin` | 本地 | `4a65dc50` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-data-import-readiness` | 本地 | `7e0ff62b` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-database-open-authority` | 本地 | `ac0a3bdf` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-dialog-focus-first-gap` | 本地 | `64649a79` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-dialog-focus-release` | 本地 | `b7bcebd4` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-dialog-focus-reprojection` | 本地 | `eb7ce50a` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-e2e-bridge-wait` | 本地 | `253d2007` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-field-protection-admission` | 本地 | `ca54e514` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-plugin-plan-authority` | 本地 | `6366213c` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-process-identity-lifecycle` | 本地 | `9c8c2b1a` | 弃用手工进程谱系实验；main 采用 Windows Job 所有权及进程退出门禁。 |
| `codex/fix-realtime-subscriber-watermark` | 本地 | `0364a8f8` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/fix-s18-recovery-boundary` | 本地 | `b5da83ec` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-search-rebuild-epoch` | 本地 | `14d840e4` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-sidecar-recovery-ack` | 本地 | `66c2183e` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-snapshot-export-lease` | 本地 | `75146bbc` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-table-selection-recovery` | 本地 | `8ff6e8de` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-workspace-activation-budget` | 本地 | `5bcc6fd0` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/fix-workspace-operation-lease` | 本地 | `bb1510b5` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/format-snapshot-tests` | 本地 | `00e7eb44` | 纯格式旧副本，无独有功能。 |
| `codex/formula-author-source-map` | 本地 | `e48dd529` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/formula-calendar-worker` | 本地 | `703349c8` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/formula-clock-contract-worker` | 本地 | `703349c8` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/formula-editor-worker` | 本地 | `14f21565` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/formula-functions-worker` | 本地 | `14f21565` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/formula-semantic-cache` | 本地 | `6631da8e` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/freeze-field-settings-describe-oracle` | 本地 | `50fcb85d` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-lookup-query-oracle` | 本地 | `78f48855` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-lookup-value-page-oracle` | 本地 | `e01cf0ca` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-query-snapshot-oracle` | 本地 | `3d586e20` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-query-view-oracle` | 本地 | `a7d06c12` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-relation-preview-oracle` | 本地 | `e26ca231` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/freeze-relation-search-oracle` | 本地 | `c4df457b` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/gate-packaged-runtime-baseline` | 本地 | `f1b32fcd` | main 已有 packaged runtime baseline / 恢复资格或新版 crash oracle，成熟度计划列明继承。 |
| `codex/implement-legacy-workspace-migration` | 本地 | `8a044a22` | 弃用当前实现；主线成熟度计划 A3 明确暂停 v0.5.0 迁移，历史代码留在恢复 bundle。 |
| `codex/linearize-sidecar-lifecycle` | 本地、远端 | `a020b6aa` | main 的 PocketBaseGenerationContext/生命周期与 detached authority 演进版已承接。 |
| `codex/lookup-aggregation-ui` | 本地 | `c38bfeb3` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/lookup-page-projection` | 本地 | `cd774407` | main 已覆盖并演进；公式作者映射/语义缓存、#397–#402 的函数/时钟/条件与聚合路径；不覆盖回旧稿。 |
| `codex/measure-packaged-recovery-latency` | 本地 | `6affbf88` | main 已有 packaged runtime baseline / 恢复资格或新版 crash oracle，成熟度计划列明继承。 |
| `codex/migrate-e2e-process-scope` | 本地 | `b00fde52` | main 已演进承接旧恢复/生命周期修复（旧 #63–#85 栈）；依据当前调用链与回归，不以相同提交 SHA 作为前提。 |
| `codex/migrate-query-validate-snapshot` | 本地 | `01dd75e6` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/own-relation-write-authority` | 本地、远端 | `6041a958` | main 已有 relation write Product RPC/oracle 与 display label 投影。 |
| `codex/prepare-schema-describe-projection` | 本地 | `c797a51b` | main schema_describe_projection_test.go 保留 lookupRevision 消费断言及 oracle 语料；旧生成器由当前 oracle 路径接续。 |
| `codex/preserve-realtime-subscriber-watermarks` | 本地 | `971197e8` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/profile-first-search-latency` | 本地、远端 | `dc44b3d0` | 一次性诊断/性能探针，不作为正式回归迁入；原稿保存在恢复 bundle。 |
| `codex/project-realtime-recovery-tasks` | 本地 | `bd181163` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/project-relation-display-labels` | 本地 | `940785ee` | main 已有 relation write Product RPC/oracle 与 display label 投影。 |
| `codex/read-bounded-realtime-frames` | 本地 | `f486d61a` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/rebind-relocated-object-repository` | 本地 | `c7e39a66` | main Kopia relocation 临时配置及 relocated workspace 回归已承接。 |
| `codex/recover-product-task-status` | 本地 | `cb4630f4` | main import_fault_outcome 与产品 task 状态恢复已承接。 |
| `codex/recover-renderer-data` | 本地 | `02f93107` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/report-lookup-refresh-outcomes` | 本地 | `f85cf09f` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/reproduce-takeover-authority-handoff` | 本地 | `63d59348` | 旧接管后重建 Host/回写收据方案不迁；main 使用 VerifyCapabilities、epoch lease 与生命周期准入，旧测试依赖已退役的接管编排。 |
| `codex/reset-realtime-session-state` | 本地 | `f0d7a0d7` | main 已承接 applied/retired 回执、generation/epoch 与 bounded realtime/replayedThrough 语义。 |
| `codex/resume-directory-replica-conflict-s24` | 本地、远端 | `301b4a61` | main 已承接目录恢复、准入及 S24 测试基础；S24 完整产品资格仍待验收，不重复搬旧代码。 |
| `codex/retain-updater-failure-evidence` | 本地、远端 | `75c4faba` | 迁入本 PR：按用户明确授权补齐 prepare 失败时七类 updater 诊断白名单上传，保留 3 天，并迁入契约测试。 |
| `codex/route-content-version-owner` | 本地 | `76ea9e37` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-host-device-settings` | 本地 | `b051fd45` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-host-device-settings-next` | 本地 | `aa021c4e` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-events-reconcile` | 本地 | `c559a9da` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-file-list` | 本地 | `d26b1107` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-history-reads` | 本地 | `9fc72478` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-schema-describe` | 本地 | `8f0d6d50` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-schema-describe-next` | 本地 | `5a3b427d` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/route-product-schema-get-table` | 本地 | `5cd911d5` | main 当前 Product RPC、capability manifest 与相应 Python oracle/契约测试已承接。 |
| `codex/stabilize-updater-journal-lock` | 本地、远端 | `a20735ad` | 迁入本 PR：并发 recovery read 不丢失 journal 锁路径的回归。 |
| `codex/test-retention-nonzero-apply` | 本地 | `de577cfb` | 弃用；主线成熟度计划 A1 明确禁止生产 test-only 时钟，以自然老化和领域 Clock 证据替代。 |
| `codex/verify-encrypted-snapshot-inspection` | 本地 | `21dd0204` | main 已有 trusted 元数据门控、Root/PinnedRoots 或自然老化测试/证据；旧断言不回退当前语义。 |
| `codex/verify-retention-natural-aging` | 本地 | `e48daf7e` | main 已有 trusted 元数据门控、Root/PinnedRoots 或自然老化测试/证据；旧断言不回退当前语义。 |
| `codex/verify-retention-roots-baseline` | 本地 | `d042c8dc` | main 已有 trusted 元数据门控、Root/PinnedRoots 或自然老化测试/证据；旧断言不回退当前语义。 |
| `codex/verify-retention-roots-natural-aging` | 本地 | `b55c508e` | main 已有 trusted 元数据门控、Root/PinnedRoots 或自然老化测试/证据；旧断言不回退当前语义。 |
| `codex/verify-snapshot-import-surfaces` | 本地 | `780180b7` | main 已有 trusted 元数据门控、Root/PinnedRoots 或自然老化测试/证据；旧断言不回退当前语义。 |
| `codex/verify-v050-workspace-storage` | 本地 | `df6fd1d3` | 暂停方案；A3 保留 producer/anchor，旧 consumer 不进入当前交付。 |
| `main` | 本地、远端 | `f10d377b` | 保留；默认保护分支。 |

## 已知边界

- DOCX 仅迁移已有实现并适配当前主线。provider/UI 接线、打包和实际峰值内存资格仍未完成，维持草稿。
- PDF 的已有缺口没有在本次修复；参见 [PDF 资格计划](../plans/2026-09-05-pdf-support-qualification.md)。
- Retention、目录镜像、v0.5.0 兼容与 crash 的现行范围见[成熟度收敛计划](../plans/2026-08-29-vibetable-maturity-convergence-and-runtime-evolution.md)。
- 未重跑全栈发布资格；局部测试不能替代 PR required。共享进程测试首次因 Node 工具链缺失失败，使用仓库脚本和同版本缓存补齐后复测。
