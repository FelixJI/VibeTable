using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using static PdfAdapterQualification.NativeMethods;

namespace PdfAdapterQualification;

internal sealed record WorkerLimits(long MemoryBytes, TimeSpan CpuTime, TimeSpan Deadline,
    int MaxStdoutBytes, int MaxStderrBytes);

internal enum WorkerRunReason
{
    Succeeded,
    Cancelled,
    DeadlineExceeded,
    CpuLimitExceeded,
    MemoryLimitExceeded,
    OutputLimitExceeded,
    WorkerFailed,
    CleanupFailed,
}

internal sealed record WorkerRunResult(WorkerRunReason Reason, int? ExitCode, string Stdout,
    string Stderr, TimeSpan WallTime, TimeSpan CpuTime, long PeakJobMemoryBytes,
    bool AllProcessesExited, string? Error)
{
    // Root worker's peak working set; separate from the job-wide committed-memory limit.
    // Zero is expected only when no process was started; Error covers failed metric reads.
    internal long PeakWorkerWorkingSetBytes { get; init; }
}

// Qualification only. A Job constrains ordinary descendants, not broker-created processes;
// this is a resource/lifetime seam for our own worker, not a hostile-code sandbox.
internal static class BoundedProcessRunner
{
    private static readonly TimeSpan CleanupDeadline = TimeSpan.FromSeconds(5);

    internal static async Task<WorkerRunResult> RunAsync(string executable,
        IReadOnlyList<string> arguments, WorkerLimits limits, CancellationToken cancellationToken)
    {
        if (!OperatingSystem.IsWindowsVersionAtLeast(10) ||
            RuntimeInformation.OSArchitecture != Architecture.X64 ||
            RuntimeInformation.ProcessArchitecture != Architecture.X64)
            throw new PlatformNotSupportedException("Qualification requires Windows 10/11 x64.");
        ArgumentException.ThrowIfNullOrWhiteSpace(executable);
        ArgumentNullException.ThrowIfNull(arguments);
        ArgumentNullException.ThrowIfNull(limits);
        if (!Path.IsPathFullyQualified(executable))
            throw new ArgumentException("Worker executable must be an absolute path.", nameof(executable));
        if (limits.MemoryBytes <= 0 || limits.CpuTime <= TimeSpan.Zero ||
            limits.Deadline <= TimeSpan.Zero || limits.MaxStdoutBytes <= 0 || limits.MaxStderrBytes <= 0)
            throw new ArgumentOutOfRangeException(nameof(limits));

        var clock = Stopwatch.StartNew();
        if (cancellationToken.IsCancellationRequested)
            return new(WorkerRunReason.Cancelled, null, "", "", clock.Elapsed, TimeSpan.Zero,
                0, true, null);

        using var job = Checked(CreateJobObjectW(0, null));
        using var port = Checked(CreateIoCompletionPort(-1, 0, 0, 1));
        Set(job, AssociateCompletionPortInformation,
            new CompletionPort { CompletionKey = 1, Port = port.DangerousGetHandle() });
        Set(job, ExtendedLimitInformation, new ExtendedLimit
        {
            BasicLimitInformation = new BasicLimit
            {
                LimitFlags = JobMemory | JobTime | KillOnJobClose | DieOnUnhandledException,
                // Windows enforces job user-mode time in kernel; total user+kernel CPU is
                // additionally sampled below. Both Windows enforcement and sampling have lag.
                PerJobUserTimeLimit = limits.CpuTime.Ticks,
            },
            // Commit bytes, not working set/RSS. Failed commit does not imply process exit.
            JobMemoryLimit = checked((nuint)limits.MemoryBytes),
        });

        using var stdout = new OutputPipe(limits.MaxStdoutBytes);
        using var stderr = new OutputPipe(limits.MaxStderrBytes);
        var security = new SecurityAttributes
        {
            Length = Marshal.SizeOf<SecurityAttributes>(), InheritHandle = 1,
        };
        using var stdin = Checked(CreateFileW("NUL", 0x80000000, 3, ref security, 3, 0, 0));
        using var attributes = new ProcessAttributes(job, stdin, stdout.Write, stderr.Write);
        var startup = new StartupInfoEx
        {
            StartupInfo = new StartupInfo
            {
                Size = Marshal.SizeOf<StartupInfoEx>(),
                Flags = 0x100 | 0x1, // STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW
                ShowWindow = 0,
                StandardInput = stdin.DangerousGetHandle(),
                StandardOutput = stdout.Write.DangerousGetHandle(),
                StandardError = stderr.Write.DangerousGetHandle(),
            },
            AttributeList = attributes.List,
        };
        KernelHandle? process = null;
        WorkerRunReason reason = WorkerRunReason.WorkerFailed;
        string? error = null;
        int? exitCode = null;
        bool allExited = false;
        TimeSpan cpu = TimeSpan.Zero;
        long peakMemory = 0;
        long peakWorkingSet = 0;
        try
        {
            string command = string.Join(" ", new[] { executable }.Concat(arguments).Select(Quote));
            if (command.Length >= 32767)
                throw new ArgumentException("Worker command line exceeds the Windows limit.", nameof(arguments));
            Check(CreateProcessW(executable, new StringBuilder(command), 0, 0, true,
                0x00080000 | 0x08000000, // EXTENDED_STARTUPINFO_PRESENT | CREATE_NO_WINDOW
                0, null, ref startup, out var information));
            process = new KernelHandle(information.Process);
            using (var thread = new KernelHandle(information.Thread)) { }
            stdout.Write.Dispose();
            stderr.Write.Dispose();
            stdin.Dispose();

            while (true)
            {
                stdout.Drain();
                stderr.Drain();
                var accounting = Query<Accounting>(job, BasicAccountingInformation);
                cpu = TimeSpan.FromTicks(checked(accounting.TotalUserTime + accounting.TotalKernelTime));
                WorkerRunReason? limitEvent = ReadLimitEvent(port);
                uint jobWait = WaitForSingleObject(job, 0);
                if (jobWait == uint.MaxValue) throw new Win32Exception(Marshal.GetLastWin32Error());
                if (stdout.Exceeded || stderr.Exceeded) reason = WorkerRunReason.OutputLimitExceeded;
                else if (cancellationToken.IsCancellationRequested) reason = WorkerRunReason.Cancelled;
                else if (limitEvent is { } observed) reason = observed;
                // A Job is signaled when its end-of-job time limit terminates the processes.
                // This is authoritative even if the optional completion message is lost.
                else if (jobWait == 0 || cpu >= limits.CpuTime) reason = WorkerRunReason.CpuLimitExceeded;
                else if (clock.Elapsed >= limits.Deadline) reason = WorkerRunReason.DeadlineExceeded;
                else if (accounting.ActiveProcesses == 0)
                {
                    uint processWait = WaitForSingleObject(process, 0);
                    if (processWait == uint.MaxValue) throw new Win32Exception(Marshal.GetLastWin32Error());
                    if (processWait != 0)
                    {
                        await Task.Delay(10, CancellationToken.None).ConfigureAwait(false);
                        continue;
                    }
                    Check(GetExitCodeProcess(process, out uint code));
                    exitCode = unchecked((int)code);
                    if (!stdout.Ended || !stderr.Ended)
                    {
                        await Task.Delay(10, CancellationToken.None).ConfigureAwait(false);
                        continue;
                    }
                    reason = code == 0 ? WorkerRunReason.Succeeded : WorkerRunReason.WorkerFailed;
                }
                else
                {
                    await Task.Delay(10, CancellationToken.None).ConfigureAwait(false);
                    continue;
                }
                break;
            }
        }
        catch (Exception exception) when (exception is Win32Exception or IOException or ArgumentException)
        {
            error = exception.Message;
            reason = WorkerRunReason.WorkerFailed;
        }
        finally
        {
            // No reader tasks exist. Always bound settlement independently from the worker's
            // deadline/cancellation, and verify descendants through Job accounting, not PID lists.
            var cleanupClock = Stopwatch.StartNew();
            try
            {
                if (Query<Accounting>(job, BasicAccountingInformation).ActiveProcesses != 0)
                    Check(TerminateJobObject(job, 1));
                while (true)
                {
                    var accounting = Query<Accounting>(job, BasicAccountingInformation);
                    cpu = TimeSpan.FromTicks(checked(accounting.TotalUserTime + accounting.TotalKernelTime));
                    uint processWait = process is null ? 0 : WaitForSingleObject(process, 0);
                    if (processWait == uint.MaxValue) throw new Win32Exception(Marshal.GetLastWin32Error());
                    allExited = accounting.ActiveProcesses == 0 && processWait == 0;
                    if (allExited || cleanupClock.Elapsed >= CleanupDeadline) break;
                    await Task.Delay(10, CancellationToken.None).ConfigureAwait(false);
                }
                peakMemory = checked((long)Query<ExtendedLimit>(job, ExtendedLimitInformation).PeakJobMemoryUsed);
                if (process is not null && WaitForSingleObject(process, 0) == 0)
                {
                    Check(GetExitCodeProcess(process, out uint code));
                    exitCode = unchecked((int)code);
                    var memory = new ProcessMemoryCounters
                    {
                        Size = (uint)Marshal.SizeOf<ProcessMemoryCounters>(),
                    };
                    Check(K32GetProcessMemoryInfo(process, ref memory, memory.Size));
                    peakWorkingSet = checked((long)memory.PeakWorkingSetSize);
                }
            }
            catch (Exception exception) when (exception is Win32Exception or IOException)
            {
                allExited = false;
                error = exception.Message;
            }
            process?.Dispose();
            // Last Job handle is never inherited, so disposal is also a kernel kill fallback.
            if (!allExited)
            {
                reason = WorkerRunReason.CleanupFailed;
                error ??= "Job did not reach zero active processes within the cleanup deadline.";
            }
        }
        return new(reason, exitCode, reason == WorkerRunReason.Succeeded ? stdout.Text : "",
            stderr.Text, clock.Elapsed, cpu, peakMemory, allExited, error)
        {
            PeakWorkerWorkingSetBytes = peakWorkingSet,
        };
    }

    private static WorkerRunReason? ReadLimitEvent(KernelHandle port)
    {
        WorkerRunReason? reason = null;
        // A process storm cannot monopolize monitoring. Ordinary completion notifications
        // are best effort: missing memory messages leave unknown failures as WorkerFailed.
        for (int count = 0; count < 64; count++)
        {
            if (!GetQueuedCompletionStatus(port, out uint message, out nuint key, out _, 0))
            {
                int error = Marshal.GetLastWin32Error();
                if (error == WaitTimeout) break;
                throw new Win32Exception(error);
            }
            if (key != 1) throw new IOException("Unexpected Job completion key.");
            if (message == 1) reason ??= WorkerRunReason.CpuLimitExceeded;
            if (message == 10) reason = WorkerRunReason.MemoryLimitExceeded;
        }
        return reason;
    }

    private static string Quote(string argument)
    {
        ArgumentNullException.ThrowIfNull(argument);
        if (argument.Contains('\0')) throw new ArgumentException("NUL in worker argument.");
        var value = new StringBuilder("\"");
        int slashes = 0;
        foreach (char character in argument)
        {
            if (character == '\\') { slashes++; continue; }
            value.Append('\\', character == '"' ? slashes * 2 + 1 : slashes);
            value.Append(character);
            slashes = 0;
        }
        return value.Append('\\', slashes * 2).Append('"').ToString();
    }

    private sealed class OutputPipe : IDisposable
    {
        private readonly KernelHandle read;
        private readonly MemoryStream content = new();
        private readonly byte[] buffer = new byte[8192];
        private readonly int limit;
        internal KernelHandle Write { get; }
        internal bool Ended { get; private set; }
        internal bool Exceeded { get; private set; }
        internal string Text => Encoding.UTF8.GetString(content.GetBuffer(), 0, checked((int)content.Length));

        internal OutputPipe(int maximum)
        {
            limit = maximum;
            var security = new SecurityAttributes
            {
                Length = Marshal.SizeOf<SecurityAttributes>(), InheritHandle = 1,
            };
            Check(CreatePipe(out read, out var write, ref security, 0));
            Write = write;
            try { Check(SetHandleInformation(read, 1, 0)); }
            catch { Dispose(); throw; }
        }

        internal void Drain()
        {
            if (Ended || Exceeded) return;
            for (int count = 0; count < 8; count++)
            {
                if (!PeekNamedPipe(read, 0, 0, 0, out uint available, 0))
                {
                    int error = Marshal.GetLastWin32Error();
                    if (error == 109) { Ended = true; return; } // ERROR_BROKEN_PIPE
                    throw new Win32Exception(error);
                }
                if (available == 0) return;
                Check(ReadFile(read, buffer, Math.Min((uint)buffer.Length, available), out uint length, 0));
                int retained = Math.Min(checked((int)length), limit - checked((int)content.Length));
                content.Write(buffer, 0, retained);
                if (retained != length) { Exceeded = true; return; }
            }
        }

        public void Dispose() { Write.Dispose(); read.Dispose(); content.Dispose(); }
    }

    private sealed class ProcessAttributes : IDisposable
    {
        internal nint List { get; private set; }
        private nint jobs;
        private nint handles;
        private bool initialized;

        internal ProcessAttributes(KernelHandle job, KernelHandle stdin, KernelHandle stdout, KernelHandle stderr)
        {
            try
            {
                nuint size = 0;
                InitializeProcThreadAttributeList(0, 2, 0, ref size);
                if (size == 0) throw new Win32Exception(Marshal.GetLastWin32Error());
                List = Marshal.AllocHGlobal(checked((nint)size));
                Check(InitializeProcThreadAttributeList(List, 2, 0, ref size));
                initialized = true;
                jobs = Marshal.AllocHGlobal(nint.Size);
                Marshal.WriteIntPtr(jobs, job.DangerousGetHandle());
                Check(UpdateProcThreadAttribute(List, 0, 0x0002000D, jobs, (nuint)nint.Size, 0, 0));
                handles = Marshal.AllocHGlobal(3 * nint.Size);
                Marshal.WriteIntPtr(handles, 0, stdin.DangerousGetHandle());
                Marshal.WriteIntPtr(handles, nint.Size, stdout.DangerousGetHandle());
                Marshal.WriteIntPtr(handles, 2 * nint.Size, stderr.DangerousGetHandle());
                Check(UpdateProcThreadAttribute(List, 0, 0x00020002, handles, (nuint)(3 * nint.Size), 0, 0));
            }
            catch { Dispose(); throw; }
        }

        public void Dispose()
        {
            if (initialized) DeleteProcThreadAttributeList(List);
            Marshal.FreeHGlobal(List);
            Marshal.FreeHGlobal(jobs);
            Marshal.FreeHGlobal(handles);
        }
    }
}
