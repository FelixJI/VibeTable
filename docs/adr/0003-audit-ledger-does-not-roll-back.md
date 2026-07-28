# ADR 0003：审计账本不随业务恢复回滚

- 状态：已接受
- 日期：2026-07-28
- 决策者：VibeTable 产品与工程

## 背景

把审计事件保存在被恢复的业务数据库中，会让恢复旧数据同时抹去“谁执行了恢复”和恢复前发生的事件。

## 决策

- 权威审计进入 `.vibetable/audit/ledger.db`，与可替换业务 data 物理分离。
- 每个权威业务事务在同库 `audit_outbox` 中写入规范 envelope；drain 以 `eventId` 幂等追加 hash chain。
- Snapshot、关闭 session 和恢复前必须 drain 到 source high-watermark。
- Snapshot 锚定 `epoch + sequence + chainHash` 并引用不可变审计前缀。
- 恢复旧业务库不替换 ledger，而是开启新 audit epoch 并追加 restore 事件。

## 安全边界

hash chain 能发现已锚定范围内的篡改或缺失；只有远端/导出锚点才能发现本机尾部截断。产品不宣称抵御本机管理员重写全部状态。
