using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal enum UpdateRollbackOperation
{
    PrepareRecovery,
    ReadLedger,
    WriteLedger,
    ValidateMoveShape,
    ValidateMoveSource,
    ValidateMoveTree,
    MoveFile,
    MoveDirectory,
    ValidateRestoredPackage,
    FinalizeReceipt,
}

internal static class UpdateRecoveryFailureEvidence
{
    internal static void WriteOnce(string stagingRoot, string suffix, Exception exception,
        UpdateRollbackOperation? operation = null, string? entry = null)
    {
        try
        {
            string path = stagingRoot.TrimEnd(
                Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + suffix;
            _ = UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(path);
            using var stream = new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.None);
            if (operation is not null && Enum.IsDefined(operation.Value))
            {
                JsonSerializer.Serialize(stream, new
                {
                    exceptionType = exception.GetType().FullName,
                    hResult = exception.HResult,
                    operation = operation.Value.ToString(),
                    entry = UpdatePackageOwnedEntries.InInstallOrder.Contains(entry) ? entry : null,
                });
            }
            else
            {
                JsonSerializer.Serialize(stream, new
                {
                    exceptionType = exception.GetType().FullName,
                    hResult = exception.HResult,
                });
            }
        }
        catch (Exception)
        {
            // Diagnostics must never replace the recovery result.
        }
    }
}