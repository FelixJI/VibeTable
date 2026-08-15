using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop;

public partial class MainWindow
{
    private async Task HandleDiagnosticsGetAsync(RoutedWebRequest request)
    {
        try
        {
            JsonElement? workspaceSummary = await ReadWorkspaceDiagnosticsAsync(
                _session.Token);
            using Process process = Process.GetCurrentProcess();
            PocketBaseSupervisor? sidecar = _runtime.CurrentSidecar;
            PythonBackendSupervisor? backend = _runtime.CurrentBackend;
            RuntimeDiagnosticsBundle bundle = RuntimeDiagnosticsBundleService.Build(
                new RuntimeDiagnosticsInput(
                    RuntimeInformation.OSDescription,
                    ApplicationVersion.FromAssembly(typeof(MainWindow).Assembly),
                    Environment.Version.ToString(),
                    _runtime.CurrentPocketBaseVersion ?? "not-started",
                    process.WorkingSet64,
                    _viewModel.State.ToString(),
                    sidecar?.GetStatus().State.ToString() ?? "Stopped",
                    backend?.State.ToString() ?? "Stopped",
                    ReadDesktopDiagnosticLog(),
                    sidecar?.GetSanitizedLog() ?? string.Empty,
                    backend?.GetStdErrorLog() ?? string.Empty,
                    workspaceSummary));
            _webBridge.PostResponse(request.Type, request.RequestId, bundle);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (Exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "无法生成诊断包。",
                "DIAGNOSTICS_BUILD_FAILED");
        }
    }

    private static string ReadDesktopDiagnosticLog()
    {
        try
        {
            string path = Path.Combine(App.DesktopLogDirectory(), "desktop.log");
            if (!File.Exists(path)) return string.Empty;
            const int maximumBytes = 512 * 1024;
            using var stream = new FileStream(
                path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
            stream.Seek(Math.Max(0, stream.Length - maximumBytes), SeekOrigin.Begin);
            using var reader = new StreamReader(stream);
            return reader.ReadToEnd();
        }
        catch (IOException) { return string.Empty; }
        catch (UnauthorizedAccessException) { return string.Empty; }
    }

    private async Task<JsonElement?> ReadWorkspaceDiagnosticsAsync(
        CancellationToken cancellationToken)
    {
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        WorkspaceRegistryEntryV2? workspace = _runtime.CurrentWorkspace;
        WorkspaceV2HttpGateway? gateway = _runtime.CurrentV2Gateway;
        if (session.WorkspaceId is not Guid workspaceId ||
            session.SessionEpoch == 0 ||
            workspace?.WorkspaceId != workspaceId ||
            gateway is null ||
            _runtime.CurrentCapabilities?.RpcMethods.Contains(
                "workspaceDiagnostics.get",
                StringComparer.Ordinal) != true)
            return null;
        Guid operationId = Guid.NewGuid();
        ulong sequence = _workspaceSessionFilter.ReserveHostSequence(
            workspaceId,
            session.SessionEpoch);
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch = session.SessionEpoch,
            operationId = operationId.ToString("D"),
            sequence,
        });
        WorkspaceV2ForwardResult result = await gateway.ForwardAsync(
            "desktop-diagnostics-" + operationId.ToString("N"),
            "workspaceDiagnostics.get",
            wire,
            JsonSerializer.SerializeToElement(new { }),
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (result.Error is not null)
            throw new InvalidOperationException(result.Error.Code);
        return result.Result;
    }
}
