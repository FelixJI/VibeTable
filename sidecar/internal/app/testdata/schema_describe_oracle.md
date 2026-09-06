# schema.describe 旧 wire oracle

生成资产 `schema_describe_oracle.json` 单独计量，保留可读 JSON；不是人工编写的 hash/snapshot。
基线 `e9bea3ac922e5ac5dbab320fbe434887f181e866`（含 temporal Lookup 兼容修复）的旧 Python wire 已被冻结为该只读 JSON corpus；`lookupList` 保存当时独立消费端的 revision 证据。它不是 DTO dump，也不再依赖 Python replay 或捕获入口。

在 `sidecar/` 执行 `go test ./internal/app -run '^TestSchemaDescribeProjection' -count=1 -v` 验证。纯 Go 测试读取冻结 corpus，保留请求内目标加载缓存、目标加载错误与取消传播的契约。

语料覆盖全逻辑类型/self relation、跨表 one/many 两跳和 target lookup、空表、view readonly/primary fallback，以及持久 Range（含 `-0`/`-0.0`）、JSON default、附件策略和 required 元数据；不测试写入能力。它是历史 wire 的固定契约，不提供重新捕获路径，也不增加文件摘要。

四个场景：全逻辑类型/self relation；跨表 one/many 两跳和 target lookup；空表；view readonly/primary fallback。普通 Go 测试只读冻结资产，不依赖 uv；目标加载缓存与错误/取消保持另有纯 Go 契约。

temporal Lookup 的 Go outputStorage `datetime` 已由兼容修复纳入现有 renderer 映射。本 corpus 已经重新捕获，四个场景均要求实际 lookup.list 成功并与 schema.describe 的 lookupRevision 一致；回放遇到消费失败直接报错，不再将空结果作为成功 oracle。保留普通 Go corpus 回归的非空 revision 校验，防止重新引入该跨 owner 阻塞。

`capabilityHash` 和 `lookupRevision` 仅保留既有跨 owner wire token，没有新增摘要层。当前 `schema.describe` 的 Product RPC owner 已切换为 Go，通过真实 PocketBase schema/catalog 读取生成兼容投影；Python 注册和旧投影实现随同删除。`lookupRevision` 的独立生产者是 `product_relation_lookup_file_rpc.py` 中的 `lookup.list`，消费端 lookup query/refresh 会重算并比较 revision，不一致时拒绝为 `Lookup query revisions are stale`（refresh 对应错误）。这防止 schema.describe 换 owner 后，未变化的 lookup 因两种序列化输出不同而被误判过期。`capabilityHash` 保持现有 SchemaDescriptor wire 字段兼容，不把它扩充为本地文件完整性校验。

本片仅完成 L3A 中 `schema.describe` 的 owner 切换；其余 query/schema 方法各按独立意图推进。Product HTTP 回归覆盖闭合参数、错误分层、任意精度整数回显、路径拒绝、缺表以及实际 catalog 查询后取消；纯投影回归覆盖非法 snapshot 与缺 lookup 元数据。宿主 route selector 与握手测试同步验证生成 policy；打包后的产品验收对应 `02-all-field-schema`、`03-schema-errors`、`06-relation-fanout`，执行结果记录在 PR，不能用本 corpus 替代。
