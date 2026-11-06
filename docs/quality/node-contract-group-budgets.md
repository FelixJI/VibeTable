# Node 契约组预算

Node 契约由 `tests/e2e/test_product_e2e_runner.py` 使用仓库锁定的 Node 运行。
纯协议契约与真实 Edge 夹具分别运行，浏览器启动不占用协议组或页面断言的预算。

| 组 | 文件 | Node 进程预算 |
| --- | --- | --- |
| 协议 | `test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime` 中的清单 | 30 秒 |
| 主题 | `theme_surface_probe.test.mjs` | 启动 30 秒 + 断言 10 秒 + 关闭 35 秒 |
| Lookup 视口 | `lookup_sources_viewport.test.mjs` | 启动 30 秒 + 旅程 15 秒 + 关闭 35 秒 |

浏览器夹具使用独立的有界 before/after 钩子，阶段失败保留诊断。
关闭预算 35 秒必须大于锁定 playwright-core 自身 `browser.close()` 的优雅关闭/杀进程窗口
（`DEFAULT_PLAYWRIGHT_TIMEOUT` 30 秒），另留 5 秒完成退出观察。
该值与产品 `LIFECYCLE_EXIT_TIMEOUT_SECONDS = 35.0`（30 秒 + 5 秒余量）一致。
外层 `_run_node_contract_files` 在超时或失败时仍检查并回收自己的 Windows Job，
Job 清理预算另计最多 5 秒等待和 5 秒终止。失败、未回收子进程或关闭错误均拒绝通过。
新增浏览器契约不得重新混入协议组；聚合回归检查两个浏览器文件的隔离与独立预算。

## 验证

```powershell
uv run --frozen --no-sync python -m pytest tests/e2e/test_product_e2e_runner.py tests/contract/test_product_e2e_capability_index.py -q --no-cov
```

背景见 [Issue #562](https://github.com/FelixJI/VibeTable/issues/562)：合并后质量检查的
50 文件串行组在 30 秒处超时，组内 Lookup 浏览器测试先触发自己的 15 秒超时。
本地对照确认浏览器夹具显著消耗组预算；日志不足以确定 CI 中每个浏览器阶段变慢的原因。
拆分保留全部文件和旅程断言，不承诺任意慢环境都能通过，也不增加自动重试。
PR #563 的 CI 42234765592 中，协议组与 Lookup 独立组通过；主题断言主体通过后，
Edge 关闭耗时约 19 秒，进程以 exitCode 0 退出。旧 10 秒清理预算早于库的关闭窗口
判失败。日志未确定关闭较慢的底层原因；不据此断言所有资源泄漏或环境问题均已排除。
