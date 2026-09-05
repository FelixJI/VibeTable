using System.Globalization;
using System.IO;
using System.Numerics;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;

namespace VibeTable.Desktop.Services;

/// <summary>Owns the legacy workspace-local JSON format, validation and atomic replacement.</summary>
internal sealed class DeviceSettingsStore(string dataDirectory)
{
    private readonly string _path = Path.Combine(dataDirectory, "state", "device-settings.json");

    public async Task<JsonElement> ReadAsync(CancellationToken cancellationToken)
    {
        try
        {
            using var document = JsonDocument.Parse(await File.ReadAllTextAsync(_path, cancellationToken));
            try
            {
                return Normalize(document.RootElement, snakeCase: false);
            }
            catch (JsonException) when (document.RootElement.ValueKind == JsonValueKind.Object
                && document.RootElement.TryGetProperty("schema_version", out _))
            {
                // Legacy recovery uses int(version), but never rewrites the file.
                return Normalize(JsonSerializer.SerializeToElement(new
                {
                    schemaVersion = JsonNode.Parse(Integer(
                        document.RootElement.GetProperty("schema_version"), truncateNumber: true)
                        .ToString(CultureInfo.InvariantCulture)),
                }), snakeCase: false);
            }
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException or JsonException)
        {
            return Normalize(JsonSerializer.SerializeToElement(new { }), snakeCase: false);
        }
    }

    public async Task<JsonElement> SaveAsync(
        JsonElement settings,
        Action ensureCurrent,
        CancellationToken cancellationToken)
    {
        JsonElement result = Normalize(settings, snakeCase: false);
        JsonElement stored = Normalize(result, snakeCase: true);
        ensureCurrent();
        cancellationToken.ThrowIfCancellationRequested();
        Directory.CreateDirectory(Path.GetDirectoryName(_path)!);
        string temporary = _path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            await File.WriteAllTextAsync(temporary, stored.GetRawText(), cancellationToken);
            ensureCurrent();
            cancellationToken.ThrowIfCancellationRequested();
            File.Move(temporary, _path, overwrite: true);
            return result;
        }
        finally
        {
            File.Delete(temporary);
        }
    }

    private static JsonElement Normalize(JsonElement settings, bool snakeCase)
    {
        Dictionary<string, JsonElement> fields = Fields(settings,
            ("schemaVersion", "schema_version"), ("theme", "theme"),
            ("windowPosition", "window_position"), ("recentCollections", "recent_collections"));
        BigInteger version = fields.TryGetValue("schemaVersion", out var schema) ? Integer(schema) : BigInteger.One;
        if (version < 1) throw new JsonException("schemaVersion must be positive.");
        Dictionary<string, JsonElement> theme = fields.TryGetValue("theme", out var themeValue)
            ? Fields(themeValue, ("mode", "mode"), ("accent", "accent"), ("background", "background"), ("foreground", "foreground"))
            : [];
        string mode = Text(theme, "mode", "system");
        if (mode is not ("system" or "light" or "dark")) throw new JsonException("Invalid theme mode.");
        var positions = new JsonObject();
        if (fields.TryGetValue("windowPosition", out var window))
        {
            if (window.ValueKind != JsonValueKind.Object) throw new JsonException("Invalid windowPosition.");
            foreach (JsonProperty item in window.EnumerateObject())
                positions[item.Name] = JsonNode.Parse(Integer(item.Value).ToString(CultureInfo.InvariantCulture));
        }
        var collections = new JsonArray();
        if (fields.TryGetValue("recentCollections", out var recent))
        {
            if (recent.ValueKind != JsonValueKind.Array || recent.GetArrayLength() > 32)
                throw new JsonException("Invalid recentCollections.");
            foreach (JsonElement item in recent.EnumerateArray())
            {
                if (item.ValueKind != JsonValueKind.String) throw new JsonException("Invalid collection name.");
                collections.Add(item.GetString());
            }
        }
        var normalized = new JsonObject
        {
            [snakeCase ? "schema_version" : "schemaVersion"] = JsonNode.Parse(version.ToString(CultureInfo.InvariantCulture)),
            ["theme"] = new JsonObject
            {
                ["mode"] = mode,
                ["accent"] = Text(theme, "accent", "#2563eb"),
                ["background"] = Text(theme, "background", "#ffffff"),
                ["foreground"] = Text(theme, "foreground", "#111827"),
            },
            [snakeCase ? "window_position" : "windowPosition"] = positions,
            [snakeCase ? "recent_collections" : "recentCollections"] = collections,
        };
        return JsonSerializer.SerializeToElement(normalized);
    }

    private static Dictionary<string, JsonElement> Fields(
        JsonElement value,
        params (string Camel, string Snake)[] allowed)
    {
        if (value.ValueKind != JsonValueKind.Object) throw new JsonException("Expected settings object.");
        var result = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        foreach (JsonProperty field in value.EnumerateObject())
        {
            string? name = allowed.FirstOrDefault(pair => field.Name == pair.Camel || field.Name == pair.Snake).Camel;
            if (name is null || !result.TryAdd(name, field.Value)) throw new JsonException("Unknown or duplicate settings field.");
        }
        return result;
    }

    private static string Text(Dictionary<string, JsonElement> fields, string name, string fallback)
    {
        if (!fields.TryGetValue(name, out var value)) return fallback;
        if (value.ValueKind != JsonValueKind.String) throw new JsonException("Expected theme string.");
        string text = value.GetString()!;
        if (name != "mode" && text.EnumerateRunes().Count() > 16) throw new JsonException("Theme token too long.");
        return text;
    }

    private static BigInteger Integer(JsonElement value, bool truncateNumber = false)
    {
        if (value.ValueKind == JsonValueKind.True) return BigInteger.One;
        if (value.ValueKind == JsonValueKind.False) return BigInteger.Zero;
        if (value.ValueKind is not (JsonValueKind.String or JsonValueKind.Number))
            throw new JsonException("Expected integer.");
        string text = value.ValueKind == JsonValueKind.String ? value.GetString()! : value.GetRawText();
        if (value.ValueKind == JsonValueKind.String)
        {
            text = text.Trim();
            if (!Regex.IsMatch(text, @"\A[+-]?[0-9](?:_?[0-9])*(?:\.0+)?\z", RegexOptions.NonBacktracking))
                throw new JsonException("Invalid integer string.");
            text = text.Replace("_", "", StringComparison.Ordinal);
        }
        NumberStyles style = value.ValueKind == JsonValueKind.String
            ? NumberStyles.Integer | NumberStyles.AllowDecimalPoint
            : NumberStyles.Integer;
        if (BigInteger.TryParse(text, style, CultureInfo.InvariantCulture, out var integer))
            return integer;
        if (value.ValueKind == JsonValueKind.Number && value.TryGetDouble(out double number)
            && double.IsFinite(number) && (truncateNumber || Math.Truncate(number) == number))
            return new BigInteger(number);
        throw new JsonException("Expected integral value.");
    }
}
