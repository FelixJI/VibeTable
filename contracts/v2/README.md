# VibeTable Contracts v2

`contracts/v2` 是 VibeTable 唯一的语言无关契约 seam。当前协议由两个深模块组成，
共享 `contractVersion: "2.0"`，但各自隐藏不同领域的生成与验证实现：

- `product-contracts.schema.json` 与 `fixtures/product-rpc-catalog.json`：数据表、字段、
  Mutation、产品错误、数据任务事件和产品 RPC。
- `contracts.schema.json` 与 `fixtures/rpc-catalog.json`：Workspace、Snapshot、
  FileHistory、Lease、Replica、Retention 与 Conflict。

规则：

- 所有 wire object 均 closed；未知字段、缺少 required、非法 enum、尾随 JSON 必须失败。
- workspace 请求/响应/事件必须携带声明的 wire scope；切换 workspace 后旧 epoch 的响应、
  事件和 capability 一律丢弃。
- 不保留历史版本目录、冻结 corpus 或 v1→v2 adapter。协议变更直接更新 v2 schema、fixture、
  运行时消费者和跨语言 round-trip 测试。
- 产品与 workspace catalog 分别由专用生成器维护，避免调用方或测试依赖另一个模块的内部
  registry。

更新契约后运行：

```powershell
uv run python contracts/v2/generate_product_rpc_catalog.py
uv run python contracts/v2/generate_rpc_catalog.py
uv run python contracts/v2/generate_product_rpc_catalog.py --check
uv run python contracts/v2/generate_rpc_catalog.py --check
uv run python scripts/automation_project.py contracts
```
