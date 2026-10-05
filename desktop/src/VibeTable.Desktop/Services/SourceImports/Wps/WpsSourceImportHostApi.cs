using System.Net.Http;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Host 原生 WPS 导入向导的接线门面。Vue 只提供 provider 选择；凭据输入、选表、
/// 预检与确认全部在 Host 侧用下列 API 组合：
///   1. CreateConnection —— file_id + 显示名 + 内存 access token（原生 UI 传入），
///      可选 SignatureCredentials（应用在开发者后台开启「接口签名」时才提供）；
///   2. ReadCatalogAsync —— 选表前的连接测试与目录：官方 schema + 每表字段映射详情
///      （Kind/ValueKind/Definition 供向导做快照/跳过降级决策）；不需要选定表，
///      也不读取任何记录；
///   3. CreateProvider(connection, 选定表ID集合) —— 仅读取所选表的记录，实现
///      IHostSourceImportProvider，交给 HostDataIoTaskRegistry 既有 #435 通道
///      （RegisterSourceImportProvider / PrepareSourceImportAsync / StartSourceImport）。
///
/// 目录浏览不在本适配器范围内：盘列表官方标注权限为 kso.drive.readwrite（只读导入
/// 不可扩权），盘内文件级列表（yundoc/file/list_file、search_files）官方正文不可得、
/// 响应形状未核实；分享链接 link_id → file_id 解析同样 UNVERIFIED。向导只接受用户
/// 直接提供的多维表格 file_id，解析路径未经验证、不猜测。
/// </summary>
internal static class WpsSourceImportHostApi
{
    /// <summary>创建连接配置；file_id 必须是多维表格文件的 file_id，不是分享 link_id。</summary>
    internal static WpsSourceImportConnection CreateConnection(string fileId, string displayName,
        string accessToken, WpsKso1Credentials? signature = null, string? apiBase = null)
        => new(fileId, displayName, accessToken, signature, apiBase ?? WpsSourceImportConnection.DefaultApiBase);

    /// <summary>
    /// 应用开启「接口签名」时的 KSO-1 凭据；传 null（或不调用）则请求不携带
    /// X-Kso-Date / X-Kso-Authorization（官方调用流程第 6 条：未开启时不要携带）。
    /// 只填一项是配置错误，封闭报错而不是静默降级为不签名。
    /// </summary>
    internal static WpsKso1Credentials? SignatureCredentials(string? accessKey, string? secretKey)
    {
        bool hasAccessKey = !string.IsNullOrWhiteSpace(accessKey);
        bool hasSecretKey = !string.IsNullOrWhiteSpace(secretKey);
        if (hasAccessKey != hasSecretKey)
            throw new ArgumentException(
                "KSO-1 签名配置不完整：AccessKey 与 SecretKey 必须成对提供；未开启签名时两者都留空。");
        return hasAccessKey ? new WpsKso1Credentials(accessKey!, secretKey!) : null;
    }

    /// <summary>选表前的连接测试与目录读取；不构造 provider、不需要选定表、不读取记录。</summary>
    internal static async Task<WpsCatalog> ReadCatalogAsync(WpsSourceImportConnection connection,
        WpsReadLimits? limits = null, HttpMessageHandler? handler = null,
        Func<TimeSpan, CancellationToken, Task>? retryDelay = null, TimeSpan? requestTimeout = null,
        CancellationToken token = default)
    {
        using var client = new WpsOpenApiClient(connection, handler, retryDelay, requestTimeout);
        return await WpsSourceImportProvider.ReadCatalogCoreAsync(client, connection,
            limits ?? WpsReadLimits.Default, token).ConfigureAwait(false);
    }

    internal static WpsSourceImportProvider CreateProvider(WpsSourceImportConnection connection,
        IReadOnlyCollection<string> selectedTableIds, WpsReadLimits? limits = null,
        HttpMessageHandler? handler = null, Func<TimeSpan, CancellationToken, Task>? retryDelay = null,
        TimeSpan? requestTimeout = null)
        => new(connection, selectedTableIds, limits, handler, retryDelay, requestTimeout);

    internal static string DescribeSupport()
        => "WPS 365 多维表格（dbsheet）只读导入：schema/选定表记录读取、游标分页、KSO-1 可选签名、"
            + "429 有界重试已按官方文档实现（合成回放验证）；附件下载、分享链接解析、盘/文件目录浏览"
            + "缺少官方端点/响应证据或超出只读最小权限，显式阻断为 unsupported（EVIDENCE_MISSING），"
            + "live 联调未授权。";
}
