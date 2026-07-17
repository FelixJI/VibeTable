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

    public bool IsExperienceIncomplete => IsBootstrapped && !IsExperienceComplete;
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
    /// Resets an interrupted first-run attempt back to the fresh state by
    /// removing the bootstrap/schema markers and the local SQLite database,
    /// so <c>directus bootstrap</c> runs again with freshly entered admin
    /// credentials on the next start.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Safety gate: this is a no-op when <see cref="ExperienceMarker"/> is
    /// present. A completed first-run experience must never be reset.
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

        // Hard safety gate: never touch a completed experience.
        if (File.Exists(Path.Combine(directory, ExperienceMarker)))
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
