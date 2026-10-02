using System.IO;

namespace VibeTable.PreviewHost;

internal sealed record PreviewHostArguments(
    string FilePath, Guid HandlerClsid, string? TestEvidenceDirectory = null)
{
    public static bool TryParse(
        IReadOnlyList<string> args,
        out PreviewHostArguments arguments)
    {
        arguments = null!;
        if (args.Count is not (4 or 7)) return false;
        string? testEvidenceDirectory = null;
        if (args.Count == 7)
        {
            if (args[4] != "--test-mode" || args[5] != "--e2e-controls-dir" ||
                string.IsNullOrWhiteSpace(args[6]) || !Path.IsPathFullyQualified(args[6]))
                return false;
            testEvidenceDirectory = args[6];
        }

        string? filePath = null;
        Guid handlerClsid = Guid.Empty;
        for (int index = 0; index < 4; index += 2)
        {
            string name = args[index];
            string value = args[index + 1];
            if (string.Equals(name, "--file", StringComparison.Ordinal)
                && filePath is null)
            {
                filePath = value;
            }
            else if (string.Equals(name, "--handler", StringComparison.Ordinal)
                && handlerClsid == Guid.Empty
                && Guid.TryParse(value, out Guid parsedClsid)
                && parsedClsid != Guid.Empty)
            {
                handlerClsid = parsedClsid;
            }
            else
            {
                return false;
            }
        }

        if (string.IsNullOrWhiteSpace(filePath)
            || !Path.IsPathFullyQualified(filePath)
            || handlerClsid == Guid.Empty)
        {
            return false;
        }
        try
        {
            arguments = new PreviewHostArguments(Path.GetFullPath(filePath), handlerClsid,
                testEvidenceDirectory is null ? null : Path.GetFullPath(testEvidenceDirectory));
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }
}
