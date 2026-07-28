using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Private loopback adapter for the workspace-v2 Sidecar endpoint. The
/// ephemeral Sidecar credential never leaves this type and every response is
/// checked against the exact request id, wire scope, and runtime identity.
/// </summary>
public sealed class WorkspaceV2HttpGateway : IDisposable
{
    private const int MaxResponseBytes = 4 * 1024 * 1024;
    private readonly Func<PocketBaseAdminContext?> _contextProvider;
    private readonly HttpClient _client;
    private bool _disposed;

    public WorkspaceV2HttpGateway(
        PocketBaseSupervisor supervisor,
        HttpMessageHandler? handler = null)
    {
        ArgumentNullException.ThrowIfNull(supervisor);
        _contextProvider = supervisor.GetAdminContext;
        _client = handler is null
            ? new HttpClient(new HttpClientHandler
            {
                AllowAutoRedirect = false,
                UseCookies = false,
            }, disposeHandler: true)
            : new HttpClient(handler, disposeHandler: false);
        _client.Timeout = TimeSpan.FromSeconds(30);
    }

    internal WorkspaceV2HttpGateway(
        Func<PocketBaseAdminContext?> contextProvider,
        HttpMessageHandler handler)
    {
        _contextProvider = contextProvider
            ?? throw new ArgumentNullException(nameof(contextProvider));
        _client = new HttpClient(
            handler ?? throw new ArgumentNullException(nameof(handler)),
            disposeHandler: false)
        {
            Timeout = TimeSpan.FromSeconds(30),
        };
    }

    public async Task<WorkspaceV2SidecarCapabilities> GetCapabilitiesAsync(
        CancellationToken cancellationToken)
    {
        using HttpRequestMessage request = CreateRequest(
            HttpMethod.Get,
            "api/vibetable/v2/capabilities");
        using HttpResponseMessage response = await _client.SendAsync(
            request,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        byte[] raw = await ReadBoundedAsync(response, cancellationToken)
            .ConfigureAwait(false);
        EnsureSuccess(response, raw);
        using JsonDocument document = JsonDocument.Parse(raw);
        JsonElement root = document.RootElement;
        RequireExactProperties(
            root,
            "contractVersion",
            "workspaceId",
            "sessionEpoch",
            "fenceEpoch",
            "claimId",
            "rpcMethods",
            "registrations");
        string contractVersion = RequiredString(root, "contractVersion");
        string workspaceId = RequiredString(root, "workspaceId");
        ulong sessionEpoch = RequiredUInt64(root, "sessionEpoch");
        ulong fenceEpoch = RequiredUInt64(root, "fenceEpoch");
        string claimId = RequiredString(root, "claimId");
        JsonElement methods = root.GetProperty("rpcMethods");
        if (methods.ValueKind != JsonValueKind.Array)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 capabilities are invalid.");
        string[] rpcMethods = methods.EnumerateArray()
            .Select(item => item.ValueKind == JsonValueKind.String
                ? item.GetString()
                    ?? throw new InvalidOperationException(
                        "Sidecar workspace-v2 capability is empty.")
                : throw new InvalidOperationException(
                    "Sidecar workspace-v2 capability is invalid."))
            .Distinct(StringComparer.Ordinal)
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (root.GetProperty("registrations").ValueKind != JsonValueKind.Array)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 registrations are invalid.");
        return new WorkspaceV2SidecarCapabilities(
            contractVersion,
            workspaceId,
            sessionEpoch,
            fenceEpoch,
            claimId,
            rpcMethods);
    }

    public async Task<WorkspaceV2ForwardResult> ForwardAsync(
        string requestId,
        string method,
        JsonElement wire,
        JsonElement parameters,
        WorkspaceSidecarPathGrant? pathGrant,
        CancellationToken cancellationToken)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(requestId);
        ArgumentException.ThrowIfNullOrWhiteSpace(method);
        ObjectDisposedException.ThrowIf(_disposed, this);
        byte[] requestBody;
        using (var stream = new MemoryStream())
        {
            using (var writer = new Utf8JsonWriter(stream))
            {
                writer.WriteStartObject();
                writer.WriteString("jsonrpc", "2.0");
                writer.WriteString("id", requestId);
                writer.WriteString("method", method);
                writer.WritePropertyName("wire");
                wire.WriteTo(writer);
                writer.WritePropertyName("params");
                if (parameters.ValueKind == JsonValueKind.Undefined)
                    writer.WriteStartObject();
                else
                    parameters.WriteTo(writer);
                if (parameters.ValueKind == JsonValueKind.Undefined)
                    writer.WriteEndObject();
                writer.WriteEndObject();
            }
            requestBody = stream.ToArray();
        }

        using HttpRequestMessage request = CreateRequest(
            HttpMethod.Post,
            "api/vibetable/v2/rpc");
        request.Content = new ByteArrayContent(requestBody);
        request.Content.Headers.ContentType =
            new MediaTypeHeaderValue("application/json");
        if (pathGrant is not null)
        {
            string grantJson = JsonSerializer.Serialize(
                new
                {
                    grantId = pathGrant.GrantId,
                    method = pathGrant.Method,
                    operationId = pathGrant.OperationId.ToString("D"),
                    purpose = pathGrant.Purpose,
                    path = pathGrant.Path,
                },
                new JsonSerializerOptions(JsonSerializerDefaults.Web));
            string encoded = Convert.ToBase64String(
                    Encoding.UTF8.GetBytes(grantJson))
                .TrimEnd('=')
                .Replace('+', '-')
                .Replace('/', '_');
            request.Headers.TryAddWithoutValidation(
                "X-VibeTable-Path-Grant",
                encoded);
        }
        using HttpResponseMessage response = await _client.SendAsync(
            request,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        byte[] raw = await ReadBoundedAsync(response, cancellationToken)
            .ConfigureAwait(false);
        if (response.StatusCode is HttpStatusCode.Unauthorized
            or HttpStatusCode.Forbidden)
        {
            throw new InvalidOperationException(
                "The private Sidecar session credential was rejected.");
        }
        using JsonDocument document = JsonDocument.Parse(raw);
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response is invalid.");
        string[] names = root.EnumerateObject()
            .Select(property => property.Name)
            .ToArray();
        bool hasResult = names.Contains("result", StringComparer.Ordinal);
        bool hasError = names.Contains("error", StringComparer.Ordinal);
        string[] expected = hasResult
            ? ["jsonrpc", "id", "wire", "result"]
            : ["jsonrpc", "id", "wire", "error"];
        if (hasResult == hasError)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response must contain exactly one outcome.");
        RequireExactProperties(root, expected);
        if (RequiredString(root, "jsonrpc") != "2.0"
            || RequiredString(root, "id") != requestId)
        {
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response identity does not match.");
        }
        JsonElement responseWire = root.GetProperty("wire");
        if (!JsonElement.DeepEquals(responseWire, wire))
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response wire does not match.");
        if (hasResult)
        {
            return new WorkspaceV2ForwardResult(
                responseWire.Clone(),
                root.GetProperty("result").Clone(),
                null);
        }
        JsonElement error = root.GetProperty("error");
        if (error.ValueKind != JsonValueKind.Object)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 error is invalid.");
        string code = RequiredString(error, "code");
        string message = RequiredString(error, "message");
        bool retryable = error.TryGetProperty(
            "retryable",
            out JsonElement retryableElement)
            && retryableElement.ValueKind is JsonValueKind.True
                or JsonValueKind.False
            ? retryableElement.GetBoolean()
            : throw new InvalidOperationException(
                "Sidecar workspace-v2 retry policy is invalid.");
        return new WorkspaceV2ForwardResult(
            responseWire.Clone(),
            null,
            new WorkspaceV2ForwardError(code, message, retryable));
    }

    /// <summary>
    /// Calls the host-only write-coordinator drain endpoint and returns the
    /// durable high-watermark. This endpoint is deliberately not part of the
    /// renderer-visible RPC catalog.
    /// </summary>
    public async Task<WorkspaceDrainHighWatermark> DrainAsync(
        TimeSpan deadline,
        CancellationToken cancellationToken)
    {
        if (deadline <= TimeSpan.Zero || deadline > TimeSpan.FromSeconds(60))
            throw new ArgumentOutOfRangeException(
                nameof(deadline),
                "Drain deadline must be between 1 ms and 60 seconds.");
        long deadlineMilliseconds = Math.Max(
            1,
            checked((long)Math.Ceiling(deadline.TotalMilliseconds)));
        byte[] body = JsonSerializer.SerializeToUtf8Bytes(
            new { deadlineMs = deadlineMilliseconds });
        using HttpRequestMessage request = CreateRequest(
            HttpMethod.Post,
            "api/vibetable/v2/workspace/drain");
        request.Content = new ByteArrayContent(body);
        request.Content.Headers.ContentType =
            new MediaTypeHeaderValue("application/json");
        using HttpResponseMessage response = await _client.SendAsync(
            request,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        byte[] raw = await ReadBoundedAsync(response, cancellationToken)
            .ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            string code = "workspace.drain_failed";
            try
            {
                using JsonDocument failure = JsonDocument.Parse(raw);
                if (failure.RootElement.ValueKind == JsonValueKind.Object
                    && failure.RootElement.TryGetProperty(
                        "code",
                        out JsonElement codeElement)
                    && codeElement.ValueKind == JsonValueKind.String
                    && !string.IsNullOrWhiteSpace(codeElement.GetString()))
                {
                    code = codeElement.GetString()!;
                }
            }
            catch (JsonException)
            {
                // Keep the fixed safe error code; never expose response text.
            }
            throw new WorkspaceDrainException(code);
        }
        using JsonDocument document = JsonDocument.Parse(raw);
        JsonElement root = document.RootElement;
        RequireExactProperties(
            root,
            "sourceEpoch",
            "sourceSequence",
            "chainHash");
        ulong sourceEpoch = RequiredUInt64(root, "sourceEpoch");
        ulong sourceSequence = RequiredNonNegativeUInt64(
            root,
            "sourceSequence");
        string chainHash = RequiredString(root, "chainHash");
        if (sourceEpoch == 0 || string.IsNullOrWhiteSpace(chainHash))
            throw new InvalidOperationException(
                "Sidecar drain high-watermark is invalid.");
        return new WorkspaceDrainHighWatermark(
            sourceEpoch,
            sourceSequence,
            chainHash);
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _client.Dispose();
    }

    private HttpRequestMessage CreateRequest(HttpMethod method, string relative)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        PocketBaseAdminContext context = _contextProvider()
            ?? throw new InvalidOperationException(
                "The workspace Sidecar is not ready.");
        var request = new HttpRequestMessage(
            method,
            new Uri(context.Origin, relative));
        request.Headers.TryAddWithoutValidation(
            context.SessionHeaderName,
            context.SessionSecret);
        return request;
    }

    private static async Task<byte[]> ReadBoundedAsync(
        HttpResponseMessage response,
        CancellationToken cancellationToken)
    {
        if (response.Content.Headers.ContentLength > MaxResponseBytes)
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response is too large.");
        await using Stream source = await response.Content.ReadAsStreamAsync(
            cancellationToken).ConfigureAwait(false);
        using var target = new MemoryStream();
        byte[] buffer = new byte[16 * 1024];
        while (true)
        {
            int read = await source.ReadAsync(
                buffer,
                cancellationToken).ConfigureAwait(false);
            if (read == 0)
                break;
            if (target.Length + read > MaxResponseBytes)
                throw new InvalidOperationException(
                    "Sidecar workspace-v2 response is too large.");
            target.Write(buffer, 0, read);
        }
        return target.ToArray();
    }

    private static void EnsureSuccess(
        HttpResponseMessage response,
        byte[] body)
    {
        if (response.IsSuccessStatusCode)
            return;
        string detail = Encoding.UTF8.GetString(body);
        throw new InvalidOperationException(
            $"Sidecar workspace-v2 request failed with HTTP " +
            $"{(int)response.StatusCode}: {detail}");
    }

    private static void RequireExactProperties(
        JsonElement element,
        params string[] expected)
    {
        if (element.ValueKind != JsonValueKind.Object)
            throw new InvalidOperationException("Expected a JSON object.");
        string[] actual = element.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        string[] wanted = expected.Order(StringComparer.Ordinal).ToArray();
        if (!actual.SequenceEqual(wanted, StringComparer.Ordinal))
            throw new InvalidOperationException(
                "Sidecar workspace-v2 response contains unknown or missing fields.");
    }

    private static string RequiredString(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString())
                ? value.GetString()!
                : throw new InvalidOperationException(
                    $"Sidecar workspace-v2 field '{name}' is invalid.");

    private static ulong RequiredUInt64(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetUInt64(out ulong parsed)
            && parsed > 0
                ? parsed
                : throw new InvalidOperationException(
                    $"Sidecar workspace-v2 field '{name}' is invalid.");

    private static ulong RequiredNonNegativeUInt64(
        JsonElement element,
        string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetUInt64(out ulong parsed)
                ? parsed
                : throw new InvalidOperationException(
                    $"Sidecar workspace-v2 field '{name}' is invalid.");
}

public sealed record WorkspaceV2SidecarCapabilities(
    string ContractVersion,
    string WorkspaceId,
    ulong SessionEpoch,
    ulong FenceEpoch,
    string ClaimId,
    IReadOnlyList<string> RpcMethods);

public sealed record WorkspaceV2ForwardResult(
    JsonElement Wire,
    JsonElement? Result,
    WorkspaceV2ForwardError? Error);

public sealed record WorkspaceV2ForwardError(
    string Code,
    string Message,
    bool Retryable);

public sealed record WorkspaceDrainHighWatermark(
    ulong SourceEpoch,
    ulong SourceSequence,
    string ChainHash);

public sealed class WorkspaceDrainException(string code)
    : InvalidOperationException("The workspace write coordinator did not drain.")
{
    public string Code { get; } = code;
}
