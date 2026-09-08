# query.selectionOpen Go Product 迁移

来源 PR284 将原子选择读取迁至 Go Product gateway，调用既有 Query Port；Python 删除专属 handler/client 注册与转发。筛选、排序、snapshot/cursor 仍由原 authority 处理，不新增 owner 状态机，也不 fallback。

原28例 Python oracle 保持不变。另保留4例从固定未迁移 Python producer 捕获的 typed 补充语料，其输入使用真实 Go DTO可表达的完整 capabilities。逐例适用性与重现方式见 [冻结语料说明](../../contracts/v2/query-selection-python-oracle.md)；不能通过补默认值改变旧 normalizedQuery 或将 typed 不可达样本记成通过。参数拒绝、公开错误、取消和真实选择窗口由 Go adapter、Product HTTP及Port测试分别覆盖。

来源端点39e05a9的新包 S02/S17于20260907T212817Z通过。来源最终端点a1067c8f包含main8cf989c5，完整CI34167677408成功；此前dispatcher fixture遗漏selection注册的CI失败已修正并保留证据。旧本地全app race清理失败及native race崩溃不改写为通过。

本批与独立通过CI的readRows、cursorOpen/cursorFetch整合，并同步包含 query.view 的 main 6ed36810，最终13Go/87Python/2native，总102。五类查询HTTP fixture完整注册并用不可调用守卫阻止误调用。原始语料和生产适配器保留；最新main的page恢复及S18、view的S02、原游标S17流程一并保留。来源测试不替代组合端点的相关验证、双轴审查、完整fresh CI及squash后的main CI/CD；闭环前不声明组合批次已交付。

本次main合并工作树的失败与聚焦修正证据统一保留在[游标资格记录](query-cursor-window.md)。来源339b686d的完整CI成功不覆盖新合并端点；本地Go TempDir清理失败仍未解决，最终fresh CI和产品包资格pending。
