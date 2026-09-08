# query.cursorOpen / query.cursorFetch Go Product 迁移

来源 PR280 将“打开并续读一个游标窗口”作为完整意图，只切换 query.cursorOpen、query.cursorFetch。WPF 读取生成 policy，Go Product adapter 调用既有 Query Port；对应 Python handler/注册同时删除，没有生产 fallback。该来源中 selectionOpen 仍由 Python adapter 处理；当前组合批次另纳入 PR284，将 selectionOpen 一并迁至 Go。游标是既有领域签名及修订绑定令牌，本片不新增内存游标注册表、过期策略或并发状态机。

参数保留 Product 闭合字段、Unicode scalar、凭据键、深度和紧凑UTF-8 1 MiB预算；加入operation后复用既有REST解码、第二层预算和公开错误映射。结果保持六字段 CursorWindow，包括 querySnapshot；rows及行元素非null、hasMore与nextCursor是否为null的一致性继续校验，空字符串nextCursor保留原语义。取消直接传给原Query Port。

旧27个Python案例原件不改。17个cursor案例中6个可在新HTTP边界完整对照，其余11个逐项记录不可达或不可typed表达原因；不会将不适用案例记为通过。新增13个typed补充案例从固定未迁移Python producer捕获，snapshot与cursor中的opaque值不代表有效领域签名。详细适用性及可复现配方见 [补充语料](../../contracts/v2/query-cursor-typed-python-oracle.md)。同步已迁移 query.page 的 main 后，全部 Python replay 退役；只读检查核验固定生产者、27例清单、请求和 authority 输入，所有重新生成入口拒绝。

真实PocketBase HTTP测试验证多个分页大小、终止/空结果、Unicode/falsy、签名snapshot、修订stale、非法输入和取消；另用真实selection authority生成cursor再由Product fetch续读。宿主组合测试使用默认policy及真实invoker/gateway、脚本化HTTP对端，验证Go selectionOpen返回的cursor原样交给Go fetch，成功和公开失败均保持Python调用为零，并核对epoch/租约闭合。独立selection宿主测试继续覆盖关闭取消及公开失败零fallback；这些测试不代替真实Go领域证据。

实际产品入口为InterfaceRuntime→SurfaceCursorController。S17保留原更新、创建、插件操作和导航流程，以pageSize1及明确降序排序创建两条记录后点击下一页，核对第二条记录与终止按钮，再回到首页。组件分页测试及脚本契约只是准备证据；未执行当前打包S17前不能宣称真实产品验收完成。

PR275 冻结语料已由 PR273 覆盖合入。来源 PR280 在 a1bffd8 的完整 CI34158478173 已通过；本次按已授权整合流程，与独立通过 CI 的 PR278、PR284 一起整合，并同步包含 query.page 和 query.view 的 main 6ed36810。PR 相对 main 仍为 readRows、cursorOpen、cursorFetch、selectionOpen 四个读取方法；保留 main 的 history 生命周期、page 恢复和 S18、view 的 S02 增量，生成清单为13 Go、87 Python、2 native。最终端点仍须独立双轴审查、fresh CI、squash及post-main CI/CD；来源绿色不替代这些门禁。

本次合并来源端点339b686d的完整CI34178397563已成功，main 6ed36810包含独立通过CI的view；以下是合并工作树的本地证据，不声明全local green。Python首次命令误写不存在的cursor oracle测试路径，0项执行；纠正为window oracle并纳入root adapter后105项中102通过、3失败。失败来自两处仍把view当Python的断言，以及唯一剩余validateSnapshot组被清空后先触发形状错误；改为真实owner断言和删除整组的coverage负例后，仅复验两个contract文件17项通过（0.63秒）。

完整.NET Release首次1201通过、1失败、1既有跳过；失败的旧Python迟到响应代表替换成了非网页入口validateSnapshot。改用既有field.settings.describe及合法tableId后，保留workspace switch、迟到响应与stale断言，受影响WorkspaceSessionEnvelopeFilterTests类35项通过（251毫秒），未重复完整矩阵。原完整结果保留，不写成全solution绿。

Go聚焦race的app包104.351秒失败，仅TestQueryCursorProductHTTPRejectsStaleInvalidAndCancelledRequests和TestQueryPageProductHTTPConsumesApplicableFrozenPythonOracle发生TempDir目录非空清理错误；dispatcher与capabilities分别1.692、1.284秒通过。进程TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart在TempDir snapshots清理阶段失败（1.822秒）。这三处失败未解决、未重复取绿，最终fresh CI仍pending。生成一致性、Ruff、Go格式/vet及仓库规定的pyright backend通过；额外对测试文件运行Pyright发现12个来源中既有的返回类型/JSON缩窄错误，不将该扩展检查记为通过，也不混入无关修复。

证据日志为pr285-view-sync-python.log、python-fixed.log、policy-fixed.log、dotnet-full.log、envelope-fixed.log、go-race.log、process.log、vet.log和pyright-backend.log（后八项同样使用pr285-view-sync-前缀）。本轮没有重建或运行产品包，来源S02/S17/S07/S10/S18证据不替代最终端点的完整required、真实产品验收与squash后main CI/CD。
