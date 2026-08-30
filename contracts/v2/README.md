# VibeTable Contracts v2

`contracts/v2` 是 VibeTable 唯一的语言无关契约 seam。当前协议由四个深模块组成，
共享 `contractVersion: "2.0"`，但各自隐藏不同领域的生成与验证实现：

- `product-contracts.schema.json` 与 `fixtures/product-rpc-catalog.json`：数据表、字段、
  Mutation、产品错误、数据任务事件和产品 RPC。
- `contracts.schema.json` 与 `fixtures/rpc-catalog.json`：Workspace、Snapshot、
  FileHistory、Lease、Replica、Retention 与 Conflict。
- `workspace-rpc-capability-policy.json` 与生成的
  `workspace-rpc-capability-manifest.json`：以 `generate_rpc_catalog.py::RPC_REGISTRY`
  为方法/scope authority，集中声明 Desktop renderer/host audience 与粗粒度
  capabilityId；Desktop Router 只消费生成 manifest。
- `product-runtime-ownership-inventory.json` 与生成的同名 schema：以 Product catalog
  精确覆盖 RPC 和事件，记录当前调用路径、稳定职责归属、effects、状态持有者及迁移切片；
  `product_runtime_inventory.py` 向审计、测试和后续迁移提供唯一的规范化读取 seam。
- `product-rpc-capability-policy.json` 与生成的 manifest/adapters：author 只声明
  method/topic、scope、audience 和 capabilityId；生成器与 Product catalog、runtime inventory
  join 后派生当前 owner/effect，供四端查询，L1 不改变生产 route。

规则：

- 所有 wire object 均 closed；未知字段、缺少 required、非法 enum、尾随 JSON 必须失败。
- workspace 请求/响应/事件必须携带声明的 wire scope；切换 workspace 后旧 epoch 的响应、
  事件和 capability 一律丢弃。
- workspace RPC policy 只声明 capabilityId 与 audience，并必须精确覆盖 registry；scope 直接由
  registry 派生，缺失、未知或重复方法均由生成器 fail closed。`rendererInternal` 与
  `hostOnly` 方法不得从 raw renderer bridge 进入。
- 不保留历史版本目录、冻结 corpus 或 v1→v2 adapter。协议变更直接更新 v2 schema、fixture、
  运行时消费者和跨语言 round-trip 测试。
- 产品与 workspace catalog 分别由专用生成器维护，避免调用方或测试依赖另一个模块的内部
  registry。
- Product runtime 清单不会切换生产路由。RPC 与事件必须精确覆盖 catalog；state 项只记录有
  代码证据的持有者，不宣称静态穷尽所有临时状态。调用方通过 loader/`require()` 消费，禁止
  自行解析并复制分组细节。`targetPr: null` 表示目标切片尚未形成迁移 PR；只有真实 PR 存在后
  才填写，不能用占位编号伪造关联证据。

更新契约后运行：

```powershell
uv run python contracts/v2/generate_product_rpc_catalog.py
uv run python contracts/v2/product_runtime_inventory.py
uv run python contracts/v2/product_rpc_capability_policy.py
uv run python contracts/v2/generate_rpc_catalog.py
uv run python contracts/v2/generate_workspace_rpc_capability_manifest.py
uv run python contracts/v2/generate_product_rpc_catalog.py --check
uv run python contracts/v2/product_runtime_inventory.py --check
uv run python contracts/v2/product_rpc_capability_policy.py --check
uv run python contracts/v2/generate_rpc_catalog.py --check
uv run python contracts/v2/generate_workspace_rpc_capability_manifest.py --check
uv run python scripts/automation_project.py contracts
```
