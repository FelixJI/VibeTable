# Go 权威实时恢复流资格

本包为 L4 提供完整的 Go v2 恢复传输：当前活动公式任务、保留窗口内原始终态通知、事务水位和订阅者隔离一起交付。旧 v1 与 Python 消费路径尚未切换；Host/Web 原子切流是另一完整交付，不将本包称为全 L4 已完成。

## 当前实现与基线

续用既有 `recover-authoritative-realtime-stream`，历史提交 `ee00f3e16ee023d755e97d6d710f89f4976a1361`，正常合入 `main@8711f460f92a59c8afe925785a3ef29b58b8fcf7`，无冲突。相对主干只包含恢复流、jobs 当前投影、subscriber 私有水位、相邻回归及 ADR/本资格记录；不回灌旧 Host/Web 分支的 schema/lookup owner 或生成物。

`GET /api/vibetable/v2/events` 在 cold/gap 时由根 App 的同一可取消事务读取水位 H、完整活动集与有限历史通知。只有本 subscriber 以 H 为已回放水位，不能吞掉旧 subscriber 尚未发布的持久事件。合法 cursor 保持增量读取，未知/未来/损坏 cursor 拒绝。

活动公式任务上限 10,000；完整恢复 JSON 上限 4 MiB，失败不截断、不交付部分帧、不推进 bookmark。cold/valid/live 共用持久事件校验；外层未提交 App 事务拒绝。权威当前状态与历史通知分开，保留 Python 本地 import/export 的 owner。

## 当前本地验证

复用已有 Go 1.27、模块/编译缓存与共享 uv 环境。没有重建环境、升级依赖或更改 lock/CI。

```text
go test -race -json ./internal/realtime ./internal/jobs ./internal/app ./tests/integration -run '^(TestRealtime|TestTerminalTaskEventsCommitAtomicallyBeforeLivePublish|TestFormulaBackfill|TestFormulaFanout|TestResumePending)' -count=1
```

43 项 tests/subtests PASS，0 fail；包耗时：jobs 1.726s、realtime 1.741s、app 8.122s、integration 305.543s。包含真实 10,000 outbox 保留/cursor 分类、活动/终态恢复、损坏、取消、注册容量、wire预算、队列溢出和多订阅水位。完整原始事件在 `build/realtime-latest-main-focused.jsonl`，不是只读日志归纳冒充重跑。

新增真实生产进程子测试沿用 `TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart`：无认证 401，认证后 200/SSE，空库第一帧精确 `rt:0` / `realtime.recovered`，闭合四字段 payload 和两空数组；关闭响应后继续既有持久化、正常退出及重启契约。

```text
go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1
```

- 旧 PR294 `7fa315c96108d75e219f49939d0659e6fce3f68c` 与 main8711 tree 等价。仅用 Go `-overlay` 注入新增测试，不改基线源码：RED，合法认证后接口404，9.201s。日志 `build/realtime-production-baseline-red.log`。
- 当前恢复实现：GREEN，10.123s。日志 `build/realtime-production-current-green.log`。这是实际子进程生产接线资格，不代替未来 WPF 消费资格。
- `go vet ./...` EXIT0，日志 `build/realtime-latest-main-vet.log`。
- 独立 Standards 与 Spec 对恢复增量及新进程测试均无未解决项。

## 历史失败与验收边界

旧实现曾取得 43 项聚焦通过；完整 Go 留有 workspacev2 TempDir RemoveAll 目录非空失败，并曾在 code review 发现请求取消、持久事件校验和 JSON null 兼容问题。真实 RED 与修正记录保存在既有 `build/progress.md` 及历史提交，没有把失败删掉或以此宣称全套通过。

当前完整 Go 入口 `uv run --frozen --no-sync python qa/next.py --stage go-test` 已在冻结提交 ffc39038 上运行并失败：既有 workspacev2/history 场景在 TempDir RemoveAll 清理时报目录非空；记录见 `build/realtime-full-go-test.log`。脚本原有有限重试未使其通过，没有修改重试或门禁。相关恢复 integration 通过，失败不能当作完整质量通过，也不据此断言外部杀软或文件占用根因。Go format 检查通过。完整多栈质量与 Go coverage 尚未运行；不得将相关验证写成完整 quality PASS。最终 fresh PR `required`、squash 与合并后 CI/CD 仍待完成。既有旧 Host 分支的 S10 报告不能替代最新源码消费者的产品验收。

L4 最终完成还需：整合已存在的 Host/Web 半成品，保持最新 main 的 owner 与共享绑定；以完整事务切换到 Go→WPF，删除 Python SSE supervisor、latest revision cache 和二次包装，保留本地任务 producer；新构建上的 S10、旧 epoch/ABA、duplicate/gap、正常关闭和端口清理均须有适用证据。

## 最新 CI 超时与夹具修复

`0f171e02` 的 CI `34241974199` 在 race-a 失败：`TestRealtimeOutboxRetainsTenThousandAndClassifiesDurableCursors` 触发既有五分钟上限，堆栈仍在10,005条fixture准备循环的 PocketBase Save。没有将 required 失败当作通过，也没有调整timeout、窗口或重试策略。

先测量仅合并事务的版本，定向 race PASS 181.193s，改善有限，未采用。最终将前10,000条真实事件序列化后通过单条绑定参数 SQL 有序准备，每行仍执行原生产 retention trigger；最后五条边界写入保留 PocketBase Save。原来的10,000窗口、9,999续读、四类cursor、活动恢复和水位一致性断言全部保留。

`go test -race ./tests/integration -run '^TestRealtimeOutboxRetainsTenThousandAndClassifiesDurableCursors$' -count=1 -timeout=5m`：PASS 160.178s，日志 `build/realtime-retention-bulk-race.log`。事务实验日志 `build/realtime-retention-transaction-race.log` 保留。独立 Standards / Spec 增量各0；生产代码未变。此本地改善不替代修复提交上的 fresh CI，远端是否消除超时仍待确认。
