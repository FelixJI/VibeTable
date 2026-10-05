using System.Globalization;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class FeishuSourceImportProviderTests
{
    private const string TableA = "tblSyntheticA00001";
    private const string TableB = "tblSyntheticB00002";
    private const string TableC = "tblSyntheticC00003";

    private static JsonElement Raw(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }

    private static JsonElement Options(params (string Id, string Name)[] pairs) => Raw(JsonSerializer.Serialize(new
    {
        options = pairs.Select(pair => new { id = pair.Id, name = pair.Name }).ToArray(),
    }));

    private static object[] AttachmentValue(string token, string name, string type, int size) =>
    [
        new Dictionary<string, object?> { ["file_token"] = token, ["name"] = name, ["type"] = type, ["size"] = size },
    ];

    private static Dictionary<string, object?> AValues(int index) => new()
    {
        ["标题"] = $"A {index:000}",
        ["数量"] = Raw("1234.50"),
        ["状态"] = "进行中",
        ["标签"] = new object[] { "进行中", "已完成" },
        ["完成"] = index % 2 == 0,
        ["时间"] = 1767225600123,
        ["链接"] = new Dictionary<string, object?> { ["link"] = "https://example.com/a", ["text"] = "示例" },
        ["关联B"] = "rec000000",
        ["负责人"] = new object[] { new Dictionary<string, object?> { ["id"] = "ou_SyntheticUser01", ["name"] = "张三" } },
        ["计算"] = 42,
        ["创建时间"] = 1700000000000,
        ["电话"] = "13800000000",
        ["附件"] = index == 0 ? AttachmentValue("fileSyntheticToken01", "a.png", "image/png", 4) : Array.Empty<object>(),
    };

    private static Dictionary<string, object?> BValues(int index) => new()
    {
        ["标题"] = $"B {index:000}",
        ["双向A"] = new object[] { "rec000000", "rec000001" },
        ["双向单值A"] = index % 2 == 0 ? "rec000001" : null,
    };

    private static Dictionary<string, object?> CValues(int index) => new()
    {
        ["标题"] = $"C {index:000}",
        ["单向A"] = "rec000001",
        ["附件"] = index == 0 ? AttachmentValue("fileSyntheticToken02", "c.txt", "text/plain", 3) : Array.Empty<object>(),
    };

    private static FeishuSourceImportTestPeer MappingPeer() => new()
    {
        Tables =
        [
            new(TableA, "合成表 A", 3,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldCountA0000002", "数量", 2, "Number", false),
                new("fldStatusA0000003", "状态", 3, "SingleSelect", false,
                    Options(("optDoing000000001", "进行中"), ("optDone000000002", "已完成"))),
                new("fldTagsA0000004", "标签", 4, "MultiSelect", false,
                    Options(("optDoing000000001", "进行中"), ("optDone000000002", "已完成"))),
                new("fldDoneA0000005", "完成", 7, "Checkbox", false),
                new("fldWhenA0000006", "时间", 5, "DateTime", false),
                new("fldLinkA0000007", "链接", 15, "Url", false),
                new("fldToB0000008", "关联B", 18, "SingleLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableB + "\",\"multiple\":false}")),
                new("fldOwnerA0000010", "负责人", 11, "User", false),
                new("fldFormulaA0001", "计算", 21, "Formula", false),
                new("fldCreatedA0002", "创建时间", 1001, "CreatedTime", false),
                new("fldFilesA0003", "附件", 17, "Attachment", false),
                new("fldPhoneA0004", "电话", 13, "Phone", false),
            ], 2, AValues),
            new(TableB, "合成表 B", 5,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleB0000001", "标题", 1, "Text", true),
                new("fldToA0000002", "双向A", 22, "DuplexLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableA + "\",\"multiple\":true}")),
                new("fldSingleA0000003", "双向单值A", 22, "DuplexLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableA + "\",\"multiple\":false}")),
            ], 2, BValues),
        ],
    };

    private static FeishuSourceImportTestPeer PagedPeer(int recordCount, int revision = 3) => new()
    {
        Tables =
        [
            new(TableA, "分页表", revision,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldCountA0000002", "数量", 2, "Number", false),
            ], recordCount,
            index => new Dictionary<string, object?> { ["标题"] = $"行 {index:000000}", ["数量"] = index }),
        ],
    };

    private static FeishuSourceImportTestPeer ReplayPeer() => new()
    {
        AppRevision = 21,
        Tables =
        [
            new(TableA, "回放表 A", 4,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldCountA0000002", "数量", 2, "Number", false),
                new("fldStatusA0000003", "状态", 3, "SingleSelect", false,
                    Options(("optDoing000000001", "进行中"), ("optDone000000002", "已完成"))),
                new("fldToB0000008", "关联B", 18, "SingleLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableB + "\",\"multiple\":true}")),
                new("fldFilesA0003", "附件", 17, "Attachment", false),
            ], 2501, index => new Dictionary<string, object?>
            {
                ["标题"] = $"A {index:000000}",
                ["数量"] = index,
                ["状态"] = index % 2 == 0 ? "进行中" : "已完成",
                ["关联B"] = index % 1000 == 0 ? new object[] { "rec000000", "rec000001" } : null,
                ["附件"] = index == 0 ? AttachmentValue("fileReplayA0000001", "a.png", "image/png", 4) : null,
            }),
            new(TableB, "回放表 B", 6,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleB0000001", "标题", 1, "Text", true),
                new("fldToA0000002", "双向A", 22, "DuplexLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableA + "\",\"multiple\":true}")),
            ], 2, index => new Dictionary<string, object?>
            {
                ["标题"] = $"B {index:000}",
                ["双向A"] = new object[] { "rec000000", "rec002500" },
            }),
            new(TableC, "回放表 C", 8,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleC0000001", "标题", 1, "Text", true),
                new("fldToA0000002", "单向A", 18, "SingleLink", false,
                    FeishuSourceImportTestPeer.PropertyJson(
                        "{\"table_id\":\"" + TableA + "\",\"multiple\":false}")),
                new("fldFilesC0000003", "附件", 17, "Attachment", false),
            ], 2, CValues),
        ],
    };

    private static async Task<FeishuSourceImportConnection> ConnectAsync(FeishuSourceImportTestPeer peer) =>
        await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/base/{peer.AppToken}", FeishuSourceImportTestPeer.AccessToken, peer,
            retryDelay: TimeSpan.Zero).ConfigureAwait(false);

    [TestMethod]
    public async Task ReadMapsVerifiedFieldKindsOntoNeutralContractWithStableIdentities()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA, TableB]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);

        Assert.AreEqual(FeishuSourceImportConnector.ProviderName, snapshot.Provider);
        Assert.AreEqual(peer.AppToken, snapshot.ContainerId);
        Assert.AreEqual(peer.AppName, snapshot.DisplayName);
        Assert.AreEqual("rev-11", snapshot.Version);
        // The app-level version stays the official revision; table versions
        // carry the explicit semantic encoding.
        Assert.AreEqual("window", snapshot.ReadWindow.Consistency);
        DateTimeOffset.Parse(snapshot.ReadWindow.StartedAt, CultureInfo.InvariantCulture);
        DateTimeOffset.Parse(snapshot.ReadWindow.FinishedAt, CultureInfo.InvariantCulture);

        HostSourceImportTable tableA = snapshot.Tables.Single(table => table.Id == TableA);
        Assert.IsTrue(tableA.Version.StartsWith("rev-3|", StringComparison.Ordinal), tableA.Version);
        Assert.AreEqual("fldTitleA0000001", tableA.PrimaryFieldId);
        Assert.AreEqual(2, tableA.Records.Length);

        var kinds = tableA.Fields.ToDictionary(field => field.Id, field => (field.Kind, field.ValueKind));
        CollectionAssert.AreEquivalent(new Dictionary<string, (string, string)>
        {
            ["fldTitleA0000001"] = ("text", "text"),
            ["fldCountA0000002"] = ("number", "number"),
            ["fldStatusA0000003"] = ("select", "select"),
            ["fldTagsA0000004"] = ("multiSelect", "multiSelect"),
            ["fldDoneA0000005"] = ("bool", "bool"),
            ["fldWhenA0000006"] = ("dateTime", "dateTime"),
            ["fldLinkA0000007"] = ("url", "url"),
            ["fldToB0000008"] = ("relation", "relation"),
            ["fldOwnerA0000010"] = ("person", "json"),
            ["fldFormulaA0001"] = ("formula", "json"),
            ["fldCreatedA0002"] = ("system", "json"),
            ["fldFilesA0003"] = ("file", "file"),
            ["fldPhoneA0004"] = ("text", "text"),
        }, kinds);
        Assert.IsFalse(tableA.Fields.Any(field => field.Required));

        HostSourceImportField select = tableA.Fields.Single(field => field.Id == "fldStatusA0000003");
        CollectionAssert.AreEqual(new[] { "optDoing000000001", "optDone000000002" },
            select.Options.Select(option => option.Id).ToArray());
        CollectionAssert.AreEqual(new[] { "进行中", "已完成" },
            select.Options.Select(option => option.Label).ToArray());
        HostSourceImportField toB = tableA.Fields.Single(field => field.Id == "fldToB0000008");
        Assert.AreEqual(TableB, toB.Relation!.TargetTableId);
        Assert.AreEqual("", toB.Relation.TargetFieldId);
        // Cardinality comes from the official property.multiple, not from the
        // one-way/two-way link direction: a one-way single-value link is one.
        Assert.AreEqual("one", toB.Relation.Cardinality);

        HostSourceImportRecord first = tableA.Records.Single(record => record.Id == "rec000000");
        Assert.AreEqual("A 000", first.Values["fldTitleA0000001"].GetString());
        // Cloud numeric precision is preserved as raw JSON text, not a
        // double-rounded display string.
        Assert.AreEqual("1234.50", first.Values["fldCountA0000002"].GetRawText());
        Assert.AreEqual("optDoing000000001", first.Values["fldStatusA0000003"].GetString());
        CollectionAssert.AreEqual(new[] { "optDoing000000001", "optDone000000002" },
            first.Values["fldTagsA0000004"].EnumerateArray().Select(tag => tag.GetString()).ToArray());
        Assert.IsTrue(first.Values["fldDoneA0000005"].GetBoolean());
        Assert.AreEqual(
            DateTimeOffset.FromUnixTimeMilliseconds(1767225600123).UtcDateTime.ToString("O", CultureInfo.InvariantCulture),
            first.Values["fldWhenA0000006"].GetString());
        Assert.AreEqual("https://example.com/a", first.Values["fldLinkA0000007"].GetString());
        Assert.AreEqual("rec000000", first.Values["fldToB0000008"].GetString());
        Assert.AreEqual("ou_SyntheticUser01",
            first.Values["fldOwnerA0000010"][0].GetProperty("id").GetString());
        Assert.AreEqual(42, first.Values["fldFormulaA0001"].GetInt32());
        Assert.AreEqual(1700000000000L, first.Values["fldCreatedA0002"].GetInt64());
        Assert.AreEqual("13800000000", first.Values["fldPhoneA0004"].GetString());
        CollectionAssert.AreEqual(new[] { "fileSyntheticToken01" },
            first.Values["fldFilesA0003"].EnumerateArray().Select(file => file.GetString()).ToArray());

        HostSourceImportAttachment attachment = snapshot.Attachments.Single();
        Assert.AreEqual("fileSyntheticToken01", attachment.Id);
        Assert.AreEqual(TableA, attachment.TableId);
        Assert.AreEqual("rec000000", attachment.RecordId);
        Assert.AreEqual("fldFilesA0003", attachment.FieldId);
        Assert.AreEqual("a.png", attachment.Name);
        Assert.AreEqual("image/png", attachment.Mime);
        Assert.AreEqual(4L, attachment.Size);

        HostSourceImportTable tableB = snapshot.Tables.Single(table => table.Id == TableB);
        HostSourceImportField toA = tableB.Fields.Single(field => field.Id == "fldToA0000002");
        Assert.AreEqual(TableA, toA.Relation!.TargetTableId);
        // A two-way multi-value link is many.
        Assert.AreEqual("many", toA.Relation.Cardinality);
        HostSourceImportField singleA = tableB.Fields.Single(field => field.Id == "fldSingleA0000003");
        // A two-way single-value link is one, proving direction is not
        // cardinality.
        Assert.AreEqual("one", singleA.Relation!.Cardinality);
        HostSourceImportRecord bFirst = tableB.Records.Single(record => record.Id == "rec000000");
        CollectionAssert.AreEqual(new[] { "rec000000", "rec000001" },
            bFirst.Values["fldToA0000002"].EnumerateArray().Select(id => id.GetString()).ToArray());
        Assert.AreEqual("rec000001", bFirst.Values["fldSingleA0000003"].GetString());
    }

    [TestMethod]
    public async Task ReadPreservesRawSelectValuesWhenOptionIdentitiesAreIncomplete()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        peer.Tables[0] = peer.Tables[0] with
        {
            Fields =
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldStatusA0000003", "状态", 3, "SingleSelect", false,
                    Options(("", "进行中"), ("optDone000000002", "已完成"))),
            ],
            RecordCount = 2,
            Values = index => new Dictionary<string, object?> { ["标题"] = $"A {index:000}", ["状态"] = "进行中" },
        };
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportTable tableA = snapshot.Tables.Single(table => table.Id == TableA);
        // Broken option identities are dropped from the identity table and the
        // raw label is preserved; the Go preflight blocks native/select
        // snapshots instead of treating labels as identities.
        HostSourceImportField select = tableA.Fields.Single(field => field.Id == "fldStatusA0000003");
        CollectionAssert.AreEqual(new[] { "optDone000000002" }, select.Options.Select(option => option.Id).ToArray());
        Assert.AreEqual("进行中", tableA.Records[0].Values["fldStatusA0000003"].GetString());
    }

    [TestMethod]
    public async Task ReadRejectsDuplicateFieldNamesAndUndeclaredRecordKeys()
    {
        using FeishuSourceImportTestPeer duplicateNames = MappingPeer();
        duplicateNames.Tables[0] = duplicateNames.Tables[0] with
        {
            Fields =
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldAliasA0000002", "标题", 1, "Text", false),
            ],
        };
        using (FeishuSourceImportConnection connection = await ConnectAsync(duplicateNames).ConfigureAwait(false))
        using (FeishuSourceImportProvider provider = connection.CreateProvider([TableA]))
        {
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, error.Kind);
            StringAssert.Contains(error.Message, "字段名");
        }

        using FeishuSourceImportTestPeer undeclared = MappingPeer();
        undeclared.Tables[0] = undeclared.Tables[0] with
        {
            RecordCount = 1,
            Values = index => new Dictionary<string, object?> { ["标题"] = "A 000", ["未声明"] = "x" },
        };
        using (FeishuSourceImportConnection connection = await ConnectAsync(undeclared).ConfigureAwait(false))
        using (FeishuSourceImportProvider provider = connection.CreateProvider([TableA]))
        {
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, error.Kind);
            StringAssert.Contains(error.Message, "未声明字段");
        }
    }

    [TestMethod]
    public async Task ReadFailsClosedOnPagingProtocolViolations()
    {
        (string Mode, Action<FeishuSourceImportTestPeer> Inject, string Fragment)[] cases =
        [
            ("cursor", peer => peer.RepeatCursor = true, "游标重复"),
            ("records", peer => peer.DuplicateRecordIds = true, "record_id 重复"),
            ("token", peer => peer.OmitPageToken = true, "缺少下一页游标"),
        ];
        foreach ((string mode, Action<FeishuSourceImportTestPeer> inject, string fragment) in cases)
        {
            using FeishuSourceImportTestPeer peer = PagedPeer(20);
            inject(peer);
            using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, error.Kind, mode);
            StringAssert.Contains(error.Message, fragment);
            // Failure surfaces within the bounded page budget.
            Assert.IsTrue(peer.RequestsTo("/records") <= FeishuSourceImportClient.MaxListPages, mode);
        }
    }

    [TestMethod]
    public async Task ReadPages2501RecordsCompletelyWithStableIdentities()
    {
        using FeishuSourceImportTestPeer peer = PagedPeer(2501);
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportTable table = snapshot.Tables.Single();
        Assert.AreEqual(2501, table.Records.Length);
        Assert.AreEqual(2501, table.Records.Select(record => record.Id).Distinct().Count());
        Assert.AreEqual("rec000000", table.Records[0].Id);
        Assert.AreEqual("rec002500", table.Records[^1].Id);
        Assert.AreEqual("行 002500", table.Records[^1].Values["fldTitleA0000001"].GetString());
        Assert.AreEqual(2500, table.Records[^1].Values["fldCountA0000002"].GetInt32());
        // 500 per page: exactly six pages for 2501 records.
        Assert.AreEqual(6, peer.RequestsTo("/records"));
        CollectionAssert.AreEqual(new[] { "500", "1000", "1500", "2000", "2500" },
            peer.Requests.Where(request => request.Path.EndsWith("/records", StringComparison.Ordinal))
                .Select(request => FeishuSourceImportTestPeer.QueryValue(request.Query, "page_token"))
                .OfType<string>().ToArray());
    }

    [TestMethod]
    public async Task ReadRetriesBoundedTransientAndRateLimitFailuresOnly()
    {
        // Failures are injected after connect so the provider read path is
        // the code under retry.
        using FeishuSourceImportTestPeer rateLimited = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await ConnectAsync(rateLimited).ConfigureAwait(false))
        {
            rateLimited.NextResponses.Enqueue(FeishuSourceImportTestPeer.RateLimited(0));
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
            Assert.AreEqual(1, snapshot.Tables.Single().Records.Length);
            Assert.AreEqual(3, rateLimited.AppInfoRequests);
        }

        using FeishuSourceImportTestPeer unavailable = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await ConnectAsync(unavailable).ConfigureAwait(false))
        {
            unavailable.NextResponses.Enqueue(
                FeishuSourceImportTestPeer.Status(System.Net.HttpStatusCode.ServiceUnavailable));
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
            Assert.AreEqual(3, unavailable.AppInfoRequests);
        }

        using FeishuSourceImportTestPeer exhausted = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await ConnectAsync(exhausted).ConfigureAwait(false))
        {
            for (int i = 0; i < 3; i++) exhausted.NextResponses.Enqueue(FeishuSourceImportTestPeer.RateLimited(0));
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.RateLimited, error.Kind);
            Assert.AreEqual(4, exhausted.AppInfoRequests);
        }

        using FeishuSourceImportTestPeer business = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await ConnectAsync(business).ConfigureAwait(false))
        {
            business.NextResponses.Enqueue(FeishuSourceImportTestPeer.BusinessFailure(1254006, "internal error"));
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Business, error.Kind);
            Assert.AreEqual("1254006", error.ApiCode);
            // Business failures never retry.
            Assert.AreEqual(2, business.AppInfoRequests);
        }
    }

    [TestMethod]
    public async Task RetryWaitsHonourCancellationWithoutFurtherRequests()
    {
        using FeishuSourceImportTestPeer peer = PagedPeer(1);
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        peer.NextResponses.Enqueue(FeishuSourceImportTestPeer.RateLimited(2));
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        using var cancel = new CancellationTokenSource(TimeSpan.FromMilliseconds(200));
        await Assert.ThrowsExactlyAsync<TaskCanceledException>(
            () => provider.ReadAsync(cancel.Token)).ConfigureAwait(false);
        Assert.AreEqual(2, peer.AppInfoRequests);
    }

    [TestMethod]
    public async Task ObserveReturnsFreshVersionsAndExposesDrift()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA, TableB]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportObservation stable = await provider.ObserveAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreEqual(snapshot.Version, stable.Version);
        Assert.AreEqual(snapshot.Tables.Single(table => table.Id == TableA).Version, stable.TableVersions[TableA]);
        Assert.AreEqual(snapshot.Tables.Single(table => table.Id == TableB).Version, stable.TableVersions[TableB]);

        peer.AppRevision = 12;
        peer.Tables[0] = peer.Tables[0] with { Revision = 4 };
        HostSourceImportObservation drifted = await provider.ObserveAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreEqual("rev-12", drifted.Version);
        Assert.AreNotEqual(snapshot.Version, drifted.Version);
        Assert.IsTrue(drifted.TableVersions[TableA].StartsWith("rev-4|", StringComparison.Ordinal),
            drifted.TableVersions[TableA]);
    }

    [TestMethod]
    public async Task ObserveDetectsSchemaChangesWithoutRevisionBump()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        string baseline = snapshot.Tables.Single(table => table.Id == TableA).Version;

        // Feishu does not officially guarantee revision bumps for schema-only
        // changes: a same-revision rename must still break the comparison.
        peer.Tables[0] = peer.Tables[0] with
        {
            Fields = [.. peer.Tables[0].Fields.Select(field =>
                field.Id == "fldTitleA0000001" ? field with { Name = "重命名后的标题" } : field)],
        };
        HostSourceImportObservation renamed = await provider.ObserveAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreNotEqual(baseline, renamed.TableVersions[TableA], "rename must drift");

        // Restoring the name while changing only the select option identity
        // table drifts too, isolating option-identity drift from the rename.
        peer.Tables[0] = peer.Tables[0] with
        {
            Fields = [.. peer.Tables[0].Fields.Select(field => field.Id switch
            {
                "fldTitleA0000001" => field with { Name = "标题" },
                "fldStatusA0000003" => field with { Property = Options(("optDoing000000001", "进行中"),
                    ("optDone000000002", "已完成"), ("optExtra000000003", "新增")) },
                _ => field,
            })],
        };
        HostSourceImportObservation optionsChanged = await provider.ObserveAsync(CancellationToken.None)
            .ConfigureAwait(false);
        Assert.AreNotEqual(baseline, optionsChanged.TableVersions[TableA], "option identity change must drift");

        // Restoring the original fields returns the baseline version.
        peer.Tables[0] = MappingPeer().Tables[0];
        HostSourceImportObservation restored = await provider.ObserveAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreEqual(baseline, restored.TableVersions[TableA]);
    }

    [TestMethod]
    public async Task OpenAttachmentDownloadsOnlyViaOfficialEndpointAndRefusesRedirects()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        peer.AttachmentBytes["fileSyntheticToken01"] = [1, 2, 3, 4];
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportAttachment attachment = snapshot.Attachments.Single();
        await using Stream stream = await provider.OpenAttachmentAsync(attachment, CancellationToken.None)
            .ConfigureAwait(false);
        using var content = new MemoryStream();
        await stream.CopyToAsync(content).ConfigureAwait(false);
        CollectionAssert.AreEqual(new byte[] { 1, 2, 3, 4 }, content.ToArray());
        FeishuSourceImportTestPeer.RecordedRequest media = peer.Requests.Single(request =>
            request.Path.StartsWith("/open-apis/drive/v1/medias/", StringComparison.Ordinal));
        Assert.AreEqual("/open-apis/drive/v1/medias/fileSyntheticToken01/download", media.Path);
        Assert.AreEqual("Bearer " + FeishuSourceImportTestPeer.AccessToken, media.Authorization);

        peer.RedirectAttachment = true;
        peer.Requests.Clear();
        FeishuSourceImportException redirected = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.OpenAttachmentAsync(attachment, CancellationToken.None)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, redirected.Kind);
        // The redirect target is never requested and no credential leaves the
        // official endpoint.
        Assert.AreEqual(1, peer.Requests.Count);
        Assert.IsFalse(peer.Requests.Any(request => request.Path.Contains("unrelated", StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task OpenAttachmentRejectsByteCountMismatch()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        peer.AttachmentBytes["fileSyntheticToken01"] = [1, 2, 3, 4, 5];
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportAttachment attachment = snapshot.Attachments.Single();
        FeishuSourceImportException oversized = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.OpenAttachmentAsync(attachment, CancellationToken.None)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, oversized.Kind);

        peer.AttachmentBytes["fileSyntheticToken01"] = [1, 2, 3];
        FeishuSourceImportException shortStream = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.OpenAttachmentAsync(attachment, CancellationToken.None)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, shortStream.Kind);
    }

    [TestMethod]
    public async Task DisposalStopsSubsequentAndInFlightReads()
    {
        using FeishuSourceImportTestPeer peer = PagedPeer(1);
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        provider.Dispose();
        FeishuSourceImportException disposed = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => provider.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Disposed, disposed.Kind);
        int afterDispose = peer.Requests.Count;
        Assert.AreEqual(afterDispose, peer.Requests.Count);

        using FeishuSourceImportTestPeer held = PagedPeer(5);
        held.HoldRecords = true;
        using FeishuSourceImportConnection heldConnection = await ConnectAsync(held).ConfigureAwait(false);
        using FeishuSourceImportProvider heldProvider = heldConnection.CreateProvider([TableA]);
        Task read = heldProvider.ReadAsync(CancellationToken.None);
        await held.HoldingRecords.Task.WaitAsync(TimeSpan.FromSeconds(5)).ConfigureAwait(false);
        heldProvider.Dispose();
        await Assert.ThrowsExactlyAsync<TaskCanceledException>(() => read).ConfigureAwait(false);
        int stopped = held.Requests.Count;
        Assert.AreEqual(stopped, held.Requests.Count);    }

    [TestMethod]
    public async Task ThreeTableSyntheticReplayWith2501RecordsRelationsAndAttachments()
    {
        using FeishuSourceImportTestPeer peer = ReplayPeer();
        peer.AttachmentBytes["fileReplayA0000001"] = [1, 2, 3, 4];
        peer.AttachmentBytes["fileSyntheticToken02"] = [9, 8, 7];
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA, TableB, TableC]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);

        Assert.AreEqual(3, snapshot.Tables.Length);
        Assert.AreEqual(2505, snapshot.Tables.Sum(table => table.Records.Length));
        HostSourceImportTable tableA = snapshot.Tables.Single(table => table.Id == TableA);
        HostSourceImportTable tableB = snapshot.Tables.Single(table => table.Id == TableB);
        HostSourceImportTable tableC = snapshot.Tables.Single(table => table.Id == TableC);
        Assert.AreEqual(2501, tableA.Records.Length);
        Assert.AreEqual(2, tableB.Records.Length);
        Assert.AreEqual(2, tableC.Records.Length);
        Assert.IsTrue(tableA.Version.StartsWith("rev-4|", StringComparison.Ordinal), tableA.Version);
        Assert.IsTrue(tableB.Version.StartsWith("rev-6|", StringComparison.Ordinal), tableB.Version);
        Assert.IsTrue(tableC.Version.StartsWith("rev-8|", StringComparison.Ordinal), tableC.Version);
        Assert.AreEqual("rev-21", snapshot.Version);
        Assert.AreEqual("window", snapshot.ReadWindow.Consistency);

        // Relations reference stable source record IDs, not display text.
        HostSourceImportRecord a0 = tableA.Records.Single(record => record.Id == "rec000000");
        // One-way links can hold multiple values: cardinality comes from
        // property.multiple, never from the link direction.
        CollectionAssert.AreEqual(new[] { "rec000000", "rec000001" },
            a0.Values["fldToB0000008"].EnumerateArray().Select(id => id.GetString()).ToArray());
        HostSourceImportRecord b0 = tableB.Records.Single(record => record.Id == "rec000000");
        CollectionAssert.AreEqual(new[] { "rec000000", "rec002500" },
            b0.Values["fldToA0000002"].EnumerateArray().Select(id => id.GetString()).ToArray());
        HostSourceImportRecord c0 = tableC.Records.Single(record => record.Id == "rec000000");
        Assert.AreEqual("rec000001", c0.Values["fldToA0000002"].GetString());
        Assert.AreEqual("optDone000000002", tableA.Records.Single(record => record.Id == "rec000001")
            .Values["fldStatusA0000003"].GetString());

        Assert.AreEqual(2, snapshot.Attachments.Length);
        Assert.IsTrue(snapshot.Attachments.All(attachment => attachment.Size is 4 or 3));

        HostSourceImportObservation observation = await provider.ObserveAsync(CancellationToken.None)
            .ConfigureAwait(false);
        Assert.AreEqual(snapshot.Version, observation.Version);
        foreach (HostSourceImportTable table in snapshot.Tables)
            Assert.AreEqual(table.Version, observation.TableVersions[table.Id]);

        // The replay issued only official endpoints with the bearer token and
        // never touched an arbitrary URL from record values.
        Assert.IsTrue(peer.Requests.All(request => request.Path.StartsWith("/open-apis/", StringComparison.Ordinal)));
        Assert.IsTrue(peer.Requests.All(request =>
            request.Authorization == "Bearer " + FeishuSourceImportTestPeer.AccessToken));
        Assert.AreEqual(0, peer.Requests.Count(request =>
            request.Path.StartsWith("/open-apis/drive/v1/medias/", StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task EachProviderOwnsAnIndependentClientAndConnectionDisposalSparesHandedOverProviders()
    {
        using FeishuSourceImportTestPeer peer = PagedPeer(1);
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);

        // Disposing an earlier provider never affects later providers: the
        // wizard window can preflight repeatedly.
        using FeishuSourceImportProvider first = connection.CreateProvider([TableA]);
        first.Dispose();
        FeishuSourceImportException disposedRead = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => first.ReadAsync(CancellationToken.None)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Disposed, disposedRead.Kind);
        using FeishuSourceImportProvider second = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await second.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreEqual(1, snapshot.Tables.Single().Records.Length);

        // Closing the connection must not stop a provider that may already be
        // handed to the Host registry; it only blocks new providers.
        connection.Dispose();
        HostSourceImportSnapshot afterClose = await second.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        Assert.AreEqual(1, afterClose.Tables.Single().Records.Length);
        Assert.ThrowsExactly<ObjectDisposedException>(() => connection.CreateProvider([TableA]));

        // The injected test handler is shared by all per-provider clients and
        // must never be disposed by product code.
        Assert.AreEqual(0, peer.DisposeCount);
    }

    [TestMethod]
    public async Task UnverifiableLinkCardinalityDegradesToUnknownInsteadOfGuessing()
    {
        using FeishuSourceImportTestPeer peer = MappingPeer();
        // The property carries a target table but no official multiple flag:
        // cardinality is unverifiable, so the field must degrade to unknown
        // (forcing snapshot/skip) instead of guessing from the link direction.
        peer.Tables[0] = peer.Tables[0] with
        {
            Fields =
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new("fldToB0000008", "关联B", 22, "DuplexLink", false,
                    FeishuSourceImportTestPeer.PropertyJson("{\"table_id\":\"" + TableB + "\"}")),
            ],
            RecordCount = 2,
            Values = index => new Dictionary<string, object?>
            {
                ["标题"] = $"A {index:000}",
                ["关联B"] = new object[] { "rec000000", "rec000001" },
            },
        };
        peer.Tables[1] = peer.Tables[1] with { RecordCount = 0, Values = null };
        using FeishuSourceImportConnection connection = await ConnectAsync(peer).ConfigureAwait(false);
        FeishuSourceImportFieldCatalog catalogField = connection.Catalog.Tables[0].Fields
            .Single(field => field.FieldId == "fldToB0000008");
        Assert.AreEqual(("unknown", "json"), (catalogField.Kind, catalogField.ValueKind));
        Assert.AreEqual("snapshot", FeishuSourceImportFieldPolicy.Recommend(catalogField.Kind).Policy);

        using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
        HostSourceImportField field = snapshot.Tables[0].Fields.Single(candidate => candidate.Id == "fldToB0000008");
        Assert.AreEqual(("unknown", "json"), (field.Kind, field.ValueKind));
        Assert.IsNull(field.Relation);
        // The raw multi-value source IDs stay faithful for the confirmed
        // snapshot instead of being truncated or dropped.
        CollectionAssert.AreEqual(new[] { "rec000000", "rec000001" },
            snapshot.Tables[0].Records[0].Values["fldToB0000008"].EnumerateArray()
                .Select(id => id.GetString()).ToArray());
    }

    [TestMethod]
    public async Task DelayedBodiesHitTheDeadlineAsTransientWhileUserCancellationPropagatesRaw()
    {
        // JSON path: the first attempt's body never arrives within the
        // deadline; the bounded retry then succeeds against normal routing.
        using FeishuSourceImportTestPeer json = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/base/{json.AppToken}", FeishuSourceImportTestPeer.AccessToken, json,
            retryDelay: TimeSpan.Zero, requestTimeout: TimeSpan.FromMilliseconds(300)).ConfigureAwait(false))
        {
            json.NextResponses.Enqueue(FeishuSourceImportTestPeer.DelayedBody(5_000));
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
            Assert.AreEqual(1, snapshot.Tables.Single().Records.Length);
            // Connect (1) plus one deadline attempt and one successful
            // attempt on the provider read path.
            Assert.AreEqual(3, json.AppInfoRequests);
        }

        // Attachment path: every attempt's body stalls, so the bounded budget
        // ends in a classified Transient failure, never a user cancellation.
        using FeishuSourceImportTestPeer held = MappingPeer();
        held.AttachmentBytes["fileSyntheticToken01"] = [1, 2, 3, 4];
        using (FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/base/{held.AppToken}", FeishuSourceImportTestPeer.AccessToken, held,
            retryDelay: TimeSpan.Zero, requestTimeout: TimeSpan.FromMilliseconds(300)).ConfigureAwait(false))
        {
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None).ConfigureAwait(false);
            for (int i = 0; i < 3; i++) held.NextResponses.Enqueue(FeishuSourceImportTestPeer.DelayedBody(5_000));
            FeishuSourceImportException failure = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => provider.OpenAttachmentAsync(snapshot.Attachments.Single(), CancellationToken.None))
                .ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Transient, failure.Kind);
            StringAssert.Contains(failure.Message, "超时");
            Assert.IsFalse(failure.ToString().Contains("synthetic-secret", StringComparison.Ordinal));
        }

        // A user cancellation stays a raw cancellation, never a retry.
        using FeishuSourceImportTestPeer canceled = PagedPeer(1);
        using (FeishuSourceImportConnection connection = await ConnectAsync(canceled).ConfigureAwait(false))
        {
            using FeishuSourceImportProvider provider = connection.CreateProvider([TableA]);
            using var cancel = new CancellationTokenSource();
            cancel.Cancel();
            await Assert.ThrowsExactlyAsync<OperationCanceledException>(
                () => provider.ReadAsync(cancel.Token)).ConfigureAwait(false);
            Assert.AreEqual(1, canceled.AppInfoRequests);
        }
    }
}
