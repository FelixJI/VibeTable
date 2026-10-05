using VibeTable.Infrastructure.Rpc;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

public partial class MainWindow
{
    private Task<HostSourceImportOpenResult> OpenSourceImportAsync(
        string provider, Action ensureCurrent, CancellationToken token)
    {
        ensureCurrent();
        HostProductRpcBinding binding = _runtime.CaptureHostProductRpcBinding()
            ?? throw new BackendUnavailableException("Source import workspace is unavailable.");
        using IHostSourceImportWizardSession session =
            binding.CreateSourceImportWizardSession(_workspaceSessionFilter, ensureCurrent);
        var window = new SourceImportWindow(provider,
            (source, accessToken, key, secret, cancellation) =>
                ConnectSourceImportAsync(provider, source, accessToken, key, secret, cancellation),
            session, DescribeSourceImportError, token) { Owner = this };
        window.ShowDialog();
        return Task.FromResult(window.Result);
    }

    private static async Task<NativeSourceConnection> ConnectSourceImportAsync(
        string provider, string source, string accessToken, string key, string secret,
        CancellationToken token)
    {
        if (provider == "feishu")
        {
            FeishuSourceImportConnection connection = await FeishuSourceImportConnector.ConnectAsync(
                source, accessToken, token: token);
            return new(connection.Catalog.AppName, connection.Catalog.Tables.Select(table =>
                new NativeSourceTable(table.TableId, table.Name, table.Fields.Select(field =>
                    new NativeSourceField(field.FieldId, field.Name, field.Kind, field.ValueKind)).ToArray())).ToArray(),
                ids => connection.CreateProvider(ids), connection.Dispose);
        }
        if (provider == "wps")
        {
            WpsSourceImportConnection connection = WpsSourceImportHostApi.CreateConnection(
                source, "WPS 多维表格", accessToken, WpsSourceImportHostApi.SignatureCredentials(key, secret));
            WpsCatalog catalog = await WpsSourceImportHostApi.ReadCatalogAsync(connection, token: token);
            return new(connection.DisplayName, catalog.Tables.Select(table =>
                new NativeSourceTable(table.Id, table.Name, table.Fields.Select(field =>
                    new NativeSourceField(field.Id, field.Name, field.Kind, field.ValueKind)).ToArray())).ToArray(),
                ids => WpsSourceImportHostApi.CreateProvider(connection, ids), () => { });
        }
        throw new ArgumentException("Unknown source provider.", nameof(provider));
    }

    private static string DescribeSourceImportError(Exception error) => error switch
    {
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.InvalidInput } =>
            "飞书来源地址无效。请使用国内飞书 Base/Wiki 链接或明确的 app_token；Wiki 需单独读取权限且节点必须是多维表格。",
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.InvalidToken } =>
            "飞书访问令牌无效或已过期，请重新输入只读授权的令牌。",
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.Forbidden } =>
            "当前飞书授权无权读取该资源。请核对应用权限和文档授权，不能将无权限视为空表。",
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.NotFound } =>
            "飞书来源不存在、已删除或不在当前授权范围内，请核对来源。",
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.RateLimited } =>
            "飞书限流且有界重试已用尽，请稍后重新连接。",
        WpsImportException { Failure: WpsImportFailure.Authentication } =>
            "WPS 访问令牌无效或已过期，请重新输入只读授权的令牌。",
        WpsImportException { Failure: WpsImportFailure.Permission } =>
            "当前 WPS 授权无权读取该资源。请核对文件授权及 kso.dbsheet.read 权限。",
        WpsImportException { Failure: WpsImportFailure.Unsupported } =>
            "该 WPS 读取能力尚不支持。分享链接解析与附件下载缺少已验证的官方路径，不能视为迁移完成。",
        WpsImportException { Failure: WpsImportFailure.RateLimited } =>
            "WPS 限流且有界重试已用尽，请稍后重新连接。",
        ArgumentException => "来源或签名配置无效。WPS 只接受明确 file_id；签名必须同时提供 access key 与 secret key，未启用时两项留空。",
        _ => "无法完整读取来源或完成预检。请核对来源类型、只读授权和网络后重新连接；未确认的导入不会写入数据。",
    };
}
