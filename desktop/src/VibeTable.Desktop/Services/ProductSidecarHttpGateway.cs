using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Text.RegularExpressions;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

public sealed class ProductSidecarHttpGateway : IDisposable, IProductSidecarRpcForwarder
{
    private const int MaxRequestBytes = 1024 * 1024, MaxResponseBytes = 4 * 1024 * 1024;
    private static readonly Regex PublicErrorCode = new(
        "^[a-z][a-z0-9_]*(\\.[a-z0-9_]+)+\\z", RegexOptions.CultureInvariant);
    private readonly object _stateGate = new();
    private readonly Uri _origin;
    private readonly string _sessionHeaderName;
    private readonly string _sessionSecret;
    private readonly ProductSidecarIdentity _expectedIdentity;
    private readonly ProductSidecarRegistration[] _expectedRegistrations;
    private readonly HttpClient _client;
    private readonly TimeSpan _timeout;
    private readonly CancellationTokenSource _lifetime = new();
    private long _handshakeAttempt;
    private long _readyAttempt = -1;
    private int _disposed;

    public ProductSidecarHttpGateway(
        PocketBaseAdminContext context,
        ProductSidecarIdentity expectedIdentity,
        IReadOnlyCollection<ProductSidecarRegistration> expectedRegistrations,
        HttpMessageHandler? handler = null,
        TimeSpan? timeout = null)
    {
        ArgumentNullException.ThrowIfNull(context);
        ArgumentNullException.ThrowIfNull(expectedIdentity);
        ArgumentNullException.ThrowIfNull(expectedRegistrations);
        if (context.Origin is null || !context.Origin.IsAbsoluteUri
            || context.Origin.Scheme != Uri.UriSchemeHttp
            || !context.Origin.IsLoopback)
            throw new ArgumentException(
                "The Product Sidecar origin must be private loopback HTTP.",
                nameof(context));
        ValidateIdentity(expectedIdentity, nameof(expectedIdentity));
        if (string.IsNullOrWhiteSpace(context.SessionHeaderName)
            || string.IsNullOrWhiteSpace(context.SessionSecret)
            || context.SessionHeaderName.Any(character =>
                !(char.IsAsciiLetterOrDigit(character) || character == '-'))
            || context.SessionSecret.Contains('\r')
            || context.SessionSecret.Contains('\n'))
            throw new ArgumentException(
                "The Product Sidecar session credential is invalid.",
                nameof(context));
        ProductSidecarRegistration[] registrations = expectedRegistrations
            .OrderBy(item => item.Method, StringComparer.Ordinal)
            .ToArray();
        if (registrations.Any(item =>
                string.IsNullOrWhiteSpace(item.Method)
                || item.Method.Length > 128
                || item.Scope is not ("global" or "workspace"))
            || registrations.Select(item => item.Method)
                .Distinct(StringComparer.Ordinal).Count() != registrations.Length)
            throw new ArgumentException(
                "The expected Product RPC registrations are invalid.",
                nameof(expectedRegistrations));

        _origin = context.Origin;
        _sessionHeaderName = context.SessionHeaderName;
        _sessionSecret = context.SessionSecret;
        _expectedIdentity = expectedIdentity with { };
        _expectedRegistrations = registrations;
        _timeout = timeout ?? TimeSpan.FromSeconds(30);
        if (_timeout <= TimeSpan.Zero)
            throw new ArgumentOutOfRangeException(nameof(timeout));
        _client = handler is null
            ? new HttpClient(CreateProductionHandler(), disposeHandler: true)
            : new HttpClient(handler, disposeHandler: false);
        _client.Timeout = Timeout.InfiniteTimeSpan;
    }

    public async Task<ProductSidecarCapabilities> GetCapabilitiesAsync(
        CancellationToken cancellationToken)
    {
        long attempt;
        lock (_stateGate)
        {
            ThrowIfDisposed();
            attempt = ++_handshakeAttempt;
            _readyAttempt = -1;
        }
        ProductSidecarCapabilities capabilities = await RunCallAsync(
            cancellationToken,
            async callToken =>
        {
            using HttpRequestMessage request = CreateRequest(
                HttpMethod.Get,
                "api/vibetable/v2/product/capabilities");
            using HttpResponseMessage response = await _client.SendAsync(
                request,
                HttpCompletionOption.ResponseHeadersRead,
                callToken).ConfigureAwait(false);
            if (response.StatusCode != HttpStatusCode.OK)
                throw Unavailable();
            byte[] raw = await ReadBoundedAsync(response, callToken)
                .ConfigureAwait(false);
            try
            {
                using JsonDocument document = JsonDocument.Parse(raw);
                return ParseCapabilities(document.RootElement);
            }
            catch (JsonException)
            {
                throw InvalidCapabilities();
            }
        }).ConfigureAwait(false);
        lock (_stateGate)
        {
            ThrowIfDisposed();
            if (attempt == _handshakeAttempt)
                _readyAttempt = attempt;
        }
        return capabilities;
    }

    public async Task<ProductSidecarForwardResult> ForwardAsync(
        string requestId,
        string method,
        JsonElement wire,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        lock (_stateGate)
        {
            ThrowIfDisposed();
            if (_readyAttempt != _handshakeAttempt)
                throw new InvalidOperationException("The Product Sidecar handshake is required.");
        }
        if (string.IsNullOrWhiteSpace(requestId)
            || string.IsNullOrWhiteSpace(method)
            || method.Length > 128
            || wire.ValueKind != JsonValueKind.Object
            || parameters.ValueKind != JsonValueKind.Object)
        {
            throw new ArgumentException("The Product RPC request is invalid.");
        }
        if (!_expectedRegistrations.Any(item => item.Method == method))
            throw new InvalidOperationException("Product RPC method is not registered.");
        byte[] body = JsonSerializer.SerializeToUtf8Bytes(new
        {
            jsonrpc = "2.0",
            id = requestId,
            method,
            wire,
            @params = parameters,
        });
        if (body.Length > MaxRequestBytes)
            throw new InvalidOperationException("The Product Sidecar request is too large.");
        return await RunCallAsync(cancellationToken, async callToken =>
        {
            using HttpRequestMessage request = CreateRequest(
                HttpMethod.Post,
                "api/vibetable/v2/product/rpc");
            request.Content = new ByteArrayContent(body);
            request.Content.Headers.ContentType =
                new MediaTypeHeaderValue("application/json");
            using HttpResponseMessage response = await _client.SendAsync(
                request,
                HttpCompletionOption.ResponseHeadersRead,
                callToken).ConfigureAwait(false);
            if (response.StatusCode is not (HttpStatusCode.OK
                or HttpStatusCode.BadRequest))
                throw Unavailable();
            byte[] raw = await ReadBoundedAsync(response, callToken)
                .ConfigureAwait(false);
            try
            {
                return ParseResponse(raw, requestId, wire);
            }
            catch (JsonException)
            {
                throw InvalidResponse();
            }
        }).ConfigureAwait(false);
    }

    public void Dispose()
    {
        lock (_stateGate)
        {
            if (_disposed != 0)
                return;
            _disposed = 1;
            _handshakeAttempt++;
            _readyAttempt = -1;
        }
        _lifetime.Cancel();
        _client.Dispose();
        _lifetime.Dispose();
    }

    private HttpRequestMessage CreateRequest(HttpMethod method, string relative)
    {
        var request = new HttpRequestMessage(method, new Uri(_origin, relative));
        request.Headers.TryAddWithoutValidation(_sessionHeaderName, _sessionSecret);
        return request;
    }

    private async Task<T> RunCallAsync<T>(
        CancellationToken callerToken,
        Func<CancellationToken, Task<T>> operation)
    {
        using CancellationTokenSource call = CancellationTokenSource
            .CreateLinkedTokenSource(callerToken, _lifetime.Token);
        call.CancelAfter(_timeout);
        try
        {
            T result = await operation(call.Token).ConfigureAwait(false);
            lock (_stateGate)
                ThrowIfDisposed();
            return result;
        }
        catch (OperationCanceledException) when (callerToken.IsCancellationRequested)
        {
            throw;
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested)
        {
            throw new ObjectDisposedException(nameof(ProductSidecarHttpGateway));
        }
        catch (OperationCanceledException)
        {
            throw Unavailable();
        }
        catch (Exception error) when (error is HttpRequestException or IOException)
        {
            throw Unavailable();
        }
    }

    private static async Task<byte[]> ReadBoundedAsync(
        HttpResponseMessage response,
        CancellationToken cancellationToken)
    {
        if (response.Content.Headers.ContentLength > MaxResponseBytes)
            throw new InvalidOperationException("The Product Sidecar response is too large.");
        await using Stream source = await response.Content.ReadAsStreamAsync(
            cancellationToken).ConfigureAwait(false);
        using var target = new MemoryStream();
        byte[] buffer = new byte[16 * 1024];
        while (true)
        {
            int read = await source.ReadAsync(buffer, cancellationToken)
                .ConfigureAwait(false);
            if (read == 0)
                break;
            if (target.Length + read > MaxResponseBytes)
                throw new InvalidOperationException("The Product Sidecar response is too large.");
            target.Write(buffer, 0, read);
        }
        return target.ToArray();
    }

    private static void ValidateIdentity(
        ProductSidecarIdentity identity,
        string parameterName)
    {
        if (!IsCanonicalUuid(identity.WorkspaceId)
            || !IsCanonicalUuid(identity.ClaimId)
            || identity.SessionEpoch == 0
            || identity.FenceEpoch == 0)
            throw new ArgumentException(
                "The expected Product Sidecar identity is invalid.",
                parameterName);
    }

    private static bool IsCanonicalUuid(string value) =>
        Guid.TryParseExact(value, "D", out Guid parsed)
        && parsed != Guid.Empty && parsed.ToString("D") == value;

    private ProductSidecarCapabilities ParseCapabilities(JsonElement root)
    {
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
        var identity = new ProductSidecarIdentity(
            RequiredString(root, "workspaceId"),
            RequiredPositiveUInt64(root, "sessionEpoch"),
            RequiredPositiveUInt64(root, "fenceEpoch"),
            RequiredString(root, "claimId"));
        if (contractVersion != "2.0"
            || !IsCanonicalUuid(identity.WorkspaceId)
            || !IsCanonicalUuid(identity.ClaimId))
            throw InvalidCapabilities();
        string[] methods = RequiredOrderedMethods(root.GetProperty("rpcMethods"));
        ProductSidecarRegistration[] registrations =
            RequiredOrderedRegistrations(root.GetProperty("registrations"));
        if (!methods.SequenceEqual(
                registrations.Select(item => item.Method),
                StringComparer.Ordinal)
            || identity != _expectedIdentity
            || !registrations.SequenceEqual(_expectedRegistrations))
            throw InvalidCapabilities();
        return new ProductSidecarCapabilities(
            contractVersion, identity.WorkspaceId,
            identity.SessionEpoch, identity.FenceEpoch,
            identity.ClaimId, methods, registrations);
    }

    private static string[] RequiredOrderedMethods(JsonElement element)
    {
        if (element.ValueKind != JsonValueKind.Array)
            throw InvalidCapabilities();
        string[] methods = element.EnumerateArray()
            .Select(item => item.ValueKind == JsonValueKind.String
                ? item.GetString() ?? ""
                : throw InvalidCapabilities())
            .ToArray();
        if (methods.Any(item => string.IsNullOrWhiteSpace(item) || item.Length > 128)
            || !IsStrictlyOrdered(methods))
            throw InvalidCapabilities();
        return methods;
    }

    private static ProductSidecarRegistration[] RequiredOrderedRegistrations(
        JsonElement element)
    {
        if (element.ValueKind != JsonValueKind.Array)
            throw InvalidCapabilities();
        ProductSidecarRegistration[] registrations = element.EnumerateArray()
            .Select(item =>
            {
                RequireExactProperties(item, "method", "scope");
                return new ProductSidecarRegistration(
                    RequiredString(item, "method"),
                    RequiredString(item, "scope"));
            })
            .ToArray();
        if (registrations.Any(item =>
                item.Method.Length > 128
                || item.Scope is not ("global" or "workspace"))
            || !IsStrictlyOrdered(registrations.Select(item => item.Method)))
            throw InvalidCapabilities();
        return registrations;
    }

    private static bool IsStrictlyOrdered(IEnumerable<string> values)
        => values.Zip(
            values.Skip(1),
            (left, right) => StringComparer.Ordinal.Compare(left, right) < 0)
            .All(ordered => ordered);

    private static void RequireExactProperties(
        JsonElement element,
        params string[] expected)
    {
        if (!HasExactProperties(element, expected))
            throw InvalidCapabilities();
    }

    private static bool HasExactProperties(
        JsonElement element,
        params string[] expected)
        => element.ValueKind == JsonValueKind.Object
            && element.EnumerateObject()
                .Select(property => property.Name)
                .Order(StringComparer.Ordinal)
                .SequenceEqual(
                    expected.Order(StringComparer.Ordinal),
                    StringComparer.Ordinal);

    private static string RequiredString(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString())
                ? value.GetString()!
                : throw InvalidCapabilities();

    private static ulong RequiredPositiveUInt64(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetUInt64(out ulong parsed)
            && parsed > 0
                ? parsed
                : throw InvalidCapabilities();

    private static InvalidOperationException InvalidCapabilities() => new("Invalid Product Sidecar capabilities.");

    private static ProductSidecarForwardResult ParseResponse(
        byte[] raw,
        string requestId,
        JsonElement requestWire)
    {
        using JsonDocument document = JsonDocument.Parse(raw);
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object)
            throw InvalidResponse();
        bool hasResult = root.TryGetProperty("result", out JsonElement result);
        bool hasError = root.TryGetProperty("error", out JsonElement error);
        string[] expected = hasResult
            ? ["jsonrpc", "id", "wire", "result"]
            : ["jsonrpc", "id", "wire", "error"];
        if (hasResult == hasError
            || !HasExactProperties(root, expected)
            || !root.TryGetProperty("jsonrpc", out JsonElement version)
            || version.ValueKind != JsonValueKind.String
            || version.GetString() != "2.0"
            || !root.TryGetProperty("id", out JsonElement id)
            || id.ValueKind != JsonValueKind.String
            || !string.Equals(id.GetString(), requestId, StringComparison.Ordinal)
            || !root.TryGetProperty("wire", out JsonElement wire)
            || wire.ValueKind != JsonValueKind.Object
            || !JsonElement.DeepEquals(wire, requestWire))
            throw InvalidResponse();
        if (hasResult)
            return new ProductSidecarSuccess(wire.Clone(), result.Clone());
        ProductSidecarRpcError parsedError = ParseError(error);
        return new ProductSidecarFailure(wire.Clone(), parsedError);
    }

    private static ProductSidecarRpcError ParseError(JsonElement error)
    {
        JsonElement data = default;
        bool hasData = error.ValueKind == JsonValueKind.Object
            && error.TryGetProperty("data", out data);
        if (!HasExactProperties(
                error,
                hasData ? ["code", "message", "data"] : ["code", "message"])
            || !error.TryGetProperty("code", out JsonElement codeElement)
            || codeElement.ValueKind != JsonValueKind.Number
            || !codeElement.TryGetInt32(out int code)
            || !error.TryGetProperty("message", out JsonElement messageElement)
            || messageElement.ValueKind != JsonValueKind.String
            || string.IsNullOrWhiteSpace(messageElement.GetString()))
            throw InvalidResponse();
        string message = messageElement.GetString()!;
        if (code is not (-32600 or -32601 or -32602 or -32603 or -32150)
            || (code == -32150 && !hasData))
            throw InvalidResponse();
        if (code == -32150)
            ValidateProductErrorData(data);
        return new ProductSidecarRpcError(
            code,
            message,
            hasData ? data.Clone() : null);
    }

    private static void ValidateProductErrorData(JsonElement data)
    {
        if (!HasExactProperties(
                data,
                "kind",
                "message",
                "code",
                "path",
                "details",
                "retryable")
            || data.GetProperty("kind").ValueKind != JsonValueKind.String
            || data.GetProperty("kind").GetString() != "product_data_error"
            || !IsNonEmptyString(data.GetProperty("message"))
            || !IsNonEmptyString(data.GetProperty("code"))
            || !PublicErrorCode.IsMatch(data.GetProperty("code").GetString()!)
            || data.GetProperty("code").GetString()!.StartsWith(
                "pocketbase.",
                StringComparison.Ordinal)
            || data.GetProperty("path").ValueKind is not (
                JsonValueKind.String or JsonValueKind.Null)
            || data.GetProperty("details").ValueKind != JsonValueKind.Object
            || data.GetProperty("retryable").ValueKind is not (
                JsonValueKind.True or JsonValueKind.False))
            throw InvalidResponse();
    }

    private static bool IsNonEmptyString(JsonElement value)
        => value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString());

    private static InvalidOperationException InvalidResponse() => new("Invalid Product Sidecar response.");
    private static BackendUnavailableException Unavailable() => new("Product Sidecar is unavailable.");

    internal static HttpClientHandler CreateProductionHandler() => new()
    {
        AllowAutoRedirect = false,
        UseCookies = false,
        UseProxy = false,
    };

    private void ThrowIfDisposed() =>
        ObjectDisposedException.ThrowIf(_disposed != 0, this);
}

public sealed record ProductSidecarIdentity(string WorkspaceId, ulong SessionEpoch, ulong FenceEpoch, string ClaimId);

public readonly record struct ProductSidecarRegistration(string Method, string Scope);

public sealed record ProductSidecarCapabilities(
    string ContractVersion, string WorkspaceId,
    ulong SessionEpoch, ulong FenceEpoch, string ClaimId,
    IReadOnlyList<string> RpcMethods, IReadOnlyList<ProductSidecarRegistration> Registrations);

public abstract record ProductSidecarForwardResult(JsonElement Wire);

public sealed record ProductSidecarSuccess(JsonElement Wire, JsonElement Result)
    : ProductSidecarForwardResult(Wire);

public sealed record ProductSidecarFailure(JsonElement Wire, ProductSidecarRpcError Error)
    : ProductSidecarForwardResult(Wire);

public sealed record ProductSidecarRpcError(int Code, string Message, JsonElement? Data);
