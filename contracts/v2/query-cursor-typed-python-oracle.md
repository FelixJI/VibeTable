# 游标窗口 typed-shape Python 补充语料

`query-cursor-typed-python-oracle.json` 固定 producer 为
`c97c83336e4aa1bdf993fc46a7de57040219fb03`。13 个案例执行该 producer 的真实
Python dispatcher、Product 参数模型、adapter/client，仅下游 transport 被脚本化。
本配方复用 producer 的 `generate_query_window_oracle.capture_case`；不覆盖旧 27 案例原件。

两种方法分别覆盖 Unicode/falsy、terminal、公开错误、null rows、null row 元素及
hasMore/nextCursor 不一致；另保留 fetch 的空字符串 nextCursor 语义。
cursorOpen 使用合法 offset=0、limit=2。snapshot 具备完整 Go QuerySnapshot 字段形状，
但 snapshotId/digest 与输入 cursor 都是固定不透明测试值，不代表有效领域签名、真实
游标生命周期或实际 HTTP 可达性。非法返回形状只验证 adapter 拒绝边界，不声称真实
Query Port 会产生这些返回值。

Go HTTP 测试完整比较 JSON-RPC 版本、id、整个 result/error，并单独验证新增 wire
原样回传；同时核对 typed Port 调用次数、方法、table/query/cursor。公开错误注入取自
producer 的脚本化异常输入，不从冻结 expected envelope 反向构造。
真实 PocketBase 分页、终止、Unicode/falsy、签名、修订 stale 与 selection authority
游标续读由同文件的独立测试验证，不能被这些脚本化语料替代。selection 测试不是完整
Python transport 或宿主路由证据。

旧 17 个 cursor 案例逐项处理如下；旧 JSON 不修改：

| 原案例 | 新 HTTP 对照范围或不适用原因 |
|---|---|
| open-empty-table、open-unknown-field | 完整参数拒绝，无 authority 调用 |
| fetch-empty-cursor、fetch-null-cursor、fetch-unknown-field | 完整参数拒绝，无 authority 调用 |
| fetch-product-error | 完整公开错误及 authority 调用 |
| open-unicode-falsy、open-terminal-window | limit=0 在原 REST 层不可达 typed Port；合法输入投影由补充语料覆盖 |
| open-malformed-snapshot | limit=0 已先被拒绝；null snapshot 也不能表示 Go QuerySnapshot 值类型 |
| open-inconsistent-null-cursor | limit=0 已先被拒绝；合法输入的对应拒绝由补充语料覆盖 |
| open-product-error | limit=0 已先被拒绝，不能到达脚本化错误；补充语料覆盖合法输入 |
| open-transport-error | limit=0 已先被拒绝，且进程内 Port 不再具有 Python→HTTP transport 异常边界 |
| fetch-unicode-cursor、fetch-terminal-window、fetch-empty-next-cursor | 原 partial snapshot 无法保持完整 typed 投影；补充语料覆盖相应语义 |
| fetch-malformed-next-cursor | 数字 nextCursor 无法表示 Go `*string` |
| fetch-transport-error | 进程内 Port 不具有 Python→HTTP transport 异常边界；未知领域错误独立验证 |

旧语料不证明 HTTP 大小预算；迁移 adapter 的参数/REST 两层预算由聚焦边界测试负责。
明确排除的 11 例不会被算成通过，6 例完整对照和 13 例补充对照分别计数。

生成配方只能从保留的未迁移 producer 工作区执行。该工作区必须含迁移前的 window
捕获脚本，且 backend（包括暂存和未提交修改）须与上述 Git 基线一致。配方只读执行
`git diff --quiet`，不检出 Git revision、不创建环境、不安装依赖。

PowerShell（变量由操作者指定实际目录，不提交本机路径）：

```powershell
$env:PYTHONPATH = $producerRoot
$env:UV_PROJECT_ENVIRONMENT = $existingLockedEnvironment
Set-Location $producerRoot
uv run --frozen --no-sync python "$recipeRoot/contracts/v2/generate_query_cursor_typed_oracle.py" --check
```

原件不存在时可用 `--write` 排他创建；现有原件永不覆盖。默认与 `--check` 只读比较，
偏差即失败。当前 Go owner 环境在捕获之前拒绝执行，即使指定 `--write` 也不写入。
使用现有 uv 锁定环境；语料检查不替代完整质量、产品 E2E 和发布门禁。
