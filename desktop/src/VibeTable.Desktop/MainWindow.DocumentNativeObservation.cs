using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Interop;
using System.Windows.Media;
using System.Windows.Threading;
using VibeTable.Desktop.Services;
using Brushes = System.Windows.Media.Brushes;
using Point = System.Windows.Point;

namespace VibeTable.Desktop;

public partial class MainWindow
{
    private DispatcherTimer? _documentNativeTimer;
    private Window? _documentNativeTarget;
    private JsonElement _documentNativeArm;
    private string? _documentNativeSource;

    private void StartDocumentNativeObservation()
    {
        if (_e2eControlsDir is null) return;
        _documentNativeTimer = new DispatcherTimer
        {
            Interval = TimeSpan.FromMilliseconds(100),
        };
        _documentNativeTimer.Tick += (_, _) =>
        {
            string armPath = Path.Combine(_e2eControlsDir, "document-native-arm.json");
            if (!IsVisible || !File.Exists(armPath)) return;
            try
            {
                UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(armPath);
                using JsonDocument arm = JsonDocument.Parse(File.ReadAllText(armPath));
                File.Delete(armPath);
                JsonElement value = arm.RootElement;
                if (!value.EnumerateObject().Select(property => property.Name).Order()
                    .SequenceEqual(new[] { "mode", "operationId", "relativePath", "workspaceId" }))
                    throw new InvalidDataException("Native observation arm has unknown fields.");
                Guid operationId = value.GetProperty("operationId").GetGuid();
                Guid workspaceId = value.GetProperty("workspaceId").GetGuid();
                string? relativePath = value.GetProperty("relativePath").GetString();
                string? mode = value.GetProperty("mode").GetString();
                WorkspaceDocumentBinding? binding = CurrentWorkspaceDocumentBinding();
                if (operationId == Guid.Empty || binding is null || binding.WorkspaceId != workspaceId ||
                    relativePath is null || Path.GetFileName(relativePath) != relativePath ||
                    !relativePath.StartsWith("document-native-", StringComparison.Ordinal) ||
                    !relativePath.EndsWith(".txt", StringComparison.Ordinal) ||
                    !Guid.TryParseExact(relativePath[16..^4], "D", out _) ||
                    mode is not ("copy" or "cancel"))
                    throw new InvalidDataException("Native observation arm is not synthetic.");
                string source = WorkspaceDocumentOsAdapter.ResolveWorkspacePath(
                    binding.Root, relativePath);
                string manifest = Path.Combine(binding.Root, ".vibetable", "workspace.json");
                string targetRoot = Path.Combine(_e2eControlsDir, "document-drop");
                UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(source, manifest, targetRoot);
                using JsonDocument identity = JsonDocument.Parse(File.ReadAllText(manifest));
                if (identity.RootElement.GetProperty("workspaceId").GetGuid() != workspaceId ||
                    File.ReadAllText(source) != $"VibeTable Task 415 native FileDocument\n{relativePath}\n")
                    throw new InvalidDataException("Native observation source identity changed.");
                _documentNativeTarget?.Close();
                _documentNativeArm = value.Clone();
                _documentNativeSource = source;
                Directory.CreateDirectory(targetRoot);
                var surface = new Border
                {
                    Background = Brushes.White,
                    Child = new TextBlock { Text = "QA FileDrop Copy", Margin = new Thickness(18) },
                    AllowDrop = true,
                };
                var target = new Window
                {
                    Title = $"VibeTable QA FileDrop · {operationId:N}",
                    Owner = this,
                    ShowInTaskbar = false,
                    ShowActivated = false,
                    ResizeMode = ResizeMode.NoResize,
                    Width = 240,
                    Height = 150,
                    Left = SystemParameters.WorkArea.Right - 250,
                    Top = SystemParameters.WorkArea.Bottom - 170,
                    Content = surface,
                };
                surface.DragOver += (_, args) =>
                {
                    args.Effects = mode == "copy" && args.Data.GetDataPresent(DataFormats.FileDrop)
                        ? DragDropEffects.Copy : DragDropEffects.None;
                    args.Handled = true;
                };
                surface.Drop += (_, args) =>
                {
                    args.Effects = DragDropEffects.None;
                    args.Handled = true;
                    string[] received = args.Data.GetData(DataFormats.FileDrop) as string[] ?? [];
                    string destination = Path.Combine(targetRoot, relativePath);
                    try
                    {
                        if (mode != "copy" || received.Length != 1 ||
                            !string.Equals(Path.GetFullPath(received[0]), source,
                                StringComparison.OrdinalIgnoreCase) ||
                            CurrentWorkspaceDocumentBinding()?.WorkspaceId != workspaceId)
                            throw new InvalidDataException("Unexpected FileDrop source.");
                        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(source, destination);
                        File.Copy(source, destination, overwrite: false);
                        args.Effects = DragDropEffects.Copy;
                        WriteDocumentNativeEvidence("document-native-drop-result.json", new
                        {
                            operationId, workspaceId, source, destination, received,
                            effect = "Copy", hostProcessId = Environment.ProcessId,
                        });
                    }
                    catch (Exception exception)
                    {
                        WriteDocumentNativeEvidence("document-native-drop-result.json", new
                        {
                            operationId, workspaceId, received, effect = "None",
                            error = exception.GetType().Name,
                        });
                    }
                };
                // Only the verified synthetic arm asks this Host to activate itself.
                // Windows may refuse; the input observer still requires this exact HWND.
                bool hostActivationAccepted = Activate();
                target.ContentRendered += (_, _) =>
                {
                    Point origin = AppWebView.PointToScreen(new Point());
                    Point center = surface.PointToScreen(new Point(
                        surface.ActualWidth / 2, surface.ActualHeight / 2));
                    DpiScale dpi = VisualTreeHelper.GetDpi(AppWebView);
                    WriteDocumentNativeEvidence("document-native-target.json", new
                    {
                        operationId, workspaceId, source, mode,
                        hostProcessId = Environment.ProcessId,
                        hostActivationAccepted,
                        hostHwnd = new WindowInteropHelper(this).Handle.ToInt64(),
                        targetHwnd = new WindowInteropHelper(target).Handle.ToInt64(),
                        targetX = center.X, targetY = center.Y,
                        webviewX = origin.X, webviewY = origin.Y,
                        webviewWidth = AppWebView.ActualWidth,
                        webviewHeight = AppWebView.ActualHeight,
                        scaleX = dpi.DpiScaleX, scaleY = dpi.DpiScaleY,
                    });
                };
                _documentNativeTarget = target;
                target.Closed += (_, _) =>
                {
                    if (ReferenceEquals(_documentNativeTarget, target)) _documentNativeTarget = null;
                };
                target.Show();
            }
            catch (Exception exception)
            {
                _documentNativeSource = null;
                _readiness?.Trace($"Document native arm rejected: {exception.GetType().Name}");
                WriteDocumentNativeEvidence("document-native-target.json", new
                {
                    error = exception.GetType().Name,
                    hostProcessId = Environment.ProcessId,
                });
            }
        };
        _documentNativeTimer.Start();
    }

    private void RecordDocumentNativeDrag(string source, string phase, DragDropEffects? effect)
    {
        if (_documentNativeSource is null ||
            !string.Equals(source, _documentNativeSource, StringComparison.OrdinalIgnoreCase)) return;
        WriteDocumentNativeEvidence($"document-native-drag-{phase}.json", new
        {
            operationId = _documentNativeArm.GetProperty("operationId").GetGuid(),
            workspaceId = _documentNativeArm.GetProperty("workspaceId").GetGuid(),
            mode = _documentNativeArm.GetProperty("mode").GetString(),
            source, effect = effect?.ToString(), hostProcessId = Environment.ProcessId,
        });
    }

    private void WriteDocumentNativeEvidence(string name, object payload)
    {
        if (_e2eControlsDir is null) return;
        string destination = Path.Combine(_e2eControlsDir, name);
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(destination);
        string temporary = destination + ".tmp";
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(temporary);
        File.WriteAllText(temporary, JsonSerializer.Serialize(payload));
        File.Move(temporary, destination, overwrite: true);
    }

    private void StopDocumentNativeObservation()
    {
        _documentNativeTimer?.Stop();
        _documentNativeTarget?.Close();
        _documentNativeTarget = null;
        _documentNativeSource = null;
    }
}
