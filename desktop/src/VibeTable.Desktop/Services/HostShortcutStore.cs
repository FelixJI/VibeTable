using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Desktop.Services;

internal sealed record HostShortcut(
    [property: JsonRequired] string ShortcutId,
    [property: JsonRequired] string Target,
    [property: JsonRequired] string Label,
    string? CommandId = null,
    string? Url = null);

internal static class HostCommandContract
{
    internal static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        PropertyNameCaseInsensitive = false,
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
        AllowDuplicateProperties = false,
    };

    internal static void Validate(HostShortcut entry)
    {
        if (!Guid.TryParseExact(entry.ShortcutId, "D", out var id) || id == Guid.Empty
            || string.IsNullOrWhiteSpace(entry.Label) || entry.Label.Length > 128)
            throw new JsonException("Invalid shortcut identity or label.");
        if (entry.Target == "built-in-command" && entry.CommandId == "export.query" && entry.Url is null) return;
        if (entry.Target == "url" && entry.CommandId is null && entry.Url is not null)
        {
            HttpsUri(entry.Url);
            return;
        }
        throw new JsonException("Unsupported shortcut target.");
    }

    internal static Uri HttpsUri(string value)
    {
        if (value.Length > 2048 || value.Any(char.IsControl) || value.Contains('\\')
            || !Uri.TryCreate(value, UriKind.Absolute, out var uri)
            || uri.Scheme != Uri.UriSchemeHttps || string.IsNullOrEmpty(uri.Host)
            || !string.IsNullOrEmpty(uri.UserInfo))
            throw new JsonException("Only absolute HTTPS URLs without credentials are supported.");
        return uri;
    }

    internal static JsonElement ExportParameters(JsonElement value)
    {
        var parameters = value.Deserialize<ExportParametersDto>(Json)
            ?? throw new JsonException("Export parameters are required.");
        if (string.IsNullOrWhiteSpace(parameters.Collection) || parameters.Collection.Length > 128
            || parameters.Query.ValueKind != JsonValueKind.Object || parameters.Format is not ("csv" or "xlsx"))
            throw new JsonException("Invalid export parameters.");
        return JsonSerializer.SerializeToElement(parameters, Json);
    }

    private sealed record ExportParametersDto(
        [property: JsonRequired] string Collection,
        [property: JsonRequired] JsonElement Query,
        [property: JsonRequired] string Format);
}

/// <summary>Device-owned definitions. Execution parameters and grants are never persisted.</summary>
internal sealed class HostShortcutStore(string directory)
{
    private readonly SemaphoreSlim _queue = new(1, 1);

    internal Task<IReadOnlyList<HostShortcut>> ListAsync(Action ensureCurrent, CancellationToken token)
        => AccessAsync(null, null, ensureCurrent, token);

    internal Task<IReadOnlyList<HostShortcut>> SaveAsync(HostShortcut entry, Action ensureCurrent, CancellationToken token)
    {
        HostCommandContract.Validate(entry);
        return AccessAsync(entry, null, ensureCurrent, token);
    }

    internal Task<IReadOnlyList<HostShortcut>> DeleteAsync(string id, Action ensureCurrent, CancellationToken token)
    {
        if (!Guid.TryParseExact(id, "D", out _)) throw new JsonException("Invalid shortcut identity.");
        return AccessAsync(null, id, ensureCurrent, token);
    }

    private async Task<IReadOnlyList<HostShortcut>> AccessAsync(HostShortcut? update, string? delete,
        Action ensureCurrent, CancellationToken token)
    {
        await _queue.WaitAsync(token);
        try
        {
            ensureCurrent();
            token.ThrowIfCancellationRequested();
            Directory.CreateDirectory(directory);
            using var exclusive = new FileStream(Path.Combine(directory, "shortcuts.lock"),
                FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None);
            string path = Path.Combine(directory, "shortcuts.json");
            var entries = File.Exists(path)
                ? JsonSerializer.Deserialize<List<HostShortcut>>(await File.ReadAllTextAsync(path, token), HostCommandContract.Json)
                    ?? throw new JsonException("Invalid shortcut document.")
                : [];
            if (entries.Count > 128 || entries.Any(entry => entry is null)
                || entries.Select(entry => entry.ShortcutId).Distinct(StringComparer.Ordinal).Count() != entries.Count)
                throw new JsonException("Invalid shortcut inventory.");
            foreach (var entry in entries) HostCommandContract.Validate(entry);
            if (update is null && delete is null) return entries;
            entries.RemoveAll(entry => entry.ShortcutId == (update?.ShortcutId ?? delete));
            if (update is not null) entries.Add(update);
            if (entries.Count > 128) throw new JsonException("Too many shortcuts.");
            string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                await File.WriteAllTextAsync(temporary, JsonSerializer.Serialize(entries, HostCommandContract.Json), token);
                ensureCurrent();
                token.ThrowIfCancellationRequested();
                File.Move(temporary, path, overwrite: true);
            }
            finally { File.Delete(temporary); }
            return entries;
        }
        finally { _queue.Release(); }
    }
}
