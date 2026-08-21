using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PocketBaseTableGatewayTests
{
    [TestMethod]
    public async Task CatalogAndQueryViewUseOnlyClosedProductMethods()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "schema.list",
            """{"tables":[""" + Schema("orders") + "]}");
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[{"id":"row-1","title":"Hello",
             "__vibetableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
             "offset":0,"limit":100,
             "filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"keyword":"","filters":[],"sorts":[],"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var opened = await gateway.OpenDatabaseAsync("ignored", CancellationToken.None);
        var page = await QueryViewAsync(gateway, "orders", 0, 100);

        CollectionAssert.AreEqual(new[] { "orders" }, opened.Tables.ToArray());
        Assert.AreEqual("Orders", opened.DisplayNames["orders"]);
        Assert.AreEqual("row-1", page.Rows[0]["rowKey"]);
        Assert.AreEqual("title", page.Columns[1].Name);
        Assert.AreEqual("text", page.Columns[1].DataType);
        CollectionAssert.AreEqual(
            new[] { "eq", "is_null" },
            page.Columns[1].FilterOperators!.ToArray());
        CollectionAssert.AreEqual(
            new[] { "schema.list", "schema.getTable", "query.view" },
            transport.Methods);
        Assert.IsFalse(transport.Serialized.Contains(
            "local",
            StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task RendererColumnsUseCompositeRelationAndLookupCatalogIds()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", RelationalSchema("orders"));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[],"offset":0,"limit":100,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await QueryViewAsync(gateway, "orders", 0, 100);

        Assert.AreEqual(
            "orders.fld_customer",
            page.Columns.Single(column => column.Name == "f_customer").RelationId);
        Assert.AreEqual(
            "orders.fld_customer_name",
            page.Columns.Single(column => column.Name == "f_customer_name").LookupId);
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
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
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
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
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
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
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
            {"contractVersion":"2.0","status":"applied","changeSetId":"change-1",
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
        JsonObject metadata = V2Field("metadata", "metadata", "Metadata", "json");
        metadata["json"] = new JsonObject
        {
            ["rootType"] = "object",
            ["maxSize"] = 1024,
            ["schema"] = new JsonObject { ["type"] = "object" },
        };
        JsonObject tagsField = V2Field("tags0000", "tags0000", "Tags", "multiSelect");
        tagsField["select"] = new JsonObject
        {
            ["options"] = new JsonArray(
                new JsonObject
                {
                    ["optionId"] = "opt_aaaaaaaa",
                    ["label"] = "A",
                    ["color"] = "red",
                    ["order"] = 0,
                    ["state"] = "active",
                },
                new JsonObject
                {
                    ["optionId"] = "opt_bbbbbbbb",
                    ["label"] = "B",
                    ["color"] = "blue",
                    ["order"] = 1,
                    ["state"] = "active",
                }),
        };
        string itemSchema = SchemaWithFields("items", metadata, tagsField);
        transport.Respond("schema.getTable", itemSchema);
        transport.Respond("schema.getTable", itemSchema);
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[],"offset":0,"limit":10,"filteredRows":0,"totalRows":0,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":10}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);
        var json = schema.Columns.Single(column => column.Name == "f_metadata");
        var tags = schema.Columns.Single(column => column.Name == "f_tags0000");

        Assert.AreEqual("json", json.DataType);
        Assert.AreEqual("json", json.Editor["kind"]);
        Assert.IsNotNull(json.Editor["schema"]);
        Assert.AreEqual("multi_select", tags.Editor["kind"]);
        CollectionAssert.AreEqual(
            new object?[] { "opt_aaaaaaaa", "opt_bbbbbbbb" },
            (object?[])tags.Editor["options"]!);
        var page = await QueryViewAsync(gateway, "items", 1, 10);
        var tagsColumn = page.Columns.Single(column => column.Name == "f_tags0000");
        Assert.AreEqual("multiSelect", tagsColumn.FilterInput);
        CollectionAssert.AreEqual(
            new[] { "opt_aaaaaaaa", "opt_bbbbbbbb" },
            tagsColumn.FilterOptions!.Select(option => option.Value).ToArray());
    }

    [TestMethod]
    public async Task EditSchemaAcceptsGeoPointWithNullJsonSpecification()
    {
        var transport = new ProductTransport();
        JsonObject title = V2Field("title000", "title000", "Title", "text");
        JsonObject location = V2Field("location0", "location0", "Location", "geoPoint");
        location["json"] = null;
        transport.Respond(
            "schema.getTable",
            SchemaWithFields("items", title, location));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        EditSchemaResult schema = await gateway.GetEditSchemaAsync(
            "items", CancellationToken.None);

        ColumnEditSchema text = schema.Columns.Single(column => column.Name == "f_title000");
        ColumnEditSchema geoPoint = schema.Columns.Single(column => column.Name == "f_location0");
        Assert.IsTrue(text.Editable);
        Assert.AreEqual("json", geoPoint.Editor["kind"]);
        Assert.IsNull(geoPoint.Editor["schema"]);
    }

    [TestMethod]
    public async Task FormulaColumnUsesDeclaredResultTypeInsteadOfNumberStorage()
    {
        var transport = new ProductTransport();
        JsonObject doubled = V2Field("doubled0", "doubled0", "Doubled", "formula");
        doubled["formula"] = new JsonObject
        {
            ["language"] = "cel-v1",
            ["source"] = "quantity * 2",
            ["resultType"] = "number",
        };
        doubled["storage"]!["kind"] = "computed";
        doubled["storage"]!["options"]!["onlyInt"] = true;
        doubled["display"]!["kind"] = "readonly";
        transport.Respond("schema.getTable", SchemaWithFields("items", doubled));
        transport.Respond(
            "query.view",
            ViewResponse("""
            {"rows":[{"id":"row-1","doubled":10}],
             "offset":0,"limit":100,"filteredRows":1,"totalRows":1,
             "snapshot":{"snapshotId":"00000000000000000000000000000000",
             "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
             "databaseId":"local","table":"items","schemaRevision":"schema_0001",
             "dataRevision":1,"normalizedQuery":{"offset":0,"limit":100}}}
            """));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await QueryViewAsync(gateway, "items", 0, 100);

        var formula = page.Columns.Single(column => column.Name == "f_doubled0");
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
              "groupRows":[{"key":["east","open"],"count":3000,"summaries":[5000],
                "parentCount":7000,"parentSummaries":[12345]}],
              "groupOffset":0,"groupLimit":100,"hasMoreGroups":false
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        var page = await gateway.QueryTableViewRawAsync(
            "orders",
            JsonSerializer.SerializeToElement(new
            {
                keyword = "",
                filters = Array.Empty<object>(),
                sorts = Array.Empty<object>(),
                offset = 0,
                limit = 1,
                groups = new[] { new { field = "title" } },
                summaries = new[] { new { field = "amount", function = "sum" } },
                groupOffset = 0,
                groupLimit = 100,
            }),
            CancellationToken.None);

        Assert.AreEqual(12500, page.FilteredRows);
        Assert.AreEqual("east", page.GroupRows![0].Key[0]);
        Assert.AreEqual(3000L, page.GroupRows[0].Count);
        Assert.AreEqual(5000L, page.GroupRows[0].Summaries[0]);
        Assert.AreEqual(7000L, page.GroupRows[0].ParentCount);
        Assert.AreEqual(12345L, page.GroupRows[0].ParentSummaries![0]);
        CollectionAssert.AreEqual(
            new[] { "schema.getTable", "query.view" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, "\"groups\"");
        StringAssert.Contains(transport.Serialized, "\"summaries\"");
    }

    [TestMethod]
    public async Task RawViewQueryPreservesUnknownAstValuesForSidecarValidation()
    {
        var transport = new ProductTransport();
        transport.Respond("schema.getTable", Schema("orders"));
        transport.Respond(
            "query.view",
            """
            {
              "page":{"rows":[],"offset":0,"limit":50,"filteredRows":0,"totalRows":0,
                "snapshot":{"snapshotId":"00000000000000000000000000000000",
                  "digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                  "databaseId":"local","table":"orders","schemaRevision":"schema_0001",
                  "dataRevision":1,"normalizedQuery":{"offset":0,"limit":50}}},
              "groupRows":[],"groupOffset":0,"groupLimit":100,"hasMoreGroups":false
            }
            """);
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[{"field":"title","operator":"raw_sql","value":{"x":1}}],"sorts":[],"offset":0,"limit":50,"groups":[],"summaries":[],"groupOffset":0,"groupLimit":100}""");

        await gateway.QueryTableViewRawAsync(
            "orders", query.RootElement, CancellationToken.None);

        StringAssert.Contains(transport.Serialized, "\"operator\":\"raw_sql\"");
        StringAssert.Contains(transport.Serialized, "\"value\":{\"x\":1}");
    }

    [TestMethod]
    public async Task ActiveCursorOpenUsesAtomicProjectionAndContinuesWithOpaqueToken()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0001", 1, "row-1", "opaque-2", true));
        transport.Respond("query.cursorFetch", CursorWindow("row-2", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[{"field":"title","operator":"raw_sql","value":{"x":1}}],"sorts":[],"limit":500,"groups":[{"field":"title"}],"summaries":[]}""");

        TablePage first = await gateway.OpenTableCursorRawAsync(
            "orders", query.RootElement, CancellationToken.None);
        TablePage second = await gateway.FetchTableCursorAsync(
            first.NextCursor!, CancellationToken.None);

        Assert.AreEqual("opaque-2", first.NextCursor);
        Assert.IsTrue(first.HasMore);
        Assert.AreEqual("row-2", second.Rows[0]["rowKey"]);
        Assert.IsFalse(second.HasMore);
        CollectionAssert.AreEqual(
            new[] { "query.selectionOpen", "query.cursorFetch" },
            transport.Methods);
        StringAssert.Contains(transport.Serialized, "\"operator\":\"raw_sql\"");
        StringAssert.Contains(transport.Serialized, "\"cursor\":\"opaque-2\"");
        Assert.IsFalse(transport.Serialized.Contains("\"groups\"", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task OlderLateSelectionCannotDowngradeTheSchemaCache()
    {
        var transport = new ProductTransport();
        var releaseOld = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        transport.RespondAfter(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0001", 1, "row-old", null, false),
            releaseOld.Task);
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0002", 2, "row-new", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse("""{"filters":[],"sorts":[],"limit":500}""");

        Task<TableSelectionProjection> old = gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        await transport.WaitForMethodCountAsync(1);
        TableSelectionProjection newer = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        releaseOld.SetResult();
        TableSelectionProjection older = await old;
        EditSchemaResult cached = await gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);

        Assert.AreEqual("schema_0002", newer.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0001", older.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0002", cached.SchemaRevision);
        CollectionAssert.AreEqual(
            new[] { "query.selectionOpen", "query.selectionOpen" },
            transport.Methods);
    }

    [TestMethod]
    public async Task OlderLateSchemaReadCannotDowngradeSelectionSchemaCache()
    {
        var transport = new ProductTransport();
        var releaseOldSchema = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        transport.RespondAfter(
            "schema.getTable",
            Schema("orders"),
            releaseOldSchema.Task);
        transport.Respond(
            "query.selectionOpen",
            SelectionProjectionJson("schema_0002", 2, "row-new", null, false));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse("""{"filters":[],"sorts":[],"limit":500}""");

        Task<EditSchemaResult> oldRead = gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);
        await transport.WaitForMethodCountAsync(1);
        TableSelectionProjection newer = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);
        releaseOldSchema.SetResult();
        EditSchemaResult completedOldRead = await oldRead;
        EditSchemaResult cached = await gateway.GetEditSchemaAsync(
            "orders", CancellationToken.None);

        Assert.AreEqual("schema_0002", newer.EditSchema.SchemaRevision);
        Assert.AreEqual("schema_0002", completedOldRead.SchemaRevision);
        Assert.AreEqual("schema_0002", cached.SchemaRevision);
        CollectionAssert.AreEqual(
            new[] { "schema.getTable", "query.selectionOpen" },
            transport.Methods);
    }

    [TestMethod]
    public async Task SelectionProjectionUsesOneRevisionMatchedProductRpc()
    {
        var transport = new ProductTransport();
        transport.Respond(
            "query.selectionOpen",
            JsonSerializer.Serialize(new
            {
                schemaSnapshot = JsonNode.Parse(Schema("orders")),
                cursorWindow = JsonNode.Parse(CursorWindow("row-1", "opaque-2", true)),
            }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[],"sorts":[],"limit":500,"groups":[],"summaries":[]}""");

        TableSelectionProjection projection = await gateway.OpenTableSelectionAsync(
            "orders", query.RootElement, CancellationToken.None);

        Assert.AreEqual("schema_0001", projection.Page.QuerySnapshot!.SchemaRevision);
        Assert.AreEqual("schema_0001", projection.EditSchema.SchemaRevision);
        Assert.AreEqual("row-1", projection.Page.Rows[0]["rowKey"]);
        CollectionAssert.AreEqual(new[] { "query.selectionOpen" }, transport.Methods);
    }

    [TestMethod]
    public async Task SelectionProjectionRejectsMismatchedDataRevisionWithoutRetry()
    {
        var transport = new ProductTransport();
        string mismatchedWindow = CursorWindow("row-1", null, false)
            .Replace("\"dataRevision\":1", "\"dataRevision\":2", StringComparison.Ordinal);
        transport.Respond(
            "query.selectionOpen",
            JsonSerializer.Serialize(new
            {
                schemaSnapshot = JsonNode.Parse(Schema("orders")),
                cursorWindow = JsonNode.Parse(mismatchedWindow),
            }));
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));
        using var query = JsonDocument.Parse(
            """{"filters":[],"sorts":[],"limit":500}""");

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.OpenTableSelectionAsync(
                "orders", query.RootElement, CancellationToken.None));

        CollectionAssert.AreEqual(new[] { "query.selectionOpen" }, transport.Methods);
    }

    [TestMethod]
    public async Task CursorStaleProductCodeSurvivesTheDesktopErrorMapper()
    {
        var transport = new ProductTransport();
        transport.RespondError(
            "query.cursorFetch",
            -32150,
            "Product data error",
            """{"kind":"product_data_error","message":"cursor changed","code":"query.cursor_stale","path":"cursor","details":{},"retryable":false}""");
        await using var client = new JsonRpcClient(transport);
        using var gateway = new PocketBaseTableGateway(
            new JsonRpcProductDataGateway(client),
            new JsonRpcWorkspaceSupportGateway(client));

        RpcRemoteException exception = await Assert.ThrowsExactlyAsync<RpcRemoteException>(
            () => gateway.FetchTableCursorAsync("opaque", CancellationToken.None));
        MutationError mapped = MutationErrorMapper.Map(exception);

        Assert.AreEqual("query.cursor_stale", mapped.Code);
        Assert.AreEqual("cursor changed", mapped.Message);
    }

    private static string CursorWindow(string rowId, string? nextCursor, bool hasMore)
        => JsonSerializer.Serialize(new
        {
            rows = new[] { new { id = rowId, title = "Hello" } },
            filteredRows = 50_000,
            totalRows = 50_000,
            querySnapshot = new
            {
                snapshotId = "00000000000000000000000000000000",
                digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                databaseId = "local",
                table = "orders",
                schemaRevision = "schema_0001",
                dataRevision = 1,
                normalizedQuery = new { offset = 0, limit = 500 },
            },
            nextCursor,
            hasMore,
        });

    private static string SelectionProjectionJson(
        string schemaRevision,
        int dataRevision,
        string rowId,
        string? nextCursor,
        bool hasMore)
    {
        JsonObject schema = JsonNode.Parse(Schema("orders"))!.AsObject();
        schema["schemaRevision"] = schemaRevision;
        schema["dataRevision"] = dataRevision;
        JsonObject cursor = JsonNode.Parse(CursorWindow(rowId, nextCursor, hasMore))!.AsObject();
        JsonObject snapshot = cursor["querySnapshot"]!.AsObject();
        snapshot["schemaRevision"] = schemaRevision;
        snapshot["dataRevision"] = dataRevision;
        return new JsonObject
        {
            ["schemaSnapshot"] = schema,
            ["cursorWindow"] = cursor,
        }.ToJsonString();
    }

    private static Task<TablePage> QueryViewAsync(
        PocketBaseTableGateway gateway,
        string table,
        int offset,
        int limit)
        => gateway.QueryTableViewRawAsync(
            table,
            JsonSerializer.SerializeToElement(new
            {
                keyword = "",
                filters = Array.Empty<object>(),
                sorts = Array.Empty<object>(),
                offset,
                limit,
                groups = Array.Empty<object>(),
                summaries = Array.Empty<object>(),
                groupOffset = 0,
                groupLimit = 100,
            }),
            CancellationToken.None);

    private static string ViewResponse(string page)
        => $"{{\"page\":{page},\"groupRows\":[],\"groupOffset\":0," +
            "\"groupLimit\":100,\"hasMoreGroups\":false}";

    private static string Schema(string table)
    {
        string fixture = File.ReadAllText(ProductCatalogFixturePath());
        JsonObject catalog = JsonNode.Parse(fixture)!.AsObject();
        JsonObject rpcCase = catalog["rpcCases"]!.AsArray()
            .Select(item => item!.AsObject())
            .Single(item => item["method"]!.GetValue<string>() == "schema.getTable");
        JsonObject schema = rpcCase["success"]!["result"]!.DeepClone().AsObject();
        schema["tableId"] = table;
        schema["displayName"] = "Orders";
        schema["schemaRevision"] = "schema_0001";
        JsonObject field = schema["fields"]!.AsArray()[0]!.AsObject();
        JsonObject identity = field["identity"]!.AsObject();
        identity["fieldId"] = "fld_title001";
        identity["physicalName"] = "title";
        identity["providerFieldId"] = "pb_title001";
        field["displayName"] = "Title";
        field["logicalType"] = "text";
        schema["capabilities"]!.AsArray()[0]!["filterOperators"] =
            new JsonArray("eq", "is_null");
        return schema.ToJsonString();
    }

    private static JsonObject V2Field(
        string fieldId,
        string physicalName,
        string displayName,
        string logicalType)
    {
        JsonObject schema = JsonNode.Parse(Schema("fixture"))!.AsObject();
        JsonObject field = schema["fields"]!.AsArray()[0]!.DeepClone().AsObject();
        JsonObject identity = field["identity"]!.AsObject();
        identity["fieldId"] = $"fld_{fieldId}";
        identity["physicalName"] = $"f_{physicalName}";
        identity["providerFieldId"] = $"pb_{fieldId}";
        field["displayName"] = displayName;
        field["logicalType"] = logicalType;
        return field;
    }

    private static string SchemaWithFields(string table, params JsonObject[] fields)
    {
        JsonObject schema = JsonNode.Parse(Schema(table))!.AsObject();
        schema["fields"] = new JsonArray(
            fields.Select(field => field.DeepClone()).ToArray());
        JsonObject capabilityTemplate = schema["capabilities"]!.AsArray()[0]!.AsObject();
        schema["capabilities"] = new JsonArray(fields
            .Select(field =>
            {
                JsonObject capability = capabilityTemplate.DeepClone().AsObject();
                capability["logicalType"] = field["logicalType"]!.GetValue<string>();
                capability["filterOperators"] = new JsonArray("eq", "is_null");
                return (JsonNode)capability;
            })
            .ToArray());
        return schema.ToJsonString();
    }

    private static string ProductCatalogFixturePath()
    {
        DirectoryInfo? directory = new(AppContext.BaseDirectory);
        while (directory is not null)
        {
            string candidate = Path.Combine(
                directory.FullName,
                "contracts",
                "v2",
                "fixtures",
                "product-rpc-catalog.json");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }

        throw new FileNotFoundException(
            "Could not locate contracts/v2/fixtures/product-rpc-catalog.json.");
    }

    private static string RelationalSchema(string table)
    {
        JsonObject relation = V2Field(
            "customer", "customer", "Customer", "relation");
        relation["relation"] = new JsonObject
        {
            ["targetTableId"] = "customers",
            ["cardinality"] = "one",
            ["deletePolicy"] = "setNull",
            ["displayFieldId"] = "fld_name0001",
        };
        JsonObject lookup = V2Field(
            "customer_name", "customer_name", "Customer name", "lookup");
        lookup["lookup"] = new JsonObject
        {
            ["path"] = new JsonArray(new JsonObject
            {
                ["relationFieldId"] = "fld_customer",
            }),
            ["targetFieldId"] = "fld_name0001",
        };
        return SchemaWithFields(table, relation, lookup);
    }

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
        private readonly Dictionary<string, Queue<(string Json, bool IsError, Task? Gate)>> _responses =
            new(StringComparer.Ordinal);
        private TaskCompletionSource _methodObserved = NewMethodObserved();

        public List<string> Methods { get; } = [];
        public string Serialized { get; private set; } = "";

        public void Respond(string method, string result)
            => RespondAfter(method, result, Task.CompletedTask);

        public void RespondAfter(string method, string result, Task gate)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue))
            {
                queue = new Queue<(string Json, bool IsError, Task? Gate)>();
                _responses[method] = queue;
            }
            queue.Enqueue((result, false, gate));
        }

        public void RespondError(string method, int code, string message, string data)
        {
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue))
            {
                queue = new Queue<(string Json, bool IsError, Task? Gate)>();
                _responses[method] = queue;
            }
            queue.Enqueue((
                JsonSerializer.Serialize(new
                {
                    code,
                    message,
                    data = JsonDocument.Parse(data).RootElement.Clone(),
                }),
                true,
                null));
        }

        public async Task WaitForMethodCountAsync(int count)
        {
            while (Methods.Count < count)
            {
                Task observed = _methodObserved.Task;
                if (Methods.Count < count)
                {
                    await observed.WaitAsync(TimeSpan.FromSeconds(2));
                }
            }
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
            TaskCompletionSource observed = _methodObserved;
            _methodObserved = NewMethodObserved();
            observed.TrySetResult();
            if (!_responses.TryGetValue(
                    method,
                    out Queue<(string Json, bool IsError, Task? Gate)>? queue)
                || queue.Count == 0)
            {
                throw new InvalidOperationException($"No response for {method}.");
            }
            (string json, bool isError, Task? gate) = queue.Dequeue();
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
            if (gate is null || gate.IsCompletedSuccessfully)
            {
                _incoming.Writer.TryWrite(response);
            }
            else
            {
                _ = CompleteAfterAsync(gate, response);
            }
            return Task.CompletedTask;
        }

        private async Task CompleteAfterAsync(Task gate, JsonElement response)
        {
            await gate.ConfigureAwait(false);
            _incoming.Writer.TryWrite(response);
        }

        private static TaskCompletionSource NewMethodObserved()
            => new(TaskCreationOptions.RunContinuationsAsynchronously);

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
