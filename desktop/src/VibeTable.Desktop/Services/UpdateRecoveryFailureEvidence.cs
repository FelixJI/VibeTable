using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal static class UpdateRecoveryFailureEvidence
{
    internal static void WriteOnce(string stagingRoot, string suffix, Exception exception)
    {
        try
        {
            string path = stagingRoot.TrimEnd(
                Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + suffix;
            _ = UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(path);
            using var stream = new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.None);
            JsonSerializer.Serialize(stream, new
            {
                exceptionType = exception.GetType().FullName,
                hResult = exception.HResult,
            });
        }
        catch (Exception)
        {
            // Diagnostics must never replace the recovery result.
        }
    }
}