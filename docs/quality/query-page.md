# query.page Go Product 迁移

本片只切换 `query.page`。生成 policy 将网页 Product 请求经 WPF gateway 发给 Go，直接使用既有 Query Port；删除对应 Python handler 和注册，没有生产 fallback。其余 Query 方法不随本片切换。同步退役仅为旧 Python query.page 设置的 BFF 恢复重试；Go 转发不可用时直接返回稳定错误，旧 epoch 的迟到响应不得重放到新 owner。其他控制器的恢复策略不在本片修改范围。

Product 参数层继续检查闭合字段、Unicode scalar、凭据键、深度和紧凑 UTF-8 1 MiB 预算；handler 加入旧 REST operation 后复用原解码器和 operation 校验，保留第二层预算、TableQuery 默认值及非法字段/limit/null 的拒绝。结果保持 Product 的六字段投影：rows、offset、limit、filteredRows、totalRows、snapshot，不能直接输出 Go Page 的 querySnapshot 字段名。

实际产品路径为既有网页 `query.page` 命令，已在 ProductDataRpcRegistry 注册。packaged S04 的 JSON 编辑、粘贴、原生导入后通过该命令核对权威值；本片不新增网页入口或改写历史场景声明。宿主组合测试验证默认 policy 通过真实 Product HTTP gateway 并保留 false 与 snapshot。

PR275 的原 27 案例保持不变，其中 page 输入的 limit=0 和部分 snapshot 形状只证明 Python DTO/下游 transport 边界，不能被当作可达 Go Port 成功结果。Go 测试完整消费三个可表达的原参数拒绝，其他七个 page 样本明确标记不适用；Python replay 仅保留两个仍属 Python 的 cursor 方法。

另新增 `contracts/v2/query-page-typed-python-oracle.json` 三个补充样本，从生产代码仍等于 c97c83336e4aa1bdf993fc46a7de57040219fb03 的 Python producer 捕获，未覆盖旧原件。输入为合法 offset/limit，返回具有完整 typed QuerySnapshot 形状，覆盖 Unicode/falsy、空页及公开错误；Go 对比完整结果、错误和 typed 调用参数。样本中的 snapshotId/digest 是明确的脚本化不透明值，不代表有效领域签名。

真实 PocketBase HTTP 测试独立创建表和字段、保存记录，验证分页/空页、过滤计数、Unicode/falsy，并用真实 Query Port ValidateSnapshot 验证返回的签名快照；还覆盖非法查询和取消。脚本化投影与真实领域证据不能相互代替。

本片尚未完成最终基线同步、双轴审查、packaged fresh CI 和合并后验收，不能记为交付完成。前置 PR273/PR275 正式合入后，应剥离前置语料差异，并与其他独立 Query 迁移按最新 main 串行重生成能力清单。
