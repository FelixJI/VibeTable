using Clippit.Word;
using DocumentFormat.OpenXml;
using DocumentFormat.OpenXml.Packaging;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.Worker;

internal static class DocxNormalizer
{
    // This method runs only inside the owned worker. Cancellation cannot interrupt
    // Clippit; the supervising host must terminate and observe that process first.
    // The caller owns a new, isolated output stream, never a source revision file.
    public static async Task NormalizeAsync(DocumentContentSource input, Stream derived,
        CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(input);
        ArgumentNullException.ThrowIfNull(derived);
        cancellationToken.ThrowIfCancellationRequested();
        if (!derived.CanRead || !derived.CanWrite || !derived.CanSeek || derived.Length != 0)
            throw new ArgumentException("Normalization requires a new read/write seekable output.", nameof(derived));
        if (input.Length is > OpenXmlExtractionLimits.MaxNonSeekablePackageBytes)
            throw new DiffBudgetExceededException();
        Stream source = await input.OpenReadAsync(cancellationToken).ConfigureAwait(false);
        if (ReferenceEquals(source, derived))
            throw new ArgumentException("Input and derived output must be separate.", nameof(derived));
        await using (source.ConfigureAwait(false))
        {
            if (source.CanSeek)
                source.Position = 0;
            byte[] buffer = new byte[64 * 1024];
            long copied = 0;
            int count;
            while ((count = await source.ReadAsync(buffer, cancellationToken).ConfigureAwait(false)) != 0)
            {
                copied = checked(copied + count);
                if (copied > OpenXmlExtractionLimits.MaxNonSeekablePackageBytes)
                    throw new DiffBudgetExceededException();
                await derived.WriteAsync(buffer.AsMemory(0, count), cancellationToken).ConfigureAwait(false);
            }
        }
        await ValidateAsync(requireFinalContent: false).ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
        derived.Position = 0;
        using (WordprocessingDocument package = WordprocessingDocument.Open(derived, true))
        {
            if (package.DocumentType != WordprocessingDocumentType.Document)
                throw new NotSupportedException("Only DOCX documents can be normalized.");
            RevisionAccepter.AcceptRevisions(package);
        }
        cancellationToken.ThrowIfCancellationRequested();
        await derived.FlushAsync(cancellationToken).ConfigureAwait(false);
        await ValidateAsync(requireFinalContent: true).ConfigureAwait(false);
        derived.Position = 0;

        Task ValidateAsync(bool requireFinalContent) => DocxPackagePreflight.ValidateAsync(new DocumentContentSource(
            "derived.docx", null, derived.Length,
            _ => ValueTask.FromResult<Stream>(new NonOwningStream(derived))), cancellationToken,
            requireFinalContent ? RejectRemainingRevisions : null);
    }

    // Clippit does not accept revisions in every Word part (for example comments).
    // Check all XML during the existing bounded postflight, not only selected stories.
    private static void RejectRemainingRevisions(string namespaceUri, string localName)
    {
        if (namespaceUri == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" &&
            (localName is "ins" or "del" or "cellIns" or "cellDel" or "cellMerge" ||
             localName.EndsWith("Change", StringComparison.Ordinal) ||
             localName.StartsWith("moveFrom", StringComparison.Ordinal) ||
             localName.StartsWith("moveTo", StringComparison.Ordinal) ||
             localName.StartsWith("customXmlInsRange", StringComparison.Ordinal) ||
             localName.StartsWith("customXmlDelRange", StringComparison.Ordinal) ||
             localName.StartsWith("customXmlMoveFromRange", StringComparison.Ordinal) ||
             localName.StartsWith("customXmlMoveToRange", StringComparison.Ordinal)))
            throw new NotSupportedException("The document contains revisions that could not be normalized.");
    }
}
