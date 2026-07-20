using System.IO;

namespace VibeTable.PreviewHost;

internal sealed record PreviewHostArguments(string FilePath, Guid HandlerClsid)
{
    public static bool TryParse(
        IReadOnlyList<string> args,
        out PreviewHostArguments arguments)
    {
        arguments = null!;
        if (args.Count != 4) return false;

        string? filePath = null;
        Guid handlerClsid = Guid.Empty;
        for (int index = 0; index < args.Count; index += 2)
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
            arguments = new PreviewHostArguments(Path.GetFullPath(filePath), handlerClsid);
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }
}
