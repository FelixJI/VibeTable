# 任务台示例应用（application + headless）

演示 VibeTable 插件 manifest v2 的两种新形态（Goal #480 / Task #482）：

- `open-task-desk`（`presentation: "application"`）：插件贡献**完整业务视口应用**，
  不需要 `ui.customViews`、不需要 Worker。单入口 `ui/index.html` 内通过 hash 路由
  组织三个内部页面（任务列表 / 详情 / 统计）；宿主仅保留始终可达的导航、返回平台
  与插件管理入口。
- `remember-active-table`（`presentation: "headless"`）：无界面动作。宿主内置受控
  文档在隔离 origin 内加载 `ui/headless.js` 模块；同一受限 surface API（`context.read`、
  `storage.set`），完成/失败通过 close 事件回报，宿主显示可见状态并释放 token。

两个动作共用一个插件包、同一权限声明与同一 surface API；没有 Worker、没有
Node 扩展协议、没有常驻 Python 执行器。页面脚本通过外部文件加载（CSP
`script-src 'self'` 禁止内联脚本），不依赖 npm 构建。

打包（无 npm 依赖）：

```
uv run python scripts/vibetable_plugin.py pack examples/plugins/team-tasks-app --output build/plugins/team-tasks-app.vtplugin
```
