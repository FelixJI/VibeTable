using System.IO;
using System.Windows;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

public partial class MainWindow
{
    private Task<HostSourceImportOpenResult> OpenSourceImportAsync(
        string provider, Action ensureCurrent, CancellationToken token)
    {
        if (provider != HostSourceImportRequestController.BaseFileProvider)
            throw new ArgumentException("Unknown source import provider.", nameof(provider));
        ensureCurrent();
        HostProductRpcBinding binding = _runtime.CaptureHostProductRpcBinding()
            ?? throw new BackendUnavailableException("Source import workspace is unavailable.");
        using IHostSourceImportWizardSession session =
            binding.CreateSourceImportWizardSession(_workspaceSessionFilter, ensureCurrent);
        // The wizard's single entry is the Host-owned .base file picker: no
        // credentials exist on this path and no source text is ever typed.
        // The window itself is the picker's explicit native owner.
        var window = new SourceImportWindow(
            (owner, cancellation) => OpenFeishuBaseFileAsync(owner, cancellation),
            session, DescribeSourceImportError, token, _appearance) { Owner = this };
        window.ShowDialog();
        return Task.FromResult(window.Result);
    }

    private static async Task<NativeSourceConnection?> OpenFeishuBaseFileAsync(
        Window owner, CancellationToken token)
    {
        string? path = await FeishuBaseFileDialog.PickFileAsync(owner, token);
        if (path is null) return null;
        token.ThrowIfCancellationRequested();
        FeishuBaseFileDocument document = await FeishuBaseFileSource.ReadFileAsync(path, token);
        token.ThrowIfCancellationRequested();
        return new(document.Snapshot.DisplayName, document.Snapshot.Tables.Select(table =>
            new NativeSourceTable(table.Id, table.Name, table.Fields.Select(field =>
                // The window's generic contract renders a reverse-name input
                // only for one-sided native relations; the .base parser has no
                // verified relation mapping yet, so every field stays
                // RequiresReverseName=false today and the gate stays hidden.
                new NativeSourceField(field.Id, field.Name, field.Kind, field.ValueKind,
                    field.Kind == "relation" && field.Relation is { TargetFieldId: "" })).ToArray())).ToArray(),
            document.CreateProvider, () => { }, Notices: document.Notices);
    }

    private static string DescribeSourceImportError(Exception error) => error switch
    {
        InvalidDataException => "无法读取 .base 文件：文件损坏、超过读取上限或包含尚未支持的结构，请重新导出并核对格式。",
        IOException or UnauthorizedAccessException => "无法打开所选文件，请检查文件是否存在及读取权限后重试。",
        FeishuBaseFileDialogException picker => picker.Message,
        FeishuSourceImportException { Kind: FeishuSourceImportErrorKind.NotFound } =>
            "所选表不在本地 .base 文件中；请重新选择文件。",
        ArgumentException => "导入请求无效。",
        _ => "无法完整读取来源或完成预检。请核对 .base 文件后重试；未确认的导入不会写入数据。",
    };
}
