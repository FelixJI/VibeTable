# 关系完整性只读诊断资格

状态：实现与聚焦验证已完成；完整本地矩阵仍有失败，真实打包场景、截图、fresh PR CI 和合并闭环待完成。此记录不代表 RR2 inspect/repair 整体完成。

## 完整意图

`relation.inspectPair` 由 Go 直接读取 PocketBase 的持久关系定义与双端原始链接，不依赖正常 query/describe 成功。按稳定记录 ID 对双端分页，冻结双方 schema/data revision；读取失败、取消和 revision 变化显式拒绝，不返回空的健康结果，不写权威数据。

报告区分每页发现、分页结束与扫描覆盖。计数和样例是页局部数据；UI 累计链接问题，重复元数据问题取各页最大值，保留前页发现；`complete` 表示覆盖完整，不表示关系健康。返回游标只表示扫描进度，不授权修复。样例数量受限，截断明确显示。

字段设置保存显式请求的 tableId/fieldId，正常描述失败后仍能检查该目标；关闭、换目标或停止隔离迟到回调。Host 使用现有 Product 注册和 Go owner，不提供 Python fallback；既有领域错误 envelope 保留 revision_changed 文案。修复写入、owner 切换、独立稳定性修复不在本意图内。

## 已取得证据

- 真实 PocketBase `go test -race ./internal/relationpair ./tests/integration -run TestRelationPairInspect -count=1`：PASS75.497s；`go vet ./internal/relationpair` PASS。覆盖双端分页、401目标200/200/1读取、损坏元数据/链接/presence、样例截断、revision变化、取消与读取错误；日志 `build/qa/relation-inspect/race-final.log`、`vet-final.log`。
- 新 Product adapter 参数/领域错误/取消回归通过。能力与 dispatcher 完整 race 分别 PASS1.271s/1.700s；旧精确注册清单先 RED，补充唯一新增方法后断言通过。
- `npm run test:coverage`：176文件1554 PASS，56.88s，全部原有覆盖率门禁通过；日志 `web-full-final.log`。之后生成样例与真实 parser 的消费回归，旧样例1 FAIL→3文件59 PASS；修复只改变生成样例语义和测试。
- Host 新路由6项回归先因缺少注册全部失败，补充后相关83项 PASS、0 skip；`ProductContractV2RoundTripTests` 10 PASS。日志 `host-routing-red.log`、`host-routing-final.log`、`host-contract-catalog.log`。
- 既有 `{error: ...}` 领域返回的真实 resolved envelope 先2 FAIL，修复后模块53 PASS、vue-tsc通过；日志 `web-envelope-red.log`、`web-envelope-green.log`、`typecheck-envelope.log`。
- 16项新 DTO 测试通过；新增场景/索引/目录及 Python owner 闭集契约192 PASS。catalog、capability、inventory由现有脚本生成/校验，没有手工改生成物。
- Standards/Spec 双轴独立审查已覆盖实现；最新真实场景仍须在打包候选上验收。

## 未通过与未完成

完整 Go app race 整体 FAIL278.794s：`TestHistoryReadProductHTTPPreservesLegacyValidationAndPublicErrors` 在 TempDir 的 coordination 目录清理失败；一次诊断定向 race 同样 FAIL6.345s。未出现业务断言或 race detector 报告，但完整命令不能计为通过；事后目录为空不足以判断清理时的文件或持有者。保留 `go-fixture-race.log`、`go-fixture-history-diagnostic.log`，不放宽清理或盲重试。

完整 Python quality 初次因新增方法未进入精确期望集合失败，已精确修复。之后完整入口为1788 PASS、1 skip、1 FAIL，覆盖率91.42%，Ruff/Pyright/mypy通过；唯一失败是 `test_repeated_launch_and_close_does_not_leak_parent_handles` 的父句柄172与基线171不相等。一次原断言定向诊断通过仅说明未复现，不能替代完整入口；保留 `python-quality.log`、`python-quality-final.log`、`python-handles-diagnostic.log`，不扩大为全量通过。

新场景 `31-relation-pair-inspection` 已进入完整 manifest，声明101条来源的双页检查、检查零写入、页间修改后的拒绝及重新检查。尚未真实运行，实际表头编辑入口依赖关系双端更新 PR #305 的修复；该依赖必须按实际 main 结果同步后完成构建和场景验证。历史23场景 main 样本不覆盖新增场景，coverage index 明确列出 gap。

进程能力闭集补充：`go test ./cmd/vibetable-pb -run '^TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart$' -count=1` 旧清单 FAIL1.306s，精确加入唯一新增的 `relation.inspectPair`（20项、workspace scope）后 PASS1.720s；加 `-race` PASS10.142s。保留全部身份/失败关闭/重启断言，日志 `build/qa/relation-inspect/go-process-{red,green,race}.log`。不改变上述完整矩阵仍失败的结论。
