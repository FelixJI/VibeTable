# 打包产品恢复时延

`uv run python -m tests.e2e.product_e2e_runner --package-root dist/VibeTable.Next --evidence-root build/qa/recovery-baseline --scenario 10-sse-reconnect` 复用现有真实 WPF/WebView2 S10：精确终止受控子进程，等待 sidecar 恢复，随后终止 BFF 并通过正式 workspace close/open 重开。所有原断言、超时、旧 epoch 拒绝和正常退出清理保持不变。

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
