# Dashboard/Panel Product owner 本地资格

## 固定 Python oracle（生产迁移前）

基于远端 main `a3ca78b9181a529d978f9fba46586fbb924ecada`，七个公开 `insights.*` 方法作为完整 Dashboard/Panel 聚合迁移；不迁移命名 Version 或 SharedSettings，不提前声明 L5 或 L6–L10 完成。

`contracts/v2/dashboard-python-oracle.json` 固定原 Python producer 的 49 个样本。捕获器经原 dispatcher、Pydantic DTO、InsightsService 和 PocketBaseInternalMetadataPort；仅以受控底层 authority responses 替代 I/O，并固定测试 UUID 来源。覆盖公开七方法、列表投影/排序、workspace revision、原子草稿/配置引用、内置 panel manifest/options、records/aggregate 查询转换与限额、DTO/error。生成器从固定可达 Git commit 归档 backend，在独立 `build/contract-oracles/dashboard-python/replay-*` 目录重放；子进程 `-I` 排除继承 Python 路径，不依赖迁移后的 Go/Python owner。UUID 是既有 ID 分配 seam；workspace revision 沿旧规范 JSON 算法，不添加额外摘要身份层。

- `uv run --frozen --no-sync python contracts/v2/generate_dashboard_python_oracle.py --write` 首次生成；随后 `--check` EXIT 0。
- `uv run --frozen --no-sync pytest tests/contract/test_dashboard_python_oracle.py -q --no-cov`：4 PASS、0.79s（`build/dashboard-oracle-tests.log`），含真实旧 producer 重放与篡改投影拒绝。相关 Ruff format/check 通过。首次 lint 曾发现两个循环闭包绑定警告，已显式绑定后复验通过。

## 有意修复与实现边界

旧 Python 创建会在 receipt 之前随机分配 Dashboard/Panel ID、读取当前状态；删除只删除 Dashboard 而不级联 Panel。这些不是应复制的兼容语义：新 Product owner 必须通过真实 Runtime 普通写 gate，在已验证请求摘要后恢复完整原结果/ID 映射，优先于状态读取，并在同事务内级联删除。原错误样本保留；新语义由独立回归覆盖同/异 payload、删除后重放、Runtime/PB 重启、epoch/取消、CAS/事务回滚。七方法、合法 DTO/投影、查询限额及既有 workspace revision 保持，关闭 dashboards/panels generic 写且保留必要读。

当前仅冻结 oracle，Go owner、Host Product 接线、generic 写关闭及 S16 新重启段尚未实施。没有发布构建、GUI、新包、远端 CI 或独立双轴资格；此文后续只追加实际完成和失败证据。