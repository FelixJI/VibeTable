using System.Diagnostics;
using System.IO;
using System.Security.Cryptography;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal enum DocumentDiffArtifactKind
{
    ComparisonDocument,
    ChangeIndex,
}

internal enum DocumentDiffOwnerLiveness
{
    Alive,
    Dead,
    Unknown,
}

internal sealed class DocumentDiffArtifactBroker : IDisposable
{
    private const int ManifestVersion = 1;
    private const string ManifestFileName = "manifest.json";
    private const string ManifestPartialFileName = "manifest.json.partial";
    private static readonly TimeSpan DefaultTimeToLive = TimeSpan.FromHours(24);
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web)
    {
        WriteIndented = true,
    };
    private static readonly string[] RoleDirectories = ["input", "normalized", "output", "index"];
    private static readonly string[] InitialKnownFiles =
    [
        ManifestFileName,
        ManifestPartialFileName,
        "input/historical.content.partial",
        "input/effective.content.partial",
        "input/historical.content",
        "input/effective.content",
    ];

    private readonly object _gate = new();
    private readonly string _root;
    private readonly TimeSpan _timeToLive;
    private readonly TimeProvider _timeProvider;
    private readonly Func<int, DateTimeOffset, DocumentDiffOwnerLiveness> _ownerLiveness;
    private readonly Dictionary<Guid, OperationState> _operations = [];
    private readonly Dictionary<Guid, ArtifactSession> _sessions = [];
    private readonly ITimer _cleanupTimer;
    private bool _disposed;

    public DocumentDiffArtifactBroker(
        string root,
        TimeSpan? timeToLive = null,
        TimeProvider? timeProvider = null,
        Func<int, DateTimeOffset, DocumentDiffOwnerLiveness>? ownerLiveness = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(root);
        _root = Path.GetFullPath(root);
        _timeToLive = timeToLive ?? DefaultTimeToLive;
        if (_timeToLive <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(timeToLive));
        _timeProvider = timeProvider ?? TimeProvider.System;
        _ownerLiveness = ownerLiveness ?? ProbeOwner;
        EnsureSafeDirectory(_root, create: true);
        CleanupExpired();
        TimeSpan cleanupInterval = _timeToLive < TimeSpan.FromMinutes(15)
            ? _timeToLive
            : TimeSpan.FromMinutes(15);
        _cleanupTimer = _timeProvider.CreateTimer(
            _ => TryScheduledCleanup(),
            null,
            cleanupInterval,
            cleanupInterval);
    }

    public DocumentDiffArtifactOperation CreateOperation(
        Guid operationId,
        Guid workspaceId,
        ulong sessionEpoch)
    {
        if (operationId == Guid.Empty)
            throw new ArgumentException("Operation ID must not be empty.", nameof(operationId));
        if (workspaceId == Guid.Empty)
            throw new ArgumentException("Workspace ID must not be empty.", nameof(workspaceId));
        if (sessionEpoch == 0)
            throw new ArgumentOutOfRangeException(nameof(sessionEpoch));

        lock (_gate)
        {
            ThrowIfDisposed();
            EnsureSafeDirectory(_root, create: true);
            string operationDirectory = DirectOperationDirectory(operationId);
            if (Directory.Exists(operationDirectory) || File.Exists(operationDirectory))
                throw new IOException("Document diff operation directory already exists.");
            Directory.CreateDirectory(operationDirectory);
            foreach (string role in RoleDirectories)
                Directory.CreateDirectory(Path.Combine(operationDirectory, role));
            EnsureSafeDirectory(operationDirectory, create: false);

            DateTimeOffset now = _timeProvider.GetUtcNow();
            using Process process = Process.GetCurrentProcess();
            var manifest = new ArtifactManifest
            {
                Version = ManifestVersion,
                OperationId = operationId,
                WorkspaceId = workspaceId,
                SessionEpoch = sessionEpoch,
                OwnerProcessId = Environment.ProcessId,
                OwnerProcessStartedAt = process.StartTime.ToUniversalTime(),
                CreatedAt = now,
                ExpiresAt = now + _timeToLive,
                State = "running",
                KnownFiles = [.. InitialKnownFiles],
                Artifacts = [],
            };
            var state = new OperationState(operationDirectory, manifest);
            try
            {
                WriteManifest(state);
                _operations.Add(operationId, state);
                return new DocumentDiffArtifactOperation(this, state);
            }
            catch
            {
                _ = TryCleanup(state, writeClosingManifest: false);
                throw;
            }
        }
    }

    public DocumentDiffArtifactReadLease OpenRead(
        Guid sessionId,
        Guid workspaceId,
        ulong sessionEpoch,
        DocumentDiffArtifactKind kind)
    {
        lock (_gate)
        {
            ThrowIfDisposed();
            if (!_sessions.TryGetValue(sessionId, out ArtifactSession? session) ||
                session.WorkspaceId != workspaceId ||
                session.SessionEpoch != sessionEpoch ||
                session.ExpiresAt <= _timeProvider.GetUtcNow() ||
                !session.Artifacts.TryGetValue(kind, out string? relativePath))
            {
                if (session is not null && session.ExpiresAt <= _timeProvider.GetUtcNow())
                    CloseSessionLocked(sessionId, session);
                throw new DocumentDiffArtifactUnavailableException();
            }
            if (!_operations.TryGetValue(session.OperationId, out OperationState? state) ||
                state.Manifest.State != "ready")
                throw new DocumentDiffArtifactUnavailableException();
            string path = ResolveKnownFile(state, relativePath);
            if (!File.Exists(path) || IsReparsePoint(path))
                throw new DocumentDiffArtifactUnavailableException();
            EnsureSafeDirectory(state.OperationDirectory, create: false);
            EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
            var stream = new FileStream(
                path,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                64 * 1024,
                FileOptions.SequentialScan);
            state.ActiveReaders += 1;
            return new DocumentDiffArtifactReadLease(this, state, stream);
        }
    }

    public void CloseSession(Guid sessionId)
    {
        lock (_gate)
        {
            if (_disposed || !_sessions.TryGetValue(sessionId, out ArtifactSession? session))
                return;
            CloseSessionLocked(sessionId, session);
        }
    }

    public void CleanupExpired()
    {
        lock (_gate)
        {
            ThrowIfDisposed();
            EnsureSafeDirectory(_root, create: true);
            foreach ((Guid sessionId, ArtifactSession session) in _sessions.ToArray())
            {
                if (session.ExpiresAt <= _timeProvider.GetUtcNow())
                    CloseSessionLocked(sessionId, session);
            }
            foreach (OperationState state in _operations.Values
                         .Where(value => value.Closing)
                         .ToArray())
                TryFinalizeCleanup(state);
            foreach (string operationDirectory in Directory.EnumerateDirectories(_root))
            {
                if (!TryParseDirectOperationDirectory(operationDirectory, out Guid operationId) ||
                    IsReparsePoint(operationDirectory) ||
                    _operations.ContainsKey(operationId))
                    continue;
                OperationState? state = ReadManifest(operationDirectory, operationId);
                if (state is null || state.Manifest.ExpiresAt > _timeProvider.GetUtcNow())
                    continue;
                DocumentDiffOwnerLiveness liveness;
                try
                {
                    liveness = _ownerLiveness(
                        state.Manifest.OwnerProcessId,
                        state.Manifest.OwnerProcessStartedAt);
                }
                catch
                {
                    liveness = DocumentDiffOwnerLiveness.Unknown;
                }
                if (liveness != DocumentDiffOwnerLiveness.Dead)
                    continue;
                _ = TryCleanup(state, writeClosingManifest: true);
            }
        }
    }

    public void Dispose()
    {
        _cleanupTimer.Dispose();
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            _sessions.Clear();
            foreach (OperationState state in _operations.Values.ToArray())
            {
                state.Closing = true;
                TryFinalizeCleanup(state);
            }
        }
    }

    internal string PrepareArtifact(
        OperationState state,
        DocumentDiffArtifactKind kind,
        string fileName)
    {
        ValidateArtifactFileName(fileName);
        lock (_gate)
        {
            RequireRunning(state);
            if (state.Artifacts.ContainsKey(kind))
                throw new InvalidOperationException("Artifact kind is already prepared.");
            string role = kind == DocumentDiffArtifactKind.ComparisonDocument
                ? "output"
                : "index";
            string relativePath = $"{role}/{fileName}";
            string path = ResolveRelativePath(state.OperationDirectory, relativePath);
            EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
            state.Manifest.KnownFiles.Add(relativePath);
            state.Artifacts.Add(kind, relativePath);
            WriteManifest(state);
            return path;
        }
    }

    internal async Task<DocumentDiffVerifiedInputLease> OpenVerifiedInputsAsync(
        OperationState state,
        string historicalContentHash,
        string effectiveContentHash,
        CancellationToken cancellationToken)
    {
        RequireContentHash(historicalContentHash);
        RequireContentHash(effectiveContentHash);
        FileStream? historical = null;
        FileStream? effective = null;
        long generation;
        lock (_gate)
        {
            RequireRunning(state);
            if (state.ActiveInputGeneration is not null)
                throw new InvalidOperationException("Verified inputs are already leased.");
            string historicalPath = ResolveKnownFile(state, "input/historical.content");
            string effectivePath = ResolveKnownFile(state, "input/effective.content");
            EnsureSafeDirectory(Path.GetDirectoryName(historicalPath)!, create: false);
            historical = OpenGuard(historicalPath);
            try
            {
                effective = OpenGuard(effectivePath);
            }
            catch
            {
                historical.Dispose();
                throw;
            }
            generation = checked(state.NextInputGeneration + 1);
            state.NextInputGeneration = generation;
            state.ActiveInputGeneration = generation;
            state.VerifiedInputGeneration = null;
            state.StableInputGeneration = null;
        }
        try
        {
            string actualHistorical = await ContentHashAsync(
                historical, cancellationToken).ConfigureAwait(false);
            string actualEffective = await ContentHashAsync(
                effective, cancellationToken).ConfigureAwait(false);
            if (!string.Equals(actualHistorical, historicalContentHash, StringComparison.Ordinal) ||
                !string.Equals(actualEffective, effectiveContentHash, StringComparison.Ordinal))
                throw new DocumentDiffArtifactStaleException();
            lock (_gate)
            {
                RequireRunning(state);
                if (state.ActiveInputGeneration != generation)
                    throw new InvalidOperationException("Verified input lease is no longer current.");
                state.VerifiedInputGeneration = generation;
            }
            return new DocumentDiffVerifiedInputLease(
                this, state, generation, historical, effective);
        }
        catch
        {
            historical.Dispose();
            effective.Dispose();
            ReleaseInputLease(state, generation);
            throw;
        }
    }

    internal void ConfirmSourceStable(OperationState state, long generation)
    {
        lock (_gate)
        {
            RequireRunning(state);
            if (state.ActiveInputGeneration != generation ||
                state.VerifiedInputGeneration != generation)
                throw new InvalidOperationException("Verified inputs must remain leased.");
            state.StableInputGeneration = generation;
        }
    }

    internal void Complete(OperationState state, Guid sessionId)
    {
        if (sessionId == Guid.Empty)
            throw new ArgumentException("Session ID must not be empty.", nameof(sessionId));
        lock (_gate)
        {
            RequireRunning(state);
            if (state.ActiveInputGeneration is not long generation ||
                state.VerifiedInputGeneration != generation ||
                state.StableInputGeneration != generation)
                throw new InvalidOperationException(
                    "Artifact publication requires verified inputs and post-comparison CAS.");
            if (state.Artifacts.Count == 0)
                throw new InvalidOperationException("At least one artifact is required.");
            if (_sessions.ContainsKey(sessionId))
                throw new InvalidOperationException("Artifact session already exists.");
            foreach (string relativePath in state.Artifacts.Values)
            {
                string path = ResolveKnownFile(state, relativePath);
                EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
                if (!File.Exists(path) || IsReparsePoint(path))
                    throw new IOException("Prepared artifact is missing or unsafe.");
            }
            state.Manifest.State = "ready";
            state.Manifest.ExpiresAt = _timeProvider.GetUtcNow() + _timeToLive;
            state.Manifest.Artifacts = state.Artifacts.ToDictionary(
                pair => KindName(pair.Key),
                pair => new ArtifactManifestEntry
                {
                    RelativePath = pair.Value,
                    DerivedArtifact = true,
                    ReadOnlyResult = true,
                    WorkspaceRevision = false,
                },
                StringComparer.Ordinal);
            WriteManifest(state);
            _sessions.Add(sessionId, new ArtifactSession(
                state.Manifest.OperationId,
                state.Manifest.WorkspaceId,
                state.Manifest.SessionEpoch,
                state.Manifest.ExpiresAt,
                new Dictionary<DocumentDiffArtifactKind, string>(state.Artifacts)));
            state.Completed = true;
        }
    }

    internal void Release(OperationState state)
    {
        lock (_gate)
        {
            if (state.Completed || !_operations.ContainsKey(state.Manifest.OperationId))
                return;
            state.Closing = true;
            TryFinalizeCleanup(state);
        }
    }

    internal void ReleaseInputLease(OperationState state, long generation)
    {
        lock (_gate)
        {
            if (state.ActiveInputGeneration != generation)
                return;
            state.ActiveInputGeneration = null;
            if (!state.Completed)
            {
                state.VerifiedInputGeneration = null;
                state.StableInputGeneration = null;
            }
            if (state.Closing)
                TryFinalizeCleanup(state);
        }
    }

    internal void ReleaseReader(OperationState state)
    {
        lock (_gate)
        {
            if (state.ActiveReaders > 0)
                state.ActiveReaders -= 1;
            if (state.Closing)
                TryFinalizeCleanup(state);
        }
    }

    internal string GetRoleDirectory(OperationState state, string role)
    {
        lock (_gate)
        {
            RequireRunning(state);
            string path = Path.Combine(state.OperationDirectory, role);
            EnsureSafeDirectory(path, create: false);
            return path;
        }
    }

    internal string GetKnownFile(OperationState state, string relativePath)
    {
        lock (_gate)
        {
            RequireRunning(state);
            string path = ResolveKnownFile(state, relativePath);
            EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
            return path;
        }
    }

    private void CloseSessionLocked(Guid sessionId, ArtifactSession session)
    {
        _sessions.Remove(sessionId);
        if (_operations.TryGetValue(session.OperationId, out OperationState? state))
        {
            state.Closing = true;
            TryFinalizeCleanup(state);
        }
    }

    private void TryFinalizeCleanup(OperationState state)
    {
        if (state.ActiveInputGeneration is not null || state.ActiveReaders != 0)
        {
            state.Manifest.State = "closing";
            try
            {
                WriteManifest(state);
            }
            catch (Exception exception) when (
                exception is IOException or UnauthorizedAccessException or JsonException)
            {
            }
            return;
        }
        if (TryCleanup(state, writeClosingManifest: true))
            _operations.Remove(state.Manifest.OperationId);
    }

    private void TryScheduledCleanup()
    {
        try
        {
            CleanupExpired();
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException or ObjectDisposedException)
        {
        }
    }

    private void RequireRunning(OperationState state)
    {
        ThrowIfDisposed();
        if (!_operations.TryGetValue(state.Manifest.OperationId, out OperationState? current) ||
            !ReferenceEquals(current, state) ||
            state.Manifest.State != "running" || state.Closing)
            throw new InvalidOperationException("Document diff operation is not running.");
        EnsureSafeDirectory(state.OperationDirectory, create: false);
    }

    private OperationState? ReadManifest(string operationDirectory, Guid operationId)
    {
        string manifestPath = Path.Combine(operationDirectory, ManifestFileName);
        try
        {
            if (!File.Exists(manifestPath) || IsReparsePoint(manifestPath))
                return null;
            using JsonDocument document = JsonDocument.Parse(File.ReadAllText(manifestPath));
            string[] expected =
            [
                "version", "operationId", "workspaceId", "sessionEpoch",
                "ownerProcessId", "ownerProcessStartedAt", "createdAt", "expiresAt",
                "state", "knownFiles", "artifacts",
            ];
            string[] actual = document.RootElement.EnumerateObject()
                .Select(property => property.Name)
                .Order(StringComparer.Ordinal)
                .ToArray();
            if (!actual.SequenceEqual(expected.Order(StringComparer.Ordinal), StringComparer.Ordinal))
                return null;
            ArtifactManifest? manifest = document.RootElement.Deserialize<ArtifactManifest>(JsonOptions);
            if (manifest is null || manifest.Version != ManifestVersion ||
                manifest.OperationId != operationId || manifest.WorkspaceId == Guid.Empty ||
                manifest.SessionEpoch == 0 || manifest.OwnerProcessId <= 0 ||
                manifest.OwnerProcessStartedAt == default || manifest.CreatedAt == default ||
                manifest.CreatedAt > manifest.ExpiresAt ||
                manifest.State is not ("running" or "ready" or "closing") ||
                manifest.KnownFiles is null || manifest.Artifacts is null ||
                manifest.KnownFiles.Count != manifest.KnownFiles.Distinct(StringComparer.Ordinal).Count() ||
                !manifest.KnownFiles.Contains(ManifestFileName, StringComparer.Ordinal) ||
                !manifest.KnownFiles.Contains(ManifestPartialFileName, StringComparer.Ordinal))
                return null;
            foreach (string relativePath in manifest.KnownFiles)
                _ = ResolveRelativePath(operationDirectory, relativePath);
            foreach ((string kind, ArtifactManifestEntry artifact) in manifest.Artifacts)
            {
                if (kind is not ("comparisonDocument" or "changeIndex") ||
                    artifact is null ||
                    !artifact.DerivedArtifact || !artifact.ReadOnlyResult ||
                    artifact.WorkspaceRevision ||
                    !manifest.KnownFiles.Contains(artifact.RelativePath, StringComparer.Ordinal))
                    return null;
            }
            return new OperationState(operationDirectory, manifest);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException or JsonException or
            ArgumentException or InvalidOperationException)
        {
            return null;
        }
    }

    private bool TryCleanup(OperationState state, bool writeClosingManifest)
    {
        try
        {
            EnsureSafeDirectory(state.OperationDirectory, create: false);
            if (writeClosingManifest)
            {
                state.Manifest.State = "closing";
                WriteManifest(state);
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException or JsonException)
        {
            return !Directory.Exists(state.OperationDirectory);
        }

        foreach (string relativePath in state.Manifest.KnownFiles)
        {
            if (IsControlFile(relativePath))
                continue;
            try
            {
                string path = ResolveKnownFile(state, relativePath);
                if (File.Exists(path))
                {
                    EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
                    if (IsReparsePoint(path))
                        continue;
                    File.Delete(path);
                }
            }
            catch (Exception exception) when (
                exception is IOException or UnauthorizedAccessException or ArgumentException)
            {
                // Continue removing other explicitly registered sensitive files.
            }
        }
        if (state.Manifest.KnownFiles.Contains(ManifestPartialFileName, StringComparer.Ordinal))
            DeleteNormalFileIfPresent(Path.Combine(
                state.OperationDirectory,
                ManifestPartialFileName));
        foreach (string role in RoleDirectories)
            DeleteEmptyDirectory(Path.Combine(state.OperationDirectory, role));
        try
        {
            string[] remaining = Directory.GetFileSystemEntries(state.OperationDirectory);
            if (remaining.Length == 0)
            {
                Directory.Delete(state.OperationDirectory, recursive: false);
            }
            else if (remaining.Length == 1 && string.Equals(
                         Path.GetFileName(remaining[0]), ManifestFileName,
                         StringComparison.Ordinal) &&
                     state.Manifest.KnownFiles.Contains(ManifestFileName, StringComparer.Ordinal) &&
                     !IsReparsePoint(remaining[0]))
            {
                File.Delete(remaining[0]);
                Directory.Delete(state.OperationDirectory, recursive: false);
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // Unknown or busy entries are preserved; never recurse.
        }
        return !Directory.Exists(state.OperationDirectory);
    }

    private void WriteManifest(OperationState state)
    {
        EnsureSafeDirectory(state.OperationDirectory, create: false);
        string partialPath = Path.Combine(state.OperationDirectory, ManifestPartialFileName);
        string manifestPath = Path.Combine(state.OperationDirectory, ManifestFileName);
        if (File.Exists(partialPath))
        {
            if (IsReparsePoint(partialPath))
                throw new IOException("Manifest partial cannot be a reparse point.");
            File.Delete(partialPath);
        }
        byte[] content = JsonSerializer.SerializeToUtf8Bytes(state.Manifest, JsonOptions);
        using (var stream = new FileStream(
                   partialPath,
                   FileMode.CreateNew,
                   FileAccess.Write,
                   FileShare.None,
                   16 * 1024,
                   FileOptions.WriteThrough))
        {
            stream.Write(content);
            stream.Flush(flushToDisk: true);
        }
        File.Move(partialPath, manifestPath, overwrite: true);
    }

    private string DirectOperationDirectory(Guid operationId)
        => Path.Combine(_root, operationId.ToString("N"));

    private bool TryParseDirectOperationDirectory(string path, out Guid operationId)
    {
        operationId = Guid.Empty;
        string fullPath = Path.GetFullPath(path);
        return string.Equals(
                   Directory.GetParent(fullPath)?.FullName,
                   _root,
                   StringComparison.OrdinalIgnoreCase) &&
               Guid.TryParseExact(Path.GetFileName(fullPath), "N", out operationId) &&
               operationId != Guid.Empty;
    }

    private static string ResolveKnownFile(OperationState state, string relativePath)
    {
        if (!state.Manifest.KnownFiles.Contains(relativePath, StringComparer.Ordinal))
            throw new IOException("Artifact path is not registered.");
        return ResolveRelativePath(state.OperationDirectory, relativePath);
    }

    private static string ResolveRelativePath(string operationDirectory, string relativePath)
    {
        if (string.IsNullOrWhiteSpace(relativePath) ||
            relativePath.Contains('\\') ||
            relativePath.Contains(':') ||
            Path.IsPathRooted(relativePath))
            throw new ArgumentException("Artifact path must be canonical and relative.");
        if (IsControlFile(relativePath))
            return Path.Combine(operationDirectory, relativePath);
        string[] parts = relativePath.Split('/');
        if (parts.Length != 2 || !RoleDirectories.Contains(parts[0], StringComparer.Ordinal))
            throw new ArgumentException("Artifact path must name one role directory and file.");
        ValidateArtifactFileName(parts[1]);
        string fullPath = Path.GetFullPath(Path.Combine(operationDirectory, parts[0], parts[1]));
        string prefix = operationDirectory.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
        if (!fullPath.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
            throw new ArgumentException("Artifact path escaped its operation directory.");
        return fullPath;
    }

    private static void ValidateArtifactFileName(string fileName)
    {
        if (string.IsNullOrWhiteSpace(fileName) || fileName is "." or ".." ||
            !string.Equals(Path.GetFileName(fileName), fileName, StringComparison.Ordinal) ||
            fileName.Contains(':') ||
            fileName.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0)
            throw new ArgumentException("Artifact file name is invalid.", nameof(fileName));
    }

    private static bool IsControlFile(string relativePath)
        => relativePath is ManifestFileName or ManifestPartialFileName;

    private static void EnsureSafeDirectory(string path, bool create)
    {
        string fullPath = Path.GetFullPath(path);
        if (create)
            Directory.CreateDirectory(fullPath);
        for (DirectoryInfo? current = new(fullPath); current is not null; current = current.Parent)
        {
            current.Refresh();
            if (string.Equals(current.FullName, fullPath, StringComparison.OrdinalIgnoreCase) &&
                !current.Exists)
                throw new DirectoryNotFoundException(fullPath);
            if (current.Exists && current.Attributes.HasFlag(FileAttributes.ReparsePoint))
                throw new IOException("Document diff paths cannot contain reparse points.");
        }
    }

    private static bool IsReparsePoint(string path)
        => File.GetAttributes(path).HasFlag(FileAttributes.ReparsePoint);

    private static void DeleteNormalFileIfPresent(string path)
    {
        try
        {
            if (File.Exists(path) && !IsReparsePoint(path))
                File.Delete(path);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
        }
    }

    private static void DeleteEmptyDirectory(string path)
    {
        try
        {
            if (Directory.Exists(path) && !IsReparsePoint(path))
                Directory.Delete(path, recursive: false);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
        }
    }

    private static FileStream OpenGuard(string path)
    {
        if (!File.Exists(path) || IsReparsePoint(path))
            throw new IOException("Materialized diff input is missing or unsafe.");
        EnsureSafeDirectory(Path.GetDirectoryName(path)!, create: false);
        return new FileStream(
            path,
            FileMode.Open,
            FileAccess.Read,
            FileShare.Read,
            64 * 1024,
            FileOptions.Asynchronous | FileOptions.SequentialScan);
    }

    private static async Task<string> ContentHashAsync(
        FileStream stream,
        CancellationToken cancellationToken)
    {
        byte[] hash = await SHA256.HashDataAsync(stream, cancellationToken).ConfigureAwait(false);
        stream.Position = 0;
        return $"sha256:{Convert.ToHexStringLower(hash)}";
    }

    private static void RequireContentHash(string value)
    {
        if (value.Length != 71 || !value.StartsWith("sha256:", StringComparison.Ordinal) ||
            value.AsSpan(7).ContainsAnyExcept("0123456789abcdef"))
            throw new JsonException("Revision content hash is invalid.");
    }

    private static string KindName(DocumentDiffArtifactKind kind)
        => kind switch
        {
            DocumentDiffArtifactKind.ComparisonDocument => "comparisonDocument",
            DocumentDiffArtifactKind.ChangeIndex => "changeIndex",
            _ => throw new ArgumentOutOfRangeException(nameof(kind)),
        };

    private static DocumentDiffOwnerLiveness ProbeOwner(
        int processId,
        DateTimeOffset expectedStartedAt)
    {
        try
        {
            using Process process = Process.GetProcessById(processId);
            return process.StartTime.ToUniversalTime() == expectedStartedAt.UtcDateTime
                ? DocumentDiffOwnerLiveness.Alive
                : DocumentDiffOwnerLiveness.Dead;
        }
        catch (ArgumentException)
        {
            return DocumentDiffOwnerLiveness.Dead;
        }
        catch (Exception exception) when (
            exception is InvalidOperationException or System.ComponentModel.Win32Exception or
            NotSupportedException)
        {
            return DocumentDiffOwnerLiveness.Unknown;
        }
    }

    private void ThrowIfDisposed()
        => ObjectDisposedException.ThrowIf(_disposed, this);

    internal sealed class OperationState(string operationDirectory, ArtifactManifest manifest)
    {
        public string OperationDirectory { get; } = operationDirectory;
        public ArtifactManifest Manifest { get; } = manifest;
        public Dictionary<DocumentDiffArtifactKind, string> Artifacts { get; } = [];
        public bool Completed { get; set; }
        public long NextInputGeneration { get; set; }
        public long? ActiveInputGeneration { get; set; }
        public long? VerifiedInputGeneration { get; set; }
        public long? StableInputGeneration { get; set; }
        public int ActiveReaders { get; set; }
        public bool Closing { get; set; }
    }

    internal sealed class ArtifactManifest
    {
        public int Version { get; set; }
        public Guid OperationId { get; set; }
        public Guid WorkspaceId { get; set; }
        public ulong SessionEpoch { get; set; }
        public int OwnerProcessId { get; set; }
        public DateTimeOffset OwnerProcessStartedAt { get; set; }
        public DateTimeOffset CreatedAt { get; set; }
        public DateTimeOffset ExpiresAt { get; set; }
        public string State { get; set; } = string.Empty;
        public List<string> KnownFiles { get; set; } = [];
        public Dictionary<string, ArtifactManifestEntry> Artifacts { get; set; } = [];
    }

    internal sealed class ArtifactManifestEntry
    {
        public string RelativePath { get; set; } = string.Empty;
        public bool DerivedArtifact { get; set; }
        public bool ReadOnlyResult { get; set; }
        public bool WorkspaceRevision { get; set; }
    }

    private sealed record ArtifactSession(
        Guid OperationId,
        Guid WorkspaceId,
        ulong SessionEpoch,
        DateTimeOffset ExpiresAt,
        IReadOnlyDictionary<DocumentDiffArtifactKind, string> Artifacts);
}

internal sealed class DocumentDiffArtifactOperation : IDisposable
{
    private readonly DocumentDiffArtifactBroker _broker;
    private readonly DocumentDiffArtifactBroker.OperationState _state;
    private bool _disposed;

    internal DocumentDiffArtifactOperation(
        DocumentDiffArtifactBroker broker,
        DocumentDiffArtifactBroker.OperationState state)
    {
        _broker = broker;
        _state = state;
    }

    public string OperationDirectory => _state.OperationDirectory;
    public string InputDirectory => _broker.GetRoleDirectory(_state, "input");
    public string NormalizedDirectory => _broker.GetRoleDirectory(_state, "normalized");
    public string OutputDirectory => _broker.GetRoleDirectory(_state, "output");
    public string IndexDirectory => _broker.GetRoleDirectory(_state, "index");
    public string ManifestPath => Path.Combine(OperationDirectory, "manifest.json");
    public string HistoricalInputPath =>
        _broker.GetKnownFile(_state, "input/historical.content");
    public string EffectiveInputPath =>
        _broker.GetKnownFile(_state, "input/effective.content");

    public string PrepareArtifact(DocumentDiffArtifactKind kind, string fileName)
    {
        ThrowIfDisposed();
        return _broker.PrepareArtifact(_state, kind, fileName);
    }

    public Task<DocumentDiffVerifiedInputLease> OpenVerifiedInputsAsync(
        string historicalContentHash,
        string effectiveContentHash,
        CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        return _broker.OpenVerifiedInputsAsync(
            _state,
            historicalContentHash,
            effectiveContentHash,
            cancellationToken);
    }

    public void Complete(Guid sessionId)
    {
        ThrowIfDisposed();
        _broker.Complete(_state, sessionId);
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _broker.Release(_state);
    }

    private void ThrowIfDisposed()
        => ObjectDisposedException.ThrowIf(_disposed, this);
}

internal sealed class DocumentDiffVerifiedInputLease : IAsyncDisposable
{
    private readonly DocumentDiffArtifactBroker _broker;
    private readonly DocumentDiffArtifactBroker.OperationState _state;
    private readonly long _generation;
    private readonly FileStream _historical;
    private readonly FileStream _effective;
    private int _disposed;

    internal DocumentDiffVerifiedInputLease(
        DocumentDiffArtifactBroker broker,
        DocumentDiffArtifactBroker.OperationState state,
        long generation,
        FileStream historical,
        FileStream effective)
    {
        _broker = broker;
        _state = state;
        _generation = generation;
        _historical = historical;
        _effective = effective;
    }

    public void ConfirmSourceStable()
    {
        ObjectDisposedException.ThrowIf(Volatile.Read(ref _disposed) != 0, this);
        _broker.ConfirmSourceStable(_state, _generation);
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
            return;
        try
        {
            await _historical.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            try
            {
                await _effective.DisposeAsync().ConfigureAwait(false);
            }
            finally
            {
                _broker.ReleaseInputLease(_state, _generation);
            }
        }
    }
}

internal sealed class DocumentDiffArtifactReadLease : IDisposable, IAsyncDisposable
{
    private readonly DocumentDiffArtifactBroker _broker;
    private readonly DocumentDiffArtifactBroker.OperationState _state;
    private FileStream? _stream;

    internal DocumentDiffArtifactReadLease(
        DocumentDiffArtifactBroker broker,
        DocumentDiffArtifactBroker.OperationState state,
        FileStream stream)
    {
        _broker = broker;
        _state = state;
        _stream = stream;
    }

    public Stream Stream => _stream ?? throw new ObjectDisposedException(GetType().Name);

    public void Dispose()
    {
        FileStream? stream = Interlocked.Exchange(ref _stream, null);
        if (stream is null)
            return;
        try
        {
            stream.Dispose();
        }
        finally
        {
            _broker.ReleaseReader(_state);
        }
    }

    public async ValueTask DisposeAsync()
    {
        FileStream? stream = Interlocked.Exchange(ref _stream, null);
        if (stream is null)
            return;
        try
        {
            await stream.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            _broker.ReleaseReader(_state);
        }
    }
}

internal sealed class DocumentDiffArtifactStaleException : Exception;

internal sealed class DocumentDiffArtifactUnavailableException : Exception
{
    public string Failure => "sessionExpired";
}
