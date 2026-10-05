using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// WPS 365 多维表格只读 provider 的合成 HTTP 回放：读请求形状、全量游标分页、
/// 重复游标/记录 ID 拒绝、HTTP 200 业务失败、401/403 快速失败、429 有界重试、
/// KSO-1 签名开关、重定向拒绝、取消/Dispose、结构漂移观察、容量上限、附件显式
/// unsupported 与凭据不外泄。全部为官方文档形状的合成回放，无 live 访问。
/// </summary>
[TestClass]
public sealed class WpsSourceImportProviderTests
{
    private const string FileId = "file_test_001";
    private const string Token = "TOKEN-SECRET-0123456789";
    private const string SecretKey = "SK-SECRET-9876543210";

    private static string Schema(params object[] sheets)
        => JsonSerializer.Serialize(new { code = 0, msg = "", data = new { sheets } });

    private static string RecordsPage(IEnumerable<string> records, string pageToken)
        => "{\"code\":0,\"msg\":\"\",\"data\":{\"records\":[" + string.Join(",", records)
            + "],\"page_token\":" + JsonSerializer.Serialize(pageToken) + "}}";

    private static object Text(string id, string name) => new { name, type = "MultiLineText", id, data = new { } };

    /// <summary>覆盖官方参数说明中出现的字段类型全集（3 表 + 关系闭包）。</summary>
    internal static string FullSchema()
    {
        object[] orderFields =
        [
            Text("fT", "标题"),
            new { name = "数量", type = "Number", id = "fN", data = new { number_format = "0.00_ " } },
            new { name = "金额", type = "Currency", id = "fC", data = new { number_format = "$#,##0.000_ " } },
            new { name = "比例", type = "Percent", id = "fP", data = new { number_format = "0.00%" } },
            new { name = "等级", type = "Rating", id = "fR", data = new { max = 5 } },
            new { name = "日期", type = "Date", id = "fD", data = new { number_format = "yyyy/mm/dd;@" } },
            new { name = "日期时间", type = "Date", id = "fDT", data = new { number_format = "yyyy/mm/dd hh:mm;@" } },
            new { name = "时间", type = "Time", id = "fTM", data = new { number_format = "hh:mm:ss;@" } },
            new { name = "完成", type = "Checkbox", id = "fB", data = new { } },
            new { name = "状态", type = "SingleSelect", id = "fS", data = new { items = new object[]
            {
                new { value = "选项1", id = "o1", color = 4292930553 },
                new { value = "选项2", id = "o2", color = 4292671479 },
            } } },
            new { name = "标签", type = "MultipleSelect", id = "fM", data = new { items = new object[]
            {
                new { value = "标签A", id = "tA" },
                new { value = "标签B", id = "tB" },
            } } },
            new { name = "客户", type = "Link", id = "fL",
                data = new { link_sheet = 2, link_view = "Y", is_auto = false, multiple_links = true, link_field = "fRB" } },
            new { name = "产品", type = "Link", id = "fLO",
                data = new { link_sheet = 3, link_view = "Z", is_auto = false, multiple_links = false } },
            new { name = "链接", type = "Url", id = "fU", data = new { display_text = "点我" } },
            new { name = "邮箱", type = "Email", id = "fE", data = new { } },
            new { name = "公式", type = "Formula", id = "fF",
                data = new { formula = "[数量]*2", number_format = "0_ ", value_type = "Fvt_number" } },
            new { name = "引用", type = "Lookup", id = "fLK",
                data = new { link_field = "fL", lookup_field = "fT", aggregation = "ToString" } },
            new { name = "编号", type = "AutoNumber", id = "fAN", data = new { number_format = "000000" } },
            new { name = "联系人", type = "Contact", id = "fCT", data = new { } },
            new { name = "创建人", type = "CreatedBy", id = "fCBY", data = new { } },
            new { name = "创建时间", type = "CreatedTime", id = "fCTM", data = new { number_format = "yyyy-mm-dd hh:mm;@" } },
            new { name = "修改人", type = "LastModifiedBy", id = "fLMB", data = new { } },
            new { name = "修改时间", type = "LastModifiedTime", id = "fLMT", data = new { } },
            new { name = "地址", type = "Address", id = "fAD", data = new { } },
            new { name = "富文本", type = "Note", id = "fNT", data = new { } },
            new { name = "附件", type = "Attachment", id = "fAT", data = new { } },
            new { name = "身份证", type = "ID", id = "fID", data = new { } },
            new { name = "电话", type = "Phone", id = "fPH", data = new { } },
            new { name = "全息", type = "Hologram", id = "fXX", data = new { } },
        ];
        return Schema(
            new { id = 1, name = "订单", primary_field_id = "fT", fields = orderFields,
                views = new object[] { new { id = "v1", name = "表格视图", type = "Grid" } } },
            new { id = 2, name = "客户", primary_field_id = "fT2",
                fields = new object[] { Text("fT2", "名称"),
                    new { name = "订单", type = "Link", id = "fRB",
                        data = new { link_sheet = 1, multiple_links = true, link_field = "fL" } } },
                views = new object[] { new { id = "v2", name = "表格视图", type = "Grid" } } },
            new { id = 3, name = "产品", primary_field_id = "fT3",
                fields = new object[] { Text("fT3", "名称") },
                views = new object[] { new { id = "v3", name = "表格视图", type = "Grid" } } });
    }

    private static string OrderRecord(string id, string title, string extra = "") => JsonSerializer.Serialize(new
    {
        id,
        fields = "{\"fT\":" + JsonSerializer.Serialize(title)
            + ",\"fN\":123.45,\"fC\":1234.001,\"fP\":98,\"fR\":3,\"fD\":\"2024/12/20\","
            + "\"fDT\":\"2024/12/20 11:30\",\"fTM\":\"11:12:15\",\"fB\":true,\"fS\":\"选项1\","
            + "\"fM\":[\"标签B\",\"标签A\"],\"fL\":[\"r9\",\"r10\"],\"fLO\":[\"r7\"],"
            + "\"fU\":{\"address\":\"https://example.com\",\"displayText\":\"示例\"},"
            + "\"fE\":\"user@example.com\",\"fF\":246.9,\"fLK\":[\"QA 重复显示值\"],\"fAN\":\"000001\","
            + "\"fCT\":[{\"id\":\"1234567890\",\"nickname\":\"张三\"}],\"fCBY\":{\"id\":\"1\",\"nickName\":\"张三\"},"
            + "\"fCTM\":\"2024-12-10 10:10\",\"fLMB\":\"张三\",\"fLMT\":\"2024/12/10 10:10:10\","
            + "\"fAD\":{\"districts\":[\"广东省\",\"珠海市\"],\"detail\":\"香洲区\"},"
            + "\"fNT\":{\"fileId\":\"f1\",\"summary\":\"s\"},"
            + "\"fAT\":[{\"uploadId\":\"up1\",\"fileName\":\"x.png\",\"size\":12,\"source\":\"Cloud\",\"type\":\"image/png\"}],"
            + "\"fID\":\"110101199001011234\",\"fPH\":\"13800000000\"" + extra + "}",
        created_time = "2024/12/20 11:30:32",
    });

    private static string SimpleRecord(string id, string fieldId, string value)
        => JsonSerializer.Serialize(new { id, fields = "{\"" + fieldId + "\":" + JsonSerializer.Serialize(value) + "}" });

    private sealed record Captured(HttpMethod Method, Uri Uri, string Body,
        string? Authorization, string? KsoDate, string? KsoAuthorization);

    private sealed class WpsReplayHandler : HttpMessageHandler
    {
        internal string SchemaJson = FullSchema();
        internal readonly Dictionary<string, List<string>> RecordPages = [];
        internal readonly List<Captured> Requests = [];
        internal Func<HttpRequestMessage, string, HttpResponseMessage?>? Intercept;
        internal Func<HttpRequestMessage, string, Task>? OnRequest;

        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken token)
        {
            string body = request.Content is null ? ""
                : await request.Content.ReadAsStringAsync(token);
            lock (Requests)
            {
                Requests.Add(new(request.Method, request.RequestUri!, body,
                    request.Headers.Authorization?.ToString(),
                    request.Headers.TryGetValues("X-Kso-Date", out var date) ? date.FirstOrDefault() : null,
                    request.Headers.TryGetValues("X-Kso-Authorization", out var auth) ? auth.FirstOrDefault() : null));
            }
            if (OnRequest is not null) await OnRequest(request, body);
            HttpResponseMessage? custom = Intercept?.Invoke(request, body);
            if (custom is not null) return custom;
            string path = request.RequestUri!.AbsolutePath;
            if (request.Method == HttpMethod.Get && path.EndsWith("/schema", StringComparison.Ordinal))
                return Reply(SchemaJson);
            if (request.Method == HttpMethod.Post && path.EndsWith("/records", StringComparison.Ordinal))
            {
                string sheetId = path.Split('/')[^2];
                lock (RecordPages)
                {
                    if (!RecordPages.TryGetValue(sheetId, out List<string>? pages))
                        throw new InvalidOperationException("Unexpected sheet: " + sheetId);
                    if (pages.Count == 0) throw new InvalidOperationException("No scripted page for sheet " + sheetId);
                    string page = pages[0];
                    pages.RemoveAt(0);
                    return Reply(page);
                }
            }
            throw new InvalidOperationException("Unexpected route: " + request.Method + " " + path);
        }

        private static HttpResponseMessage Reply(string json) => new(HttpStatusCode.OK)
        {
            Content = new StringContent(json, Encoding.UTF8, "application/json"),
        };
    }

    private static WpsSourceImportProvider Create(WpsReplayHandler handler,
        IReadOnlyCollection<string>? selection = null, WpsKso1Credentials? signature = null,
        WpsReadLimits? limits = null, Func<TimeSpan, CancellationToken, Task>? delay = null,
        TimeSpan? requestTimeout = null)
    {
        var connection = new WpsSourceImportConnection(FileId, "WPS 合成多维表", Token, signature);
        return new WpsSourceImportProvider(connection, selection ?? ["1", "2", "3"], limits, handler, delay,
            requestTimeout);
    }

    private static WpsReplayHandler SimpleHandler(int orders, int customers = 1, int products = 1,
        string schemaJson = "USE_FULL")
    {
        var handler = new WpsReplayHandler();
        if (schemaJson != "USE_FULL") handler.SchemaJson = schemaJson;
        handler.RecordPages["1"] = [RecordsPage(
            Enumerable.Range(0, orders).Select(i => OrderRecord("r" + i, "订单 " + i)).ToArray(), "")];
        handler.RecordPages["2"] = [RecordsPage(
            Enumerable.Range(0, customers).Select(i => SimpleRecord("r" + (100 + i), "fT2", "客户 " + i)).ToArray(), "")];
        handler.RecordPages["3"] = [RecordsPage(
            Enumerable.Range(0, products).Select(i => SimpleRecord("r" + (200 + i), "fT3", "产品 " + i)).ToArray(), "")];
        return handler;
    }

    private static int RecordsRequests(WpsReplayHandler handler)
        => handler.Requests.Count(request => request.Method == HttpMethod.Post);

    [TestMethod]
    public async Task ReadAsync_MapsOfficialFieldMatrixAndValues()
    {
        using WpsReplayHandler handler = SimpleHandler(2, 2, 2);
        using WpsSourceImportProvider provider = Create(handler);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);

        Assert.AreEqual(WpsSourceImportConnection.ProviderName, snapshot.Provider);
        Assert.AreEqual(FileId, snapshot.ContainerId);
        Assert.AreEqual("WPS 合成多维表", snapshot.DisplayName);
        Assert.AreEqual(HostSourceImportResult.ContractName, snapshot.Contract);
        Assert.AreEqual("window", snapshot.ReadWindow.Consistency);
        DateTimeOffset.Parse(snapshot.ReadWindow.StartedAt, System.Globalization.CultureInfo.InvariantCulture);
        DateTimeOffset.Parse(snapshot.ReadWindow.FinishedAt, System.Globalization.CultureInfo.InvariantCulture);
        Assert.AreEqual(0, snapshot.Attachments.Length);

        Assert.AreEqual(3, snapshot.Tables.Length);
        HostSourceImportTable orders = snapshot.Tables.Single(table => table.Id == "1");
        Assert.AreEqual("订单", orders.Name);
        Assert.AreEqual("fT", orders.PrimaryFieldId);
        Assert.AreEqual(2, orders.Records.Length);

        var kinds = orders.Fields.ToDictionary(field => field.Id, field => field.Kind);
        Assert.AreEqual("text", kinds["fT"]);
        Assert.AreEqual("number", kinds["fN"]);
        Assert.AreEqual("number", kinds["fC"]);
        Assert.AreEqual("number", kinds["fP"]);
        Assert.AreEqual("number", kinds["fR"]);
        Assert.AreEqual("date", kinds["fD"]);
        Assert.AreEqual("unknown", kinds["fDT"], "日期时间无时区证据，必须阻断而非虚构偏移");
        Assert.AreEqual("time", kinds["fTM"]);
        Assert.AreEqual("bool", kinds["fB"]);
        Assert.AreEqual("select", kinds["fS"]);
        Assert.AreEqual("multiSelect", kinds["fM"]);
        Assert.AreEqual("relation", kinds["fL"]);
        Assert.AreEqual("relation", kinds["fLO"]);
        Assert.AreEqual("url", kinds["fU"]);
        Assert.AreEqual("email", kinds["fE"]);
        Assert.AreEqual("formula", kinds["fF"]);
        Assert.AreEqual("lookup", kinds["fLK"]);
        Assert.AreEqual("unknown", kinds["fAN"]);
        Assert.AreEqual("person", kinds["fCT"]);
        Assert.AreEqual("system", kinds["fCBY"]);
        Assert.AreEqual("system", kinds["fCTM"]);
        Assert.AreEqual("system", kinds["fLMB"]);
        Assert.AreEqual("system", kinds["fLMT"]);
        Assert.AreEqual("unknown", kinds["fAD"]);
        Assert.AreEqual("unknown", kinds["fNT"]);
        Assert.AreEqual("unknown", kinds["fAT"]);
        Assert.AreEqual("text", kinds["fID"]);
        Assert.AreEqual("text", kinds["fPH"]);
        Assert.AreEqual("unknown", kinds["fXX"]);

        var byId = orders.Fields.ToDictionary(field => field.Id);
        Assert.IsTrue(byId["fN"].NumberFormat is { DisplayScale: 2, ScaleMode: "fixed", OnlyInt: false,
            PercentStorage: "ratio" });
        Assert.IsTrue(byId["fC"].NumberFormat is { Currency: "USD", DisplayScale: 3, UseGrouping: true });
        Assert.IsTrue(byId["fP"].NumberFormat is { PercentStorage: "percent" });
        Assert.IsNull(byId["fR"].NumberFormat);
        Assert.IsTrue(byId["fS"].Options.SequenceEqual(new HostSourceImportOption[]
        {
            new("o1", "选项1", $"#{4292930553 & 0xFFFFFF:X6}"),
            new("o2", "选项2", $"#{4292671479 & 0xFFFFFF:X6}"),
        }));
        Assert.AreEqual(new HostSourceImportRelation("2", "fRB", "many"), byId["fL"].Relation);
        Assert.AreEqual(new HostSourceImportRelation("3", "", "one"), byId["fLO"].Relation);
        StringAssert.Contains(byId["fF"].Definition, "wps:type=Formula");
        StringAssert.Contains(byId["fF"].Definition, "[数量]*2");
        StringAssert.Contains(byId["fDT"].Definition, "时区");
        StringAssert.Contains(byId["fAT"].Definition, "EVIDENCE_MISSING");
        StringAssert.Contains(byId["fXX"].Definition, "wps:type=Hologram");
        foreach (HostSourceImportField field in orders.Fields)
            Assert.IsFalse(field.Required, "WPS schema 无必填元数据，不虚构 required");

        var row = orders.Records.Single(record => record.Id == "r0").Values;
        Assert.AreEqual("订单 0", row["fT"].GetString());
        Assert.AreEqual("123.45", row["fN"].GetRawText(), "数值保留原始精度 token");
        Assert.AreEqual("2024-12-20", row["fD"].GetString(), "日期按格式解析为 ISO 日期");
        Assert.AreEqual("11:12:15", row["fTM"].GetString());
        Assert.IsTrue(row["fB"].GetBoolean());
        Assert.AreEqual("o1", row["fS"].GetString(), "选项值按 label→ID 身份映射");
        CollectionAssert.AreEqual(new[] { "tB", "tA" },
            row["fM"].EnumerateArray().Select(item => item.GetString()).ToArray());
        CollectionAssert.AreEqual(new[] { "r9", "r10" },
            row["fL"].EnumerateArray().Select(item => item.GetString()).ToArray());
        Assert.AreEqual("r7", row["fLO"].GetString(), "单值关联压平为记录 ID 字符串");
        Assert.AreEqual("https://example.com", row["fU"].GetString(), "超链接取 address 身份");
        Assert.AreEqual("2024/12/20 11:30", row["fDT"].GetString(), "日期时间保留原值待快照确认");
        Assert.AreEqual(JsonValueKind.Array, row["fAT"].ValueKind, "附件元数据保留原值，不伪装已下载");
        Assert.AreEqual(JsonValueKind.Array, row["fLK"].ValueKind);

        HostSourceImportTable customers = snapshot.Tables.Single(table => table.Id == "2");
        Assert.AreEqual(new HostSourceImportRelation("1", "fL", "many"),
            customers.Fields.Single(field => field.Id == "fRB").Relation, "双向关联按 link_field 保留反向身份");
    }

    [TestMethod]
    public async Task ReadAsync_SendsOfficialRequestShapeAndPaginatesAllSheets()
    {
        // 单表 2501 条：1000 + 1000 + 501 三页游标回放（page_size=1000）。
        var handler = new WpsReplayHandler();
        handler.RecordPages["1"] =
        [
            RecordsPage(Enumerable.Range(0, 1000).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), "tok-2"),
            RecordsPage(Enumerable.Range(1000, 1000).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), "tok-3"),
            RecordsPage(Enumerable.Range(2000, 501).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), ""),
        ];
        handler.RecordPages["2"] = [RecordsPage([SimpleRecord("r100", "fT2", "客户")], "")];
        handler.RecordPages["3"] = [RecordsPage([SimpleRecord("r200", "fT3", "产品")], "")];
        using WpsSourceImportProvider provider = Create(handler, selection: ["1"],
            limits: new WpsReadLimits(100, 50_000, 512, 1000));
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);

        Assert.AreEqual(1, snapshot.Tables.Length, "仅读取选定表");
        HostSourceImportTable orders = snapshot.Tables.Single(table => table.Id == "1");
        Assert.AreEqual("订单", orders.Name);
        Assert.AreEqual(2501, orders.Records.Length, "分页必须完整，不截断后续页");
        Assert.AreEqual(2501, orders.Records.Select(record => record.Id).Distinct().Count());
        Assert.AreEqual(4, handler.Requests.Count, "1 次 schema + 3 次选定表分页；未选表不发请求");
        Assert.IsFalse(handler.Requests.Any(request => request.Uri.AbsolutePath.Contains("/sheets/2/", StringComparison.Ordinal)
            || request.Uri.AbsolutePath.Contains("/sheets/3/", StringComparison.Ordinal)),
            "未选表不得有任何 records 请求");

        Captured schemaRequest = handler.Requests[0];
        Assert.AreEqual(HttpMethod.Get, schemaRequest.Method);
        Assert.AreEqual("https://openapi.wps.cn/v7/coop/dbsheet/file_test_001/schema",
            schemaRequest.Uri.ToString());
        Assert.AreEqual("Bearer " + Token, schemaRequest.Authorization);
        Assert.IsNull(schemaRequest.KsoDate, "未开启签名时不得携带 X-Kso-Date");
        Assert.IsNull(schemaRequest.KsoAuthorization, "未开启签名时不得携带 X-Kso-Authorization");

        List<Captured> recordRequests = handler.Requests.Skip(1).ToList();
        for (int index = 0; index < 3; index++)
        {
            Captured request = recordRequests[index];
            Assert.AreEqual(HttpMethod.Post, request.Method);
            Assert.AreEqual("/v7/coop/dbsheet/file_test_001/sheets/1/records", request.Uri.AbsolutePath);
            using JsonDocument body = JsonDocument.Parse(request.Body);
            Assert.IsTrue(body.RootElement.GetProperty("prefer_id").GetBoolean(), "必须按字段 ID 解析");
            Assert.AreEqual("original", body.RootElement.GetProperty("text_value").GetString());
            Assert.AreEqual(1000, body.RootElement.GetProperty("page_size").GetInt32());
            bool hasToken = body.RootElement.TryGetProperty("page_token", out JsonElement pageToken);
            if (index == 0)
                Assert.IsFalse(hasToken, "首页请求不得携带 page_token");
            else
            {
                Assert.IsTrue(hasToken, "后续页必须携带上一响应的 page_token");
                Assert.AreEqual("tok-" + (index + 1), pageToken.GetString());
            }
        }
    }

    [TestMethod]
    public async Task ReadAsync_RejectsRepeatedPageCursor()
    {
        var handler = new WpsReplayHandler();
        handler.RecordPages["1"] =
        [
            RecordsPage([SimpleRecord("r0", "fT", "a")], "tok-1"),
            RecordsPage([SimpleRecord("r1", "fT", "b")], "tok-1"), // 重复游标
            RecordsPage([SimpleRecord("r2", "fT", "c")], ""),
        ];
        handler.RecordPages["2"] = [RecordsPage([], "")];
        handler.RecordPages["3"] = [RecordsPage([], "")];
        using WpsSourceImportProvider provider = Create(handler);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Pagination, error.Failure);
        StringAssert.Contains(error.Message, "重复的分页游标");
    }

    [TestMethod]
    public async Task ReadAsync_RejectsDuplicateRecordIdAcrossSheetsAndPages()
    {
        var handler = new WpsReplayHandler();
        handler.RecordPages["1"] =
        [
            RecordsPage([SimpleRecord("r0", "fT", "a")], "tok-1"),
            RecordsPage([SimpleRecord("r0", "fT", "重复")], ""), // 跨页重复 ID
        ];
        handler.RecordPages["2"] = [];
        handler.RecordPages["3"] = [];
        using WpsSourceImportProvider provider = Create(handler);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Pagination, error.Failure);
        StringAssert.Contains(error.Message, "记录 ID 重复");
    }

    [TestMethod]
    public async Task ReadAsync_Http200WithBusinessFailureIsFailureAndNeverEchoesServerMessage()
    {
        var handler = new WpsReplayHandler();
        handler.RecordPages["1"] = [];
        handler.RecordPages["2"] = [];
        handler.RecordPages["3"] = [];
        // 服务器 msg 可能回显凭据；异常只允许封闭错误码与固定文案。
        handler.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/records")
            ? Reply(JsonSerializer.Serialize(new { code = 400000103,
                msg = "资源不存在 token=" + Token + " sk=" + SecretKey }))
            : null;
        using WpsSourceImportProvider provider = Create(handler);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
        Assert.AreEqual(400000103L, error.BusinessCode);
        StringAssert.Contains(error.Message, "400000103");
        string full = error.ToString();
        Assert.IsFalse(full.Contains(Token, StringComparison.Ordinal), "业务失败不得回显服务器 msg 中的 token");
        Assert.IsFalse(full.Contains(SecretKey, StringComparison.Ordinal), "业务失败不得回显服务器 msg 中的密钥");
        Assert.IsFalse(full.Contains("资源不存在", StringComparison.Ordinal), "服务器 msg 不得进入异常文本");
        Assert.AreEqual(1, RecordsRequests(handler), "业务失败不重试");

        static HttpResponseMessage Reply(string json) => new(HttpStatusCode.OK)
        { Content = new StringContent(json, Encoding.UTF8, "application/json") };
    }

    [TestMethod]
    public async Task AuthAndPermissionFailuresAreImmediateWithoutRetry()
    {
        var handler = new WpsReplayHandler();
        handler.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? new HttpResponseMessage(HttpStatusCode.Unauthorized)
            { Content = new StringContent("{\"code\":0,\"msg\":\"\"}", Encoding.UTF8, "application/json") }
            : null;
        using (WpsSourceImportProvider provider = Create(handler))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Authentication, error.Failure);
            StringAssert.Contains(error.Message, "重新授权");
        }
        Assert.AreEqual(1, handler.Requests.Count, "HTTP 401 必须快速失败");

        var permission = new WpsReplayHandler();
        permission.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? Reply(JsonSerializer.Serialize(new { code = 403000001,
                msg = "无权限 token=" + Token + " sk=" + SecretKey }))
            : null;
        using (WpsSourceImportProvider provider = Create(permission))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Permission, error.Failure);
            Assert.AreEqual(403000001L, error.BusinessCode);
            StringAssert.Contains(error.Message, "kso.dbsheet.read");
            Assert.IsFalse(error.ToString().Contains(Token, StringComparison.Ordinal));
            Assert.IsFalse(error.ToString().Contains(SecretKey, StringComparison.Ordinal));
        }
        Assert.AreEqual(1, permission.Requests.Count, "业务码 403* 必须快速失败");

        static HttpResponseMessage Reply(string json) => new(HttpStatusCode.OK)
        { Content = new StringContent(json, Encoding.UTF8, "application/json") };
    }

    [TestMethod]
    public async Task RateLimitRetriesAreBoundedAndCancellable()
    {
        int delays = 0;
        Func<TimeSpan, CancellationToken, Task> delay = (_, token) => { delays++; return Task.CompletedTask; };

        var recover = SimpleHandler(1, 1, 1);
        int recordsCalls = 0;
        recover.Intercept = (request, _) =>
        {
            if (!request.RequestUri!.AbsolutePath.EndsWith("/records")) return null;
            return ++recordsCalls == 1
                ? new HttpResponseMessage(HttpStatusCode.TooManyRequests)
                : null; // 交回默认脚本
        };
        using (WpsSourceImportProvider provider = Create(recover, delay: delay))
        {
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);
            Assert.AreEqual(1, snapshot.Tables.Single(table => table.Id == "1").Records.Length);
        }
        Assert.AreEqual(4, RecordsRequests(recover), "三页正常分页 + HTTP 429 有界重试一次");
        Assert.AreEqual(1, delays);

        delays = 0;
        var persistent = new WpsReplayHandler();
        persistent.RecordPages["1"] = [];
        persistent.RecordPages["2"] = [];
        persistent.RecordPages["3"] = [];
        persistent.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/records")
            ? new HttpResponseMessage(HttpStatusCode.TooManyRequests)
            : null;
        using (WpsSourceImportProvider provider = Create(persistent, delay: delay))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.RateLimited, error.Failure);
        }
        Assert.AreEqual(WpsOpenApiClient.MaxAttempts, RecordsRequests(persistent), "持续限频必须停止在固定上限");
        Assert.AreEqual(WpsOpenApiClient.MaxAttempts - 1, delays);

        delays = 0;
        var businessRate = SimpleHandler(1, 1, 1);
        int businessCalls = 0;
        businessRate.Intercept = (request, _) =>
        {
            if (!request.RequestUri!.AbsolutePath.EndsWith("/records")) return null;
            return ++businessCalls == 1
                ? Reply(JsonSerializer.Serialize(new { code = 429000001, msg = "请求太频繁" }))
                : null;
        };
        using (WpsSourceImportProvider provider = Create(businessRate, delay: delay))
        {
            await provider.ReadAsync(CancellationToken.None);
        }
        Assert.AreEqual(4, RecordsRequests(businessRate), "三页正常分页 + 业务码 429000001 有界重试一次");

        static HttpResponseMessage Reply(string json) => new(HttpStatusCode.OK)
        { Content = new StringContent(json, Encoding.UTF8, "application/json") };
    }

    [TestMethod]
    public async Task SignatureWhenConfiguredMatchesOfficialAlgorithm()
    {
        var handler = SimpleHandler(1, 1, 1);
        var signature = new WpsKso1Credentials("AK-TEST", SecretKey);
        using WpsSourceImportProvider provider = Create(handler, signature: signature);
        await provider.ReadAsync(CancellationToken.None);

        Assert.AreNotEqual(0, handler.Requests.Count);
        foreach (Captured request in handler.Requests)
        {
            Assert.IsNotNull(request.KsoDate, "开启签名后每个请求必须携带 X-Kso-Date");
            Assert.IsNotNull(request.KsoAuthorization);
            StringAssert.StartsWith(request.KsoAuthorization, "KSO-1 AK-TEST:");
            string expected = WpsKso1Signer.Sign(request.Method.Method,
                request.Uri.PathAndQuery, "application/json", request.KsoDate!, SecretKey,
                Encoding.UTF8.GetBytes(request.Body));
            Assert.AreEqual("KSO-1 AK-TEST:" + expected, request.KsoAuthorization,
                "签名必须可在相同输入下按官方算法复算（含空 GET 请求体）");
        }
    }

    [TestMethod]
    public async Task RedirectsAreRejectedWithoutFollowing()
    {
        var handler = SimpleHandler(1, 1, 1);
        handler.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? new HttpResponseMessage(HttpStatusCode.Redirect)
            { Headers = { Location = new Uri("https://attacker.example/collect") } }
            : null;
        using WpsSourceImportProvider provider = Create(handler);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
        StringAssert.Contains(error.Message, "重定向");
        Assert.AreEqual(1, handler.Requests.Count, "认证头绝不能跟随重定向外泄");
    }

    [TestMethod]
    public async Task FailuresNeverExposeCredentials()
    {
        var handler = SimpleHandler(1, 1, 1);
        var signature = new WpsKso1Credentials("AK-TEST", SecretKey);
        handler.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? new HttpResponseMessage(HttpStatusCode.Unauthorized)
            : null;
        using WpsSourceImportProvider provider = Create(handler, signature: signature);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        string full = error.ToString() + error.Message + (error.StackTrace ?? "");
        Assert.IsFalse(full.Contains(Token, StringComparison.Ordinal), "access token 不得出现在异常中");
        Assert.IsFalse(full.Contains(SecretKey, StringComparison.Ordinal), "签名密钥不得出现在异常中");
        Assert.IsFalse(full.Contains("AK-TEST", StringComparison.Ordinal), "AccessKey 不得出现在异常中");
    }

    [TestMethod]
    public async Task CancellationStopsPaginationImmediately()
    {
        var handler = SimpleHandler(2, 2, 2);
        using var gate = new SemaphoreSlim(0, 1);
        using var cts = new CancellationTokenSource();
        handler.RecordPages["1"] =
        [
            RecordsPage([SimpleRecord("r0", "fT", "a")], "tok-1"),
            RecordsPage([SimpleRecord("r1", "fT", "b")], ""),
        ];
        handler.RecordPages["2"] = [];
        handler.RecordPages["3"] = [];
        handler.OnRequest = async (request, _) =>
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/sheets/1/records"))
            {
                await gate.WaitAsync(cts.Token);
            }
        };
        using WpsSourceImportProvider provider = Create(handler);
        Task read = provider.ReadAsync(cts.Token);
        await WaitUntil(() => handler.Requests.Count >= 2, cts.Token);
        cts.Cancel();
        gate.Release();
        try
        {
            await read;
            Assert.Fail("取消后 ReadAsync 不应成功返回。");
        }
        catch (OperationCanceledException)
        {
            // HttpClient 将用户取消包装为 TaskCanceledException（其派生类），均接受。
        }
        Assert.AreEqual(2, handler.Requests.Count, "取消后不得继续发起后续请求");
    }

    private static async Task WaitUntil(Func<bool> condition, CancellationToken token)
    {
        while (!condition())
        {
            await Task.Delay(10, token);
            if (token.IsCancellationRequested) throw new OperationCanceledException(token);
        }
    }

    [TestMethod]
    public async Task VersionIsOrderInsensitiveButSemanticallySensitive()
    {
        using WpsReplayHandler handler = SimpleHandler(1, 1, 1);
        using WpsSourceImportProvider provider = Create(handler, selection: ["1"]);
        HostSourceImportSnapshot first = await provider.ReadAsync(CancellationToken.None);
        HostSourceImportObservation same = await provider.ObserveAsync(CancellationToken.None);
        Assert.AreEqual(first.Version, same.Version, "结构未变时语义版本必须稳定");
        Assert.AreEqual(first.Tables[0].Version, same.TableVersions["1"]);

        // 字段数组倒序 + 字段对象属性倒序：语义相同，版本不变。
        handler.SchemaJson = Reshuffle(FullSchema());
        Assert.AreEqual(first.Version, (await provider.ObserveAsync(CancellationToken.None)).Version,
            "字段顺序与属性顺序无关");

        // 逐项语义变更：rename / type / option / relation / 表名都必须失效。
        foreach ((string name, string mutated) in new (string, string)[]
        {
            ("rename", MutateField("fT", "name", "标题X")),
            ("type", MutateField("fT", "type", "Number")),
            ("option", MutateNode("fS", data => data!["items"]![0]!["value"] = "选项1X")),
            ("relation", MutateNode("fL", data => data!["link_sheet"] = 3)),
            ("sheetname", MutateSheetName("订单X")),
        })
        {
            handler.SchemaJson = mutated;
            HostSourceImportObservation drifted = await provider.ObserveAsync(CancellationToken.None);
            Assert.AreNotEqual(first.Version, drifted.Version, $"{name} 变更必须使版本失效");
            Assert.AreNotEqual(first.Tables[0].Version, drifted.TableVersions["1"], name);
        }

        // 未选表变化不影响本次迁移的版本投影。
        handler.SchemaJson = MutateField("fT2", "name", "名称X");
        Assert.AreEqual(first.Version, (await provider.ObserveAsync(CancellationToken.None)).Version,
            "未选表变化不得阻断本次迁移");
        handler.SchemaJson = FullSchema();
        Assert.AreEqual(first.Version, (await provider.ObserveAsync(CancellationToken.None)).Version);
    }

    private static System.Text.Json.Nodes.JsonObject Root() => (System.Text.Json.Nodes.JsonObject)System.Text.Json.Nodes.JsonNode.Parse(FullSchema())!;

    private static string MutateField(string fieldId, string property, System.Text.Json.Nodes.JsonNode? value)
    {
        System.Text.Json.Nodes.JsonObject root = Root();
        foreach (System.Text.Json.Nodes.JsonNode? sheet in root["data"]!["sheets"]!.AsArray())
        {
            foreach (System.Text.Json.Nodes.JsonNode? field in sheet!["fields"]!.AsArray())
            {
                if (field!["id"]!.GetValue<string>() == fieldId)
                {
                    field[property] = value;
                    return root.ToJsonString();
                }
            }
        }
        throw new InvalidOperationException("Test mutation field not found: " + fieldId);
    }

    private static string MutateNode(string fieldId, Action<System.Text.Json.Nodes.JsonObject?> mutate)
    {
        System.Text.Json.Nodes.JsonObject root = Root();
        foreach (System.Text.Json.Nodes.JsonNode? field in root["data"]!["sheets"]![0]!["fields"]!.AsArray())
        {
            if (field!["id"]!.GetValue<string>() == fieldId)
            {
                mutate(field["data"]!.AsObject());
                break;
            }
        }
        return root.ToJsonString();
    }

    private static string MutateSheetName(string name)
    {
        System.Text.Json.Nodes.JsonObject root = Root();
        root["data"]!["sheets"]![0]!["name"] = name;
        return root.ToJsonString();
    }

    private static string Reshuffle(string schemaJson)
    {
        System.Text.Json.Nodes.JsonNode node = System.Text.Json.Nodes.JsonNode.Parse(schemaJson)!;
        foreach (System.Text.Json.Nodes.JsonNode? sheet in node["data"]!["sheets"]!.AsArray())
        {
            System.Text.Json.Nodes.JsonArray fields = sheet!["fields"]!.AsArray();
            System.Text.Json.Nodes.JsonNode?[] reversed = fields.ToArray().Reverse().ToArray();
            fields.Clear();
            foreach (System.Text.Json.Nodes.JsonNode? field in reversed)
            {
                var rebuilt = new System.Text.Json.Nodes.JsonObject();
                foreach (KeyValuePair<string, System.Text.Json.Nodes.JsonNode?> property
                    in field!.AsObject().Reverse())
                    rebuilt[property.Key] = property.Value?.DeepClone();
                fields.Add(rebuilt);
            }
        }
        return node.ToJsonString();
    }

    [TestMethod]
    public async Task CatalogProvidesFieldDetailsForDowngradeDecisions()
    {
        using WpsReplayHandler handler = SimpleHandler(1, 1, 1);
        using WpsSourceImportProvider provider = Create(handler);
        WpsCatalog catalog = await provider.ReadCatalogAsync(CancellationToken.None);
        Assert.AreEqual(FileId, catalog.FileId);
        Assert.AreEqual(3, catalog.Tables.Count);
        WpsTableSummary orders = catalog.Tables.Single(table => table.Id == "1");
        Assert.AreEqual(29, orders.Fields.Count);
        Assert.AreEqual(1, catalog.Tables.Single(table => table.Id == "2").ViewCount);
        WpsFieldSummary link = orders.Fields.Single(field => field.Id == "fL");
        Assert.AreEqual(("2", "fRB", "many"),
            (link.RelationTargetTableId, link.RelationTargetFieldId, link.RelationCardinality));
        WpsFieldSummary attachment = orders.Fields.Single(field => field.Id == "fAT");
        Assert.AreEqual("unknown", attachment.Kind);
        StringAssert.Contains(attachment.Definition, "EVIDENCE_MISSING");
        WpsFieldSummary select = orders.Fields.Single(field => field.Id == "fS");
        Assert.AreEqual("select", select.Kind);
        Assert.AreEqual(2, select.OptionCount);
        Assert.AreEqual(0, handler.Requests.Count(request => request.Method == HttpMethod.Post),
            "catalog 不读取任何记录");

        WpsImportException unsupported = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.OpenAttachmentAsync(
                new HostSourceImportAttachment("up1", "1", "r0", "fAT", "x.png", "image/png", 12),
                CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Unsupported, unsupported.Failure);
        StringAssert.Contains(unsupported.Message, "EVIDENCE_MISSING");
    }

    [TestMethod]
    public async Task CapacityLimitsFailFastWithoutTruncation()
    {
        using WpsReplayHandler overRecords = SimpleHandler(6, 0, 0);
        using (WpsSourceImportProvider provider = Create(overRecords,
            limits: new WpsReadLimits(100, 5, 512, 500)))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Capacity, error.Failure);
        }

        using WpsReplayHandler overTables = SimpleHandler(1, 1, 1);
        using (WpsSourceImportProvider provider = Create(overTables,
            limits: new WpsReadLimits(1, 50_000, 512, 500)))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Capacity, error.Failure);
        }

        string twoFields = Schema(new { id = 1, name = "T", primary_field_id = "a",
            fields = new object[] { Text("a", "A"), Text("b", "B") },
            views = new object[] { new { id = "v" } } });
        using WpsReplayHandler overFields = SimpleHandler(0, 0, 0, twoFields);
        overFields.RecordPages["1"] = [RecordsPage([], "")];
        using (WpsSourceImportProvider provider = Create(overFields, selection: ["1"],
            limits: new WpsReadLimits(100, 50_000, 1, 500)))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Capacity, error.Failure);
        }
    }

    [TestMethod]
    public async Task ProtocolViolationsFailClosed()
    {
        string empty = Schema();
        using (WpsReplayHandler handler = SimpleHandler(0, 0, 0, empty))
        {
            using WpsSourceImportProvider provider = Create(handler);
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
            StringAssert.Contains(error.Message, "多维表格");
        }

        string noFields = Schema(new { id = 1, name = "T", primary_field_id = "", fields = Array.Empty<object>(),
            views = new object[] { new { id = "v" } } });
        using (WpsReplayHandler handler = SimpleHandler(0, 0, 0, noFields))
        {
            handler.RecordPages["1"] = [RecordsPage([], "")];
            using WpsSourceImportProvider provider = Create(handler, selection: ["1"]);
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
            StringAssert.Contains(error.Message, "没有任何字段");
        }

        // 服务器忽略 prefer_id 时按唯一名称回退；完全未知的键必须失败。
        using (WpsReplayHandler handler = SimpleHandler(1, 1, 1))
        {
            handler.RecordPages["1"] = [JsonSerializer.Serialize(new
            {
                code = 0,
                msg = "",
                data = new
                {
                    records = new object[] { new { id = "r0", fields = "{\"标题\":\"按名称\"}" } },
                    page_token = "",
                },
            })];
            using WpsSourceImportProvider provider = Create(handler);
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);
            Assert.AreEqual("按名称", snapshot.Tables[0].Records[0].Values["fT"].GetString());
        }

        using (WpsReplayHandler handler = SimpleHandler(1, 1, 1))
        {
            handler.RecordPages["1"] = [JsonSerializer.Serialize(new
            {
                code = 0,
                msg = "",
                data = new
                {
                    records = new object[] { new { id = "r0", fields = "{\"未知键\":\"x\"}" } },
                    page_token = "",
                },
            })];
            using WpsSourceImportProvider provider = Create(handler);
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
            StringAssert.Contains(error.Message, "未声明");
        }
    }

    [TestMethod]
    public async Task DisposeStopsAllUse()
    {
        using WpsReplayHandler handler = SimpleHandler(1, 1, 1);
        WpsSourceImportProvider provider = Create(handler);
        provider.Dispose();
        provider.Dispose();
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(
            () => provider.ReadAsync(CancellationToken.None));
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(
            () => provider.ObserveAsync(CancellationToken.None));
    }

    [TestMethod]
    public async Task OnlySelectedSheetsAreReadAndSelectionIsValidated()
    {
        using WpsReplayHandler handler = SimpleHandler(2, 2, 2);
        using (WpsSourceImportProvider provider = Create(handler, selection: ["2"]))
        {
            HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);
            Assert.AreEqual(1, snapshot.Tables.Length);
            Assert.AreEqual("2", snapshot.Tables[0].Id);
            Assert.AreEqual(2, snapshot.Tables[0].Records.Length);
            Assert.AreEqual(2, handler.Requests.Count, "仅 schema + 选定表一页");
            Assert.IsTrue(handler.Requests.All(request =>
                request.Method == HttpMethod.Get || request.Uri.AbsolutePath.EndsWith("/sheets/2/records", StringComparison.Ordinal)),
                "未选表不得有任何 records 请求");
            HostSourceImportObservation observation = await provider.ObserveAsync(CancellationToken.None);
            Assert.AreEqual(snapshot.Version, observation.Version);
            Assert.IsTrue(observation.TableVersions.ContainsKey("2"));
            Assert.IsFalse(observation.TableVersions.ContainsKey("1"), "观察投影只含选定表");
        }

        using (WpsReplayHandler missing = SimpleHandler(2, 2, 2))
        using (WpsSourceImportProvider provider = Create(missing, selection: ["9"]))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Protocol, error.Failure);
            StringAssert.Contains(error.Message, "不存在");
        }

        Assert.ThrowsExactly<ArgumentException>(() => Create(SimpleHandler(0, 0, 0), selection: []));
        Assert.ThrowsExactly<ArgumentException>(() => Create(SimpleHandler(0, 0, 0), selection: ["1", "1"]));
        Assert.ThrowsExactly<ArgumentException>(() => Create(SimpleHandler(0, 0, 0), selection: [" "]));
    }

    [TestMethod]
    public async Task SameRecordIdAcrossTablesIsLegal()
    {
        using WpsReplayHandler handler = SimpleHandler(1, 1, 1);
        handler.RecordPages["1"] = [RecordsPage([SimpleRecord("rShared", "fT", "甲")], "")];
        handler.RecordPages["2"] = [RecordsPage([SimpleRecord("rShared", "fT2", "乙")], "")];
        handler.RecordPages["3"] = [RecordsPage([SimpleRecord("rShared", "fT3", "丙")], "")];
        using WpsSourceImportProvider provider = Create(handler);
        HostSourceImportSnapshot snapshot = await provider.ReadAsync(CancellationToken.None);
        Assert.AreEqual(3, snapshot.Tables.Length);
        Assert.IsTrue(snapshot.Tables.All(table => table.Records.Single().Id == "rShared"),
            "记录 ID 去重必须按表作用域，不同表允许相同 ID");
    }

    [TestMethod]
    public async Task ReadAsync_ThreeTablesTotal2505AndSingleTable2501()
    {
        var handler = new WpsReplayHandler();
        handler.RecordPages["1"] =
        [
            RecordsPage(Enumerable.Range(0, 1000).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), "tok-2"),
            RecordsPage(Enumerable.Range(1000, 1000).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), "tok-3"),
            RecordsPage(Enumerable.Range(2000, 501).Select(i => SimpleRecord("r" + i, "fT", "行 " + i)).ToArray(), ""),
        ];
        handler.RecordPages["2"] = [RecordsPage(
            [SimpleRecord("rA", "fT2", "客户 A"), SimpleRecord("rB", "fT2", "客户 B")], "")];
        handler.RecordPages["3"] = [RecordsPage(
            [SimpleRecord("rP", "fT3", "产品 P"), SimpleRecord("rQ", "fT3", "产品 Q")], "")];
        string[] sheet1Pages = [.. handler.RecordPages["1"]];

        using (WpsSourceImportProvider provider = Create(handler,
            limits: new WpsReadLimits(100, 50_000, 512, 1000)))
        {
            HostSourceImportSnapshot all = await provider.ReadAsync(CancellationToken.None);
            Assert.AreEqual(3, all.Tables.Length);
            Assert.AreEqual(2505, all.Tables.Sum(table => table.Records.Length), "选 3 表总 2505 条完整读取");
            Assert.AreEqual(2501, all.Tables.Single(table => table.Id == "1").Records.Length);
            Assert.AreEqual(6, handler.Requests.Count, "1 schema + 3 + 1 + 1 分页");
        }

        var single = new WpsReplayHandler { SchemaJson = handler.SchemaJson };
        single.RecordPages["1"] = [.. sheet1Pages];
        using (WpsSourceImportProvider provider = Create(single, selection: ["1"],
            limits: new WpsReadLimits(100, 50_000, 512, 1000)))
        {
            HostSourceImportSnapshot only = await provider.ReadAsync(CancellationToken.None);
            Assert.AreEqual(1, only.Tables.Length);
            Assert.AreEqual(2501, only.Tables[0].Records.Length, "单表选 2501 条完整读取");
            Assert.AreEqual(4, single.Requests.Count);
        }
    }

    /// <summary>读入阶段无限阻塞直到取消的合成响应体流。</summary>
    private sealed class BlockingStream(TaskCompletionSource cancelled) : Stream
    {
        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => 0;
        public override long Position { get => 0; set => throw new NotSupportedException(); }
        public override void Flush() => throw new NotSupportedException();
        public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken token)
        {
            try
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, token);
            }
            catch (OperationCanceledException)
            {
                cancelled.TrySetResult();
                throw;
            }
            return 0;
        }
    }

    [TestMethod]
    public async Task FullRequestBodyReadIsBoundedByTimeoutAndSessionLifetime()
    {
        // 1) 有限超时覆盖完整请求（含响应体读取）：headers 已返回、body 阻塞 → 超时。
        var timeoutHandler = new WpsReplayHandler();
        timeoutHandler.RecordPages["1"] = [];
        timeoutHandler.RecordPages["2"] = [];
        timeoutHandler.RecordPages["3"] = [];
        var timeoutCancelled = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        timeoutHandler.Intercept = (_, _) => new HttpResponseMessage(HttpStatusCode.OK)
        { Content = new StreamContent(new BlockingStream(timeoutCancelled)) };
        System.Diagnostics.Stopwatch elapsed = System.Diagnostics.Stopwatch.StartNew();
        using (WpsSourceImportProvider provider = Create(timeoutHandler, requestTimeout: TimeSpan.FromMilliseconds(200)))
        {
            WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
                () => provider.ReadAsync(CancellationToken.None));
            Assert.AreEqual(WpsImportFailure.Connection, error.Failure);
            StringAssert.Contains(error.Message, "超时");
        }
        Assert.IsTrue(elapsed.Elapsed < TimeSpan.FromSeconds(5), "超时必须限制响应体读取全过程");
        Assert.IsTrue(timeoutCancelled.Task.IsCompleted, "阻塞的响应体读入必须被超时取消");

        // 2) Dispose 立即中止在途响应体（会话 lifetime），而不是等超时。
        var disposeHandler = new WpsReplayHandler();
        disposeHandler.RecordPages["1"] = [];
        disposeHandler.RecordPages["2"] = [];
        disposeHandler.RecordPages["3"] = [];
        var disposeCancelled = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        disposeHandler.Intercept = (_, _) => new HttpResponseMessage(HttpStatusCode.OK)
        { Content = new StreamContent(new BlockingStream(disposeCancelled)) };
        elapsed.Restart();
        WpsSourceImportProvider disposing = Create(disposeHandler, requestTimeout: TimeSpan.FromSeconds(30));
        Task read = disposing.ReadAsync(CancellationToken.None);
        await WaitUntil(() => disposeHandler.Requests.Count > 0, CancellationToken.None);
        disposing.Dispose();
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() => read);
        Assert.IsTrue(elapsed.Elapsed < TimeSpan.FromSeconds(5), "Dispose 不得等满 30s 超时");
        Assert.IsTrue(disposeCancelled.Task.IsCompleted, "在途响应体读入必须被 Dispose 取消");
    }

    [TestMethod]
    public async Task NonJsonHttp500FailsWithNumericStatus()
    {
        var handler = SimpleHandler(1, 1, 1);
        handler.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? new HttpResponseMessage(HttpStatusCode.InternalServerError)
            { Content = new StringContent("<html>server error</html>", Encoding.UTF8, "text/html") }
            : null;
        using WpsSourceImportProvider provider = Create(handler);
        WpsImportException error = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Connection, error.Failure);
        StringAssert.Contains(error.Message, "500", "状态码必须以数值呈现，不允许非法枚举格式串");

        var invalidJson = SimpleHandler(1, 1, 1);
        invalidJson.Intercept = (request, _) => request.RequestUri!.AbsolutePath.EndsWith("/schema")
            ? new HttpResponseMessage(HttpStatusCode.OK)
            { Content = new StringContent("not-json", Encoding.UTF8, "application/json") }
            : null;
        using WpsSourceImportProvider provider2 = Create(invalidJson);
        WpsImportException protocol = await Assert.ThrowsExactlyAsync<WpsImportException>(
            () => provider2.ReadAsync(CancellationToken.None));
        Assert.AreEqual(WpsImportFailure.Protocol, protocol.Failure);
        StringAssert.Contains(protocol.Message, "有效 JSON");
    }

    [TestMethod]
    public async Task HostApiCatalogEntryNeedsNoSelectionAndReadsNoRecords()
    {
        using WpsReplayHandler handler = SimpleHandler(0, 0, 0);
        var connection = new WpsSourceImportConnection(FileId, "WPS 合成多维表", Token, null);
        WpsCatalog catalog = await WpsSourceImportHostApi.ReadCatalogAsync(connection,
            handler: handler, retryDelay: null);
        Assert.AreEqual(3, catalog.Tables.Count);
        Assert.AreEqual(29, catalog.Tables.Single(table => table.Id == "1").Fields.Count);
        Assert.AreEqual(1, handler.Requests.Count);
        Assert.AreEqual(HttpMethod.Get, handler.Requests[0].Method, "目录入口不读取任何记录");
    }

    [TestMethod]
    public void CredentialCarriersPrintRedactedToString()
    {
        var credentials = new WpsKso1Credentials("AK-PRINT", SecretKey);
        Assert.AreEqual("WpsKso1Credentials[accessKey=<redacted> secretKey=<redacted>]", credentials.ToString());
        var connection = new WpsSourceImportConnection(FileId, "WPS 合成多维表", Token, credentials);
        string printed = connection.ToString();
        StringAssert.Contains(printed, "<redacted>");
        Assert.IsFalse(printed.Contains(Token, StringComparison.Ordinal), "ToString 不得包含 token");
        Assert.IsFalse(printed.Contains(SecretKey, StringComparison.Ordinal), "ToString 不得包含 SecretKey");
        Assert.IsFalse(printed.Contains("AK-PRINT", StringComparison.Ordinal), "ToString 不得包含 AccessKey");
        Assert.IsFalse(credentials.ToString().Contains(SecretKey, StringComparison.Ordinal));
    }

    [TestMethod]
    public void ConnectionLocksCredentialsToOfficialApiBase()
    {
        foreach (string hostile in new[]
        {
            "https://user:pw@openapi.wps.cn",
            "https://openapi.wps.cn:8443",
            "https://openapi.wps.cn/v7",
            "https://openapi.wps.cn/?x=1",
            "https://openapi.wps.cn#fragment",
            "http://openapi.wps.cn",
            "https://evil.example",
            "https://sub.openapi.wps.cn",
            "https://openapi.wps.cn.evil.example",
            "not-a-uri",
        })
        {
            Assert.ThrowsExactly<ArgumentException>(() =>
                new WpsSourceImportConnection("file-1", "名称", "token-value", null, hostile), hostile);
        }
        var locked = new WpsSourceImportConnection("file-1", "名称", "token-value", null,
            WpsSourceImportConnection.DefaultApiBase);
        Assert.AreEqual("https://openapi.wps.cn/", locked.ApiBase.ToString());
    }

    [TestMethod]
    public void ConnectionValidationRejectsShareLinksAndMissingCredentials()
    {
        Assert.ThrowsExactly<ArgumentException>(() =>
            new WpsSourceImportConnection("https://kdocs.cn/l/abcdef", "分享", Token, null));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new WpsSourceImportConnection(" link-1", "分享", Token, null));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new WpsSourceImportConnection("file-1", "名称", "  ", null));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new WpsSourceImportConnection("file-1", "名称", Token, new WpsKso1Credentials("", "sk")));
        Assert.IsNull(WpsSourceImportHostApi.SignatureCredentials("", ""));
        Assert.IsNotNull(WpsSourceImportHostApi.SignatureCredentials("ak", "sk"));
        Assert.ThrowsExactly<ArgumentException>(() =>
            WpsSourceImportHostApi.SignatureCredentials("ak", null),
            "签名只填一项必须封闭报错，不静默降级");
        Assert.ThrowsExactly<ArgumentException>(() =>
            WpsSourceImportHostApi.SignatureCredentials(" ", "sk"));
        StringAssert.Contains(WpsSourceImportHostApi.DescribeSupport(), "EVIDENCE_MISSING");
    }
}
