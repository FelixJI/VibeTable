# history.read Python/Go 对照语料

`contracts/v2/fixtures/history-read-python-parity.json` 的预期结果来自仍由 Python BFF 承接 `history.read` 的独立、干净 checkout，当前生产提交为 `0ae2a75a2d85d764520a0fa1574bc4e4df7417c2`。生成器记录生产提交和 Python 版本，不从待验收的 Go 实现反推预期值。

在消费分支的锁定 Python 环境中运行（`<producer-checkout>` 指向该历史提交）：

```text
uv run python tests/contract/generate_history_read_parity.py --producer-root <producer-checkout> --output contracts/v2/fixtures/history-read-python-parity.json
go -C sidecar test ./internal/app -run '^TestHistoryReadMatchesFrozenPythonParityCorpus$' -count=1
```

生成器使用历史契约模型、RPC dispatcher、PocketBaseProductRpc 与 HTTP transport；仅将权威 HTTP 的响应替换为受控历史页或错误。记录完整 Python RPC 响应及真实发出的 HTTP method/path/query。语料覆盖 Unicode 筛选、可选字段、非空历史页、参数验证与 handler 错误阶段、两类存储错误；不是历史用户数据库快照。

Go 消费端使用实际 product dispatcher 与 history.read registration，以只读接口替身捕获 `audit.ReadParams` 并映射为历史 HTTP 请求语义。比较完整响应时仅移除 Go 独有的认证 `wire` 外壳，不忽略业务结果、错误码或错误数据。这不验证实际 HTTP 路由、PocketBase 投影刷新、完整认证链或 WebView2 页面；这些边界由相应集成测试和包含本次改动的新构建物验收。

新增样例应先确定输入与受控权威响应，再由固定历史 producer 重新生成。不得因 Go 测试失败而手改预期结果，也不得换用已经移除 Python history.read owner 的 checkout。若复用现有 `.venv`，可沿用本机 `UV_PROJECT_ENVIRONMENT` 和 `--frozen --no-sync`，无需重建环境。
