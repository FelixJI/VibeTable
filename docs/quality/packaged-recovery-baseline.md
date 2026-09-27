# 打包产品恢复时延

`uv run python -m tests.e2e.product_e2e_runner --package-root dist/VibeTable.Next --evidence-root build/qa/recovery-baseline --scenario 10-sse-reconnect` 复用现有真实 WPF/WebView2 S10：先经显式产品请求按需启动 Python，随后精确终止受控子进程，等待 sidecar 恢复，再终止 BFF 并通过正式 workspace close/open 重开。重开取得的新可写 session 只证明工作区准入恢复，不意味着后台存在常驻或已重启的 Python backend。所有原断言、超时、旧 epoch 拒绝和正常退出清理保持不变。

结果在 `product-e2e-report.json` 的 `performance.recovery` 中；单场景原始 `uiTimings` 保留以下四项，汇总不将它们混入普通 `byUiAction`：

| 名称 | 起点 | 终点 |
| --- | --- | --- |
| recovery.sidecar.killToReadableTable | 发出受控 sidecar kill 请求前 | 原选中表的 query.page 成功返回预期行数及有效 schemaRevision |
| recovery.backend.killToWritableSession | 发出受控 BFF kill 请求前 | 重开后验证同 workspace 的新可写 epoch |
| recovery.workspace.closeAfterBackendExit | 故障后发送 workspace.close 前 | 验证返回 closed 且 workspaceId 为空 |
| recovery.workspace.reopenAfterBackendExit | 点击该 workspace 前 | 验证新可写 epoch |

使用 Node 进程内 `performance.now()` 的单调时钟，单位毫秒。这些是恢复流程的观察时间，包含控制请求往返、等待轮询与自动 UI 操作；BFF 总时间还包含正式关闭、进入 workspace center 和重开。它们不是单次 RPC 延迟，也不代表用户手动操作的思考时间。

只有 S10 整体成功、最终宿主生命周期与进程清理成功、四个闭集计时各恰好出现一次且均有限非负时，样本进入 `runs`。没有 S10、计时不全或包含失败运行时，`status` 为 `not-measured`，`unmeasuredRuns` 明确计数；已成功样本仍可保留，但不能据此宣称整个批次测量完成。没有新增性能阈值。

L0 基线应引用同一源码候选的身份、RPC/启动基线与本恢复报告，不能把不同候选的数字拼成单一版本基线。普通 RPC 基线中的 recovery 未测量标记不会被这个独立产品场景自动改写。比较优化前后时须保持候选来源、S10 夹具和此处计时边界一致。

## 主干固定样本恢复计时对照

下表是恢复时延的唯一权威当前表，性能页不维护第二套恢复数字。最终样本来自 main@0bdbc1b 主干 CI 36333755042 的 `resilience` 分片 S10（见[当前产品 E2E 证据](../e2e-performance.md#当前产品-e2e-证据)）；前一固定样本为 61d（CI 36316404213）；L0 历史样本为 25b26039（常驻三进程）。

| 计时项 | 最终样本 2026-09-27（0bdbc1b） | 前一固定样本 2026-09-27（61d） | L0 历史 2026-09-09（25b26039，常驻） |
|---|---:|---:|---:|
| recovery.sidecar.killToReadableTable | 956.26ms | 909.71ms | 2273.97ms |
| recovery.workspace.closeAfterBackendExit | 1166.22ms | 1571.2ms | 2664.85ms |
| recovery.workspace.reopenAfterBackendExit | 981.03ms | 1029.19ms | 2590.33ms |
| recovery.backend.killToWritableSession | 2786.19ms | 3508.82ms | 5747.57ms |

三次样本使用相同的四项计时边界，但夹具启动前提从 L0 的常驻 Python 改为按需显式启动，阶段外行为不同；均为单次故障注入样本、OS 文件缓存未清，不构成因果或稳定收益证明，也不新增性能阈值。最终样本四项均低于 L0，其中三项也低于 61d 前一样本，如实记录为单次样本观察。
