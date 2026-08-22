# 跨进程 seam 索引

> 本页只索引现行边界。PocketBase/Go sidecar 是业务数据权威；Web 不取得数据库、对象仓库或本机
> 绝对路径，WPF 持有进程生命周期和 path grant。

| 边界 | 现行入口 | Authority / session | 错误与取消语义 | 现有验证 |
|---|---|---|---|---|
| Web → WPF WebView2 bridge | `desktop/web-grid/src/bridge/hostBridge.ts`、`services/workspaceV2HostAdapter.ts` → `MainWindow.Product.cs` / `Services/WebMessageRouter.cs` | Host 白名单；`workspaceId`、`sessionEpoch`、request/operation id 绑定当前 session。 | Host bridge 以稳定 error/reply 结束 request；旧 epoch 响应不得写入新 session。 | `hostBridge.test.ts`、`workspaceV2HostAdapter.test.ts`、`WebMessageRouterTests.cs`。 |
| WPF dispatcher → Python BFF | `JsonRpc*Gateway.cs` → `backend/rpc/{server,dispatcher,messages,framing}.py` | WPF 是进程与本机能力 owner；Python 不拥有 SQLite 写路径。 | JSON-RPC error envelope；调用方传递取消，具体 method 保持闭集。 | `JsonRpcProductDataGatewayTests.cs`、`tests/backend/rpc/*`。 |
| WPF → Go sidecar v2 RPC | `WorkspaceV2HttpGateway.cs` → `/api/vibetable/v2/rpc` → `protocolv2/dispatcher.go` → `workspacev2/runtime.go` | sidecar/PocketBase 是业务数据 authority；wire 绑定 `workspaceId`、`sessionEpoch`、fence/operation/sequence，path grant 仅由 Host 注入。 | `workspaceV2ErrorEnvelope` 使用稳定 JSON-RPC envelope；context cancel 传播到 Go handler。 | `WorkspaceV2HttpGatewayTests.cs`、`protocolv2/dispatcher_test.go`、`workspacev2/runtime_test.go`。 |
| Go sidecar capability → Web 可见性 | `Runtime.Capabilities()` → `/api/vibetable/v2/capabilities` → `MainWindow.Product.cs` bootstrap → Web adapter | Producer capability 与 Host allowlist 必须共同满足；Web 只消费 Host 下发的闭集。 | capability 缺失返回稳定 `workspace.capability_unavailable`；不得以 UI 猜测替代。 | `workspacev2/runtime_test.go`、Desktop capability/dispatcher tests，以及能力矩阵所列最终产品场景。 |
| Web Document Diff → WPF → sidecar | `document.diffRequested` / `document.diffCancelRequested` → `WorkspaceDocumentOsAdapter` / coordinator → Host-only `fileHistory.materializeDiffPair` 与 `assertEffectiveRevision` | sidecar 固定 target/effective revision；Host 持有两文件 path grant 和 epoch/sequence lease；Web 只持 entry handle/revision id。 | operationId + entryHandle 精确取消；materialize 前后 CAS stale 映射为稳定 `stale`；epoch 轮换取消在途请求，raw Web materialize 被拒绝。 | Workspace/OpenXml engine tests、Desktop adapter/cancel tests、Web service/store/view tests、产品场景 `14-document-diff`。 |
| WPF test-mode controls → 产品 E2E | runner 写入受控 controls dir → WPF test-mode picker/normal-close watcher | 仅 `--test-mode + --e2e-controls-dir` 可用；production 始终使用 native picker，不向 renderer 暴露 raw path route。 | 缺失/无效 fixture fail closed；normal close 后报告 Host exit、后代进程和端口释放。 | `ProductE2eControlTests`、`WorkspacePathGrantStoreTests`、runner 契约，以及[当前场景声明](../quality/product-e2e-capability-index.md)与对应 `required` 报告。 |

## 维护规则

- 新跨进程 operation 同时更新本页、capability 矩阵、producer/Host/Web 的闭集测试和至少一条产品证据。
- session/epoch 轮换、取消与错误 envelope 属于 wire 行为；行为保持型重构不得顺手改变。
- Document Diff 继续复用 sidecar authority、Host path grant 和 epoch seam；不得建立 Web 到 repository 的旁路，
  也不得把本机绝对路径放进 bridge payload。
