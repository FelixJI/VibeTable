# 文档差异资格语料

这些 fixture 用于锁定真实 Microsoft Office OOXML 的结构边界，不作为用户模板或产品运行时资产。
`manifest.json` 是覆盖清单；`OpenXmlQualificationCorpusTests` 还直接检查关键 package part，避免只验证
manifest 的自我声明。

## 来源与限制

- DOCX 由 Microsoft Word 16.0 生成，XLSX/XLSM 由 Microsoft Excel 16.0 生成。
- 提交前把 OOXML core properties、tracked revisions、comments 与 people parts 中的本机身份元数据统一
  匿名化为 `VibeTable Qualification`/`qualification`，并移除 Excel 自动保存的本机绝对路径扩展属性；
  测试会校验所有 XML/relationship parts 中的身份和路径字段，其他包结构仍来自对应 Office 应用。
- `docx/corrupt.docx` 是故意损坏的合成输入；其余文件均由对应 Office 应用保存。
- `xlsx/macro-container.xlsm` 包含一个 XLM macro sheet，单元格公式为惰性的 `RETURN()`。生成器没有
  调用该宏，测试只读取 ZIP/XML 结构；产品实现也不得执行宏、重算公式或更新外链。
- 检测到按用户安装的 WPS Office 12.1.0.28488，但没有注册 `KWPS.Application`、`WPS.Application` 或
  `Kingsoft.WPS.Application` COM ProgID。隐藏 `/n` viewer probe 被 launcher 委派后无法观察文档标题，
  因而结论是 `inconclusive`，不是“未安装”或“已验证兼容”。
- `qualification-results.json` 记录去本机路径后的命令设置与机器可读结果；`evidence/` 保留三个小型
  Clippit 派生包，使中文粒度、格式-only 和接受已有修订可以由 CI 直接复核。950 KB 综合比较输出、
  Word/WPS 临时输出及临时工具不提交。

## 复现命令

从仓库根目录运行，`<temp-tool-path>` 与 `<temp-output.docx>` 必须放在 `build/` 下：

```powershell
dotnet tool install Clippit.Cli --version 0.9.0 --tool-path <temp-tool-path>
<temp-tool-path>\clippit.exe version --format json
<temp-tool-path>\clippit.exe word compare <before.docx> <after.docx> --output <temp-output.docx> --author "VibeTable Qualification" --date-time 2026-09-05T00:00:00Z --format json
<temp-tool-path>\clippit.exe word accept-revisions <input.docx> --output <temp-output.docx> --format json
<temp-tool-path>\clippit.exe word verify <temp-output.docx> --format json
```

Word probe 使用独立隐藏 `Word.Application`；两份输入均以 `Documents.Open(..., ReadOnly=true)` 打开，
`CompareDocuments` 使用 `Destination=New`、`Granularity=WordLevel` 和显式 compare flags，所有文档均以
`SaveChanges=false` 关闭。WPS probe 只打开 `build/` 中的派生比较结果，不接触 corpus 源输入。

## 维护规则

- 新增或替换 fixture 时，必须同时更新 `manifest.json`、`qualification-results.json`（如适用）和成对
  结构/结果断言，并说明对应风险类别。
- CI 不依赖本机安装 Word/Excel/WPS；提交的文件就是测试输入。
- 不为这些本地 Git fixture 维护重复 SHA-256 清单。完整性由 Git tree、OOXML 结构断言和公开 diff 契约
  测试验证；只有出现独立跨系统交接契约时才增加由明确 producer/consumer 消费的 checksum。
