using System;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Rpc;

/// <summary>
/// Reads and writes JSON-RPC frames over a pair of streams using newline
/// delimiters. Each frame is a single UTF-8 JSON object terminated by '\n'.
/// </summary>
/// <remarks>
/// <para>
/// Wire conventions match the Python framing in <c>backend/rpc/framing.py</c>:
/// compact JSON, no BOM, '\n' as the sole line terminator, and a hard 4 MiB
/// cap per frame enforced <em>before</em> the JSON is parsed.
/// </para>
/// <para>
/// The byte limit is enforced with a manual scan so that an oversized line
/// never has to be buffered in full: once the running byte count crosses
/// <see cref="MaxFrameBytes"/> we stop reading, drain the rest of the line to
/// re-synchronize the stream, and surface a <see cref="RpcException"/>.
/// </para>
/// </remarks>
public sealed class StreamJsonLineTransport : IJsonLineTransport
{
    /// <summary>
    /// Hard cap on a single frame, in bytes. Matches
    /// <c>backend.rpc.framing.MAX_FRAME_BYTES</c>.
    /// </summary>
    public const int MaxFrameBytes = 4 * 1024 * 1024;

    private static readonly UTF8Encoding Utf8NoBom = new(encoderShouldEmitUTF8Identifier: false);

    private static readonly char[] NewLineChars = { '\n' };
    private static readonly ReadOnlyMemory<char> NewLineCharMemory = new(NewLineChars);

    private readonly Stream _readStream;
    private readonly StreamWriter _writer;
    private readonly SemaphoreSlim _writeGate = new(1, 1);
    private int _disposed;

    public StreamJsonLineTransport(Stream readStream, Stream writeStream)
    {
        if (readStream is null)
        {
            throw new ArgumentNullException(nameof(readStream));
        }
        if (writeStream is null)
        {
            throw new ArgumentNullException(nameof(writeStream));
        }
        if (!readStream.CanRead)
        {
            throw new ArgumentException("readStream must be readable.", nameof(readStream));
        }
        if (!writeStream.CanWrite)
        {
            throw new ArgumentException("writeStream must be writable.", nameof(writeStream));
        }

        _readStream = readStream;
        _writer = new StreamWriter(writeStream, Utf8NoBom)
        {
            NewLine = "\n",
            AutoFlush = true,
        };
    }

    public async Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();

        // Manual byte-oriented read so we can enforce the byte limit before
        // touching System.Text.Json and before buffering the entire line.
        // The limit applies to the entire on-wire line including the trailing
        // '\n' (matches backend/rpc/framing.py which counts len(raw) inclusive
        // of the newline delimiter from readuntil).
        var buffer = new MemoryStream(MaxFrameBytes);
        var oneByte = new byte[1];
        bool sawAnyByte = false;
        int totalRead = 0;

        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();

            int read = await _readStream.ReadAsync(oneByte.AsMemory(0, 1), cancellationToken)
                .ConfigureAwait(false);
            if (read == 0)
            {
                // Clean EOF. If we accumulated partial bytes without a newline,
                // that is a truncated frame: treat as malformed input.
                if (sawAnyByte)
                {
                    throw new EndOfStreamException(
                        "RPC stream ended mid-frame without a trailing newline.");
                }
                return null;
            }

            sawAnyByte = true;
            totalRead++;
            byte b = oneByte[0];

            // Enforce the byte limit BEFORE consuming the byte (and before
            // any JSON parsing). The limit covers the full on-wire line,
            // newline included: a line whose on-wire size exceeds
            // MaxFrameBytes is rejected as soon as the limit is crossed,
            // regardless of whether the next byte would have been the
            // terminator.
            if (totalRead > MaxFrameBytes)
            {
                await DrainRestOfLineAsync(cancellationToken).ConfigureAwait(false);
                throw new RpcException(
                    $"RPC frame exceeds the {MaxFrameBytes}-byte limit.");
            }

            if (b == (byte)'\n')
            {
                break;
            }

            buffer.WriteByte(b);
        }

        // Empty frame (just a newline) is treated as clean EOF, mirroring
        // the Python framing layer which rejects non-object payloads anyway.
        if (buffer.Length == 0)
        {
            return null;
        }

        var utf8 = buffer.ToArray();
        try
        {
            using var doc = JsonDocument.Parse(utf8);
            // Clone the root so it survives disposal of the JsonDocument.
            return doc.RootElement.Clone();
        }
        catch (JsonException ex)
        {
            throw new JsonException(
                $"Failed to parse RPC frame as JSON: {ex.Message}", ex);
        }
    }

    public async Task WriteAsync(string line, CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        if (line is null)
        {
            throw new ArgumentNullException(nameof(line));
        }

        // Validate length up front: UTF-8 byte count is what the wire sees.
        int byteCount = Utf8NoBom.GetByteCount(line);
        if (byteCount >= MaxFrameBytes)
        {
            throw new RpcException(
                $"RPC frame exceeds the {MaxFrameBytes}-byte limit.");
        }

        await _writeGate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            cancellationToken.ThrowIfCancellationRequested();

            // Cancellation is an admission decision only. Once the first byte
            // of a newline-delimited frame may be written, finish that frame:
            // interrupting a StreamWriter flush can leave a JSON prefix in the
            // pipe, which corrupts the next otherwise-valid request.
            await _writer.WriteAsync(line.AsMemory(), CancellationToken.None)
                .ConfigureAwait(false);
            await _writer.WriteAsync(NewLineCharMemory, CancellationToken.None)
                .ConfigureAwait(false);
            await _writer.FlushAsync(CancellationToken.None).ConfigureAwait(false);
        }
        finally
        {
            _writeGate.Release();
        }
    }

    public ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return default;
        }

        _writeGate.Dispose();
        return new ValueTask(_writer.DisposeAsync().AsTask());
    }

    private async Task DrainRestOfLineAsync(CancellationToken cancellationToken)
    {
        var oneByte = new byte[1];
        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();
            int read = await _readStream.ReadAsync(oneByte.AsMemory(0, 1), cancellationToken)
                .ConfigureAwait(false);
            if (read == 0 || oneByte[0] == (byte)'\n')
            {
                return;
            }
        }
    }

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
        {
            throw new ObjectDisposedException(nameof(StreamJsonLineTransport));
        }
    }
}
