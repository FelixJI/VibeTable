# Preset 持久视图：原 Python 公开语义冻结

当前候选已将 `preset.list/save/delete` 完整迁移到 Go/PocketBase 权威路由，关闭旧
Python 写入口，并修复真实 workspace gate 的重放及 Host 冲突投影。后文分阶段保留冻结、
实施、失败与纠正证据；历史冻结结果不替代当前新包或远端 CI 资格。

冻结生产者为 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`。初始冻结提交
`18bf403b272d28e6c9489aab2212f79b4f2cec95` 捕获 53 个原 Python 样本，当时未改 owner。
当前校验器隔离提取可从 main 到达的 producer backend，重放原捕获并精确比较完整 JSON；
不依赖初始冻结提交对象，不调用 Go 生成期望。

## 独立聚合与 module 设计

Preset 是单个持久视图 aggregate：业务身份、collection、name、view、revision。
稳定 interface 仍为 List / Save / Delete。五种展示（table/calendar/timeline/kanban/gallery）
是同一个 view DTO 的 kind，不是五个存储 owner。filter/sort/group/summary、共享可见字段、
布局配置均属于 view；设备窗口/滚动/像素宽度属于 GridState，不纳入本意图。

本批把 Preset 的验证、身份派生、单聚合保存删除及公开投影集中进独立 module；
InsightsService 保留 Dashboard 与 Content Version，不迁移其他能力，不另建通用
framework。底层复用 PocketBase metadata 的事务/CAS/receipt/audit/outbox；真正 seam 是
三方法的公开请求，而非让调用方掌握物理 collection。Python 的 metadata adapter 是冻结捕获时
真实执行的 adapter，脚本只替换最底层 client 回复，保留 current 读取与写参数编排。

冻结时的真实调用链：`backend/__main__.py` 注册三方法 → `RpcDispatcher` 验证 Params 并解包 →
`InsightsService` → `PocketBaseInternalMetadataPort` → 当前 sidecar metadata client。
冻结时使用完全相同的三个 handler 与 Params 注册，不启动整个 BFF/sidecar。

## 冻结内容和证据边界

`preset-python-oracle.json` 每例包含公开 method/params/repeats、显式 transportReplies、
真实 Python 产生的 transportCalls 和 responses。响应的 `id` 固定以便复验。
下游 reply 是测试输入，不是从 Go 生成的期望。脚本顺序不匹配或有未消费 reply 会直接失败，
不能被 dispatcher 的 Internal error 掩盖。

| 契约 | 捕获事实 |
|---|---|
| 五视图与默认值 | 五种 kind 的保存；空 view 默认 table；完整 filters/sorts/groups/summaries、collapsedGroupKeys、columns、isDefault；snake aliases 与 bool coercion |
| DTO 边界 | required nullable target/revision 必须成对、空/缺失/多余字段、未知 kind/density、50 filter conditions、16 sorts、2 groups、3 summaries、256 search、128 visibleFields/columns、512 collapsedGroupKeys、3/4 层 filter tree |
| 列表 | adapter 精确按 scope 过滤，按 payload.key 或 logicalId 排序；稳定同键顺序沿用底层返回顺序，不按 name 或 isDefault 排序。未知 presetScope 回退 personal，缺省 view 补默认 |
| 身份 | 新建使用原 service 的 UUIDv5(NAMESPACE_URL, `vibetable:preset:{operationId}`)；更新保留 presetId；operationId 是公开 params 的字符串，不擅自改为 workspace wire UUID |
| 保存 | 每次先 list current，保留已有额外 payload 字段，再覆盖 scope/name/presetScope/view；presetScope 强制 system。expectedRevision 为 null 时采用 current revision 或空串；显式 revision 原样下传 |
| 删除 | 公开 DTO 要求非空 expectedRevision，真实 adapter 直接 delete，不预先 list；key 为 `preset:delete:{operationId}` |
| 错误 | 保存 metadata.revision_conflict 转为 -32080 / insights_error / preset_edit_conflict / field=expectedRevision；这里是 field，不是 path。delete 同类冲突以及未映射错误为 -32603，无错误 data；DTO 为 -32602 |
| current 前置失败 | list 中任一 malformed item 或读取失败阻止后续 upsert，即使已有成功 receipt 也先经过 current 读取；旧实现不先查 durable receipt |
| 重复公开请求 | 同一 request 重复经过 dispatcher/service/adapter，捕获两次完整调用。新建同 operation 的第二次请求因 current 而改变 expectedRevision；脚本分别给出成功和 idempotency conflict，不把任何一种断言为真实 PB 重放结论 |

revision-scripted-before/after、change-scripted、event-scripted 是明确的下游输入占位值，
不是生产 revision/changeSet/event ID 的算法结果。receipt-item-scripted 故意与请求 ID 不同，
记录原 service 用自身派生 ID 返回、只从 receipt 取 revision/trace 的行为。
输入 item 只使用 metadata transport 的 logicalId/revision/payload 结构，不伪造 PB 内部字段。

本 corpus 证明 Python DTO、编排、投影与下游请求形状，不证明真实 PB CAS、事务回滚、
审计/事件去重或重启持久化。metadata 上的实际 expectedRevision/idempotency 契约必须在
后续 Go 公开 HTTP + 真实 PocketBase 层验证；不能把脚本 applied 当成数据库已执行。
尤其不能因为想得到更好的重放行为，直接改写这里保存的旧错误或 current 前置顺序。

## 验证与移交

本轮命令复用既有 uv 管理环境；命令级设置 UV_PROJECT_ENVIRONMENT 指向该环境、
PYTHONPATH 指向当前工作树，不改配置/lock、不安装新依赖。

```text
uv run --frozen --no-sync python contracts/v2/generate_preset_python_oracle.py
uv run --frozen --no-sync python contracts/v2/generate_preset_python_oracle.py --check
uv run --frozen --no-sync python -m ruff format --check contracts/v2/generate_preset_python_oracle.py
uv run --frozen --no-sync python -m ruff check contracts/v2/generate_preset_python_oracle.py
uv run --frozen --no-sync python -m pyright --pythonpath <既有环境的python> contracts/v2/generate_preset_python_oracle.py
uv run --frozen --no-sync python -m pytest tests/backend/application/test_insights_port_service.py tests/backend/adapters/test_pocketbase_internal_metadata.py --no-cov -q
```

53 例生成/复验一致；Ruff 与单文件 Pyright 通过；原 service/adapter 测试 63 passed（0.39s）。
Pyright 提示本工作树无 .venv，使用显式既有解释器完成解析，没有为此升级工具。
提交时正常执行已安装 hook，不绕过。未执行完整质量、构建或真实包 S19–S22。

下一步将此 corpus 作为外部期望消费，设计三方法一起迁移。实现前明确旧行为兼容或有意
差异，尤其 replay/current 顺序与 delete 错误投影；随后补真实 HTTP 的 CAS/失败回滚/
幂等重放和重启持久化，再关闭旧 Python 三 handler 与通用绕过写入口。S19–S22 留作
后续一次最终真实包资格，冻结样本与当前局部测试不能替代该资格。


## Preset 三方法生产迁移（本地纵切）

Go 的 `metadata.PresetService` 提供 list/save/delete 小接口。save/delete 在既有
metadata 同一事务内完成 current CAS、payload 写入、audit、outbox 与 durable receipt；
公开请求规范化后形成既有幂等摘要，在读取 current 前检查 receipt。生产注册使用已有
普通 business coordinator，保留准入、epoch 校验与取消上下文。不引入另一数据权威或新 schema 服务。

五种 view 的公开字段、别名、默认值、filter 三层/五十条件限制、UUIDv5 URL namespace
及 `vibetable:preset:{operationId}` 名称保留。Preset 是保存的展示配置；保存时不查询
实时 schema，也不验证 field 是否存在。已存在 payload 的扩展字段保留，已知 scope/name/
presetScope/view 被本次值覆盖；原 adapter 排除的 id/revision 同样排除。保存结果 userId
仍为 null，后续 list 保留存储中的 userId。list 保留 scope 过滤与默认 DTO；仅非空字符串 key 参与排序，否则使用 logicalId。

原 Python 三 handler 和 InsightsService 对应方法已删除；Dashboard、Version、其它
namespace 与 snapshot 读取保留。generic presets upsert/delete 和 workspace 写路径已拒绝，
Host 继续使用既有 JsonRpcProductDataGateway → HostProductRpcInvoker → Product HTTP，
生成 owner 清单为 Go 25 / Python 77 / Host 2。14 个已有 HTTP fixture 各显式添加三个
Preset registration，不从生成清单构造独立预期。Host manifest、owner 与 Whitelist 的
合法 workspace scope 输入同样独立更新，原无 scope 拒绝契约保留。

### 有意差异及冻结来源

- create/save/delete 的同一公开请求重放优先于 current 状态检查，即使对象更新、删除，
  仍返回首次 durable receipt；不会额外写 audit/outbox，也不会恢复已删除对象。
- save/delete CAS 冲突统一 `-32080 / insights_error / preset_edit_conflict /
  expectedRevision`；重复 operationId 但请求不同返回 `preset_idempotency_conflict /
  operationId`。冻结旧 delete/idempotency `-32603` 不改写，不复制偶然内部错误。
- 原 adapter 会先扫描整个 namespace；新写路径只在事务中读取目标对象，避免不相关
  current 或其旧 DTO 错误阻挡已有 receipt。存储/事务失败仍失败，不吞异常。

`preset-python-oracle.json` 的 53 例及原捕获 inputs/cases 不变。原捕获程序移入
`preset_python_capture.py`，仅在隔离的原 producer backend 下重放。`--check` 从
main 可达的 `146a9c2cac5998ee013daebc78eedff0bd4a7ca5` 使用 git archive 读取 backend，
写入 build/preset-python-producer 下唯一新目录；拒绝路径逃逸/链接，使用当前 uv 解释器
的隔离子进程，核对 backend 导入来源，再完整精确比较所有 JSON 输入、调用和输出。
不依赖 squash 前的 freeze commit 可达性，不由 Go 生成预期，不覆盖既有取证目录。

### 本地验证记录与限制

- 原 producer 隔离重放：53 例完整 JSON 精确一致；Go `TestPresetFrozenPythonPublicDTO`
  消费全部 53 例，检查拒绝边界和规范化 view。
- 真实 PB HTTP 创建/更新/删除、重放、重启持久化、CAS、审计失败回滚、扩展 payload
  与排序：前三个生命周期测试通过。新增并发 CAS 与 idempotent gate 取消断言执行无
  业务失败，但最后整组 `go test ./internal/app -run '^TestPreset' -count=1` EXIT 1，
  原 schemaProductStore fixture 在并发测试 TempDir RemoveAll 清理时目录非空；不称整组通过。
- `go test ./internal/metadata ./internal/productrpc ./internal/contracts/productcapabilities -count=1`
  通过；受影响五包 `go vet` 通过。
- `go test ./cmd/vibetable-pb -run '^TestSidecarProcessReadyHealthAuthAndGracefulShutdown$' -count=1`
  真实进程 25 方法注册通过（1.073s）。
- 全部 14 个受影响已有 HTTP fixture 的 47 测试整体 EXIT 1（9.270s）：唯一报告为
  TestSchemaProductStoreCleanupTerminatesBeforeReset 的 TempDir 非空；无重复注册或业务断言失败。
  完整日志保留在 build/preset-existing-fixtures.log，不把该组当作通过。
- Host invoker/HTTP 49 测试通过；manifest/route selector/WebMessageRouter/Host composition/
  WorkspaceRequestDispatcherQuery 138 测试通过，涵盖三方法合法 scope 和错误投影。
- Python service/adapter/capability/inventory 初次 77 项为 76 PASS/1 FAIL，失败是独立 inventory
  Go owner 预期漏三方法；精确补齐后 inventory 10 项通过（1.50s）。
- 完整 backend Pyright 0 errors、mypy 80 source files 通过。扩查原 tests 文件时 Pyright
  有 3 个 Dashboard 类型错误；使用 producer146a 原 backend、原测试、原 pyproject 同配置
  复现相同 3 错（原827/1024/1029行），不混入本次修改，也不忽略规则。

### S19–S22 后续真实包资格设计

复用既有编号，不增加小场景：S19 gallery 保存 cover/columns 后关闭并重开同一 workspace，
核对同一 presetId/revision、展示字段和删除后的列表；S20 kanban 保留 lane drag 的真实记录
写入断言，再保存/重启核对 groupField 与列配置；S21 calendar 保留日期移动断言，核对保存
的 dateField/titleField；S22 timeline 保留区间移动断言，核对 dateField/endDateField。
各场景的业务成功门禁不变，重放/CAS/rollback 由本地公开 HTTP 回归补证。
本轮不执行完整构建、S19–S22 包资格、push 或 PR；局部结果不替代完整 CI/包验收。

补充：严格 producer checker 的负向契约已通过（1 passed，0.83s）：只修改临时副本
的首个响应输出即可触发精确比较失败；原 corpus 不变。Ruff/生成一致性由提交前入口检查。


## e7dc 后独立复审修复

数字保真回归先在 e7dc 上 RED：首次保存准确，但列表、保留的 payload.extension、
receipt 重放及重启重放把合法整数 9007199254740993 舍入成 9007199254740992。
旧 Python adapter 的 _freeze 保留 int；本次复用 Preset UseNumber + EOF 解码处理存储
payload，并让私有 presetReceipt 实现数字保真的 JSON 解码，未改变其它 metadata DTO。
回归以原始 JSON 输入和 json.Number 断言，避免测试本身先舍入。

真实 workspaceRuntime gate 回归也先 RED：原 background idempotent gate 在发现 workspace
receipt 后跳过业务回调，create/save/delete 重放输出 null，不同参数同 operationId
也返回 null。原无 gate HTTP fixture 不能证明此生产边界。修复改用普通 CoordinateBusinessWrite；
只在 metadata 已核对 request digest 并解码原 receipt 后发出已有 ReplayedBusinessWrite
exact signal。executeIdempotent 仅新增可选 replay 回调，默认调用行为不变；沿已有 Mutation
模式跳过新 workspace receipt，完成只读事务后传出 signal，runtime 安全 abort prepared
intent 后返回原业务结果。未修改公共 workspace 协调器，也未绕过 writer/epoch 准入。
真实 gate 回归检查三种重放、changed request 冲突、旧 create 在删除后重放、stale epoch
拒绝，以及 workspace revision/proof 与 metadata/audit/outbox/receipt 均不重复增长。

排序作明确开发期收窄：只用非空字符串 key，否则 logicalId。原公开 Preset DTO 无 key
字段且拒绝额外参数，原 save 从不产生顶层 key；非字符串只可能来自历史 generic/imported
payload。无需兼容旧 adapter 对任意 JSON 的 Python str 排序。直接 PB 契约保留数值 key 2/1、
空字符串及正常字符串样本，验证新规则；不称其与旧任意 JSON 排序相等。冻结 53 例保持原样。

原 e7dc 完整 Python 质量记录（root）：EXIT 1，1856 PASS / 1 SKIP / 1 FAIL，覆盖率
91.41%，Ruff/Pyright/mypy 通过。唯一 catalog 测试只聚合 Python 注册与 Host owner，漏了
已经迁移的 Go owner；官方 generator --check 原本就通过，静态 catalog 未漏 Preset。
测试现补跨 owner 聚合，独立 25 方法 manifest/Host/cmd 固定预期保留，不手改生成物。
完整 product_contracts 12 项复验通过（1.02s）。该局部复验不替代新的完整 Python 入口。

数字与真实 gate RED 日志分别保留 build/preset-numbers-red.log、
build/preset-runtime-replay-red.log；三项修复聚焦 GREEN 为 1.645s，日志
build/preset-number-runtime-green.log。此后补充数字 key 收窄及 trace 不增断言，并运行相关
回归。S19–S22 的产品旅程由 root 单独提交，本地无真实包资格声明。


修复提交前最终记录：metadata/productrpc/capabilities 三包测试通过；原 metadata/Dashboard
六个真实 PB 事务集成测试通过（1.502s），证明无 replay 回调的旧调用保持默认行为。
受影响 metadata/productrpc/app 的 go vet 通过；真实 cmd 25 方法通过（1.081s）。
最终全部 `TestPreset` 整组 EXIT 1（2.013s），唯一失败是
TestPresetProductHTTPLifecycleReplayCASAndRestart 的 TempDir 清理目录非空；数字、
真实 gate、排序及业务断言无失败。保留此最终失败，不把先前聚焦 GREEN 写成整组通过。

## 同包 Gallery 冲突暴露的 Host 二次投影修复

主任务在 source `8f50f64e2f7391c111c77d791efd599f3c6b9474` 首次完整构建 EXIT 0，
同包 S19–S22 为3 PASS、1 FAIL，报告
`build/qa/preset-metadata/product-e2e/20260910T031255Z/product-e2e-report.json`，
原日志 `build/qa/preset-metadata/product-e2e.log`。S20/S21/S22 含新重启旅程通过；
S19 的 Gallery stale rename 返回 operation.failed / PRODUCT_DATA_FAILED，未到达原
preset_edit_conflict typed terminal。该原失败保持不变，不延长等待或修改场景断言。

根因在 ProductDataRequestController.PostSidecarFailure：HTTP parser 已按固定方法、
-32080、Insights error、精确data字段和两组code/field/message验证Preset冲突，但controller
只投影-32150，丢弃了已验证的Preset错误。ProductRpcErrorMapper本身已有field→path能力。
修复将原HTTP封闭验证提为内部predicate，parser继续拒绝不匹配输入，controller复用同一
predicate才投影-32080；不开放其他Insights方法、未知码或额外字段，不复制Content分支。

新增真实 ProductDataRequestController→ProductSidecarHttpGateway HTTP parser 回归，
覆盖save/delete的revision/idempotency两种冲突，核对typed终态、requestId、path/message、
单次HTTP请求和Python零调用；未知码、错误field、额外private字段继续拒绝。
首次7项为4 FAIL / 3 PASS（合法冲突全部得到operation.failed），日志
`build/preset-controller-red.log`、TRX `build/qa/preset-controller/preset-controller-red.trx`。

修复后执行：

```text
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --configuration Release -p:RestoreLockedMode=true --filter 'FullyQualifiedName~ProductSidecarHttpGatewayTests|FullyQualifiedName~ProductDataSidecarRoutingTests|FullyQualifiedName~ProductRpcErrorMapperTests|FullyQualifiedName~HostProductRpcInvokerTests' --logger 'trx;LogFileName=preset-controller-green.trx' --results-directory build/qa/preset-controller --verbosity quiet
```

101 PASS / 0 FAIL / 0 skip，197ms；包括同7项回归。日志 `build/preset-controller-green.log`，
GREEN TRX在同results目录。未改冻结53例oracle或Go authority，未构建/重跑产品场景。
Host runtime已变，旧包S20–S22通过也不归为当前源码资格；主任务需要安排新包与必要产品
验证。此次源码测试通过不证明S19已经在真实包恢复，也不替代独立审查或远端required。
## Host 冲突投影修复后的完整包资格

生产 source `c673a2a1b103e6104bd352ff8a63b51b47e79b36` 的独立 Standards/Spec 尾审均无确定问题。
`uv run --frozen --no-sync python scripts/build_next.py --release` 完整入口退出 0，含 self-update smoke 和原子发布目录；日志 `build/qa/preset-metadata/build-release-host-projection.log`。

同一包运行：

```text
uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root dist/VibeTable.Next --evidence-root build/qa/preset-metadata/product-e2e-host-projection --scenario 19-gallery-lifecycle --scenario 20-kanban-lane-drag --scenario 21-calendar-date-move --scenario 22-timeline-date-move
```

4 passed、0 failed、0 skipped，合计51项断言；S19/S20/S21/S22分别11.507s、14.778s、14.581s、14.431s。
报告 `build/qa/preset-metadata/product-e2e-host-projection/20260910T032738Z/product-e2e-report.json` 的包审计及 freshness通过，四场景未预期bridge failure/pending均0、正常退出码均0、清理均通过。Gallery已证明typed冲突恢复、完整配置跨sidecar重启及公开删除；其他三场景保留原真实记录移动与新重启配置检查。

该结果覆盖最新Host修复，原S19失败和升级复制/TempDir失败记录保留，不将其它整组EXIT1改写为成功。远端fresh CI、合并及合并后CI/CD仍是后续门禁。
