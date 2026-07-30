using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>
/// Renderer-safe error for native document actions. No local path or provider
/// detail crosses the WebView boundary.
/// </summary>
public sealed record DocumentOperationFailedPayload(
    [property: JsonPropertyName("message")] string Message,
    [property: JsonPropertyName("code")] string? Code = null);
