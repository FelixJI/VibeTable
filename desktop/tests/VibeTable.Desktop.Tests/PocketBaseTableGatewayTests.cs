using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PocketBaseTableGatewayTests
{
    [TestMethod]
    public async Task CatalogAndQueryPageUseOnlyClosedProductMethods()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.list",
            """{"tables":[""" + Schema("orders") + "]}");
        transport.Respond(
            "identifier.reconcile",
            """{"mappings":[]}""");
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.page",
            """
            {"rows":[{"id":"row-1","title":"Hello",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "offset":0,"limit":100,
             "filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"keyword":"","filters":[],"sorts":[],"offset":0,"limit":100}}}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var opened = await gateway.OpenDatabaseAsync("ignored", CancellationToken.None);
        var page = await gateway.ReadTablePageAsync(
            "orders", 0, 100, CancellationToken.None);

        CollectionAssert.AreEqual(new[] { "orders" }, opened.Tables.ToArray());
        Assert.AreEqual("Orders", opened.DisplayNames!["orders"]);
        Assert.AreEqual("row-1", page.Rows[0]["rowKey"]);
        Assert.AreEqual("title", page.Columns[1].Name);
        Assert.AreEqual("text", page.Columns[1].DataType);
        CollectionAssert.AreEqual(
            new[] { "identifier.reconcile", "schema.list", "schema.getTable", "query.page" },
            transport.Methods);
        Assert.IsFalse(transport.Serialized.Contains(
            "local",
            StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task RestoredWorkspaceListsSchemaWhenLegacyAliasWriteIsBlocked()
    {
        var transport = new ProductTransport();
        transport.RespondError(
            "identifier.reconcile",
            -32120,
            "Product data error",
            """{"kind":"product_data_error","code":"workspace.v1_write_disabled"}""");
        transport.Respond(
            "schema.list",
            """{"tables":[""" + Schema("orders") + "]}");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var opened = await gateway.OpenDatabaseAsync("ignored", CancellationToken.None);

        CollectionAssert.AreEqual(new[] { "orders" }, opened.Tables.ToArray());
        Assert.AreEqual("Orders", opened.DisplayNames!["orders"]);
        CollectionAssert.AreEqual(
            new[] { "identifier.reconcile", "schema.list" },
            transport.Methods);
    }

    [TestMethod]
    public async Task RendererColumnsUseCompositeRelationAndLookupCatalogIds()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", RelationalSchema("orders"));
        transport.Respond(
            "query.page",
            """
            {"rows":[],"offset":0,"limit":100,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await gateway.ReadTablePageAsync(
            "orders", 0, 100, CancellationToken.None);

        Assert.AreEqual(
            "orders.customer",
            page.Columns.Single(column => column.Name == "customer").RelationId);
        Assert.AreEqual(
            "orders.customer_name",
            page.Columns.Single(column => column.Name == "customer_name").LookupId);
    }

    [TestMethod]
    public async Task LaterPageRefreshesSchemaAndRetriesOnceBeforeProjectingRows()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.page", PageResponse("schema_0001", 0, "title"));
        transport.Respond(
            "schema.getTable",
            Schema("orders")
                .Replace("schema_0001", "schema_0002", StringComparison.Ordinal)
                .Replace("title", "summary", StringComparison.Ordinal)
                .Replace("Title", "Summary", StringComparison.Ordinal));
        transport.Respond("query.page", PageResponse("schema_0002", 100, "summary"));
        transport.Respond("query.page", PageResponse("schema_0002", 100, "summary"));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await gateway.ReadTablePageAsync("orders", 0, 100, CancellationToken.None);
        var page = await gateway.ReadTablePageAsync(
            "orders", 100, 100, CancellationToken.None);

        Assert.AreEqual("schema_0002", page.QuerySnapshot!.SchemaRevision);
        Assert.AreEqual("schema_0002", page.Revision!.SchemaRevision);
        Assert.AreEqual("summary", page.Columns[1].Name);
        Assert.AreEqual("value", page.Rows[0]["summary"]);
        CollectionAssert.AreEqual(
            new[]
            {
                "schema.getTable",
                "query.page",
                "query.page",
                "schema.getTable",
                "query.page",
            },
            transport.Methods);
    }

    [TestMethod]
    public async Task ContinuingSchemaChangesFailAfterOneRetry()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond("query.page", PageResponse("schema_0001", 0, "title"));
        transport.Respond(
            "schema.getTable",
            Schema("orders").Replace(
                "schema_0001",
                "schema_0002",
                StringComparison.Ordinal));
        transport.Respond("query.page", PageResponse("schema_0002", 100, "title"));
        transport.Respond("query.page", PageResponse("schema_0003", 100, "title"));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await gateway.ReadTablePageAsync("orders", 0, 100, CancellationToken.None);
        var exception = await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.ReadTablePageAsync(
                "orders", 100, 100, CancellationToken.None));

        StringAssert.Contains(exception.Message, "schema changed");
        CollectionAssert.AreEqual(
            new[]
            {
                "schema.getTable",
                "query.page",
                "query.page",
                "schema.getTable",
                "query.page",
            },
            transport.Methods);
    }

    [TestMethod]
    public async Task UpdateChecksCurrentValueAndCommitsThroughMutationKernel()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Before","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"1.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"After","__vibetableDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var result = await gateway.UpdateCellAsync(
            "orders",
            "row-1",
            "title",
            "Before",
            "After",
            "schema_0001",
            CancellationToken.None);

        Assert.AreEqual("After", result.StoredValue);
        Assert.AreEqual(2, result.Revision.DataRevision);
        CollectionAssert.AreEqual(
            new[]
            {
                "schema.getTable",
                "query.readRows",
                "mutation.apply",
                "query.readRows",
            },
            transport.Methods);
        StringAssert.Contains(
            transport.Serialized,
            @"""operations"":[{""kind"":""update"",""recordId"":""row-1""");
        StringAssert.Contains(
            transport.Serialized,
            @"""expectedDigest"":""sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa""");
    }

    [TestMethod]
    public async Task UpdateUsesDigestCapturedWhenCellEditingStarted()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Before","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"1.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"After","__vibetableDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await gateway.UpdateCellAsync(
            "orders",
            "row-1",
            "title",
            "Before",
            "After",
            "schema_0001",
            CancellationToken.None,
            "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");

        StringAssert.Contains(
            transport.Serialized,
            @"""expectedDigest"":""sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb""");
    }

    [TestMethod]
    public async Task StaleCellValueStopsBeforeMutation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """{"rows":[{"id":"row-1","title":"Changed elsewhere","__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await Assert.ThrowsExactlyAsync<TableEditConflictException>(() =>
            gateway.UpdateCellAsync(
                "orders",
                "row-1",
                "title",
                "Before",
                "After",
                "schema_0001",
                CancellationToken.None));

        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task StaleCheckIgnoresJsonObjectPropertyOrder()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":{"b":[1,2],"a":{"y":true,"x":"v"}},
            "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"1.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[{"recordId":"row-1","operation":"update","revision":"row_0002",
             "digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],
             "computedFields":{},"newRevision":"data_0002","emittedEvents":[],"warnings":[]}
            """);
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":{"saved":true},
            "__vibetableDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        var oldValue = new Dictionary<string, object?>
        {
            ["a"] = new Dictionary<string, object?>
            {
                ["x"] = "v",
                ["y"] = true,
            },
            ["b"] = new object?[] { 1, 2 },
        };

        await gateway.UpdateCellAsync(
            "orders",
            "row-1",
            "title",
            oldValue,
            new Dictionary<string, object?> { ["saved"] = true },
            "schema_0001",
            CancellationToken.None);

        Assert.IsTrue(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task StaleCheckTreatsJsonArrayOrderAsMeaningful()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.readRows",
            """
            {"rows":[{"id":"row-1","title":[1,2],
            "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await Assert.ThrowsExactlyAsync<TableEditConflictException>(() =>
            gateway.UpdateCellAsync(
                "orders",
                "row-1",
                "title",
                new object?[] { 2, 1 },
                new object?[] { 1, 2, 3 },
                "schema_0001",
                CancellationToken.None));

        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    [DataRow("none", "delete")]
    [DataRow("deletedAt", "archive")]
    public async Task DeleteUsesSchemaArchivePolicyAndAuthoritativeDigest(
        string archiveMode,
        string operationKind)
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.getTable",
            Schema("orders").Replace(
                @"""mode"":""none""",
                $@"""mode"":""{archiveMode}""",
                StringComparison.Ordinal));
        transport.Respond(
            "mutation.apply",
            """
            {"contractVersion":"1.0","status":"applied","changeSetId":"change-1",
             "affectedRows":[],"computedFields":{},"newRevision":"data_0002",
             "emittedEvents":[],"warnings":[]}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await gateway.DeleteRowsAsync(
            "orders",
            new[]
            {
                (
                    (object)"row-1",
                    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
            },
            "schema_0001",
            CancellationToken.None);

        StringAssert.Contains(
            transport.Serialized,
            $@"""kind"":""{operationKind}""");
        StringAssert.Contains(
            transport.Serialized,
            @"""expectedDigest"":""sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa""");
    }

    [TestMethod]
    public async Task DeleteRejectsMissingDigestBeforeMutation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.DeleteRowsAsync(
                "orders",
                new[] { ((object)"row-1", "") },
                "schema_0001",
                CancellationToken.None));

        Assert.IsFalse(transport.Methods.Contains("mutation.apply"));
    }

    [TestMethod]
    public async Task EditSchemaNormalizesJsonAndMultiSelectEditors()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable",
            """
            {
              "contractVersion":"1.0","tableId":"items","physicalName":"items",
              "displayName":"Items","kind":"base","schemaRevision":"schema_0001",
              "archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
              "fields":[
                {"fieldId":"metadata","physicalName":"metadata","displayName":"Metadata",
                 "kind":"scalar","dataType":"json","storageType":"json","nullable":true,
                 "defaultValue":null,
                 "constraints":[{"kind":"jsonSchema","schema":{"type":"object"}}],
                 "editor":{"kind":"json","config":{}},"readOnly":false,
                 "formula":null,"relation":null,"lookup":null,"attachmentPolicy":null},
                {"fieldId":"tags","physicalName":"tags","displayName":"Tags",
                 "kind":"scalar","dataType":"multiSelect","storageType":"select","nullable":true,
                 "defaultValue":null,
                 "constraints":[{"kind":"enum","multiple":true,"minSelected":0,
                   "maxSelected":null,"options":[{"value":"a","displayName":"A"},
                   {"value":"b","displayName":"B"}]}],
                 "editor":{"kind":"multiSelect","config":{}},"readOnly":false,
                 "formula":null,"relation":null,"lookup":null,"attachmentPolicy":null}
              ],"indexes":[]
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);
        var json = schema.Columns.Single(column => column.Name == "metadata");
        var tags = schema.Columns.Single(column => column.Name == "tags");

        Assert.AreEqual("json", json.DataType);
        Assert.AreEqual("json", json.Editor["kind"]);
        Assert.IsNotNull(json.Editor["schema"]);
        Assert.AreEqual("multi_select", tags.Editor["kind"]);
        CollectionAssert.AreEqual(
            new object?[] { "a", "b" },
            (object?[])tags.Editor["options"]!);
    }

    [TestMethod]
    public async Task FormulaColumnUsesDeclaredResultTypeInsteadOfNumberStorage()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.getTable",
            """
            {
              "contractVersion":"1.0","tableId":"items","physicalName":"items",
              "displayName":"Items","kind":"base","schemaRevision":"schema_0001",
              "archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
              "fields":[
                {"fieldId":"doubled","physicalName":"doubled","displayName":"Doubled",
                 "kind":"formula","dataType":"formula","storageType":"number",
                 "nullable":true,"defaultValue":null,"constraints":[],
                 "editor":{"kind":"formula","config":{}},"readOnly":true,
                 "formula":{"language":"cel-v1","source":"quantity * 2",
                   "resultType":"integer","dependencies":["quantity"],
                   "state":"valid","diagnostics":[]},
                 "relation":null,"lookup":null,"attachmentPolicy":null}
              ],"indexes":[]
            }
            """);
        transport.Respond(
            "query.page",
            """
            {"rows":[{"id":"row-1","doubled":10}],
             "offset":0,"limit":100,"filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await gateway.ReadTablePageAsync(
            "items", 0, 100, CancellationToken.None);

        var formula = page.Columns.Single(column => column.Name == "doubled");
        Assert.AreEqual("integer", formula.DataType);
        Assert.IsFalse(formula.Editable);
    }

    [TestMethod]
    public async Task ViewQueryCarriesGroupsAndParsesFullResultSummaries()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            """
            {
              "page":{"rows":[{"id":"row-1","title":"Hello"}],
                "offset":0,"limit":1,"filteredRows":12500,"totalRows":25000,
                "snapshot":{"snapshotId":"00000000000000000000000000000000",
                  "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                  "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
                  "dataRevision":1,"normalizedQuery":{"offset":0,"limit":1}}},
              "groupRows":[{"key":["east"],"count":7000,"summaries":[12345]}],
              "groupOffset":0,"groupLimit":100,"hasMoreGroups":false
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await gateway.QueryTableViewAsync(
            "orders",
            0,
            1,
            new TableQuery(
                Limit: 1,
                Groups: new[] { new GroupCondition("title") },
                Summaries: new[] { new SummaryCondition("amount", "sum") }),
            CancellationToken.None);

        Assert.AreEqual(12500, page.FilteredRows);
        Assert.AreEqual("east", page.GroupRows![0].Key[0]);
        Assert.AreEqual(7000L, page.GroupRows[0].Count);
        Assert.AreEqual(12345L, page.GroupRows[0].Summaries[0]);
        CollectionAssert.AreEqual(
            new[] { "schema.getTable", "query.view" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, "\"groups\"");
        StringAssert.Contains(transport.Serialized, "\"summaries\"");
    }

    private static string Schema(string table) =>
        """
        {
        "contractVersion":"1.0","tableId":"__TABLE__","physicalName":"__TABLE__",
        "displayName":"Orders","kind":"base","schemaRevision":"schema_0001",
        "archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
        "fields":[
          {"fieldId":"title","physicalName":"title","displayName":"Title","kind":"scalar",
           "dataType":"shortText","storageType":"text","nullable":false,"defaultValue":null,
           "constraints":[],"editor":{"kind":"text","config":{}},"readOnly":false,
           "formula":null,"relation":null,"lookup":null,"attachmentPolicy":null}
        ],"indexes":[]
        }
        """.Replace("__TABLE__", table, StringComparison.Ordinal);

    private static string RelationalSchema(string table) =>
        """
        {
        "contractVersion":"1.0","tableId":"__TABLE__","physicalName":"__TABLE__",
        "displayName":"Orders","kind":"base","schemaRevision":"schema_0001",
        "archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
        "fields":[
          {"fieldId":"customer","physicalName":"customer","displayName":"Customer",
           "kind":"relation","dataType":"relation","storageType":"relation",
           "nullable":true,"defaultValue":null,"constraints":[],
           "editor":{"kind":"relation","config":{}},"readOnly":false,
           "formula":null,
           "relation":{"targetTableId":"customers","cardinality":"one",
             "deletePolicy":"setNull","junctionTableId":null},
           "lookup":null,"attachmentPolicy":null},
          {"fieldId":"customer_name","physicalName":"customer_name",
           "displayName":"Customer name","kind":"lookup","dataType":"lookup",
           "storageType":"text","nullable":true,"defaultValue":null,
           "constraints":[],"editor":{"kind":"lookup","config":{}},
           "readOnly":true,"formula":null,"relation":null,
           "lookup":{"relationFieldId":"customer","targetFieldId":"name",
             "aggregate":"first"},"attachmentPolicy":null}
        ],"indexes":[]
        }
        """.Replace("__TABLE__", table, StringComparison.Ordinal);

    private static string PageResponse(
        string schemaRevision,
        int offset,
        string field) =>
        """
        {"rows":[{"id":"row-1","__FIELD__":"value",
        "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
        "offset":__OFFSET__,"limit":100,"filteredRows":1,"totalRows":201,
        "snapshot":{"snapshotId":"00000000000000000000000000000000",
        "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "databaseId":"local","table":"orders","schemaRevision":"__SCHEMA__",
        "dataRevision":1,"normalizedQuery":{"offset":__OFFSET__,"limit":100}}}
        """
        .Replace("__FIELD__", field, StringComparison.Ordinal)
        .Replace(
            "__OFFSET__",
            offset.ToString(System.Globalization.CultureInfo.InvariantCulture),
            StringComparison.Ordinal)
        .Replace("__SCHEMA__", schemaRevision, StringComparison.Ordinal);

    private sealed class ProductTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        private readonly Dictionary<string, Queue<(string Json, bool IsError)>> _responses =
            new(StringComparer.Ordinal);

        public List<string> Methods { get; } = [];
        public string Serialized { get; private set; } = "";

        public void Respond(string method, string result)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError)>? queue))
            {
                queue = new Queue<(string Json, bool IsError)>();
                _responses[method] = queue;
            }
            queue.Enqueue((result, false));
        }

        public void RespondError(string method, int code, string message, string data)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError)>? queue))
            {
                queue = new Queue<(string Json, bool IsError)>();
                _responses[method] = queue;
            }
            queue.Enqueue((
                JsonSerializer.Serialize(new
                {
                    code,
                    message,
                    data = JsonDocument.Parse(data).RootElement.Clone(),
                }),
                true));
        }

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            Serialized += line;
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            Methods.Add(method);
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError)>? queue)
                || queue.Count == 0)
            {
                throw new InvalidOperationException($"No response for {method}.");
            }
            (string json, bool isError) = queue.Dequeue();
            using var result = JsonDocument.Parse(json);
            JsonElement response = isError
                ? JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id,
                    error = result.RootElement.Clone(),
                })
                : JsonSerializer.SerializeToElement(new
                {
                    jsonrpc = "2.0",
                    id,
                    result = result.RootElement.Clone(),
                });
            _incoming.Writer.TryWrite(response);
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
