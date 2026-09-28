# 条件 Lookup

在字段设置中选择“查找引用”，进入编辑器后选择“条件筛选”，再选择来源表、返回字段和匹配条件。不需要预先创建 Relation。条件支持当前行字段或类型化常量，组合方式为 ALL / ANY；预览读取当前表按记录 ID 排序的第一条记录，空表显示无样例。取消编辑保留原定义。

## 语义

- `path + targetFieldId` 继续表示原有路径模式；条件模式使用空 `path` 和 `condition`，两种模式互斥。条件必须有 1–50 条完整规则，不完整草稿不能保存或变成全表匹配。
- 基础比较支持 text、number、bool、date、dateTime、select。等于、不等于、空判断适用全部基础类型；大小比较适用数值和日期；包含适用文本。字段操作数必须同类型；date 与 dateTime 不混用。
- 文本比较区分大小写；空字符串不等于 null，0 和 false 是有效值。空当前字段的普通比较不匹配；ANY 可以由其余规则命中。单选字段跨表按精确的有效选项标签比较，保存的字段引用仍为稳定 ID。
- date 按日比较，dateTime 按表示的时刻比较；时区偏移会归一化。配置或字段重命名不改变 stable table/field ID；被引用字段改类型或删除须先处理依赖。
- 选择“原值”时，条件结果为完整列表：无匹配 `[]`，单匹配也是列表。顺序按来源记录 ID 升序；去重按类型和值保留首次结果。来源计数仍包含重复值对应的所有实际匹配记录；来源详情分页不截断值集合。
- 来源插入、删除、条件字段或返回值变化，以及当前行条件变化，都通过既有依赖、新鲜度及持久重算任务更新结果。条件模式仍是只读计算字段。

## 结果汇总

路径和条件模式都可在“结果汇总”中选择原值、去重、命中记录数、非空值数、去重非空值数、SUM、AVERAGE、MIN、MAX。汇总读取完整命中集合，来源详情页只影响追溯显示。数值汇总仅适用于数字来源；路径模式也支持已有数值公式来源，其他类型明确禁用。

对 `[10,20,0,null,10]`，命中记录数为 5、非空值数为 4、去重非空值数为 3，SUM 为 40、AVERAGE 为 10、MIN 为 0、MAX 为 20。计数中的非空排除 null 和空字符串，0 与 false 仍为有效值；数值汇总忽略 null，拒绝空字符串、布尔、非法文本及非有限数。空集合的计数和 SUM 为 0，AVERAGE/MIN/MAX 为 null。去重按首次出现顺序保留值，包括 null。

计数和数值汇总输出数字，可参与公式、数字筛选排序和现有导出。顶层可选 `aggregation` 保存汇总方式；缺省保持旧行为，旧 `condition.distinct=true` 仍可读取，编辑保存后规范为 `aggregation="distinct"` 与 `condition.distinct=false`。显式汇总不能与旧 distinct=true 混用。

## 执行与兼容边界

Go QueryCompiler 复用类型化过滤和归档策略，将同值操作数组合去重，每批最多 64 组，来源每页 256 条；不按每个当前行逐次扫描全表。完整值、来源记录读取及重复输出沿用 32 MiB 物化预算，超限或取消明确失败。任意条件的反向影响仍使用可取消、可恢复的分批当前表扫描；有实测瓶颈后再考虑条件反向索引。

`lookup.draft.preview` 是 workspace scope 的 Go 只读 RPC，绑定当前表和来源表的 schema revision。Web 防抖并使编辑、取消、切换工作区前的在途结果失效。

迁移 `2026092801_conditional_lookup` 将依赖元数据的 `relation_field_id` 改为可空，不创建隐藏 Relation。回退时若仍有条件依赖则拒绝并保留数据；空依赖时可安全恢复 required。现有路径 Lookup 保持原有单值/多值与按需来源分页语义。

## 验证入口

- `go test ./tests/integration -run TestConditionalLookup -count=1`：真实存储的类型、ALL/ANY、范围、稳定 ID、跨表单选、写入/删除重算、151 个完整值与分页、65 组的两次匹配查询、重复输出预算和取消。
- `go test ./internal/schema/v2 ./internal/app ./migrations -run 'TestLookupConditionWire|TestConditionalLookup' -count=1`：闭合 wire、只读预览与双 revision、迁移回滚。
- Web 字段设置测试覆盖草稿取消、类型化输入、250ms 防抖和迟到结果失效。
- `uv run python qa/product_acceptance.py --package-root dist/VibeTable.Next --evidence-root build/q --scenario 26-lookup-definition-read`：同包 WPF/WebView2 的离线编辑、预览、保存、去重、重算、同 UUID 重开和取消。局部证据由本次 PR 绑定 source/head，不替代完整 CI 门禁。

实现与冻结检查点：[Task #391](https://github.com/FelixJI/VibeTable/issues/391)。
