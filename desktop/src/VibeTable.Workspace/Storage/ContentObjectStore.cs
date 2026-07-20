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
        if (!File.Exists(sourceFilePath))
            throw new FileNotFoundException("source file not found", sourceFilePath);

        var info = new FileInfo(sourceFilePath);
        long size = info.Length;
        string hash = ComputeHash(sourceFilePath);
        string objectPath = GetObjectPath(hash);
        if (File.Exists(objectPath))
        {
            ValidateCommittedObject(objectPath, hash, size);
            return new CommitResult(hash, size, AlreadyExisted: true);
        }

        string objectDirectory = Path.GetDirectoryName(objectPath)!;
        Directory.CreateDirectory(objectDirectory);
        string temporaryPath = Path.Combine(
            objectDirectory,
            $".{hash}.{Guid.NewGuid():N}.tmp");
        try
        {
            CopyAndFlush(sourceFilePath, temporaryPath);
            ValidateCommittedObject(temporaryPath, hash, size);

            // A concurrent writer may have committed the same object while we
            // copied. Never overwrite it; verify the winner before deduplicating.
            if (File.Exists(objectPath))
            {
                ValidateCommittedObject(objectPath, hash, size);
                return new CommitResult(hash, size, AlreadyExisted: true);
            }

            try
            {
                // The temp file is in the final object's directory, so this is
                // an atomic same-volume rename and never exposes partial bytes.
                File.Move(temporaryPath, objectPath, overwrite: false);
                return new CommitResult(hash, size, AlreadyExisted: false);
            }
            catch (IOException) when (File.Exists(objectPath))
            {
                // Another process won between the existence check and Move.
                ValidateCommittedObject(objectPath, hash, size);
                return new CommitResult(hash, size, AlreadyExisted: true);
            }
        }
        finally
        {
            TryDeleteTemporary(temporaryPath);
        }
    }

    private static void CopyAndFlush(string sourceFilePath, string temporaryPath)
    {
        using var source = new FileStream(
            sourceFilePath,
            FileMode.Open,
            FileAccess.Read,
            FileShare.Read,
            128 * 1024,
            FileOptions.SequentialScan);
        using var temporary = new FileStream(
            temporaryPath,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            128 * 1024,
            FileOptions.SequentialScan);
        source.CopyTo(temporary, 128 * 1024);
        temporary.Flush(flushToDisk: true);
    }

    private static void ValidateCommittedObject(
        string objectPath,
        string expectedHash,
        long expectedSize)
    {
        var info = new FileInfo(objectPath);
        if (!info.Exists || info.Length != expectedSize)
            throw new InvalidDataException("content object size verification failed");
        string actualHash = ComputeHash(objectPath);
        if (!string.Equals(actualHash, expectedHash, StringComparison.Ordinal))
            throw new InvalidDataException("content object hash verification failed");
    }

    private static void TryDeleteTemporary(string temporaryPath)
    {
        try
        {
            if (File.Exists(temporaryPath)) File.Delete(temporaryPath);
        }
        catch
        {
            // Best effort only. A random name prevents a failed cleanup from
            // colliding with a later commit, and recovery can remove stale temps.
        }
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
