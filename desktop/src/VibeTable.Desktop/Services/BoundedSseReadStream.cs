using System.IO;

namespace VibeTable.Desktop.Services;

/// <summary>Bounds unfinished SSE blocks before the .NET parser can grow its buffers.</summary>
internal sealed class BoundedSseReadStream(Stream source) : Stream
{
    internal const int MaxJsonBytes = 4 * 1024 * 1024;
    // Go writes one id/event/data block: rt:int64 (22 bytes), topic (18 bytes),
    // and 21 bytes of LF framing. 128 also accommodates CRLF and an initial BOM.
    // This producer-profile wire budget is not a process RAM limit or a promise
    // to accept arbitrary SSE metadata/comment combinations.
    private const int MaxBlockBytes = MaxJsonBytes + 128;
    private int _blockBytes;
    private bool _lineHasContent;
    private bool _pendingCr;

    public override async ValueTask<int> ReadAsync(
        Memory<byte> buffer, CancellationToken cancellationToken = default)
    {
        int read = await source.ReadAsync(buffer[..Math.Min(buffer.Length, 16 * 1024)],
            cancellationToken).ConfigureAwait(false);
        foreach (byte value in buffer.Span[..read])
        {
            if (_pendingCr)
            {
                _pendingCr = false;
                if (value == '\n')
                {
                    CountByte();
                    EndLine();
                    continue;
                }
                EndLine();
            }
            CountByte();
            if (value == '\r') _pendingCr = true;
            else if (value == '\n') EndLine();
            else _lineHasContent = true;
        }
        return read;
    }

    private void CountByte()
    {
        if (++_blockBytes > MaxBlockBytes)
            throw new InvalidDataException("Realtime SSE block exceeds the wire budget.");
    }

    private void EndLine()
    {
        if (!_lineHasContent) _blockBytes = 0;
        _lineHasContent = false;
    }

    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();
    public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
    public override void Flush() => throw new NotSupportedException();
    public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
}
