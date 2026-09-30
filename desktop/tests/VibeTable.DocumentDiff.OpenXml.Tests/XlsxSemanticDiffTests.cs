using System.IO.Compression;
using System.Text;
using System.Xml;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class XlsxSemanticDiffTests
{
    private const string S = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
    private const string R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/";
    internal sealed record Sheet(string Name, string Cells = "", uint Id = 1,
        string Part = "xl/worksheets/sheet1.xml", string State = "visible", string Extra = "",
        string? Rows = null);

    [TestMethod]
    public async Task TypedValues_ReportsCompleteIndependentOracle()
    {
        byte[] before = Package([new("数据",
            "<c r=\"A1\"><v>123456789012345678901234567890</v></c>" +
            "<c r=\"B1\" t=\"b\"><v>0</v></c><c r=\"C1\" t=\"e\"><v>#REF!</v></c>" +
            "<c r=\"D1\" t=\"d\"><v>2026-01-01T00:00:00Z</v></c><c r=\"E1\"/>" +
            "<c r=\"F1\"><v>12</v></c>")]);
        byte[] after = Package([new("数据",
            "<c r=\"A1\"><v>123456789012345678901234567891</v></c>" +
            "<c r=\"B1\" t=\"b\"><v>1</v></c><c r=\"C1\" t=\"e\"><v>#N/A</v></c>" +
            "<c r=\"D1\" t=\"d\"><v>2026-01-02T00:00:00Z</v></c>" +
            "<c r=\"F1\" t=\"inlineStr\"><is><t>12</t></is></c><c r=\"G1\"/>")]);
        DocumentDiffDetails details = await Details(before, after);
        Oracle(details,
            "Replace|数据|A1|value: number: 12345678901234567890123456789e1|value: number: 123456789012345678901234567891",
            "Replace|数据|B1|value: boolean: false|value: boolean: true",
            "Replace|数据|C1|value: error: #REF!|value: error: #N/A",
            "Replace|数据|D1|value: date: 2026-01-01T00:00:00Z|value: date: 2026-01-02T00:00:00Z",
            "Delete|数据|E1|cell: number: <empty>; style: default|",
            "Replace|数据|F1|value: number: 12|value: text: 12",
            "Insert|数据|G1||cell: number: <empty>; style: default");
        Assert.AreEqual(0, details.Changes[0].Location.RowIndex);
        Assert.AreEqual(0, details.Changes[0].Location.ColumnIndex);
        Assert.IsFalse(details.Coverage.Truncated);
    }

    [TestMethod]
    public async Task FormulaAndCache_AreIndependentIncludingMissingAndShared()
    {
        string b = "<c r=\"A1\"><f>1+1</f><v>2</v></c>" +
            "<c r=\"B1\"><f>3+3</f><v>6</v></c>" +
            "<c r=\"C1\"><f t=\"shared\" si=\"0\" ref=\"C1:D1\">A1+1</f><v>3</v></c>" +
            "<c r=\"D1\"><f t=\"shared\" si=\"0\"/><v>7</v></c>";
        string a = "<c r=\"A1\"><f>4-2</f><v>2</v></c>" +
            "<c r=\"B1\"><f>3+3</f></c>" +
            "<c r=\"C1\"><f t=\"shared\" si=\"9\" ref=\"C1:D1\">A1+2</f><v>3</v></c>" +
            "<c r=\"D1\"><f t=\"shared\" si=\"9\"/><v>8</v></c>";
        Oracle(await Details(Package([new("公式", b)]), Package([new("公式", a)])),
            "Replace|公式|A1|formula: normal: 1+1|formula: normal: 4-2",
            "Replace|公式|B1|cache: number: 6|cache: missing",
            "Replace|公式|C1|formula: shared: anchor=C1; ref=C1:D1; text=A1+1; own=A1+1|formula: shared: anchor=C1; ref=C1:D1; text=A1+2; own=A1+2",
            "Replace|公式|D1|formula: shared: anchor=C1; ref=C1:D1; text=A1+1; own=|formula: shared: anchor=C1; ref=C1:D1; text=A1+2; own=",
            "Replace|公式|D1|cache: number: 7|cache: number: 8");
    }

    [TestMethod]
    public async Task DefinitionAndIdentityReordering_IsSemanticallyEquivalent()
    {
        string styleBefore = StyleSheet(
            "<font><name val=\"A\"/></font><font><b/><name val=\"B\"/></font>",
            "<xf fontId=\"0\"/><xf fontId=\"1\" numFmtId=\"164\"/>",
            "<numFmt numFmtId=\"164\" formatCode=\"0.00\"/>");
        string styleAfter = StyleSheet(
            "<font><name val=\"B\"/><b val=\"true\"/></font><font><name val=\"A\"/></font>",
            "<xf fontId=\"0\" numFmtId=\"169\"/><xf fontId=\"1\"/>",
            "<numFmt numFmtId=\"169\" formatCode=\"0.00\"/>");
        string b = "<c r=\"A1\" t=\"s\" s=\"1\"><v>0</v></c><c r=\"B1\"><v>1.000e2</v></c>" +
            "<c r=\"C1\"><f t=\"shared\" si=\"1\" ref=\"C1:D1\">1</f><v>1</v></c>" +
            "<c r=\"D1\"><f t=\"shared\" si=\"1\"/><v>1</v></c>";
        string a = "<c r=\"A1\" t=\"inlineStr\" s=\"0\"><is><r><t>中文</t></r></is></c>" +
            "<c r=\"B1\" s=\"1\"><v>100</v></c>" +
            "<c r=\"C1\" s=\"1\"><f t=\"shared\" si=\"8\" ref=\"C1:D1\">1</f><v>1</v></c>" +
            "<c r=\"D1\" s=\"1\"><f t=\"shared\" si=\"8\"/><v>1</v></c>";
        var result = await Compare(Package([new("é", b)], styleBefore, "<si><t>中文</t></si>"),
            Package([new("e\u0301", a, 99, "xl/worksheets/changed.xml")], styleAfter,
                "<si><t>未引用</t></si><si><t>中文</t></si>"));
        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, result.Kind);
        Assert.IsNotNull(result.Details);
        Oracle(result.Details);
    }

    [TestMethod]
    public async Task Styles_ReportResolvedFontFillBorderNumberAndAlignment()
    {
        string before = StyleSheet("<font><name val=\"A\"/></font>", "<xf/>");
        string after = "<styleSheet xmlns=\"" + S + "\"><numFmts><numFmt numFmtId=\"164\" formatCode=\"yyyy-mm-dd\"/></numFmts>" +
            "<fonts><font><b/><name val=\"B\"/></font></fonts><fills><fill><patternFill patternType=\"solid\"><fgColor rgb=\"FF00FF00\"/></patternFill></fill></fills>" +
            "<borders><border><left style=\"thin\"/></border></borders><cellXfs><xf numFmtId=\"164\"><alignment horizontal=\"center\" wrapText=\"1\"/></xf></cellXfs></styleSheet>";
        Oracle(await Details(Package([new("样式", "<c r=\"A1\"><v>1</v></c>")], before),
            Package([new("样式", "<c r=\"A1\"><v>1</v></c>")], after)),
            "Format|样式|A1|style: font=font()[name(val=1:A)[]]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=|style: font=font()[b=true,name(val=1:B)[]]; fill=fill()[patternFill(patternType=5:solid)[fgColor(rgb=8:FF00FF00)[]]]; border=border()[left(style=4:thin)[]]; numFmt=yyyy-mm-dd; alignment=alignment(horizontal=6:center,wrapText=4:true)[]");
    }

    [TestMethod]
    public async Task SheetRenameReorderMergesAndVisibility_HaveCompleteOracle()
    {
        byte[] before = Package([
            new("原名", "<c r=\"A1\"><v>1</v></c>", Extra: "<mergeCells><mergeCell ref=\"A1:B1\"/></mergeCells>",
                Rows: "<row r=\"1\" hidden=\"1\"><c r=\"A1\"><v>1</v></c></row>"),
            new("第二", Id: 2, Part: "xl/worksheets/sheet2.xml")]);
        byte[] after = Package([
            new("第二", Id: 2, Part: "xl/worksheets/sheet2.xml"),
            new("新名", "<c r=\"A1\"><v>1</v></c>", State: "hidden",
                Extra: "<cols><col min=\"2\" max=\"3\" hidden=\"1\"/></cols><mergeCells><mergeCell ref=\"A1:C1\"/></mergeCells>")]);
        Oracle(await Details(before, after),
            "Move|第二||sheet order: 2|sheet order: 1",
            "Other|新名||sheet name: 原名|sheet name: 新名",
            "Move|新名||sheet order: 1|sheet order: 2",
            "Other|新名||visibility: visible|visibility: hidden",
            "Table|新名|A1:B1|merge: A1:B1|",
            "Table|新名|A1:C1||merge: A1:C1",
            "Other|新名||row 1 hidden: true|row 1 hidden: false",
            "Other|新名||hidden columns: <none>|hidden columns: 2:3");
    }

    [TestMethod]
    public async Task UnmatchedSheetIdentity_IsAdditionAndDeletion()
    {
        Oracle(await Details(Package([new("旧", "<c r=\"A1\"/>")]),
            Package([new("新", "<c r=\"B1\"/>", 2, "xl/worksheets/new.xml")])),
            "Delete|旧||sheet: 旧|",
            "Delete|旧|A1|cell: number: <empty>; style: default|",
            "Insert|新|||sheet: 新",
            "Insert|新|B1||cell: number: <empty>; style: default");
    }

    [TestMethod]
    public async Task SparseExtremeAddresses_VisitOnlyActualCells()
    {
        string rowsBefore = "<row r=\"1\"><c r=\"A1\"><v>1</v></c></row>" +
            "<row r=\"1048576\"><c r=\"XFD1048576\"><v>7</v></c></row>";
        string rowsAfter = rowsBefore.Replace("<v>7</v>", "<v>8</v>", StringComparison.Ordinal);
        var details = await Details(Package([new("极限", Rows: rowsBefore)]),
            Package([new("极限", Rows: rowsAfter)]));
        Oracle(details, "Replace|极限|XFD1048576|value: number: 7|value: number: 8");
        Assert.AreEqual(1_048_575, details.Changes[0].Location.RowIndex);
        Assert.AreEqual(16_383, details.Changes[0].Location.ColumnIndex);
    }

    [TestMethod]
    public async Task RowInsertion_IsAddressDeletionAndInsertionWithoutInventedMove()
    {
        Oracle(await Details(Package([new("位置", Rows: "<row r=\"1\"><c r=\"A1\"><v>9</v></c></row>")]),
            Package([new("位置", Rows: "<row r=\"2\"><c r=\"A2\"><v>9</v></c></row>")])),
            "Delete|位置|A1|cell: number: 9; style: default|",
            "Insert|位置|A2||cell: number: 9; style: default");
    }

    [TestMethod]
    public async Task CoverageAndLongSnippet_AreHonestAndBounded()
    {
        string text = new('中', 3000);
        var details = await Details(Package([new("长文", "<c r=\"A1\" t=\"inlineStr\"><is><t>旧</t></is></c>")]),
            Package([new("长文", "<c r=\"A1\" t=\"inlineStr\"><is><t>" + text + "</t></is></c>")]));
        Assert.AreEqual(1, details.Changes.Count);
        Assert.IsTrue(details.Coverage.Truncated);
        Assert.AreEqual(2048, details.Changes[0].After!.Runs[0].Text.Length);
        Assert.AreEqual("value: text: 旧", Snippet(details.Changes[0].Before));
        Assert.AreEqual("value: text: " + new string('中', 2034) + "…", Snippet(details.Changes[0].After));
        CollectionAssert.AreEquivalent(new[]
        {
            "WorksheetValues:Covered", "WorksheetFormulas:Covered", "WorksheetStyles:Partial",
            "WorksheetMerges:Covered", "WorksheetVisibility:Covered", "Structure:Partial",
            "Images:NotCovered", "Comments:NotCovered", "Fields:NotCovered",
        }, details.Coverage.Areas.Select(a => a.Area + ":" + a.Status).ToArray());
    }

    [TestMethod]
    public async Task MalformedCells_FailClosedEvenWhenBothPackagesAreEqual()
    {
        string[] invalid =
        [
            "<c r=\"XFE1\"><v>1</v></c>", "<c r=\"A0\"/>", "<c r=\"A1048577\"/>",
            "<c r=\"a1\"/>", "<c r=\"A1\"/><c r=\"A1\"/>",
            "<c r=\"A1\" t=\"b\"><v>2</v></c>", "<c r=\"A1\"><v>NaN</v></c>",
            "<c r=\"A1\" t=\"e\"><v>arbitrary</v></c>",
            "<c r=\"A1\" t=\"d\"><v>yesterday</v></c>",
            "<c r=\"A1\" t=\"s\"><v>0</v></c>", "<c r=\"A1\" s=\"1\"/>",
            "<c r=\"A1\"><f t=\"shared\" si=\"1\"/><v>1</v></c>",
            "<c r=\"A1\"><v>1</v><v>2</v></c>", "<c r=\"A1\" t=\"unknown\"/>",
        ];
        foreach (string cells in invalid)
        {
            byte[] package = Package([new("畸形", cells)]);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package), cells);
        }
        foreach (string name in new[] { "非法:", "'apostrophe", new string('a', 32) })
        {
            byte[] package = Package([new(name)]);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package));
        }
        byte[] duplicate = Package([new("Sheet"), new("SHEET", Id: 2, Part: "xl/worksheets/two.xml")]);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(duplicate, duplicate));
    }

    [TestMethod]
    public async Task DangerousPackagePartsRelationshipsAndDtd_FailClosed()
    {
        foreach ((string name, string xml) in new[]
        {
            ("../escape.xml", "<root/>"),
            ("xl/%2e%2e/escape.xml", "<root/>"),
            ("xl/workbook.xml", "<root/>"),
            ("xl/unselected.xml", "<!DOCTYPE root [<!ENTITY e SYSTEM \"file:///C:/secret\">]><root>&e;</root>"),
            ("xl/_rels/extra.xml.rels", "<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\"><Relationship Id=\"r1\" Type=\"" + R + "hyperlink\" Target=\"https://invalid.example\" TargetMode=\"External\"/></Relationships>"),
        })
        {
            byte[] package = Package([new("安全")], extra: [(name, xml)]);
            if (name.EndsWith("unselected.xml", StringComparison.Ordinal))
                await Assert.ThrowsExactlyAsync<XmlException>(() => Compare(package, package));
            else if (name.EndsWith("extra.xml.rels", StringComparison.Ordinal))
                await Assert.ThrowsExactlyAsync<NotSupportedException>(() => Compare(package, package));
            else await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package));
        }
        byte[] missing = Package([new("安全", Part: "xl/worksheets/sheet1.xml")],
            extra: [("xl/_rels/orphan.xml.rels", "<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\"><Relationship Id=\"r1\" Type=\"" + R + "worksheet\" Target=\"../../../escape.xml\"/></Relationships>")]);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(missing, missing));
    }

    [TestMethod]
    public async Task FixedBudgetsAndCancellation_AreExplicit()
    {
        byte[] manySheets = Package(Enumerable.Range(1, 257)
            .Select(i => new Sheet("S" + i, Id: (uint)i, Part: "xl/worksheets/s" + i + ".xml")).ToArray());
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(manySheets, manySheets));
        string rows = string.Concat(Enumerable.Range(1, 100_001)
            .Select(i => "<row r=\"" + i + "\"><c r=\"A" + i + "\"/></row>"));
        byte[] manyCells = Package([new("多单元格", Rows: rows)]);
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(manyCells, manyCells));
        string beforeRows = string.Concat(Enumerable.Range(1, 20_001)
            .Select(i => "<row r=\"" + i + "\"><c r=\"A" + i + "\"><v>1</v></c></row>"));
        byte[] b = Package([new("多变化", Rows: beforeRows)]);
        byte[] a = Package([new("多变化", Rows: beforeRows.Replace("<v>1</v>", "<v>2</v>", StringComparison.Ordinal))]);
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(b, a));
        string oversized = new('x', OpenXmlExtractionLimits.MaxVisibleTextCharacters + 1);
        byte[] text = Package([new("超长", "<c r=\"A1\" t=\"inlineStr\"><is><t>" + oversized + "</t></is></c>")]);
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(text, text));
        using var cancel = new CancellationTokenSource();
        cancel.Cancel();
        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            XlsxSemanticDiff.CompareAsync(new(Source(b), Source(a)), cancel.Token));
        var declaredOversize = new DocumentContentSource("large.xlsx", null,
            OpenXmlExtractionLimits.MaxNonSeekablePackageBytes + 1,
            _ => ValueTask.FromResult<Stream>(new MemoryStream(b, writable: false)));
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() =>
            XlsxSemanticDiff.CompareAsync(new(declaredOversize, Source(a)), CancellationToken.None));
    }

    [TestMethod]
    public async Task InputsAreReadOnlyAndReleased()
    {
        byte[] b = Package([new("只读", "<c r=\"A1\"><v>1</v></c>")]);
        byte[] a = Package([new("只读", "<c r=\"A1\"><v>2</v></c>")]);
        byte[] oldBefore = b.ToArray(), oldAfter = a.ToArray();
        var streams = new List<MemoryStream>();
        DocumentContentSource Content(byte[] bytes) => new("read-only.xlsx", null, bytes.Length,
            _ =>
            {
                var stream = new MemoryStream(bytes, writable: false);
                streams.Add(stream);
                return ValueTask.FromResult<Stream>(stream);
            });
        var outcome = await XlsxSemanticDiff.CompareAsync(new(Content(b), Content(a)), CancellationToken.None);
        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        CollectionAssert.AreEqual(oldBefore, b);
        CollectionAssert.AreEqual(oldAfter, a);
        Assert.IsTrue(streams.All(s => !s.CanRead));
    }


    [TestMethod]
    public async Task AmbiguousRenameIdentity_IsReportedAsSheetDeletionAndAddition()
    {
        // Multiple workbook sheets share an internal part; this cannot establish a unique rename.
        Oracle(await Details(Package([new("旧一"), new("旧二")]),
            Package([new("新一"), new("新二")])),
            "Delete|旧一||sheet: 旧一|", "Delete|旧二||sheet: 旧二|",
            "Insert|新一|||sheet: 新一", "Insert|新二|||sheet: 新二");
    }

    [TestMethod]
    public async Task MissingRowNumberAndEscapedText_AreDecodedWithoutFalseChanges()
    {
        Oracle((await Compare(Package([new("文本", Rows:
            "<row><c r=\"A10\" t=\"inlineStr\"><is><t>_x4E2D__x6587_</t></is></c>" +
            "<c r=\"B10\" t=\"str\"><v>_x005F_x0041_</v></c></row>")]),
            Package([new("文本", Rows:
                "<row r=\"10\"><c r=\"A10\" t=\"s\"><v>0</v></c>" +
                "<c r=\"B10\" t=\"inlineStr\"><is><t>_x005F_x0041_</t></is></c></row>")],
                shared: "<si><t>中文</t></si>"))).Details!);
    }

    [TestMethod]
    public async Task HiddenColumnIntervalDecomposition_IsEquivalent()
    {
        Oracle((await Compare(Package([new("列", Extra:
            "<cols><col min=\"2\" max=\"4\" hidden=\"1\"/></cols>")]),
            Package([new("列", Extra:
                "<cols><col min=\"3\" max=\"4\" hidden=\"true\"/><col min=\"2\" max=\"2\" hidden=\"1\"/></cols>")]))).Details!);
    }

    [TestMethod]
    public async Task DefinitionReordering_ResolvesFillBorderAndCustomFormat()
    {
        string b = "<styleSheet xmlns=\"" + S + "\"><numFmts><numFmt numFmtId=\"164\" formatCode=\"0.00\"/></numFmts>" +
            "<fonts><font/></fonts><fills><fill/><fill><patternFill patternType=\"solid\"/></fill></fills>" +
            "<borders><border/><border><left style=\"thin\"/></border></borders><cellXfs><xf/>" +
            "<xf fillId=\"1\" borderId=\"1\" numFmtId=\"164\"><alignment wrapText=\"true\"/></xf></cellXfs></styleSheet>";
        string a = "<styleSheet xmlns=\"" + S + "\"><numFmts><numFmt numFmtId=\"200\" formatCode=\"0.00\"/></numFmts>" +
            "<fonts><font/></fonts><fills><fill><patternFill patternType=\"solid\"/></fill><fill/></fills>" +
            "<borders><border><left style=\"thin\"/></border><border/></borders><cellXfs>" +
            "<xf fillId=\"0\" borderId=\"0\" numFmtId=\"200\"><alignment wrapText=\"1\"/></xf>" +
            "<xf fillId=\"1\" borderId=\"1\"/></cellXfs></styleSheet>";
        Oracle((await Compare(Package([new("定义", "<c r=\"A1\" s=\"1\"><v>1</v></c>")], b),
            Package([new("定义", "<c r=\"A1\" s=\"0\"><v>1</v></c>")], a))).Details!);
    }


    [TestMethod]
    public async Task FormulaAttributesAndDateSystem_HaveIndependentSemanticChanges()
    {
        Oracle(await Details(Package([new("日期", "<c r=\"A1\"><f t=\"dataTable\" r1=\"B1\" dtr=\"0\"/><v>1</v></c>")]),
            Package([new("日期", "<c r=\"A1\"><f t=\"dataTable\" r1=\"C1\" dtr=\"1\"/><v>1</v></c>")], date1904: true)),
            "Other|日期||date system: 1900|date system: 1904",
            "Replace|日期|A1|formula: dataTable: ; attributes=dtr=5:false,r1=2:B1|formula: dataTable: ; attributes=dtr=4:true,r1=2:C1");
    }

    [TestMethod]
    public async Task OverlappingColumnsAndMalformedFormulaAttributes_FailClosed()
    {
        foreach (Sheet sheet in new[]
        {
            new Sheet("非法列", Extra: "<cols><col min=\"1\" max=\"3\" hidden=\"1\"/><col min=\"2\" max=\"4\" hidden=\"0\"/></cols>"),
            new Sheet("非法公式", "<c r=\"A1\"><f ca=\"perhaps\">1</f><v>1</v></c>"),
            new Sheet("非法引用", "<c r=\"A1\"><f t=\"dataTable\" r1=\"XFE1\"/><v>1</v></c>"),
        })
        {
            byte[] package = Package([sheet]);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package));
        }
    }

    [TestMethod]
    public async Task PackageEntryXmlAndExpandedBudgets_AreEnforcedBeforeDomAllocation()
    {
        byte[] entries = Package([new("条目")], extra: Enumerable.Range(1, 4096)
            .Select(i => ("xl/extra" + i + ".xml", "<root/>")).ToArray());
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(entries, entries));
        string oversized = "<root>" + new string('x', (int)OpenXmlExtractionLimits.MaxXmlPartBytes) + "</root>";
        byte[] xml = Package([new("XML")], extra: [("xl/oversized.xml", oversized)]);
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(xml, xml));
        string part = "<root>" + new string('x', 14 * 1024 * 1024) + "</root>";
        byte[] expanded = Package([new("展开")], extra: Enumerable.Range(1, 5)
            .Select(i => ("xl/large" + i + ".xml", part)).ToArray());
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() => Compare(expanded, expanded));
    }

    [TestMethod]
    public async Task NumericStyleAttributesAndTint_CompareByCanonicalNumber()
    {
        string before = StyleSheet(
            "<font><name val=\"A\"/><color rgb=\"FF808080\" tint=\"0.50\"/></font>",
            "<xf fontId=\"0\"><alignment textRotation=\"07\" indent=\"03\" relativeIndent=\"01\" readingOrder=\"01\"/></xf>");
        string after = StyleSheet(
            "<font><name val=\"A\"/><color rgb=\"FF808080\" tint=\"0.5\"/></font>",
            "<xf fontId=\"0\"><alignment textRotation=\"7\" indent=\"3\" relativeIndent=\"1\" readingOrder=\"1\"/></xf>");
        string cells = "<c r=\"A1\" s=\"0\"><v>1</v></c>";
        var equivalent = await Compare(Package([new("样式", cells)], before),
            Package([new("样式", cells)], after));
        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, equivalent.Kind);
        Oracle(equivalent.Details!);
        // Canonical values still detect real numeric differences; the oracle pins the
        // normalized representation instead of the raw spelling.
        Oracle(await Details(Package([new("样式", cells)], before),
            Package([new("样式", cells)], after.Replace("textRotation=\"7\"", "textRotation=\"45\"", StringComparison.Ordinal))),
            "Format|样式|A1|style: font=font()[color(rgb=8:FF808080,tint=4:5e-1)[],name(val=1:A)[]]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=alignment(indent=1:3,readingOrder=1:1,relativeIndent=1:1,textRotation=1:7)[]|style: font=font()[color(rgb=8:FF808080,tint=4:5e-1)[],name(val=1:A)[]]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=alignment(indent=1:3,readingOrder=1:1,relativeIndent=1:1,textRotation=2:45)[]");
        byte[] invalid = Package([new("样式", cells)],
            StyleSheet("<font><name val=\"A\"/><color rgb=\"FF808080\" tint=\"dark\"/></font>", "<xf fontId=\"0\"/>"));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(invalid, invalid));
    }

    [TestMethod]
    public async Task ZonedDates_CompareByUtcInstantZonelessStayUnspecified()
    {
        string before = "<c r=\"A1\" t=\"d\"><v>2026-01-01T12:00:00+02:00</v></c>" +
            "<c r=\"B1\" t=\"d\"><v>2026-01-01T10:00:00.250Z</v></c>" +
            "<c r=\"C1\" t=\"d\"><v>2026-01-01T00:00:00</v></c>" +
            "<c r=\"D1\" t=\"d\"><v>2026-01-01T00:00:00.5Z</v></c>";
        string after = "<c r=\"A1\" t=\"d\"><v>2026-01-01T10:00:00Z</v></c>" +
            "<c r=\"B1\" t=\"d\"><v>2026-01-01T12:00:00.25+02:00</v></c>" +
            "<c r=\"C1\" t=\"d\"><v>2026-01-01T00:00:00Z</v></c>" +
            "<c r=\"D1\" t=\"d\"><v>2026-01-01T00:00:00.25Z</v></c>";
        // A1/B1 are the same instants in different zone spellings and stay equal; C1/D1 pin
        // that zone-less values keep their Unspecified semantics and sub-second precision.
        Oracle(await Details(Package([new("日期", before)]), Package([new("日期", after)])),
            "Replace|日期|C1|value: date: 2026-01-01T00:00:00|value: date: 2026-01-01T00:00:00Z",
            "Replace|日期|D1|value: date: 2026-01-01T00:00:00.5Z|value: date: 2026-01-01T00:00:00.25Z");
    }

    [TestMethod]
    public async Task MalformedStylesAndDefinitions_FailClosed()
    {
        foreach (string styles in new[]
        {
            "<wrongRoot xmlns=\"" + S + "\"><fonts><font/></fonts></wrongRoot>",
            StyleSheet("<font/>", "<xf/>", "<numFmt numFmtId=\"163\" formatCode=\"0\"/>"),
            StyleSheet("<font/>", "<xf/>", "<numFmt numFmtId=\"164\" formatCode=\"0\"/><numFmt numFmtId=\"164\" formatCode=\"0.0\"/>"),
            StyleSheet("<font/>", "<xf/>", "<other/>"),
            "<styleSheet xmlns=\"" + S + "\"><fills><fill/></fills><borders><border/></borders><cellXfs><xf/></cellXfs></styleSheet>",
            "<styleSheet xmlns=\"" + S + "\"><fonts><font/></fonts><fills><fill/></fills><borders><border/></borders><cellXfs></cellXfs></styleSheet>",
            "<styleSheet xmlns=\"" + S + "\"><fonts><font/></fonts><fills><fill/></fills><borders><border/></borders><cellXfs><bad/></cellXfs></styleSheet>",
            StyleSheet("<bad/>", "<xf/>"),
        })
        {
            byte[] package = Package([new("样式", "<c r=\"A1\"><v>1</v></c>")], styles);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package), styles);
        }
    }

    [TestMethod]
    public async Task CanonicalUnderlineSizeAndWrapTextOff_HaveStableSemantics()
    {
        string cell = "<c r=\"A1\" s=\"0\"><v>1</v></c>";
        // Underline default "single" vs explicit "double" is a real change; sz "11"/"011" is not.
        Oracle(await Details(
            Package([new("样式", cell)], StyleSheet("<font><u/><sz val=\"11\"/></font>", "<xf fontId=\"0\"/>")),
            Package([new("样式", cell)], StyleSheet("<font><u val=\"double\"/><sz val=\"011\"/></font>", "<xf fontId=\"0\"/>"))),
            "Format|样式|A1|style: font=font()[sz(val=2:11)[],u=single]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=|style: font=font()[sz(val=2:11)[],u=double]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=");
        // u val="none" contributes nothing, exactly like an omitted underline.
        var none = await Compare(
            Package([new("无", cell)], StyleSheet("<font/>", "<xf fontId=\"0\"/>")),
            Package([new("无", cell)], StyleSheet("<font><u val=\"none\"/></font>", "<xf fontId=\"0\"/>")));
        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, none.Kind);
        Oracle(none.Details!);
        // wrapText=false canonicalizes away like an absent flag.
        Oracle(await Details(
            Package([new("标志", cell)], StyleSheet("<font/>", "<xf fontId=\"0\"><alignment wrapText=\"1\"/></xf>")),
            Package([new("标志", cell)], StyleSheet("<font/>", "<xf fontId=\"0\"><alignment wrapText=\"false\"/></xf>"))),
            "Format|标志|A1|style: font=font()[]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=alignment(wrapText=4:true)[]|style: font=font()[]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=alignment()[]");
        foreach (string color in new[] { "12345", "GG808080" })
        {
            byte[] invalid = Package([new("颜色", cell)],
                StyleSheet("<font><color rgb=\"" + color + "\"/></font>", "<xf fontId=\"0\"/>"));
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(invalid, invalid), color);
        }
    }

    [TestMethod]
    public async Task MalformedCellValuesRangesAndExponents_FailClosed()
    {
        foreach (string cells in new[]
        {
            "<c r=\"A1\" t=\"inlineStr\"><v>1</v></c>",
            "<c r=\"A1\" t=\"inlineStr\"/>",
            "<c r=\"A1\"><is><t>x</t></is></c>",
            "<c r=\"A1\"><v><b/></v></c>",
            "<c r=\"A1\"><v>1</v><is><t>y</t></is></c>",
            "<c r=\"A1\"><v>1e2000000</v></c>",
            "<c r=\"A1\"><f t=\"dataTable\" r1=\"A1\" r2=\"XFE1\"/><v>1</v></c>",
        })
        {
            byte[] package = Package([new("值", cells)]);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package), cells);
        }
        foreach (string extra in new[]
        {
            "<mergeCells><mergeCell ref=\"B1:A1\"/></mergeCells>",
            "<mergeCells><mergeCell ref=\"A1:B1:C1\"/></mergeCells>",
        })
        {
            byte[] package = Package([new("合并", Extra: extra)]);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(package, package), extra);
        }
    }

    [TestMethod]
    public async Task SharedStringRunsAndPhonetics_DecodeOrFailClosed()
    {
        byte[] valid = Package([new("字", "<c r=\"A1\" t=\"s\"><v>0</v></c><c r=\"B1\" t=\"s\"><v>1</v></c>")],
            shared: "<si><r><rPr><b val=\"1\"/></rPr><t>富</t></r></si>" +
                "<si><t>振</t><rPh sb=\"0\" eb=\"1\"><t>しん</t></rPh></si>");
        var identical = await Compare(valid, Package([new("字",
            "<c r=\"A1\" t=\"inlineStr\"><is><t>富</t></is></c><c r=\"B1\" t=\"inlineStr\"><is><t>振</t></is></c>")]));
        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, identical.Kind);
        Oracle(identical.Details!);
        foreach (string shared in new[]
        {
            "<si><t>a</t><t>b</t></si>",
            "<si><t><b/>a</t></si>",
            "<si><r><t>a</t></r><t>b</t></si>",
            "<si><r/></si>",
            "<si><r><t>a</t><x/></r></si>",
            "<si><bad/></si>",
        })
        {
            byte[] invalid = Package([new("字")], shared: shared);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Compare(invalid, invalid), shared);
        }
    }

    [TestMethod]
    public async Task CanonicalZeroAndLeadingDotNumbers_CompareEqual()
    {
        var outcome = await Compare(
            Package([new("零", "<c r=\"A1\"><v>0</v></c><c r=\"B1\"><v>0.000</v></c><c r=\"C1\"><v>.5</v></c>")]),
            Package([new("零", "<c r=\"A1\"><v>0.0</v></c><c r=\"B1\"><v>0</v></c><c r=\"C1\"><v>0.5</v></c>")]));
        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, outcome.Kind);
        Oracle(outcome.Details!);
    }

    [TestMethod]
    public async Task DeletedFormulaCellCarriesFormulaAndCacheInCellText()
    {
        Oracle(await Details(
            Package([new("旧", "<c r=\"A1\"><f>1+1</f><v>2</v></c>")]),
            Package([new("新", "<c r=\"B1\"/>", 2, "xl/worksheets/new.xml")])),
            "Delete|旧||sheet: 旧|",
            "Delete|旧|A1|cell: number; formula: normal: 1+1; cache: number: 2; style: default|",
            "Insert|新|||sheet: 新",
            "Insert|新|B1||cell: number: <empty>; style: default");
    }

    [TestMethod]
    public async Task TruncatedSurrogatePair_NeverSplitsCodePoints()
    {
        // The 2048-char cut is applied to the full change text (prefix included); placing
        // a surrogate pair so index 2046 is its high surrogate keeps the code point whole.
        string text = new string('中', 2033) + string.Concat(Enumerable.Repeat("\U0001F600", 500));
        var details = await Details(
            Package([new("边界", "<c r=\"A1\" t=\"inlineStr\"><is><t>旧</t></is></c>")]),
            Package([new("边界", "<c r=\"A1\" t=\"inlineStr\"><is><t>" + text + "</t></is></c>")]));
        Assert.IsTrue(details.Coverage.Truncated);
        string snippet = details.Changes[0].After!.Runs[0].Text;
        Assert.AreEqual("value: text: ".Length + 2033 + 1, snippet.Length);
        Assert.AreEqual('…', snippet[^1]);
        Assert.AreEqual('中', snippet[^2]);
    }

    private static Task<DocumentDiffOutcome> Compare(byte[] before, byte[] after) =>
        XlsxSemanticDiff.CompareAsync(new(Source(before), Source(after)), CancellationToken.None);
    private static DocumentContentSource Source(byte[] bytes) => new("synthetic.xlsx", null, bytes.Length,
        _ => ValueTask.FromResult<Stream>(new MemoryStream(bytes, writable: false)));
    private static async Task<DocumentDiffDetails> Details(byte[] before, byte[] after)
    {
        var outcome = await Compare(before, after);
        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.IsNotNull(outcome.Details);
        Assert.AreEqual(DocumentDiffFormat.Xlsx, outcome.Details.Format);
        return outcome.Details;
    }
    internal static void Oracle(DocumentDiffDetails details, params string[] expected) =>
        CollectionAssert.AreEqual(expected, details.Changes.Select(c => c.Kind + "|" +
            c.Location.SheetName + "|" + c.Location.CellAddress + "|" + Snippet(c.Before) + "|" +
            Snippet(c.After)).ToArray());
    internal static string Snippet(DocumentDiffRichSnippet? snippet) =>
        snippet is null ? "" : string.Concat(snippet.Runs.Select(r => r.Text));
    internal static string StyleSheet(string fonts, string xfs, string formats = "") =>
        "<styleSheet xmlns=\"" + S + "\"><numFmts>" + formats + "</numFmts><fonts>" + fonts +
        "</fonts><fills><fill/></fills><borders><border/></borders><cellXfs>" + xfs + "</cellXfs></styleSheet>";

    internal static byte[] Package(Sheet[] sheets, string? styles = null, string? shared = null,
        (string Name, string Xml)[]? extra = null, bool date1904 = false)
    {
        static string Escape(string value) => new XAttribute("value", value).ToString()[7..^1];
        var parts = new List<(string Name, string Xml)>();
        string overrides = string.Concat(sheets.Select(s => s.Part).Distinct(StringComparer.Ordinal)
            .Select(p => "<Override PartName=\"/" + Escape(p) + "\" ContentType=\"application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml\"/>"));
        if (styles is not null) overrides += "<Override PartName=\"/xl/styles.xml\" ContentType=\"application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml\"/>";
        if (shared is not null) overrides += "<Override PartName=\"/xl/sharedStrings.xml\" ContentType=\"application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml\"/>";
        string workbook = "<workbook xmlns=\"" + S + "\" xmlns:r=\"" + R[..^1] + "\"><sheets>";
        string rels = "<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\">";
        for (int i = 0; i < sheets.Length; i++)
        {
            Sheet sheet = sheets[i];
            workbook += "<sheet name=\"" + Escape(sheet.Name) + "\" sheetId=\"" + sheet.Id +
                "\" state=\"" + sheet.State + "\" r:id=\"r" + i + "\"/>";
            rels += "<Relationship Id=\"r" + i + "\" Type=\"" + R + "worksheet\" Target=\"" +
                sheet.Part[3..] + "\"/>";
            if (parts.All(p => p.Name != sheet.Part)) parts.Add((sheet.Part, "<worksheet xmlns=\"" + S + "\"><sheetData>" +
                (sheet.Rows ?? "<row r=\"1\">" + sheet.Cells + "</row>") + "</sheetData>" +
                sheet.Extra + "</worksheet>"));
        }
        if (styles is not null)
        {
            parts.Add(("xl/styles.xml", styles));
            rels += "<Relationship Id=\"styles\" Type=\"" + R + "styles\" Target=\"styles.xml\"/>";
        }
        if (shared is not null)
        {
            parts.Add(("xl/sharedStrings.xml", "<sst xmlns=\"" + S + "\">" + shared + "</sst>"));
            rels += "<Relationship Id=\"strings\" Type=\"" + R + "sharedStrings\" Target=\"sharedStrings.xml\"/>";
        }
        parts.Add(("xl/workbook.xml", workbook + "</sheets><workbookPr date1904=\"" + (date1904 ? "1" : "0") + "\"/></workbook>"));
        parts.Add(("xl/_rels/workbook.xml.rels", rels + "</Relationships>"));
        parts.Add(("_rels/.rels", "<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\"><Relationship Id=\"office\" Type=\"" +
            R + "officeDocument\" Target=\"xl/workbook.xml\"/></Relationships>"));
        parts.Add(("[Content_Types].xml", "<Types xmlns=\"http://schemas.openxmlformats.org/package/2006/content-types\"><Default Extension=\"xml\" ContentType=\"application/xml\"/><Default Extension=\"rels\" ContentType=\"application/vnd.openxmlformats-package.relationships+xml\"/><Override PartName=\"/xl/workbook.xml\" ContentType=\"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml\"/>" + overrides + "</Types>"));
        if (extra is not null) parts.AddRange(extra);
        using var stream = new MemoryStream();
        using (var zip = new ZipArchive(stream, ZipArchiveMode.Create, leaveOpen: true))
            foreach (var part in parts)
            {
                using var writer = new StreamWriter(zip.CreateEntry(part.Name, CompressionLevel.Fastest).Open(),
                    new UTF8Encoding(false));
                writer.Write(part.Xml);
            }
        return stream.ToArray();
    }
}
