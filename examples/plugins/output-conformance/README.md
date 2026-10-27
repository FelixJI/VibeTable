# 合成产物验收示例

仅生成普通及大于 1 MiB 的合成 XLSX，使用真实插件 SDK 的 `output.create`、原生保存、附件归档及原字节重开。无业务账、网络或新增运行依赖。

通过仓库 `scripts/vibetable_plugin.py build/pack` 构建；构建环境复用现有 surface-data-editor 的 esbuild（本次命令设置 `VIBETABLE_ESBUILD_JS`）。真实 WebView2 场景为 `55-plugin-surface-outputs`。产物中的 ZIP CRC32 是格式要求，由 ZIP reader 验证。

归档前需要当前表具有允许 XLSX、单文件至少 32 MiB 的附件字段和一条合成记录；归档执行宿主精确确认。来源版本是插件声明的元数据，不表示平台验证了业务内容。保存、重印和改变模板不会自动提交业务操作。
