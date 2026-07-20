using System;
using System.Diagnostics;
using System.IO;
using System.Text;

namespace VibeTable.Infrastructure.Directus;

/// <summary>Persistent markers that distinguish engine bootstrap from the full desktop experience.</summary>
public sealed record DirectusFirstRunStatus(
    bool IsBootstrapped,
    bool IsSchemaApplied,
    bool IsExperienceComplete)
{
    public bool IsFresh => !IsBootstrapped;

    /// <summary>
    /// Whether the persistent Directus engine and the VibeTable schema have
    /// both completed their idempotent initialization steps.
    /// </summary>
    public bool IsRuntimeReady => IsBootstrapped && IsSchemaApplied;

    /// <summary>
    /// Whether native runtime progress is still useful. Authentication and
    /// renderer failures deliberately do not affect this value.
    /// </summary>
    public bool NeedsRuntimeInitialization => !IsRuntimeReady;

    /// <summary>
    /// Whether the runtime is usable but the optional desktop experience
    /// marker has not yet been persisted. This is never a reason to reset the
    /// database; login and renderer setup can be resumed independently.
    /// </summary>
    public bool IsExperienceIncomplete => IsRuntimeReady && !IsExperienceComplete;
}

public static class DirectusFirstRunState
{
    public const string BootstrapMarker = ".bootstrapped";
    public const string SchemaMarker = ".schema-applied";
    public const string ExperienceMarker = ".vibetable-initialized";

    private const string DefaultSqliteRelativePath = "data/directus.sqlite";

    public static DirectusFirstRunStatus Inspect(string directory)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(directory);
        return new DirectusFirstRunStatus(
            File.Exists(Path.Combine(directory, BootstrapMarker)),
            File.Exists(Path.Combine(directory, SchemaMarker)),
            File.Exists(Path.Combine(directory, ExperienceMarker)));
    }

    public static void MarkExperienceComplete(string directory)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(directory);
        File.WriteAllText(
            Path.Combine(directory, ExperienceMarker),
            $"completed={DateTimeOffset.UtcNow:O}{Environment.NewLine}",
            Encoding.UTF8);
    }

    /// <summary>
    /// Explicitly resets an interrupted runtime-bootstrap attempt back to the
    /// fresh state by removing the bootstrap/schema markers and the local
    /// SQLite database,
    /// so <c>directus bootstrap</c> runs again with freshly entered admin
    /// credentials on the next start.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Safety gate: this is a no-op once the bootstrap and schema markers are
    /// both present, regardless of <see cref="ExperienceMarker"/>. A login or
    /// renderer failure after runtime initialization must never make the
    /// database eligible for deletion. A completed experience is also never
    /// reset even if its runtime markers are unexpectedly missing.
    /// </para>
    /// <para>
    /// Preserved on disk:
    /// <list type="bullet">
    /// <item><c>.env</c> — keeps KEY/SECRET stable so JWT signing does not
    /// rotate; ADMIN_EMAIL/ADMIN_PASSWORD will be overwritten by the next
    /// first-run dialog because <see cref="BootstrapMarker"/> is gone.</item>
    /// <item><c>node_modules/</c> — package install is expensive and
    /// independent of the database; package verification still runs.</item>
    /// <item><c>uploads/</c> — user files are not the database.</item>
    /// </list>
    /// </para>
    /// <para>
    /// Removed:
    /// <c>.bootstrapped</c>, <c>.schema-applied</c>, and the SQLite database
    /// file(s) under <c>./data/</c> (the <c>directus.sqlite</c> file plus its
    /// <c>-wal</c>/<c>-shm</c> sidecars) along with the <c>./data/</c>
    /// directory itself.
    /// </para>
    /// <para>
    /// All filesystem errors are swallowed and traced: a cleanup failure must
    /// not block the new first-run attempt. If the database file survives
    /// (e.g. locked by another process), <c>directus bootstrap</c> tolerates
    /// it via its "already bootstrapped" output path.
    /// </para>
    /// </remarks>
    public static void ResetUncompletedBootstrap(string directory)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(directory);

        // Hard safety gate: runtime readiness is independent of login/UI
        // completion. Never turn a missing experience marker into permission
        // to remove an initialized database.
        var status = Inspect(directory);
        if (status.IsRuntimeReady || status.IsExperienceComplete)
        {
            return;
        }

        TryDeleteFile(Path.Combine(directory, BootstrapMarker));
        TryDeleteFile(Path.Combine(directory, SchemaMarker));

        foreach (string sidecar in new[] { "", "-wal", "-shm" })
        {
            TryDeleteFile(Path.Combine(directory, DefaultSqliteRelativePath + sidecar));
        }

        TryDeleteDirectory(Path.Combine(directory, "data"));
    }

    private static void TryDeleteFile(string path)
    {
        try
        {
            if (File.Exists(path))
            {
                File.Delete(path);
            }
        }
        catch (Exception ex)
        {
            Trace.WriteLine($"[directus] unable to delete {path} during reset: {ex.Message}");
        }
    }

    private static void TryDeleteDirectory(string path)
    {
        try
        {
            if (Directory.Exists(path))
            {
                Directory.Delete(path, recursive: true);
            }
        }
        catch (Exception ex)
        {
            Trace.WriteLine($"[directus] unable to delete directory {path} during reset: {ex.Message}");
        }
    }
}
