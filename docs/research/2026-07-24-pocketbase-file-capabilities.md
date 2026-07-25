# PocketBase 文件能力与 VibeTable 落地边界

> 调研日期：2026-07-24  
> 资料范围：PocketBase 当前官方文档及 `pocketbase/pocketbase` 官方源码  
> 结论：PocketBase 原生文件能力足以覆盖 VibeTable 的记录附件场景；需要自研的是产品界面和可选的全局资产库，而不是基础文件存储。

## 结论摘要

PocketBase 的 `file` 字段原生支持单文件、多文件、大小和 MIME 约束、上传、替换、追加、单项删除、受保护下载、图片缩略图、HTTP Range、本地或 S3-compatible 存储以及整库备份。

因此，VibeTable 中普通的图片、PDF、音视频和其他记录附件应直接作为 PocketBase 记录字段保存。只有产品明确需要跨记录复用、全局搜索、标签、文件夹或资产版本管理时，才值得增加独立的 `vibetable_files` Module。

需要特别说明的是：PocketBase 的记录元数据位于 SQLite，文件内容位于本地文件系统或 S3。数据库写入可以使用 ACID 事务，文件写入则由 PocketBase 使用上传失败清理、保存失败回滚清理和保存成功后删除旧文件等补偿机制维持一致性，不属于跨 SQLite 与对象存储的严格 ACID 事务。

## PocketBase 原生能力

| 能力 | 官方行为 | VibeTable 判断 |
|---|---|---|
| 字段配置 | `file` 字段支持 `required`、`maxSelect`、`maxSize`、`mimeTypes`、`thumbs`、`protected`、`hidden`、`presentable` 和 `help`。`maxSize=0` 时默认每文件 5 MiB。 | 后端原生等价；建表和字段编辑 UI 需要暴露这些约束。 |
| 单文件/多文件 | `maxSelect <= 1` 时记录值是字符串；`maxSelect >= 2` 时是文件名数组。 | 原生等价。 |
| 大小和 MIME 验证 | 文件大小按单文件上限验证；MIME 通过读取文件内容检测，而不只是依据扩展名。 | 后端原生；前端可做同约束的快速预检，但后端仍是权威验证源。 |
| 上传 | 创建或更新记录时使用 `multipart/form-data`；官方 SDK 可直接提交 `File` 或 `Blob`。文件名会清理并追加随机后缀。 | 原生等价。 |
| 替换和追加 | 直接设置字段表示替换；`field+` 追加、`+field` 前插。 | 原生等价。 |
| 删除 | 将字段设为 `""` 或 `[]` 删除全部；`field-` 删除多文件字段中的指定文件。旧文件在记录成功保存后清理。 | 原生等价。 |
| 下载 | 使用 `GET /api/files/{collection}/{record}/{filename}`；`download=1` 强制下载。 | 原生等价。 |
| Protected 文件 | 文件默认在知道完整 URL 时可访问；字段标记为 `protected` 后，可使用短期 file token，并按集合 `viewRule` 检查访问资格。 | 原生等价。单机应用仍应通过 VibeTable 的受控接口签发访问地址。 |
| 图片缩略图 | 字段预配置允许尺寸后，可通过 `?thumb=...` 按需生成和缓存；支持居中/顶部/底部裁剪、fit 和仅宽/高缩放。不支持或生成失败时回退原图。 | 原生等价；前端负责选择合适规格并展示。 |
| Range 和缓存 | 文件响应基于 Go `http.ServeContent`，支持 Range、If-Match、If-Unmodified-Since 等；默认缓存头为 `max-age=2592000, stale-while-revalidate=86400`。 | 原生等价，适合 PDF、音视频和大文件预览。 |
| 存储后端 | 默认存入 `pb_data/storage`；也支持 S3-compatible 存储。 | 单机 VibeTable 使用本地存储即可，不需要单独引入对象存储。 |
| SQLite 关系 | `pb_data/data.db` 是主数据库，`pb_data/auxiliary.db` 保存日志等辅助信息；File 字段在数据库中只保存文件名，文件内容位于本地 storage 或 S3。记录文件键为 `collectionId/recordId/filename`。 | 备份和工作区设计必须同时考虑数据库与 storage，不应把附件误认为 SQLite BLOB。 |
| 备份 | 可在停机时复制整个 `pb_data`；也有 Dashboard 和 API 的列举、创建、上传、删除、恢复和下载。内建备份是 `pb_data` ZIP，创建期间应用临时只读，包含本地上传文件，但不包含 S3 中的上传文件。 | 单机本地方案可直接使用内建备份；恢复后会重启 PocketBase 进程。 |
| Hooks | 提供 `OnFileDownloadRequest`、`OnFileTokenRequest` 以及记录 validate/create/update hooks。事务 hook 内应使用 `e.App`。 | 足以实现 VibeTable 的访问控制、审计和下载行为定制。 |
| 自定义 Go route | `OnServe` 可注册自定义 endpoint，`e.FindUploadedFiles()` 可解析 multipart 文件；自定义 route 默认约 32 MiB body limit，可重绑 `apis.BodyLimit`。 | VibeTable 统一 mutation endpoint 可以同时接收记录数据和附件。 |
| 数据库事务 | `RunInTransaction` 可使记录、公式结果、审计、幂等键和 outbox 等数据库写入共同提交或回滚；事务中必须使用回调提供的 `txApp`。 | 适合作为 VibeTable 权威写入路径。文件本身仍采用补偿一致性。 |

### “按表指定存放位置”的准确含义

PocketBase 标准 `FileField` 会自动按 `collectionId/recordId/filename` 生成对象键，并统一通过应用级 filesystem 写入本机 `pb_data/storage` 或全局 S3-compatible 后端。因此：

- 每张表（collection）天然拥有独立的物理命名空间，不需要 VibeTable 再分目录；
- 备份、删除、缩略图、受保护下载和记录生命周期都能继续使用 PocketBase 原生实现；
- `FileField` 没有“为某张表选择任意本机目录”或“为每张表配置不同 S3 后端”的标准选项；
- 首版把“按表存放”定义为 PocketBase 原生命名空间，不在建表界面提供磁盘路径或存储后端选择器；
- 如果未来真的需要任意目录或 per-table 独立存储后端，届时再增加 `AttachmentStorage` Module、第二个存储 Adapter 和迁移/备份规则，而不是提前创建只有一个实现的抽象层。

## 文件事务边界

推荐的记录附件写入顺序为：

1. VibeTable 自定义 Go route 解析并验证 multipart 文件。
2. 在 `RunInTransaction` 中使用 `txApp.Save(record)` 保存记录，并同时写入公式结果、审计、幂等键和 outbox。
3. PocketBase 的 FileField interceptor 负责上传新文件。
4. 数据库保存失败时，PocketBase 尝试清理已上传的新文件。
5. 记录保存成功后，PocketBase 再删除被替换的旧文件及相关缩略图。

这保证了较强的一致性，但文件系统或 S3 不参与 SQLite 提交协议。VibeTable 的故障测试仍应覆盖：

- 文件上传成功但数据库保存失败；
- 多文件上传中途失败；
- 数据库保存成功但旧文件清理失败；
- 进程在上传、提交或清理阶段退出；
- 本地 storage 文件缺失或存在孤儿文件；
- 备份和恢复后记录引用与文件内容一致。

## 当前 VibeTable 的两类“文件”

当前产品界面实际混合了两个不同概念：

| 当前入口 | 实际含义 | PocketBase 替换后的处理 |
|---|---|---|
| 工作区文档 | 本地受管版本文件，由 VibeTable 工作区管理版本和恢复。 | 保留为本地工作区能力，不迁入 PocketBase File 字段，也不把它称为记录附件。 |
| “云端资源附件” | 当前按钮处于 disabled 状态，是为 Directus Files 记录附件预留的占位入口。 | Directus 从未实际发布，不保留“云端”兼容概念；改名为“托管附件”，作为记录的 File 字段使用。 |

单机本地 PocketBase 下不再存在有意义的“本地文件 vs 云端资源”二分。附件虽然由 PocketBase 管理，但实际仍存放在本机 `pb_data/storage`，因此：

- 删除独立的“云端资源”tab；
- 将“云端资源附件”改名为“托管附件”；
- 上传入口放在具体记录的 File 字段单元格或记录详情中；
- 字段创建时允许配置单/多文件、必填、单文件大小、允许 MIME、缩略图规格和 protected；
- 记录删除、字段清空、导入导出、审计和备份都围绕记录字段语义处理；
- 工作区文档继续使用现有本地版本模型，避免与记录附件共享生命周期。

## 何时才需要 `vibetable_files` Module

PocketBase File 字段是记录作用域的附件能力，不是 Directus 风格的全局资产库。仅当产品确认需要以下能力时，才增加独立的 `vibetable_files` Module：

- 一个文件被多条记录引用；
- 跨表搜索全部文件；
- 文件夹、标签、收藏或全局最近使用；
- 独立于记录的文件生命周期；
- 文件级版本、审核或发布状态；
- 丰富元数据查询，例如按原始文件名、MIME、大小、尺寸或哈希筛选；
- 去重和引用计数。

如果未来引入该 Module，建议由 `vibetable_files` collection 保存原始文件名、MIME、大小、哈希和 File 字段，再由业务记录使用 relation 引用。第一版不应为尚未确认的资产库需求增加这层间接性。

## 官方一手资料

- [PocketBase Files upload and handling](https://pocketbase.io/docs/files-handling/)
- [PocketBase Collections / FileField](https://pocketbase.io/docs/collections/)
- [PocketBase API Records](https://pocketbase.io/docs/api-records/)
- [PocketBase API Files](https://pocketbase.io/docs/api-files/)
- [PocketBase API Backups](https://pocketbase.io/docs/api-backups/)
- [PocketBase Going to production / Backup and Restore](https://pocketbase.io/docs/going-to-production/)
- [PocketBase Extend with Go / Filesystem](https://pocketbase.io/docs/go-filesystem/)
- [PocketBase Extend with Go / Routing](https://pocketbase.io/docs/go-routing/)
- [PocketBase Extend with Go / Database](https://pocketbase.io/docs/go-database/)
- [PocketBase Extend with Go / Event hooks](https://pocketbase.io/docs/go-event-hooks/)
- [PocketBase `core.FileField` implementation](https://github.com/pocketbase/pocketbase/blob/master/core/field_file.go)
- [PocketBase file size and MIME validators](https://github.com/pocketbase/pocketbase/blob/master/core/validators/file.go)
- [PocketBase file download and thumbnail API implementation](https://github.com/pocketbase/pocketbase/blob/master/apis/file.go)
- [PocketBase filesystem implementation](https://github.com/pocketbase/pocketbase/blob/master/tools/filesystem/filesystem.go)
