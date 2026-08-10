# 微信读书可视化插件：weread-notes-dashboard

> **迁移说明：** 本目录是迁移前快照，不再是发布权威。当前源码、lock、CI 与正式
> `.vtplugin` Release 位于
> [FelixJI/VibeTable-WeRead-Notes-Dashboard](https://github.com/FelixJI/VibeTable-WeRead-Notes-Dashboard)。
> 新版本和问题修复请在独立仓库维护；VibeTable 插件中心使用该仓库的正式 Release 远程安装。

用于把微信读书导出的阅读/笔记数据做聚合分析，生成书籍、笔记/标注和导出字段分布的可视化看板。

## 目录结构

- `manifest.json`：插件声明（id、权限、动作、视图入口）
- `schemas/action-input.v1.json`：动作入参映射（可配置字段名）
- `schemas/action-output.v1.json`：动作结果结构
- `src/weread-insights.ts`：Worker 业务逻辑（可读+可维护）
- `dist/workers/weread-insights.js`：构建产物（运行时加载）
- `dist/views/weread-overview/*`：自定义视图页面（HTML/CSS/JS）
- `tests/offline.test.mjs`：离线结构性测试

## 一、标准发布（build + pack）

1. 安装依赖（仅第一次执行）

```bash
cd examples/plugins/weread-notes-dashboard
npm install
```

2. 构建校验（会重新打包 Worker）

```bash
npm run build
```

3. 打包成可安装文件

```bash
npm run pack
```

4. 安装到 VibeTable

- 打开插件管理页 → 安装插件 → 选择 `*.vtplugin`。
- 安装后在表格工具栏会出现动作：**生成微信读书可视化**。
- 点击后在自定义视图打开：**阅读与笔记可视化**。

> `npm run pack` 成功后，会在当前目录输出 `.vtplugin`，通常形如：
> `weread-notes-dashboard-1.0.0.vtplugin`（以实际版本号为准）

## 二、与微信读书导出字段的对应关系（重要）

插件不直接抓取微信读书接口。请先保证微信读书 skills 已将数据落到当前集合，再在本插件中配置字段映射。

默认映射（可在运行参数里修改）：

- `book_title`：书名
- `author`：作者
- `category`：分类
- `read_progress`：进度（0~100）
- `read_minutes`：阅读时长（分钟）
- `note`：笔记内容/高亮文字
- `note_count`：该条记录对应笔记数（可选）
- `note_created_at`：记录时间（可选）
- `note_type`：笔记/标注类型（可选，例如：文本笔记、划线、想法）
- `chapter`：章节（可选）
- `location`：位置/页码（可选）
- `highlight_color`：高亮颜色（可选）
- `quote`：原文片段（可选）
- `startDate`：分析起始日期（可选，支持 YYYY-MM-DD）
- `endDate`：分析结束日期（可选，支持 YYYY-MM-DD）

## 三、运行结果与可视化功能（当前已实现）

### 已实现功能（v1）

- 基于配置字段聚合：
  - 书籍数、累计分钟、平均进度、有效记录数
  - 分类 Top（按记录数）
  - 笔记/标注 Top 书籍
  - 阅读时长 Top 书籍
- 标注导出增强（新）：
  - 标注类型 Top（如文本笔记、划线、想法等）
  - 章节 Top
  - 高亮颜色 Top
  - 最近标注明细（显示书名、时间、类型、章节、颜色、位置信息）
- 标签词频：
  - 按笔记文本拆分统计高频词（用于快速识别主题）
- 结果回填：
  - `metrics`：用于任务摘要卡片
  - `artifacts`：`{ kind: "weread-overview", payload }`，可被视图持续渲染

### 日期区间过滤（新增）

- 新增配置项：
  - `startDate`：起始时间（可选）
  - `endDate`：结束时间（可选）
- 支持输入格式：
  - `YYYY-MM-DD`
  - 可解析的 ISO 日期字符串
- 过滤逻辑：
  - 仅处理 `note_created_at` 落在区间内的记录
  - 不输入起始或结束表示单边无限
  - 若起始晚于结束，自动交换两个边界，避免因输入顺序导致误过滤
- 输出增强：
  - summary 中会显示 `处理条数 / 原始条数 / 过滤范围`
  - 启用过滤但无命中时会返回 warning，提示检查时间范围与 `note_created_at`

### 结果状态提示

- 空集合或字段映射错误时返回 warning，同时尽量返回可读的零值统计，便于你快速修正字段映射。

## 四、微信读书skills能力 vs 插件已实现能力

- 微信读书 skills（官方）能力（按描述）
  - 搜索书籍
  - 管理书架
  - 查看笔记与划线
  - 浏览书评
  - 阅读统计
  - 发现推荐书籍
- 本插件已实现能力
  - 基于集合字段的书籍/分类聚合看板
  - 读书进度与阅读时长聚合
  - 书籍、分类、标注类型、章节、颜色、标签 Top 排行
  - 最近标注明细渲染（书名、时间、类型、章节、颜色、位置信息）
  - 按日期区间过滤分析
  - 输出字段映射与可视化渲染能力
- 当前未覆盖（可扩展）
  - 微信读书源侧动作（搜索/书架管理/书评/推荐）本插件不直接执行

## 五、下一步可扩展方向

- 增加“每本书近30天标注趋势”
- 增加“导出分析快照（CSV/JSON）”能力（写文件权限）
- 增加“按作者/系列/出版社”维度分析
