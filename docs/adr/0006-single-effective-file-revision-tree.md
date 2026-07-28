# ADR 0006：文件历史采用单文档修订树与唯一有效叶

- 状态：已接受
- 日期：2026-07-28
- 决策者：VibeTable 产品与工程

## 背景

现有 scheme/main/adoption 模型允许多条产品主线，和用户理解的“一个文件、一个当前版本、可查看历史”不一致。

## 决策

- `FileDocument` 只有一个 `effectiveRevisionId`，且必须指向叶节点。
- 修订最多一个父节点、可以有多个子节点；从历史继续编辑创建新叶。
- autosave 不占正式版本号；晋升创建引用同一对象的新 formal 修订。
- 恢复历史内容创建新的 restore 正式叶并分配下一个版本号，不截断历史。
- `files/` 只物化当前有效叶；对象内容进入统一 repository。
- 中间节点不能直接激活，只能“从此升级”；复制创建新 document。

## 后果

旧 `SchemeRef`、`SchemeStatus`、`SchemeService`、`SchemeAdoptionService`、`mainHead` 和 device-local 权威 index 在 consumer 切换后删除。
