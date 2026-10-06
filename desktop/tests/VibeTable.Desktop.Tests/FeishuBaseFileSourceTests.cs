using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Synthetic-only regression for the offline .base file reader: every
/// envelope is gzip/base64 built in code from the shapes verified on a real
/// export; no sample data, no network, no attachment bytes.
/// </summary>
[TestClass]
public sealed class FeishuBaseFileSourceTests
{
    private const string BaseToken = "basetoken0000000001";
    private const string TableA = "tblSyntheticA00001";
    private const string TableB = "tblSyntheticB00002";
    private const string FldTitleA = "fldTitleA0000001";
    private const string FldNumA = "fldNumA0000002";
    private const string FldSelA = "fldSelA0000003";
    private const string FldDateA = "fldDateA0000004";
    private const string FldUrlA = "fldUrlA0000005";
    private const string FldUserA = "fldUserA0000006";
    private const string FldModA = "fldModA0000007";
    private const string FldFormulaA = "fldFormulaA0000008";
    private const string FldLookupA = "fldLookupA0000009";
    private const string FldAttA = "fldAttA0000010";
    private const string FldTitleB = "fldTitleB0000001";
    private const string FldSelB = "fldSelB0000002";
    private const string FldUrlB = "fldUrlB0000003";
    private const string FldTextB = "fldTextB0000004";
    private const string FldOddB = "fldOddB0000005";

    private static byte[] GzipBytes(string json)
    {
        MemoryStream output = new();
        using (GZipStream gzip = new(output, CompressionLevel.Fastest, leaveOpen: true))
            gzip.Write(Encoding.UTF8.GetBytes(json));
        return output.ToArray();
    }

    private static byte[] WriteEnvelope(string snapshotJson, string? dashboardJson = null,
        string? automationJson = null, bool withSign = true)
    {
        var envelope = new Dictionary<string, object?> { ["gzipSnapshot"] = Convert.ToBase64String(GzipBytes(snapshotJson)) };
        if (dashboardJson is not null) envelope["gzipDashboard"] = Convert.ToBase64String(GzipBytes(dashboardJson));
        if (automationJson is not null) envelope["gzipAutomation"] = Convert.ToBase64String(GzipBytes(automationJson));
        if (withSign) envelope["sign"] = "synthetic-sign-not-decoded";
        return JsonSerializer.SerializeToUtf8Bytes(envelope);
    }

    private static object Field(string name, int type, string uiType, object? property = null) =>
        new { name, type, fieldUIType = uiType, property, isPrimary = false };

    private static object Cell(object? value) =>
        new { value, modifiedTime = 1700000000L, modifiedUser = "ouSyntheticOwner00000001" };

    private static object Seg(string text) => new { text, type = "text" };

    private static object UrlSeg(string text, string link) => new { text, type = "url", link };

    private static string TableItem(Dictionary<string, object?> fields, Dictionary<string, object?> records,
        string primaryKey, string baseToken = BaseToken, string tableId = TableA, string tableName = "合成表A",
        int structVersion = 1, int schemaVersion = 5, bool declareTable = true,
        int metaSchemaVersion = 5, int? metaRecordsNum = null, bool mismatchRecordMeta = false)
    {
        var schema = new Dictionary<string, object?>
        {
            ["base"] = new Dictionary<string, object?>
            {
                ["token"] = baseToken, ["name"] = "合成经营看板", ["rev"] = 3L,
                ["schemaVersion"] = schemaVersion, ["timezone"] = "Asia/Shanghai",
            },
            ["owner"] = "ouSyntheticOwner00000001",
            ["tableMap"] = declareTable
                ? new Dictionary<string, object?> { [tableId] = new { id = tableId, name = tableName } }
                : new Dictionary<string, object?>(),
            ["structVersion"] = structVersion,
            ["data"] = new Dictionary<string, object?>
            {
                ["table"] = new Dictionary<string, object?>
                {
                    ["meta"] = new
                    {
                        id = tableId, name = "", rev = 1L,
                        schemaVersion = metaSchemaVersion, recordsNum = metaRecordsNum ?? records.Count,
                    },
                    ["primaryKey"] = primaryKey,
                    ["fieldMap"] = fields,
                    ["viewMap"] = new Dictionary<string, object?>
                        { ["viewSynthetic0001"] = new { id = "viewSynthetic0001", name = "默认视图" } },
                },
                ["recordMap"] = records,
                ["recordMeta"] = mismatchRecordMeta
                    ? new Dictionary<string, object?>()
                    : records.Keys.ToDictionary(key => key, key => (object?)new { recMeta = new { rev = 1 } }),
            },
        };
        return JsonSerializer.Serialize(new Dictionary<string, object?> { ["schema"] = schema });
    }

    private static string SnapshotOf(params string[] items) => "[" + string.Join(",", items) + "]";

    private static Dictionary<string, object?> FieldsA() => new()
    {
        [FldTitleA] = Field("标题", 1, "Text"),
        [FldNumA] = Field("数量", 2, "Number"),
        [FldSelA] = Field("状态", 3, "SingleSelect", new
        {
            options = new object[] { new { id = "optSynthetic00001", name = "进行中", color = 0 } },
            optionsType = 1,
        }),
        [FldDateA] = Field("时间", 5, "DateTime"),
        [FldUrlA] = Field("链接", 15, "Url"),
        [FldUserA] = Field("负责人", 11, "User", new { multiple = false }),
        [FldModA] = Field("修改时间", 1002, "ModifiedTime"),
        [FldFormulaA] = Field("计算", 20, "Formula", new { formula = "[求和]SUM" }),
        [FldLookupA] = Field("引用", 19, "Lookup", new
        {
            filterInfo = new { conditions = Array.Empty<object>() },
            rollup = "SUM", targetField = FldTitleA, formula = "[引用]聚合",
        }),
        [FldAttA] = Field("附件", 17, "Attachment", new { capture = false }),
    };

    private static Dictionary<string, object?> RecordsA() => new()
    {
        ["recSynthetic000001"] = new Dictionary<string, object?>
        {
            [FldTitleA] = Cell(new object[] { Seg("A"), Seg("B") }),
            [FldNumA] = Cell(1234.50m),
            [FldSelA] = Cell("optSynthetic00001"),
            [FldDateA] = Cell(1767225600123L),
            [FldUrlA] = Cell(new object[] { UrlSeg("https://example.com/synthetic", "https://example.com/synthetic") }),
            [FldUserA] = Cell(new Dictionary<string, object?>
            {
                ["users"] = new object[]
                {
                    new Dictionary<string, object?>
                    {
                        ["userId"] = "ouSyntheticUser0000001", ["name"] = "张三", ["enName"] = "Zhang San",
                        ["avatarUrl"] = "https://internal.example/avatar", ["notify"] = false,
                    },
                },
            }),
            [FldModA] = Cell(1700000000),
            [FldAttA] = Cell(null),
        },
        ["recSynthetic000002"] = new Dictionary<string, object?>
        {
            [FldTitleA] = Cell(Array.Empty<object>()),
        },
    };

    private static string HappySnapshot() => SnapshotOf(
        TableItem(FieldsA(), RecordsA(), FldTitleA),
        TableItem(new Dictionary<string, object?>
        {
            [FldTitleB] = Field("标题B", 1, "Text"),
            [FldSelB] = Field("状态B", 3, "SingleSelect", new
            {
                options = new object[] { new { id = "optSyntheticB00001", name = "进行中" } },
            }),
            [FldUrlB] = Field("链接B", 15, "Url"),
            [FldTextB] = Field("富文本B", 1, "Text"),
            [FldOddB] = Field("组件B", 999, "Widget"),
        }, new Dictionary<string, object?>
        {
            ["recSyntheticB000001"] = new Dictionary<string, object?>
            {
                [FldTitleB] = Cell(new object[] { Seg("B0") }),
                [FldSelB] = Cell("optNotDeclared00001"),
                [FldUrlB] = Cell(new object[] { UrlSeg("甲", "https://example.com/one") }),
                [FldTextB] = Cell(new object[] { Seg("前"), UrlSeg("链", "https://example.com/mixed") }),
                [FldOddB] = Cell(new Dictionary<string, object?> { ["any"] = "shape" }),
            },
        }, FldTitleB, tableId: TableB, tableName: "合成表B"));

    /// <summary>Synthetic .base files live only under the repository's
    /// build/automation/base-file evidence directory; each run writes one
    /// guid-named file and deletes exactly that file afterwards.</summary>
    private static string SyntheticBaseFilePath()
    {
        for (string? directory = AppContext.BaseDirectory; directory is not null;
            directory = Path.GetDirectoryName(directory))
        {
            if (File.Exists(Path.Combine(directory, "pyproject.toml"))
                && Directory.Exists(Path.Combine(directory, "desktop")))
            {
                string build = Path.Combine(directory, "build", "automation", "base-file");
                Directory.CreateDirectory(build);
                return Path.Combine(build, "synthetic-" + Guid.NewGuid().ToString("N") + ".base");
            }
        }
        throw new AssertFailedException("Repository build directory was not found.");
    }

    private static async Task<FeishuBaseFileDocument> ReadEnvelopeAsync(byte[] envelope)
    {
        string path = SyntheticBaseFilePath();
        await File.WriteAllBytesAsync(path, envelope);
        try
        {
            return await FeishuBaseFileSource.ReadFileAsync(path, CancellationToken.None);
        }
        finally
        {
            File.Delete(path);
        }
    }

    [TestMethod]
    public async Task ReadFileAsync_MapsObservedShapesToFrozenSnapshot()
    {
        FeishuBaseFileDocument document = await ReadEnvelopeAsync(WriteEnvelope(HappySnapshot(),
            dashboardJson: "[{\"dashboardID\":\"dashSynthetic00001\"}]",
            automationJson: "[{\"id\":\"wfSynthetic01\"},{\"id\":\"wfSynthetic02\"}]"));

        HostSourceImportSnapshot snapshot = document.Snapshot;
        Assert.AreEqual("feishu", snapshot.Provider);
        Assert.AreEqual(BaseToken, snapshot.ContainerId);
        Assert.IsTrue(snapshot.DisplayName.EndsWith(FeishuBaseFileSource.LocalFileSuffix, StringComparison.Ordinal));
        Assert.AreEqual("base-rev-3", snapshot.Version);
        Assert.AreEqual("snapshot", snapshot.ReadWindow.Consistency);
        Assert.AreEqual(0, snapshot.Attachments.Length);
        Assert.AreEqual(2, snapshot.Tables.Length);
        HostSourceImportTable tableA = snapshot.Tables[0];
        Assert.AreEqual(TableA, tableA.Id);
        Assert.AreEqual("合成表A", tableA.Name);
        Assert.AreEqual("table-rev-1", tableA.Version);
        Assert.AreEqual(FldTitleA, tableA.PrimaryFieldId);
        Assert.AreEqual(10, tableA.Fields.Length);

        HostSourceImportField text = tableA.Fields.First(field => field.Id == FldTitleA);
        Assert.AreEqual("text", text.Kind);
        Assert.AreEqual("text", text.ValueKind);
        Assert.AreEqual("Asia/Shanghai", text.Timezone);
        HostSourceImportField select = tableA.Fields.First(field => field.Id == FldSelA);
        Assert.AreEqual("select", select.Kind);
        Assert.AreEqual(1, select.Options.Length);
        Assert.AreEqual("optSynthetic00001", select.Options[0].Id);
        Assert.AreEqual("进行中", select.Options[0].Label);
        HostSourceImportField formula = tableA.Fields.First(field => field.Id == FldFormulaA);
        Assert.AreEqual("formula", formula.Kind);
        StringAssert.Contains(formula.Definition, "formulaExpression");
        using JsonDocument formulaDefinition = JsonDocument.Parse(formula.Definition);
        Assert.AreEqual("[求和]SUM", formulaDefinition.RootElement.GetProperty("formulaExpression").GetString());
        HostSourceImportField lookup = tableA.Fields.First(field => field.Id == FldLookupA);
        Assert.AreEqual("lookup", lookup.Kind);
        StringAssert.Contains(lookup.Definition, "lookupProperty");
        StringAssert.Contains(lookup.Definition, "rollup");
        StringAssert.Contains(lookup.Definition, "targetField");

        HostSourceImportRecord first = tableA.Records[0];
        Assert.AreEqual("recSynthetic000001", first.Id);
        Assert.AreEqual("AB", first.Values[FldTitleA].GetString());
        Assert.AreEqual("1234.50", first.Values[FldNumA].GetRawText());
        Assert.AreEqual("optSynthetic00001", first.Values[FldSelA].GetString());
        string expectedDate = DateTimeOffset.FromUnixTimeMilliseconds(1767225600123L).UtcDateTime
            .ToString("O", CultureInfo.InvariantCulture);
        Assert.AreEqual(expectedDate, first.Values[FldDateA].GetString());
        Assert.AreEqual("https://example.com/synthetic", first.Values[FldUrlA].GetString());
        Assert.AreEqual(1700000000, first.Values[FldModA].GetInt32());
        Assert.IsFalse(first.Values.ContainsKey(FldAttA), "空附件单元格必须跳过");
        JsonElement person = first.Values[FldUserA];
        Assert.AreEqual(JsonValueKind.Array, person.ValueKind);
        Assert.AreEqual("ouSyntheticUser0000001", person[0].GetProperty("userId").GetString());
        Assert.AreEqual("张三", person[0].GetProperty("name").GetString());
        Assert.IsFalse(person[0].TryGetProperty("avatarUrl", out _), "头像链接不得进入快照");
        HostSourceImportRecord second = tableA.Records[1];
        Assert.IsFalse(second.Values.ContainsKey(FldTitleA), "空文本段必须跳过");

        Assert.IsFalse(document.Notices.Any(notice => notice.Contains("sign")));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("1 个仪表盘")
            && notice.Contains("2 个自动化流程") && notice.Contains("2 个视图配置")));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("头像")));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("合成表A")
            && notice.Contains("计算") && notice.Contains("公式") && notice.Contains("将为空")));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("合成表A")
            && notice.Contains("引用") && notice.Contains("将为空")));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("附件")));
    }
    [TestMethod]
    public async Task ReadFileAsync_OptionalMembersAbsent_StillSucceeds()
    {
        FeishuBaseFileDocument document = await ReadEnvelopeAsync(WriteEnvelope(HappySnapshot()));
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("0 个仪表盘")
            && notice.Contains("2 个视图配置")), "视图计数仍应如实报告");
        Assert.IsFalse(document.Notices.Any(notice => notice.Contains("sign")));
        Assert.IsFalse(document.Notices.Any(notice => notice.Contains("冻结")));

        HostSourceImportTable tableB = document.Snapshot.Tables[1];
        HostSourceImportField url = tableB.Fields.First(field => field.Id == FldUrlB);
        Assert.AreEqual("unknown", url.Kind, "显示名称与链接不一致的单段链接必须整列降级");
        JsonElement raw = tableB.Records[0].Values[FldUrlB];
        Assert.AreEqual(JsonValueKind.Array, raw.ValueKind);
        Assert.AreEqual("https://example.com/one", raw[0].GetProperty("link").GetString());
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("显示名称与链接地址不一致")));
    }

    [TestMethod]
    public async Task ReadFileAsync_FormulaCachedValue_MigratesSnapshotAndNoticesAccurately()
    {
        var records = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?>
            {
                [FldTitleA] = Cell(new object[] { Seg("A") }),
                [FldFormulaA] = Cell(42),
            },
        };
        FeishuBaseFileDocument document = await ReadEnvelopeAsync(WriteEnvelope(
            SnapshotOf(TableItem(FieldsA(), records, FldTitleA))));
        HostSourceImportTable table = document.Snapshot.Tables[0];
        Assert.AreEqual(42, table.Records[0].Values[FldFormulaA].GetInt32());
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("缓存值")));
        Assert.IsFalse(document.Notices.Any(notice => notice.Contains("（公式）") && notice.Contains("将为空")), "有缓存值时不得误报为空");
        Assert.IsTrue(document.Notices.Any(notice => notice.Contains("引用") && notice.Contains("将为空")),
            "无缓存值的 lookup 列仍应如实告知为空");
    }

    [TestMethod]
    public async Task CreateProvider_SelectsOnlyRequestedTables()
    {
        FeishuBaseFileDocument document = await ReadEnvelopeAsync(WriteEnvelope(HappySnapshot()));
        HostSourceImportSnapshot single = await document.CreateProvider([TableB]).ReadAsync(CancellationToken.None);
        Assert.AreEqual(1, single.Tables.Length);
        Assert.AreEqual(TableB, single.Tables[0].Id);
        HostSourceImportSnapshot both = await document.CreateProvider([TableA, TableB])
            .ReadAsync(CancellationToken.None);
        Assert.AreEqual(2, both.Tables.Length);
        FeishuSourceImportException notFound = Assert.ThrowsExactly<FeishuSourceImportException>(
            () => document.CreateProvider(["tblNoSuchTable00000"]));
        Assert.AreEqual(FeishuSourceImportErrorKind.NotFound, notFound.Kind);
    }

    [TestMethod]
    public async Task Provider_IsFrozenAndFailsClosedForAttachments()
    {
        FeishuBaseFileDocument document = await ReadEnvelopeAsync(WriteEnvelope(HappySnapshot()));
        IHostSourceImportProvider provider = document.CreateProvider([TableB]);
        HostSourceImportSnapshot first = await provider.ReadAsync(CancellationToken.None);
        Assert.AreSame(first, await provider.ReadAsync(CancellationToken.None), "冻结快照不得重读文件");
        HostSourceImportObservation observation = await provider.ObserveAsync(CancellationToken.None);
        Assert.AreEqual("base-rev-3", observation.Version);
        Assert.AreEqual("table-rev-1", observation.TableVersions[TableB]);
        FeishuSourceImportException attachment = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.OpenAttachmentAsync(new HostSourceImportAttachment(
                "fileSyntheticToken01", TableB, "recSyntheticB000001", FldOddB, "a.png", "image/png", 4),
                CancellationToken.None));
        Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, attachment.Kind);
        provider.Dispose();
        FeishuSourceImportException disposed = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(FeishuSourceImportErrorKind.Disposed, disposed.Kind);
    }

    [TestMethod]
    public async Task ReadFileAsync_StructuralFailures_FailClosed()
    {
        string snapshotB64 = Convert.ToBase64String(GzipBytes(
            SnapshotOf(TableItem(FieldsA(), RecordsA(), FldTitleA))));
        var ghostCell = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { ["fldGhost00000000001"] = Cell("x") },
        };
        var nullGhostCell = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { ["fldGhostNull0000001"] = null },
        };
        var cellNotObject = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { [FldNumA] = "raw-string" },
        };
        var cellMissingValue = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { [FldNumA] = new { modifiedTime = 1L } },
        };
        var numberShape = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { [FldNumA] = Cell("not-a-number") },
        };
        var dateOverflow = new Dictionary<string, object?>
        {
            ["recSynthetic000001"] = new Dictionary<string, object?> { [FldDateA] = Cell(long.MaxValue) },
        };
        var duplicateOptions = new Dictionary<string, object?>(FieldsA())
        {
            [FldSelA] = Field("状态", 3, "SingleSelect", new
            {
                options = new object[]
                {
                    new { id = "optDup00000000001", name = "甲" },
                    new { id = "optDup00000000001", name = "乙" },
                },
            }),
        };
        var optionMissingName = new Dictionary<string, object?>(FieldsA())
        {
            [FldSelA] = Field("状态", 3, "SingleSelect", new { options = new object[] { new { id = "optSynthetic00001" } } }),
        };
        (string Name, byte[] Envelope)[] cases =
        [
            ("missing snapshot member", JsonSerializer.SerializeToUtf8Bytes(
                new Dictionary<string, object?> { ["gzipExtraInfo"] = "" })),
            ("invalid base64", JsonSerializer.SerializeToUtf8Bytes(
                new Dictionary<string, object?> { ["gzipSnapshot"] = "!!!not-base64!!!" })),
            ("not gzip", JsonSerializer.SerializeToUtf8Bytes(new Dictionary<string, object?>
                { ["gzipSnapshot"] = Convert.ToBase64String(Encoding.UTF8.GetBytes("{}")) })),
            ("dashboard corrupt base64", JsonSerializer.SerializeToUtf8Bytes(new Dictionary<string, object?>
                { ["gzipSnapshot"] = snapshotB64, ["gzipDashboard"] = "!!!" })),
            ("dashboard not array", JsonSerializer.SerializeToUtf8Bytes(new Dictionary<string, object?>
                { ["gzipSnapshot"] = snapshotB64,
                  ["gzipDashboard"] = Convert.ToBase64String(GzipBytes("{}")) })),
            ("snapshot not array", WriteEnvelope("{}")),
            ("duplicate json key", WriteEnvelope("[{\"x\":1,\"x\":2}]")),
            ("unsupported structVersion", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, structVersion: 2)))),
            ("unsupported schemaVersion", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, schemaVersion: 6)))),
            ("unsupported meta schemaVersion", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, metaSchemaVersion: 4)))),
            ("recordsNum mismatch", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, metaRecordsNum: 5)))),
            ("recordMeta mismatch", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, mismatchRecordMeta: true)))),
            ("primary missing", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), "fldMissingPrimary0")))),
            ("table undeclared", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA, declareTable: false)))),
            ("undeclared cell field", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), ghostCell, FldTitleA)))),
            ("undeclared null cell field", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), nullGhostCell, FldTitleA)))),
            ("cell not object", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), cellNotObject, FldTitleA)))),
            ("cell missing value", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), cellMissingValue, FldTitleA)))),
            ("number shape", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), numberShape, FldTitleA)))),
            ("date overflow", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), dateOverflow, FldTitleA)))),
            ("duplicate option id", WriteEnvelope(SnapshotOf(
                TableItem(duplicateOptions, RecordsA(), FldTitleA)))),
            ("option missing name", WriteEnvelope(SnapshotOf(
                TableItem(optionMissingName, RecordsA(), FldTitleA)))),
            ("mixed base tokens", WriteEnvelope(SnapshotOf(
                TableItem(FieldsA(), RecordsA(), FldTitleA),
                TableItem(FieldsA(), RecordsA(), FldTitleA, baseToken: "basetoken0000000002")))),
            ("depth overflow", WriteEnvelope("[" + new string('[', 200) + new string(']', 200) + "]")),
        ];
        foreach ((string name, byte[] envelope) in cases)
        {
            InvalidDataException failure = await Assert.ThrowsExactlyAsync<InvalidDataException>(
                () => ReadEnvelopeAsync(envelope), name);
            Assert.IsFalse(string.IsNullOrEmpty(failure.Message), name);
        }
    }

    [TestMethod]
    public async Task ReadFileAsync_FieldCap_FailsClosed()
    {
        var fields = new Dictionary<string, object?> { ["fldCapPrimary00001"] = Field("主", 1, "Text") };
        for (int i = 1; i <= FeishuBaseFileSource.MaxFieldsPerTable; i++)
            fields["fldCapFiller" + i.ToString("0000", CultureInfo.InvariantCulture)] = Field("F", 1, "Text");
        InvalidDataException failure = await Assert.ThrowsExactlyAsync<InvalidDataException>(
            () => ReadEnvelopeAsync(WriteEnvelope(SnapshotOf(
                TableItem(fields, new Dictionary<string, object?>(), "fldCapPrimary00001")))));
        Assert.IsFalse(string.IsNullOrEmpty(failure.Message));
    }

    [TestMethod]
    public async Task ReadFileAsync_FileAndDecompressionLimitsRejectOversizedInput()
    {
        string path = SyntheticBaseFilePath();
        try
        {
            using (FileStream file = File.Create(path))
                file.SetLength(FeishuBaseFileSource.MaxFileBytes + 1L);
            await Assert.ThrowsExactlyAsync<InvalidDataException>(
                () => FeishuBaseFileSource.ReadFileAsync(path, CancellationToken.None));
        }
        finally { File.Delete(path); }

        byte[] compressedBomb = WriteEnvelope(new string(' ', FeishuBaseFileSource.MaxDecompressedBytes + 1));
        InvalidDataException failure = await Assert.ThrowsExactlyAsync<InvalidDataException>(
            () => ReadEnvelopeAsync(compressedBomb));
        StringAssert.Contains(failure.Message, "64 MiB");
    }

    [TestMethod]
    public async Task ReadFileAsync_MissingFile_FailsClosed()
    {
        string path = SyntheticBaseFilePath();
        await Assert.ThrowsExactlyAsync<InvalidDataException>(
            () => FeishuBaseFileSource.ReadFileAsync(path, CancellationToken.None));
    }
}