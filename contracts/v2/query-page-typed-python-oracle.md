# query.page typed-shape 补充语料生成

`query-page-typed-python-oracle.json` 的三个 case 补充固定 producer
`c97c83336e4aa1bdf993fc46a7de57040219fb03` 的 Python 输出：Unicode/falsy 分页、
空页偏移和公开错误。输入响应具备 Go typed Page/QuerySnapshot DTO 的字段形状，仍由
真实 Python dispatcher、adapter/client 投影。snapshotId/digest 是固定不透明测试值，
不是有效签名、真实领域快照或 cursor 生命周期证据；不可替代真实 Query Port/HTTP 测试。

配方复用同一 producer 的 window Case 与 capture_case，不修改原 window oracle。
必须从保留的未迁移 producer 工作区运行，令 PYTHONPATH 指向该工作区，再以绝对路径
执行本配方。该工作区须包含迁移前的 window 捕获脚本；backend 必须与上述 Git 基线
一致，包括暂存和未提交修改。配方只读比较 Git tree，不切换或加载任意 Git revision。

PowerShell 示例（两个路径由操作者填入实际工作区，不提交本机路径）：

```powershell
$env:PYTHONPATH = $producerRoot
Set-Location $producerRoot
uv run --frozen --no-sync python "$recipeRoot/contracts/v2/generate_query_page_typed_oracle.py" --check
```

原件尚不存在时可将 `--check` 换为 `--write`，仅能排他创建配方同目录的输出。
默认只读比较；已有文件永不覆盖。当前 Go owner 环境在捕获前拒绝执行，即使输出文件
不存在也不能重新生成。环境由既有 uv 锁定环境复用，配方不安装依赖、不创建环境。
