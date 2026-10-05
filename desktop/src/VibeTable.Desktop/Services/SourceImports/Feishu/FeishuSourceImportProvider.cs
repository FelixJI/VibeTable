using System.Globalization;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Feishu implementation of the Host source import provider. Reads only the
/// explicitly selected tables through the verified official endpoints and maps
/// them onto the neutral #435 snapshot contract. Source field/record/option
/// identities (field_id, record_id, option id, file_token) are preserved as
/// stable IDs; display labels are never used as identities. The read is a
/// bounded per-page window, not a point-in-time snapshot.
/// Ownership contract: each provider exclusively owns the independent client
/// created for it by FeishuSourceImportConnection.CreateProvider. Dispose
/// cancels this provider's in-flight reads/downloads and closes that client
/// only; the wizard connection, other providers and providers already handed
/// to the Host registry are never affected.
/// </summary>
internal sealed class FeishuSourceImportProvider : IHostSourceImportProvider
{
    private readonly FeishuSourceImportClient _client;
    private readonly FeishuSourceImportCatalog _catalog;
    private readonly IReadOnlyList<FeishuSourceImportTableCatalog> _selected;
    private readonly CancellationTokenSource _lifetime = new();
    private int _disposed;

    internal FeishuSourceImportProvider(FeishuSourceImportClient client, FeishuSourceImportCatalog catalog,
        IReadOnlyList<FeishuSourceImportTableCatalog> selected)
    {
        _client = client;
        _catalog = catalog;
        _selected = selected;
    }

    internal string AppToken => _catalog.AppToken;

    public async Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        using var call = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token, token);
        CancellationToken cancel = call.Token;
        string startedAt = DateTimeOffset.UtcNow.ToString("O");
        FeishuAppWire app = await FeishuWireReader.ReadAppAsync(_client, _catalog.AppToken, cancel)
            .ConfigureAwait(false);
        List<FeishuTableWire> tables = (await FeishuWireReader.ListTablesAsync(_client, _catalog.AppToken, cancel)
            .ConfigureAwait(false)).Select(FeishuWireReader.ReadTable).ToList();
        var current = new Dictionary<string, FeishuTableWire>(StringComparer.Ordinal);
        foreach (FeishuTableWire table in tables) current[table.Id] = table;
        var mappedTables = new List<HostSourceImportTable>(_selected.Count);
        var attachments = new List<HostSourceImportAttachment>();
        foreach (FeishuSourceImportTableCatalog selected in _selected)
        {
            cancel.ThrowIfCancellationRequested();
            if (!current.TryGetValue(selected.TableId, out FeishuTableWire? fresh))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.NotFound,
                    $"来源表 {selected.TableId} 在当前授权范围内已不存在或不可见；请重新连接来源。");
            List<FeishuFieldWire> fields = (await FeishuWireReader.ListFieldsAsync(
                _client, _catalog.AppToken, selected.TableId, cancel).ConfigureAwait(false))
                .Select(FeishuWireReader.ReadField).ToList();
            mappedTables.Add(await BuildTableAsync(fresh, fields, attachments, cancel).ConfigureAwait(false));
        }
        string finishedAt = DateTimeOffset.UtcNow.ToString("O");
        string displayName = string.IsNullOrWhiteSpace(app.Name) ? "飞书多维表格" : app.Name;
        return new HostSourceImportSnapshot(FeishuSourceImportConnector.ProviderName, _catalog.AppToken,
            displayName, FeishuSourceImportCatalogFactory.TableVersion(app.Revision),
            new HostSourceImportReadWindow(startedAt, finishedAt, "window"),
            mappedTables.ToArray(), attachments.ToArray());
    }

    /// <summary>
    /// Cheap drift probe of the officially exposed signals: the app revision,
    /// per-table revisions, and — because Feishu does not officially guarantee
    /// that revisions bump on schema-only changes — a fresh field read for
    /// every selected table whose semantics feed the same non-hash schema
    /// version used at read time. A rename or option/cardinality change at an
    /// unchanged revision therefore still breaks the Host's comparison.
    /// </summary>
    public async Task<HostSourceImportObservation> ObserveAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        using var call = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token, token);
        CancellationToken cancel = call.Token;
        FeishuAppWire app = await FeishuWireReader.ReadAppAsync(_client, _catalog.AppToken, cancel)
            .ConfigureAwait(false);
        List<FeishuTableWire> tables = (await FeishuWireReader.ListTablesAsync(_client, _catalog.AppToken, cancel)
            .ConfigureAwait(false)).Select(FeishuWireReader.ReadTable).ToList();
        var selected = new HashSet<string>(_selected.Select(table => table.TableId), StringComparer.Ordinal);
        var versions = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (FeishuTableWire table in tables)
        {
            if (!selected.Contains(table.Id))
            {
                versions[table.Id] = FeishuSourceImportCatalogFactory.TableVersion(table.Revision);
                continue;
            }
            List<FeishuFieldWire> fields = (await FeishuWireReader.ListFieldsAsync(
                _client, _catalog.AppToken, table.Id, cancel).ConfigureAwait(false))
                .Select(FeishuWireReader.ReadField).ToList();
            versions[table.Id] = FeishuSourceImportSchemaVersion.TableVersion(table.Revision, fields);
        }
        return new HostSourceImportObservation(
            FeishuSourceImportCatalogFactory.TableVersion(app.Revision), versions);
    }

    /// <summary>
    /// Download attachment bytes only through the official media endpoint.
    /// Arbitrary URLs from record values are never fetched and redirects are
    /// refused, so Authorization can never travel to an unrelated host.
    /// </summary>
    public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        var call = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token, token);
        return DownloadAttachmentAsync(call, attachment);
    }

    private async Task<Stream> DownloadAttachmentAsync(
        CancellationTokenSource call, HostSourceImportAttachment attachment)
    {
        try
        {
            return await _client.DownloadAttachmentAsync(attachment.Id, attachment.Size, call.Token)
                .ConfigureAwait(false);
        }
        finally
        {
            call.Dispose();
        }
    }

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Disposed,
                "飞书来源会话已释放；请重新连接来源。");
    }

    public void Dispose()
    {
        // Cancels this provider's lifetime (stopping its in-flight external
        // reads) and closes its own independent client. The wizard connection
        // and any other provider keep their own sessions.
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _lifetime.Cancel();
        _lifetime.Dispose();
        _client.Dispose();
    }

    private async Task<HostSourceImportTable> BuildTableAsync(FeishuTableWire table, List<FeishuFieldWire> fields,
        List<HostSourceImportAttachment> attachments, CancellationToken cancel)
    {
        string[] primaries = fields.Where(field => field.IsPrimary).Select(field => field.Id).ToArray();
        if (primaries.Length != 1)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                $"来源表 {table.Name}（{table.Id}）缺少唯一主字段；本次读取不完整，已停止。");
        // Record values are keyed by field name; a stable name->ID mapping must
        // be unique, otherwise identities cannot be established and the read
        // is rejected instead of guessed.
        var byName = new Dictionary<string, FeishuFieldWire>(StringComparer.Ordinal);
        foreach (FeishuFieldWire field in fields)
        {
            if (field.Name.Length == 0 || !byName.TryAdd(field.Name, field))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                    $"来源表 {table.Id} 的字段名缺失或重复（{field.Name}）；字段名到 ID 的映射不唯一，已停止读取。");
        }
        var mappedFields = new List<HostSourceImportField>(fields.Count);
        foreach (FeishuFieldWire field in fields)
            mappedFields.Add(FeishuSourceImportFieldMapping.ToHostField(field));
        var records = new List<HostSourceImportRecord>();
        foreach (JsonElement item in await FeishuWireReader.ListRecordsAsync(
            _client, _catalog.AppToken, table.Id, cancel).ConfigureAwait(false))
        {
            cancel.ThrowIfCancellationRequested();
            records.Add(FeishuSourceImportFieldMapping.ToHostRecord(
                item, table.Id, byName, attachments));
        }
        return new HostSourceImportTable(table.Id, table.Name,
            FeishuSourceImportSchemaVersion.TableVersion(table.Revision, fields), primaries[0],
            mappedFields.ToArray(), records.ToArray());
    }
}

/// <summary>
/// Mapping between verified Feishu field wire shapes and the neutral #435
/// source contract. Types without a verified executable conversion degrade to
/// explicit snapshot/skip kinds; they are never silently mapped to text.
/// </summary>
internal static class FeishuSourceImportFieldMapping
{
    private static readonly IReadOnlyDictionary<int, (string Kind, string ValueKind)> Types =
        new Dictionary<int, (string, string)>
        {
            [1] = ("text", "text"),           // 文本
            [2] = ("number", "number"),       // 数字
            [3] = ("select", "select"),       // 单选
            [4] = ("multiSelect", "multiSelect"), // 多选
            [5] = ("dateTime", "dateTime"),   // 日期
            [7] = ("bool", "bool"),           // 复选框
            [11] = ("person", "json"),        // 人员：仅快照
            [13] = ("text", "text"),          // 电话：按文本
            [15] = ("url", "url"),            // 超链接
            [17] = ("file", "file"),          // 附件
            [18] = ("relation", "relation"),  // 单向关联（单值）
            [20] = ("lookup", "json"),        // 引用：仅快照
            [21] = ("formula", "json"),       // 公式：仅快照
            [22] = ("relation", "relation"),  // 双向关联（多值）
            [23] = ("unknown", "json"),       // 地理位置：未验证转换
            [1001] = ("system", "json"),      // 创建时间等系统字段：仅快照
            [1002] = ("system", "json"),
            [1003] = ("system", "json"),
            [1004] = ("system", "json"),
            [1005] = ("system", "json"),
        };

    internal static string KindOf(FeishuFieldWire field) => field.Type switch
    {
        // 18/22 encode link direction (单向/双向), not cardinality. The
        // official cardinality signal is property.multiple; without it the
        // field degrades to unknown and forces snapshot/skip at preflight.
        18 or 22 => LinkCardinality(field) is null ? "unknown" : "relation",
        _ => Types.TryGetValue(field.Type, out (string Kind, string _) mapped) ? mapped.Kind : "unknown",
    };

    internal static string ValueKindOf(FeishuFieldWire field) => field.Type switch
    {
        // An unverifiable link cardinality degrades the value kind too: raw
        // values must snapshot as JSON, never pretend to be relations.
        18 or 22 => LinkCardinality(field) is null ? "json" : "relation",
        _ => Types.TryGetValue(field.Type, out (string _, string ValueKind) mapped) ? mapped.ValueKind : "json",
    };

    internal static HostSourceImportField ToHostField(FeishuFieldWire field)
    {
        string kind = KindOf(field);
        HostSourceImportOption[] options = kind is "select" or "multiSelect"
            ? ReadOptions(field) : [];
        HostSourceImportRelation? relation = kind == "relation" ? ReadRelation(field) : null;
        // Required=false: Feishu has no per-field insert-required constraint
        // and the local contract must not invent one. Definition carries the
        // official auditable semantics (formula expression, lookup filter) so
        // snapshot provenance survives into the durable result; it is never
        // truncated here — the Go preflight's 512KiB provenance budget stays
        // the sole, explicit limiter.
        return new HostSourceImportField(field.Id, field.Name, kind, ValueKindOf(field), false,
            options, relation, null, "", ReadDefinition(field));
    }

    private static readonly JsonSerializerOptions DefinitionWire = new(JsonSerializerDefaults.Web);

    /// <summary>
    /// Project the officially exposed, auditable field semantics into the
    /// Definition provenance: formula_expression for formulas (21) and the
    /// lookup filter_info (target_table plus nested conditions, verified in
    /// the SDK models AppTableFieldProperty/LookupFilter) for lookups (20).
    /// Only official definition fields are included — never credentials or
    /// temporary download URLs — and nothing is invented for other kinds.
    /// </summary>
    private static string ReadDefinition(FeishuFieldWire field)
    {
        if (field.Property.ValueKind != JsonValueKind.Object) return "";
        switch (field.Type)
        {
            case 21:
                if (field.Property.TryGetProperty("formula_expression", out JsonElement expression)
                    && expression.ValueKind == JsonValueKind.String)
                    return JsonSerializer.Serialize(
                        new Dictionary<string, object?> { ["formulaExpression"] = expression.GetString() },
                        DefinitionWire);
                return "";
            case 20:
                if (field.Property.TryGetProperty("filter_info", out JsonElement filter)
                    && filter.ValueKind == JsonValueKind.Object)
                    return JsonSerializer.Serialize(
                        new Dictionary<string, object?> { ["lookupFilter"] = filter.Clone() },
                        DefinitionWire);
                return "";
            default:
                return "";
        }
    }

    private static HostSourceImportOption[] ReadOptions(FeishuFieldWire field)
    {
        if (field.Property.ValueKind != JsonValueKind.Object
            || !field.Property.TryGetProperty("options", out JsonElement options)
            || options.ValueKind != JsonValueKind.Array)
            return [];
        var result = new List<HostSourceImportOption>();
        foreach (JsonElement option in options.EnumerateArray())
        {
            if (option.ValueKind != JsonValueKind.Object) continue;
            string id = option.TryGetProperty("id", out JsonElement idElement)
                && idElement.ValueKind == JsonValueKind.String ? idElement.GetString() ?? "" : "";
            string name = option.TryGetProperty("name", out JsonElement nameElement)
                && nameElement.ValueKind == JsonValueKind.String ? nameElement.GetString() ?? "" : "";
            // Incomplete option identities are omitted; the Go preflight then
            // blocks native/select-snapshot strategies for this field instead
            // of silently treating labels as identities.
            if (id.Length > 0 && name.Length > 0)
                result.Add(new HostSourceImportOption(id, name, ""));
        }
        return result.ToArray();
    }

    /// <summary>
    /// Official link cardinality from property.multiple (verified in the SDK
    /// model AppTableFieldProperty): true = many, false = one, absent =
    /// unverifiable (null). Link direction (type 18/22) is never used as
    /// cardinality.
    /// </summary>
    internal static string? LinkCardinality(FeishuFieldWire field)
    {
        if (field.Property.ValueKind != JsonValueKind.Object
            || !field.Property.TryGetProperty("multiple", out JsonElement multiple)) return null;
        return multiple.ValueKind switch
        {
            JsonValueKind.True => "many",
            JsonValueKind.False => "one",
            _ => null,
        };
    }

    private static HostSourceImportRelation? ReadRelation(FeishuFieldWire field)
    {
        string targetTable = field.Property.ValueKind == JsonValueKind.Object
            && field.Property.TryGetProperty("table_id", out JsonElement target)
            && target.ValueKind == JsonValueKind.String ? target.GetString()! : "";
        // The field list exposes only the target table (and a reverse field
        // NAME, which is not an identity), so relations stay one-sided with an
        // empty TargetFieldId and require the explicit reverse confirmation.
        return new HostSourceImportRelation(targetTable, "", LinkCardinality(field)!);
    }

    internal static HostSourceImportRecord ToHostRecord(JsonElement item, string tableId,
        Dictionary<string, FeishuFieldWire> byName, List<HostSourceImportAttachment> attachments)
    {
        if (item.ValueKind != JsonValueKind.Object
            || !item.TryGetProperty("record_id", out JsonElement idElement)
            || idElement.ValueKind != JsonValueKind.String
            || idElement.GetString() is not { Length: > 0 } recordId)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                $"来源表 {tableId} 存在缺少 record_id 的记录；读取不完整，已停止。");
        if (!item.TryGetProperty("fields", out JsonElement cells) || cells.ValueKind != JsonValueKind.Object)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                $"来源表 {tableId} 记录 {recordId} 缺少 fields 对象；读取不完整，已停止。");
        var values = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        foreach (JsonProperty cell in cells.EnumerateObject())
        {
            if (cell.Value.ValueKind is JsonValueKind.Null or JsonValueKind.Undefined) continue;
            if (!byName.TryGetValue(cell.Name, out FeishuFieldWire? field))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                    $"来源表 {tableId} 记录 {recordId} 包含未声明字段“{cell.Name}”；字段名到 ID 不一致，已停止读取。");
            if (ConvertValue(field, cell.Value, tableId, recordId, attachments) is { } converted)
                values[field.Id] = converted;
        }
        return new HostSourceImportRecord(recordId, values);
    }

    private static JsonElement? ConvertValue(FeishuFieldWire field, JsonElement value, string tableId,
        string recordId, List<HostSourceImportAttachment> attachments)
    {
        switch (KindOf(field))
        {
            case "text":
                return RequireString(value, field, tableId, recordId);
            case "number":
                if (value.ValueKind != JsonValueKind.Number)
                    throw ValueShape(field, tableId, recordId);
                // Raw JSON text is preserved so cloud numeric precision rides
                // through as an exact JSON number into the Go kernel.
                return value.Clone();
            case "bool":
                if (value.ValueKind is not (JsonValueKind.True or JsonValueKind.False))
                    throw ValueShape(field, tableId, recordId);
                return value.Clone();
            case "select":
                return ResolveOption(value, field);
            case "multiSelect":
            {
                if (value.ValueKind != JsonValueKind.Array) throw ValueShape(field, tableId, recordId);
                return JsonSerializer.SerializeToElement(value.EnumerateArray()
                    .Select(item => ResolveOption(item, field)).ToArray());
            }
            case "dateTime":
            {
                if (value.ValueKind != JsonValueKind.Number || !value.TryGetInt64(out long millis))
                    throw ValueShape(field, tableId, recordId);
                try
                {
                    return JsonSerializer.SerializeToElement(
                        DateTimeOffset.FromUnixTimeMilliseconds(millis).UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                }
                catch (ArgumentOutOfRangeException)
                {
                    throw ValueShape(field, tableId, recordId);
                }
            }
            case "url":
            {
                if (value.ValueKind == JsonValueKind.String) return value.Clone();
                if (value.ValueKind == JsonValueKind.Object
                    && value.TryGetProperty("link", out JsonElement link) && link.ValueKind == JsonValueKind.String)
                    return link.Clone();
                if (value.ValueKind == JsonValueKind.Object
                    && value.TryGetProperty("text", out JsonElement text) && text.ValueKind == JsonValueKind.String)
                    return text.Clone();
                throw ValueShape(field, tableId, recordId);
            }
            case "relation" when LinkCardinality(field) == "one":
            {
                if (value.ValueKind == JsonValueKind.String) return value.Clone();
                if (value.ValueKind == JsonValueKind.Array)
                {
                    // A single-cardinality link holds at most one record ID;
                    // more than one is a real shape mismatch and is never
                    // silently truncated to the first item.
                    if (value.GetArrayLength() == 0) return null;
                    if (value.GetArrayLength() == 1 && value[0].ValueKind == JsonValueKind.String)
                        return value[0].Clone();
                }
                throw ValueShape(field, tableId, recordId);
            }
            case "relation":
            {
                // Empty arrays are legitimate empty multi-value links.
                if (value.ValueKind != JsonValueKind.Array
                    || value.EnumerateArray().Any(item => item.ValueKind != JsonValueKind.String))
                    throw ValueShape(field, tableId, recordId);
                return value.Clone();
            }
            case "file":
            {
                if (value.ValueKind != JsonValueKind.Array) throw ValueShape(field, tableId, recordId);
                var tokens = new List<string>();
                foreach (JsonElement file in value.EnumerateArray())
                {
                    if (file.ValueKind != JsonValueKind.Object
                        || !file.TryGetProperty("file_token", out JsonElement token)
                        || token.ValueKind != JsonValueKind.String)
                        throw ValueShape(field, tableId, recordId);
                    string fileToken = token.GetString()!;
                    tokens.Add(fileToken);
                    // Metadata rides along when complete; incomplete entries
                    // stay value-only so the Go preflight reports them as
                    // non-migrated instead of the read silently succeeding.
                    if (FeishuSourceImportClient.IsValidToken(fileToken)
                        && file.TryGetProperty("name", out JsonElement name) && name.ValueKind == JsonValueKind.String
                        && file.TryGetProperty("type", out JsonElement type) && type.ValueKind == JsonValueKind.String
                        && file.TryGetProperty("size", out JsonElement size)
                        && size.ValueKind == JsonValueKind.Number && size.TryGetInt64(out long bytes) && bytes > 0)
                        attachments.Add(new HostSourceImportAttachment(fileToken, tableId, recordId,
                            field.Id, name.GetString()!, type.GetString()!, bytes));
                }
                return JsonSerializer.SerializeToElement(tokens);
            }
            default:
                // person/system/formula/lookup/unknown: faithful raw snapshot
                // values; executable conversion stays an explicit user choice.
                return value.Clone();
        }
    }

    /// <summary>
    /// Resolve a select label to its stable source option ID. When the option
    /// identity table is incomplete or the label is unknown, the raw value is
    /// preserved: native and select-snapshot strategies are then blocked by
    /// the Go preflight (labels are never silently identities), while text/JSON
    /// snapshots stay available.
    /// </summary>
    private static JsonElement ResolveOption(JsonElement value, FeishuFieldWire field)
    {
        if (value.ValueKind != JsonValueKind.String)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                $"来源字段 {field.Name}（{field.Id}）的选项值不是字符串；读取不完整，已停止。");
        string label = value.GetString()!;
        if (field.Property.ValueKind != JsonValueKind.Object
            || !field.Property.TryGetProperty("options", out JsonElement options)
            || options.ValueKind != JsonValueKind.Array)
            return value.Clone();
        string? resolved = null;
        bool ambiguous = false;
        foreach (JsonElement option in options.EnumerateArray())
        {
            if (option.ValueKind != JsonValueKind.Object) continue;
            if (!option.TryGetProperty("id", out JsonElement idElement)
                || idElement.ValueKind != JsonValueKind.String
                || idElement.GetString() is not { Length: > 0 }
                || !option.TryGetProperty("name", out JsonElement nameElement)
                || nameElement.ValueKind != JsonValueKind.String) continue;
            if (nameElement.GetString() != label) continue;
            if (resolved is not null) ambiguous = true;
            else resolved = idElement.GetString();
        }
        return resolved is null || ambiguous ? value.Clone() : JsonSerializer.SerializeToElement(resolved);
    }

    private static JsonElement RequireString(JsonElement value, FeishuFieldWire field, string tableId, string recordId)
    {
        if (value.ValueKind != JsonValueKind.String) throw ValueShape(field, tableId, recordId);
        return value.Clone();
    }

    private static FeishuSourceImportException ValueShape(FeishuFieldWire field, string tableId, string recordId) =>
        new(FeishuSourceImportErrorKind.Protocol,
            $"来源字段 {field.Name}（{field.Id}）在表 {tableId} 记录 {recordId} 中的值形状与已验证类型不符；读取不完整，已停止。");
}

/// <summary>
/// Deterministic, explicitly-encoded (non-hash) table schema version built
/// from the officially exposed field semantics: field identity, name, type,
/// ui type, primary/hidden flags, the complete option identity table in
/// declaration order, and the link target plus official multiple cardinality.
/// Feishu does not officially guarantee that revisions bump on schema-only
/// changes (e.g. a rename), so Read and Observe both compute this string from
/// freshly read fields; any observable semantic change therefore breaks the
/// Host's version comparison even at an unchanged revision.
/// </summary>
internal static class FeishuSourceImportSchemaVersion
{
    internal static string TableVersion(long revision, IReadOnlyList<FeishuFieldWire> fields)
    {
        static string Clean(string value) =>
            value.Replace('\n', ' ').Replace('\r', ' ').Replace('\t', ' ');
        IEnumerable<string> segments = fields
            .OrderBy(field => field.Id, StringComparer.Ordinal)
            .Select(field => string.Join("\t", field.Id, Clean(field.Name),
                field.Type.ToString(CultureInfo.InvariantCulture), Clean(field.UiType),
                field.IsPrimary ? "1" : "0", field.IsHidden ? "1" : "0",
                OptionIdentity(field), LinkIdentity(field)));
        string primary = fields.FirstOrDefault(field => field.IsPrimary)?.Id ?? "-";
        return "rev-" + revision.ToString(CultureInfo.InvariantCulture)
            + "|primary=" + primary + "|" + string.Join("\n", segments);
    }

    private static string OptionIdentity(FeishuFieldWire field)
    {
        if (field.Property.ValueKind != JsonValueKind.Object
            || !field.Property.TryGetProperty("options", out JsonElement options)
            || options.ValueKind != JsonValueKind.Array)
            return "";
        var identities = new List<string>();
        foreach (JsonElement option in options.EnumerateArray())
        {
            if (option.ValueKind != JsonValueKind.Object
                || !option.TryGetProperty("id", out JsonElement id)
                || id.ValueKind != JsonValueKind.String
                || !option.TryGetProperty("name", out JsonElement name)
                || name.ValueKind != JsonValueKind.String) continue;
            identities.Add(id.GetString() + ":" + name.GetString());
        }
        return string.Join(",", identities);
    }

    private static string LinkIdentity(FeishuFieldWire field)
    {
        if (field.Type is not (18 or 22)) return "";
        string target = field.Property.ValueKind == JsonValueKind.Object
            && field.Property.TryGetProperty("table_id", out JsonElement tableId)
            && tableId.ValueKind == JsonValueKind.String ? tableId.GetString()! : "";
        return target + ":" + (FeishuSourceImportFieldMapping.LinkCardinality(field) ?? "unverified");
    }
}

/// <summary>
/// Recommended default strategy per raw source kind, for the Host-native
/// wizard. Values mirror the Go #435 policies; the Go preflight remains the
/// authority — these hints never bypass its validation.
/// </summary>
internal sealed record FeishuSourceImportFieldStrategy(
    string Policy, string SnapshotValueKind, bool ConfirmationRequired, string Reason);

internal static class FeishuSourceImportFieldPolicy
{
    internal static FeishuSourceImportFieldStrategy Recommend(string kind) => kind switch
    {
        "text" or "number" or "bool" or "dateTime" or "url" => new("native", "", false,
            "已验证的值转换，可原生迁移。"),
        "select" or "multiSelect" => new("native", "", false,
            "选项按来源选项 ID 映射；仅当来源选项身份表完整时可原生迁移。"),
        "relation" => new("native", "", false,
            "按来源记录 ID 迁移；单向来源关系需要确认本地新增反向字段。"),
        "file" => new("native", "", false,
            "附件元数据先预检，字节经 Host 受控下载后迁移。"),
        "person" or "system" or "unknown" => new("snapshot", "json", true,
            "本版本未验证该来源类型的可执行转换；需确认以原始值快照迁移或跳过。"),
        "formula" or "lookup" => new("snapshot", "json", true,
            "公式/引用不承诺表达式迁移；需确认以计算结果快照迁移或跳过。"),
        _ => new("snapshot", "json", true, "未知来源类型；需确认快照或跳过。"),
    };
}
