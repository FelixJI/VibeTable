# Preset 持久视图：原 Python 公开语义冻结

生产者固定为 main `146a9c2cac5998ee013daebc78eedff0bd4a7ca5`。本提交仅冻结
`preset.list/save/delete` 的 53 个样本，不迁移 owner、不改变生产代码、catalog 或门禁。
生成器在运行前用 Git diff 确认 backend、pyproject.toml、uv.lock 与生产者一致，并确认
实际导入的是本工作树 backend；生成期望不调用任何拟迁移的 Go 实现。

## 独立聚合与 module 设计

Preset 是单个持久视图 aggregate：业务身份、collection、name、view、revision。
稳定 interface 仍为 List / Save / Delete。五种展示（table/calendar/timeline/kanban/gallery）
是同一个 view DTO 的 kind，不是五个存储 owner。filter/sort/group/summary、共享可见字段、
布局配置均属于 view；设备窗口/滚动/像素宽度属于 GridState，不纳入本意图。

现在 InsightsService 同时承载 Dashboard 与 Content Version；后续仅把 Preset 的验证、
身份派生、单聚合保存删除及公开投影集中进独立 module，不迁移其他能力，不另建通用
framework。底层复用 PocketBase metadata 的事务/CAS/receipt/audit/outbox；真正 seam 是
三方法的公开请求，而非让调用方掌握物理 collection。Python 的 metadata adapter 是本轮
真实执行的 adapter，脚本只替换最底层 client 回复，保留 current 读取与写参数编排。

真实调用链：`backend/__main__.py` 注册三方法 → `RpcDispatcher` 验证 Params 并解包 →
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
idempotent business coordinator，保留取消上下文。不引入另一数据权威或新 schema 服务。

五种 view 的公开字段、别名、默认值、filter 三层/五十条件限制、UUIDv5 URL namespace
及 `vibetable:preset:{operationId}` 名称保留。Preset 是保存的展示配置；保存时不查询
实时 schema，也不验证 field 是否存在。已存在 payload 的扩展字段保留，已知 scope/name/
presetScope/view 被本次值覆盖；原 adapter 排除的 id/revision 同样排除。保存结果 userId
仍为 null，后续 list 保留存储中的 userId。list 保留 scope 过滤、key/id 稳定排序与默认 DTO。

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
