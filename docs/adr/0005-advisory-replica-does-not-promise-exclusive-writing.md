# ADR 0005：advisory 副本不承诺强一致排他

- 状态：已接受
- 日期：2026-07-28
- 决策者：VibeTable 产品与工程

## 背景

普通同步盘通常没有可证明线性化的 CAS。仅靠可同步的 `lease.json` 无法阻止两个离线设备同时写，也无法安全覆盖共享 head。

## 决策

- 只有通过权威 CAS 测试的 provider 才属于 strong。
- advisory provider 只追加不可变 claim/publication DAG，不维护可覆盖共享 head。
- 离线强制编辑产生 provisional 写入；联网后由确定性 resolver 裁决。
- 败方状态保留为 recovery Snapshot，不丢弃。
- advisory 远端不执行 destructive maintenance、覆盖同步或 `sync --delete`。
- UI 明确说明同步用于携带数据，不支持同时编辑；“强制接管”不描述为即时排他。
