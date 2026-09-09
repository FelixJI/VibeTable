# Unicode 数据往返资格边界

A5 的源码集成测试 `tests/integration/test_unicode_data_io_roundtrip.py` 在同一次 sidecar
构建下，以独立工作区分别验证 UTF-8 CSV、带 BOM 的 CSV 和 XLSX preview/apply，
再核对真实 Go authority 查询及 CSV、XLSX 导出。
每一阶段都对照固定文本和独立列出的 Unicode codepoint 序列：

- NFC 与 NFD 的重音文本，含 Emoji、肤色修饰符和 ZWJ；
- CJK 基本区文本和补充平面字符 U+20000；
- 阿拉伯文、数字和 U+200F 方向标记；
- `I`、`i`、U+0130、U+0131、U+00DF 与 `SS`，保持原始大小写。

这证明上述文本的数据保存和往返不发生规范化、大小写折叠或方向标记丢失。
它不证明 RTL 的视觉排版、locale 排序/搜索，或 packaged UI 的完整矩阵。
A5 仍为部分完成；falsy/container 的独立边界见
[数据互操作资格](data-io-interoperability.md)。

本地聚焦入口（复用锁定环境）：

```text
uv run --frozen --no-sync python -m pytest tests/integration/test_unicode_data_io_roundtrip.py -k test_unicode_code_points_survive_import_authority_read_and_exports --no-cov -q
```

聚焦入口不统计全后端覆盖率；完整 CI 仍执行仓库既有的 85% 覆盖率门禁。
