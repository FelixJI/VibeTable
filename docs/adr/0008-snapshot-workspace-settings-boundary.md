# ADR 0008：Snapshot 工作区设置边界

- 状态：已接受
- 日期：2026-07-29
- 适用格式：workspace format 1 / protocol 2.0

## 决策

Snapshot 的 `workspace-settings` 对象使用 closed、版本化的
`formatVersion: 1` 结构。它只投影 coordination DB 中由工作区拥有、但不在可恢复业务
数据库内的设置。当前清单为：

- Snapshot 保留天数、最近条数和时间分桶；
- 文件自动修订保留天数、最近条数和时间分桶；
- workspace repository 磁盘限额。

`policyRevision` 和 `mutationRevision` 是并发控制元数据，不是用户设置。恢复时比较并列出
`workspace-settings:retention` 差异；应用目标值时在当前权威行上递增
`policyRevision`，不把旧 revision 倒退。

以下内容明确不进入 `workspace-settings` 对象：

- 业务库中的共享设置（例如工作日历）；它们已随一致性 SQLite 对象恢复；
- 全局设置（语言、主题、启动页、产品 UI 偏好）；
- 设备级 Registry、recent tables、窗口/UI 状态和本机路径；
- repository 密钥材料、凭据、设备身份；
- lease、claim、session、RPC receipt、restore/rotation plan；
- 同步、完整性、quota 告警、缓存和健康状态。

恢复 journal 位于 coordination 目录。新 journal 在安装前把当前权威设置投影写入 rollback
区，随后才进入 `installing`；中断恢复按该投影补偿 retention。Snapshot 中设置对象不会
安装成 `.vibetable/settings.json`，避免文件与 coordination DB 双权威。

## 兼容

早期 Snapshot 的无 `formatVersion` JSON 对象被解释为“该格式尚未声明当前外部工作区
设置”。它可继续校验、导入和恢复，但恢复时不重置当前 retention。带
`formatVersion` 的对象严格拒绝未知字段、缺失字段、非法分桶、零值和尾随 JSON。

## 后果

- Snapshot barrier 已持有 workspace mutation gate，读取业务库、文件 topology、审计锚点
  和 retention 投影属于同一冻结窗口。
- replica one-shot 与冲突裁决复用同一解码和 authority 更新路径。
- 新增任何独立于业务库的工作区设置时，必须升级此对象格式、列入本 ADR 的后继决策，
  并补充预览、恢复、回滚和旧 Snapshot 兼容测试。
