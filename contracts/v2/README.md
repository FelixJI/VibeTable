# VibeTable protocol/catalog v2

`contracts/v2/contracts.schema.json` 是 Workspace、Snapshot、FileHistory、Lease、
Replica、Retention 与 Conflict 跨进程契约的语言中立来源。

规则：

- v2 wire object 全部 closed；未知字段、缺少 required、非法 enum、尾随 JSON 必须失败。
- global 请求使用 `scope: "global"`；工作区请求/响应/事件必须带
  `workspaceId + sessionEpoch + operationId + sequence`。
- 切换工作区会旋转 `sessionEpoch`；旧 epoch 的响应、事件和 capability 一律丢弃。
- `contracts/v1` 已冻结；v2 生成器不得修改 v1 文件。
- 更新 registry 后运行：

  ```powershell
  python contracts/v2/generate_rpc_catalog.py
  python contracts/v2/generate_rpc_catalog.py --check
  ```
