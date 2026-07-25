# 附件历史版本发现

- Go 工具链位于 `C:/Users/felji/PycharmProjects/vibetable/.tools/go-full/go/bin`。
- 当前 audit preview 对 attachment 字段明确返回“必须通过 attachment manifest workflow”，尚未实现该 workflow。
- 当前 attachment metadata collection 为 `vibetable_attachment_meta`；integrity 已校验现行引用、metadata、hash/size 与孤儿。
- `Manager.Prepare` 在 PocketBase 记录保存前构造文件字段，在同一事务的 finalizer 中删除/新增 metadata；`CleanupRecord` 目前硬删 metadata，但都没有在 PB 原文件被删除前复制历史二进制。
- audit 目标镜像只保存 PB 文件字段的 stored names。恢复 token 当前只保存 scalar patch，Apply 只会生成 insert/update/restore；attachment 被明确排除。
- 版本 manifest 必须包含 table/record/field/stored name、原名、MIME、size、hash、storage key，并能按目标 revision/附件名解析。版本归档应在原文件仍存在且事务提交前完成，失败必须阻止 mutation。
- `MutationKernel.applyOperation` 的顺序是 Prepare/Save/finalizer，再写 audit；因此 manifest 不应依赖尚未生成的 revision ID，而应以原 PB `stored_name`（包含随机后缀）作为不可变内容身份，audit 目标镜像据此解析。
- 内部版本 collection 采用受保护、单文件、100 MiB 上限的 `core.FileField`。把历史 blob 直接挂在 manifest record 上，可复用 PB 的事务文件生命周期和全库 backup，不需要手写外部对象提交补偿。
- restore preview 会校验当前待移除文件和目标历史文件的 manifest、size、hash、MIME 与现行字段 policy；token 仅保存版本身份，不保存二进制。
- restore apply 先把经过二次校验的 live/version 源暂存为内部 handle，再生成 `setAttachments` operations，与 scalar/insert/restore operations 一起交给同一 MutationKernel。
- integrity 现同时覆盖现行 metadata、版本 manifest/blob、audit 引用、版本 namespace 孤儿；backup 集成测试直接检查 zip 中包含版本 blob。
