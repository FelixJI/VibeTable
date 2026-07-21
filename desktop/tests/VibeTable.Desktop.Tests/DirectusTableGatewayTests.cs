using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DirectusTableGatewayTests
{
    [TestMethod]
    public async Task Read_MapsCollectionSchemaAndRowsToExistingGridContract()
    {
        using var directus = new FakeDirectusGateway();
        var gateway = new DirectusTableGateway(directus, new FakeLocalStateGateway());

        var opened = await gateway.OpenDatabaseAsync("directus://configured", CancellationToken.None);
        var page = await gateway.ReadTablePageAsync("vibetable_demo", 0, 100, CancellationToken.None);

        CollectionAssert.AreEqual(new[] { "vibetable_demo" }, opened.Tables.ToArray());
        Assert.AreEqual("项目表", opened.DisplayNames!["vibetable_demo"]);
        Assert.AreEqual("vibetable_demo", page.Table);
        Assert.AreEqual("Project One", page.Rows[0]["name"]);
        Assert.AreEqual("p1", page.Rows[0]["rowKey"]);
        Assert.AreEqual("client", page.Mode);
        Assert.IsNotNull(page.Revision);
        Assert.AreEqual("schema-1", page.Revision!.SchemaRevision);
        Assert.IsNotNull(page.QuerySnapshot);
        Assert.AreEqual("schema-1", page.QuerySnapshot!.SchemaRevision);
        Assert.AreEqual("vibetable_demo", page.QuerySnapshot.Table);
    }

    [TestMethod]
    public async Task ReadFirstPage_RefreshesSchemaAfterStudioAddsField()
    {
        using var directus = new FakeDirectusGateway();
        var gateway = new DirectusTableGateway(directus, new FakeLocalStateGateway());

        var before = await gateway.ReadTablePageAsync(
            "vibetable_demo", 0, 100, CancellationToken.None);

        directus.AddFormulaField();

        var after = await gateway.ReadTablePageAsync(
            "vibetable_demo", 0, 100, CancellationToken.None);

        Assert.AreEqual(2, directus.GetSchemaCalls);
        CollectionAssert.DoesNotContain(before.Columns.Select(column => column.Name).ToArray(), "total");
        CollectionAssert.Contains(after.Columns.Select(column => column.Name).ToArray(), "total");
        Assert.AreEqual("schema-2", after.Revision!.SchemaRevision);
    }

    [TestMethod]
    public async Task EditSchema_UsesWebBooleanAndDateTypeDiscriminators()
    {
        using var directus = new FakeDirectusGateway();
        var gateway = new DirectusTableGateway(directus, new FakeLocalStateGateway());

        var schema = await gateway.GetEditSchemaAsync(
            "vibetable_demo", CancellationToken.None);
        var columns = schema.Columns.ToDictionary(column => column.Name);

        Assert.AreEqual("boolean", columns["approved"].Editor["kind"]);
        Assert.AreEqual("date", columns["signed_on"].Editor["kind"]);
        Assert.AreEqual("date", columns["signed_on"].Editor["dateType"]);
        Assert.AreEqual("date", columns["occurred_at"].Editor["kind"]);
        Assert.AreEqual("datetime", columns["occurred_at"].Editor["dateType"]);
        Assert.AreEqual("date", columns["starts_at"].Editor["kind"]);
        Assert.AreEqual("time", columns["starts_at"].Editor["dateType"]);
    }

    [TestMethod]
    public async Task DeleteRows_UsesArchiveInsteadOfPermanentDelete()
    {
        using var directus = new FakeDirectusGateway();
        var gateway = new DirectusTableGateway(directus, new FakeLocalStateGateway());

        var result = await gateway.DeleteRowsAsync(
            "vibetable_demo", new[] { ((object)"p1", "digest") }, "schema-1", CancellationToken.None);

        CollectionAssert.AreEqual(new object[] { "p1" }, result.DeletedRowKeys.ToArray());
        CollectionAssert.AreEqual(new[] { "p1" }, directus.ArchivedIds.ToArray());
        Assert.AreEqual(0, directus.PermanentDeleteCalls);
    }

    private sealed class FakeDirectusGateway : IDirectusRpcGateway
    {
        public List<string> ArchivedIds { get; } = [];
        public int PermanentDeleteCalls { get; private set; }
        public int GetSchemaCalls { get; private set; }
        private bool _hasFormulaField;
        public event Action<DirectusChange>? Changed
        {
            add { }
            remove { }
        }

        public Task<DirectusCollectionList> ListCollectionsAsync(CancellationToken token)
            => Task.FromResult(new DirectusCollectionList(
                new[] { "vibetable_demo" },
                new Dictionary<string, string> { ["vibetable_demo"] = "hash" },
                new Dictionary<string, string> { ["vibetable_demo"] = "项目表" }));

        public Task<DirectusSchema> GetSchemaAsync(string collection, CancellationToken token)
        {
            GetSchemaCalls++;
            var columns = new List<ColumnSchema>
            {
                new("id", "Id", "string", false, false),
                new("name", "Name", "string", true, false),
                new("approved", "Approved", "boolean", true, false),
                new("signed_on", "Signed On", "date", true, true),
                new("occurred_at", "Occurred At", "datetime", true, true),
                new("starts_at", "Starts At", "time", true, true),
            };
            if (_hasFormulaField)
            {
                columns.Add(new ColumnSchema("total", "Total", "decimal", false, true));
            }
            return Task.FromResult(new DirectusSchema(
                collection,
                "id",
                columns,
                Array.Empty<JsonElement>(),
                _hasFormulaField ? "schema-2" : "schema-1",
                _hasFormulaField ? "hash-2" : "hash"));
        }

        public void AddFormulaField() => _hasFormulaField = true;

        public Task<DirectusPage> ReadAsync(
            string collection, TableQuery query, bool includeArchived, CancellationToken token)
        {
            using var id = JsonDocument.Parse("\"p1\"");
            using var name = JsonDocument.Parse("\"Project One\"");
            IReadOnlyDictionary<string, JsonElement> row = new Dictionary<string, JsonElement>
            {
                ["id"] = id.RootElement.Clone(),
                ["name"] = name.RootElement.Clone(),
            };
            return Task.FromResult(new DirectusPage(
                collection, new[] { row }, query.Offset, query.Limit, 1, 1, Array.Empty<string>(), "hash"));
        }

        public Task<DirectusItem> ArchiveAsync(
            string collection, string itemId, CancellationToken token)
        {
            ArchivedIds.Add(itemId);
            return Task.FromResult(EmptyItem(collection));
        }

        public Task<DirectusItem> DeleteAsync(
            string collection, string itemId, CancellationToken token)
        {
            PermanentDeleteCalls++;
            return Task.FromResult(EmptyItem(collection));
        }

        public Task<DirectusSessionStatus> LoginAsync(string email, string password, string? otp, CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusSessionStatus> RefreshAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusSessionStatus> LogoutAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusSessionStatus> GetStatusAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusServerInfo> GetServerInfoAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusCurrentUser> GetCurrentUserAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusItem> CreateAsync(string collection, IReadOnlyDictionary<string, object?> values, string? requestId, CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusItem> UpdateAsync(string collection, string itemId, IReadOnlyDictionary<string, object?> values, string? expectedDateUpdated, string? requestId, CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusItem> RestoreAsync(string collection, string itemId, CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusSubscription> SubscribeAsync(string uid, string collection, IReadOnlyList<string> fields, CancellationToken token) => throw new NotSupportedException();
        public Task<DirectusSubscription> UnsubscribeAsync(string uid, CancellationToken token) => throw new NotSupportedException();
        public Task<CreateTableResult> CreateTableAsync(string name, IReadOnlyList<FieldDefinition> fields, CancellationToken token) => throw new NotSupportedException();
        public Task<DeleteTableResult> DeleteTableAsync(string name, CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> ListIdentifierMappingsAsync(string? search, CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> UpdateIdentifierAliasesAsync(string mappingId, IReadOnlyList<string> aliases, CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> ImportIdentifierMappingsAsync(IReadOnlyList<IdentifierMappingImportItem> mappings, CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> ReconcileIdentifierMappingsAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> DeleteIdentifierMappingAsync(string mappingId, CancellationToken token) => throw new NotSupportedException();
        public Task<IdentifierMappingsResult> PurgeIdentifierMappingsAsync(CancellationToken token) => throw new NotSupportedException();
        public void Dispose() { }

        private static DirectusItem EmptyItem(string collection)
            => new(collection, new Dictionary<string, JsonElement>());
    }

    private sealed class FakeLocalStateGateway : IWorkspaceSupportRpcGateway
    {
        public Task<GridStateResult> GetGridStateAsync(string databaseId, string table, CancellationToken token)
            => Task.FromResult(new GridStateResult(new GridState(), "1"));
        public Task<GridStateResult> SaveGridStateAsync(string databaseId, string table, GridState state, string? revision, CancellationToken token)
            => Task.FromResult(new GridStateResult(state, "2"));
        public Task<PastePlan> PreviewPasteAsync(string collection, string schemaRevision, IReadOnlyDictionary<string, object?> selection, PasteStartCell startCell, IReadOnlyList<IReadOnlyList<PasteCell>> cells, CancellationToken token) => throw new NotSupportedException();
        public Task<ApplyPasteResult> ApplyPasteAsync(string collection, string token, string idempotencyKey, CancellationToken cancellationToken) => throw new NotSupportedException();
    }
}
