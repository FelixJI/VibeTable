using System.Diagnostics;
using System.IO;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

public interface ILocalDocumentPreview : IDisposable
{
    bool CanPreview(string fullPath);
    void Show(string fullPath);
}

/// <summary>
/// Resolves the same per-file-type preview-handler registration used by
/// Windows Explorer. This class is read-only and never modifies associations.
/// </summary>
public sealed class ShellPreviewHandlerResolver
{
    public const string PreviewHandlerAssociation =
        "{8895b1c6-b41f-4c1c-a562-0d564250836f}";

    private readonly Func<string, string?> _readDefaultValue;

    public ShellPreviewHandlerResolver(Func<string, string?>? readDefaultValue = null)
    {
        _readDefaultValue = readDefaultValue ?? ReadClassesRootDefaultValue;
    }

    public Guid? Resolve(string fileName)
    {
        string extension = Path.GetExtension(fileName);
        if (string.IsNullOrWhiteSpace(extension)) return null;

        foreach (string key in CandidateKeys(extension))
        {
            string? raw = _readDefaultValue(key);
            if (Guid.TryParse(raw, out Guid clsid)) return clsid;
        }
        return null;
    }

    private IEnumerable<string> CandidateKeys(string extension)
    {
        yield return $@"{extension}\shellex\{PreviewHandlerAssociation}";
        yield return $@"SystemFileAssociations\{extension}\shellex\{PreviewHandlerAssociation}";

        string? progId = _readDefaultValue(extension);
        if (!string.IsNullOrWhiteSpace(progId))
            yield return $@"{progId}\shellex\{PreviewHandlerAssociation}";
    }

    private static string? ReadClassesRootDefaultValue(string subKey)
    {
        using var key = Registry.ClassesRoot.OpenSubKey(subKey, writable: false);
        return key?.GetValue(null) as string;
    }
}

/// <summary>
/// Owns one out-of-process shell preview at a time. Process.Start with
/// UseShellExecute=false inherits the current user's token: this isolates
/// third-party COM failures from the main WPF process, but is not AppContainer
/// or a lower-privilege security sandbox.
/// </summary>
public sealed class ShellDocumentPreview : ILocalDocumentPreview
{
    private const int PreviewStartupTimeoutMilliseconds = 8_000;
    private readonly object _gate = new();
    private readonly ShellPreviewHandlerResolver _resolver;
    private readonly string _appBaseDirectory;
    private Process? _process;
    private bool _disposed;

    public ShellDocumentPreview(ShellPreviewHandlerResolver? resolver = null)
        : this(resolver ?? new ShellPreviewHandlerResolver(), AppContext.BaseDirectory)
    {
    }

    internal ShellDocumentPreview(
        ShellPreviewHandlerResolver resolver,
        string appBaseDirectory)
    {
        _resolver = resolver ?? throw new ArgumentNullException(nameof(resolver));
        ArgumentException.ThrowIfNullOrWhiteSpace(appBaseDirectory);
        _appBaseDirectory = Path.GetFullPath(appBaseDirectory);
    }

    public bool CanPreview(string fullPath)
    {
        if (string.IsNullOrWhiteSpace(fullPath) || !File.Exists(fullPath))
            return false;
        try
        {
            return _resolver.Resolve(fullPath) is not null;
        }
        catch (Exception ex)
        {
            TraceSafeFailure("probe", ex);
            return false;
        }
    }

    public void Show(string fullPath)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(fullPath);
        string normalizedPath = Path.GetFullPath(fullPath);
        if (!File.Exists(normalizedPath))
            throw new DocumentPreviewException("文件不存在，无法预览。", "DOCUMENT_MISSING");

        Guid? handlerClsid;
        try
        {
            handlerClsid = _resolver.Resolve(normalizedPath);
        }
        catch (Exception ex)
        {
            TraceSafeFailure("resolve", ex);
            throw new DocumentPreviewException(
                "无法读取系统预览器配置，请使用默认应用打开。",
                "PREVIEW_HANDLER_UNAVAILABLE");
        }
        if (handlerClsid is null)
            throw new DocumentPreviewException(
                "系统没有为此文件类型注册预览器，可使用默认应用打开。",
                "PREVIEW_HANDLER_UNAVAILABLE");

        var launchSpec = PreviewHostLaunchSpec.Create(
            _appBaseDirectory,
            normalizedPath,
            handlerClsid.Value);
        Process next = StartHelper(launchSpec);
        Process? previous;
        lock (_gate)
        {
            if (_disposed)
            {
                StopHelper(next);
                throw new DocumentPreviewException(
                    "预览服务已关闭。",
                    "PREVIEW_HOST_CREATE_FAILED");
            }
            previous = _process;
            _process = next;
        }
        StopHelper(previous);
    }

    public void Dispose()
    {
        Process? process;
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            process = _process;
            _process = null;
        }
        StopHelper(process);
    }

    private static Process StartHelper(PreviewHostLaunchSpec launchSpec)
    {
        if (!File.Exists(launchSpec.ExecutablePath))
            throw new DocumentPreviewException(
                "系统预览组件不可用，请重新安装或修复应用。",
                "PREVIEW_HOST_CREATE_FAILED");
        try
        {
            Process process = Process.Start(launchSpec.CreateStartInfo())
                ?? throw new InvalidOperationException("preview host returned no process");
            try
            {
                if (!process.WaitForInputIdle(PreviewStartupTimeoutMilliseconds))
                {
                    throw new TimeoutException("preview host did not become responsive");
                }
                if (process.HasExited && process.ExitCode != 0)
                {
                    int exitCode = process.ExitCode;
                    throw new InvalidOperationException(
                        $"preview host exited during startup ({exitCode})");
                }
                return process;
            }
            catch
            {
                StopHelper(process);
                throw;
            }
        }
        catch (Exception ex)
        {
            TraceSafeFailure("start", ex);
            throw new DocumentPreviewException(
                "无法启动系统预览进程，请稍后重试。",
                "PREVIEW_HOST_CREATE_FAILED");
        }
    }

    private static void StopHelper(Process? process)
    {
        if (process is null) return;
        try
        {
            if (!process.HasExited)
            {
                bool closeRequested = process.CloseMainWindow();
                if (!closeRequested || !process.WaitForExit(500))
                {
                    if (!process.HasExited) process.Kill(entireProcessTree: true);
                }
            }
        }
        catch (Exception ex)
        {
            TraceSafeFailure("stop", ex);
        }
        finally
        {
            process.Dispose();
        }
    }

    private static void TraceSafeFailure(string operation, Exception exception)
        => Trace.TraceError(
            $"Preview host {operation} failed ({exception.GetType().Name}, "
            + $"0x{exception.HResult:X8}).");
}

internal sealed record PreviewHostLaunchSpec(
    string ExecutablePath,
    string FullPath,
    Guid HandlerClsid)
{
    public const string ExecutableName = "VibeTable.PreviewHost.exe";

    public static PreviewHostLaunchSpec Create(
        string appBaseDirectory,
        string fullPath,
        Guid handlerClsid)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(appBaseDirectory);
        ArgumentException.ThrowIfNullOrWhiteSpace(fullPath);
        if (handlerClsid == Guid.Empty)
            throw new ArgumentException("Handler CLSID cannot be empty.", nameof(handlerClsid));

        string executablePath = Path.Combine(
            Path.GetFullPath(appBaseDirectory),
            "PreviewHost",
            ExecutableName);
        return new PreviewHostLaunchSpec(
            executablePath,
            Path.GetFullPath(fullPath),
            handlerClsid);
    }

    public ProcessStartInfo CreateStartInfo()
    {
        var startInfo = new ProcessStartInfo
        {
            FileName = ExecutablePath,
            WorkingDirectory = Path.GetDirectoryName(ExecutablePath)!,
            UseShellExecute = false,
            CreateNoWindow = false,
        };
        startInfo.ArgumentList.Add("--file");
        startInfo.ArgumentList.Add(FullPath);
        startInfo.ArgumentList.Add("--handler");
        startInfo.ArgumentList.Add(HandlerClsid.ToString("D"));
        return startInfo;
    }
}

public sealed class DocumentPreviewException : InvalidOperationException
{
    public DocumentPreviewException(string message, string code) : base(message)
    {
        Code = code;
    }

    public string Code { get; }
}
