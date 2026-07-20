using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

/// <summary>
/// STA-bound Windows picker used only by the native host. Selected absolute
/// paths never enter the WebView message payload.
/// </summary>
public sealed class WindowsLocalDocumentFilePicker : ILocalDocumentFilePicker
{
    public async Task<string?> PickFileAsync(
        DocumentFilePickPurpose purpose,
        string? suggestedFileName,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        var application = Application.Current
            ?? throw new InvalidOperationException("WPF application is unavailable.");
        return await application.Dispatcher.InvokeAsync(() =>
        {
            token.ThrowIfCancellationRequested();
            var dialog = new OpenFileDialog
            {
                AddExtension = false,
                CheckFileExists = true,
                CheckPathExists = true,
                DereferenceLinks = false,
                Multiselect = false,
                RestoreDirectory = true,
                Title = purpose == DocumentFilePickPurpose.RelinkMissing
                    ? "重新定位缺失文件"
                    : "导入到 VibeTable 工作区",
                Filter = "所有文件 (*.*)|*.*",
            };
            if (!string.IsNullOrWhiteSpace(suggestedFileName))
                dialog.FileName = suggestedFileName;
            bool? accepted = application.MainWindow is null
                ? dialog.ShowDialog()
                : dialog.ShowDialog(application.MainWindow);
            return accepted == true ? dialog.FileName : null;
        });
    }
}
