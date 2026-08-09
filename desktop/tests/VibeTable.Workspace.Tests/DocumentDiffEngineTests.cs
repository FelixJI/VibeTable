using System.Text;
using VibeTable.Workspace.Diff;

namespace VibeTable.Workspace.Tests;

[TestClass]
public sealed class DocumentDiffEngineTests
{
    [TestMethod]
    public async Task CompareAsync_IdenticalBinaryContent_ReturnsIdentical()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request("sample.bin", "same bytes", "same bytes");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, outcome.Kind);
        Assert.IsNull(outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_TextLineAdded_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request("sample.txt", "first\nsecond", "first\nsecond\nthird");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(0, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_TextLineRemoved_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request("sample.txt", "first\nsecond", "first");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(0, outcome.AddedLines);
        Assert.AreEqual(1, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_TextMimeOnUnknownExtension_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request("sample.bin", "first", "first\nsecond", "text/plain");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(0, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_ParameterizedTextMime_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request(
            "sample.bin",
            "first",
            "first\nsecond",
            "text/plain; charset=utf-8");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(0, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_OnlyLineEncodingDiffers_ReturnsChangedWithoutZeroDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = Request(
            "sample.txt",
            "first\r\nsecond\r\n",
            "first\nsecond\n");

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
        Assert.IsNull(outcome.AddedLines);
        Assert.IsNull(outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_IncompatibleContentTypes_ReturnsUnsupportedFailure()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.txt", "before", "text/plain"),
            Content("after.bin", "after", "application/octet-stream"));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Unsupported, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_DifferentBinaryContent_ReturnsChangedWithoutDetails()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();

        var outcome = await engine.CompareAsync(
            Request("sample.bin", "before", "after"),
            CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
        Assert.IsNull(outcome.AddedLines);
        Assert.IsNull(outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_EqualStreamsWithDifferentReadChunks_ReturnsIdentical()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var bytes = Encoding.UTF8.GetBytes("same content split into different stream chunks");
        var request = new DocumentDiffRequest(
            new DocumentContentSource(
                "before.bin",
                "application/octet-stream",
                bytes.Length,
                _ => ValueTask.FromResult<Stream>(new ChunkedReadStream(bytes, 3))),
            new DocumentContentSource(
                "after.bin",
                "application/octet-stream",
                bytes.Length,
                _ => ValueTask.FromResult<Stream>(new ChunkedReadStream(bytes, 11))));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, outcome.Kind);
    }

    [TestMethod]
    public async Task CompareAsync_TextDiffExceedsDeterministicBudget_DegradesToChanged()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var before = string.Join('\n', Enumerable.Range(0, 2001).Select(index => $"before-{index}"));
        var after = string.Join('\n', Enumerable.Range(0, 2001).Select(index => $"after-{index}"));

        var outcome = await engine.CompareAsync(
            Request("sample.txt", before, after),
            CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
        Assert.IsNull(outcome.AddedLines);
        Assert.IsNull(outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_InvalidUtf8Text_ReturnsInvalidContentFailure()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.txt", [0xff], "text/plain"),
            Content("after.txt", [0xfe], "text/plain"));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.InvalidContent, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_SourceReadFails_ReturnsIoFailure()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        var failing = new DocumentContentSource(
            "failing.bin",
            "application/octet-stream",
            null,
            _ => ValueTask.FromException<Stream>(new IOException()));
        var request = new DocumentDiffRequest(failing, failing);

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Io, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_CancelledRequest_ReturnsCancelledFailure()
    {
        IDocumentDiffEngine engine = new DocumentDiffEngine();
        using var cancellation = new CancellationTokenSource();
        cancellation.Cancel();

        var outcome = await engine.CompareAsync(
            Request("sample.bin", "before", "after"),
            cancellation.Token);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Cancelled, outcome.Failure);
    }

    private static DocumentDiffRequest Request(
        string name,
        string before,
        string after,
        string? mimeType = null)
    {
        return new DocumentDiffRequest(
            Content(name, before, mimeType),
            Content(name, after, mimeType));
    }

    private static DocumentContentSource Content(
        string name,
        string content,
        string? mimeType)
    {
        return Content(name, Encoding.UTF8.GetBytes(content), mimeType);
    }

    private static DocumentContentSource Content(
        string name,
        byte[] bytes,
        string? mimeType)
    {
        return new DocumentContentSource(
            name,
            mimeType,
            bytes.Length,
            _ => ValueTask.FromResult<Stream>(new MemoryStream(bytes, writable: false)));
    }

    private sealed class ChunkedReadStream(byte[] bytes, int maxChunkSize)
        : MemoryStream(bytes, writable: false)
    {
        public override int Read(byte[] buffer, int offset, int count)
        {
            return base.Read(buffer, offset, Math.Min(count, maxChunkSize));
        }

        public override ValueTask<int> ReadAsync(
            Memory<byte> buffer,
            CancellationToken cancellationToken = default)
        {
            return base.ReadAsync(buffer[..Math.Min(buffer.Length, maxChunkSize)], cancellationToken);
        }
    }
}
