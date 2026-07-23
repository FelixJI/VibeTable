using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;

namespace VibeTable.Infrastructure.Directus;

/// <summary>
/// Stages the first-party Directus extensions declared by the repository
/// manifest into a local Directus runtime.
/// </summary>
internal static class DirectusExtensionDeployer
{
    internal static void Deploy(string resourceRoot, string localDirectusDirectory)
    {
        string extensionsRoot = Path.Combine(resourceRoot, "directus", "extensions");
        string manifestPath = Path.Combine(extensionsRoot, "manifest.json");
        if (!File.Exists(manifestPath))
        {
            throw new InvalidOperationException(
                $"Required Directus extension manifest was not found: {manifestPath}");
        }

        using JsonDocument manifest = JsonDocument.Parse(File.ReadAllText(manifestPath));
        if (!manifest.RootElement.TryGetProperty("extensions", out JsonElement extensions)
            || extensions.ValueKind != JsonValueKind.Array)
        {
            throw new InvalidOperationException(
                $"Directus extension manifest has no extensions array: {manifestPath}");
        }

        var deployedNames = new HashSet<string>(StringComparer.Ordinal);
        var plans = new List<DeploymentPlan>();
        foreach (JsonElement extension in extensions.EnumerateArray())
        {
            string name = ReadRequiredString(extension, "name", manifestPath);
            string manifestType = ReadRequiredString(extension, "type", manifestPath);
            string manifestEntry = ReadRequiredString(extension, "entry", manifestPath);
            ValidateExtensionName(name);
            if (!deployedNames.Add(name))
            {
                throw new InvalidOperationException(
                    $"Directus extension manifest contains duplicate extension '{name}'.");
            }

            plans.Add(ValidateOne(
                extensionsRoot,
                name,
                manifestType,
                manifestEntry));
        }

        // Validate the entire release layout before touching the local runtime.
        // A missing artifact in a later manifest entry must not partially refresh
        // extensions listed before it.
        foreach (DeploymentPlan plan in plans)
        {
            DeployOne(plan, localDirectusDirectory);
        }
    }

    private static DeploymentPlan ValidateOne(
        string extensionsRoot,
        string name,
        string manifestType,
        string manifestEntry)
    {
        string extensionRoot = ResolveChildPath(extensionsRoot, name, $"extension name '{name}'");
        string packagePath = Path.Combine(extensionRoot, "package.json");
        if (!File.Exists(packagePath))
        {
            throw new InvalidOperationException(
                $"Required Directus extension package was not found for '{name}': {packagePath}");
        }

        using JsonDocument package = JsonDocument.Parse(File.ReadAllText(packagePath));
        if (!package.RootElement.TryGetProperty("directus:extension", out JsonElement metadata)
            || metadata.ValueKind != JsonValueKind.Object)
        {
            throw new InvalidOperationException(
                $"Directus extension package '{name}' has no directus:extension metadata.");
        }

        string packageType = ReadRequiredString(metadata, "type", packagePath);
        if (!string.Equals(manifestType, packageType, StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                $"Directus extension '{name}' type mismatch: manifest declares "
                + $"'{manifestType}', package declares '{packageType}'.");
        }

        IReadOnlyList<string> packageEntries = ReadPackageEntries(metadata, packageType, name);
        bool manifestEntryFound = false;
        foreach (string packageEntry in packageEntries)
        {
            ValidatePackageEntry(packageEntry, name);
            if (string.Equals(manifestEntry, packageEntry, StringComparison.Ordinal))
            {
                manifestEntryFound = true;
            }

            string artifactPath = ResolveChildPath(
                extensionRoot,
                packageEntry,
                $"package entry '{packageEntry}' for extension '{name}'");
            if (!File.Exists(artifactPath))
            {
                throw new InvalidOperationException(
                    $"Required Directus extension artifact is missing for '{name}': {packageEntry}. "
                    + "Build first-party Directus extensions before starting VibeTable.");
            }
        }

        if (!manifestEntryFound)
        {
            throw new InvalidOperationException(
                $"Directus extension '{name}' entry mismatch: manifest declares "
                + $"'{manifestEntry}', but package.json does not expose that entry.");
        }

        return new DeploymentPlan(name, extensionRoot, packagePath, packageEntries);
    }

    private static void DeployOne(DeploymentPlan plan, string localDirectusDirectory)
    {
        string targetRoot = ResolveChildPath(
            Path.Combine(localDirectusDirectory, "extensions"),
            plan.Name,
            $"extension name '{plan.Name}'");
        Directory.CreateDirectory(targetRoot);
        File.Copy(plan.PackagePath, Path.Combine(targetRoot, "package.json"), overwrite: true);

        var copiedDirectories = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (string packageEntry in plan.PackageEntries)
        {
            string relativeDirectory = Path.GetDirectoryName(
                packageEntry.Replace('/', Path.DirectorySeparatorChar)) ?? string.Empty;
            if (!copiedDirectories.Add(relativeDirectory))
            {
                continue;
            }

            string sourceDirectory = ResolveChildPath(
                plan.ExtensionRoot,
                relativeDirectory,
                $"package entry directory for extension '{plan.Name}'");
            string targetDirectory = ResolveChildPath(
                targetRoot,
                relativeDirectory,
                $"package entry directory for extension '{plan.Name}'");
            if (Directory.Exists(targetDirectory))
            {
                Directory.Delete(targetDirectory, recursive: true);
            }
            CopyDirectory(sourceDirectory, targetDirectory);
        }
    }

    private sealed record DeploymentPlan(
        string Name,
        string ExtensionRoot,
        string PackagePath,
        IReadOnlyList<string> PackageEntries);

    private static IReadOnlyList<string> ReadPackageEntries(
        JsonElement metadata,
        string packageType,
        string name)
    {
        if (!metadata.TryGetProperty("path", out JsonElement path))
        {
            throw new InvalidOperationException(
                $"Directus extension package '{name}' has no directus:extension.path entry.");
        }

        if (path.ValueKind == JsonValueKind.String)
        {
            return new[] { path.GetString()! };
        }

        if (string.Equals(packageType, "bundle", StringComparison.Ordinal)
            && path.ValueKind == JsonValueKind.Object)
        {
            var entries = new List<string>();
            foreach (JsonProperty entry in path.EnumerateObject())
            {
                if (entry.Value.ValueKind != JsonValueKind.String
                    || string.IsNullOrWhiteSpace(entry.Value.GetString()))
                {
                    throw new InvalidOperationException(
                        $"Directus bundle '{name}' has an invalid '{entry.Name}' package entry.");
                }
                entries.Add(entry.Value.GetString()!);
            }
            if (entries.Count > 0)
            {
                return entries;
            }
        }

        throw new InvalidOperationException(
            $"Directus extension package '{name}' has an invalid directus:extension.path entry.");
    }

    private static string ReadRequiredString(JsonElement element, string property, string source)
    {
        if (!element.TryGetProperty(property, out JsonElement value)
            || value.ValueKind != JsonValueKind.String
            || string.IsNullOrWhiteSpace(value.GetString()))
        {
            throw new InvalidOperationException(
                $"Required string property '{property}' is missing or invalid in {source}.");
        }
        return value.GetString()!;
    }

    private static void ValidatePackageEntry(string packageEntry, string name)
    {
        string[] segments = packageEntry.Split(
            new[] { '/', '\\' },
            StringSplitOptions.None);
        if (segments.Length < 2
            || !string.Equals(segments[0], "dist", StringComparison.Ordinal)
            || Array.Exists(segments, segment =>
                string.IsNullOrWhiteSpace(segment)
                || string.Equals(segment, ".", StringComparison.Ordinal)
                || string.Equals(segment, "..", StringComparison.Ordinal)))
        {
            throw new InvalidOperationException(
                $"Unsafe Directus package entry '{packageEntry}' for extension '{name}'. "
                + "Built entries must be normalized paths below dist/.");
        }
    }

    private static void ValidateExtensionName(string name)
    {
        if (string.Equals(name, ".", StringComparison.Ordinal)
            || string.Equals(name, "..", StringComparison.Ordinal)
            || name.IndexOfAny(new[] { '/', '\\' }) >= 0
            || name.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0)
        {
            throw new InvalidOperationException(
                $"Unsafe Directus extension name '{name}'. Extension names must be one directory segment.");
        }
    }

    private static string ResolveChildPath(string root, string relativePath, string description)
    {
        if (string.IsNullOrWhiteSpace(relativePath) || Path.IsPathRooted(relativePath))
        {
            throw new InvalidOperationException($"Unsafe Directus {description}.");
        }

        string fullRoot = Path.GetFullPath(root);
        string candidate = Path.GetFullPath(Path.Combine(
            fullRoot,
            relativePath.Replace('/', Path.DirectorySeparatorChar)));
        string rootPrefix = fullRoot.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar)
            + Path.DirectorySeparatorChar;
        if (!candidate.StartsWith(rootPrefix, StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException($"Unsafe Directus {description}.");
        }
        return candidate;
    }

    private static void CopyDirectory(string source, string target)
    {
        Directory.CreateDirectory(target);
        foreach (string file in Directory.GetFiles(source))
        {
            File.Copy(file, Path.Combine(target, Path.GetFileName(file)), overwrite: true);
        }
        foreach (string directory in Directory.GetDirectories(source))
        {
            CopyDirectory(directory, Path.Combine(target, Path.GetFileName(directory)));
        }
    }
}
