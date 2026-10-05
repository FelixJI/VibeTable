using System.Globalization;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace VibeTable.Desktop.Services;

/// <summary>
/// WPS 多维表格字段/值到中立来源契约的映射。类型依据官方「多维表格参数说明」：
/// https://open.wps.cn/documents/app-integration-dev/wps365/server/dbsheet/parameters-description
/// 原则：只有官方文档证实语义的类型做原生映射；公式/Lookup/人员/系统/未知与
/// 无证据的结构（含附件下载）一律映射为需用户确认快照或跳过的类型，不静默降级。
/// </summary>
internal sealed record WpsFieldMap
{
    internal required string Id { get; init; }
    internal required string Name { get; init; }
    internal required string WpsType { get; init; }
    internal required string Kind { get; init; }
    internal required string ValueKind { get; init; }
    internal required string Definition { get; init; }
    internal HostSourceImportOption[] Options { get; init; } = [];
    internal HostSourceImportRelation? Relation { get; init; }
    internal HostSourceImportNumberFormat? NumberFormat { get; init; }

    internal HostSourceImportField ToField() => new(Id, Name, Kind, ValueKind, Required: false,
        Options, Relation, NumberFormat, Timezone: "", Definition);
}

internal sealed class WpsFieldMapping
{
    private readonly Dictionary<string, WpsFieldMap> _byId = new(StringComparer.Ordinal);
    // 名称仅作为 prefer_id 被服务器忽略时的回退；同名冲突时置空并禁用该回退。
    private readonly Dictionary<string, WpsFieldMap?> _byName = new(StringComparer.Ordinal);

    internal IReadOnlyDictionary<string, WpsFieldMap> ById => _byId;
    internal int Count => _byId.Count;

    internal static WpsFieldMapping Parse(JsonElement fields)
    {
        var mapping = new WpsFieldMapping();
        if (fields.ValueKind != JsonValueKind.Array) throw new WpsImportException(
            WpsImportFailure.Protocol, "WPS schema 的 fields 不是数组。");
        foreach (JsonElement field in fields.EnumerateArray())
        {
            if (field.ValueKind != JsonValueKind.Object
                || field.GetProperty("id").GetString() is not { Length: > 0 } id
                || field.GetProperty("name").GetString() is not { } name
                || field.GetProperty("type").GetString() is not { } type)
                throw new WpsImportException(WpsImportFailure.Protocol, "WPS schema 字段缺少 id/name/type。");
            JsonElement data = field.TryGetProperty("data", out JsonElement element)
                && element.ValueKind == JsonValueKind.Object ? element : default;
            WpsFieldMap mapped = Map(id, name, type, data);
            if (mapping._byId.ContainsKey(id))
                throw new WpsImportException(WpsImportFailure.Protocol, $"WPS 字段 ID 重复：{id}。");
            mapping._byId.Add(id, mapped);
            if (mapping._byName.ContainsKey(name)) mapping._byName[name] = null;
            else mapping._byName.Add(name, mapped);
        }
        return mapping;
    }

    private static WpsFieldMap Map(string id, string name, string type, JsonElement data) => type switch
    {
        "MultiLineText" or "ID" or "Phone" => Text(id, name, type, "text", data),
        "Email" => Text(id, name, type, "email", data),
        "Url" => Text(id, name, type, "url", data),
        "Number" or "Currency" or "Rating" or "Complete" => Number(id, name, type, percent: false, data),
        "Percent" => Number(id, name, type, percent: true, data),
        "Checkbox" => new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "bool", ValueKind = "bool",
            Definition = Provenance(type, data) },
        "Time" => new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "time", ValueKind = "time",
            Definition = Provenance(type, data) },
        "Date" => MapDate(id, name, data),
        "SingleSelect" => Select(id, name, type, multiple: false, data),
        "MultipleSelect" => Select(id, name, type, multiple: true, data),
        "Link" => MapLink(id, name, data),
        "Formula" => new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "formula", ValueKind = "",
            Definition = Provenance(type, data, data.TryGetProperty("formula", out JsonElement formula)
                && formula.ValueKind == JsonValueKind.String ? formula.GetString() : null) },
        "Lookup" => new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "lookup", ValueKind = "",
            Definition = Provenance(type, data) },
        "AutoNumber" => Unknown(id, name, type, data, "编号为自动字段，原生迁移未验证"),
        "Contact" => new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "person", ValueKind = "",
            Definition = Provenance(type, data) },
        "CreatedBy" or "LastModifiedBy" or "CreatedTime" or "LastModifiedTime" =>
            new WpsFieldMap { Id = id, Name = name, WpsType = type, Kind = "system", ValueKind = "",
                Definition = Provenance(type, data) },
        "Address" => Unknown(id, name, type, data, "地址结构无本地等价类型"),
        "Note" => Unknown(id, name, type, data, "富文本引用云端文件，无字节读取证据"),
        "Attachment" => Unknown(id, name, type, data,
            "官方 dbsheet 服务端 API 未提供附件下载端点（EVIDENCE_MISSING）；请确认值快照或跳过，不伪装为已迁移文件"),
        _ => Unknown(id, name, type, data, "未知 WPS 字段类型"),
    };

    private static WpsFieldMap Text(string id, string name, string type, string kind, JsonElement data)
        => new() { Id = id, Name = name, WpsType = type, Kind = kind, ValueKind = kind,
            Definition = Provenance(type, data) };

    private static WpsFieldMap Number(string id, string name, string type, bool percent, JsonElement data)
    {
        string format = data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("number_format", out JsonElement element)
            && element.ValueKind == JsonValueKind.String ? element.GetString()! : "";
        return new WpsFieldMap
        {
            Id = id, Name = name, WpsType = type, Kind = "number", ValueKind = "number",
            NumberFormat = WpsNumberFormat.Parse(format, percent),
            Definition = Provenance(type, data, string.IsNullOrWhiteSpace(format) ? null : format),
        };
    }

    private static WpsFieldMap MapDate(string id, string name, JsonElement data)
    {
        string format = data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("number_format", out JsonElement element)
            && element.ValueKind == JsonValueKind.String ? element.GetString()! : "";
        // 官方仅给出按 number_format 展示的日期字符串，未携带时区证据；含时间部分
        // 的格式无法在不虚构时区的情况下转 dateTime，因此映射为需确认的 unknown。
        if (WpsDateFormat.HasTimeComponent(format))
            return Unknown(id, name, "Date", data, "日期时间无来源时区证据，不虚构偏移");
        return new WpsFieldMap
        {
            Id = id, Name = name, WpsType = "Date", Kind = "date", ValueKind = "date",
            Definition = Provenance("Date", data, string.IsNullOrWhiteSpace(format) ? null : format),
        };
    }

    private static WpsFieldMap Select(string id, string name, string type, bool multiple, JsonElement data)
    {
        List<HostSourceImportOption> options = [];
        bool complete = true;
        if (data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("items", out JsonElement items) && items.ValueKind == JsonValueKind.Array)
        {
            foreach (JsonElement item in items.EnumerateArray())
            {
                if (item.ValueKind == JsonValueKind.Object
                    && item.TryGetProperty("value", out JsonElement label)
                    && label.ValueKind == JsonValueKind.String
                    && item.TryGetProperty("id", out JsonElement optionId)
                    && optionId.ValueKind == JsonValueKind.String)
                {
                    string color = item.TryGetProperty("color", out JsonElement colorElement)
                        && colorElement.ValueKind == JsonValueKind.Number
                        && colorElement.TryGetInt64(out long argb)
                        ? $"#{argb & 0xFFFFFF:X6}" : "";
                    options.Add(new(optionId.GetString()!, label.GetString()!, color));
                }
                else complete = false;
            }
        }
        return new WpsFieldMap
        {
            Id = id, Name = name, WpsType = type,
            Kind = multiple ? "multiSelect" : "select",
            ValueKind = multiple ? "multiSelect" : "select",
            Options = [.. options],
            Definition = Provenance(type, data, complete ? null : "items_incomplete=true"),
        };
    }

    private static WpsFieldMap MapLink(string id, string name, JsonElement data)
    {
        string target = "";
        if (data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("link_sheet", out JsonElement sheet)
            && sheet.ValueKind == JsonValueKind.Number && sheet.TryGetInt64(out long sheetId))
            target = sheetId.ToString(CultureInfo.InvariantCulture);
        bool multiple = data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("multiple_links", out JsonElement multi)
            && (multi.ValueKind is JsonValueKind.True or JsonValueKind.False) && multi.GetBoolean();
        string reverse = data.ValueKind == JsonValueKind.Object
            && data.TryGetProperty("link_field", out JsonElement linkField)
            && linkField.ValueKind == JsonValueKind.String ? linkField.GetString()! : "";
        return new WpsFieldMap
        {
            Id = id, Name = name, WpsType = "Link", Kind = "relation", ValueKind = "relation",
            Relation = new(target, reverse, multiple ? "many" : "one"),
            Definition = Provenance("Link", data, "link_sheet=" + target
                + ";multiple_links=" + (multiple ? "true" : "false")
                + (reverse.Length == 0 ? ";link_field=absent" : "")),
        };
    }

    private static WpsFieldMap Unknown(string id, string name, string type, JsonElement data, string reason)
        => new() { Id = id, Name = name, WpsType = type, Kind = "unknown", ValueKind = "",
            Definition = Provenance(type, data, reason) };

    private static string Provenance(string type, JsonElement data, string? detail = null)
    {
        // 溯源串保留来源类型与关键定义，不包含任何凭据；长度约束在引擎溯源预算内。
        string provenance = "wps:type=" + type;
        if (detail is { Length: > 0 } && provenance.Length + detail.Length + 1 <= 256)
            provenance += ";" + detail;
        return provenance;
    }

    /// <summary>
    /// 解析 records[].fields（官方为 JSON 字符串）并转换为中立记录值。键优先按
    /// 字段 ID（prefer_id=true），服务器忽略 prefer_id 时按唯一名称回退；无法解析
    /// 的键视为协议漂移直接失败，不静默丢弃。
    /// </summary>
    internal Dictionary<string, JsonElement> ConvertValues(JsonElement fieldsObject)
    {
        var values = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        if (fieldsObject.ValueKind != JsonValueKind.Object) throw new WpsImportException(
            WpsImportFailure.Protocol, "记录的 fields 不是 JSON 对象。");
        foreach (JsonProperty property in fieldsObject.EnumerateObject())
        {
            WpsFieldMap? field = _byId.GetValueOrDefault(property.Name);
            if (field is null && _byName.TryGetValue(property.Name, out WpsFieldMap? byName)) field = byName;
            if (field is null) throw new WpsImportException(WpsImportFailure.Protocol,
                $"记录包含 schema 未声明的字段键：{property.Name}；来源结构已漂移，请重新读取。");
            JsonElement? converted = ConvertValue(field, property.Value);
            if (converted is { } value) values[field.Id] = value;
        }
        return values;
    }

    internal static JsonElement? ConvertValue(WpsFieldMap field, JsonElement value)
    {
        switch (field.WpsType)
        {
            case "SingleSelect":
            {
                if (value.ValueKind == JsonValueKind.String)
                    return JsonSerializer.SerializeToElement(ResolveOptionId(field, value.GetString()!));
                return value.Clone(); // 非字符串形状保留原值，由引擎值校验阻断。
            }
            case "MultipleSelect":
            {
                if (value.ValueKind == JsonValueKind.Array)
                {
                    List<string> ids = [];
                    foreach (JsonElement item in value.EnumerateArray())
                    {
                        if (item.ValueKind != JsonValueKind.String) return value.Clone();
                        ids.Add(ResolveOptionId(field, item.GetString()!));
                    }
                    return JsonSerializer.SerializeToElement(ids);
                }
                return value.Clone();
            }
            case "Link" when field.Relation is { Cardinality: "one" }:
            {
                if (value.ValueKind == JsonValueKind.Array)
                {
                    if (value.GetArrayLength() == 0) return null;
                    if (value.GetArrayLength() == 1 && value[0].ValueKind == JsonValueKind.String)
                        return value[0].Clone();
                }
                return value.Clone(); // 多值/异常形状交由引擎按关系校验阻断。
            }
            case "Url":
            {
                if (value.ValueKind == JsonValueKind.Object) return UrlAddress(value);
                if (value.ValueKind == JsonValueKind.Array)
                {
                    if (value.GetArrayLength() == 0) return null;
                    if (value.GetArrayLength() == 1 && value[0].ValueKind == JsonValueKind.Object)
                        return UrlAddress(value[0]);
                }
                return value.Clone();
            }
            case "Date" when field.Kind == "date":
            {
                if (value.ValueKind == JsonValueKind.String
                    && WpsDateFormat.TryParseDateOnly(value.GetString()!, out DateOnly date))
                    return JsonSerializer.SerializeToElement(
                        date.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture));
                return value.Clone(); // 解析失败保留原值，由引擎值校验阻断。
            }
            case "Time":
            {
                if (value.ValueKind == JsonValueKind.String
                    && WpsDateFormat.TryParseTime(value.GetString()!, out TimeOnly time))
                    return JsonSerializer.SerializeToElement(
                        time.ToString("HH:mm:ss", CultureInfo.InvariantCulture));
                return value.Clone();
            }
            default:
                return value.Clone();
        }
    }

    private static JsonElement UrlAddress(JsonElement link)
        => link.TryGetProperty("address", out JsonElement address)
            && address.ValueKind == JsonValueKind.String
            ? address.Clone() : link.Clone();

    /// <summary>
    /// 官方 prefer_id=true 声称字段与选项按 ID 标识，但未提供返回示例；因此同时
    /// 接受选项 ID 与选项值（label→ID 精确映射）。两者都不匹配时保留原值，由
    /// 引擎以「来源选项 ID 不存在」阻断，绝不按同名标签猜测。
    /// </summary>
    private static string ResolveOptionId(WpsFieldMap field, string raw)
    {
        foreach (HostSourceImportOption option in field.Options)
            if (string.Equals(option.Id, raw, StringComparison.Ordinal)) return raw;
        foreach (HostSourceImportOption option in field.Options)
            if (string.Equals(option.Label, raw, StringComparison.Ordinal)) return option.Id;
        return raw;
    }
}

/// <summary>从 WPS number_format（Excel 风格格式串）派生纯展示元数据；数值本身原样透传。</summary>
internal static class WpsNumberFormat
{
    internal static HostSourceImportNumberFormat? Parse(string format, bool percent)
    {
        if (string.IsNullOrWhiteSpace(format)) return null;
        string section = format.Split(';', 2)[0];
        int scale = 0;
        int dot = section.IndexOf('.', StringComparison.Ordinal);
        if (dot >= 0)
            for (int i = dot + 1; i < section.Length && (section[i] is '0' or '#'); i++)
                scale++;
        bool hasPlaceholder = section.Contains('#');
        return new(
            OnlyInt: dot < 0,
            DisplayScale: Math.Min(scale, 15),
            ScaleMode: hasPlaceholder ? "max" : "fixed",
            TrimTrailingZeros: hasPlaceholder,
            UseGrouping: section.Contains("#,##", StringComparison.Ordinal),
            Currency: CurrencyOf(section),
            PercentStorage: percent ? "percent" : "ratio",
            Unit: null);
    }

    private static string CurrencyOf(string section) => section switch
    {
        _ when section.Contains('$') => "USD",
        _ when section.Contains('¥') || section.Contains('￥') => "CNY",
        _ when section.Contains('€') => "EUR",
        _ => "",
    };
}

/// <summary>
/// 解析 WPS 日期/时间显示串。仅实现官方样例中出现的常见格式；不支持形状返回
/// false，对应字段值保持原样并交由引擎阻断，不猜测语义。
/// </summary>
internal static partial class WpsDateFormat
{
    [GeneratedRegex(@"\[\$-[0-9A-Fa-f]+\]")]
    private static partial Regex LocalePrefix();

    internal static bool HasTimeComponent(string format)
    {
        if (string.IsNullOrWhiteSpace(format)) return false;
        string section = Clean(format);
        return section.Contains('h') || section.Contains('H') || section.Contains('s')
            || section.Contains("AM", StringComparison.OrdinalIgnoreCase)
            || section.Contains("A/P", StringComparison.OrdinalIgnoreCase)
            || section.Contains("上午", StringComparison.Ordinal);
    }

    private static string Clean(string format)
    {
        string section = format.Split(';', 2)[0];
        section = LocalePrefix().Replace(section, "");
        // 星期记号（aaaa/ddd 等）是派生显示，不参与日期身份解析。
        section = Regex.Replace(section, "a{3,4}|d{3,4}", "");
        return section.TrimEnd('@').Trim();
    }

    internal static bool TryParseDateOnly(string text, out DateOnly date)
    {
        if (DateOnly.TryParseExact(text, ["yyyy-M-d", "yyyy/M/d", "yyyy年M月d日"],
                CultureInfo.InvariantCulture, DateTimeStyles.None, out date)) return true;
        // 未声明 number_format 时的保守兜底；无法识别的形状返回 false 并由引擎阻断。
        return DateOnly.TryParse(text, CultureInfo.InvariantCulture, DateTimeStyles.None, out date);
    }

    internal static bool TryParseTime(string text, out TimeOnly time)
    {
        if (TimeOnly.TryParseExact(text, ["H:m:s", "H:m", "h:m:s tt", "h:m tt"],
                CultureInfo.InvariantCulture, DateTimeStyles.None, out time)) return true;
        return TimeOnly.TryParse(text, CultureInfo.InvariantCulture, out time);
    }
}
