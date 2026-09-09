# 产品 E2E 性能基线

本页记录真实发布包在 Windows WPF/WebView2 + Python + PocketBase 进程栈上的首轮可比较基线。它不是浏览器 mock，也不把 20–30 秒的测试等待上限当作性能目标。

## 当前产品 E2E 证据

- source SHA：`GitHub/main@ede74ab2d9880052beb7532f039cd3ac3420611b`
- GitHub run：[main CI 33975773081](https://github.com/FelixJI/VibeTable/actions/runs/33975773081)
- 报告契约：`contractVersion=2.0`
- 结果：23/23 passed、0 failed、0 skipped。
- 当前 manifest gap：6（`26-lookup-definition-read`、`27-relation-target-search`、`28-relation-delta-preview`、`29-lookup-source-pagination`、`30-query-snapshot-validation`、`31-relation-pair-inspection`）。
- 当前 manifest surplus：无。
- 当前 manifest changed：2（`06-relation-fanout`、`07-attachment-history`）。

同编号场景 "06-relation-fanout" 已从公共 cascade 预览改为两端关系字段的真实 UI 编辑、冻结计划与应用，并保留公共 cascade 拒绝边界。上述历史 source/run 的结果只覆盖当时语义，不证明当前 S06 资格；该项待正式 main 打包报告重新验收。本机局部验证不能关闭此证据缺口。gap、surplus 和 changed 均为空才满足发布证据闭合要求。

同编号场景 "07-attachment-history" 现在保留历史抽屉的 Workspace V2 恢复，并新增公开 `history.previewRestoreRequested` / `history.applyRestoreRequested` 桥接闭环，独立验证 Go Product owner。旧 main 报告中的 S07 没有这些 Product 断言；即使场景编号相同，也不能用旧报告证明新增恢复入口。新增段验证预览不改变当前附件、Product 五字段结果不含 `mutationRevision`，以及返回行中的当前存储名与附件权威列表、表/记录/字段身份、原名、既有内容 checksum 和长度一致。恢复会重新生成托管存储名，不要求沿用历史名称。此项仍待包含新断言的本候选同包报告及正式 main 报告验收，S12 的快照恢复结果不能替代它。

场景 "26-lookup-definition-read"、"27-relation-target-search"、"28-relation-delta-preview"、"29-lookup-source-pagination" 和 "30-query-snapshot-validation" 已进入 manifest，尚无覆盖它们的正式 main 打包报告；新加入的 "31-relation-pair-inspection" 同样待取得正式 main 报告；上述 23/23 历史样本不包含这六项新增场景，也不证明新的关系、来源分页与快照校验资格。
- 诊断：0 个未确认 bridge failure、0 个 pending request；诊断记录另有 21 个已确认事件（含预期取消），与性能汇总的 16 次失败统计口径不同。`history.query` 与
  `history.drawer.initialLoad` 均为 `within-budget`。

目录镜像工作区的公开创建、表与记录写入、释放活动缓存、同 UUID 重开，以及精确终止 sidecar 后的替代进程恢复，均已在同一 main 打包候选的场景 23 通过。16 项断言包含单次 `query.page` 与 `replica.status` 观察、精确终态、记录及 revision 保持。全部场景均附着真实 WebView2；正常退出后 Host exit code 为 0、进程组及后代为空、端口已释放。

本结论仅覆盖场景声明的目录副本恢复，不扩展为手动同步、跨设备 offline/reconnect、冲突处理或 exclusive-writer 资格。手动 `replica.synchronize` 继续 Internal only。

后续 CI 的场景 23 按钮超时与场景 14 桥接失败尚未查明根因，详见[历史失败记录](quality/product-e2e-failure-notes.md)。因此本节记录的是一份已通过样本，不代表最新 main 的全场景零失败结论。后续局部测试通过不能替代失败根因确认及当前提交的完整 CI。

该结论来自 run 的 `ci-lane-resilience` 中 `product-e2e-report.json`。lane artifact 按 CI 策略短期
保留，长期出处使用上面的 source SHA、run URL 与报告契约版本；不能用本机临时报告路径替代。

### 该样本的 23 场景耗时（2026-09-06）

| 场景 | 耗时 |
|---|---:|
| `01-offline-first-start` | 6.421s |
| `02-all-field-schema` | 48.319s |
| `03-schema-errors` | 34.583s |
| `04-json-round-trip` | 35.900s |
| `05-formula-lifecycle` | 30.614s |
| `06-relation-fanout` | 16.260s |
| `07-attachment-history` | 23.165s |
| `08-stale-conflict` | 16.044s |
| `09-atomic-import-scale` | 19.081s |
| `10-sse-reconnect` | 28.551s |
| `11-plugin-mutation` | 17.088s |
| `12-backup-consistency` | 53.705s |
| `13-protection-policy` | 8.534s |
| `14-document-diff` | 9.413s |
| `15-workspace-snapshot-package` | 64.465s |
| `16-dashboard-lifecycle` | 47.677s |
| `17-interface-lifecycle` | 28.134s |
| `18-workspace-search` | 55.800s |
| `19-gallery-lifecycle` | 25.694s |
| `20-kanban-lane-drag` | 45.762s |
| `21-calendar-date-move` | 45.006s |
| `22-timeline-date-move` | 36.335s |
| `23-directory-replica-recovery` | 50.376s |

该样本 `history.query` 共 8 次，p50 31.1ms、p95/max 352.6ms，低于 500ms 告警线；
`history.drawer.initialLoad` 共 2 次，p50 140.41ms、p95/max 355.38ms，低于 750ms 告警线。
场景耗时范围为 6.421s–64.465s，均低于 180s 防挂死上限；这些耗时包含应用启动与 fixture
准备，不能解释为单次用户交互延迟，也不据单次样本宣称性能改善。

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
