namespace VibeTable.Desktop.Tests;

internal sealed class TestSseStream(byte[] bytes, int chunkSize = 4093, bool blockAtEnd = false) : MemoryStream(bytes)
{
    internal int BytesRead { get; private set; }
    internal bool Disposed { get; private set; }
    internal Exception? ReadFailure { get; init; }
    internal TaskCompletionSource Blocked { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken token = default)
    {
        if (ReadFailure is not null) throw ReadFailure;
        if (blockAtEnd && Position == Length)
        {
            Blocked.TrySetResult();
            await Task.Delay(Timeout.Infinite, token);
        }
        int read = await base.ReadAsync(buffer[..Math.Min(buffer.Length, chunkSize)], token);
        BytesRead += read;
        return read;
    }

    protected override void Dispose(bool disposing)
    {
        Disposed = true;
        base.Dispose(disposing);
    }
}
