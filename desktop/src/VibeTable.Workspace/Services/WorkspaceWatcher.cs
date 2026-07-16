using System.Collections.Concurrent;

namespace VibeTable.Workspace.Services;

/// <summary>
/// File-system watcher that detects dirty (changed) working files.
///
/// The watcher only produces dirty state — it does NOT auto-commit. A short
/// snapshot is produced after the file is stable for a configurable debounce
/// period (default 10 seconds). Formal versions require explicit user action.
///
/// The watcher ignores <c>.backup</c>, <c>.staging</c>, temp files, Synology
/// conflict copies and Office lock files.
/// </summary>
public sealed class WorkspaceWatcher : IDisposable
{
    private readonly FileSystemWatcher _fsWatcher;
    private readonly ConcurrentDictionary<string, DateTime> _dirtyFiles = new();
    private readonly Timer _debounceTimer;
    private readonly TimeSpan _debounce;
    private bool _disposed;

    /// <summary>
    /// Fired when a file has been stable for the debounce period.
    /// The handler should create a snapshot (not a formal version).
    /// </summary>
    public event EventHandler<FileStableEventArgs>? FileStable;

    /// <summary>
    /// Fired when a file is first marked dirty (changed).
    /// </summary>
    public event EventHandler<FileDirtyEventArgs>? FileDirty;

    public WorkspaceWatcher(string workspaceRoot, TimeSpan? debounce = null)
    {
        _debounce = debounce ?? TimeSpan.FromSeconds(10);
        _fsWatcher = new FileSystemWatcher(workspaceRoot)
        {
            IncludeSubdirectories = true,
            NotifyFilter = NotifyFilters.FileName | NotifyFilters.LastWrite
                | NotifyFilters.Size | NotifyFilters.DirectoryName,
        };
        _fsWatcher.Changed += OnFileEvent;
        _fsWatcher.Created += OnFileEvent;
        _fsWatcher.Renamed += OnFileEvent;
        _fsWatcher.Deleted += OnFileEvent;
        _fsWatcher.Error += OnError;

        _debounceTimer = new Timer(CheckStableFiles, null, _debounce, _debounce);
    }

    /// <summary>
    /// Start watching.
    /// </summary>
    public void Start()
    {
        _fsWatcher.EnableRaisingEvents = true;
    }

    /// <summary>
    /// Stop watching.
    /// </summary>
    public void Stop()
    {
        _fsWatcher.EnableRaisingEvents = false;
    }

    /// <summary>
    /// Returns the set of currently-dirty file paths.
    /// </summary>
    public IReadOnlyCollection<string> GetDirtyFiles() => _dirtyFiles.Keys.ToList();

    /// <summary>
    /// Returns true if the given relative path should be ignored.
    /// </summary>
    public static bool ShouldIgnore(string fullPath, string rootPath)
    {
        var relative = Path.GetRelativePath(rootPath, fullPath);
        // Check each path segment.
        foreach (var segment in relative.Split(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar))
        {
            if (Storage.WorkspacePathGuard.ShouldIgnore(segment))
                return true;
        }
        return false;
    }

    private void OnFileEvent(object sender, FileSystemEventArgs e)
    {
        if (ShouldIgnore(e.FullPath, _fsWatcher.Path))
            return;

        var now = DateTime.UtcNow;
        _dirtyFiles[e.FullPath] = now;
        FileDirty?.Invoke(this, new FileDirtyEventArgs(e.FullPath, now));
    }

    private void OnError(object sender, ErrorEventArgs e)
    {
        // FileSystemWatcher errors are non-fatal; the scanner will catch
        // any missed changes on the next scan.
    }

    private void CheckStableFiles(object? state)
    {
        if (_disposed)
            return;

        var now = DateTime.UtcNow;
        var stable = _dirtyFiles
            .Where(kvp => now - kvp.Value >= _debounce)
            .Select(kvp => kvp.Key)
            .ToList();

        foreach (var path in stable)
        {
            _dirtyFiles.TryRemove(path, out _);
            if (File.Exists(path))
            {
                FileStable?.Invoke(this, new FileStableEventArgs(path, now));
            }
        }
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _fsWatcher.EnableRaisingEvents = false;
        _fsWatcher.Dispose();
        _debounceTimer.Dispose();
    }
}

public sealed record FileDirtyEventArgs(string FilePath, DateTime Timestamp);

public sealed record FileStableEventArgs(string FilePath, DateTime Timestamp);
