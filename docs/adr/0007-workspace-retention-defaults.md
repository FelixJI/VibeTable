# ADR 0007：工作区版本保留默认值

- 状态：已接受
- 日期：2026-07-28
- 适用格式：workspace format 1 / protocol 2.0

## 决策

所有新工作区只使用 `contracts/v2/fixtures/retention-policy.json` 作为产品默认值来源：

- Snapshot 最长保留 30 天，至少保留最近 50 个；
- Snapshot 启用 hourly、daily、weekly、monthly 桶；
- 自动文件修订最长保留 30 天，至少保留最近 100 个；
- 自动文件修订启用 daily、weekly、monthly 桶；
- 工作区回收站固定保留 3 个月；
- repository 磁盘限额默认为未设置，不能因估算值自动删除正式版本；
- 固定 Snapshot、正式文件版本、有效叶、活动 root、持久 pin 和未完成 publication
  始终优先于数量、时间和空间限制。

Go 清理引擎的 `MinimumRecent` 对 Snapshot 映射为 50；桶只在 30 天窗口内选择。
文件修订由 file-history retention adapter 独立使用 100/30 天规则，不把 Snapshot 的
`MinimumRecent` 误用于文件修订。回收站 grace 始终为 90 天。

## 依据

当前产品仍处于首次发布前，没有可声明为真实用户历史的正式版本数据。WP1 的固定读视图、
对象去重、root pin、GC 和包资源限制已由自动化负载验证；SMB、Cloud Files、可移动盘和
双设备环境尚无硬件证据，因此这些 provider 保持 fail-closed，不能用模拟结果宣称
default-on。第一版采用保守、可解释且已在四端契约中冻结的内部默认值；首次正式版后的
真实遥测只允许通过新的 ADR 和显式 format/policy revision 调整，不能静默改写已有工作区。

## 后果

- Web 不持有独立默认策略；未收到权威投影时只显示不可提交的占位状态。
- 更新策略必须携带 `expectedRevision`，清理 Apply 必须重新验证 inventory revision 和 digest。
- 超限时先停普通自动恢复点并告警，不阻止业务保存，也不删除保护对象。
- 后续默认值变化必须新增 ADR、更新契约 fixture，并通过四语言与发布门禁。
