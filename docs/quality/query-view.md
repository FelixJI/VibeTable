# query.view Go owner 资格

`query.view` 通过默认 Host Product gateway 直达既有 Go `ExecuteViewQuery`。Python 删除该方法注册和专属客户端转发；PocketBase authority、业务查询、分组分页和 revision 配对不变，没有 fallback。

原33例 Python oracle 保持不变；固定生产者和捕获方式见 `contracts/v2/query-view-python-oracle.md`。Go HTTP 测试完整比较22个可表达案例，其他11例明确区分 typed Port 不可达形状、原 REST 边界和进程间 transport 错误，不伪造 parity。真实 Port 测试覆盖两层分组、计数与汇总、独立分组分页、snapshot校验和取消。

本地迁移端点 f3095171：Python相关84项、聚焦Go race与vet通过；完整app race仍记录既有附件fixture TempDir清理失败，不声称全包通过。桌面首次完整运行2项路由期望未同步，修正后相关71项通过。独立 Standards / Spec 均无发现。

该端点完整组件构建通过（未使用release模式）；真实包 S02 `02-all-field-schema` 于20260907T223935Z通过。新增分组／汇总控件检查两组计数与金额、零值、Unicode、DOM结果和 `table.datasetReady` revision配对；包freshness及场景清理门禁通过。此证据不替代合入最新main后的完整PR required、release build/smoke及合并后CI/CD。
