using System.IO;
using System.Reflection;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed record ProductRpcCapability(
    string Method,
    string Scope,
    string Audience,
    string CapabilityId,
    string Owner,
    string Effect);

internal sealed record ProductEventCapability(
    string Topic,
    string Scope,
    string Audience,
    string CapabilityId,
    string Owner,
    string Effect);

/// <summary>
/// Generated Product routing policy reader. It centralizes route admission
/// knowledge without replacing the typed product endpoint registries.
/// </summary>
internal sealed class ProductRpcCapabilityManifest
{
    private const string ResourceName =
        "VibeTable.Desktop.product-rpc-capability-manifest.json";
    private static readonly Lazy<ProductRpcCapabilityManifest> DefaultManifest =
        new(LoadEmbedded);
    private readonly IReadOnlyDictionary<string, ProductRpcCapability> _methods;
    private readonly IReadOnlyDictionary<string, ProductEventCapability> _events;
    private readonly IReadOnlyList<ProductSidecarRegistration>
        _productSidecarRegistrations;

    private ProductRpcCapabilityManifest(
        IEnumerable<ProductRpcCapability> methods,
        IEnumerable<ProductEventCapability> events)
    {
        ArgumentNullException.ThrowIfNull(methods);
        ArgumentNullException.ThrowIfNull(events);
        _methods = BuildMethodDictionary(methods);
        _events = BuildEventDictionary(events);
        _productSidecarRegistrations = Array.AsReadOnly(
            _methods.Values
                .Where(capability => capability.Owner == "goSidecar")
                .OrderBy(capability => capability.Method, StringComparer.Ordinal)
                .Select(capability => new ProductSidecarRegistration(
                    capability.Method,
                    capability.Scope))
                .ToArray());
    }

    internal static ProductRpcCapabilityManifest Default => DefaultManifest.Value;

    internal static ProductRpcCapabilityManifest CreateForTests(
        params ProductRpcCapability[] capabilities)
        => new(capabilities, Array.Empty<ProductEventCapability>());

    internal static ProductRpcCapabilityManifest CreateForTests(
        IEnumerable<ProductRpcCapability> methods,
        IEnumerable<ProductEventCapability> events)
        => new(methods, events);

    internal bool TryGet(string method, out ProductRpcCapability capability)
        => _methods.TryGetValue(method, out capability!);

    internal bool TryGetEvent(string topic, out ProductEventCapability capability)
        => _events.TryGetValue(topic, out capability!);

    internal IReadOnlyList<ProductSidecarRegistration>
        GetProductSidecarRegistrations() => _productSidecarRegistrations;

    internal static ProductRpcCapabilityManifest Parse(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        IReadOnlyDictionary<string, JsonElement> root = ReadClosedObject(
            document.RootElement, "Product capability manifest",
            "contractVersion", "rpcMethods", "eventTopics");
        if (ReadString(root["contractVersion"], "contractVersion") != "2.0")
            throw new JsonException("Product capability manifest contractVersion is invalid.");
        if (root["rpcMethods"].ValueKind != JsonValueKind.Array ||
            root["eventTopics"].ValueKind != JsonValueKind.Array)
            throw new JsonException("Product capability manifest arrays are invalid.");
        var methods = new List<ProductRpcCapability>();
        foreach (JsonElement item in root["rpcMethods"].EnumerateArray())
        {
            IReadOnlyDictionary<string, JsonElement> entry = ReadClosedObject(
                item, "Product RPC capability", "method", "scope", "audience",
                "capabilityId", "owner", "effect");
            string method = ReadString(entry["method"], "method");
            string scope = ReadString(entry["scope"], "scope");
            string audience = ReadString(entry["audience"], "audience");
            string owner = ReadString(entry["owner"], "owner");
            string effect = ReadString(entry["effect"], "effect");
            methods.Add(new ProductRpcCapability(method, scope, audience,
                ReadString(entry["capabilityId"], "capabilityId"), owner, effect));
        }
        var events = new List<ProductEventCapability>();
        foreach (JsonElement item in root["eventTopics"].EnumerateArray())
        {
            IReadOnlyDictionary<string, JsonElement> entry = ReadClosedObject(
                item, "Product event capability", "topic", "scope", "audience",
                "capabilityId", "owner", "effect");
            string topic = ReadString(entry["topic"], "topic");
            string scope = ReadString(entry["scope"], "scope");
            string audience = ReadString(entry["audience"], "audience");
            string owner = ReadString(entry["owner"], "owner");
            string effect = ReadString(entry["effect"], "effect");
            events.Add(new ProductEventCapability(topic, scope, audience,
                ReadString(entry["capabilityId"], "capabilityId"), owner, effect));
        }
        try
        {
            return new ProductRpcCapabilityManifest(methods, events);
        }
        catch (ArgumentException exception)
        {
            throw new JsonException("Product capability manifest values are invalid.", exception);
        }
    }

    private static ProductRpcCapabilityManifest LoadEmbedded()
    {
        Assembly assembly = typeof(ProductRpcCapabilityManifest).Assembly;
        using Stream stream = assembly.GetManifestResourceStream(ResourceName)
            ?? throw new InvalidOperationException($"Missing resource '{ResourceName}'.");
        using var reader = new StreamReader(stream);
        return Parse(reader.ReadToEnd());
    }

    private static IReadOnlyDictionary<string, ProductRpcCapability> BuildMethodDictionary(
        IEnumerable<ProductRpcCapability> capabilities)
    {
        var methods = new Dictionary<string, ProductRpcCapability>(StringComparer.Ordinal);
        foreach (ProductRpcCapability capability in capabilities)
        {
            ValidateMethodCapability(capability);
            if (!methods.TryAdd(capability.Method, capability))
                throw new ArgumentException($"Product RPC method '{capability.Method}' is duplicated.");
        }
        return methods;
    }

    private static IReadOnlyDictionary<string, ProductEventCapability> BuildEventDictionary(
        IEnumerable<ProductEventCapability> capabilities)
    {
        var events = new Dictionary<string, ProductEventCapability>(StringComparer.Ordinal);
        foreach (ProductEventCapability capability in capabilities)
        {
            ValidateEventCapability(capability);
            if (!events.TryAdd(capability.Topic, capability))
                throw new ArgumentException($"Product event topic '{capability.Topic}' is duplicated.");
        }
        return events;
    }

    private static void ValidateMethodCapability(ProductRpcCapability capability)
    {
        ArgumentNullException.ThrowIfNull(capability);
        ValidateCapability(
            capability.Method,
            capability.Scope,
            capability.Audience,
            capability.CapabilityId,
            capability.Owner,
            capability.Effect,
            isEvent: false);
    }

    private static void ValidateEventCapability(ProductEventCapability capability)
    {
        ArgumentNullException.ThrowIfNull(capability);
        ValidateCapability(
            capability.Topic,
            capability.Scope,
            capability.Audience,
            capability.CapabilityId,
            capability.Owner,
            capability.Effect,
            isEvent: true);
    }

    private static void ValidateCapability(
        string identifier,
        string scope,
        string audience,
        string capabilityId,
        string owner,
        string effect,
        bool isEvent)
    {
        if (string.IsNullOrWhiteSpace(identifier)
            || string.IsNullOrWhiteSpace(scope)
            || string.IsNullOrWhiteSpace(audience)
            || string.IsNullOrWhiteSpace(capabilityId)
            || string.IsNullOrWhiteSpace(owner)
            || string.IsNullOrWhiteSpace(effect))
            throw new ArgumentException("Product capability fields must be non-empty.");
        ValidateEnums(scope, audience, owner, effect, isEvent);
    }

    private static IReadOnlyDictionary<string, JsonElement> ReadClosedObject(
        JsonElement element, string context, params string[] fields)
    {
        if (element.ValueKind != JsonValueKind.Object)
            throw new JsonException($"{context} must be an object.");
        var expected = fields.ToHashSet(StringComparer.Ordinal);
        var result = new Dictionary<string, JsonElement>(StringComparer.Ordinal);
        foreach (JsonProperty property in element.EnumerateObject())
        {
            if (!expected.Contains(property.Name) || !result.TryAdd(property.Name, property.Value))
                throw new JsonException($"{context} has an unknown or duplicate field.");
        }
        if (result.Count != expected.Count)
            throw new JsonException($"{context} is missing fields.");
        return result;
    }

    private static string ReadString(JsonElement element, string field)
    {
        if (element.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(element.GetString()))
            throw new JsonException($"Product capability field '{field}' is invalid.");
        return element.GetString()!;
    }

    private static void ValidateEnums(
        string scope, string audience, string owner, string effect, bool isEvent)
    {
        if (scope is not ("global" or "workspace") ||
            audience is not ("rendererPublic" or "rendererInternal" or "hostOnly") ||
            owner is not ("pythonBff" or "goSidecar" or "wpfHost" or "pythonWorker") ||
            (isEvent ? effect != "notification" : effect is not ("read" or "write")))
            throw new ArgumentException("Product capability enum is invalid.");
    }
}
