# Surface / Interface 四方法迁移：固定公开基线与设计

状态：仅冻结与设计，未切换生产 owner、未修改公共 catalog，未运行完整发布构建或 S17。本提交只属于未来完整迁移 PR，不单独发起 oracle PR。

## 固定生产者与证据边界

producer 为 `12556e5db81dd49592d69b5af1780007ccd36c37`，创建独立分支时已实际 fetch 验证。之后主干新合入的 Mutation owner 不改写此基线；进入生产实施前正常同步最新 main。

`generate_surface_python_oracle.py` 调用原 `_register_surface_methods`、RpcDispatcher、SurfaceService 和生成的 Pydantic DTO，捕获 `interface.list/load/commit/delete` 的 78 组、81 次公开请求响应。四方法预期由捕获脚本独立列明，不从将来的 Go 注册或生成 owner 清单反推。脚本核对八个 producer 源文件与固定 Git tree 一致，并核对实际 import 文件来自本工作树。

metadata 依赖是明确的 ScriptedMetadata adapter：返回 `fixture-revision-N`，按 seed 指定行和顺序，模拟写入冲突或存储异常。它不模拟 PB revision 算法，不计算新增 hash，也不证明真实数据库、并发或 durable replay。本 JSON 的 revision 只说明领域结果透传 metadata 返回值；未来真实 Go 测试须先用权威写入建立 fixture，再将固定 revision token 与真实 revision 对应，不能要求 Go 输出 fixture 字符串。

一次捕获后脚本拒绝覆盖 JSON；`--check` 在旧 producer 仍存在时重放并逐值比较。实施删掉 Python handler 后，保留本次冻结提交中的原捕获器，最终 checker 只验证冻结输入/producer，不能用新 Go 输出刷新原件。

已捕获的区分性边界：

- 四方法成功、缺失、create/update/delete CAS、空 revision 不等于 null、权威写入竞争错误及存储失败；结果与错误的 code/message/path 均为原公开输出。
- element 总数 200 接受 / 201 拒绝，树深 8 接受 / 9 拒绝，跨 page element ID 重复、结构/children、binding/action 引用、navigation/form 动作类型。
- binding/field/variable/page/action 重复；literal/selectedRecordField 的 target/source 约束；正常 DAG、自环、多 binding 环和 source 字段缺失。
- 五种动作的成功配置、缺失/多余目标；原 DTO/领域允许的 navigate 空 unrelated binding、plugin 空身份也如实捕获，不擅自当作原拒绝契约。
- Unicode casefold 排序与相同 folded name 下 interface ID 排序：Straße/STRASSE/strasse、ﬀ/FF、K/k、ς/Σ。不能以 strings.ToLower 代替 full casefold。
- 11 次 invalid DTO，包括缺 required-nullable、未知属性、空 ID/key/revision、空 pages/fields、未知 element kind、33 variables；同时捕获 snake_case alias 与整数字符串 pageSize 的原接受行为。
- create/update 同请求重放在旧 current 检查被 `surface.edit_conflict` 拒绝；delete 重放被 `surface.not_found` 拒绝。每组首次写入已成功且 adapter 只执行一次写入/删除。后续 Go 应修复 replay，不能把旧失败当作必须维持的成功行为。

## 真实调用链与 namespace 收口

固定 producer 的调用链：

1. `desktop/src/VibeTable.Desktop/MainWindow.Product.cs:620` 将 `new JsonRpcSurfaceGateway(client)` 直接绑定给 Surface controller；`Services/JsonRpcSurfaceGateway.cs:17` 起四个 typed 方法直连 Python JSON-RPC。
2. `Services/SurfaceRequestController.cs:74` 起处理 interface.listRequested/loadRequested/commitRequested/deleteRequested，返回 interface.listLoaded/loaded/committed/deleted；CorrelatedRequestRunner 持有现有取消、超时、并发及迟到回复规则。`SurfaceErrorMapper.cs:15` 保留已有 renderer-safe 提示，不能机械套用 Content 的普通 ProductData 响应。
3. `backend/__main__.py:202` 注册四个真实 handler，474 行注入 SurfaceService。`surface_service.py:62/107/149/160` 通过 RevisionedMetadataPort 对 interfaces list/read/write/delete；`revisioned_metadata_port.py:106` 起 adapter 转为 PocketBase transport。
4. `backend/adapters/pocketbase/internal_metadata.py:56`、`client.py:34` 为 namespace allowlist。生产源码检索中，interfaces 的业务写调用者仅 SurfaceService；其余出现处是 adapter、Go metadata 注册和测试 fixture。未来只收口 interfaces generic upsert/delete，不改变其他 namespace。
5. `sidecar/internal/metadata/service.go:38` 将 interfaces 指到 vibetable_interfaces，读取业务 logical_id；不能以 PB record.id 代替 interfaceId。`sidecar/internal/conflict/sqlite_candidate.go:311` 将该内部集合纳入 snapshot dependency 投影，必须保留；集合与 snapshot/export 读取不得随 generic 写入口删除。

SurfaceDefinition 的 tableId/fields/plugin identity 只是定义中的引用，旧 SurfaceService 不查询真实 SchemaCore/table/plugin 是否存在。迁移不能顺带增加跨领域存在性校验，也不迁移 runtime 动作执行（query/mutation/plugin broker）。

## 推荐四方法 module 与事务 seam

Go 在现有 `internal/metadata` 内提供一个 SurfaceService，外部只暴露 List、Load、Commit、Delete，使用 generated workbench Request/Result。构造依赖为现有 PocketBase app；不为纯聚合验证增加 SchemaCore/query/plugin port。树、DAG、动作、排序与存储投影均藏在该 module 内部。

Commit/Delete 使用 `service.go:534` 的 executeIdempotent：先查现有 durable receipt，再在同一 tx app 内做当前版本检查及 upsert/delete，通过原 saveTrace 同事务写 audit/outbox。保留原 request.idempotencyKey 与 metadata.interfaces.upsert/delete coordinator 语义，wire 操作身份按届时主干已有机制接线，不复制尚未合并的 seam。复用现有 request-hash/receipt 契约，不新增业务 hash 层。相同 key/相同请求在 revision 已改变或目标已删后仍重放原结果；相同 key/不同请求应以明确的 surface 幂等冲突码失败，不重做变更。

所有领域校验仍需对 stored definition 的 load/list 执行；损坏 DTO、领域非法、identity 不一致统一保留 surface.storage_invalid。提交校验顺序保留原先可以观察到的第一个错误和 path。expectedRevision null/空串存在性、required nullable、整数与 bool coercion、full casefold 是独立 decoder/投影测试重点。full casefold 可评估现有锁定依赖 golang.org/x/text/cases.Fold（无需新增依赖），必须与固定 Unicode 样本对照，未以本次设计证明所有 Unicode 版本等价。

Host 必须同时迁移专用 UI 路径：优先让现有、已由 MainWindow 持有与释放的 JsonRpcProductDataGateway 实现 ISurfaceRpcGateway，四 typed 方法走其拥有的 HostProductRpcInvoker；绑定时复用 `_productGateway`，移除旧 JsonRpcSurfaceGateway 生产实例/类，不另起一个无人释放的 invoker。Surface 的 *Requested/*committed 等 renderer 协议与 CorrelatedRequestRunner 保留。需要直接测试 Surface controller→typed gateway→真实 HTTP parser→SurfaceErrorMapper 的成功、not_found/edit_conflict、未知错误拒绝、取消及 retired generation，不能只测 HttpGateway 接受错误码。

Go dispatcher 与 Host HTTP parser 仅允许四方法实际定义的 surface 错误码和 -32170 / surface_error data 形状；不能放开任意 domain/prefix。SurfaceErrorMapper 的现有友好提示保持，新增幂等冲突语义需明确而非落成通用内部错误。若届时 Content mapper 已合并，可按其共享入口模式整合，不能把当前未合并 Content 候选当作隐含依赖。

## 完整迁移的删除与验证范围

同一完整 PR 才切换四方法 inventory/生成 capability/Host 绑定，并删除 Python SurfaceService、四 handler、错误注册和 interfaces 专属通用写入口；原应用单测由四方法真实 Go/PB 测试替代，保留公共 DTO/oracle。catalog 生成器改用生成 workbench Request/Result 作为参数/结果来源，公共四方法不删除。所有精确 process/registration 预期独立更新，不能由生产清单自证。

源码资格：冻结 corpus 经四方法 seam 对照；真实 PB 的业务 ID、list casefold、CAS 竞争和事务 trace rollback；重启后 receipt replay 与 key 冲突；generic interfaces 写拒绝而 snapshot/internal read 保持；Host 全调用链与 generation/cancel seam。旧 replay 前置检查会失败的新回归必须先 RED。无需要的任意 generic namespace 组合不扩大。

现有 S17 位于 `tests/e2e/webview_product_scenarios.mjs:5961`：真实安装插件、创建 Interface、编辑并保存、在其他导航后重载、执行 record create/binding refresh/page navigate，以及 plugin 拒绝/取消/最终审批成功。它目前没有 Interface 删除或 sidecar restart 的场景断言，不能宣称已有 S17 证明二者。完整迁移稳定后在同一个 S17 扩展删除与 fresh reopen/restart（包括持久 definition 状态），再经授权用一次最终真实包核验；不额外拆小 oracle PR、不用源代码单测替代实际产品结果。

本轮实际验证：`uv run --frozen --no-sync python contracts/v2/generate_surface_python_oracle.py --check` 78 例一致；`uv run --frozen --no-sync pytest tests/backend/application/test_surface_service.py tests/backend/test_main_surfaces.py --no-cov -q` 19 PASS/0.47s；生成脚本 Ruff format/check 通过。未运行完整质量、Go 构建、产品包或 S17。
