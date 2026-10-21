# 微信读书可视化插件 weread-notes-dashboard（冻结历史 fixture）

> **历史 fixture，非可安装示例。** 本目录保留一个真实的旧
> `vibetable.plugin-manifest.v1` / `pluginApi 1.x` Worker 包，仅用于旧包
> 只读解析、迁移诊断与保全测试（#486）。它不会被构建、安装或作为新包使用，
> 也不要在本目录运行 `npm install` / `npm run build`；CLI `build` / `pack` 对它
> 固定拒绝（`legacy_worker_package`）。

- `manifest.json`、`schemas/`、`src/weread-insights.ts`：v1 Worker 源码与清单的
  历史原样快照（`src` 引用已退役的 `PluginAction` SDK 入口，仅作历史记录，
  不参与类型检查或构建）。
- `weread-notes-dashboard.vtplugin`：保留下来的唯一真实旧包 ZIP。旧版 packer
  写入的带 BOM `integrity.json` 保持历史原样，当前只读核验对它 fail closed、
  不执行旧代码；`tests/tools/test_vibetable_plugin_cli.py` 用它验证旧包
  fail-closed 与 build/pack 拒绝，成功路径的 `legacy: true` 迁移诊断由合成
  v1 fixture 覆盖。
- 曾经的 `release/`、`tmp/` 验证副本与 `examples/plugins/weread-notes-dashboard.vtplugin`
  重复包已在 #486 移除；需要新版本时请在独立仓库构建。

在用的迁移后示例见 `examples/plugins/data-overview/`（surface 视图）、
`examples/plugins/normalize-text/`（headless 动作）、`examples/plugins/record-manager/`
与 `examples/plugins/team-tasks-app/`（完整应用）。旧 Worker 包的迁移路径见
`docs/plugin-development.md`。
