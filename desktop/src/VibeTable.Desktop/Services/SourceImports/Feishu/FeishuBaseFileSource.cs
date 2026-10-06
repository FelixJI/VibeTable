using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Offline reader for a Feishu ".base" local export file. The file is a JSON
/// envelope {gzipSnapshot,gzipExtraInfo,gzipDashboard,gzipAutomation,sign}
/// whose members are base64+gzip payloads; gzipSnapshot decompresses to a
/// JSON array with one entry per table, each {"schema":{base,owner,tableMap,
/// data{table,recordMap,recordMeta},structVersion}}. Only the observed
/// contract is accepted (structVersion 1, base.schemaVersion 5, field
/// semantics confirmed by fieldUIType); anything else fails closed with a
/// fixed readable InvalidDataException that never echoes file values.
/// The read is one bounded offline pass: the mapped snapshot is frozen in
/// memory, no URL is ever opened, and owner/userMap/avatar/automation
/// metadata (including webhook_token) is never mapped into the contract.
/// The sign member is neither verified nor hashed (no public contract).
/// </summary>
internal static class FeishuBaseFileSource
{
    internal const int MaxFileBytes = 32 * 1024 * 1024;
    internal const int MaxDecompressedBytes = 64 * 1024 * 1024;
    internal const int MaxTables = 100;
    internal const int MaxFieldsPerTable = 512;
    internal const int MaxRecordsPerTable = 50_000;
    private const int MaxJsonDepth = 128;
    internal const string LocalFileSuffix = "（.base 本地文件）";
    /// <summary>Go validWindow only accepts snapshot|window; a local file
    /// read is a point-in-time snapshot, not a bounded read window.</summary>
    internal const string Consistency = "snapshot";

    private const string MsgOpenFailed = "无法读取 .base 文件：文件不存在、不可读或被占用。";
    private const string MsgFileTooLarge = ".base 文件超过 32 MiB 读取上限，已停止解析。";
    private const string MsgEnvelopeShape = ".base 文件不是有效的 JSON 信封，已停止解析。";
    private const string MsgSnapshotMissing = ".base 文件缺少 gzipSnapshot 成员，无法解析表格数据。";
    private const string MsgBase64 = ".base 文件的 gzip 成员不是有效的 base64 数据，已停止解析。";
    private const string MsgGzip = ".base 文件的 gzip 成员解压失败，已停止解析。";
    private const string MsgDecompressBudget = ".base 文件解压内容超过 64 MiB 总量上限，已停止解析。";
    private const string MsgJson = ".base 文件成员不是有效的 JSON，已停止解析。";
    private const string MsgDuplicateKey = ".base 文件包含重复的 JSON 键，身份不唯一，已停止解析。";
    private const string MsgSnapshotArray = "gzipSnapshot 解压后不是 JSON 数组，已停止解析。";
    private const string MsgItemSchema = "快照条目不符合已观察的 .base 结构，已停止解析。";
    private const string MsgStructVersion = "快照 structVersion 不是受支持的 1，已停止解析。";
    private const string MsgSchemaVersion = "base.schemaVersion 不是受支持的 5，已停止解析。";
    private const string MsgBaseIdentity = "快照缺少有效的 base 身份（token），已停止解析。";
    private const string MsgBaseMixed = "快照条目属于不同的 Base（token 不一致），已停止解析。";
    private const string MsgTableCap = "快照表数量超过 100 上限，已停止解析。";
    private const string MsgFieldCap = "单表字段数超过 512 上限，已停止解析。";
    private const string MsgRecordCap = "单表记录数超过 50000 上限，已停止解析。";
    private const string MsgTableUndeclared = "快照中的表未在 tableMap 中声明，已停止解析。";
    private const string MsgTableDuplicate = "快照包含重复的表，身份不唯一，已停止解析。";
    private const string MsgPrimary = "表缺少唯一有效的主字段（primaryKey），已停止解析。";
    private const string MsgUndeclaredField = "记录包含未声明字段的单元格，身份不唯一，已停止解析。";
    private const string MsgValueShape = "字段值形状与已验证类型不符，已停止解析。";
    private const string MsgTableSchemaVersion = "table.meta.schemaVersion 不是受支持的 5，已停止解析。";
    private const string MsgRecordCount = "table.meta.recordsNum 与记录数不一致，文件可能不完整，已停止解析。";
    private const string MsgRecordMetaMismatch = "recordMeta 与 recordMap 的记录集合不一致，文件可能不完整，已停止解析。";
    private const string MsgMemberString = ".base 文件中存在的 gzip 成员不是字符串，已停止解析。";
    private const string MsgMemberArray = ".base 文件中存在的可选成员解压后不是 JSON 数组，已停止解析。";
    private const string MsgOptionIdentity = "选项缺少唯一身份（id/name 缺失或重复），已停止解析。";

    private const string NoticePersonGeneral =
        "人员字段仅保留 userId 与姓名展示信息；头像链接等无关元数据不迁移。";

    private static readonly JsonDocumentOptions DocumentOptions = new() { MaxDepth = MaxJsonDepth };
    private static readonly JsonSerializerOptions DefinitionWire = new(JsonSerializerDefaults.Web);

    /// <summary>
    /// type→fieldUIType pairs observed together on a real export — the only
    /// evidence available; there is no official .base format contract.
    /// Local export codes differ from the online API
    /// (19=Lookup, 20=Formula locally), so this table is the sole gate for
    /// native mapping; any other pair degrades to an explicit JSON snapshot.
    /// </summary>
    private static readonly IReadOnlyDictionary<int, string[]> VerifiedUiTypes = new Dictionary<int, string[]>
    {
        [1] = ["Text"],
        [2] = ["Number", "Currency"],
        [3] = ["SingleSelect"],
        [5] = ["DateTime"],
        [11] = ["User"],
        [15] = ["Url"],
        [17] = ["Attachment"],
        [19] = ["Lookup"],
        [20] = ["Formula"],
        [1002] = ["ModifiedTime"],
    };

    internal static async Task<FeishuBaseFileDocument> ReadFileAsync(string path, CancellationToken token)
    {
        string startedAt = DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture);
        byte[] envelopeBytes = await ReadFileBytesAsync(path, token).ConfigureAwait(false);
        using JsonDocument envelopeDocument = ParseSingleJson(envelopeBytes, MsgEnvelopeShape);
        JsonElement envelope = envelopeDocument.RootElement;
        if (envelope.ValueKind != JsonValueKind.Object)
            throw new InvalidDataException(MsgEnvelopeShape);
        long budget = MaxDecompressedBytes;
        byte[] snapshotBytes = ReadSnapshotMember(envelope, ref budget, token);
        int dashboardCount = CountArrayMember(envelope, "gzipDashboard", ref budget, token);
        int automationCount = CountArrayMember(envelope, "gzipAutomation", ref budget, token);
        using JsonDocument snapshotDocument = ParseSingleJson(snapshotBytes, MsgSnapshotArray);
        JsonElement snapshot = snapshotDocument.RootElement;
        if (snapshot.ValueKind != JsonValueKind.Array)
            throw new InvalidDataException(MsgSnapshotArray);
        if (snapshot.GetArrayLength() > MaxTables)
            throw new InvalidDataException(MsgTableCap);

        string? containerToken = null;
        string baseName = "";
        long baseRevision = 0;
        string timezone = "";
        int viewCount = 0;
        var tables = new List<HostSourceImportTable>();
        var tableVersions = new Dictionary<string, string>(StringComparer.Ordinal);
        var generalNotices = new List<string>();
        var tableNotices = new List<string>();
        bool anyPersonField = false;
        var seenTables = new HashSet<string>(StringComparer.Ordinal);

        foreach (JsonElement item in snapshot.EnumerateArray())
        {
            token.ThrowIfCancellationRequested();
            if (item.ValueKind != JsonValueKind.Object
                || !item.TryGetProperty("schema", out JsonElement schema)
                || schema.ValueKind != JsonValueKind.Object)
                throw new InvalidDataException(MsgItemSchema);
            if (!schema.TryGetProperty("structVersion", out JsonElement structVersion)
                || structVersion.ValueKind != JsonValueKind.Number
                || !structVersion.TryGetInt32(out int structValue) || structValue != 1)
                throw new InvalidDataException(MsgStructVersion);
            JsonElement baseObject = RequireObject(schema, "base");
            if (!baseObject.TryGetProperty("schemaVersion", out JsonElement schemaVersion)
                || schemaVersion.ValueKind != JsonValueKind.Number
                || !schemaVersion.TryGetInt32(out int schemaValue) || schemaValue != 5)
                throw new InvalidDataException(MsgSchemaVersion);
            if (!baseObject.TryGetProperty("token", out JsonElement tokenElement)
                || tokenElement.ValueKind != JsonValueKind.String
                || tokenElement.GetString() is not { Length: > 0 } fileToken
                || !FeishuSourceImportClient.IsValidToken(fileToken))
                throw new InvalidDataException(MsgBaseIdentity);
            if (containerToken is null)
            {
                containerToken = fileToken;
                baseName = StringProperty(baseObject, "name");
                timezone = StringProperty(baseObject, "timezone");
                baseRevision = baseObject.TryGetProperty("rev", out JsonElement revision)
                    && revision.ValueKind == JsonValueKind.Number
                    && revision.TryGetInt64(out long rev) ? rev : 0;
            }
            else if (fileToken != containerToken)
                throw new InvalidDataException(MsgBaseMixed);
            JsonElement tableMap = RequireObject(schema, "tableMap");
            JsonElement data = RequireObject(schema, "data");
            JsonElement table = RequireObject(data, "table");
            JsonElement recordMap = RequireObject(data, "recordMap");
            if (!data.TryGetProperty("recordMeta", out JsonElement recordMeta)
                || recordMeta.ValueKind != JsonValueKind.Object)
                throw new InvalidDataException(MsgItemSchema);
            JsonElement meta = RequireObject(table, "meta");
            if (!meta.TryGetProperty("id", out JsonElement metaId)
                || metaId.ValueKind != JsonValueKind.String
                || metaId.GetString() is not { Length: > 0 } tableId)
                throw new InvalidDataException(MsgItemSchema);
            if (!seenTables.Add(tableId))
                throw new InvalidDataException(MsgTableDuplicate);
            if (!tableMap.TryGetProperty(tableId, out JsonElement declared)
                || declared.ValueKind != JsonValueKind.Object
                || !declared.TryGetProperty("name", out JsonElement declaredName)
                || declaredName.ValueKind != JsonValueKind.String)
                throw new InvalidDataException(MsgTableUndeclared);
            string tableName = declaredName.GetString()!;
            if (tableName.Length == 0) tableName = tableId;
            long tableRevision = meta.TryGetProperty("rev", out JsonElement metaRevision)
                && metaRevision.ValueKind == JsonValueKind.Number
                && metaRevision.TryGetInt64(out long tableRev) ? tableRev : 0;
            if (!meta.TryGetProperty("schemaVersion", out JsonElement tableSchemaVersion)
                || tableSchemaVersion.ValueKind != JsonValueKind.Number
                || !tableSchemaVersion.TryGetInt32(out int tableSchemaValue) || tableSchemaValue != 5)
                throw new InvalidDataException(MsgTableSchemaVersion);
            int recordCount = recordMap.EnumerateObject().Count();
            if (!meta.TryGetProperty("recordsNum", out JsonElement recordsNum)
                || recordsNum.ValueKind != JsonValueKind.Number
                || !recordsNum.TryGetInt32(out int declaredCount) || declaredCount != recordCount)
                throw new InvalidDataException(MsgRecordCount);
            if (recordMeta.EnumerateObject().Count() != recordCount
                || recordMeta.EnumerateObject().Any(property =>
                    !recordMap.TryGetProperty(property.Name, out _)))
                throw new InvalidDataException(MsgRecordMetaMismatch);
            if (table.TryGetProperty("viewMap", out JsonElement viewMap)
                && viewMap.ValueKind == JsonValueKind.Object)
                viewCount += viewMap.EnumerateObject().Count();
            tables.Add(BuildTable(tableId, tableName, tableRevision, table, recordMap,
                timezone, tableNotices, ref anyPersonField, token));
            tableVersions[tableId] = "table-rev-" + tableRevision.ToString(CultureInfo.InvariantCulture);
        }
        if (containerToken is null)
            throw new InvalidDataException(MsgBaseIdentity);
        if (dashboardCount > 0 || automationCount > 0 || viewCount > 0)
            generalNotices.Add(string.Format(CultureInfo.InvariantCulture,
                "文件包含 {0} 个仪表盘、{1} 个自动化流程、{2} 个视图配置，均不会导入。",
                dashboardCount, automationCount, viewCount));
        if (anyPersonField)
            generalNotices.Add(NoticePersonGeneral);

        string finishedAt = DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture);
        var readWindow = new HostSourceImportReadWindow(startedAt, finishedAt, Consistency);
        string displayName = (baseName.Length == 0 ? "飞书多维表格" : baseName) + LocalFileSuffix;
        string baseVersion = "base-rev-" + baseRevision.ToString(CultureInfo.InvariantCulture);
        var result = new HostSourceImportSnapshot(FeishuSourceImportConnector.ProviderName,
            containerToken, displayName, baseVersion, readWindow, tables.ToArray(), []);
        var observation = new HostSourceImportObservation(baseVersion, tableVersions);
        string[] notices = [.. generalNotices, .. tableNotices];
        return new FeishuBaseFileDocument(result, observation, notices);
    }
    private enum CellPlan
    {
        TextNative,
        TextSnapshot,
        Number,
        Select,
        DateTimeNative,
        UrlNative,
        UrlSnapshot,
        Person,
        File,
        Lookup,
        Formula,
        System,
        Unknown,
    }

    private sealed record FieldPlan(HostSourceImportField Field, CellPlan Plan, HashSet<string> OptionIds);

    private static HostSourceImportTable BuildTable(string tableId, string tableName, long tableRevision,
        JsonElement table, JsonElement recordMap, string timezone, List<string> notices,
        ref bool anyPersonField, CancellationToken token)
    {
        JsonElement fieldMap = RequireObject(table, "fieldMap");
        if (fieldMap.EnumerateObject().Count() > MaxFieldsPerTable)
            throw new InvalidDataException(MsgFieldCap);
        if (recordMap.EnumerateObject().Count() > MaxRecordsPerTable)
            throw new InvalidDataException(MsgRecordCap);
        if (!table.TryGetProperty("primaryKey", out JsonElement primaryKey)
            || primaryKey.ValueKind != JsonValueKind.String
            || primaryKey.GetString() is not { Length: > 0 } primaryId
            || !fieldMap.TryGetProperty(primaryId, out _))
            throw new InvalidDataException(MsgPrimary);

        var plans = new Dictionary<string, FieldPlan>(StringComparer.Ordinal);
        var mappedFields = new List<HostSourceImportField>();
        var selectForeign = new HashSet<string>(StringComparer.Ordinal);
        var personSkipped = new HashSet<string>(StringComparer.Ordinal);

        foreach (JsonProperty fieldProperty in fieldMap.EnumerateObject())
        {
            JsonElement field = fieldProperty.Value;
            if (field.ValueKind != JsonValueKind.Object)
                throw new InvalidDataException(MsgItemSchema);
            string fieldId = fieldProperty.Name;
            string fieldName = field.TryGetProperty("name", out JsonElement name)
                && name.ValueKind == JsonValueKind.String ? name.GetString()! : "";
            string fieldDisplay = fieldName.Length > 0 ? fieldName : fieldId;
            int type = field.TryGetProperty("type", out JsonElement typeElement)
                && typeElement.ValueKind == JsonValueKind.Number
                && typeElement.TryGetInt32(out int typeValue) ? typeValue : -1;
            string uiType = field.TryGetProperty("fieldUIType", out JsonElement ui)
                && ui.ValueKind == JsonValueKind.String ? ui.GetString()! : "";
            JsonElement property = field.TryGetProperty("property", out JsonElement propertyElement)
                && propertyElement.ValueKind == JsonValueKind.Object ? propertyElement : default;
            bool verified = VerifiedUiTypes.TryGetValue(type, out string[]? uiNames)
                && uiNames.Contains(uiType);
            CellPlan plan;
            switch (type)
            {
                case 1 when verified:
                    plan = AllCellsSatisfy(recordMap, fieldId, IsPureTextValue)
                        ? CellPlan.TextNative : CellPlan.TextSnapshot;
                    break;
                case 2 when verified:
                    plan = CellPlan.Number;
                    break;
                case 3 when verified:
                    plan = CellPlan.Select;
                    break;
                case 5 when verified:
                    plan = CellPlan.DateTimeNative;
                    break;
                case 11 when verified:
                    plan = CellPlan.Person;
                    anyPersonField = true;
                    break;
                case 15 when verified:
                    plan = AllCellsSatisfy(recordMap, fieldId, IsSingleUrlValue)
                        ? CellPlan.UrlNative : CellPlan.UrlSnapshot;
                    break;
                case 17 when verified:
                    plan = CellPlan.File;
                    notices.Add($"表 {tableName} 的字段 {fieldDisplay} 是附件字段：.base 导出不包含文件字节，仅保留元数据快照且不会下载，需要用户确认。");
                    break;
                case 19 when verified:
                    plan = CellPlan.Lookup;
                    break;
                case 20 when verified:
                    plan = CellPlan.Formula;
                    break;
                case 1002 when verified:
                    plan = CellPlan.System;
                    break;
                default:
                    plan = CellPlan.Unknown;
                    string typeLabel = uiType.Length > 0
                        ? type.ToString(CultureInfo.InvariantCulture) + "/" + uiType
                        : type.ToString(CultureInfo.InvariantCulture);
                    notices.Add($"表 {tableName} 的字段 {fieldDisplay}（本地类型 {typeLabel}）缺少已验证的原生映射，整列按原始 JSON 快照迁移，需要用户确认。");
                    break;
            }
            if (plan is CellPlan.TextSnapshot or CellPlan.UrlSnapshot)
                notices.Add($"表 {tableName} 的字段 {fieldDisplay} 包含无法保真拼接的富文本或链接内容（多段链接或显示名称与链接地址不一致），整列按原始 JSON 快照迁移，需要用户确认。");
            (string kind, string valueKind) = plan switch
            {
                CellPlan.TextNative => ("text", "text"),
                CellPlan.Number => ("number", "number"),
                CellPlan.Select => ("select", "select"),
                CellPlan.DateTimeNative => ("dateTime", "dateTime"),
                CellPlan.UrlNative => ("url", "url"),
                CellPlan.Person => ("person", "json"),
                CellPlan.File => ("file", "json"),
                CellPlan.Lookup => ("lookup", "json"),
                CellPlan.Formula => ("formula", "json"),
                CellPlan.System => ("system", "json"),
                _ => ("unknown", "json"),
            };
            HostSourceImportOption[] options = plan == CellPlan.Select ? ReadOptions(property) : [];
            var mapped = new HostSourceImportField(fieldId, fieldName, kind, valueKind, false,
                options, null, null, timezone, ReadDefinition(type, property));
            mappedFields.Add(mapped);
            plans[fieldId] = new FieldPlan(mapped, plan,
                new HashSet<string>(options.Select(option => option.Id), StringComparer.Ordinal));
        }
        var records = new List<HostSourceImportRecord>();
        foreach (JsonProperty recordProperty in recordMap.EnumerateObject())
        {
            token.ThrowIfCancellationRequested();
            string recordId = recordProperty.Name;
            if (recordId.Length == 0 || recordProperty.Value.ValueKind != JsonValueKind.Object)
                throw new InvalidDataException(MsgItemSchema);
            var values = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
            foreach (JsonProperty cellProperty in recordProperty.Value.EnumerateObject())
            {
                JsonElement cell = cellProperty.Value;
                if (!plans.TryGetValue(cellProperty.Name, out FieldPlan? plan))
                    throw new InvalidDataException(MsgUndeclaredField);
                if (cell.ValueKind == JsonValueKind.Null) continue;
                if (cell.ValueKind != JsonValueKind.Object
                    || !cell.TryGetProperty("value", out JsonElement value))
                    throw new InvalidDataException(MsgValueShape);
                if (value.ValueKind == JsonValueKind.Null) continue;
                switch (plan.Plan)
                {
                    case CellPlan.TextNative:
                        if (value.GetArrayLength() == 0) continue;
                        StringBuilder text = new();
                        foreach (JsonElement segment in value.EnumerateArray())
                            text.Append(segment.GetProperty("text").GetString());
                        values[plan.Field.Id] = JsonSerializer.SerializeToElement(text.ToString());
                        break;
                    case CellPlan.Number:
                        if (value.ValueKind != JsonValueKind.Number)
                            throw new InvalidDataException(MsgValueShape);
                        values[plan.Field.Id] = value.Clone();
                        break;
                    case CellPlan.Select:
                        if (value.ValueKind != JsonValueKind.String)
                            throw new InvalidDataException(MsgValueShape);
                        if (plan.OptionIds.Count == 0 || !plan.OptionIds.Contains(value.GetString()!))
                            selectForeign.Add(plan.Field.Id);
                        values[plan.Field.Id] = value.Clone();
                        break;
                    case CellPlan.DateTimeNative:
                        if (value.ValueKind != JsonValueKind.Number || !value.TryGetInt64(out long millis))
                            throw new InvalidDataException(MsgValueShape);
                        try
                        {
                            values[plan.Field.Id] = JsonSerializer.SerializeToElement(
                                DateTimeOffset.FromUnixTimeMilliseconds(millis).UtcDateTime
                                    .ToString("O", CultureInfo.InvariantCulture));
                        }
                        catch (ArgumentOutOfRangeException)
                        {
                            throw new InvalidDataException(MsgValueShape);
                        }
                        break;
                    case CellPlan.UrlNative:
                        values[plan.Field.Id] = JsonSerializer.SerializeToElement(
                            value[0].GetProperty("link").GetString());
                        break;
                    case CellPlan.Person:
                    {
                        if (value.ValueKind != JsonValueKind.Object
                            || !value.TryGetProperty("users", out JsonElement users)
                            || users.ValueKind != JsonValueKind.Array
                            || users.EnumerateArray().Any(user => user.ValueKind != JsonValueKind.Object
                                || !user.TryGetProperty("userId", out JsonElement userId)
                                || userId.ValueKind != JsonValueKind.String
                                || userId.GetString() is not { Length: > 0 }))
                        {
                            personSkipped.Add(plan.Field.Id);
                            continue;
                        }
                        var projected = new List<Dictionary<string, object?>>(users.GetArrayLength());
                        foreach (JsonElement user in users.EnumerateArray())
                            projected.Add(new Dictionary<string, object?>
                            {
                                ["userId"] = user.GetProperty("userId").GetString(),
                                ["name"] = user.TryGetProperty("name", out JsonElement userName)
                                    && userName.ValueKind == JsonValueKind.String ? userName.GetString() : null,
                                ["enName"] = user.TryGetProperty("enName", out JsonElement enName)
                                    && enName.ValueKind == JsonValueKind.String ? enName.GetString() : null,
                            });
                        values[plan.Field.Id] = JsonSerializer.SerializeToElement(projected);
                        break;
                    }
                    default:
                        // Text/Url snapshot, file, lookup, formula, system and
                        // unknown plans keep a faithful raw JSON snapshot.
                        values[plan.Field.Id] = value.Clone();
                        break;
                }
            }
            records.Add(new HostSourceImportRecord(recordId, values));
        }
        foreach (FieldPlan plan in plans.Values)
        {
            string display = plan.Field.Name.Length > 0 ? plan.Field.Name : plan.Field.Id;
            if (plan.Plan is CellPlan.Formula or CellPlan.Lookup)
            {
                int cached = records.Count(record => record.Values.ContainsKey(plan.Field.Id));
                string label = plan.Plan == CellPlan.Formula ? "公式" : "引用";
                string detail = cached == 0
                    ? "计算值未随文件导出，迁移后该列将为空"
                    : $"仅保留 {cached}/{records.Count} 条已导出的缓存值，未导出的结果仍为空，不保留动态计算";
                notices.Add($"表 {tableName} 的字段 {display}（{label}）：{detail}，需要用户确认。");
            }
            if (selectForeign.Contains(plan.Field.Id))
                notices.Add($"表 {tableName} 的字段 {display} 存在不在选项身份表中的选择值，已按原始字符串保留，需要用户确认。");
            if (personSkipped.Contains(plan.Field.Id))
                notices.Add($"表 {tableName} 的字段 {display} 含无法安全识别的人员成员，相关单元格已留空，需要用户确认。");
        }
        return new HostSourceImportTable(tableId, tableName,
            "table-rev-" + tableRevision.ToString(CultureInfo.InvariantCulture),
            primaryId, mappedFields.ToArray(), records.ToArray());
    }

    private static bool IsPureTextValue(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Array) return false;
        foreach (JsonElement segment in value.EnumerateArray())
        {
            if (segment.ValueKind != JsonValueKind.Object) return false;
            bool hasText = false;
            foreach (JsonProperty part in segment.EnumerateObject())
            {
                if (part.Name == "text")
                {
                    if (part.Value.ValueKind != JsonValueKind.String) return false;
                    hasText = true;
                }
                else if (part.Name == "type")
                {
                    if (part.Value.ValueKind != JsonValueKind.String
                        || part.Value.GetString() != "text") return false;
                }
                else return false;
            }
            if (!hasText) return false;
        }
        return true;
    }

    /// <summary>Native url only for a single url segment whose display text
    /// equals the target link and which carries no unknown members — nothing
    /// observable is dropped. Any different label, extra member or multi
    /// segment degrades the whole field to a raw JSON snapshot.</summary>
    private static bool IsSingleUrlValue(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Array || value.GetArrayLength() != 1) return false;
        JsonElement segment = value[0];
        if (segment.ValueKind != JsonValueKind.Object) return false;
        string? text = null, type = null, link = null;
        foreach (JsonProperty part in segment.EnumerateObject())
        {
            if (part.Value.ValueKind != JsonValueKind.String) return false;
            switch (part.Name)
            {
                case "text": text = part.Value.GetString(); break;
                case "type": type = part.Value.GetString(); break;
                case "link": link = part.Value.GetString(); break;
                default: return false;
            }
        }
        return type == "url" && link is { Length: > 0 } && text == link;
    }

    private static bool AllCellsSatisfy(JsonElement recordMap, string fieldId, Func<JsonElement, bool> predicate)
    {
        foreach (JsonProperty record in recordMap.EnumerateObject())
        {
            if (record.Value.ValueKind != JsonValueKind.Object) continue;
            if (!record.Value.TryGetProperty(fieldId, out JsonElement cell)
                || cell.ValueKind != JsonValueKind.Object) continue;
            if (!cell.TryGetProperty("value", out JsonElement value)
                || value.ValueKind is JsonValueKind.Null or JsonValueKind.Undefined) continue;
            if (!predicate(value)) return false;
        }
        return true;
    }

    private static HostSourceImportOption[] ReadOptions(JsonElement property)
    {
        if (property.ValueKind != JsonValueKind.Object
            || !property.TryGetProperty("options", out JsonElement options)
            || options.ValueKind != JsonValueKind.Array) return [];
        var result = new List<HostSourceImportOption>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (JsonElement option in options.EnumerateArray())
        {
            if (option.ValueKind != JsonValueKind.Object
                || !option.TryGetProperty("id", out JsonElement id)
                || id.ValueKind != JsonValueKind.String
                || id.GetString() is not { Length: > 0 } optionId
                || !option.TryGetProperty("name", out JsonElement name)
                || name.ValueKind != JsonValueKind.String
                || !seen.Add(optionId))
                throw new InvalidDataException(MsgOptionIdentity);
            result.Add(new HostSourceImportOption(optionId, name.GetString()!, ""));
        }
        return result.ToArray();
    }

    /// <summary>Provenance keys mirror the online provider: formulaExpression
    /// for formulas, lookupFilter for lookups. Nothing else is invented.</summary>
    private static string ReadDefinition(int type, JsonElement property)
    {
        if (property.ValueKind != JsonValueKind.Object) return "";
        if (type == 20 && property.TryGetProperty("formula", out JsonElement formula)
            && formula.ValueKind == JsonValueKind.String)
            return JsonSerializer.Serialize(
                new Dictionary<string, object?> { ["formulaExpression"] = formula.GetString() },
                DefinitionWire);
        if (type == 19 && property.ValueKind == JsonValueKind.Object)
            return JsonSerializer.Serialize(
                new Dictionary<string, object?> { ["lookupProperty"] = property.Clone() },
                DefinitionWire);
        return "";
    }
    private static JsonElement RequireObject(JsonElement parent, string name) =>
        parent.TryGetProperty(name, out JsonElement child) && child.ValueKind == JsonValueKind.Object
            ? child : throw new InvalidDataException(MsgItemSchema);

    private static string StringProperty(JsonElement parent, string name) =>
        parent.TryGetProperty(name, out JsonElement value) && value.ValueKind == JsonValueKind.String
            ? value.GetString()! : "";

    private static async Task<byte[]> ReadFileBytesAsync(string path, CancellationToken token)
    {
        try
        {
            await using FileStream stream = File.OpenRead(path);
            if (stream.Length > MaxFileBytes) throw new InvalidDataException(MsgFileTooLarge);
            byte[] bytes = new byte[(int)stream.Length];
            int read = 0;
            while (read < bytes.Length)
            {
                int chunk = await stream.ReadAsync(bytes.AsMemory(read), token).ConfigureAwait(false);
                if (chunk == 0) throw new InvalidDataException(MsgOpenFailed);
                read += chunk;
            }
            return bytes;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException
            or NotSupportedException or System.Security.SecurityException)
        {
            throw new InvalidDataException(MsgOpenFailed, ex);
        }
    }

    private static byte[] ReadSnapshotMember(JsonElement envelope, ref long budget, CancellationToken token)
    {
        if (!envelope.TryGetProperty("gzipSnapshot", out JsonElement member)
            || member.ValueKind != JsonValueKind.String)
            throw new InvalidDataException(MsgSnapshotMissing);
        byte[] gzip;
        try
        {
            gzip = Convert.FromBase64String(member.GetString()!);
        }
        catch (FormatException ex)
        {
            throw new InvalidDataException(MsgBase64, ex);
        }
        return DecompressBounded(gzip, ref budget, token);
    }

    /// <summary>Optional members carry informational counts only, but a
    /// member that exists must decode, decompress and parse strictly — a
    /// corrupt or over-budget payload is a broken file, never a silent zero
    /// count. Only a genuinely absent member counts as zero.</summary>
    private static int CountArrayMember(JsonElement envelope, string name, ref long budget, CancellationToken token)
    {
        if (!envelope.TryGetProperty(name, out JsonElement member))
            return 0;
        if (member.ValueKind != JsonValueKind.String)
            throw new InvalidDataException(MsgMemberString);
        byte[] gzip;
        try
        {
            gzip = Convert.FromBase64String(member.GetString()!);
        }
        catch (FormatException ex)
        {
            throw new InvalidDataException(MsgBase64, ex);
        }
        byte[] plain = DecompressBounded(gzip, ref budget, token);
        try
        {
            using JsonDocument document = JsonDocument.Parse(plain, DocumentOptions);
            if (document.RootElement.ValueKind != JsonValueKind.Array)
                throw new InvalidDataException(MsgMemberArray);
            return document.RootElement.GetArrayLength();
        }
        catch (JsonException ex)
        {
            throw new InvalidDataException(MsgJson, ex);
        }
    }

    private static byte[] DecompressBounded(byte[] gzip, ref long budget, CancellationToken token)
    {
        bool overLimit = false;
        long total = 0;
        using MemoryStream output = new();
        try
        {
            using MemoryStream input = new(gzip, writable: false);
            using GZipStream decompressor = new(input, CompressionMode.Decompress);
            byte[] buffer = new byte[64 * 1024];
            while (true)
            {
                token.ThrowIfCancellationRequested();
                int read = decompressor.Read(buffer, 0, buffer.Length);
                if (read == 0) break;
                total += read;
                if (total > budget)
                {
                    overLimit = true;
                    break;
                }
                output.Write(buffer, 0, read);
            }
        }
        catch (Exception ex) when (ex is InvalidDataException or IOException or ObjectDisposedException)
        {
            throw new InvalidDataException(MsgGzip, ex);
        }
        if (overLimit) throw new InvalidDataException(MsgDecompressBudget);
        budget -= total;
        return output.ToArray();
    }

    /// <summary>System.Text.Json silently keeps the last duplicate object key;
    /// this bounded pre-pass rejects duplicates so identities are never
    /// silently overwritten, and pre-validates the depth budget.</summary>
    private static void RejectDuplicateKeys(byte[] utf8, string shapeMessage)
    {
        Utf8JsonReader reader = new(utf8, new JsonReaderOptions { MaxDepth = MaxJsonDepth });
        Stack<HashSet<string>?> frames = new();
        try
        {
            while (reader.Read())
            {
                switch (reader.TokenType)
                {
                    case JsonTokenType.StartObject:
                        frames.Push(new HashSet<string>(StringComparer.Ordinal));
                        break;
                    case JsonTokenType.StartArray:
                        frames.Push(null);
                        break;
                    case JsonTokenType.EndObject or JsonTokenType.EndArray:
                        frames.Pop();
                        break;
                    case JsonTokenType.PropertyName:
                        if (frames.Peek() is not { } keys || !keys.Add(reader.GetString() ?? ""))
                            throw new InvalidDataException(MsgDuplicateKey);
                        break;
                }
            }
        }
        catch (JsonException ex)
        {
            throw new InvalidDataException(shapeMessage, ex);
        }
    }

    private static JsonDocument ParseSingleJson(byte[] utf8, string shapeMessage)
    {
        RejectDuplicateKeys(utf8, shapeMessage);
        try
        {
            return JsonDocument.Parse(utf8, DocumentOptions);
        }
        catch (JsonException ex)
        {
            throw new InvalidDataException(shapeMessage, ex);
        }
    }
}

/// <summary>
/// Frozen result of one .base file read. The snapshot and notices are fixed
/// for the document lifetime; CreateProvider hands table selections to the
/// Host import flow without touching the file or any network again.
/// </summary>
internal sealed class FeishuBaseFileDocument
{
    internal HostSourceImportSnapshot Snapshot { get; }
    internal string[] Notices { get; }
    private readonly HostSourceImportObservation _observation;

    internal FeishuBaseFileDocument(HostSourceImportSnapshot snapshot,
        HostSourceImportObservation observation, string[] notices)
    {
        Snapshot = snapshot;
        _observation = observation;
        Notices = notices;
    }

    /// <summary>Unknown selection ids fail with NotFound instead of guessing.
    /// A null or empty selection means every table in the file.</summary>
    internal IHostSourceImportProvider CreateProvider(string[]? selectedIds = null)
    {
        HostSourceImportTable[] selected = SelectTables(selectedIds);
        HostSourceImportSnapshot snapshot = selected.Length == Snapshot.Tables.Length
            ? Snapshot
            : Snapshot with { Tables = selected };
        return new FeishuBaseFileSourceProvider(snapshot, _observation);
    }

    private HostSourceImportTable[] SelectTables(string[]? selectedIds)
    {
        if (selectedIds is null || selectedIds.Length == 0) return Snapshot.Tables;
        var all = new HashSet<string>(Snapshot.Tables.Select(table => table.Id), StringComparer.Ordinal);
        var selected = new HashSet<string>(selectedIds, StringComparer.Ordinal);
        foreach (string id in selected)
        {
            if (!all.Contains(id))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.NotFound,
                    "所选表不在本地 .base 文件中；请重新选择文件。");
        }
        return Snapshot.Tables.Where(table => selected.Contains(table.Id)).ToArray();
    }
}

/// <summary>
/// Provider over the frozen document snapshot. ReadAsync/ObserveAsync return
/// the same frozen facts (consistency=snapshot), OpenAttachmentAsync always
/// fails closed because .base exports carry no attachment bytes, and Dispose
/// only flips the session state — there is no client or file handle left.
/// </summary>
internal sealed class FeishuBaseFileSourceProvider : IHostSourceImportProvider
{
    private readonly HostSourceImportSnapshot _snapshot;
    private readonly HostSourceImportObservation _observation;
    private int _disposed;

    internal FeishuBaseFileSourceProvider(HostSourceImportSnapshot snapshot,
        HostSourceImportObservation observation)
    {
        _snapshot = snapshot;
        _observation = observation;
    }

    public Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        return Task.FromResult(_snapshot);
    }

    public Task<HostSourceImportObservation> ObserveAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        return Task.FromResult(_observation);
    }

    public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        ThrowIfDisposed();
        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
            "本地 .base 文件不包含附件字节，附件下载不可用。");
    }

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Disposed,
                "本地 .base 来源会话已释放；请重新选择文件。");
    }

    public void Dispose() => Interlocked.Exchange(ref _disposed, 1);
}