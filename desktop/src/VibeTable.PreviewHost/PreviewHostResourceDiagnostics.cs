using System.Diagnostics;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace VibeTable.PreviewHost;

// Current-helper cumulative counters only. Pair Started/Returned (or the next
// PreviewSessionFailed) in the same preview-PID log; CPU ticks are 100 ns units.
internal static class PreviewHostResourceDiagnostics
{
    internal static void Trace(PreviewHostStage stage, Func<PreviewResourceSample>? capture = null)
    {
        try { TraceCore(stage, capture); }
        catch { /* Diagnostics, including formatting/sinks, must not change COM behavior. */ }
    }

    private static void TraceCore(PreviewHostStage stage, Func<PreviewResourceSample>? capture)
    {
        if (stage is not (PreviewHostStage.ComActivationStarted or PreviewHostStage.ComActivationReturned
            or PreviewHostStage.HandlerStreamInitializeStarted or PreviewHostStage.HandlerStreamInitializeReturned
            or PreviewHostStage.HandlerFileInitializeStarted or PreviewHostStage.HandlerFileInitializeReturned
            or PreviewHostStage.HandlerDoPreviewStarted or PreviewHostStage.HandlerDoPreviewReturned
            or PreviewHostStage.PreviewSessionFailed)) return;

        PreviewResourceSample? sample = null;
        try { sample = (capture ?? Capture)(); }
        catch { /* Counter access must not change the COM operation. */ }
        if (sample is not { CpuTicks: >= 0 } value)
        {
            PreviewHostEntry.TraceEvent($"preview.host.resources.{stage}.unavailable");
            return;
        }
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.resources.{stage}.cpuTicks.{value.CpuTicks}"));
        if (value.Io is not { } io)
        {
            PreviewHostEntry.TraceEvent($"preview.host.io.{stage}.unavailable");
            return;
        }
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.ioOps.{stage}.read.{io.ReadOperations}.write.{io.WriteOperations}.other.{io.OtherOperations}"));
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.ioBytes.{stage}.read.{io.ReadBytes}.write.{io.WriteBytes}.other.{io.OtherBytes}"));
    }

    internal static PreviewResourceSample Capture()
    {
        using var process = Process.GetCurrentProcess();
        long cpuTicks = process.TotalProcessorTime.Ticks;
        return new(cpuTicks, ReadIo(process.SafeHandle));
    }

    internal static PreviewIoCounters? ReadIo(SafeProcessHandle process)
    {
        try
        {
            return GetProcessIoCounters(process, out PreviewIoCounters counters) ? counters : null;
        }
        catch { return null; } // Unsupported/denied counters stay unavailable, never zero.
    }

    // Read-only current process query. No paths, file contents or other PIDs.
    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetProcessIoCounters(SafeProcessHandle process, out PreviewIoCounters counters);
}

internal readonly record struct PreviewResourceSample(long CpuTicks, PreviewIoCounters? Io);

[StructLayout(LayoutKind.Sequential)]
internal struct PreviewIoCounters
{
    public ulong ReadOperations;
    public ulong WriteOperations;
    public ulong OtherOperations;
    public ulong ReadBytes;
    public ulong WriteBytes;
    public ulong OtherBytes;
}
