using System.Buffers;
using System.Text;

namespace VibeTable.Workspace.Diff;

public enum DocumentDiffOutcomeKind
{
    Identical,
    Changed,
    ChangedWithDetails,
    Failure,
}

public enum DocumentDiffFailureKind
{
    Unsupported,
    InvalidContent,
    Io,
    Cancelled,
}

public sealed record DocumentDiffOutcome
{
    private DocumentDiffOutcome(
        DocumentDiffOutcomeKind kind,
        int? addedLines = null,
        int? removedLines = null,
        DocumentDiffFailureKind? failure = null)
    {
        Kind = kind;
        AddedLines = addedLines;
        RemovedLines = removedLines;
        Failure = failure;
    }

    public DocumentDiffOutcomeKind Kind { get; }

    public int? AddedLines { get; }

    public int? RemovedLines { get; }

    public DocumentDiffFailureKind? Failure { get; }

    public static DocumentDiffOutcome Identical { get; } = new(DocumentDiffOutcomeKind.Identical);

    public static DocumentDiffOutcome Changed { get; } = new(DocumentDiffOutcomeKind.Changed);

    public static DocumentDiffOutcome ChangedWithDetails(int addedLines, int removedLines)
    {
        ArgumentOutOfRangeException.ThrowIfNegative(addedLines);
        ArgumentOutOfRangeException.ThrowIfNegative(removedLines);
        return new DocumentDiffOutcome(
            DocumentDiffOutcomeKind.ChangedWithDetails,
            addedLines,
            removedLines);
    }

    public static DocumentDiffOutcome Failed(DocumentDiffFailureKind failure)
    {
        return new DocumentDiffOutcome(DocumentDiffOutcomeKind.Failure, failure: failure);
    }
}

public delegate ValueTask<Stream> DocumentContentStreamFactory(
    CancellationToken cancellationToken);

public sealed class DocumentContentSource
{
    public DocumentContentSource(
        string name,
        string? mimeType,
        long? length,
        DocumentContentStreamFactory openReadAsync)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(name);
        if (length is < 0)
        {
            throw new ArgumentOutOfRangeException(nameof(length));
        }

        Name = name;
        MimeType = mimeType;
        Length = length;
        OpenReadAsync = openReadAsync ?? throw new ArgumentNullException(nameof(openReadAsync));
    }

    public string Name { get; }

    public string? MimeType { get; }

    public long? Length { get; }

    public DocumentContentStreamFactory OpenReadAsync { get; }
}

public sealed record DocumentDiffRequest(
    DocumentContentSource Before,
    DocumentContentSource After);

public interface IDocumentDiffEngine
{
    Task<DocumentDiffOutcome> CompareAsync(
        DocumentDiffRequest request,
        CancellationToken cancellationToken);
}

public sealed class DocumentDiffEngine : IDocumentDiffEngine
{
    private const int BufferSize = 64 * 1024;
    private const int MaxTextCharacters = 4 * 1024 * 1024;
    private const int MaxTextLines = 20_000;
    private const long MaxLcsCells = 4_000_000;

    private static readonly UTF8Encoding StrictUtf8 = new(
        encoderShouldEmitUTF8Identifier: false,
        throwOnInvalidBytes: true);

    private static readonly HashSet<string> TextExtensions = new(StringComparer.OrdinalIgnoreCase)
    {
        ".csv", ".html", ".json", ".log", ".md", ".txt", ".xml", ".yaml", ".yml",
    };

    private static readonly HashSet<string> TextMimeTypes = new(StringComparer.OrdinalIgnoreCase)
    {
        "application/json", "text/csv", "text/html", "text/markdown", "text/plain",
    };

    public async Task<DocumentDiffOutcome> CompareAsync(
        DocumentDiffRequest request,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(request);

        try
        {
            cancellationToken.ThrowIfCancellationRequested();
            var beforeIsText = IsText(request.Before);
            var afterIsText = IsText(request.After);
            if (beforeIsText != afterIsText)
            {
                return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Unsupported);
            }

            await using var before = await request.Before.OpenReadAsync(cancellationToken)
                .ConfigureAwait(false);
            await using var after = await request.After.OpenReadAsync(cancellationToken)
                .ConfigureAwait(false);
            var identical = await StreamsEqualAsync(before, after, cancellationToken)
                .ConfigureAwait(false);
            if (identical)
            {
                return DocumentDiffOutcome.Identical;
            }

            if (!beforeIsText)
            {
                return DocumentDiffOutcome.Changed;
            }

            return await CompareTextAsync(request, cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Cancelled);
        }
        catch (IOException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Io);
        }
        catch (UnauthorizedAccessException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.Io);
        }
        catch (DecoderFallbackException)
        {
            return DocumentDiffOutcome.Failed(DocumentDiffFailureKind.InvalidContent);
        }
    }

    private static bool IsText(DocumentContentSource source)
    {
        if (!string.IsNullOrWhiteSpace(source.MimeType))
        {
            var separator = source.MimeType.IndexOf(';');
            var mediaType = (separator < 0
                    ? source.MimeType
                    : source.MimeType[..separator])
                .Trim();
            return TextMimeTypes.Contains(mediaType);
        }

        return TextExtensions.Contains(Path.GetExtension(source.Name));
    }

    private static async Task<DocumentDiffOutcome> CompareTextAsync(
        DocumentDiffRequest request,
        CancellationToken cancellationToken)
    {
        await using var beforeStream = await request.Before.OpenReadAsync(cancellationToken)
            .ConfigureAwait(false);
        await using var afterStream = await request.After.OpenReadAsync(cancellationToken)
            .ConfigureAwait(false);
        var beforeLines = await ReadLinesAsync(beforeStream, cancellationToken).ConfigureAwait(false);
        var afterLines = await ReadLinesAsync(afterStream, cancellationToken).ConfigureAwait(false);
        if (beforeLines is null || afterLines is null)
        {
            return DocumentDiffOutcome.Changed;
        }

        var cellCount = (long)beforeLines.Count * afterLines.Count;
        if (cellCount > MaxLcsCells)
        {
            return DocumentDiffOutcome.Changed;
        }

        var lcs = LongestCommonSubsequence(beforeLines, afterLines, cancellationToken);
        var addedLines = afterLines.Count - lcs;
        var removedLines = beforeLines.Count - lcs;
        return addedLines == 0 && removedLines == 0
            ? DocumentDiffOutcome.Changed
            : DocumentDiffOutcome.ChangedWithDetails(addedLines, removedLines);
    }

    private static async Task<List<string>?> ReadLinesAsync(
        Stream stream,
        CancellationToken cancellationToken)
    {
        using var reader = new StreamReader(
            stream,
            StrictUtf8,
            detectEncodingFromByteOrderMarks: true,
            bufferSize: 4096,
            leaveOpen: true);
        var lines = new List<string>();
        var currentLine = new StringBuilder();
        var characters = 0L;
        var previousWasCarriageReturn = false;
        var buffer = ArrayPool<char>.Shared.Rent(4096);
        try
        {
            while (true)
            {
                cancellationToken.ThrowIfCancellationRequested();
                var read = await reader.ReadAsync(
                    buffer.AsMemory(0, buffer.Length),
                    cancellationToken).ConfigureAwait(false);
                if (read == 0)
                {
                    break;
                }

                for (var index = 0; index < read; index++)
                {
                    if ((index & 255) == 0)
                    {
                        cancellationToken.ThrowIfCancellationRequested();
                    }

                    var character = buffer[index];
                    characters++;
                    if (characters > MaxTextCharacters)
                    {
                        return null;
                    }

                    if (character == '\n')
                    {
                        if (previousWasCarriageReturn)
                        {
                            previousWasCarriageReturn = false;
                            continue;
                        }

                        if (!TryAddLine(lines, currentLine))
                        {
                            return null;
                        }

                        continue;
                    }

                    if (character == '\r')
                    {
                        if (!TryAddLine(lines, currentLine))
                        {
                            return null;
                        }

                        previousWasCarriageReturn = true;
                        continue;
                    }

                    previousWasCarriageReturn = false;
                    currentLine.Append(character);
                }
            }

            if (currentLine.Length > 0 && !TryAddLine(lines, currentLine))
            {
                return null;
            }

            return lines;
        }
        finally
        {
            ArrayPool<char>.Shared.Return(buffer);
        }
    }

    private static bool TryAddLine(List<string> lines, StringBuilder currentLine)
    {
        if (lines.Count == MaxTextLines)
        {
            return false;
        }

        lines.Add(currentLine.ToString());
        currentLine.Clear();
        return true;
    }

    private static int LongestCommonSubsequence(
        IReadOnlyList<string> before,
        IReadOnlyList<string> after,
        CancellationToken cancellationToken)
    {
        var previous = new int[after.Count + 1];
        var current = new int[after.Count + 1];
        for (var beforeIndex = 1; beforeIndex <= before.Count; beforeIndex++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            for (var afterIndex = 1; afterIndex <= after.Count; afterIndex++)
            {
                if ((afterIndex & 255) == 0)
                {
                    cancellationToken.ThrowIfCancellationRequested();
                }

                current[afterIndex] = before[beforeIndex - 1] == after[afterIndex - 1]
                    ? previous[afterIndex - 1] + 1
                    : Math.Max(previous[afterIndex], current[afterIndex - 1]);
            }

            (previous, current) = (current, previous);
            Array.Clear(current);
        }

        return previous[after.Count];
    }

    private static async Task<bool> StreamsEqualAsync(
        Stream before,
        Stream after,
        CancellationToken cancellationToken)
    {
        var beforeBuffer = ArrayPool<byte>.Shared.Rent(BufferSize);
        var afterBuffer = ArrayPool<byte>.Shared.Rent(BufferSize);
        try
        {
            while (true)
            {
                cancellationToken.ThrowIfCancellationRequested();
                var beforeRead = await FillBufferAsync(before, beforeBuffer, cancellationToken)
                    .ConfigureAwait(false);
                var afterRead = await FillBufferAsync(after, afterBuffer, cancellationToken)
                    .ConfigureAwait(false);
                if (beforeRead != afterRead)
                {
                    return false;
                }

                if (beforeRead == 0)
                {
                    return true;
                }

                if (!beforeBuffer.AsSpan(0, beforeRead).SequenceEqual(afterBuffer.AsSpan(0, afterRead)))
                {
                    return false;
                }
            }
        }
        finally
        {
            ArrayPool<byte>.Shared.Return(beforeBuffer);
            ArrayPool<byte>.Shared.Return(afterBuffer);
        }
    }

    private static async Task<int> FillBufferAsync(
        Stream stream,
        byte[] buffer,
        CancellationToken cancellationToken)
    {
        var totalRead = 0;
        while (totalRead < buffer.Length)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var read = await stream.ReadAsync(
                buffer.AsMemory(totalRead, buffer.Length - totalRead),
                cancellationToken).ConfigureAwait(false);
            if (read == 0)
            {
                break;
            }

            totalRead += read;
        }

        return totalRead;
    }
}
