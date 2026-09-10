# ContentProfile / RecordDocumentLink 原 Python 公开契约

固定 producer：`79c2ce4faa53d65af1fa9ee297655739d5407a2e`。
`generate_content_metadata_oracle.py` 在该源码状态以真实 RpcDispatcher、注册函数、
ContentModelService 和 Pydantic DTO 捕获 28 例公开请求；metadata 与 schema/record 查询为
明确的内存 adapter，metadata revision 复现现有 Go canonical payload 契约。
脚本在捕获前核对 producer 文件 Git diff，并验证实际 import 来自当前 worktree。
这些结果证明 Python 公开编排与投影，不代表 packaged UI、真实 PocketBase 或并发资格。

迁移保持成功结果和 domain error 的 code/message/path；参数错误只比较 -32602，
Pydantic 自身的英文 validation details 不成为 Go 的新实现负担。
旧实现先读 revision 再调用持久化，不能成功重放已提交的 commit/repair/delete；
此缺陷通过新增权威事务测试修复，不将旧失败冻结为必须保留的业务语义。

本文件和 JSON 原件不由新 Go 实现重生成。捕获实现保留在首次冻结提交中。

迁移额外边界以锁定的原 DTO/源码为依据：`expectedRevision: ""` 不等于 null/create；
`summaryFieldId: ""` 的 field_missing 必须保留显式空 `path`；order 的整数小数字符串
（如 `"1.0"`）可接受，指数/十六进制/非零小数尾部字符串不能通过浮点舍入放行。
Go 的真实 Product HTTP 测试覆盖空 path，参数测试覆盖上述 order 反例；原 28 例原件不改写。
