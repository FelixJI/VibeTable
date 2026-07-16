using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

namespace VibeTable.Infrastructure.Backend;

/// <summary>
/// Owns a Windows Job Object configured with
/// <see cref="JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE"/> so that any process
/// assigned to the job — and any of its descendants — is killed by the OS
/// when this handle is closed (i.e. when the host dies or disposes the
/// supervisor).
/// </summary>
/// <remarks>
/// <para>
/// Without this guarantee a host crash would orphan the Python backend: the
/// redirected stdin/stdout pipes would close, but a backend that ignores EOF
/// (or blocks on a long RPC) could outlive the host. Tying the child to a
/// Job Object that the OS auto-kills on handle close is the standard Windows
/// pattern for "kill my child if I die".
/// </para>
/// <para>
/// On non-Windows hosts this class is a no-op: <see cref="Handle"/> is
/// <see cref="IntPtr.Zero"/> and <see cref="AssignProcess"/> returns without
/// doing anything. The supervisor still tears down the child via
/// <see cref="System.Diagnostics.Process"/> on <c>Dispose</c>, so the
/// cross-platform guarantee is "best effort on close, hard kill on Windows".
/// </para>
/// <para>
/// P/Invoke reference (Windows SDK):
/// </para>
/// <list type="bullet">
/// <item><c>CreateJobObjectW(lpJobAttributes=NULL, lpName=NULL)</c> — creates
/// an unnamed job. Returns <c>NULL</c> on failure; <see cref="Win32Exception"/>
/// surfaces <c>GetLastError()</c>.</item>
/// <item><c>SetInformationJobObject(JobObjectExtendedLimitInformation=9,
/// &amp;info, sizeof(info))</c> — applies the extended-limit struct whose
/// <c>BasicLimitInformation.LimitFlags</c> carries
/// <see cref="JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE"/>.</item>
/// <item><c>AssignProcessToJobObject(job, hProcess)</c> — binds the child
/// process to the job. Must be called before the child can spawn its own
/// descendants to guarantee coverage.</item>
/// </list>
/// <para>
/// The kill-on-close behavior is triggered by closing the job handle: when
/// the last open handle to the job is released (either explicitly here in
/// <see cref="Dispose"/> or implicitly by the OS when the host process dies),
/// the kernel terminates every process in the job.
/// </para>
/// </remarks>
internal sealed class JobObject : IDisposable
{
    /// <summary>
    /// Job Object limit flag: terminate all processes in the job when the
    /// last handle to the job is closed. Value = 0x2000.
    /// </summary>
    /// <remarks>
    /// Equivalent to the Win32 <c>JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE</c>
    /// constant. Defined here (rather than via a magic number) so the
    /// contract is self-documenting at the call site.
    /// </remarks>
    private const uint JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000;

    /// <summary>
    /// <c>JOBOBJECTINFOCLASS.ExtendedLimitInformation</c> = 9.
    /// </summary>
    private const int JobObjectExtendedLimitInformation = 9;

    private IntPtr _handle;
    private bool _disposed;

    private JobObject(IntPtr handle)
    {
        _handle = handle;
    }

    /// <summary>
    /// True on Windows; on other OSes the wrapper is inert and the supervisor
    /// relies on <see cref="System.Diagnostics.Process.Kill"/> for cleanup.
    /// </summary>
    public static bool IsSupported => RuntimeInformation.IsOSPlatform(OSPlatform.Windows);

    /// <summary>
    /// Creates a new Job Object with kill-on-close semantics. On non-Windows
    /// hosts returns a no-op instance.
    /// </summary>
    /// <exception cref="Win32Exception">if <c>CreateJobObject</c> or
    /// <c>SetInformationJobObject</c> fails on Windows.</exception>
    public static JobObject Create()
    {
        if (!IsSupported)
        {
            return new JobObject(IntPtr.Zero);
        }

        IntPtr handle = CreateJobObjectW(IntPtr.Zero, null);
        if (handle == IntPtr.Zero)
        {
            throw new Win32Exception(Marshal.GetLastWin32Error(),
                "CreateJobObject failed.");
        }

        // Configure kill-on-close BEFORE assigning any process, so the job is
        // armed for the whole lifetime of every assigned child.
        var info = new JOBOBJECT_BASIC_LIMIT_INFORMATION
        {
            LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
        };
        var extended = new JOBOBJECT_EXTENDED_LIMIT_INFORMATION
        {
            BasicLimitInformation = info,
        };

        int size = Marshal.SizeOf<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>();
        IntPtr ptr = Marshal.AllocHGlobal(size);
        try
        {
            Marshal.StructureToPtr(extended, ptr, false);
            if (!SetInformationJobObject(handle,
                    JobObjectExtendedLimitInformation,
                    ptr,
                    (uint)size))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(),
                    "SetInformationJobObject(KILL_ON_JOB_CLOSE) failed.");
            }
        }
        finally
        {
            Marshal.FreeHGlobal(ptr);
        }

        return new JobObject(handle);
    }

    /// <summary>
    /// Binds the given process handle to this job. On non-Windows no-ops.
    /// Safe to call once per child; the OS rejects duplicate assignment with
    /// an access-denied error which we intentionally swallow (the child is
    /// already covered).
    /// </summary>
    public void AssignProcess(IntPtr processHandle)
    {
        if (_disposed)
        {
            throw new ObjectDisposedException(nameof(JobObject));
        }
        if (!IsSupported || _handle == IntPtr.Zero || processHandle == IntPtr.Zero)
        {
            return;
        }

        if (!AssignProcessToJobObject(_handle, processHandle))
        {
            int err = Marshal.GetLastWin32Error();
            // ERROR_ACCESS_DENIED (5) is benign: the process is already in a
            // job (nested jobs are allowed on Vista+, but the simplest case is
            // a re-assignment to the same job). Anything else is a real error.
            if (err != 5)
            {
                throw new Win32Exception(err,
                    "AssignProcessToJobObject failed.");
            }
        }
    }

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;

        if (_handle != IntPtr.Zero)
        {
            // Closing the last handle to the job triggers kill-on-close for
            // every process still assigned to it.
            CloseHandle(_handle);
            _handle = IntPtr.Zero;
        }
    }

    // ---------- P/Invoke ----------

    [DllImport(Lib.Kernel32, SetLastError = true, CharSet = CharSet.Unicode,
        EntryPoint = "CreateJobObjectW")]
    private static extern IntPtr CreateJobObjectW(IntPtr lpJobAttributes, string? lpName);

    [DllImport(Lib.Kernel32, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetInformationJobObject(
        IntPtr hJob,
        int infoClass,
        IntPtr lpJobObjectInfo,
        uint cbJobObjectInfoLength);

    [DllImport(Lib.Kernel32, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool AssignProcessToJobObject(IntPtr hJob, IntPtr hProcess);

    [DllImport(Lib.Kernel32, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CloseHandle(IntPtr hObject);

    private static class Lib
    {
        public const string Kernel32 = "kernel32.dll";
    }

    // ---------- structs ----------

    /// <summary>
    /// Win32 <c>JOBOBJECT_BASIC_LIMIT_INFORMATION</c>. Only
    /// <see cref="LimitFlags"/> is used here; the other fields default to
    /// zero, which leaves the job's CPU/ioctl/memory limits unchanged.
    /// </summary>
    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_BASIC_LIMIT_INFORMATION
    {
        public long PerProcessUserTimeLimit;
        public long PerJobUserTimeLimit;
        public uint LimitFlags;
        public UIntPtr MinimumWorkingSetSize;
        public UIntPtr MaximumWorkingSetSize;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint PriorityClass;
        public uint SchedulingClass;
    }

    /// <summary>
    /// Win32 <c>IO_COUNTERS</c>. Unused but required to make
    /// <see cref="JOBOBJECT_EXTENDED_LIMIT_INFORMATION"/> byte-exact with the
    /// SDK layout.
    /// </summary>
    [StructLayout(LayoutKind.Sequential)]
    private struct IO_COUNTERS
    {
        public ulong ReadOperationCount;
        public ulong WriteOperationCount;
        public ulong OtherOperationCount;
        public ulong ReadTransferCount;
        public ulong WriteTransferCount;
        public ulong OtherTransferCount;
    }

    /// <summary>
    /// Win32 <c>JOBOBJECT_EXTENDED_LIMIT_INFORMATION</c>. We only set
    /// <see cref="BasicLimitInformation"/>.<see cref="JOBOBJECT_BASIC_LIMIT_INFORMATION.LimitFlags"/>;
    /// the I/O and memory counter fields are present to preserve struct size.
    /// </summary>
    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION
    {
        public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
        public IO_COUNTERS IoInfo;
        public UIntPtr ProcessMemoryLimit;
        public UIntPtr JobMemoryLimit;
        public UIntPtr PeakProcessMemoryUsed;
        public UIntPtr PeakJobMemoryUsed;
    }
}
