# Retention 自然老化验收记录

本记录仅证明非零逻辑清理，不把 Apply 的零物理回收字节数视为失败，也不替代 grace 后的物理 Sweep 资格。

## 冻结样本与执行

- 候选源提交：`b55c508e01fb5d04b47ef3f5d8177ad12092b9d6`，包含快照独立根集合修复与两阶段自然老化入口；不是当前 main 构建的验收声明。
- seed 报告：`20260906T105327Z`，完成时间 `2026-09-06T10:53:54.128832Z`；最早 resume 时间 `2026-09-07T10:53:54.128832Z`。
- resume 报告：`20260907T105945Z`，通过真实 packaged WPF/WebView2 和 `playwright-core.connectOverCDP` 执行；没有启动替代浏览器、改动时钟、重建候选或改写 seed 数据。
- workspace UUID：`37f40923-e1d7-49a9-85f2-af10052a9070`。原始报告保留在该验收工作树的 `build/qa/natural-retention-aging/attempt-1/evidence/natural-retention-aging/` 下，seed/resume 子目录各自保留对应报告。

复现入口见 [QA 说明](../../qa/README.md)。本次两阶段命令分别使用 `seed` / `resume`，其余参数相同：

```powershell
uv run --frozen --no-sync python qa/retention_natural_aging.py resume --state build/qa/natural-retention-aging/attempt-1/state.json --package-root dist/VibeTable.Next
```

## 实际结果

resume 退出码 0，1/1 场景、8/8 断言通过。首次 plan 的 `reclaimableBytes=617044`；Apply 返回 `deletedObjects=3`、`reclaimedBytes=0`。老快照 `c97101ff-32a9-4590-96b2-7f0d2ab97ded` 从公开列表移除，较新的 pinned 快照 `7547a60c-e3b8-4aae-8afe-bffc19ec2f43` 保留。第二次 plan 的候选字节数为 0，第二次 Apply 的删除数和回收字节数均为 0。

桥接无失败或 pending 请求，renderer 无错误或外部 HTTP 请求。Host 正常退出码 0，owned members/descendants 为空，端口已释放，owner lease 和最终清理均 passed。

## 资格边界

这是自然等待超过 24 小时后的产品逻辑清理样本；不是自然等待 90 天的物理回收实验。物理 Sweep 的独立证据见下节，不能由本报告的零回收字节数推断。迁入最新代码的验收入口还须经过对应 PR 的相关测试、双轴审查和完整 fresh CI；本机冻结候选的通过不替代该门禁。

## 独立物理 Sweep 验证

2026-09-07，在入口与证据提交 `6a236532e8ea41c2d090f6b8ada55ca19e5ea693` 上复用现有 Go 环境执行：

```powershell
cd sidecar
go test -race ./internal/workspacev2 -run '^TestRetainedSnapshotProtectsHistoryOnlyObjectsThroughMaintenance$' -count=1
```

结果 passed，6.912 秒。该既有测试使用真实 Kopia repository、受控领域 Clock 和工作区 runtime：Apply 后推进 91 天，Sweep 完成 1 个对象的物理退休，仓库 revision 推进且 verification 执行；退休对象 Open 返回 NotFound、CompletedRetirements 有记录，受保留快照及历史对象仍可读。关闭并重新打开 runtime 后再次 Sweep 的删除数为 0。

此结论是物理退休与重启幂等的领域验证，不保证新写入的 pack 当轮缩小，也不声称真实等待 90 天。它与前述 packaged 自然老化样本分别保留报告和结论；当前 PR 的完整 fresh CI 与合并后门禁仍待完成。