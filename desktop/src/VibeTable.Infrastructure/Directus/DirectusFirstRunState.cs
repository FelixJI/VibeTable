using System;
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
    public bool IsInterrupted => IsBootstrapped && !IsExperienceComplete;
    public bool NeedsWelcome => IsFresh || IsInterrupted;
}

public static class DirectusFirstRunState
{
    public const string BootstrapMarker = ".bootstrapped";
    public const string SchemaMarker = ".schema-applied";
    public const string ExperienceMarker = ".vibetable-initialized";

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
}
