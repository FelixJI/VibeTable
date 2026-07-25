using System.IO;
using System.Text;
using System.Text.Json;
using VibeTable.Workspace.Domain;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Atomic JSON file reader/writer with format-version checking.
///
/// Writes use a temp-file + <see cref="File.Replace"/> pattern so a crash
/// during write never corrupts the existing file. Unknown format versions
/// open read-only and refuse writes.
/// </summary>
public sealed class AtomicJsonStore
{
    private readonly JsonSerializerOptions _options = new(JsonSerializerDefaults.Web);

    /// <summary>
    /// Read and deserialize a JSON file. Returns null if the file does not exist.
    /// </summary>
    public T? Read<T>(string path)
    {
        if (!File.Exists(path))
            return default;

        var json = File.ReadAllText(path, Encoding.UTF8);
        return JsonSerializer.Deserialize<T>(json, _options);
    }

    /// <summary>
    /// Atomically write a JSON file. The write is atomic with respect to crashes:
    /// the existing file is only replaced after the new content is fully written
    /// to a temp file on the same volume.
    /// </summary>
    public void Write<T>(string path, T value)
    {
        var dir = Path.GetDirectoryName(path);
        if (!string.IsNullOrEmpty(dir))
            Directory.CreateDirectory(dir);

        var json = JsonSerializer.Serialize(value, _options);
        var tempPath = path + "." + Guid.NewGuid().ToString("N") + ".tmp";

        try
        {
            File.WriteAllText(tempPath, json, new UTF8Encoding(false));

            if (File.Exists(path))
            {
                // Atomic replace on the same volume.
                File.Replace(tempPath, path, destinationBackupFileName: null);
            }
            else
            {
                File.Move(tempPath, path);
            }
        }
        finally
        {
            if (File.Exists(tempPath))
                File.Delete(tempPath);
        }
    }

    /// <summary>
    /// Read a manifest and verify its format version. If the format version is
    /// unknown (not the current one), the file is opened read-only and writes
    /// must be refused by the caller.
    /// </summary>
    public (T? Manifest, bool FormatKnown) ReadWithFormatCheck<T>(
        string path,
        int expectedFormatVersion,
        Func<T, int> getFormatVersion
    ) where T : class
    {
        var manifest = Read<T>(path);
        if (manifest is null)
            return (null, true);

        var actualVersion = getFormatVersion(manifest);
        return (manifest, actualVersion == expectedFormatVersion);
    }
}
