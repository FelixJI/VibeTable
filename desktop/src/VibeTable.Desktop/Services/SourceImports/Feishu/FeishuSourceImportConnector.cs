using System.Net.Http;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Host entry for the Feishu source connector. <see cref="ConnectAsync"/>
/// is the connection test: it validates an explicit app_token or a Base/Wiki
/// link, resolves Wiki containers only through the official wiki get_node
/// endpoint (never by treating a node token as an app token), and reads the
/// table/field catalog. <see cref="CreateProvider"/> then builds the
/// table-selection provider for the Host import flow.
/// </summary>
internal static class FeishuSourceImportConnector
{
    internal const string ProviderName = "feishu";
    internal const string WikiObjectType = "bitable";

    /// <summary>Current authorization scope, surfaced to the Host UI/reports.</summary>
    internal const string AuthorizationScope =
        "仅以当前授权范围只读访问：多维表格元数据与记录（bitable v1 读取）、Wiki 节点到多维表格的官方解析"
        + "（wiki v2 get_node）与附件字节下载（drive v1 medias）。不包含源端写入、通讯录全量读取或视图克隆；"
        + "读取按分页窗口进行，不是时点快照，超出当前授权可见范围的数据无法读取也不会被标记为空数据。";

    /// <summary>
    /// Parse a user source input. Accepted forms: an explicit app_token, an
    /// https Base link on a *.feishu.cn host, or an https Wiki link which is
    /// resolved through the official endpoint during connect. International
    /// larksuite hosts are explicitly out of scope.
    /// </summary>
    internal static FeishuSourceReference ParseSource(string input)
    {
        input = input.Trim();
        if (input.Length == 0 || input.Length > 2048)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "请输入飞书多维表格的 Base 链接（https://…/base/…）、Wiki 链接（https://…/wiki/…）或明确的 app_token。");
        bool looksLikeUrl = input.StartsWith("http://", StringComparison.OrdinalIgnoreCase)
            || input.StartsWith("https://", StringComparison.OrdinalIgnoreCase);
        if (!looksLikeUrl)
        {
            if (input.Contains('/', StringComparison.Ordinal) || input.Contains(' ', StringComparison.Ordinal)
                || input.Contains('@', StringComparison.Ordinal))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                    "输入既不是有效的 https 链接也不是明确的 app_token；请检查来源地址。");
            if (!FeishuSourceImportClient.IsValidToken(input))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                    "app_token 格式无效；请复制多维表格的 Base 链接或明确 app_token。");
            return new FeishuSourceReference(input, FeishuSourceReferenceKind.AppToken);
        }
        if (!Uri.TryCreate(input, UriKind.Absolute, out Uri? uri))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "链接格式无效；请使用 https:// 开头的飞书链接。");
        if (uri.Scheme != Uri.UriSchemeHttps)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "仅支持 https 链接；http 链接已拒绝。");
        if (uri.Host.EndsWith(".larksuite.com", StringComparison.OrdinalIgnoreCase))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "仅支持国内飞书（*.feishu.cn）；国际版（larksuite.com）端点未经验证，已拒绝。");
        if (uri.Host != "feishu.cn" && !uri.Host.EndsWith(".feishu.cn", StringComparison.OrdinalIgnoreCase))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "链接主机不是国内飞书域名（*.feishu.cn）；请核对后重试。");
        string[] segments = uri.AbsolutePath.Split('/', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        return segments switch
        {
            ["base", string token] when FeishuSourceImportClient.IsValidToken(token) =>
                new FeishuSourceReference(token, FeishuSourceReferenceKind.BaseLink),
            ["wiki", string token] when FeishuSourceImportClient.IsValidToken(token) =>
                new FeishuSourceReference(token, FeishuSourceReferenceKind.WikiNode),
            ["wiki", _, string token] when FeishuSourceImportClient.IsValidToken(token) =>
                new FeishuSourceReference(token, FeishuSourceReferenceKind.WikiNode),
            _ => throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "链接不是可识别的多维表格地址。请使用 /base/<app_token> 链接、/wiki/<node_token> 链接，或直接输入明确的 app_token。"),
        };
    }

    /// <summary>
    /// Connection test + catalog read. Throws categorized exceptions for
    /// invalid input, auth failure, permission denial, missing app, rate
    /// limiting and protocol violations.
    /// </summary>
    internal static async Task<FeishuSourceImportConnection> ConnectAsync(
        string sourceInput, string accessToken, HttpMessageHandler? handler = null,
        TimeSpan? retryDelay = null, CancellationToken token = default, TimeSpan? requestTimeout = null)
    {
        FeishuSourceReference source = ParseSource(sourceInput);
        // Every client instance is an independent credential-bearing session.
        // The connection keeps one only for its own catalog reads; each
        // CreateProvider call builds a fresh client so provider lifetimes and
        // the wizard window never share a closable HTTP session.
        FeishuSourceImportClient CreateClient() => new(accessToken, handler, retryDelay, requestTimeout: requestTimeout);
        FeishuSourceImportClient client = CreateClient();
        try
        {
            string appToken = source.Kind == FeishuSourceReferenceKind.WikiNode
                ? await ResolveWikiAppTokenAsync(client, source.Token, token).ConfigureAwait(false)
                : source.Token;
            FeishuAppWire app = await FeishuWireReader.ReadAppAsync(client, appToken, token).ConfigureAwait(false);
            List<FeishuTableWire> tables = (await FeishuWireReader.ListTablesAsync(client, appToken, token)
                .ConfigureAwait(false)).Select(FeishuWireReader.ReadTable).ToList();
            var catalogTables = new List<FeishuSourceImportTableCatalog>(tables.Count);
            foreach (FeishuTableWire table in tables)
            {
                List<FeishuFieldWire> fields = (await FeishuWireReader.ListFieldsAsync(client, appToken, table.Id, token)
                    .ConfigureAwait(false)).Select(FeishuWireReader.ReadField).ToList();
                catalogTables.Add(FeishuSourceImportCatalogFactory.BuildTable(table, fields));
            }
            return new FeishuSourceImportConnection(client,
                new FeishuSourceImportCatalog(appToken, app.Name, app.Revision, catalogTables, AuthorizationScope),
                CreateClient);
        }
        catch
        {
            client.Dispose();
            throw;
        }
    }

    /// <summary>
    /// Resolve a Wiki node token through GET /open-apis/wiki/v2/spaces/get_node
    /// ?token=&amp;obj_type=wiki. The returned node.obj_token is the only
    /// accepted app token; non-bitable objects are rejected explicitly.
    /// </summary>
    private static async Task<string> ResolveWikiAppTokenAsync(
        FeishuSourceImportClient client, string nodeToken, CancellationToken token)
    {
        JsonElement data = await client.GetDataAsync(
            "/open-apis/wiki/v2/spaces/get_node?token=" + Uri.EscapeDataString(nodeToken)
            + "&obj_type=wiki", token).ConfigureAwait(false);
        if (!data.TryGetProperty("node", out JsonElement node) || node.ValueKind != JsonValueKind.Object
            || !node.TryGetProperty("obj_token", out JsonElement objToken)
            || objToken.ValueKind != JsonValueKind.String)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "Wiki 节点解析响应缺少 obj_token；请改为使用多维表格自身的 /base/ 链接或明确 app_token。");
        if (!node.TryGetProperty("obj_type", out JsonElement objType)
            || objType.ValueKind != JsonValueKind.String
            || objType.GetString() != WikiObjectType)
            // Fixed wording only: the raw obj_type is server-controlled text
            // and never crosses into exceptions or reports.
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "该 Wiki 节点不是多维表格；请在飞书中打开对应多维表格并使用其 /base/ 链接，不能把节点 token 当作 app_token。");
        string resolved = objToken.GetString()!;
        if (!FeishuSourceImportClient.IsValidToken(resolved))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "Wiki 节点解析出的 obj_token 无效；请改用多维表格 /base/ 链接。");
        return resolved;
    }
}

internal enum FeishuSourceReferenceKind { AppToken, BaseLink, WikiNode }

internal readonly record struct FeishuSourceReference(string Token, FeishuSourceReferenceKind Kind);

/// <summary>Verified wire shapes (subset actually consumed).</summary>
internal sealed record FeishuAppWire(string AppToken, string Name, long Revision);
internal sealed record FeishuTableWire(string Id, string Name, long Revision);
internal sealed record FeishuFieldWire(string Id, string Name, int Type, string UiType,
    bool IsPrimary, bool IsHidden, JsonElement Property);

internal static class FeishuWireReader
{
    internal static async Task<FeishuAppWire> ReadAppAsync(
        FeishuSourceImportClient client, string appToken, CancellationToken token)
    {
        JsonElement data = await client.GetDataAsync(
            "/open-apis/bitable/v1/apps/" + Uri.EscapeDataString(appToken), token).ConfigureAwait(false);
        if (!data.TryGetProperty("app", out JsonElement app) || app.ValueKind != JsonValueKind.Object)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "飞书应用信息响应缺少 app 对象；请稍后重试或核对 app_token。");
        return new FeishuAppWire(appToken,
            app.TryGetProperty("name", out JsonElement name) && name.ValueKind == JsonValueKind.String
                ? name.GetString() ?? "" : "",
            app.TryGetProperty("revision", out JsonElement revision)
                && revision.ValueKind == JsonValueKind.Number && revision.TryGetInt64(out long value) ? value : 0);
    }

    internal static Task<List<JsonElement>> ListTablesAsync(
        FeishuSourceImportClient client, string appToken, CancellationToken token) =>
        client.ListAllAsync("/open-apis/bitable/v1/apps/" + Uri.EscapeDataString(appToken) + "/tables",
            "table_id", 100, 100, token);

    internal static Task<List<JsonElement>> ListFieldsAsync(
        FeishuSourceImportClient client, string appToken, string tableId, CancellationToken token) =>
        client.ListAllAsync("/open-apis/bitable/v1/apps/" + Uri.EscapeDataString(appToken)
            + "/tables/" + Uri.EscapeDataString(tableId) + "/fields", "field_id", 100, 512, token);

    internal static Task<List<JsonElement>> ListRecordsAsync(
        FeishuSourceImportClient client, string appToken, string tableId, CancellationToken token) =>
        client.ListAllAsync("/open-apis/bitable/v1/apps/" + Uri.EscapeDataString(appToken)
            + "/tables/" + Uri.EscapeDataString(tableId) + "/records", "record_id", 500, 50_000, token);

    internal static FeishuTableWire ReadTable(JsonElement item) => new(
        item.GetProperty("table_id").GetString()!,
        item.TryGetProperty("name", out JsonElement name) && name.ValueKind == JsonValueKind.String
            ? name.GetString() ?? "" : "",
        item.TryGetProperty("revision", out JsonElement revision)
            && revision.ValueKind == JsonValueKind.Number && revision.TryGetInt64(out long value) ? value : 0);

    internal static FeishuFieldWire ReadField(JsonElement item) => new(
        item.GetProperty("field_id").GetString()!,
        item.TryGetProperty("field_name", out JsonElement name) && name.ValueKind == JsonValueKind.String
            ? name.GetString() ?? "" : "",
        item.TryGetProperty("type", out JsonElement type) && type.ValueKind == JsonValueKind.Number
            && type.TryGetInt32(out int typeValue) ? typeValue : 0,
        item.TryGetProperty("ui_type", out JsonElement ui) && ui.ValueKind == JsonValueKind.String
            ? ui.GetString() ?? "" : "",
        item.TryGetProperty("is_primary", out JsonElement primary) && primary.ValueKind is JsonValueKind.True,
        item.TryGetProperty("is_hidden", out JsonElement hidden) && hidden.ValueKind is JsonValueKind.True,
        item.TryGetProperty("property", out JsonElement property) && property.ValueKind == JsonValueKind.Object
            ? property.Clone() : default);
}

/// <summary>Catalog handed to the Host selection UI: app identity plus every
/// readable table with its fields and neutral kind hints.</summary>
internal sealed record FeishuSourceImportCatalog(string AppToken, string AppName, long AppRevision,
    IReadOnlyList<FeishuSourceImportTableCatalog> Tables, string AuthorizationScope);

internal sealed record FeishuSourceImportTableCatalog(string TableId, string Name, string Revision,
    string PrimaryFieldId, IReadOnlyList<FeishuSourceImportFieldCatalog> Fields);

internal sealed record FeishuSourceImportFieldCatalog(string FieldId, string Name, int Type, string UiType,
    bool IsPrimary, bool IsHidden, string Kind, string ValueKind);

internal static class FeishuSourceImportCatalogFactory
{
    internal static FeishuSourceImportTableCatalog BuildTable(FeishuTableWire table, List<FeishuFieldWire> fields)
    {
        string[] primaries = fields.Where(field => field.IsPrimary).Select(field => field.Id).ToArray();
        if (primaries.Length != 1)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                $"来源表 {table.Name}（{table.Id}）缺少唯一主字段；本次读取不完整，已停止。");
        return new FeishuSourceImportTableCatalog(table.Id, table.Name, TableVersion(table.Revision), primaries[0],
            fields.Select(field => new FeishuSourceImportFieldCatalog(field.Id, field.Name, field.Type, field.UiType,
                field.IsPrimary, field.IsHidden, FeishuSourceImportFieldMapping.KindOf(field),
                FeishuSourceImportFieldMapping.ValueKindOf(field))).ToArray());
    }

    internal static string TableVersion(long revision) => "rev-" + revision.ToString(System.Globalization.CultureInfo.InvariantCulture);
}

/// <summary>
/// A live connection to one Feishu base. Ownership contract: the connection
/// owns exactly one catalog-time HTTP client; every CreateProvider call
/// builds an independent controlled client for that provider. Therefore one
/// wizard window can preflight repeatedly (disposing an earlier provider
/// never affects later providers), and providers already handed to the Host
/// registry keep working after the connection itself is closed. Providers
/// that were never handed over are discarded by their own Dispose.
/// Credentials stay in Host memory inside each client. Injected
/// HttpMessageHandler instances (tests) are never disposed by any client.
/// There is intentionally no shared or cached client between connection and
/// providers.
/// </summary>
internal sealed class FeishuSourceImportConnection : IDisposable
{
    private readonly FeishuSourceImportClient _catalogClient;
    private readonly Func<FeishuSourceImportClient>? _clientFactory;
    private int _disposed;

    internal FeishuSourceImportCatalog Catalog { get; }

    internal FeishuSourceImportConnection(FeishuSourceImportClient catalogClient,
        FeishuSourceImportCatalog catalog, Func<FeishuSourceImportClient>? clientFactory = null)
    {
        _catalogClient = catalogClient;
        Catalog = catalog;
        _clientFactory = clientFactory;
    }

    /// <summary>
    /// Build the Host provider for an explicit table selection. Selection is
    /// by stable table ID only; unknown or duplicated IDs are rejected. The
    /// returned provider owns a fresh independent client.
    /// </summary>
    internal FeishuSourceImportProvider CreateProvider(IReadOnlyCollection<string> selectedTableIds)
    {
        ObjectDisposedException.ThrowIf(_disposed != 0, this);
        if (_clientFactory is null)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "连接未配置独立客户端工厂，无法创建迁移 provider。");
        if (selectedTableIds.Count == 0)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput, "请至少选择一张来源表。");
        var selected = new List<FeishuSourceImportTableCatalog>(selectedTableIds.Count);
        HashSet<string> seen = new(StringComparer.Ordinal);
        foreach (string id in selectedTableIds)
        {
            if (!seen.Add(id))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                    $"来源表重复选择：{id}。");
            FeishuSourceImportTableCatalog? match = Catalog.Tables.FirstOrDefault(table => table.TableId == id);
            if (match is null)
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                    $"所选来源表不存在于当前连接：{id}；请重新读取目录。");
            selected.Add(match);
        }
        return new FeishuSourceImportProvider(_clientFactory(), Catalog, selected);
    }

    public void Dispose()
    {
        // Ends only the connection's own catalog client. Providers already
        // created (and possibly handed to the Host registry) own their own
        // clients and are deliberately untouched.
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _catalogClient.Dispose();
    }
}
