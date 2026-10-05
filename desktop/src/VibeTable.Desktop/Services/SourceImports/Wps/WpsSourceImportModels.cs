using System.Net.Http;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// WPS 365 多维表格（dbsheet）只读导入连接配置。Access token 仅由 Host 在内存中
/// 持有并经原生 UI 传入；不来自渲染层、环境变量或磁盘，也不会被写入日志或异常。
/// 凭据载体是普通 sealed class（非 record），ToString 固定脱敏，防止默认编译器
/// 生成的 ToString 泄露 token/密钥。API 目标锁定为官方 https://openapi.wps.cn。
/// </summary>
internal sealed class WpsSourceImportConnection
{
    internal const string ProviderName = "wps";
    internal const string DefaultApiBase = "https://openapi.wps.cn";

    internal WpsSourceImportConnection(string fileId, string displayName, string accessToken,
        WpsKso1Credentials? signature, string apiBase = DefaultApiBase)
    {
        if (string.IsNullOrWhiteSpace(fileId) || fileId.Trim() != fileId
            || fileId.Contains('/') || fileId.Contains('\\') || fileId.Contains(':'))
            throw new ArgumentException(
                "WPS file_id 无效。仅接受多维表格文件的 file_id；分享链接中的 link_id 不能当作 file_id 使用，" +
                "且分享链接解析暂无官方只读接口证据，当前不支持自动解析。", nameof(fileId));
        if (string.IsNullOrWhiteSpace(displayName))
            throw new ArgumentException("来源显示名不能为空。", nameof(displayName));
        if (string.IsNullOrWhiteSpace(accessToken))
            throw new ArgumentException("WPS access token 不能为空。", nameof(accessToken));
        signature?.Validate();
        FileId = fileId;
        DisplayName = displayName;
        AccessToken = accessToken;
        Signature = signature;
        ApiBase = ValidateApiBase(apiBase);
    }

    internal string FileId { get; }
    internal string DisplayName { get; }
    internal string AccessToken { get; }
    internal WpsKso1Credentials? Signature { get; }
    internal Uri ApiBase { get; }

    public override string ToString()
        => $"WpsSourceImportConnection[provider={ProviderName} fileId={FileId} "
            + $"displayName={DisplayName} accessToken=<redacted> signature={(Signature is null ? "off" : "on")}]";

    private static Uri ValidateApiBase(string apiBase)
    {
        // 凭据只能发往官方 OpenAPI 域；拒 userInfo、非默认端口、额外路径、query、
        // fragment 与任何其他域名，防止配置改写凭据投递目标。
        if (!Uri.TryCreate(apiBase, UriKind.Absolute, out Uri? uri)
            || uri.Scheme != Uri.UriSchemeHttps
            || uri.Host != "openapi.wps.cn"
            || !uri.IsDefaultPort
            || uri.UserInfo.Length > 0
            || uri.AbsolutePath != "/"
            || uri.Query.Length > 0
            || uri.Fragment.Length > 0)
            throw new ArgumentException(
                "WPS OpenAPI 地址被锁定为 https://openapi.wps.cn；不允许 userInfo、端口、路径、query、fragment 或其他域名。",
                nameof(apiBase));
        return uri;
    }
}

/// <summary>
/// KSO-1 签名凭据。仅在应用于开发者后台开启「接口签名」时由 Host 提供；
/// 未开启时不携带 X-Kso-Date / X-Kso-Authorization（官方调用流程第 6 条）。
/// 普通 sealed class + 脱敏 ToString，防止默认 ToString 泄露密钥。
/// </summary>
internal sealed class WpsKso1Credentials(string accessKey, string secretKey)
{
    internal string AccessKey { get; } = accessKey;
    internal string SecretKey { get; } = secretKey;

    internal void Validate()
    {
        if (string.IsNullOrWhiteSpace(AccessKey) || string.IsNullOrWhiteSpace(SecretKey))
            throw new ArgumentException("KSO-1 签名的 AccessKey/SecretKey 不能为空。");
    }

    public override string ToString() => "WpsKso1Credentials[accessKey=<redacted> secretKey=<redacted>]";
}

/// <summary>Provider 侧读取上限，全部在引擎容量内提前失败，绝不静默截断。</summary>
internal sealed record WpsReadLimits
{
    // 与 sidecar sourceimport 引擎容量保持一致：MaxTables=100、MaxRecords=50000、
    // MaxFields=512。读取越界直接失败并给出可行动信息，而不是丢弃后续页。
    internal static WpsReadLimits Default { get; } = new(100, 50_000, 512, 500);

    internal WpsReadLimits(int maxTables, int maxRecords, int maxFieldsPerSheet, int pageSize)
    {
        if (maxTables < 1 || maxRecords < 1 || maxFieldsPerSheet < 1 || pageSize is < 1 or > 1000)
            throw new ArgumentException("WPS 读取上限配置无效；page_size 官方取值范围 1-1000。");
        MaxTables = maxTables;
        MaxRecords = maxRecords;
        MaxFieldsPerSheet = maxFieldsPerSheet;
        PageSize = pageSize;
    }

    internal int MaxTables { get; }
    internal int MaxRecords { get; }
    internal int MaxFieldsPerSheet { get; }
    internal int PageSize { get; }
}

internal enum WpsImportFailure
{
    Connection,
    Authentication,
    Permission,
    RateLimited,
    Capacity,
    Pagination,
    Protocol,
    Unsupported,
    Cancelled,
}

/// <summary>
/// WPS 只读访问失败。异常文本只包含固定文案与数值错误码；服务器 msg 可能回显
/// token/URL 等敏感内容，绝不进入异常、日志或报告。
/// </summary>
internal sealed class WpsImportException : Exception
{
    internal WpsImportException(WpsImportFailure failure, string message, long? businessCode = null)
        : base(message)
    {
        Failure = failure;
        BusinessCode = businessCode;
    }

    internal WpsImportFailure Failure { get; }
    internal long? BusinessCode { get; }
}

/// <summary>官方 GET /v7/coop/dbsheet/{file_id}/schema 的受控摘要（连接测试与选表入口）。</summary>
internal sealed record WpsFieldSummary(string Id, string Name, string WpsType, string Kind,
    string ValueKind, string Definition, int OptionCount, string RelationTargetTableId,
    string RelationTargetFieldId, string RelationCardinality);

internal sealed record WpsTableSummary(string Id, string Name, string PrimaryFieldId,
    IReadOnlyList<WpsFieldSummary> Fields, int ViewCount);

internal sealed record WpsCatalog(string FileId, string Version, DateTimeOffset ReadAt,
    IReadOnlyList<WpsTableSummary> Tables);

/// <summary>解析后的官方 schema 形状；字段保留原始 JSON 供映射与结构指纹使用。</summary>
internal sealed record WpsSchemaSheet(
    string Id, string Name, string PrimaryFieldId, JsonElement Fields, JsonElement Views)
{
    internal JsonElement CloneFields() => Fields.Clone();
}
