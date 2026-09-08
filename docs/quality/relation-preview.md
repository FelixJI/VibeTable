# relation.previewDelta 迁移资格

当前从main `6ed36810f3753caed5e2e8ca27a4d4ad2117d41d` 迁移单一只读方法，
闭集10 Go /90 Python /2 native。沿用唯一PocketBase authority与既有relation.Service/
Kernel.Preview；不改变apply、owner生命周期、数据格式或写入协调器。Python专属preview
handler/注册退役，共享_translate_delta/_renderer_target仍供其他方法使用。

## 契约及真实读取

37项原Python案例固定于main6ed。Go HTTP完整比较26个可表达案例的wire、请求翻译和响应；
11项旧HTTP transport/Go DTO无法表达的形状逐项说明，不当作领域成功。原JSON不重写。
delta回显原始参数，expectedDateUpdated不传authority，current为修改前集合，diagnostics空，
保留两层1MiB、Unicode/深度、原错误投影与上下文取消。capture/write已明确退役，默认检查历史输入。

真实many关系fixture在已有A时预览增加B/删除A：返回旧A，记录、schema/data metadata、
audit/outbox/idempotency状态不变；Service重复目标与Kernel目标不存在拒绝也保持零写入。

Host使用已存在Go forwarder及workspace lease，不回退Python，保留Relation错误和迟到响应语义。
完整.NET首次暴露Host强制updates与当前前端及Python公开六字段契约冲突；仅preview改为
接受六字段，apply保留旧约束，有回归区分两者。S28从真实many编辑器加载已关联目标，再增加
本地选择并取消，比较源/目标双方记录及schema/data revision。S06历史语义不变；历史main
23/23样本不覆盖新S28，manifest gap保留S26并新增S28。

## 本地证据

- Go聚焦race：app25.920s、dispatcher1.696s、capabilities1.257s passed。
  `go test -race ./internal/app ./internal/productrpc ./internal/contracts/productcapabilities -run 'TestRelationPreviewProduct|TestQueryViewProductHTTP|TestQueryPageProductHTTP|TestNewRequiresRegistrations|TestGenerated' -count=1`。
- `go test -race ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1`：passed10.504s；相关四包go vet通过。
- Python相关64项首次63 passed/1 failed：新增无fallback测试用了非法extra参数；改为合法六字段后adapter文件21 passed0.33s，未重跑整组取绿。
- locked .NET restore复用缓存；完整solution1198 passed/7 failed/1既有skip，7项均由preview Host updates冲突触发。修正后六类相关Host测试99 passed243ms；未宣称完整solution再跑全绿。
- `uv run --frozen --no-sync python -m pytest tests/contract/test_product_e2e_capability_index.py tests/e2e/test_product_e2e_runner.py -q --no-cov`：150 passed38.19s。
- Ruff、Pyright0、Go vet、policy生成检查及冻结输入检查通过。原Go gcc/uv沙箱启动失败与Python/.NET失败日志保留；获准使用缓存后执行的测试与未启动尝试区分。

复用uv环境、npm junction、锁定Go/CGO与.NET/NuGet缓存，不更改lock、依赖来源或CI。
代码/metadata独立Standards0、Spec0；文档增量两处命令/覆盖范围问题已修正，待复核。
全组件本地包构建通过；真实S28于QA `20260908T042431Z` 1/1 passed。加载权威旧目标、
本地选择与取消后双方rows/schema/data revision保持、bridge/renderer诊断全部通过；package audit
及四组件新鲜度通过，正常Host退出0、成员/后代为空、端口释放及lease/final cleanup通过。
包来自本次提交前已审查的最终生产代码，后续文档修改未改变产品输入；未重建环境。
最终fresh CI、squash和同SHA main CI/CD均未完成。
0.5.0/N-1兼容移出当前开发验收，现有CI及不支持格式零写入拒绝契约保持。

Python首轮精确命令：
`uv run --frozen --no-sync python -m pytest tests/backend/test_main_product_data.py tests/backend/adapters/test_pocketbase_product_rpc.py tests/contract/test_product_rpc_capability_policy.py tests/contract/test_product_runtime_inventory.py tests/contract/test_relation_preview_python_oracle.py -q --no-cov`。
修正后仅运行 `uv run --frozen --no-sync python -m pytest tests/backend/adapters/test_pocketbase_product_rpc.py -q --no-cov`。
真实包：`uv run --frozen --no-sync python scripts/build_next.py`；产品：
`uv run --frozen --no-sync python -m tests.e2e.product_e2e_runner --scenario 28-relation-delta-preview`。