using System.Globalization;
using System.IO;
using System.Net.Http;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// WPS 365 多维表格只读来源适配器，对接 #435 的 IHostSourceImportProvider 契约。
///
/// 官方端点证据（均要求 kso.dbsheet.read 或 readwrite、应用/用户授权之一）：
/// - GET  https://openapi.wps.cn/v7/coop/dbsheet/{file_id}/schema
///       （数据表清单：id/name/primary_field_id/fields/views）
/// - POST https://openapi.wps.cn/v7/coop/dbsheet/{file_id}/sheets/{sheet_id}/records
///       （prefer_id=true 按字段 ID；records[].fields 为 JSON 字符串；page_token 游标分页；
///        page_size 1-1000；code!=0 为业务失败）
/// 文档：https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/get-schema 、
///       https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/records/list-record
///
/// 选表：实例在构造时绑定显式选定的 sheet ID 集合；ReadAsync/ObserveAsync 只读取并
/// 只投影所选表，未选表不发任何 records 请求。选表前的连接测试/目录浏览使用
/// WpsSourceImportHostApi.ReadCatalogAsync（无需选定表，也不读取记录）。
/// 目录浏览（盘列表需要 kso.drive.readwrite、文件级列表无官方响应证据）与分享链接
/// 解析均不在只读导入的最小权限与证据范围内，入口只接受 file_id，解析路径 UNVERIFIED。
///
/// 明确不支持（EVIDENCE_MISSING，不伪装完成）：
/// - 附件下载：官方 dbsheet 服务端文档树（docs/api/collections/wps365）无附件下载端点，
///   Attachment 字段映射为 unknown，需用户确认快照或跳过；
/// - 父子（parent-child）记录结构：schema 响应不暴露父子配置，平面记录读取是否
///   覆盖子记录 UNVERIFIED，见支持矩阵。
/// </summary>
internal sealed class WpsSourceImportProvider : IHostSourceImportProvider
{
    internal const int MaxPaginationRequests = 200;

    private readonly WpsSourceImportConnection _connection;
    private readonly WpsReadLimits _limits;
    private readonly WpsOpenApiClient _client;
    private readonly HashSet<string> _selectedTableIds;
    private int _disposed;

    internal WpsSourceImportProvider(WpsSourceImportConnection connection,
        IReadOnlyCollection<string> selectedTableIds, WpsReadLimits? limits = null,
        HttpMessageHandler? handler = null, Func<TimeSpan, CancellationToken, Task>? retryDelay = null,
        TimeSpan? requestTimeout = null)
    {
        ArgumentNullException.ThrowIfNull(selectedTableIds);
        if (selectedTableIds.Count == 0)
            throw new ArgumentException("必须显式选定至少一张数据表；未选表不会被读取。", nameof(selectedTableIds));
        _selectedTableIds = new HashSet<string>(selectedTableIds, StringComparer.Ordinal);
        if (_selectedTableIds.Count != selectedTableIds.Count)
            throw new ArgumentException("选定的数据表 ID 重复。", nameof(selectedTableIds));
        foreach (string id in _selectedTableIds)
        {
            if (string.IsNullOrWhiteSpace(id) || id.Trim() != id)
                throw new ArgumentException($"选定的数据表 ID 无效：{id}。", nameof(selectedTableIds));
        }
        _connection = connection;
        _limits = limits ?? WpsReadLimits.Default;
        _client = new WpsOpenApiClient(connection, handler, retryDelay, requestTimeout);
    }

    /// <summary>
    /// 连接测试与选表入口：读取官方 schema，返回文件结构、每表字段映射详情（供向导
    /// 做预检降级决策：unknown/formula/lookup/person/system 等需确认快照或跳过）与
    /// 全文件语义版本。此调用不读取任何记录。
    /// </summary>
    internal Task<WpsCatalog> ReadCatalogAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        EnsureNotDisposed();
        return ReadCatalogCoreAsync(_client, _connection, _limits, token);
    }

    /// <summary>选表前的目录/连接测试核心；由本类与 HostApi 共用，不复制实现。</summary>
    internal static async Task<WpsCatalog> ReadCatalogCoreAsync(WpsOpenApiClient client,
        WpsSourceImportConnection connection, WpsReadLimits limits, CancellationToken token)
    {
        List<WpsSchemaSheet> sheets = await ReadSchemaAsync(client, connection, token).ConfigureAwait(false);
        var tables = new List<WpsTableSummary>(sheets.Count);
        foreach (WpsSchemaSheet sheet in sheets)
        {
            WpsFieldMapping mapping = WpsFieldMapping.Parse(sheet.CloneFields());
            var fields = new List<WpsFieldSummary>(mapping.Count);
            foreach (WpsFieldMap field in mapping.ById.Values)
            {
                fields.Add(new(field.Id, field.Name, field.WpsType, field.Kind, field.ValueKind,
                    field.Definition, field.Options.Length,
                    field.Relation?.TargetTableId ?? "", field.Relation?.TargetFieldId ?? "",
                    field.Relation?.Cardinality ?? ""));
            }
            tables.Add(new(sheet.Id, sheet.Name, sheet.PrimaryFieldId, fields,
                sheet.Views.ValueKind == JsonValueKind.Array ? sheet.Views.GetArrayLength() : 0));
        }
        return new WpsCatalog(connection.FileId,
            WpsSchemaVersion.File(connection.FileId, sheets.OrderBy(sheet => sheet.Id, StringComparer.Ordinal)),
            DateTimeOffset.UtcNow, tables);
    }

    public async Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        EnsureNotDisposed();
        DateTimeOffset started = DateTimeOffset.UtcNow;
        List<WpsSchemaSheet> sheets = await ReadSchemaAsync(_client, _connection, token).ConfigureAwait(false);
        if (sheets.Count == 0)
            throw new WpsImportException(WpsImportFailure.Protocol,
                "WPS 多维表格不包含任何数据表；请确认 file_id 指向多维表格（db/.dbt），传统表格与智能表格不在支持范围。");
        List<WpsSchemaSheet> selected = SelectSheets(sheets);
        var tables = new List<HostSourceImportTable>(selected.Count);
        var tableVersions = new Dictionary<string, string>(StringComparer.Ordinal);
        // 记录 ID 去重按表作用域：不同表允许相同的记录 ID。
        var seenRecordIds = new HashSet<(string TableId, string RecordId)>();
        int totalRecords = 0;
        foreach (WpsSchemaSheet sheet in selected)
        {
            token.ThrowIfCancellationRequested();
            WpsFieldMapping mapping = WpsFieldMapping.Parse(sheet.CloneFields());
            if (mapping.Count == 0)
                throw new WpsImportException(WpsImportFailure.Protocol,
                    $"数据表 {sheet.Name}（{sheet.Id}）没有任何字段，无法建立迁移模型。");
            if (mapping.Count > _limits.MaxFieldsPerSheet)
                throw new WpsImportException(WpsImportFailure.Capacity,
                    $"数据表 {sheet.Name}（{sheet.Id}）字段数 {mapping.Count} 超过上限 {_limits.MaxFieldsPerSheet}。");
            List<HostSourceImportRecord> records = await ReadRecordsAsync(sheet, mapping,
                seenRecordIds, token).ConfigureAwait(false);
            totalRecords += records.Count;
            if (totalRecords > _limits.MaxRecords)
                throw new WpsImportException(WpsImportFailure.Capacity,
                    $"来源记录总数超过本次迁移上限 {_limits.MaxRecords} 条；请缩小选表范围后重试。");
            tableVersions[sheet.Id] = WpsSchemaVersion.Sheet(sheet);
            tables.Add(new HostSourceImportTable(sheet.Id, sheet.Name, tableVersions[sheet.Id],
                sheet.PrimaryFieldId,
                [.. mapping.ById.Values.Select(field => field.ToField())], [.. records]));
        }
        DateTimeOffset finished = DateTimeOffset.UtcNow;
        // 分页读取不是时点快照：按引擎 readWindow 契约声明实际读取窗口。
        return new HostSourceImportSnapshot(WpsSourceImportConnection.ProviderName, _connection.FileId,
            _connection.DisplayName, WpsSchemaVersion.File(_connection.FileId, selected),
            new HostSourceImportReadWindow(started.UtcDateTime.ToString("O", CultureInfo.InvariantCulture),
                finished.UtcDateTime.ToString("O", CultureInfo.InvariantCulture), "window"),
            [.. tables.OrderBy(table => table.Id, StringComparer.Ordinal)], []);
    }

    public async Task<HostSourceImportObservation> ObserveAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        EnsureNotDisposed();
        List<WpsSchemaSheet> sheets = await ReadSchemaAsync(_client, _connection, token).ConfigureAwait(false);
        // 与 ReadAsync 相同语义投影：只观察所选表，未选表变化不阻断本次迁移。
        List<WpsSchemaSheet> selected = SelectSheets(sheets);
        var tableVersions = selected.ToDictionary(
            sheet => sheet.Id, sheet => WpsSchemaVersion.Sheet(sheet), StringComparer.Ordinal);
        return new HostSourceImportObservation(WpsSchemaVersion.File(_connection.FileId, selected), tableVersions);
    }

    public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        EnsureNotDisposed();
        throw new WpsImportException(WpsImportFailure.Unsupported,
            "WPS 多维表格附件读取缺少官方下载端点证据（EVIDENCE_MISSING）；附件字段需确认值快照或跳过，不能伪装为已迁移文件。");
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _client.Dispose();
    }

    private List<WpsSchemaSheet> SelectSheets(List<WpsSchemaSheet> sheets)
    {
        var byId = new Dictionary<string, WpsSchemaSheet>(StringComparer.Ordinal);
        foreach (WpsSchemaSheet sheet in sheets)
        {
            if (byId.ContainsKey(sheet.Id))
                throw new WpsImportException(WpsImportFailure.Protocol, $"WPS schema 数据表 ID 重复：{sheet.Id}。");
            byId[sheet.Id] = sheet;
        }
        List<string> missing = _selectedTableIds.Where(id => !byId.ContainsKey(id))
            .OrderBy(id => id, StringComparer.Ordinal).ToList();
        if (missing.Count > 0)
            throw new WpsImportException(WpsImportFailure.Protocol,
                $"所选数据表在 schema 中不存在：{string.Join(",", missing)}；请重新选表。");
        if (sheets.Count > _limits.MaxTables)
            throw new WpsImportException(WpsImportFailure.Capacity,
                $"来源数据表 {sheets.Count} 张超过本次迁移上限 {_limits.MaxTables} 张；请缩小迁移范围。");
        return byId.Where(pair => _selectedTableIds.Contains(pair.Key))
            .Select(pair => pair.Value)
            .OrderBy(sheet => sheet.Id, StringComparer.Ordinal)
            .ToList();
    }

    private void EnsureNotDisposed()
        => ObjectDisposedException.ThrowIf(_disposed != 0, this);

    internal static async Task<List<WpsSchemaSheet>> ReadSchemaAsync(WpsOpenApiClient client,
        WpsSourceImportConnection connection, CancellationToken token)
    {
        JsonElement data = await client.GetAsync(
            $"/v7/coop/dbsheet/{Uri.EscapeDataString(connection.FileId)}/schema", token).ConfigureAwait(false);
        if (data.ValueKind != JsonValueKind.Object
            || data.TryGetProperty("sheets", out JsonElement sheetsElement) is false
            || sheetsElement.ValueKind != JsonValueKind.Array)
            throw new WpsImportException(WpsImportFailure.Protocol, "WPS schema 响应缺少 data.sheets 数组。");
        var sheets = new List<WpsSchemaSheet>();
        foreach (JsonElement sheet in sheetsElement.EnumerateArray())
        {
            if (sheet.ValueKind != JsonValueKind.Object
                || !sheet.TryGetProperty("id", out JsonElement idElement)
                || !idElement.TryGetInt64(out long sheetId) || sheetId <= 0
                || sheet.GetProperty("name").GetString() is not { } name)
                throw new WpsImportException(WpsImportFailure.Protocol, "WPS schema 数据表缺少 id/name。");
            string primary = sheet.TryGetProperty("primary_field_id", out JsonElement primaryElement)
                && primaryElement.ValueKind == JsonValueKind.String ? primaryElement.GetString()! : "";
            JsonElement fields = sheet.TryGetProperty("fields", out JsonElement fieldsElement)
                && fieldsElement.ValueKind == JsonValueKind.Array ? fieldsElement : default;
            if (fields.ValueKind != JsonValueKind.Array)
                throw new WpsImportException(WpsImportFailure.Protocol,
                    $"WPS schema 数据表 {name}（{sheetId}）缺少 fields 数组。");
            JsonElement views = sheet.TryGetProperty("views", out JsonElement viewsElement)
                && viewsElement.ValueKind == JsonValueKind.Array ? viewsElement.Clone()
                : JsonSerializer.SerializeToElement(Array.Empty<object>());
            sheets.Add(new WpsSchemaSheet(sheetId.ToString(CultureInfo.InvariantCulture), name, primary,
                fields.Clone(), views));
        }
        return sheets;
    }

    private async Task<List<HostSourceImportRecord>> ReadRecordsAsync(WpsSchemaSheet sheet,
        WpsFieldMapping mapping, HashSet<(string TableId, string RecordId)> seenRecordIds, CancellationToken token)
    {
        string uri = $"/v7/coop/dbsheet/{Uri.EscapeDataString(_connection.FileId)}/sheets/{sheet.Id}/records";
        var records = new List<HostSourceImportRecord>();
        var seenPageTokens = new HashSet<string>(StringComparer.Ordinal);
        string? pageToken = null;
        for (int request = 1; ; request++)
        {
            token.ThrowIfCancellationRequested();
            if (request > MaxPaginationRequests)
                throw new WpsImportException(WpsImportFailure.Pagination,
                    $"数据表 {sheet.Name}（{sheet.Id}）分页超过 {MaxPaginationRequests} 页仍未结束；已中止，未静默截断。");
            var body = new Dictionary<string, object>
            {
                ["prefer_id"] = true,
                ["text_value"] = "original",
                ["page_size"] = _limits.PageSize,
            };
            if (pageToken is not null) body["page_token"] = pageToken;
            JsonElement data = await _client.PostAsync(uri, body, token).ConfigureAwait(false);
            if (data.ValueKind != JsonValueKind.Object
                || !data.TryGetProperty("records", out JsonElement recordsElement)
                || recordsElement.ValueKind != JsonValueKind.Array)
                throw new WpsImportException(WpsImportFailure.Protocol,
                    $"数据表 {sheet.Name}（{sheet.Id}）记录响应缺少 data.records 数组。");
            foreach (JsonElement record in recordsElement.EnumerateArray())
            {
                if (record.ValueKind != JsonValueKind.Object
                    || record.GetProperty("id").GetString() is not { Length: > 0 } recordId)
                    throw new WpsImportException(WpsImportFailure.Protocol,
                        $"数据表 {sheet.Name}（{sheet.Id}）存在缺少 id 的记录。");
                if (!seenRecordIds.Add((sheet.Id, recordId)))
                    throw new WpsImportException(WpsImportFailure.Pagination,
                        $"数据表 {sheet.Name}（{sheet.Id}）记录 ID 重复出现：{recordId}；分页结果不可信，已中止。");
                Dictionary<string, JsonElement> values = ParseRecordFields(sheet, record, mapping);
                records.Add(new HostSourceImportRecord(recordId, values));
            }
            string next = data.TryGetProperty("page_token", out JsonElement tokenElement)
                && tokenElement.ValueKind == JsonValueKind.String ? tokenElement.GetString()! : "";
            if (next.Length == 0) break;
            if (!seenPageTokens.Add(next))
                throw new WpsImportException(WpsImportFailure.Pagination,
                    $"数据表 {sheet.Name}（{sheet.Id}）返回了重复的分页游标；已中止，不会循环拉取。");
            pageToken = next;
        }
        return records;
    }

    private static Dictionary<string, JsonElement> ParseRecordFields(WpsSchemaSheet sheet,
        JsonElement record, WpsFieldMapping mapping)
    {
        if (!record.TryGetProperty("fields", out JsonElement fieldsElement)) return [];
        JsonElement fields = fieldsElement;
        if (fields.ValueKind == JsonValueKind.String)
        {
            try
            {
                using JsonDocument document = JsonDocument.Parse(fields.GetString()!);
                fields = document.RootElement.Clone();
            }
            catch (JsonException error)
            {
                throw new WpsImportException(WpsImportFailure.Protocol,
                    $"数据表 {sheet.Name}（{sheet.Id}）记录的 fields 不是合法 JSON 字符串：{error.GetType().Name}");
            }
        }
        return mapping.ConvertValues(fields);
    }
}

/// <summary>
/// 结构漂移语义版本：有界规范化字符串（非摘要/hash）。生产者是本 provider 的
/// schema 读取，消费者是 #435 引擎的 Observation 字符串比对，不一致即阻断并要求
/// 重新预检。规范化规则：object 属性按键序、数组元素按规范化串排序（字段/选项
/// 顺序无关）、数值/布尔/字符串保留原始文本；字段 rename、type、options、
/// relation、primary、表 rename 均必然改变版本。超长 fail-closed，不截断。
/// </summary>
internal static class WpsSchemaVersion
{
    internal const int MaxLength = 262_144;

    internal static string Sheet(WpsSchemaSheet sheet)
        => Bound("wps-sv1:" + sheet.Id + ":n=" + sheet.Name + ":p=" + sheet.PrimaryFieldId
            + ":f=" + Canon(sheet.CloneFields()));

    internal static string File(string fileId, IEnumerable<WpsSchemaSheet> sheets)
        => Bound("wps-fv1:" + fileId + ":" + string.Join("|",
            sheets.Select(Sheet).Order(StringComparer.Ordinal)));

    private static string Bound(string value)
        => value.Length <= MaxLength ? value
            : throw new WpsImportException(WpsImportFailure.Protocol,
                $"WPS schema 语义版本串超过 {MaxLength} 字符上限；已中止，不截断来源结构。");

    private static string Canon(JsonElement value) => value.ValueKind switch
    {
        JsonValueKind.Object => "{" + string.Join(",",
            value.EnumerateObject().Select(property => JsonSerializer.Serialize(property.Name) + ":" + Canon(property.Value))
                .Order(StringComparer.Ordinal)) + "}",
        JsonValueKind.Array => "[" + string.Join(",",
            value.EnumerateArray().Select(Canon).Order(StringComparer.Ordinal)) + "]",
        JsonValueKind.String => JsonSerializer.Serialize(value.GetString()),
        _ => value.GetRawText(),
    };
}
