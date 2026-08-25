# 产品 E2E 性能基线

本页记录真实发布包在 Windows WPF/WebView2 + Python + PocketBase 进程栈上的首轮可比较基线。它不是浏览器 mock，也不把 20–30 秒的测试等待上限当作性能目标。

## 当前产品 E2E 证据

- source SHA：`GitHub/main@f9f83cbb8c7a950e054ba534ac64829bb7c912c7`
- GitHub run：[main CI 32865525272](https://github.com/FelixJI/VibeTable/actions/runs/32865525272)
- 报告契约：`contractVersion=2.0`
- 结果：19/19 passed、0 failed、0 skipped。
- 当前 manifest gap：1（`20-kanban-lane-drag`）。
- 当前 manifest surplus：无。
- 诊断：0 个未确认 bridge failure、0 个 pending request；`history.query` 与
  `history.drawer.initialLoad` 均为 `within-budget`。

该结论来自 run 的 `ci-lane-resilience` 中 `product-e2e-report.json`。lane artifact 按 CI 策略短期
保留，长期出处使用上面的 source SHA、run URL 与报告契约版本；不能用本机临时报告路径替代。

### 当前 19 场景耗时（2026-08-25）

| 场景 | 耗时 |
|---|---:|
| `01-offline-first-start` | 82.927s |
| `02-all-field-schema` | 54.589s |
| `03-schema-errors` | 38.731s |
| `04-json-round-trip` | 36.833s |
| `05-formula-lifecycle` | 17.229s |
| `06-relation-fanout` | 16.298s |
| `07-attachment-history` | 29.586s |
| `08-stale-conflict` | 17.158s |
| `09-atomic-import-scale` | 18.701s |
| `10-sse-reconnect` | 31.423s |
| `11-plugin-mutation` | 20.012s |
| `12-backup-consistency` | 49.170s |
| `13-protection-policy` | 9.849s |
| `14-document-diff` | 10.575s |
| `15-workspace-snapshot-package` | 63.372s |
| `16-dashboard-lifecycle` | 50.208s |
| `17-interface-lifecycle` | 33.150s |
| `18-workspace-search` | 54.105s |
| `19-gallery-lifecycle` | 23.794s |

当前 `history.query` 共 8 次，p50 26.8ms、p95/max 317.4ms，低于 500ms 告警线；
`history.drawer.initialLoad` 共 2 次，p50 119.39ms、p95/max 317.69ms，低于 750ms 告警线。
场景耗时范围为 9.849s–82.927s，均低于 180s 防挂死上限；这些耗时包含应用启动与 fixture
准备，不能解释为单次用户交互延迟。

## 测量口径

- `场景耗时`：Playwright 连接到真实 WebView2 后，到场景断言与证据采集前的业务步骤耗时；包含建表、导入、故障注入等测试准备。
- `bridge 往返`：renderer 发出带 `requestId` 的请求，到对应成功响应或 `operation.failed` 的耗时。
- `历史抽屉首屏`：点击历史按钮，到 `history-timeline` 可见；覆盖 UI 触发、宿主、Python、sidecar 查询与 Vue 渲染。
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
