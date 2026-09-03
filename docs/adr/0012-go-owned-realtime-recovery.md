# ADR 0012：Go 恢复当前活动投影并保留终态通知

- 状态：Accepted
- 日期：2026-09-03

Go 持有实时游标、去重与 authority revision；WPF 只持有连接代际、session/epoch 和 renderer 投递生命周期。outbox 只保留最近 10,000 条，不能用旧事件聚合代替 PocketBase 当前状态；也不建立 WPF 任务权威缓存或 renderer→Host 已知任务 ID 握手。

`GET /api/vibetable/v2/events` 在空 cursor 或 retention 缺口时，由 Go 在同一事务与 subscriber 注册同步窗口内捕获水位 H、完整活动公式任务和保留窗口内原始终态通知。首帧 `realtime.recovered` 的 `activeFormulaTasks` 是当前状态，不伪造 eventId/occurredAt；`terminalNotifications` 保留原事件身份、时间和错误，但绝不能覆盖 H 时刻已经 resume 的活动任务。之后只交付 H 之后的事件；恢复水位只属于本 subscriber，不推进其他 subscriber 的全局 drain。

- `rt:0` 是明确的空 outbox 锚点，不代表一条虚构的事件。合法保留 cursor 继续增量重放；非法、未来、未知 cursor 与存储损坏均失败，不降级为空状态。
- 活动集沿用 Go 启动恢复的 10,000 上限，32 仅是调度并发数。完整恢复帧的 JSON 载荷最多 4 MiB（不含 SSE 的 id/event/data 行封套），参考 Product Go gateway 的响应预算；这是 wire 预算，不是硬 RAM 上限。超量或解码失败时不截断、不交付帧、不推进 bookmark，并释放订阅。
- 本接口只恢复 `formula_backfill` / `formula_fanout`。共表的字段迁移、资源清理不是公式任务；Python 导入/导出任务与轮询保持自己的 owner。
- 这是当前活动投影与有限通知窗口，不是完整 durable 任务历史。不存在的旧活动项只移除，不合成成功；窗口外的终态提示不作承诺。仅 active 的方案被否决，因为现有空 cursor 重启契约仍要求保留窗口内的成功、失败、取消通知。

本次仅新增 Go v2 transport，现有 v1/Python 生产路径保持不变。后续紧邻切片先准备 renderer 消费者，再由 WPF 原子切流并删除 Python SSE supervisor/latest revision cache/二次包装：仅 app.ready 后且 UI 实际投递时仍属当前 generation 才可交付，epoch 退休不能沿用未投递恢复帧的 bookmark。renderer 必须真正重读目录/当前表页、Relation/Lookup、Dashboard 目录/定义/panels，隐藏消费者保存 dirty 状态；刷新失败不得冒充恢复完成。完整 L4 验收仍需新路径场景 10、recycle/gap/duplicate/ABA/late event/正常关闭与端口清理。
