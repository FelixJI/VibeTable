# 插件完整能力服务产品 QA

`52-plugin-capability-services` 是 file-io lane 的独立合成旅程。它编译仓库真实 Plugin SDK、经正常 packer 产生 manifest.v2 包，安装到本次新工作区，并在禁用 NodePluginWorkerAdapter 的真实 WPF/WebView2 中执行。它不依赖 48 的打印阶段；48 原生打印和 51 unknown 提交回执继续是独立必需门禁。

在受控 Windows CI 新包上执行：

```powershell
$env:GITHUB_ACTIONS = "true"
$env:CI = "true"
uv run --frozen --no-sync python tests/e2e/product_e2e_runner.py --package-root VibeTable.Next --evidence-root build/qa/483-capability-services --scenario 52-plugin-capability-services
```

正式资格仍由 `uv run python qa/next.py --ci` 和 required check 判定。不得为了本机调查设置上述标记并启动真实系统 UI。

场景通过 SDK 查询活动表、拒绝未授予表/未知字段、预览非法混合批次并核零提交；经原生确认拒绝/批准 update、create、delete 和合法自关系更新。普通文件 picker 先真实取消，再实际选择、读取、一次性使用拒绝和保存字面内容；附件实际选择、暂存、原生确认后读回原内容；CSV 导入生成真实任务/预览和批准后的 Go 行，CSV 导出只投影明确字段并逐字核对选定输出。context、通知、任务列表/取消、返回、范围内单调失效、unsubscribe，以及退役旧 token/跨 surface 文件和附件 handle 均有真实调用。unsubscribe 后通过新订阅真实送达作为观察屏障再核旧 listener 零事件；SDK SurfaceTransport 透传原始行为，前后两 surface 各创建/读取/释放12个真实SDK client，每轮必须回到精确 listener baseline=1。辅助合同故意漏 dispose 验证计数断言会失败，不用失效事件恰1冒充 listener 数量。51 独立覆盖提交回执 unknown；52 的批准前任务取消必须无写入。

原生 picker 沿既有文件宿主入口走真实 Windows 对话框。只有 E2E controls 中精确 `native-dialog` 且两个 CI 标记均为 `true`，才进入既有 dialog；普通控制缺失/空和非 CI 哨兵继续拒绝。picker 请求是闭集 kind/action，不接受路径。父 runner 从自身 Job 验证 Host，观察器复核 Host PID、`#32770`、闭集标题及通向 VibeTable 的 owner 链；仅观察固定文件名编辑框与接受/取消控件。在每次 SendInput 前再次检查归属、前景、遮挡与编辑焦点，输入只来自本次合成 controls 的四个固定文件。失败时只能再次验证并取消同一 owned dialog；最终 Job cleanup 回收自有进程。没有 UIA Invoke、自动批准、系统设置或打印机操作。

成功证据 `52-capability-services.json` 包含 SDK 回执、真实原生观测、逐阶段 Go query 结果、附件内容及实际导出 CSV；另有 picker/原生确认截图、最终页面和 Playwright trace。门禁对缺失必需证据 fail closed，失败保留固定的 picker 诊断，不归档工作区数据库或任意目录。

本机辅助合同/编译不能证明 Windows dialog 的实际 UIA 控件可见性、真实 SendInput 交付或产品全链完成。若特定 WebView2/Windows runner 的 filename 控件 ID/窗口链不同，本场景明确失败并保留诊断，不回退为测试路径选择或伪造成功；需以受控 CI 的真实观测修正。字段/附件边界的权威反例仍由 Host/Go/SDK 合同与独立审阅共同验收；普通附件本场景不替代约 1 MiB 附件包体边界回归。
