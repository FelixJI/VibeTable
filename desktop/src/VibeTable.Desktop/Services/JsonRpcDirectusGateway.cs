using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

public sealed record DirectusEmptyParams;
public sealed record DirectusLoginParams(string Email, string Password, string? Otp);
public sealed record DirectusCollectionParams(string Collection);
public sealed record DirectusItemParams(string Collection, string ItemId);
public sealed record DirectusReadParams(
    string Collection, TableQuery Query, bool IncludeArchived);
public sealed record DirectusCreateParams(
    string Collection, IReadOnlyDictionary<string, object?> Values, string? RequestId);
public sealed record DirectusUpdateParams(
    string Collection, string ItemId, IReadOnlyDictionary<string, object?> Values,
    string? ExpectedDateUpdated, string? RequestId);
public sealed record DirectusSubscribeParams(
    string Uid, string Collection, IReadOnlyList<string> Fields);
public sealed record DirectusUnsubscribeParams(string Uid);
public sealed record CreateTableParams(string Name, FieldDefinition[] Fields);
public sealed record DeleteTableParams(string Name);
public sealed record ListIdentifierMappingsParams(string? Search);
public sealed record UpdateIdentifierAliasesParams(string MappingId, IReadOnlyList<string> Aliases);
public sealed record ImportIdentifierMappingsParams(IReadOnlyList<IdentifierMappingImportItem> Mappings);

/// <summary>Directus JSON-RPC adapter over the supervisor-owned local pipe.</summary>
public sealed class JsonRpcDirectusGateway : IDirectusRpcGateway
{
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    private readonly JsonRpcClient _client;
    private bool _disposed;

    public JsonRpcDirectusGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
        _client.NotificationReceived += OnNotificationReceived;
    }

    public event Action<DirectusChange>? Changed;

    public Task<DirectusSessionStatus> LoginAsync(
        string email, string password, string? otp, CancellationToken token)
        => _client.InvokeAsync<DirectusLoginParams, DirectusSessionStatus>(
            "directus.login", new(email, password, otp), token);

    public Task<DirectusSessionStatus> RefreshAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusSessionStatus>("directus.refresh", token);

    public Task<DirectusSessionStatus> LogoutAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusSessionStatus>("directus.logout", token);

    public Task<DirectusSessionStatus> GetStatusAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusSessionStatus>("directus.status", token);

    public Task<DirectusServerInfo> GetServerInfoAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusServerInfo>("directus.serverInfo", token);

    public Task<DirectusCurrentUser> GetCurrentUserAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusCurrentUser>("directus.currentUser", token);

    public Task<DirectusCollectionList> ListCollectionsAsync(CancellationToken token)
        => InvokeEmptyAsync<DirectusCollectionList>("directus.collections", token);

    public Task<CreateTableResult> CreateTableAsync(
        string name, IReadOnlyList<FieldDefinition> fields, CancellationToken token)
        => _client.InvokeAsync<CreateTableParams, CreateTableResult>(
            "table_admin.createTable",
            new(name, fields is null ? Array.Empty<FieldDefinition>() : fields.ToArray()),
            token);

    public Task<DeleteTableResult> DeleteTableAsync(string name, CancellationToken token)
        => _client.InvokeAsync<DeleteTableParams, DeleteTableResult>(
            "table_admin.deleteTable", new(name), token);

    public Task<IdentifierMappingsResult> ListIdentifierMappingsAsync(
        string? search, CancellationToken token)
        => _client.InvokeAsync<ListIdentifierMappingsParams, IdentifierMappingsResult>(
            "table_admin.listIdentifierMappings", new(search), token);

    public Task<IdentifierMappingsResult> UpdateIdentifierAliasesAsync(
        string mappingId, IReadOnlyList<string> aliases, CancellationToken token)
        => _client.InvokeAsync<UpdateIdentifierAliasesParams, IdentifierMappingsResult>(
            "table_admin.updateIdentifierAliases", new(mappingId, aliases), token);

    public Task<IdentifierMappingsResult> ImportIdentifierMappingsAsync(
        IReadOnlyList<IdentifierMappingImportItem> mappings, CancellationToken token)
        => _client.InvokeAsync<ImportIdentifierMappingsParams, IdentifierMappingsResult>(
            "table_admin.importIdentifierMappings", new(mappings), token);

    public Task<IdentifierMappingsResult> ReconcileIdentifierMappingsAsync(CancellationToken token)
        => InvokeEmptyAsync<IdentifierMappingsResult>(
            "table_admin.reconcileIdentifierMappings", token);

    public Task<DirectusSchema> GetSchemaAsync(string collection, CancellationToken token)
        => _client.InvokeAsync<DirectusCollectionParams, DirectusSchema>(
            "directus.schema", new(collection), token);

    public Task<DirectusPage> ReadAsync(
        string collection, TableQuery query, bool includeArchived, CancellationToken token)
        => _client.InvokeAsync<DirectusReadParams, DirectusPage>(
            "directus.read", new(collection, query, includeArchived), token);

    public Task<DirectusItem> CreateAsync(
        string collection, IReadOnlyDictionary<string, object?> values,
        string? requestId, CancellationToken token)
        => _client.InvokeAsync<DirectusCreateParams, DirectusItem>(
            "directus.create", new(collection, values, requestId), token);

    public Task<DirectusItem> UpdateAsync(
        string collection, string itemId, IReadOnlyDictionary<string, object?> values,
        string? expectedDateUpdated, string? requestId, CancellationToken token)
        => _client.InvokeAsync<DirectusUpdateParams, DirectusItem>(
            "directus.update",
            new(collection, itemId, values, expectedDateUpdated, requestId), token);

    public Task<DirectusItem> ArchiveAsync(
        string collection, string itemId, CancellationToken token)
        => InvokeItemAsync("directus.archive", collection, itemId, token);

    public Task<DirectusItem> RestoreAsync(
        string collection, string itemId, CancellationToken token)
        => InvokeItemAsync("directus.restore", collection, itemId, token);

    public Task<DirectusItem> DeleteAsync(
        string collection, string itemId, CancellationToken token)
        => InvokeItemAsync("directus.delete", collection, itemId, token);

    public Task<DirectusSubscription> SubscribeAsync(
        string uid, string collection, IReadOnlyList<string> fields, CancellationToken token)
        => _client.InvokeAsync<DirectusSubscribeParams, DirectusSubscription>(
            "directus.subscribe", new(uid, collection, fields), token);

    public Task<DirectusSubscription> UnsubscribeAsync(string uid, CancellationToken token)
        => _client.InvokeAsync<DirectusUnsubscribeParams, DirectusSubscription>(
            "directus.unsubscribe", new(uid), token);

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        _client.NotificationReceived -= OnNotificationReceived;
    }

    private Task<TResult> InvokeEmptyAsync<TResult>(string method, CancellationToken token)
        => _client.InvokeAsync<DirectusEmptyParams, TResult>(method, new(), token);

    private Task<DirectusItem> InvokeItemAsync(
        string method, string collection, string itemId, CancellationToken token)
        => _client.InvokeAsync<DirectusItemParams, DirectusItem>(
            method, new(collection, itemId), token);

    private void OnNotificationReceived(string method, JsonElement parameters)
    {
        if (_disposed || !string.Equals(method, "directus.changed", StringComparison.Ordinal))
        {
            return;
        }
        var change = parameters.Deserialize<DirectusChange>(JsonOptions);
        if (change is not null)
        {
            Changed?.Invoke(change);
        }
    }
}
