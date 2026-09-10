using System.Diagnostics;
using System.IO;
using System.Runtime.ExceptionServices;

namespace VibeTable.Desktop.Services;

// Owns the journal's process-exclusive file lock and its bounded acquisition.
internal sealed class UpdateActivationJournalLock : IDisposable
{
    private readonly FileStream _claim;

    private UpdateActivationJournalLock(FileStream claim) => _claim = claim;

    private const int ErrorSharingViolation = 32;
    private const int ErrorLockViolation = 33;
    private static readonly TimeSpan LockAcquisitionBudget = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan LockRetryInterval = TimeSpan.FromMilliseconds(25);
    private static ReleaseUpdateException InvalidPointer(string message) =>
        new(message, "UPDATE_ACTIVATION_INVALID");

    // The optional checkpoint controls only a deterministic filesystem interleave in tests.
    internal static UpdateActivationJournalLock Acquire(
        string lockPath,
        Action? existingLockObserved = null)
    {
        Stopwatch wait = Stopwatch.StartNew();
        IOException? contention = null;
        while (true)
        {
            if (contention is not null && wait.Elapsed >= LockAcquisitionBudget)
            {
                ExceptionDispatchInfo.Capture(contention).Throw();
            }
            FileStream claim;
            try
            {
                UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
                    Path.GetDirectoryName(lockPath)
                        ?? throw InvalidPointer("无法确定更新锁目录。"));
                if (File.Exists(lockPath) || Directory.Exists(lockPath))
                {
                    existingLockObserved?.Invoke();
                    UpdateProcessCommand.RejectReparsePoint(lockPath);
                }
                claim = new FileStream(
                    lockPath,
                    FileMode.OpenOrCreate,
                    FileAccess.ReadWrite,
                    FileShare.None,
                    bufferSize: 4096,
                    FileOptions.WriteThrough);
            }
            catch (IOException exception) when (IsLockContention(exception))
            {
                contention = exception;
                TimeSpan remaining = LockAcquisitionBudget - wait.Elapsed;
                if (remaining <= TimeSpan.Zero)
                {
                    throw;
                }
                Thread.Sleep(remaining < LockRetryInterval ? remaining : LockRetryInterval);
                if (wait.Elapsed >= LockAcquisitionBudget)
                {
                    throw;
                }
                continue;
            }
            if (contention is not null && wait.Elapsed >= LockAcquisitionBudget)
            {
                new UpdateActivationJournalLock(claim).Dispose();
                ExceptionDispatchInfo.Capture(contention).Throw();
            }
            return new UpdateActivationJournalLock(claim);
        }
    }

    private static bool IsLockContention(IOException exception) =>
        OperatingSystem.IsWindows()
        && (exception.HResult & 0xffff) is ErrorSharingViolation or ErrorLockViolation;

    // The OS handle is the lock. Keep its empty pathname stable across owners:
    // unlinking it after close races another contender's path validation.
    public void Dispose() => _claim.Dispose();
}
