using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

namespace PdfAdapterQualification;

internal static class NativeMethods
{
    internal const uint JobTime = 0x4;
    internal const uint JobMemory = 0x200;
    internal const uint DieOnUnhandledException = 0x400;
    internal const uint KillOnJobClose = 0x2000;
    internal const int BasicAccountingInformation = 1;
    internal const int AssociateCompletionPortInformation = 7;
    internal const int ExtendedLimitInformation = 9;
    internal const uint WaitTimeout = 258;

    [StructLayout(LayoutKind.Sequential)]
    internal struct BasicLimit
    {
        internal long PerProcessUserTimeLimit;
        internal long PerJobUserTimeLimit;
        internal uint LimitFlags;
        internal nuint MinimumWorkingSetSize;
        internal nuint MaximumWorkingSetSize;
        internal uint ActiveProcessLimit;
        internal nuint Affinity;
        internal uint PriorityClass;
        internal uint SchedulingClass;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct IoCounters
    {
        internal ulong ReadOperationCount;
        internal ulong WriteOperationCount;
        internal ulong OtherOperationCount;
        internal ulong ReadTransferCount;
        internal ulong WriteTransferCount;
        internal ulong OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct ExtendedLimit
    {
        internal BasicLimit BasicLimitInformation;
        internal IoCounters IoInfo;
        internal nuint ProcessMemoryLimit;
        internal nuint JobMemoryLimit;
        internal nuint PeakProcessMemoryUsed;
        internal nuint PeakJobMemoryUsed;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct Accounting
    {
        internal long TotalUserTime;
        internal long TotalKernelTime;
        internal long ThisPeriodTotalUserTime;
        internal long ThisPeriodTotalKernelTime;
        internal uint TotalPageFaultCount;
        internal uint TotalProcesses;
        internal uint ActiveProcesses;
        internal uint TotalTerminatedProcesses;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct ProcessMemoryCounters
    {
        internal uint Size;
        internal uint PageFaultCount;
        internal nuint PeakWorkingSetSize;
        internal nuint WorkingSetSize;
        internal nuint QuotaPeakPagedPoolUsage;
        internal nuint QuotaPagedPoolUsage;
        internal nuint QuotaPeakNonPagedPoolUsage;
        internal nuint QuotaNonPagedPoolUsage;
        internal nuint PagefileUsage;
        internal nuint PeakPagefileUsage;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct CompletionPort
    {
        internal nint CompletionKey;
        internal nint Port;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct SecurityAttributes
    {
        internal int Length;
        internal nint SecurityDescriptor;
        internal int InheritHandle;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    internal struct StartupInfo
    {
        internal int Size;
        internal nint Reserved;
        internal nint Desktop;
        internal nint Title;
        internal uint X;
        internal uint Y;
        internal uint XSize;
        internal uint YSize;
        internal uint XCountChars;
        internal uint YCountChars;
        internal uint FillAttribute;
        internal uint Flags;
        internal ushort ShowWindow;
        internal ushort ReservedSize;
        internal nint ReservedBytes;
        internal nint StandardInput;
        internal nint StandardOutput;
        internal nint StandardError;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct StartupInfoEx
    {
        internal StartupInfo StartupInfo;
        internal nint AttributeList;
    }

    [StructLayout(LayoutKind.Sequential)]
    internal struct ProcessInformation
    {
        internal nint Process;
        internal nint Thread;
        internal uint ProcessId;
        internal uint ThreadId;
    }

    internal sealed class KernelHandle : SafeHandleZeroOrMinusOneIsInvalid
    {
        public KernelHandle() : base(true) { }
        internal KernelHandle(nint value) : base(true) => SetHandle(value);
        protected override bool ReleaseHandle() => CloseHandle(handle);
    }

    internal static void Check(bool success)
    {
        if (!success) throw new Win32Exception(Marshal.GetLastWin32Error());
    }

    internal static KernelHandle Checked(KernelHandle handle)
    {
        if (!handle.IsInvalid) return handle;
        int error = Marshal.GetLastWin32Error();
        handle.Dispose();
        throw new Win32Exception(error);
    }

    internal static void Set<T>(KernelHandle job, int informationClass, T value) where T : struct
    {
        nint data = Marshal.AllocHGlobal(Marshal.SizeOf<T>());
        try
        {
            Marshal.StructureToPtr(value, data, false);
            Check(SetInformationJobObject(job, informationClass, data, (uint)Marshal.SizeOf<T>()));
        }
        finally { Marshal.FreeHGlobal(data); }
    }

    internal static T Query<T>(KernelHandle job, int informationClass) where T : struct
    {
        nint data = Marshal.AllocHGlobal(Marshal.SizeOf<T>());
        try
        {
            Check(QueryInformationJobObject(job, informationClass, data,
                (uint)Marshal.SizeOf<T>(), out _));
            return Marshal.PtrToStructure<T>(data);
        }
        finally { Marshal.FreeHGlobal(data); }
    }

    [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    internal static extern KernelHandle CreateJobObjectW(nint attributes, string? name);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(nint handle);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(KernelHandle job, int informationClass,
        nint information, uint length);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool QueryInformationJobObject(KernelHandle job, int informationClass,
        nint information, uint length, out uint returnedLength);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool TerminateJobObject(KernelHandle job, uint exitCode);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern KernelHandle CreateIoCompletionPort(nint file, nint existing,
        nuint key, uint concurrentThreads);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool GetQueuedCompletionStatus(KernelHandle port, out uint message,
        out nuint key, out nint overlapped, uint milliseconds);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool CreatePipe(out KernelHandle read, out KernelHandle write,
        ref SecurityAttributes attributes, uint size);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool SetHandleInformation(KernelHandle handle, uint mask, uint flags);

    [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    internal static extern KernelHandle CreateFileW(string name, uint access, uint share,
        ref SecurityAttributes attributes, uint creation, uint flags, nint template);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool InitializeProcThreadAttributeList(nint list, int count,
        uint flags, ref nuint size);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool UpdateProcThreadAttribute(nint list, uint flags, nuint attribute,
        nint value, nuint size, nint previous, nint returnedSize);

    [DllImport("kernel32.dll")]
    internal static extern void DeleteProcThreadAttributeList(nint list);

    [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    internal static extern bool CreateProcessW(string application, StringBuilder commandLine,
        nint processAttributes, nint threadAttributes, bool inheritHandles, uint flags,
        nint environment, string? directory, ref StartupInfoEx startup,
        out ProcessInformation information);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool PeekNamedPipe(KernelHandle pipe, nint buffer, uint size,
        nint bytesRead, out uint available, nint bytesLeft);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool ReadFile(KernelHandle file, byte[] buffer, uint size,
        out uint bytesRead, nint overlapped);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern uint WaitForSingleObject(KernelHandle handle, uint milliseconds);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool GetExitCodeProcess(KernelHandle process, out uint exitCode);

    [DllImport("kernel32.dll", SetLastError = true)]
    internal static extern bool K32GetProcessMemoryInfo(KernelHandle process,
        ref ProcessMemoryCounters counters, uint size);
}
