# 真实包运行时基线

`uv run python -m tests.e2e.packaged_runtime_baseline` 在一个新宿主进程、独立 WebView2 用户目录和新 workspace 中测量。输入必须通过现有候选校验，报告绑定包归档和 `build-identity.json` 的 source SHA；版本号相同不代表源码相同。

```powershell
uv run python -m tests.e2e.packaged_runtime_baseline `
  --package-root dist/VibeTable.Next `
  --package-archive build/automation/artifacts/VibeTable-v0.5.1-win-x64.zip `
  --build-identity build/automation/artifacts/build-identity.json `
  --source-sha <candidate-source-sha> `
  --workspace-root build/qa/runtime-baseline/workspace `
  --evidence-root build/qa/runtime-baseline/evidence `
  --json-report build/qa/runtime-baseline/report.json
```

包、归档和 identity 必须来自同一次实际构建；示例版本随候选变化。workspace 和 evidence 使用新的运行目录，报告不能与候选或运行目录重叠。优先复用已验证、适合当前验证目的的新版候选，不为运行探针重建依赖环境。0.5.0 仅用于旧数据兼容或升级起点。

## RPC 采样

首表稳定计时结束、静默内存采样完成后，探针通过 CDP 连接同一真实桌面宿主的 WebView2，依次执行 `schema.getTable` 和 `query.page` 各 30 次。请求使用正式 workspace wire allocator，目标是本次创建的同一张空表；查询为无筛选、无排序、offset 0、limit 100。schema revision 或表身份改变、非空查询结果、错误回复、无计时或超时都会使运行失败。

每个请求使用页面内 `performance.now()`，从 `postMessage` 前计时，到第一次匹配 requestId 的回复。Playwright 轮询、进程间观测和 Node 启动时间不计入 RPC 耗时。所有请求串行且保留首个样本；这是首表建立后的读取负载，不声称清空了 OS 缓存，也不声称代表所有 RPC 或生产数据规模。

`rpcLatency.methods` 保留样本顺序、count、firstMs、p50Ms、p95Ms 和 maxMs。百分位采用 nearest-rank，即排序后第 `ceil(n × p)` 个样本。原始 RPC payload 不进入报告。失败报告保持 `coverage.rpcLatency: not-measured`，成功报告仅在宿主正常退出、进程和端口及 owner lease 清理通过后写入。

没有新增性能合格阈值。`coverage.recovery` 仍为 `not-measured`；此测量不替代 kill/recovery 验证，也不据此宣布 L0 全部完成。

## 2026-09-09 主干固定样本

[main CI 34309394462](https://github.com/FelixJI/VibeTable/actions/runs/34309394462) 的 `ci-lane-resilience` / `lane-evidence/resilience/packaged-runtime-baseline.json` 绑定 source `25b260394a0a01e8432d23fa3d1a6e8b9b65922f`。报告 passed，四组件 freshness、正常退出和端口清理通过，errors 为空。完整 CI 与关联 CD 状态见[当前产品 E2E 证据](../e2e-performance.md#当前产品-e2e-证据)。

| 测量 | 本次样本 |
|---|---:|
| launch → Host ready | 1878.3761ms |
| workspace open request → opened | 4345.8069ms |
| workspace open request → first table stable | 8374.3995ms |
| quiet endpoint working set：Host / Python / Go | 258797568 / 71327744 / 211374080 bytes |
| quiet endpoint working set 合计 | 541499392 bytes |
| 展开包：Host / Python / Go / Web / 未分类 | 71822180 / 34445775 / 134173027 / 15887786 / 4248065 bytes |
| 展开包总计 | 260576833 bytes |
| schema.getTable / query.page p95 | 8.4 / 4.2ms |

这是单次新进程、独立 WebView2 目录下的样本，working set 不是峰值或全设备 RSS。展开包字节不是 ZIP 下载体积。此报告的 recovery 仍为 not-measured；同一 CI 的产品 E2E 报告另有 S10 故障恢复计时，见规范证据页，不能把两种测量来源混写。尚未完成 Worker 拓扑后的前后对比，不据本次数字宣称性能改善或 L0/L10 全部完成。
