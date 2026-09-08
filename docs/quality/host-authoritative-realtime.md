# Go→WPF 实时恢复接入资格

本变更完成 PR #140 L4 的生产消费切换：Go 持有 cursor、恢复水位、当前公式任务和 revision；WPF 持有连接代际、session/epoch 和实际 renderer 投递；Web 以实际刷新回执清除恢复 dirty。删除 Python SSE supervisor、latest revision cache 和 data.changed 二次包装，保留 import/export 与插件任务 producer。task.changed 的 renderer envelope 为 Host owner，不代表所有底层任务都已迁往 Go/Worker。

## 当前来源与范围

续用已有 Host/Web 半成品，正常合入 main `8711f460`，冻结实现提交 `e8b093b93ce2f31fb4c9a6dc095ffe56e6feff3b`。保留主线的 schema/query/lookup 17 个 Go RPC owner、设备偏好 Host owner、composition 和同表 selection 语义；不重新注册旧 app/realtime_product_rpc.go。

Go producer 已独立提交 PR #297；本分支目前包含相同 producer 及真实进程回归，须在 #297 合入后同步主线，最终消费者 PR 不重复交付该基础包。共享 catalog/grid/Lookup/Dashboard 改动均服务于完整恢复；没有变更 CI、依赖、历史兼容验收或零写入拒绝契约。

## 本地验证

- `uv run --frozen --no-sync python scripts/automation_project.py python-quality`：PASS；1721 passed、1 skipped，coverage 91.42%，Ruff/Pyright/mypy 通过。日志 `build/realtime-merged-python-quality-formatted.log`。此前聚焦 93 项全通过但全仓 coverage 52.55% 失败、初次完整入口仅格式失败均保留。
- Web `npm run typecheck` PASS；`npm run test -- --reporter=default --reporter=junit --outputFile.junit=../../build/realtime-merged-web.junit.xml`：174 文件、1488 tests PASS。日志 `build/realtime-merged-web-tests.log`。
- `.NET test desktop/VibeTable.Desktop.sln --configuration Release --no-restore`：六项目 1296 PASS、1 skipped，零失败。TRX `build/realtime-merged-dotnet/`。`uv run --frozen --no-sync python qa/next.py --stage dotnet` 通过配置声明的全部覆盖率门禁；Desktop line 70.86%、branch 61.37%。日志 `build/realtime-current-dotnet-coverage.log`。
- Go task 版 `TestRealtimeCatchupDoesNotSkipPendingPublicationForExistingSubscriber` race PASS 4.595s。首次筛选没有命中 capability/dispatcher，不算其测试通过；随后两包全量暴露旧 L1 data.changed owner 断言，修正为 Go data.changed/realtime.recovered 与 Host task.changed 后，两包全量 race PASS。原 RED 与 corrected GREEN 日志分别为 `build/realtime-merged-go-capabilities-dispatcher.log`、`build/realtime-merged-go-capabilities-dispatcher-corrected.log`。
- producer 的 43 项 race、真实生产接口 RED/GREEN 见 #297 资格。完整 Go 仍留有既有 TempDir RemoveAll 目录非空失败；不宣称全 Go/完整多栈 quality PASS，Go coverage 未完成。
- 冻结实现独立双轴 Standards 0 / Spec 0；Go owner 测试与此文档为随后的一致性增量，需补复核。

## 最新实际产品证据

`uv run --frozen --no-sync python scripts/build_next.py` 完整构建成功；复用已装 Node/.NET/Go/uv 环境，未使用 skip-build 或旧 0.5.0 产品。包 `dist/VibeTable.Next` 对应源码 `e8b093b93ce2`，四组件 freshness 通过。其后仅测试/资格文档变化，不影响产品构建内容。

通过 `qa/product_acceptance.py --package-root dist/VibeTable.Next --evidence-root build/qa/p --scenario <id>` 驱动真实 WPF WebView2 的 CDP 页面，不另起浏览器。

| 场景 | report 运行目录 | 耗时 ms | 断言 |
|---|---|---:|---:|
| 10-sse-reconnect | 20260908T144434Z | 10322 | 18 |
| 04-json-round-trip | 20260908T144615Z | 9489 | 20 |
| 09-atomic-import-scale | 20260908T144615Z | 7517 | 10 |
| 15-workspace-snapshot-package | 20260908T144615Z | 25371 | 14 |
| 16-dashboard-lifecycle | 20260908T144615Z | 26726 | 17 |
| 29-lookup-source-pagination | 20260908T144615Z | 6845 | 9 |

两个 `build/qa/p/<运行目录>/product-e2e-report.json` 共 6/6 PASS、0 fail、0 skip；所有 pageErrors、异常 bridge failures/pending 为空，Host 正常退出 0、无 members/descendants 遗留、端口释放及 owner lease/final cleanup 通过。

S10 证明真实 sidecar 重启后选择保持、后续写入与原位刷新不重复；Python 退出后新 epoch 收到完整 Go recovered shape，旧 epoch 写入被拒绝，新会话继续写入。S04/S09 证明保留的 import/export 路径，S15/S16/S29 证明换代和相邻消费者，没有把这些场景等同于所有故障注入。

## L4 故障要求的适用证据

- cursor gap：Go `TestRealtimeOutboxRetainsTenThousandAndClassifiesDurableCursors` 在真实 10,000 保留窗口外恢复权威状态与 H。
- duplicate：Go private-watermark 回归及 Web ordinary/concurrent/retained-terminal→live deduplication 回归。
- ABA、旧 epoch late：Host `RetiredSidecarDropsQueuedPostEvenWhenTheWorkspaceIdentityIsUnchanged`、`RetiredRendererDropsQueuedRecoveryAndReopensColdWithoutWaitingForUi`、`EpochDrainCancelsQueuedDeliveryWithoutReportingAnOperationalFailure` 和关闭投递回归；是实际 session/generation 语义测试，不声称 OS PID/端口复用注入。
- 投递与恢复失败：Host actual Post 才推进 bookmark，catalog/queued/retired/closing 均 fail closed；Web 表页、Relation/Lookup、Dashboard 等待实际刷新，隐藏/草稿/失败保留 dirty。
- 正常关闭与端口：上述新 S10 lifecycle。

S10 单独不证明 cursor-gap、重复注入或 ABA；这些由对应 Go/Host/Web 契约测试证明。旧 Sep3 S10 仅为历史证据。

## 未完成交付

最终消费者 PR 的最新 main 同步、fresh required、review conversation、squash、合并后 CI/CD 仍须完成。此页记录本地资格，不声明 PR140 全部完成，也不替代尚未实施的 L5–L10。