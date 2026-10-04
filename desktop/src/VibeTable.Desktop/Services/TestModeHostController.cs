using System.IO;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public sealed record TestModeHostState(
    bool WindowVisible,
    bool TrayVisible,
    WorkspaceSessionV2 Session);

public interface ITestModeHost
{
    bool CanDispatch { get; }

    void Schedule(Func<Task> action);

    void RequestExit();

    void CloseWindow();

    Task OpenWorkspaceAsync(Guid workspaceId, CancellationToken cancellationToken);

    TestModeHostState CaptureState();

    void Trace(string message);
}

/// <summary>
/// Owns the packaged-host test control protocol: polling, one-shot request
/// consumption, UI scheduling, workspace open reporting, and atomic evidence.
/// The WPF host supplies lifecycle actions but never interprets control files.
/// </summary>
public sealed class TestModeHostController : IDisposable
{
    private static readonly TimeSpan PollInterval = TimeSpan.FromMilliseconds(100);
    private readonly string _controlsRoot;
    private readonly ITestModeHost _host;
    private readonly Func<CancellationToken> _sessionToken;
    private readonly Func<string, CancellationToken, Task<JsonElement>>? _sourceImport;
    private readonly Timer _timer;
    private int _checking;
    private int _disposed;
    private int _sourceImportRunning;

    public TestModeHostController(
        string controlsRoot,
        ITestModeHost host,
        Func<CancellationToken>? sessionToken = null,
        Func<string, CancellationToken, Task<JsonElement>>? sourceImport = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(controlsRoot);
        _controlsRoot = Path.GetFullPath(controlsRoot);
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
        _sourceImport = sourceImport;
        _timer = new Timer(
            _ => Check(),
            null,
            PollInterval,
            PollInterval);
    }

    public void ReportStartupVisibility(bool startHidden) =>
        WriteState(startHidden ? "silent-startup" : "visible-startup");

    internal void Check()
    {
        if (Volatile.Read(ref _disposed) != 0
            || !_host.CanDispatch
            || Interlocked.Exchange(ref _checking, 1) != 0)
        {
            return;
        }
        try
        {
            if (TryConsume("host-normal-close.request"))
            {
                Dispatch("normal close", () => _host.RequestExit());
                return;
            }
            if (TryConsume("host-window-close.request"))
            {
                Dispatch(
                    "window close",
                    () =>
                    {
                        _host.CloseWindow();
                        WriteState("close-to-tray");
                    });
                return;
            }
            if (TryConsume("host-tray-exit.request"))
            {
                Dispatch(
                    "tray exit",
                    () =>
                    {
                        WriteState("tray-exit-requested");
                        _host.RequestExit();
                    });
                return;
            }
            TryOpenWorkspace();
            TrySourceImport();
        }
        finally
        {
            Volatile.Write(ref _checking, 0);
        }
    }

    private void TrySourceImport()
    {
        if (_sourceImport is null || Volatile.Read(ref _sourceImportRunning) != 0) return;
        string path = Path.Combine(_controlsRoot, "host-source-import.request");
        if (!File.Exists(path)) return;
        string scenario;
        try
        {
            scenario = new FileInfo(path).Length <= 64 ? File.ReadAllText(path).Trim() : "";
            File.Delete(path);
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
        {
            _host.Trace($"TestModeHostController: source import control rejected: {exception.GetType().Name}");
            return;
        }
        if (!TestModeSourceImport.IsScenario(scenario))
        {
            WriteSourceImportState("source-import-failed", "invalid", null, "invalid scenario");
            return;
        }
        Interlocked.Exchange(ref _sourceImportRunning, 1);
        if (!Dispatch("source import", async () =>
        {
            TestModeHostState origin = _host.CaptureState();
            try
            {
                JsonElement status = await _sourceImport(scenario, _sessionToken()).ConfigureAwait(true);
                WriteSourceImportState("source-import-completed", scenario, status, null, origin);
            }
            catch (Exception exception)
            {
                // Evidence contains no provider payload, secret or address.
                WriteSourceImportState("source-import-failed", scenario, null, exception.GetType().Name, origin);
            }
            finally { Volatile.Write(ref _sourceImportRunning, 0); }
        })) Volatile.Write(ref _sourceImportRunning, 0);
    }

    private void WriteSourceImportState(string action, string scenario, JsonElement? status, string? error,
        TestModeHostState? origin = null)
    {
        TestModeHostState host = origin ?? _host.CaptureState();
        WriteEvidence("host-source-import-state.json", new
        {
            evidenceKind = "packaged-host-source-import",
            action,
            scenario,
            workspaceId = host.Session.WorkspaceId,
            sessionEpoch = host.Session.SessionEpoch,
            taskId = status?.GetProperty("taskId").GetString(),
            state = status?.GetProperty("state").GetString(),
            error,
        });
    }

    private void TryOpenWorkspace()
    {
        string controlPath = Path.Combine(
            _controlsRoot,
            "host-open-workspace.request");
        if (!File.Exists(controlPath)) return;
        string workspaceText;
        try
        {
            workspaceText = File.ReadAllText(controlPath).Trim();
            File.Delete(controlPath);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            _host.Trace(
                $"TestModeHostController: workspace open control rejected: {exception.GetType().Name}");
            return;
        }
        if (!Guid.TryParse(workspaceText, out Guid workspaceId))
        {
            WriteState(
                "workspace-open-failed",
                "workspace ID control is invalid");
            return;
        }
        Dispatch(
            "workspace open",
            async () =>
            {
                try
                {
                    await _host.OpenWorkspaceAsync(
                        workspaceId,
                        _sessionToken()).ConfigureAwait(true);
                    WriteState("workspace-opened");
                }
                catch (Exception exception)
                {
                    WriteState(
                        "workspace-open-failed",
                        $"{exception.GetType().Name}: {exception.Message}");
                }
            });
    }

    private bool TryConsume(string controlName)
    {
        string controlPath = Path.Combine(_controlsRoot, controlName);
        if (!File.Exists(controlPath)) return false;
        try
        {
            File.Delete(controlPath);
            return true;
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            _host.Trace(
                $"TestModeHostController: {controlName} rejected: {exception.GetType().Name}");
            return false;
        }
    }

    private void Dispatch(string action, Action callback) =>
        Dispatch(
            action,
            () =>
            {
                callback();
                return Task.CompletedTask;
            });

    private bool Dispatch(string action, Func<Task> callback)
    {
        _host.Trace($"TestModeHostController: {action} requested");
        try
        {
            _host.Schedule(callback);
            return true;
        }
        catch (InvalidOperationException exception)
        {
            _host.Trace(
                $"TestModeHostController: {action} dispatch rejected: {exception.GetType().Name}");
            return false;
        }
    }

    private void WriteState(string action, string? error = null)
    {
        TestModeHostState state = _host.CaptureState();
        bool hasWorkspaceSession = state.Session.WorkspaceId is not null;
        var payload = new
        {
            evidenceKind = "packaged-host-control",
            action,
            hostExecutable = Path.GetFileName(Environment.ProcessPath),
            hostProcessId = Environment.ProcessId,
            windowVisible = state.WindowVisible,
            trayVisible = state.TrayVisible,
            workspaceId = state.Session.WorkspaceId,
            sessionEpoch = hasWorkspaceSession
                ? state.Session.SessionEpoch
                : (ulong?)null,
            sessionState = !hasWorkspaceSession
                ? null
                : JsonNamingPolicy.CamelCase.ConvertName(
                    state.Session.State.ToString()),
            error,
        };
        WriteEvidence("host-lifecycle-state.json", payload);
    }

    private void WriteEvidence(string fileName, object payload)
    {
        string destination = Path.Combine(_controlsRoot, fileName);
        string temporary = destination + $".{Guid.NewGuid():N}.tmp";
        try
        {
            File.WriteAllText(
                temporary,
                JsonSerializer.Serialize(payload, WorkspaceV2Json.StrictOptions));
            for (int attempt = 0; ; attempt++)
            {
                try
                {
                    File.Move(temporary, destination, overwrite: true);
                    break;
                }
                catch (Exception exception) when (
                    attempt < 19
                    && exception is IOException or UnauthorizedAccessException
                    && (exception.HResult & 0xffff) is 5 or 32 or 33)
                {
                    // Windows readers (including Python's read_text) do not share
                    // delete access. Keep the completed snapshot until the short
                    // read closes; never repeat the workspace operation itself.
                    if (attempt == 0)
                        _host.Trace("TestModeHostController: state write temporarily blocked; retrying");
                    Thread.Sleep(10);
                }
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            try { File.Delete(temporary); } catch { }
            _host.Trace(
                $"TestModeHostController: state write rejected: {exception.GetType().Name}");
        }
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _timer.Dispose();
    }
}

public sealed class TestModeHost(
    Func<bool> canDispatch,
    Action<Func<Task>> schedule,
    Action requestExit,
    Action closeWindow,
    Func<Guid, CancellationToken, Task> openWorkspace,
    Func<TestModeHostState> captureState,
    Action<string> trace) : ITestModeHost
{
    public bool CanDispatch => canDispatch();

    public void Schedule(Func<Task> action) => schedule(action);

    public void RequestExit() => requestExit();

    public void CloseWindow() => closeWindow();

    public Task OpenWorkspaceAsync(
        Guid workspaceId,
        CancellationToken cancellationToken) =>
        openWorkspace(workspaceId, cancellationToken);

    public TestModeHostState CaptureState() => captureState();

    public void Trace(string message) => trace(message);
}
