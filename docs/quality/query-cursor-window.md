# query.cursorOpen / query.cursorFetch Go Product 迁移

来源 PR280 将“打开并续读一个游标窗口”作为完整意图，只切换 query.cursorOpen、query.cursorFetch。WPF 读取生成 policy，Go Product adapter 调用既有 Query Port；对应 Python handler/注册同时删除，没有生产 fallback。该来源中 selectionOpen 仍由 Python adapter 处理；当前组合批次另纳入 PR284，将 selectionOpen 一并迁至 Go。游标是既有领域签名及修订绑定令牌，本片不新增内存游标注册表、过期策略或并发状态机。

参数保留 Product 闭合字段、Unicode scalar、凭据键、深度和紧凑UTF-8 1 MiB预算；加入operation后复用既有REST解码、第二层预算和公开错误映射。结果保持六字段 CursorWindow，包括 querySnapshot；rows及行元素非null、hasMore与nextCursor是否为null的一致性继续校验，空字符串nextCursor保留原语义。取消直接传给原Query Port。

旧27个Python案例原件不改。17个cursor案例中6个可在新HTTP边界完整对照，其余11个逐项记录不可达或不可typed表达原因；不会将不适用案例记为通过。新增13个typed补充案例从固定未迁移Python producer捕获，snapshot与cursor中的opaque值不代表有效领域签名。详细适用性及可复现配方见 [补充语料](../../contracts/v2/query-cursor-typed-python-oracle.md)。同步已迁移 query.page 的 main 后，全部 Python replay 退役；只读检查核验固定生产者、27例清单、请求和 authority 输入，所有重新生成入口拒绝。

真实PocketBase HTTP测试验证多个分页大小、终止/空结果、Unicode/falsy、签名snapshot、修订stale、非法输入和取消；另用真实selection authority生成cursor再由Product fetch续读。宿主组合测试使用默认policy及真实invoker/gateway、脚本化HTTP对端，验证Go selectionOpen返回的cursor原样交给Go fetch，成功和公开失败均保持Python调用为零，并核对epoch/租约闭合。独立selection宿主测试继续覆盖关闭取消及公开失败零fallback；这些测试不代替真实Go领域证据。

实际产品入口为InterfaceRuntime→SurfaceCursorController。S17保留原更新、创建、插件操作和导航流程，以pageSize1及明确降序排序创建两条记录后点击下一页，核对第二条记录与终止按钮，再回到首页。组件分页测试及脚本契约只是准备证据；未执行当前打包S17前不能宣称真实产品验收完成。

PR275 冻结语料已由 PR273 覆盖合入。来源 PR280 在 a1bffd8 的完整 CI34158478173 已通过；本次按已授权整合流程，与独立通过 CI 的 PR278、PR284 一起整合，并同步包含 query.page 的 main 8cf989c5。PR 相对 main 仍为 readRows、cursorOpen、cursorFetch、selectionOpen 四个读取方法；保留 main 的 history 生命周期、page 恢复和 S18 增量，生成清单为12 Go、88 Python、2 native。最终端点仍须独立双轴审查、fresh CI、squash及post-main CI/CD；来源绿色不替代这些门禁。
