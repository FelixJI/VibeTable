using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class FeishuSourceImportConnectorTests
{
    private static FeishuSourceImportTestPeer StandardPeer() => new()
    {
        Tables =
        [
            new("tblSyntheticA00001", "合成表 A", 3,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleA0000001", "标题", 1, "Text", true),
                new FeishuSourceImportTestPeer.FieldSpec("fldOwnerA0000002", "负责人", 11, "User", false),
                new FeishuSourceImportTestPeer.FieldSpec("fldFormulaA0003", "计算", 21, "Formula", false),
            ], 0),
            new("tblSyntheticB00002", "合成表 B", 5,
            [
                new FeishuSourceImportTestPeer.FieldSpec("fldTitleB0000001", "标题", 1, "Text", true),
            ], 0),
        ],
    };

    [TestMethod]
    public void ParseSourceRejectsInvalidAndOutOfScopeInputs()
    {
        string[] invalid =
        [
            "", "   ", "not a url!!", "spaces in token",
            "http://x.feishu.cn/base/bascnHttpOnlyLink1",
            "https://x.larksuite.com/base/bascnInternational1",
            "https://evil.example.com/base/bascnForeignHost001",
            "https://x.feishu.cn/docs/docxnSyntheticDoc01",
            "https://x.feishu.cn/base/",
            "https://x.feishu.cn/base/bascnAb",
            "https://x.feishu.cn/base/bascnAbcd1234/extra",
            "https://x.feishu.cn/wiki/",
        ];
        foreach (string input in invalid)
        {
            FeishuSourceImportException error = Assert.ThrowsExactly<FeishuSourceImportException>(
                () => FeishuSourceImportConnector.ParseSource(input), input);
            Assert.AreEqual(FeishuSourceImportErrorKind.InvalidInput, error.Kind, input);
            Assert.IsFalse(string.IsNullOrWhiteSpace(error.Message), input);
        }
    }

    [TestMethod]
    public void ParseSourceAcceptsBaseWikiAndExplicitTokenForms()
    {
        Assert.AreEqual(new FeishuSourceReference("bascnAbcd123456", FeishuSourceReferenceKind.BaseLink),
            FeishuSourceImportConnector.ParseSource("https://example.feishu.cn/base/bascnAbcd123456?table=tbl"));
        Assert.AreEqual(new FeishuSourceReference("wiknAbcd1234567", FeishuSourceReferenceKind.WikiNode),
            FeishuSourceImportConnector.ParseSource("https://example.feishu.cn/wiki/wiknAbcd1234567"));
        Assert.AreEqual(new FeishuSourceReference("wiknAbcd1234567", FeishuSourceReferenceKind.WikiNode),
            FeishuSourceImportConnector.ParseSource("https://example.feishu.cn/wiki/7123456789/wiknAbcd1234567"));
        Assert.AreEqual(new FeishuSourceReference("bascnExplicitApp001", FeishuSourceReferenceKind.AppToken),
            FeishuSourceImportConnector.ParseSource(" bascnExplicitApp001 "));
    }

    [TestMethod]
    public async Task ConnectReadsCatalogThroughOfficialEndpointsWithScope()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        using FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/base/{peer.AppToken}", FeishuSourceImportTestPeer.AccessToken, peer,
            retryDelay: TimeSpan.Zero).ConfigureAwait(false);
        Assert.AreEqual(peer.AppToken, connection.Catalog.AppToken);
        Assert.AreEqual(peer.AppName, connection.Catalog.AppName);
        Assert.AreEqual(2, connection.Catalog.Tables.Count);
        FeishuSourceImportTableCatalog first = connection.Catalog.Tables[0];
        Assert.AreEqual("tblSyntheticA00001", first.TableId);
        Assert.AreEqual("rev-3", first.Revision);
        Assert.AreEqual("fldTitleA0000001", first.PrimaryFieldId);
        Assert.AreEqual(3, first.Fields.Count);
        Assert.AreEqual(("person", "json"),
            (first.Fields.Single(field => field.FieldId == "fldOwnerA0000002").Kind,
             first.Fields.Single(field => field.FieldId == "fldOwnerA0000002").ValueKind));
        Assert.AreEqual(("formula", "json"),
            (first.Fields.Single(field => field.FieldId == "fldFormulaA0003").Kind,
             first.Fields.Single(field => field.FieldId == "fldFormulaA0003").ValueKind));
        Assert.AreEqual(FeishuSourceImportConnector.AuthorizationScope, connection.Catalog.AuthorizationScope);
        // First request is the official app endpoint; no wiki call happened.
        Assert.AreEqual("/open-apis/bitable/v1/apps/" + peer.AppToken, peer.Requests[0].Path);
        Assert.AreEqual(0, peer.Requests.Count(request => request.Path.Contains("/wiki/", StringComparison.Ordinal)));
        Assert.IsTrue(peer.Requests.All(request =>
            request.Authorization == "Bearer " + FeishuSourceImportTestPeer.AccessToken));
        Assert.IsTrue(peer.Requests.All(request => request.Path.StartsWith("/open-apis/", StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task ConnectResolvesWikiNodeOnlyThroughOfficialGetNode()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        using FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/wiki/{FeishuSourceImportTestPeer.NodeToken}",
            FeishuSourceImportTestPeer.AccessToken, peer, retryDelay: TimeSpan.Zero).ConfigureAwait(false);
        Assert.AreEqual(peer.WikiObjToken, connection.Catalog.AppToken);
        FeishuSourceImportTestPeer.RecordedRequest resolution = peer.Requests.Single(request =>
            request.Path == "/open-apis/wiki/v2/spaces/get_node");
        Assert.IsTrue(resolution.Query.Contains("token=" + FeishuSourceImportTestPeer.NodeToken,
            StringComparison.Ordinal));
        Assert.IsTrue(resolution.Query.Contains("obj_type=wiki", StringComparison.Ordinal));
        // The bitable app endpoint is addressed with the resolved obj_token,
        // never with the wiki node token.
        Assert.IsTrue(peer.Requests.Any(request => request.Path.EndsWith("/apps/" + peer.WikiObjToken,
            StringComparison.Ordinal)));
        Assert.IsFalse(peer.Requests.Any(request => request.Path.EndsWith(
            "/apps/" + FeishuSourceImportTestPeer.NodeToken, StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task ConnectRejectsNonBitableWikiNodeWithoutTouchingBitableEndpoints()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        peer.WikiObjType = "docx";
        FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync(
                $"https://example.feishu.cn/wiki/{FeishuSourceImportTestPeer.NodeToken}",
                FeishuSourceImportTestPeer.AccessToken, peer, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.InvalidInput, error.Kind);
        StringAssert.Contains(error.Message, "多维表格");
        Assert.IsFalse(peer.Requests.Any(request => request.Path.Contains("/bitable/", StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task ConnectClassifiesConnectionTestFailures()
    {
        using FeishuSourceImportTestPeer unauthorized = StandardPeer();
        unauthorized.NextResponses.Enqueue(FeishuSourceImportTestPeer.Status(System.Net.HttpStatusCode.Unauthorized));
        await AssertFailure(unauthorized, FeishuSourceImportErrorKind.InvalidToken).ConfigureAwait(false);

        using FeishuSourceImportTestPeer forbidden = StandardPeer();
        forbidden.NextResponses.Enqueue(FeishuSourceImportTestPeer.Status(System.Net.HttpStatusCode.Forbidden));
        await AssertFailure(forbidden, FeishuSourceImportErrorKind.Forbidden).ConfigureAwait(false);

        using FeishuSourceImportTestPeer missing = StandardPeer();
        FeishuSourceImportException notFound = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync("bascnUnknownApp00001",
                FeishuSourceImportTestPeer.AccessToken, missing, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.NotFound, notFound.Kind);

        using FeishuSourceImportTestPeer business = StandardPeer();
        business.NextResponses.Enqueue(FeishuSourceImportTestPeer.BusinessFailure(
            99991663, "invalid access token for ai.plugin.appInfo"));
        FeishuSourceImportException token = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{business.AppToken}",
                FeishuSourceImportTestPeer.AccessToken, business, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.InvalidToken, token.Kind);
        Assert.AreEqual("99991663", token.ApiCode);

        using FeishuSourceImportTestPeer generic = StandardPeer();
        generic.NextResponses.Enqueue(FeishuSourceImportTestPeer.BusinessFailure(1254301, "no permission"));
        FeishuSourceImportException code = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{generic.AppToken}",
                FeishuSourceImportTestPeer.AccessToken, generic, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Business, code.Kind);
        Assert.AreEqual("1254301", code.ApiCode);

        static async Task AssertFailure(FeishuSourceImportTestPeer peer, FeishuSourceImportErrorKind kind)
        {
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{peer.AppToken}",
                    FeishuSourceImportTestPeer.AccessToken, peer, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
            Assert.AreEqual(kind, error.Kind);
        }
    }

    [TestMethod]
    public async Task ConnectRejectsMissingAccessTokenAndTablesWithoutPrimaryField()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(() =>
            FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{peer.AppToken}",
                "  ", peer, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);

        using FeishuSourceImportTestPeer broken = StandardPeer();
        broken.Tables[1].Fields.Clear();
        FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{broken.AppToken}",
                FeishuSourceImportTestPeer.AccessToken, broken, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, error.Kind);
    }

    [TestMethod]
    public async Task CreateProviderValidatesTableSelectionAndStrategyHintsMatchKinds()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        using FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
            $"https://example.feishu.cn/base/{peer.AppToken}", FeishuSourceImportTestPeer.AccessToken, peer,
            retryDelay: TimeSpan.Zero).ConfigureAwait(false);
        Assert.ThrowsExactly<FeishuSourceImportException>(() => connection.CreateProvider([]));
        Assert.ThrowsExactly<FeishuSourceImportException>(() =>
            connection.CreateProvider(["tblSyntheticA00001", "tblSyntheticA00001"]));
        Assert.ThrowsExactly<FeishuSourceImportException>(() =>
            connection.CreateProvider(["tblMissing000000000"]));
        using FeishuSourceImportProvider provider = connection.CreateProvider(["tblSyntheticA00001"]);
        Assert.AreEqual(peer.AppToken, provider.AppToken);
        Assert.AreEqual("native", FeishuSourceImportFieldPolicy.Recommend("text").Policy);
        Assert.AreEqual("native", FeishuSourceImportFieldPolicy.Recommend("relation").Policy);
        FeishuSourceImportFieldStrategy person = FeishuSourceImportFieldPolicy.Recommend("person");
        Assert.AreEqual("snapshot", person.Policy);
        Assert.AreEqual("json", person.SnapshotValueKind);
        Assert.IsTrue(person.ConfirmationRequired);
        foreach (string kind in new[] { "formula", "lookup", "unknown", "system", "telepathy" })
        {
            FeishuSourceImportFieldStrategy strategy = FeishuSourceImportFieldPolicy.Recommend(kind);
            Assert.AreEqual("snapshot", strategy.Policy, kind);
            Assert.IsTrue(strategy.ConfirmationRequired, kind);
        }
    }

    [TestMethod]
    public async Task NonOfficialRequestTargetsAreRejectedBeforeAnyRequestIsIssued()
    {
        using FeishuSourceImportTestPeer peer = StandardPeer();
        using FeishuSourceImportClient client =
            new(FeishuSourceImportTestPeer.AccessToken, peer, TimeSpan.Zero);
        string[] escapes =
        [
            "https://unrelated.example.com/open-apis/bitable/v1/apps/x",
            "http://open.feishu.cn/open-apis/bitable/v1/apps/x",
            "//unrelated.example.com/open-apis/bitable/v1/apps/x",
            "/not-open-apis/bitable/v1/apps/x",
            "open-apis/bitable/v1/apps/relative",
            "/open-apis/\\\\evil",
        ];
        foreach (string escape in escapes)
        {
            FeishuSourceImportException error = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
                () => client.GetDataAsync(escape, CancellationToken.None)).ConfigureAwait(false);
            Assert.AreEqual(FeishuSourceImportErrorKind.Protocol, error.Kind, escape);
        }
        // Rejection happens before any request: the handler saw nothing and
        // no real network access occurs.
        Assert.AreEqual(0, peer.Requests.Count);
    }

    [TestMethod]
    public async Task ServerControlledTextNeverEntersExceptionMessagesOrToString()
    {
        const string secret = "synthetic-secret-from-server-XYZ";
        using FeishuSourceImportTestPeer business = StandardPeer();
        business.NextResponses.Enqueue(FeishuSourceImportTestPeer.BusinessFailure(1254301, "leak " + secret));
        FeishuSourceImportException businessError = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{business.AppToken}",
                FeishuSourceImportTestPeer.AccessToken, business, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.Business, businessError.Kind);
        Assert.AreEqual("1254301", businessError.ApiCode);
        Assert.IsFalse(businessError.Message.Contains(secret, StringComparison.Ordinal));
        Assert.IsFalse(businessError.ToString().Contains(secret, StringComparison.Ordinal));

        using FeishuSourceImportTestPeer token = StandardPeer();
        token.NextResponses.Enqueue(FeishuSourceImportTestPeer.BusinessFailure(99991663, "invalid " + secret));
        FeishuSourceImportException tokenError = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync($"https://example.feishu.cn/base/{token.AppToken}",
                FeishuSourceImportTestPeer.AccessToken, token, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.InvalidToken, tokenError.Kind);
        Assert.AreEqual("99991663", tokenError.ApiCode);
        Assert.IsFalse(tokenError.Message.Contains(secret, StringComparison.Ordinal));
        Assert.IsFalse(tokenError.ToString().Contains(secret, StringComparison.Ordinal));

        using FeishuSourceImportTestPeer wiki = StandardPeer();
        wiki.WikiObjType = "docx " + secret;
        FeishuSourceImportException wikiError = await Assert.ThrowsExactlyAsync<FeishuSourceImportException>(
            () => FeishuSourceImportConnector.ConnectAsync(
                $"https://example.feishu.cn/wiki/{FeishuSourceImportTestPeer.NodeToken}",
                FeishuSourceImportTestPeer.AccessToken, wiki, retryDelay: TimeSpan.Zero)).ConfigureAwait(false);
        Assert.AreEqual(FeishuSourceImportErrorKind.InvalidInput, wikiError.Kind);
        StringAssert.Contains(wikiError.Message, "多维表格");
        Assert.IsFalse(wikiError.Message.Contains(secret, StringComparison.Ordinal));
        Assert.IsFalse(wikiError.ToString().Contains(secret, StringComparison.Ordinal));
    }
}
