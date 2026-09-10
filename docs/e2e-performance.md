# 产品 E2E 性能基线

本页记录真实发布包在 Windows WPF/WebView2 + Python + PocketBase 进程栈上的首轮可比较基线。它不是浏览器 mock，也不把 20–30 秒的测试等待上限当作性能目标。

## 当前产品 E2E 证据

- source SHA：`GitHub/main@25b260394a0a01e8432d23fa3d1a6e8b9b65922f`
- GitHub run：[main CI 34309394462](https://github.com/FelixJI/VibeTable/actions/runs/34309394462)
- 报告契约：`contractVersion=2.0`
- 结果：29/29 passed、0 failed、0 skipped。
- 当前 manifest gap：无。
- 当前 manifest surplus：无。
- 当前 manifest changed：1（`17-interface-lifecycle`）。

该 main 候选覆盖当时全部场景：01–23、26–31，累计 382 项断言通过。S06 两端关系字段的真实 UI 编辑、冻结计划与应用，以及 S26–S31 的 Lookup 描述、关系搜索与预览、来源分页、查询快照与关系完整性检查，均获得本次打包报告；不再沿用旧 S06 语义或六场缺口。

当前分支为 S17 新增 sidecar 重启后的 fresh Interface list/load、完整定义及 revision 持久和 UI 删除断言；这部分尚待最终源码的真实包资格，不由上述旧语义的 S17 报告证明。

四组件 desktop-host、web-grid、python-backend、pocketbase-sidecar freshness 全部通过。所有场景附着真实 WPF/WebView2，Node/Host 正常退出，进程组及后代为空、端口与 owner lease 清理通过。诊断为 0 个未确认 bridge failure、0 个 pending request，另有 33 个已确认事件（含预期取消）；性能汇总的 28 次失败使用不同口径，不能称为全程没有拒绝响应。

S23 证明目录副本的公开创建、读写、释放活动缓存、同 UUID 重开与 sidecar 替代进程恢复。此范围不扩展为手动同步、跨设备 offline/reconnect、冲突处理或 exclusive-writer 资格；手动 `replica.synchronize` 保持 Internal only。S12/S14/S23 本次均通过；[历史失败记录](quality/product-e2e-failure-notes.md)继续保留原始出处与未归因状态，本次通过不替代历史根因分析。

报告来自该 run 的 `ci-lane-resilience`，内部路径 `lane-evidence/resilience/20260909T042302Z/product-e2e-report.json`。artifact 按 CI 策略短期保留，长期出处为上述 source、run 与报告契约。这里只声明该固定主干样本，不代表后续所有提交均通过，也不以一次样本宣称性能改善。

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

S10 恢复测量使用 Node 单调时钟：sidecar kill→可读表 2273.97ms，backend 退出后关闭工作区 2664.85ms、重开 2590.33ms，backend kill→可写 session 5747.57ms。它们是本次故障注入样本，不是普通 RPC 延迟或新增性能门槛。

## 本次主干 CI/CD 归属

上述 main CI 的 `required` 成功，core、resilience、release lane 均成功，固定候选构建与 package contract 通过。关联 CD `34312545249` 成功：Stage release 执行，正式 Publish/attestation 步骤跳过，普通合并未发布。先前 main312 的覆盖率收集失败仍为历史失败，不能改写为该 SHA 通过；本次是后继主干的完整资格。

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
