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

    public DocumentDiffDetails? Details { get; init; }

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

/// <summary>Provider output consumed by the shared revision-session lifecycle.</summary>
public sealed record DocumentDiffDetails(
    DocumentDiffFormat Format,
    IReadOnlyList<DocumentDiffChange> Changes,
    DocumentDiffCoverage Coverage);

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

    public static bool IsText(DocumentContentSource source)
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

        var (lcs, changes, truncated) = CompareLines(beforeLines, afterLines, cancellationToken);
        var addedLines = afterLines.Count - lcs;
        var removedLines = beforeLines.Count - lcs;
        var outcome = addedLines == 0 && removedLines == 0
            ? DocumentDiffOutcome.Changed
            : DocumentDiffOutcome.ChangedWithDetails(addedLines, removedLines);
        return outcome with { Details = new DocumentDiffDetails(
            DocumentDiffFormat.Text, changes, new DocumentDiffCoverage(
                [new(DocumentDiffCoverageArea.VisibleText, DocumentDiffCoverageStatus.Covered)],
                truncated)) };
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

    private static (int Length, DocumentDiffChange[] Changes, bool Truncated) CompareLines(
        IReadOnlyList<string> before,
        IReadOnlyList<string> after,
        CancellationToken cancellationToken)
    {
        var lengths = new int[before.Count + 1, after.Count + 1];
        for (var beforeIndex = before.Count - 1; beforeIndex >= 0; beforeIndex--)
        {
            cancellationToken.ThrowIfCancellationRequested();
            for (var afterIndex = after.Count - 1; afterIndex >= 0; afterIndex--)
            {
                if ((afterIndex & 255) == 0)
                {
                    cancellationToken.ThrowIfCancellationRequested();
                }

                lengths[beforeIndex, afterIndex] = before[beforeIndex] == after[afterIndex]
                    ? lengths[beforeIndex + 1, afterIndex + 1] + 1
                    : Math.Max(lengths[beforeIndex + 1, afterIndex], lengths[beforeIndex, afterIndex + 1]);
            }
        }
        var changes = new List<DocumentDiffChange>();
        bool truncated = false;
        int b = 0, a = 0;
        while (b < before.Count || a < after.Count)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (b < before.Count && a < after.Count && before[b] == after[a])
            {
                b++;
                a++;
                continue;
            }
            int location = b;
            var removed = new List<string>();
            var added = new List<string>();
            while (b < before.Count || a < after.Count)
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (b < before.Count && a < after.Count && before[b] == after[a]) break;
                if (a < after.Count && (b == before.Count || lengths[b, a + 1] > lengths[b + 1, a]))
                    added.Add(after[a++]);
                else
                    removed.Add(before[b++]);
            }
            changes.Add(new DocumentDiffChange(Guid.NewGuid(),
                removed.Count == 0 ? DocumentDiffChangeKind.Insert
                    : added.Count == 0 ? DocumentDiffChangeKind.Delete : DocumentDiffChangeKind.Replace,
                new DocumentDiffLocation(DocumentDiffPart.Body, paragraphIndex: location),
                Snippet(removed, DocumentDiffRichRunRole.Deleted, location > 0 ? before[location - 1] : null,
                    b < before.Count ? before[b] : null, ref truncated),
                Snippet(added, DocumentDiffRichRunRole.Inserted, location > 0 ? before[location - 1] : null,
                    b < before.Count ? before[b] : null, ref truncated), DocumentDiffConfidence.Exact));
        }
        return (lengths[0, 0], changes.ToArray(), truncated);
    }

    private static DocumentDiffRichSnippet? Snippet(List<string> lines, DocumentDiffRichRunRole role,
        string? previous, string? next, ref bool truncated)
    {
        if (lines.Count == 0) return null;
        string text = string.Join("\n", lines) + "\n";
        if (text.Length > 2048)
        {
            truncated = true;
            int end = char.IsHighSurrogate(text[2047]) ? 2047 : 2048;
            text = text[..end] + "…";
        }
        var runs = new List<DocumentDiffRichRun>();
        for (int position = 0; position < 3; position++)
        {
            if (position == 1)
            {
                runs.Add(new DocumentDiffRichRun(text, role));
                continue;
            }
            string? context = position == 0 ? previous : next;
            if (context is null) continue;
            string content = context;
            if (content.Length > 256)
            {
                truncated = true;
                content = content[..(char.IsHighSurrogate(content[255]) ? 255 : 256)] + "…";
            }
            var run = new DocumentDiffRichRun(content + "\n", DocumentDiffRichRunRole.Context);
            runs.Add(run);
        }
        return new(runs);
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
