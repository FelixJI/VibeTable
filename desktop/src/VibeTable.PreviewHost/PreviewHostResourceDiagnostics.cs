using System.Diagnostics;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace VibeTable.PreviewHost;

// Current-helper cumulative counters only. Pair Started/Returned (or the next
// PreviewSessionFailed) in the same preview-PID log; CPU ticks are 100 ns units.
internal static class PreviewHostResourceDiagnostics
{
    // Existing COM boundaries execute on the preview STA. One slot per caller
    // thread prevents an unrelated thread from completing another call's pair.
    [ThreadStatic] private static PendingObservation? _pending;

    internal static void Trace(PreviewHostStage stage, Func<PreviewResourceSample>? capture = null,
        Action? writePhase = null, Func<DateTimeOffset>? clock = null)
    {
        bool phaseWritten = false;
        void Phase()
        {
            phaseWritten = true;
            try { writePhase?.Invoke(); }
            catch { /* A sink failure must not change the COM operation. */ }
        }
        try { TraceCore(stage, capture, Phase, clock); }
        catch
        {
            _pending = null;
            if (!phaseWritten) Phase();
        }
    }

    private static void TraceCore(PreviewHostStage stage, Func<PreviewResourceSample>? capture,
        Action writePhase, Func<DateTimeOffset>? clock)
    {
        bool started = stage is PreviewHostStage.ComActivationStarted
            or PreviewHostStage.HandlerStreamInitializeStarted or PreviewHostStage.HandlerFileInitializeStarted
            or PreviewHostStage.HandlerDoPreviewStarted;
        bool terminal = stage is PreviewHostStage.ComActivationReturned
            or PreviewHostStage.HandlerStreamInitializeReturned or PreviewHostStage.HandlerFileInitializeReturned
            or PreviewHostStage.HandlerDoPreviewReturned or PreviewHostStage.PreviewSessionFailed;
        if (started)
        {
            DiscardPending();
            writePhase();
            // Nothing is emitted after this capture until the matching end is captured.
            _pending = new(stage, Observe(capture, clock));
            return;
        }
        PendingObservation? pending = _pending;
        _pending = null;
        if (terminal && pending is { } begin && Matches(begin.Stage, stage))
        {
            Observation end = Observe(capture, clock); // Before phase or resource flushes.
            writePhase();
            Write(begin.Stage, begin.Observation);
            Write(stage, end);
            return;
        }
        // Unexpected phases invalidate the open interval rather than including
        // their synchronous writes in a later apparent COM measurement.
        if (pending is { } orphan) Unpaired(orphan.Stage, orphan.Observation.At);
        writePhase();
        if (terminal) Unpaired(stage, DateTimeOffset.UtcNow);
    }

    private static bool Matches(PreviewHostStage start, PreviewHostStage end)
        => end == PreviewHostStage.PreviewSessionFailed || (start, end) is
            (PreviewHostStage.ComActivationStarted, PreviewHostStage.ComActivationReturned) or
            (PreviewHostStage.HandlerStreamInitializeStarted, PreviewHostStage.HandlerStreamInitializeReturned) or
            (PreviewHostStage.HandlerFileInitializeStarted, PreviewHostStage.HandlerFileInitializeReturned) or
            (PreviewHostStage.HandlerDoPreviewStarted, PreviewHostStage.HandlerDoPreviewReturned);

    private static Observation Observe(Func<PreviewResourceSample>? capture, Func<DateTimeOffset>? clock)
    {
        try { return new((clock ?? (() => DateTimeOffset.UtcNow))(), (capture ?? Capture)()); }
        catch { return new(DateTimeOffset.UtcNow, null); }
    }

    private static void DiscardPending()
    {
        PendingObservation? pending = _pending;
        _pending = null;
        if (pending is { } orphan) Unpaired(orphan.Stage, orphan.Observation.At);
    }

    private static void Unpaired(PreviewHostStage stage, DateTimeOffset at)
        => PreviewHostEntry.TraceEvent($"preview.host.resources.{stage}.unpaired", observedAt: at);

    private static void Write(PreviewHostStage stage, Observation observation)
    {
        if (observation.Sample is not { CpuTicks: >= 0 } value)
        {
            PreviewHostEntry.TraceEvent($"preview.host.resources.{stage}.unavailable", observedAt: observation.At);
            return;
        }
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.resources.{stage}.cpuTicks.{value.CpuTicks}"), observedAt: observation.At);
        if (value.Io is not { } io)
        {
            PreviewHostEntry.TraceEvent($"preview.host.io.{stage}.unavailable", observedAt: observation.At);
            return;
        }
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.ioOps.{stage}.read.{io.ReadOperations}.write.{io.WriteOperations}.other.{io.OtherOperations}"), observedAt: observation.At);
        PreviewHostEntry.TraceEvent(FormattableString.Invariant(
            $"preview.host.ioBytes.{stage}.read.{io.ReadBytes}.write.{io.WriteBytes}.other.{io.OtherBytes}"), observedAt: observation.At);
    }

    private readonly record struct Observation(DateTimeOffset At, PreviewResourceSample? Sample);
    private readonly record struct PendingObservation(PreviewHostStage Stage, Observation Observation);

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
