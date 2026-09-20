# Host 命令与快捷方式

#347 将六个 command/shortcut 方法收归 WPF，并在原快捷键窗口增加设备级管理面板。原 Python 实现没有产品 UI 调用者，快捷方式仅驻留内存，URL launch 只返回成功。本次补齐真实行为，不保留占位兼容路径。

## 使用与边界

从侧栏帮助按钮打开“命令、快捷方式与快捷键”。打开工作区并选择表后，可导出当前筛选查询，或把“导出当前查询”保存为有名称的快捷方式。每次执行都重新选择输出目标、获取一次授权；CSV/XLSX 流式导出全部匹配行；只传行查询的关键词、过滤和排序，网格分组与分组分页不进入导出契约。定义不保存查询、路径、grant 或工作区身份，切换表后使用当时的查询。

HTTPS 快捷方式保存名称和 URL。执行时 Host 展示完整 URL，用户确认后交默认浏览器处理；拒绝、关闭确认框、工作区切换均不能报告已打开。禁止 file-action、非 HTTPS、URL 凭据、任意 shell/脚本及自定义命令。原快捷键帮助保留，不提供键位重绑定。

设备定义保存在 Host 数据根的 commands/shortcuts.json，以文件互斥和同目录原子替换提交，最多128项。损坏文件不会被覆盖为空；失败回显，草稿仍保留。定义跨工作区和 Host 重开保留，运行权限不持久化。

## 接口与生命周期

| 接口 | Owner / 权限 | 行为 |
|---|---|---|
| command.list / command.run | WPF / 当前 workspace epoch | 固定 export.query；执行参数为collection/query/format，由Host选择文件 |
| shortcut.list / save / delete | WPF / 当前 workspace epoch | 设备定义读写，GUID身份和目标白名单 |
| shortcut.launch | WPF / 当前 workspace epoch | 根据持久定义执行；导出另带当前params，URL必须经原生确认 |
| path.revokeExportTarget | 现有 Python Task/Data IO / hostOnly | 已知grantId → settled回执；撤销未来准入，取消并等待该授权下已准入的导出writer |

必要DTO改动是移除caller grant、无实现的accelerator/file-action，要求非空名称和GUID，给shortcut.launch增加一次执行params和output。公开CreateTaskParams/TaskStatus不变。新增hostOnly撤销接口是为了找回已创建但task.create回复被退休的导出，不能只依赖回复中的taskId。

Host命令持有epoch lease至执行和收尾结束。正常请求仍经完整当前绑定准入；唯一允许退休后使用的动作，是固定旧Python client上本次grant的撤销/收尾，不向renderer提供通用RPC或旧epoch执行开关。命令两分钟上限；清理另有10秒通信上限，未获settled回执不会报告成功。永久断线、进程强杀后的产物恢复不在本轮验收范围，不能据本轮测试宣称零遗留；完整Task/grant/Worker迁移仍属后续范围。

Python复用现有TaskRuntime和ExportService。撤销与export准入串行，授权即使过期也能找到活动writer；取消join覆盖CSV文件和openpyxl临时worksheet，提交点后的取消保留成功结果和完整目标。业务查询与数据authority仍归Go/PocketBase。

## 验证入口

- 原Python服务14项测试由test_host_commands_legacy_service.py在固定producer 4259359697e812e962b6859c92be60cc0c11bbb3隔离执行；当前生产不再加载旧服务。
- HostCommandRequestControllerTests覆盖六方法、存储重开、非法参数、HTTPS拒绝、epoch退休；HostProductRpcInvokerTests使用真实JsonRpcClient及epoch drain验证创建回复丢失后仍按grant收拢旧client。
- test_host_export_cleanup.py覆盖真实CSV/XLSX writer的取消、授权过期、准入先后与提交后取消；HostCommandsPanel.test.ts覆盖UI命名/编辑/执行/删除、重开、错误草稿和退休回复。
- 真实包S33的seed/resume两阶段分别正常退出Host，验证定义跨工作区与第二Host恢复，从UI执行实际CSV导出、HTTPS原生取消和删除；截图与运行结论保存在该次QA证据目录，最终结果以PR记录为准。
- 完整入口遵循quality/pr-e2e/build/smoke及当前head required；本页不将未跑场景或上游Windows清理问题写成通过。
