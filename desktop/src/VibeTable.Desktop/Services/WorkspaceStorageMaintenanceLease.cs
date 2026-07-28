using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Cross-process exclusion for storage mutations.
///
/// The intent file is created while the current writer is still alive. New
/// writer acquisitions check it both before and after taking the writer lock.
/// After the local session has stopped, the storage operation takes and keeps
/// the writer lock itself until the copy is verified and the registry update
/// is durably published. DeleteOnClose makes an abrupt process exit release
/// the intent without relying on startup cleanup.
/// </summary>
public sealed class WorkspaceStorageMaintenanceLease : IAsyncDisposable
{
    internal const string IntentFileName = "storage-maintenance.lock";
    internal const string WriterFileName = "desktop-writer.lock";

    private readonly FileStream _intent;
    private FileStream? _writerFence;
    private bool _disposed;

    private WorkspaceStorageMaintenanceLease(FileStream intent)
    {
        _intent = intent;
    }

    public static WorkspaceStorageMaintenanceLease Acquire(
        string runtimeRoot,
        Guid workspaceId)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(runtimeRoot);
        if (workspaceId == Guid.Empty)
            throw new ArgumentOutOfRangeException(nameof(workspaceId));
        string normalizedRoot = Path.GetFullPath(runtimeRoot);
        string parent = Path.GetDirectoryName(normalizedRoot)
            ?? throw new WorkspaceRegistryException(
                "workspace.storage_maintenance_unavailable",
                "Workspace storage-maintenance intent has no safe parent.");
        Directory.CreateDirectory(parent);
        string intentPath = IntentPath(normalizedRoot);
        try
        {
            var stream = new FileStream(
                intentPath,
                FileMode.CreateNew,
                FileAccess.ReadWrite,
                FileShare.Read,
                bufferSize: 4096,
                FileOptions.WriteThrough | FileOptions.DeleteOnClose);
            try
            {
                byte[] marker = JsonSerializer.SerializeToUtf8Bytes(new
                {
                    workspaceId = workspaceId.ToString("D"),
                    processId = Environment.ProcessId,
                    acquiredAt = DateTimeOffset.UtcNow,
                });
                stream.Write(marker);
                stream.Flush(flushToDisk: true);
                return new WorkspaceStorageMaintenanceLease(stream);
            }
            catch
            {
                stream.Dispose();
                throw;
            }
        }
        catch (IOException exception)
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_maintenance_conflict",
                "Another process is already changing this workspace's storage.",
                exception);
        }
        catch (UnauthorizedAccessException exception)
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_maintenance_unavailable",
                "The workspace storage-maintenance lease cannot be acquired.",
                exception);
        }
    }

    public async Task AcquireWriterFenceAsync(
        string runtimeRoot,
        CancellationToken cancellationToken)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_writerFence is not null)
            return;
        string writerPath = Path.Combine(
            WorkspaceLayout.Paths(runtimeRoot).Coordination,
            WriterFileName);
        IOException? lastError = null;
        for (int attempt = 0; attempt < 40; attempt++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            try
            {
                _writerFence = new FileStream(
                    writerPath,
                    FileMode.OpenOrCreate,
                    FileAccess.ReadWrite,
                    FileShare.None,
                    bufferSize: 1,
                    FileOptions.WriteThrough);
                return;
            }
            catch (IOException exception)
            {
                lastError = exception;
                await Task.Delay(25, cancellationToken).ConfigureAwait(false);
            }
            catch (UnauthorizedAccessException exception)
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_writer_fence_unavailable",
                    "The workspace writer fence cannot be acquired.",
                    exception);
            }
        }
        throw new WorkspaceRegistryException(
            "workspace.storage_writer_fence_conflict",
            "Another process still holds the workspace writer lease.",
            lastError);
    }

    internal static void EnsureNoIntent(string runtimeRoot)
    {
        string intentPath = IntentPath(runtimeRoot);
        if (File.Exists(intentPath))
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_maintenance_conflict",
                "Workspace storage maintenance is in progress.");
        }
    }

    /// <summary>
    /// Releases the in-root writer handle while retaining the external
    /// maintenance intent. New writers still fail both intent checks, allowing
    /// the activity directory itself to be removed safely.
    /// </summary>
    public void ReleaseWriterFenceForDeletion()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        _writerFence?.Dispose();
        _writerFence = null;
    }

    internal static string IntentPath(string runtimeRoot)
    {
        string normalized = Path.TrimEndingDirectorySeparator(
            Path.GetFullPath(runtimeRoot));
        string parent = Path.GetDirectoryName(normalized)
            ?? throw new WorkspaceRegistryException(
                "workspace.storage_maintenance_unavailable",
                "Workspace storage-maintenance intent has no safe parent.");
        return Path.Combine(
            parent,
            $".{Path.GetFileName(normalized)}.{IntentFileName}");
    }

    public ValueTask DisposeAsync()
    {
        if (_disposed)
            return ValueTask.CompletedTask;
        _disposed = true;
        _writerFence?.Dispose();
        _intent.Dispose();
        return ValueTask.CompletedTask;
    }
}
