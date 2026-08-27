using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

namespace VibeTable.Desktop.Services;

internal sealed record UpdateUpdatedPackageLaunch(
    string ExecutablePath,
    string WorkingDirectory,
    IReadOnlyList<string> Arguments,
    string? OwnedGroupId = null);

internal sealed record UpdateRollbackLaunch(
    string ExecutablePath,
    string WorkingDirectory,
    IReadOnlyList<string> Arguments,
    string? OwnedGroupId = null);

internal sealed record UpdateRestoredPackageLaunch(
    string ExecutablePath,
    string WorkingDirectory,
    IReadOnlyList<string> Arguments);

internal sealed record ExactProcessExit(
    bool Exited,
    bool IdentityMatched,
    int? ExitCode = null);

internal sealed record OwnedProcessGroupExit(bool GroupEmpty);

internal sealed record ExactProcessTermination(bool Terminated, bool GroupEmpty);

internal sealed class UpdateOwnedProcessStartException(
    string message,
    bool groupEmpty,
    Exception innerException,
    UpdateOwnedProcessGroup? retainedGroup = null) : Exception(message, innerException)
{
    internal bool GroupEmpty { get; } = groupEmpty;

    internal UpdateOwnedProcessGroup? RetainedGroup { get; } = retainedGroup;
}

internal sealed class UpdateOwnedProcessGroup(
    string groupId,
    UpdateProcessIdentity root,
    SafeHandle ownership,
    SafeHandle? exactProcessOwnership = null) : IDisposable
{
    private readonly SafeHandle _ownership = ownership
        ?? throw new ArgumentNullException(nameof(ownership));

    public string GroupId { get; } = string.IsNullOrWhiteSpace(groupId)
        ? throw new ArgumentException("Owned group identity is required.", nameof(groupId))
        : groupId;

    public UpdateProcessIdentity Root { get; } = root;

    internal SafeHandle Ownership => _ownership;

    internal SafeHandle? ExactProcessOwnership { get; } = exactProcessOwnership;

    public void Dispose()
    {
        ExactProcessOwnership?.Dispose();
        _ownership.Dispose();
    }
}

internal interface IUpdateRecoveryProcessPort
{
    UpdateProcessIdentity Current();

    UpdateOwnedProcessGroup StartUpdatedPackage(UpdateUpdatedPackageLaunch launch);

    UpdateOwnedProcessGroup StartRollbackWorker(UpdateRollbackLaunch launch);

    Task<ExactProcessExit> WaitForExactExitAsync(
        UpdateProcessIdentity process,
        TimeSpan timeout,
        CancellationToken cancellationToken);

    Task<OwnedProcessGroupExit> WaitForOwnedProcessGroupExitAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken);

    Task<ExactProcessTermination> TerminateOwnedProcessGroupAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken);

    void StartRestoredPackage(UpdateRestoredPackageLaunch launch);
}

internal interface IUpdateExactProcessProbe
{
    IUpdateExactProcess Open(int processId);
}

internal interface IUpdateExactProcess : IDisposable
{
    DateTimeOffset StartedAtUtc { get; }

    int ExitCode { get; }

    Task WaitForExitAsync(CancellationToken cancellationToken);
}

internal interface IUpdateOwnedProcessQuarantine
{
    Task RetainUntilEmptyAsync(
        UpdateOwnedProcessGroup group,
        IUpdateRecoveryProcessPort processes,
        TimeSpan attemptBudget);
}

internal sealed class UpdateOwnedProcessQuarantine : IUpdateOwnedProcessQuarantine
{
    private static readonly TimeSpan RetryDelay = TimeSpan.FromMilliseconds(250);

    public async Task RetainUntilEmptyAsync(
        UpdateOwnedProcessGroup group,
        IUpdateRecoveryProcessPort processes,
        TimeSpan attemptBudget)
    {
        ArgumentNullException.ThrowIfNull(group);
        ArgumentNullException.ThrowIfNull(processes);
        while (true)
        {
            try
            {
                ExactProcessTermination termination =
                    await processes.TerminateOwnedProcessGroupAsync(
                        group,
                        attemptBudget,
                        CancellationToken.None).ConfigureAwait(false);
                if (termination.Terminated && termination.GroupEmpty)
                {
                    group.Dispose();
                    return;
                }
            }
            catch (Exception)
            {
                // Retaining the non-kill Job handle is the fail-closed boundary.
            }
            try
            {
                OwnedProcessGroupExit exit = await processes.WaitForOwnedProcessGroupExitAsync(
                    group,
                    attemptBudget,
                    CancellationToken.None).ConfigureAwait(false);
                if (exit.GroupEmpty)
                {
                    group.Dispose();
                    return;
                }
            }
            catch (Exception)
            {
                // Keep ownership and retry after a bounded delay.
            }
            await Task.Delay(RetryDelay).ConfigureAwait(false);
        }
    }
}

internal sealed class WindowsUpdateRecoveryProcessAdapter(
    TimeProvider? timeProvider = null,
    IUpdateExactProcessProbe? processProbe = null) : IUpdateRecoveryProcessPort
{
    private const uint CreateSuspended = 0x00000004;
    private const uint Infinite = 0xffffffff;
    private const uint WaitObject0 = 0x00000000;
    private const uint NativeCleanupTimeoutMilliseconds = 5000;
    private readonly TimeProvider _timeProvider = timeProvider ?? TimeProvider.System;
    private readonly IUpdateExactProcessProbe _processProbe =
        processProbe ?? new WindowsExactProcessProbe();

    public UpdateProcessIdentity Current() =>
        PendingUpdateActivationJournal.CurrentProcessIdentity();

    public UpdateOwnedProcessGroup StartUpdatedPackage(UpdateUpdatedPackageLaunch launch) =>
        StartOwned(
            launch.ExecutablePath,
            launch.WorkingDirectory,
            launch.Arguments,
            launch.OwnedGroupId);

    public UpdateOwnedProcessGroup StartRollbackWorker(UpdateRollbackLaunch launch) =>
        StartOwned(
            launch.ExecutablePath,
            launch.WorkingDirectory,
            launch.Arguments,
            launch.OwnedGroupId);

    public async Task<ExactProcessExit> WaitForExactExitAsync(
        UpdateProcessIdentity expected,
        TimeSpan timeout,
        CancellationToken cancellationToken)
    {
        ValidateTimeout(timeout);
        if (expected.ProcessId == Environment.ProcessId)
        {
            return new ExactProcessExit(false, true);
        }
        try
        {
            using IUpdateExactProcess process = _processProbe.Open(expected.ProcessId);
            DateTimeOffset actualStartedAt;
            try
            {
                actualStartedAt = process.StartedAtUtc;
            }
            catch (Exception exception) when (exception is
                InvalidOperationException or Win32Exception)
            {
                return new ExactProcessExit(false, false);
            }
            if (actualStartedAt != expected.StartedAtUtc)
            {
                return new ExactProcessExit(true, false);
            }
            using var timeoutSource = CancellationTokenSource.CreateLinkedTokenSource(
                cancellationToken);
            timeoutSource.CancelAfter(timeout);
            try
            {
                await process.WaitForExitAsync(timeoutSource.Token).ConfigureAwait(false);
                return new ExactProcessExit(true, true, process.ExitCode);
            }
            catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
            {
                return new ExactProcessExit(false, true);
            }
        }
        catch (ArgumentException)
        {
            return new ExactProcessExit(true, true);
        }
    }

    public async Task<OwnedProcessGroupExit> WaitForOwnedProcessGroupExitAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(processGroup);
        ValidateTimeout(timeout);
        DateTimeOffset deadline = _timeProvider.GetUtcNow() + timeout;
        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (processGroup.ExactProcessOwnership is not null)
            {
                uint exactWait = WaitForSingleObject(
                    processGroup.ExactProcessOwnership,
                    0);
                if (exactWait == WaitObject0)
                {
                    return new OwnedProcessGroupExit(true);
                }
                if (exactWait == Infinite)
                {
                    throw NativeFailure(
                        "无法检查未认领的 suspended 更新进程状态。",
                        "UPDATE_PROCESS_CLEANUP_WAIT_FAILED");
                }
            }
            else if (ActiveProcessCount(processGroup) == 0)
            {
                return new OwnedProcessGroupExit(true);
            }
            TimeSpan remaining = deadline - _timeProvider.GetUtcNow();
            if (remaining <= TimeSpan.Zero)
            {
                return new OwnedProcessGroupExit(false);
            }
            await Task.Delay(
                remaining < TimeSpan.FromMilliseconds(25)
                    ? remaining
                    : TimeSpan.FromMilliseconds(25),
                _timeProvider,
                cancellationToken).ConfigureAwait(false);
        }
    }

    public async Task<ExactProcessTermination> TerminateOwnedProcessGroupAsync(
        UpdateOwnedProcessGroup processGroup,
        TimeSpan timeout,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(processGroup);
        ValidateTimeout(timeout);
        if (processGroup.ExactProcessOwnership is not null)
        {
            if (!TerminateProcess(processGroup.ExactProcessOwnership, 1))
            {
                throw NativeFailure(
                    "无法终止未认领的 suspended 更新进程。",
                    "UPDATE_PROCESS_CLEANUP_TERMINATE_FAILED");
            }
            OwnedProcessGroupExit exactExit = await WaitForOwnedProcessGroupExitAsync(
                processGroup,
                timeout,
                cancellationToken).ConfigureAwait(false);
            return new ExactProcessTermination(true, exactExit.GroupEmpty);
        }
        if (!TerminateJobObject(processGroup.Ownership, 1))
        {
            throw NativeFailure("无法终止更新 owned process group。", "UPDATE_GROUP_TERMINATE_FAILED");
        }
        OwnedProcessGroupExit exit = await WaitForOwnedProcessGroupExitAsync(
            processGroup,
            timeout,
            cancellationToken).ConfigureAwait(false);
        return new ExactProcessTermination(true, exit.GroupEmpty);
    }

    public void StartRestoredPackage(UpdateRestoredPackageLaunch launch)
    {
        ArgumentNullException.ThrowIfNull(launch);
        var start = new ProcessStartInfo
        {
            FileName = launch.ExecutablePath,
            WorkingDirectory = launch.WorkingDirectory,
            UseShellExecute = false,
        };
        foreach (string argument in launch.Arguments)
        {
            start.ArgumentList.Add(argument);
        }
        using Process? process = Process.Start(start);
        if (process is null)
        {
            throw new ReleaseUpdateException(
                "无法启动已恢复的旧版 VibeTable。",
                "UPDATE_RESTORED_LAUNCH_FAILED");
        }
    }

    private static UpdateOwnedProcessGroup StartOwned(
        string executablePath,
        string workingDirectory,
        IReadOnlyList<string> arguments,
        string? ownedGroupId)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(executablePath);
        ArgumentException.ThrowIfNullOrWhiteSpace(workingDirectory);
        ArgumentNullException.ThrowIfNull(arguments);
        string resolvedGroupId = ownedGroupId ?? Guid.NewGuid().ToString("N");
        if (string.IsNullOrWhiteSpace(resolvedGroupId))
        {
            throw new ArgumentException(
                "Owned group identity is required.",
                nameof(ownedGroupId));
        }
        var job = new SafeNativeHandle(CreateJobObject(IntPtr.Zero, null), ownsHandle: true);
        if (job.IsInvalid)
        {
            ReleaseUpdateException failure = NativeFailure(
                "无法创建更新 owned process group。",
                "UPDATE_GROUP_CREATE_FAILED");
            throw new UpdateOwnedProcessStartException(
                failure.Message,
                groupEmpty: true,
                failure);
        }

        string commandLine = BuildCommandLine(executablePath, arguments);
        var startup = new StartupInfo { Size = Marshal.SizeOf<StartupInfo>() };
        if (!CreateProcess(
                executablePath,
                new StringBuilder(commandLine),
                IntPtr.Zero,
                IntPtr.Zero,
                false,
                CreateSuspended,
                IntPtr.Zero,
                workingDirectory,
                ref startup,
                out ProcessInformation processInformation))
        {
            job.Dispose();
            ReleaseUpdateException failure = NativeFailure(
                "无法以 suspended 状态启动更新进程。",
                "UPDATE_PROCESS_START_FAILED");
            throw new UpdateOwnedProcessStartException(
                failure.Message,
                groupEmpty: true,
                failure);
        }

        using var processHandle = new SafeNativeHandle(
            processInformation.ProcessHandle,
            ownsHandle: true);
        using var threadHandle = new SafeNativeHandle(
            processInformation.ThreadHandle,
            ownsHandle: true);
        bool assignedToJob = false;
        UpdateProcessIdentity? identity = null;
        try
        {
            if (!AssignProcessToJobObject(job, processHandle))
            {
                throw NativeFailure(
                    "无法在 resume 前认领更新进程。",
                    "UPDATE_GROUP_ASSIGN_FAILED");
            }
            assignedToJob = true;
            identity = ReadIdentity(
                processHandle,
                checked((int)processInformation.ProcessId));
            if (ResumeThread(threadHandle) == Infinite)
            {
                throw NativeFailure("无法 resume 更新进程。", "UPDATE_PROCESS_RESUME_FAILED");
            }
            return new UpdateOwnedProcessGroup(
                resolvedGroupId,
                identity,
                job);
        }
        catch (Exception startFailure)
        {
            Exception? cleanupFailure = null;
            try
            {
                TerminateCreatedProcessAndWait(processHandle, job, assignedToJob);
            }
            catch (Exception exception)
            {
                cleanupFailure = exception;
            }
            if (cleanupFailure is not null)
            {
                SafeHandle? retainedExactProcess = null;
                if (!assignedToJob)
                {
                    retainedExactProcess = new SafeNativeHandle(
                        processHandle.DangerousGetHandle(),
                        ownsHandle: true);
                    processHandle.SetHandleAsInvalid();
                }
                var aggregate = new AggregateException(
                    "更新进程启动失败，且无法证明 suspended 进程已退出。",
                    startFailure,
                    cleanupFailure);
                throw new UpdateOwnedProcessStartException(
                    aggregate.Message,
                    groupEmpty: false,
                    aggregate,
                    new UpdateOwnedProcessGroup(
                        resolvedGroupId,
                        identity ?? new UpdateProcessIdentity(
                            checked((int)processInformation.ProcessId),
                            DateTimeOffset.MinValue),
                        job,
                        retainedExactProcess));
            }
            job.Dispose();
            throw new UpdateOwnedProcessStartException(
                startFailure.Message,
                groupEmpty: true,
                startFailure);
        }
    }

    private static void TerminateCreatedProcessAndWait(
        SafeHandle process,
        SafeHandle job,
        bool assignedToJob)
    {
        bool terminationRequested = assignedToJob
            ? TerminateJobObject(job, 1)
            : TerminateProcess(process, 1);
        if (!terminationRequested)
        {
            throw NativeFailure(
                "无法终止启动失败的 suspended 更新进程。",
                "UPDATE_PROCESS_CLEANUP_TERMINATE_FAILED");
        }
        uint wait = WaitForSingleObject(process, NativeCleanupTimeoutMilliseconds);
        if (wait != WaitObject0)
        {
            throw new ReleaseUpdateException(
                $"无法证明启动失败的 suspended 更新进程已退出。 ({wait})",
                "UPDATE_PROCESS_CLEANUP_WAIT_FAILED");
        }
    }

    private static UpdateProcessIdentity ReadIdentity(SafeHandle process, int processId)
    {
        if (!GetProcessTimes(
                process,
                out FileTime created,
                out _,
                out _,
                out _))
        {
            throw NativeFailure("无法读取更新进程启动身份。", "UPDATE_PROCESS_IDENTITY_FAILED");
        }
        long fileTime = ((long)created.High << 32) | created.Low;
        return new UpdateProcessIdentity(
            processId,
            new DateTimeOffset(DateTime.FromFileTimeUtc(fileTime), TimeSpan.Zero));
    }

    private static uint ActiveProcessCount(UpdateOwnedProcessGroup group)
    {
        int length = Marshal.SizeOf<JobBasicAccountingInformation>();
        IntPtr buffer = Marshal.AllocHGlobal(length);
        try
        {
            if (!QueryInformationJobObject(
                    group.Ownership,
                    1,
                    buffer,
                    (uint)length,
                    out _))
            {
                throw NativeFailure(
                    "无法读取更新 owned process group 状态。",
                    "UPDATE_GROUP_QUERY_FAILED");
            }
            return Marshal.PtrToStructure<JobBasicAccountingInformation>(buffer)
                .ActiveProcesses;
        }
        finally
        {
            Marshal.FreeHGlobal(buffer);
        }
    }

    private static string BuildCommandLine(
        string executablePath,
        IReadOnlyList<string> arguments)
    {
        var command = new StringBuilder(QuoteArgument(executablePath));
        foreach (string argument in arguments)
        {
            command.Append(' ').Append(QuoteArgument(argument));
        }
        return command.ToString();
    }

    private static string QuoteArgument(string argument)
    {
        if (argument.Length != 0
            && !argument.Any(character => char.IsWhiteSpace(character) || character == '"'))
        {
            return argument;
        }
        var quoted = new StringBuilder("\"");
        int slashes = 0;
        foreach (char character in argument)
        {
            if (character == '\\')
            {
                slashes++;
                continue;
            }
            if (character == '"')
            {
                quoted.Append('\\', slashes * 2 + 1).Append('"');
                slashes = 0;
                continue;
            }
            quoted.Append('\\', slashes).Append(character);
            slashes = 0;
        }
        quoted.Append('\\', slashes * 2).Append('"');
        return quoted.ToString();
    }

    private static void ValidateTimeout(TimeSpan timeout)
    {
        if (timeout <= TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(nameof(timeout));
        }
    }

    private static ReleaseUpdateException NativeFailure(string message, string code) =>
        new($"{message} ({new Win32Exception(Marshal.GetLastWin32Error()).NativeErrorCode})", code);

    private sealed class SafeNativeHandle : SafeHandleZeroOrMinusOneIsInvalid
    {
        public SafeNativeHandle() : base(true)
        {
        }

        public SafeNativeHandle(IntPtr nativeHandle, bool ownsHandle) : base(ownsHandle) =>
            SetHandle(nativeHandle);

        protected override bool ReleaseHandle() => CloseHandle(handle);
    }

    private sealed class WindowsExactProcessProbe : IUpdateExactProcessProbe
    {
        public IUpdateExactProcess Open(int processId) =>
            new WindowsExactProcess(Process.GetProcessById(processId));
    }

    private sealed class WindowsExactProcess(Process process) : IUpdateExactProcess
    {
        public DateTimeOffset StartedAtUtc => new(
            process.StartTime.ToUniversalTime(),
            TimeSpan.Zero);

        public int ExitCode => process.ExitCode;

        public Task WaitForExitAsync(CancellationToken cancellationToken) =>
            process.WaitForExitAsync(cancellationToken);

        public void Dispose() => process.Dispose();
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct StartupInfo
    {
        public int Size;
        public string? Reserved;
        public string? Desktop;
        public string? Title;
        public uint X;
        public uint Y;
        public uint XSize;
        public uint YSize;
        public uint XCountChars;
        public uint YCountChars;
        public uint FillAttribute;
        public uint Flags;
        public ushort ShowWindow;
        public ushort Reserved2;
        public IntPtr Reserved2Pointer;
        public IntPtr StandardInput;
        public IntPtr StandardOutput;
        public IntPtr StandardError;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ProcessInformation
    {
        public IntPtr ProcessHandle;
        public IntPtr ThreadHandle;
        public uint ProcessId;
        public uint ThreadId;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct FileTime
    {
        public uint Low;
        public uint High;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct JobBasicAccountingInformation
    {
        public long TotalUserTime;
        public long TotalKernelTime;
        public long ThisPeriodTotalUserTime;
        public long ThisPeriodTotalKernelTime;
        public uint TotalPageFaultCount;
        public uint TotalProcesses;
        public uint ActiveProcesses;
        public uint TotalTerminatedProcesses;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateJobObject(IntPtr jobAttributes, string? name);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool AssignProcessToJobObject(
        SafeHandle job,
        SafeHandle process);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool TerminateJobObject(SafeHandle job, uint exitCode);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool QueryInformationJobObject(
        SafeHandle job,
        int informationClass,
        IntPtr information,
        uint informationLength,
        out uint returnLength);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CreateProcess(
        string applicationName,
        StringBuilder commandLine,
        IntPtr processAttributes,
        IntPtr threadAttributes,
        [MarshalAs(UnmanagedType.Bool)] bool inheritHandles,
        uint creationFlags,
        IntPtr environment,
        string currentDirectory,
        ref StartupInfo startupInfo,
        out ProcessInformation processInformation);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern uint ResumeThread(SafeHandle thread);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool TerminateProcess(SafeHandle process, uint exitCode);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetProcessTimes(
        SafeHandle process,
        out FileTime creationTime,
        out FileTime exitTime,
        out FileTime kernelTime,
        out FileTime userTime);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern uint WaitForSingleObject(SafeHandle handle, uint milliseconds);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CloseHandle(IntPtr handle);
}
