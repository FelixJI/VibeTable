# DataIO 外部输入准入与文件授权资格

本变更承接成熟度方案 A5 的文件路径、授权与系统字段边界。PocketBase 继续作为唯一数据权威；本次不迁移 L7 的完整任务所有权，也不新增关系映射或 Lookup 选择 UI。

## 根因与行为

导入计划此前没有绑定原始 grant；实际 Go mutation 完成后才消费 grant。真实 dispatcher/sidecar 回归证明：grant 过期、换成同路径另一 grant，或已消费 grant 搭配另一有效计划，都可能先写入记录再报告失败。

现在计划绑定 grant、目标表与导入模式。`SessionPathGrantStore.reserve()` 在副作用前检查用途、方向、过期与消费状态，并阻止同一 grant 重入。只有成功回执才消费授权；失败或取消释放占用。TTL 决定是否准入，已经准入的写入成功返回后不再因 TTL 到期被误报为失败。原插件直接消费授权的语义保持不变。

Host 原先在进入错误处理前读取文件元数据；无效选择可绕过关联的 `PATH_GRANT_FAILED` 响应。现在参数物化在既有异常边界内执行。

## 可执行资格

- `tests/integration/test_data_io_path_grants.py`：真实 RPC dispatcher、Host 专用注册方法和源码 sidecar，覆盖 CSV/XLSX 超过 280 字符的中文、NFD 与 ZWJ 路径；授权过期、错配、已消费与重放的零写入拒绝；真实 mutation 返回后推进授权时钟，仍成功且仅调用一次。比较 rows、总数、schema/data revision，不比较每次查询自然变化的 snapshot ID。
- `tests/integration/test_data_io_system_fields.py` 与固定 JSON corpus：CSV 自动映射、XLSX 显式映射不接受伪造系统 ID/日期，权威生成系统值；双格式导出的日期与权威一致；Go 拒绝直接修改只读字段且数据及版本不变。
- `test_path_grant_reservation.py`：过期准入、占用期间拒绝重入、成功跨 TTL、失败/取消释放、离开作用域后提交回调失效。
- Host controller 测试：保留设备名 `CON.csv`、`NUL.csv` 只作为无效路径字符串测试，不创建或打开设备流；真实长路径验证元数据传递。

这些测试证明接口与数据契约，不声称覆盖真实文件选择对话框交互、RTL 排版或区域排序。

## 本地验证记录

使用冻结 Python 环境及同一主干源码构建的 sidecar，未修改锁文件或依赖来源。

- 原实现授权拒绝：3 项失败，能观察到不应发生的行和 revision 变化。
- 修复后路径授权集成：6 项通过；系统字段集成：2 项通过。首次组合运行暴露系统字段 fixture 未导入，6 通过、2 error；显式注册共享 fixture 后，保留仓库严格选项的组合运行 8 项通过。
- Python 后端测试：750 项通过，覆盖率 88.91%，高于 85% 门禁。
- Host 文件请求 controller：22 项通过。
- 全仓 Ruff、后端 Pyright/mypy 通过。后续计划绑定回归、项目质量入口及包级验证结果仍需补入，pending 不计为通过。

本次不声称解决任务通知失败、进程崩溃后的幂等恢复或完整 Host 任务所有权；这些仍由 L7 后续纵向变更处理。
- 项目 Python 质量入口已运行：1910 passed、1 skipped，但全套覆盖率为 73.77%，未达到 85%，入口失败；不能以单独 backend 的 88.91% 替代。本地覆盖差异正在诊断。原格式检查失败日志亦保留；新增计划目标/模式回归 25 项通过。
