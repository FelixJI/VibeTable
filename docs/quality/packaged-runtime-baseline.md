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
