# query.validateSnapshot 原 Python 执行契约

固定 producer：`2f02bfcb8afdda46fa003d6c546d2ff2a8910aae`。本变更仅冻结契约，不切换 owner，不改变生产代码，不替换旧 `query-read-python-oracle.json` 中的历史十例。

初始 29 例及审查后追加 1 例（合计 30 个独立输入）经真实 `RpcDispatcher` → `PRODUCT_RPC_REGISTRY` 的 `QueryValidateSnapshotParams` → `PocketBaseProductRpc` → `ProductQuerySchemaRpc._validate_snapshot` → `PocketBaseProductContext.post` → recording transport 捕获。记录公开 request/response、脚本 authority 输入及每一次实际 authority 请求；expected 来自执行结果，没有从 Go 输出反推。

生产顺序来自 `backend/contracts/product_rpc.py:35–80,274–279` 与 `backend/adapters/pocketbase/product_query_schema_rpc.py:106–111`：先递归 JSON/凭据/深度及紧凑 UTF-8 1 MiB 守卫，再 closed 字段检查（未知→缺失→字段类型）；`snapshot` 必需且为 object，`currentQuery` 可选但显式提供必须为 object。handler 先读取 snapshot，再读取提供的 currentQuery，然后 POST `/api/vibetable/v1/query/validate-snapshot`。空对象可通过 Python；无 currentQuery 时不补该字段。嵌套 snapshot/query 不经过 Python QuerySnapshot 模型，不自行补字段、归一化查询或检查签名。

结果由 `product_rpc_support.py:37–62` 验证 JSON object 并完整转发：不投影，不补 valid/revision，不删除 reason 或扩展字段。null/array 根进入一次 authority 后公开 `-32603 Internal error`。前置错误不会调用 authority，即便脚本预设领域错误。领域/transport 错误经真实注册映射到 `-32150`，保留各自公开错误内容。

## Typed Go 表达边界

`sidecar/internal/query/types.go:264–279` 的 QuerySnapshot 有七字段：snapshotId、digest、databaseId、table、schemaRevision、dataRevision、normalizedQuery。SnapshotValidation 固定输出 valid(bool)、currentDataRevision(int64)、currentSchemaRevision(string)，reason(string) 使用 omitempty。`TableQuery` 的自定义解码见同文件，缺省 limit 为 100，未知字段被拒绝；empty currentQuery 在 Python 的原样 HTTP body 与未来 Go 的解码值不是同一层，不应通过改写原件抹掉该区别。

核心五例保留完整 typed 结果：省略 currentQuery 的 valid=true、带非空 Unicode filters/sorts 的 valid=true，以及 query_changed、schema_changed、application_write 三类 false 结果。它们不属于 typed boundary。所有成功均是脚本响应；snapshotId/digest 是显式测试字符串，没有从真实 authority 签发，不能据此声称 Go 领域或真实产品允许这些快照。empty snapshot/currentQuery 同样只锁定 Python 转发/默认边界，不能当领域成功资格。

逐案 `typedGoBoundary` 仅标记十例：transport-error（取消 HTTP 传输层后的故障来源区别）；empty-response（缺三个必需输出）；explicit-empty-reason（omitempty）；dynamic-response（额外根字段）；wrong-response-types；null-response；array-response；opaque-nested-snapshot（额外 snapshot 字段）；wrong-nested-snapshot-type（dataRevision 字符串）；unknown-current-query-member（closed TableQuery 无此成员）。不将公共领域错误、false、0、Unicode 或所有动态值整体豁免。

当前脚本 `public-domain-error` 输入的 code 是 `query.snapshot_invalid`，用于验证 Python 对 PocketBaseProductError 的公开透传，它可由公开错误对象表达，但不是声称现有 QueryPort 实际发出该拼写。真实 `port.go:1002–1024` 使用 `query.snapshot.invalid`。后续真实领域资格必须独立核对实际错误与签名；冻结原件不为匹配 Go 而静默修改。
审查发现原虚构领域 code 不能单独覆盖真实公开错误，因此经明确授权仅追加 `domain-invalid-snapshot-id`。输入为完整七字段 snapshot（snapshotId="invalid"），脚本 authority 按 `port.go:1003–1005`、`query_routes.go:207–218` 及 `types.go:406–423` 的自定义 MarshalJSON 独立构造 HTTP 422 body：contractVersion="2.0"、code="query.snapshot.invalid"、path="snapshotId"、message="query snapshot id is invalid"、details={}、retryable=false。没有 contract 或 occurredAt；不能仅看 struct tags 而遗漏自定义 MarshalJSON 字段。

新增公开 response 由真实 Python 捕获，类型边界为 null；它完整断言 -32150/Product data error 与 data 中 kind/message/code/path/details/retryable。原件最后一例的 authorityFixture.response 此时表示错误 HTTP body，failure 标识使 recording transport 以 422 抛出真实 PocketBaseProductError，不会把错误 body 当成功 result。

一次性 `build/append_snapshot_domain_case.py` 调用生成器完整 capture，写前确认初始 29 cases 解析值完全相等及原 JSON 文本前缀保留，仅追加第 30 个对象；日志 `build/query-snapshot-domain-append.log` 两项 PASS。生成器 `--write` 的 exclusive-create 拒覆盖规则不变。此追加锁定真实错误形状的 Python 公开映射，仍不声称已执行真实 Go/PocketBase 或产品包。

Go `ValidateSnapshot` 的顺序见 `port.go:657–711`：验证配置/取消、snapshot signature、describe、database mismatch、currentQuery normalize/equality、schema revision、data revision。本冻结没有执行该链。将来 owner 迁移应复用 app 的既有 QueryPort，保持当前快照生产与验证配置一致，不在本变更新建 port 或修领域行为。

## 来源与保存规则

capture_case 每次先检查实际导入的 handler、adapter、client、DTO、dispatcher、消息/错误注册及公共辅助函数来源均属于当前工作树 backend，再以固定 producer 的 `git diff --quiet --no-ext-diff --no-textconv` 检查对应源码内容。Git 不可验证或有漂移则拒绝捕获；没有新增 hash。

RecordingTransport 先记录尝试，再检查唯一 POST、精确路径、body、session header 和 expectedStatus；违规另存记录，并在真实 dispatcher 返回后直接失败，不能冻结成普通内部错误。负向测试包括错误路径和第二次请求、外部导入、源码漂移/Git 失败。

`--write` 仅独占创建，原件存在则失败。默认及 `--check` 都完整重新捕获并比较，只读不写；差异应检查原因，不能覆盖原件取绿。大尺寸/孤立 surrogate/深度拒绝另在聚焦测试直接执行，不向 JSON 填入百万字符。

复用已有 UV 环境，显式设置 `UV_NO_SYNC=1`、`UV_PROJECT_ENVIRONMENT` 和指向本工作树的 `PYTHONPATH`；入口如下：

```text
uv run --frozen --no-sync python -m contracts.v2.generate_query_validate_snapshot_oracle --write
uv run --frozen --no-sync python -m contracts.v2.generate_query_validate_snapshot_oracle
uv run --frozen --no-sync python -m contracts.v2.generate_query_validate_snapshot_oracle --check
uv run --frozen --no-sync pytest tests/contract/test_query_validate_snapshot_python_oracle.py -q --no-cov
uv run --frozen --no-sync ruff check contracts/v2/generate_query_validate_snapshot_oracle.py tests/contract/test_query_validate_snapshot_python_oracle.py
uv run --frozen --no-sync pyright --venvpath <共享环境父目录> contracts/v2/generate_query_validate_snapshot_oracle.py tests/contract/test_query_validate_snapshot_python_oracle.py
```

首轮捕获 EXIT 0；其中预期的 null/array 响应会由真实 dispatcher 记录内部异常日志，公开响应已冻结。首轮 pytest 41 PASS / 9.10s，Ruff PASS；Pyright 首轮 1 个测试 `len(JsonValue)` 类型收窄错误，后补显式 list 断言。该历史失败不属于生产行为失败。默认/check 与最终类型结果在交回证据中记录，不把此脚本资格视为真实 UI、Go owner 或 fresh CI 验收。
追加后的定向命令 `uv run --frozen --no-sync pytest tests/contract/test_query_validate_snapshot_python_oracle.py -k 'domain_invalid or full_capture or domain-invalid' -q --no-cov` 为3 PASS / 1.96s；Ruff format/check、Pyright、默认完整捕获比较和`--check`均通过。没有重新运行全部测试，首次41例通过及类型检查修正历史仍按上述记录保留。
