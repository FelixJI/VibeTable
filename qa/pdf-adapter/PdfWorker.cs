using System.Text;
using UglyToad.PdfPig;
using UglyToad.PdfPig.Exceptions;
using UglyToad.PdfPig.Logging;

namespace PdfAdapterQualification;

internal sealed record PdfResult(
    string Status, string Text, string? ErrorCode, int ParsedPages, int WarningCount);

internal static class PdfWorker
{
    internal const long InputLimit = 64L * 1024 * 1024;
    internal const int TextLimit = 2_000_000;

    public static PdfResult Extract(string source)
    {
        int pages = 0;
        var log = new QualificationLog();
        try
        {
            using var stream = new FileStream(source, FileMode.Open, FileAccess.Read, FileShare.Read);
            if (stream.Length > InputLimit)
                return Reject("resourceLimited", "extract.input_limit", pages, log);
            var options = new ParsingOptions
            {
                UseLenientParsing = false,
                Logger = log,
                FilterProvider = new QualificationFilterProvider(),
            };
            using var document = PdfDocument.Open(stream, options);
            if (document.IsEncrypted)
                return Reject("passwordProtected", "extract.password_required", pages, log);
            var text = new StringBuilder();
            int remaining = TextLimit;
            bool truncated = false;
            foreach (var page in document.GetPages())
            {
                pages++;
                foreach (var rune in page.Text.EnumerateRunes())
                {
                    if (remaining == 0)
                    {
                        truncated = true;
                        break;
                    }
                    text.Append(rune.ToString());
                    remaining--;
                }
                // Exhausting output does not end validation of later reachable pages.
            }
            if (log.WarningCount != 0)
                return Reject("unsupported", "extract.unsupported", pages, log);
            if (text.Length == 0)
                return Reject("noTextLayer", "extract.pdf_no_text", pages, log);
            return new PdfResult(
                truncated ? "truncated" : "indexed", text.ToString(),
                truncated ? "extract.text_limit" : null, pages, log.WarningCount);
        }
        catch (PdfDocumentEncryptedException)
        {
            return Reject("passwordProtected", "extract.password_required", pages, log);
        }
        catch (PdfBudgetException error)
        {
            return Reject("resourceLimited", error.Code, pages, log);
        }
        catch (PdfStreamException)
        {
            return Reject("failed", "extract.pdf_stream_invalid", pages, log);
        }
        catch (NotSupportedException)
        {
            return Reject("unsupported", "extract.unsupported", pages, log);
        }
        catch (OutOfMemoryException)
        {
            // The supervisor retains the actual Job-limit observation separately.
            return Reject("resourceLimited", "extract.memory_limit", pages, log);
        }
        catch (Exception)
        {
            return Reject("failed", "extract.pdf_invalid", pages, log);
        }
    }

    private static PdfResult Reject(string status, string code, int pages, QualificationLog log)
        => new(status, string.Empty, code, pages, log.WarningCount);
}

internal sealed class QualificationLog : ILog
{
    public int WarningCount { get; private set; }
    public void Debug(string message) { }
    public void Debug(string message, Exception exception) { }
    public void Warn(string message) => WarningCount++;
    public void Error(string message) => WarningCount++;
    public void Error(string message, Exception exception) => WarningCount++;
}

