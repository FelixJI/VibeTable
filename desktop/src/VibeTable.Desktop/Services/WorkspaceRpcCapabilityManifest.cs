using System.IO;
using System.Reflection;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal enum WorkspaceRpcAudience
{
    RendererPublic,
    RendererInternal,
    HostOnly,
}

internal sealed record WorkspaceRpcCapability(
    string Method,
    string Scope,
    string CapabilityId,
    WorkspaceRpcAudience Audience);

/// <summary>
/// Owns the generated workspace-v2 RPC catalog and renderer admission policy.
/// Callers ask one question at this seam and do not duplicate method, scope,
/// capability, or audience knowledge.
/// </summary>
internal sealed class WorkspaceRpcCapabilityManifest
{
    private const string ResourceName =
        "VibeTable.Desktop.workspace-rpc-capability-manifest.json";

    private static readonly Lazy<WorkspaceRpcCapabilityManifest> DefaultManifest =
        new(LoadEmbedded);

    private readonly IReadOnlyDictionary<string, WorkspaceRpcCapability> _methods;

    private WorkspaceRpcCapabilityManifest(
        IEnumerable<WorkspaceRpcCapability> capabilities)
    {
        var methods = new Dictionary<string, WorkspaceRpcCapability>(
            StringComparer.Ordinal);
        foreach (WorkspaceRpcCapability capability in capabilities)
        {
            ArgumentException.ThrowIfNullOrWhiteSpace(capability.Method);
            ArgumentException.ThrowIfNullOrWhiteSpace(capability.CapabilityId);
            if (capability.Scope is not ("global" or "workspace"))
                throw new ArgumentException(
                    $"Workspace RPC scope '{capability.Scope}' is invalid.",
                    nameof(capabilities));
            if (!methods.TryAdd(capability.Method, capability))
                throw new ArgumentException(
                    $"Duplicate workspace RPC method '{capability.Method}'.",
                    nameof(capabilities));
        }
        _methods = methods;
    }

    internal static WorkspaceRpcCapabilityManifest Default => DefaultManifest.Value;

    internal static WorkspaceRpcCapabilityManifest CreateForTests(
        params WorkspaceRpcCapability[] capabilities)
        => new(capabilities);

    internal bool TryGet(
        string method,
        out WorkspaceRpcCapability capability)
        => _methods.TryGetValue(method, out capability!);

    internal static WorkspaceRpcCapabilityManifest Parse(string json)
    {
        ArgumentNullException.ThrowIfNull(json);
        using JsonDocument document = JsonDocument.Parse(json);
        IReadOnlyDictionary<string, JsonElement> root = ReadClosedObject(
            document.RootElement,
            "workspace RPC capability manifest",
            "contractVersion",
            "methods");
        if (root["contractVersion"].ValueKind != JsonValueKind.String ||
            root["contractVersion"].GetString() != "2.0")
            throw new JsonException(
                "Workspace RPC capability manifest contractVersion is invalid.");
        if (root["methods"].ValueKind != JsonValueKind.Array)
            throw new JsonException(
                "Workspace RPC capability manifest methods must be an array.");

        var capabilities = new List<WorkspaceRpcCapability>();
        var methods = new HashSet<string>(StringComparer.Ordinal);
        foreach (JsonElement item in root["methods"].EnumerateArray())
        {
            IReadOnlyDictionary<string, JsonElement> entry = ReadClosedObject(
                item,
                "workspace RPC capability entry",
                "method",
                "scope",
                "capabilityId",
                "audience");
            string method = ReadRequiredString(entry["method"], "method");
            string scope = ReadRequiredString(entry["scope"], "scope");
            if (scope is not ("global" or "workspace"))
                throw new JsonException(
                    $"Workspace RPC scope '{scope}' is invalid.");
            if (!methods.Add(method))
                throw new JsonException(
                    $"Workspace RPC method '{method}' is duplicated.");
            string capabilityId = ReadRequiredString(
                entry["capabilityId"],
                "capabilityId");
            string audienceValue = ReadRequiredString(entry["audience"], "audience");
            WorkspaceRpcAudience audience = audienceValue switch
            {
                "rendererPublic" => WorkspaceRpcAudience.RendererPublic,
                "rendererInternal" => WorkspaceRpcAudience.RendererInternal,
                "hostOnly" => WorkspaceRpcAudience.HostOnly,
                _ => throw new JsonException(
                    $"Workspace RPC audience '{audienceValue}' is invalid."),
            };
            capabilities.Add(new WorkspaceRpcCapability(
                method,
                scope,
                capabilityId,
                audience));
        }
        return new WorkspaceRpcCapabilityManifest(capabilities);
    }

    private static WorkspaceRpcCapabilityManifest LoadEmbedded()
    {
        Assembly assembly = typeof(WorkspaceRpcCapabilityManifest).Assembly;
        using Stream stream = assembly.GetManifestResourceStream(ResourceName)
            ?? throw new InvalidOperationException(
                $"Embedded workspace RPC capability manifest '{ResourceName}' is missing.");
        using var reader = new StreamReader(stream);
        return Parse(reader.ReadToEnd());
    }

    private static IReadOnlyDictionary<string, JsonElement> ReadClosedObject(
        JsonElement element,
        string context,
        params string[] fields)
    {
        if (element.ValueKind != JsonValueKind.Object)
            throw new JsonException($"{context} must be an object.");
        var expected = fields.ToHashSet(StringComparer.Ordinal);
        var result = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        foreach (JsonProperty property in element.EnumerateObject())
        {
            if (!expected.Contains(property.Name))
                throw new JsonException(
                    $"{context} field '{property.Name}' is unknown.");
            if (!result.TryAdd(property.Name, property.Value))
                throw new JsonException(
                    $"{context} field '{property.Name}' is duplicated.");
        }
        if (result.Count != expected.Count)
        {
            string missing = string.Join(
                ", ",
                expected.Where(field => !result.ContainsKey(field)).Order());
            throw new JsonException($"{context} is missing fields: {missing}.");
        }
        return result;
    }

    private static string ReadRequiredString(JsonElement element, string field)
    {
        if (element.ValueKind != JsonValueKind.String ||
            string.IsNullOrWhiteSpace(element.GetString()))
            throw new JsonException(
                $"Workspace RPC capability field '{field}' is invalid.");
        return element.GetString()!;
    }
}
