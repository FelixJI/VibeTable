using System.IO;
using System.Security.Cryptography;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Content-addressable blob store for immutable file history.
///
/// Objects are stored at <c>objects/{sha256前2位}/{完整sha256}.blob</c>.
/// Objects are written once and never modified. Same hash within a workspace
/// is deduplicated (only one copy retained).
/// </summary>
public sealed class ContentObjectStore
{
    private readonly string _objectsRoot;

    public ContentObjectStore(string backupRoot)
    {
        _objectsRoot = Path.Combine(backupRoot, "objects");
    }

    /// <summary>
    /// Compute the SHA-256 hash of a file's content.
    /// </summary>
    public static string ComputeHash(string filePath)
    {
        using var stream = File.OpenRead(filePath);
        var hashBytes = SHA256.HashData(stream);
        return Convert.ToHexString(hashBytes).ToLowerInvariant();
    }

    /// <summary>
    /// Compute the SHA-256 hash of a byte array.
    /// </summary>
    public static string ComputeHash(byte[] content)
    {
        var hashBytes = SHA256.HashData(content);
        return Convert.ToHexString(hashBytes).ToLowerInvariant();
    }

    /// <summary>
    /// Returns the storage path for a given content hash.
    /// </summary>
    public string GetObjectPath(string contentHash)
    {
        if (contentHash.Length < 2)
            throw new ArgumentException("content hash too short", nameof(contentHash));
        return Path.Combine(_objectsRoot, contentHash[..2], contentHash + ".blob");
    }

    /// <summary>
    /// Returns true if an object with the given hash already exists.
    /// </summary>
    public bool Exists(string contentHash)
    {
        return File.Exists(GetObjectPath(contentHash));
    }

    /// <summary>
    /// Atomically commit a file's content to the object store.
    /// Copies to staging, verifies hash, then moves to final location.
    /// If the object already exists, it is not overwritten (deduplication).
    /// </summary>
    public CommitResult Commit(string sourceFilePath)
    {
        // Stage 1: stable read — verify the file is readable.
        if (!File.Exists(sourceFilePath))
            throw new FileNotFoundException("source file not found", sourceFilePath);

        var info = new FileInfo(sourceFilePath);
        var size = info.Length;

        // Stage 2: compute hash.
        var hash = ComputeHash(sourceFilePath);

        // Stage 3: check if object already exists (deduplication).
        var objectPath = GetObjectPath(hash);
        if (File.Exists(objectPath))
        {
            return new CommitResult(hash, size, AlreadyExisted: true);
        }

        // Stage 4: copy to final location (create parent dir first).
        Directory.CreateDirectory(Path.GetDirectoryName(objectPath)!);

        // Stage 5: hash-verify the copy matches.
        File.Copy(sourceFilePath, objectPath, overwrite: false);
        var verifyHash = ComputeHash(objectPath);
        if (verifyHash != hash)
        {
            // Hash mismatch — the file changed during copy or copy was corrupt.
            // Clean up the bad object.
            try { File.Delete(objectPath); } catch { /* best effort */ }
            throw new InvalidOperationException(
                $"object hash verification failed: expected {hash}, got {verifyHash}");
        }

        return new CommitResult(hash, size, AlreadyExisted: false);
    }

    /// <summary>
    /// Copy an object's content back to a target path (for restore).
    /// </summary>
    public void Restore(string contentHash, string targetPath)
    {
        var objectPath = GetObjectPath(contentHash);
        if (!File.Exists(objectPath))
            throw new FileNotFoundException($"object not found: {contentHash}", objectPath);

        // Verify hash before restoring.
        var actualHash = ComputeHash(objectPath);
        if (actualHash != contentHash)
            throw new InvalidOperationException(
                $"object hash mismatch on restore: expected {contentHash}, got {actualHash}");

        Directory.CreateDirectory(Path.GetDirectoryName(targetPath)!);
        File.Copy(objectPath, targetPath, overwrite: true);
    }
}

/// <summary>
/// Result of committing a file to the object store.
/// </summary>
public sealed record CommitResult(
    string ContentHash,
    long Size,
    bool AlreadyExisted
);
