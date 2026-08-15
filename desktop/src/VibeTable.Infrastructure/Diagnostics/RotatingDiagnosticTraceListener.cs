using System.Diagnostics;

namespace VibeTable.Infrastructure.Diagnostics;

/// <summary>Persists only closed-schema, content-free JSON trace events.</summary>
public sealed class RotatingDiagnosticTraceListener : TraceListener
{
    private readonly RotatingLogSink _sink;

    public RotatingDiagnosticTraceListener(string path) => _sink = new RotatingLogSink(path);

    public override void Write(string? message)
    {
        if (message is not null) WriteLine(message);
    }

    public override void WriteLine(string? message)
    {
        if (message is null || !DiagnosticLogLine.IsSafe(message)) return;
        _sink.WriteLineAsync(message).AsTask().GetAwaiter().GetResult();
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing) _sink.DisposeAsync().AsTask().GetAwaiter().GetResult();
        base.Dispose(disposing);
    }
}
