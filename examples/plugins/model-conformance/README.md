# 合成三表模型

通过生产 SDK 的 `client.model.configure({ mode: "create" | "map" })` 请求原生配置。
这是合成数据示例，没有任意表、活动表或通配字段权限。

使用已锁定、已恢复的 esbuild，通过仓库 `scripts/vibetable_plugin.py build` 构建，再通过同一脚本 `pack` 打包安装。
原生确认之前不创建业务表。只有 `status === "completed"` 才能使用 `bindings[entity].tableId` 和 `bindings[entity].fields[field]` 访问声明字段。
用户取消返回 `null`；请求失败或窗口关闭后，先调用 `model.read()` 或 `model.status({ requestId })` 查看已提交范围，不把异常当作整体回滚。

升级只增加表或字段，使用更大的模型 `version` 并重新确认。字段名称可由用户重命名；绑定始终使用 canonical ID。
数字声明必须写明 `integer` 或 `float64`，当前合同不提供精确十进制类型。关系两端必须互相声明 `entity`、`field` 与各自的 `cardinality`。
