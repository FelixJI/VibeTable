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

## 宿主 Product 调用前置（尚未切换生产 owner）

`JsonRpcProductDataGateway(HostProductRpcInvoker)` 保持 typed method、严格 Schema v2 解析
及原 Python client 的通知。invoker 只读现有生成 policy/Workspace catalog 分类，拥有固定代际
HTTP 实例与一次共享握手；握手独立持有 epoch lease 至实际 HTTP 完成，单个 caller 的取消不终止
其他 caller 的握手，epoch drain／Dispose 取消全部自有调用。
每次调用取得现有 workspace epoch lease，在发送及返回（包括错误）时核对绑定；不重试到新代际，
不 fallback、shadow 或双发。Product wire 只有原 scope 字段，fence/claim 仍由 capabilities 验证。

`ProductionWorkspaceRuntimeFactory.CaptureHostProductRpcBinding` 在 factory → backend Ready admission
→ Sidecar context 的锁序中捕获不透明三元组，可附期望 workspace UUID/epoch；每次发送及返回由
同一 factory 核对 runtime、精确 Python client 和 canonical Sidecar snapshot，再委托现有 Sidecar
authority。callback 只同步启动调用，不持锁等待异步完成，也不保证子进程不会退出。
binding 只提供配对 client、完整代际比较与 typed gateway 构造；一次 capture 不延长 runtime 寿命。
内部 construction seam 复用真实 supervisors 与现有 process/health/HTTP adapter，生产默认 policy
不变；测试 policy 的 selector 与 canonical registrations 同源。

三个生产入口（MainWindow、LazyProductTableGateway、update health reader）尚需独立接入，再进行
L3A owner cutover。本前置不改变 renderer gateway lifecycle，也不宣称产品已走 Go。
`HostProductRpcInvokerTests` 在 typed gateway seam 使用实际 HTTP/JSON-RPC adapter 和 session drain
验证此契约；进程和网络由测试 peer 提供。
`HostProductRpcCompositionTests` 通过真实 factory/runtime、Python supervisor 和 session close，验证
非 Ready/错误期望不捕获、Python 或 Sidecar 换代拒绝旧发送/迟到响应，以及默认 owner 仍为 Python。

## 维护规则

- 新跨进程 operation 同时更新本页、capability 矩阵、producer/Host/Web 的闭集测试和至少一条产品证据。
- session/epoch 轮换、取消与错误 envelope 属于 wire 行为；行为保持型重构不得顺手改变。
- Document Diff 继续复用 sidecar authority、Host path grant 和 epoch seam；不得建立 Web 到 repository 的旁路，
  也不得把本机绝对路径放进 bridge payload。
