using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.ServerSentEvents;
using System.Runtime.CompilerServices;
using System.Text;
using System.Text.Json;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// One lazy HTTP connection per enumeration, fixed to captured Sidecar credentials.
/// EOF ends enumeration (including discarded incomplete events); the caller decides
/// recovery. Cancellation/early disposal closes the response. No retry, bookmark,
/// deduplication, task projection or renderer delivery acknowledgement is owned here.
/// The caller must validate generation/epoch and acknowledge only actual delivery.
/// Payload validation covers UTF-8, JSON and topic identity, not full product schemas.
/// </summary>
internal sealed class ProductRealtimeStream
{
    private static readonly UTF8Encoding StrictUtf8 = new(false, true);
    private readonly PocketBaseAdminContext _context;
    private readonly HttpMessageHandler? _handler;

    internal ProductRealtimeStream(PocketBaseAdminContext context, HttpMessageHandler? handler = null)
    {
        ArgumentNullException.ThrowIfNull(context);
        if (context.Origin is null || !context.Origin.IsAbsoluteUri
            || context.Origin.Scheme != Uri.UriSchemeHttp || !context.Origin.IsLoopback
            || string.IsNullOrWhiteSpace(context.SessionHeaderName)
            || context.SessionHeaderName.Any(value => !(char.IsAsciiLetterOrDigit(value) || value == '-'))
            || string.IsNullOrWhiteSpace(context.SessionSecret)
            || context.SessionSecret.Contains('\r') || context.SessionSecret.Contains('\n'))
            throw new ArgumentException("Invalid private Sidecar connection context.", nameof(context));
        _context = context;
        _handler = handler;
    }

    internal async IAsyncEnumerable<ProductRealtimeFrame> ReadAsync(
        string? after = null, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        if (!string.IsNullOrEmpty(after) && !IsCursor(after, allowZero: true))
            throw new ArgumentException("Invalid realtime cursor.", nameof(after));
        using var client = new HttpClient(_handler ?? ProductSidecarHttpGateway.CreateProductionHandler(),
            disposeHandler: _handler is null) { Timeout = Timeout.InfiniteTimeSpan };
        using var request = new HttpRequestMessage(HttpMethod.Get,
            new Uri(_context.Origin, "api/vibetable/v2/events"));
        request.Headers.Add(_context.SessionHeaderName, _context.SessionSecret);
        request.Headers.Accept.ParseAdd("text/event-stream");
        if (!string.IsNullOrEmpty(after)) request.Headers.Add("Last-Event-ID", after);
        using HttpResponseMessage response = await ReadSafelyAsync(() => client.SendAsync(
            request, HttpCompletionOption.ResponseHeadersRead, cancellationToken), cancellationToken)
            .ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
        if (response.StatusCode != HttpStatusCode.OK)
            throw await ReadFailureAsync(response, cancellationToken).ConfigureAwait(false);
        if (response.Content.Headers.ContentType?.MediaType != "text/event-stream")
            throw InvalidFrame();
        await using Stream source = await ReadSafelyAsync(
            () => response.Content.ReadAsStreamAsync(cancellationToken), cancellationToken).ConfigureAwait(false);
        using var bounded = new BoundedSseReadStream(source);
        await using var items = SseParser.Create(bounded, ParsePayload)
            .EnumerateAsync(cancellationToken).GetAsyncEnumerator(cancellationToken);
        while (await ReadSafelyAsync(() => items.MoveNextAsync().AsTask(), cancellationToken).ConfigureAwait(false))
        {
            cancellationToken.ThrowIfCancellationRequested();
            SseItem<JsonElement> item = items.Current;
            if (!IsCursor(item.EventId, allowZero: item.EventType == "realtime.recovered"))
                throw InvalidFrame();
            yield return new ProductRealtimeFrame(item.EventId!, item.EventType, item.Data);
        }
        cancellationToken.ThrowIfCancellationRequested();
    }

    private static JsonElement ParsePayload(string topic, ReadOnlySpan<byte> bytes)
    {
        if (bytes.Length > BoundedSseReadStream.MaxJsonBytes)
            throw new InvalidDataException("Realtime JSON exceeds the wire budget.");
        if (topic is not ("data.changed" or "task.changed" or "realtime.recovered")) throw InvalidFrame();
        JsonElement payload;
        try
        {
            _ = StrictUtf8.GetCharCount(bytes);
            payload = JsonSerializer.Deserialize<JsonElement>(bytes);
        }
        catch (Exception error) when (error is JsonException or DecoderFallbackException) { throw InvalidFrame(); }
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty("contractVersion", out var version)
            || version.ValueKind != JsonValueKind.String || version.GetString() != "2.0"
            || !payload.TryGetProperty("topic", out var actualTopic)
            || actualTopic.ValueKind != JsonValueKind.String || actualTopic.GetString() != topic)
            throw InvalidFrame();
        return payload;
    }

    private static bool IsCursor(string? cursor, bool allowZero)
        => cursor is not null && cursor.StartsWith("rt:", StringComparison.Ordinal)
            && long.TryParse(cursor.AsSpan(3), NumberStyles.None, CultureInfo.InvariantCulture, out long value)
            && value >= (allowZero ? 0 : 1)
            && cursor == "rt:" + value.ToString(CultureInfo.InvariantCulture);

    private static async Task<T> ReadSafelyAsync<T>(Func<Task<T>> read, CancellationToken token)
    {
        try { return await read().ConfigureAwait(false); }
        catch (OperationCanceledException) when (token.IsCancellationRequested) { throw; }
        catch (Exception error) when (error is HttpRequestException or IOException or OperationCanceledException)
        {
            token.ThrowIfCancellationRequested();
            throw new BackendUnavailableException("Realtime stream is unavailable.");
        }
    }

    private static InvalidDataException InvalidFrame() => new("Invalid realtime frame.");

    private static async Task<ProductRealtimeRequestException> ReadFailureAsync(
        HttpResponseMessage response, CancellationToken token)
    {
        string code = "realtime.request_failed";
        bool retryable = false;
        // Only these statuses can carry a retryable Go error. Other HTTP errors
        // remain terminal without reading their potentially unrelated body.
        if (response.StatusCode is (HttpStatusCode.InternalServerError or HttpStatusCode.ServiceUnavailable)
            && response.Content.Headers.ContentType?.MediaType == "application/json")
        {
            try
            {
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(token);
                timeout.CancelAfter(TimeSpan.FromSeconds(5));
                await using Stream body = await response.Content.ReadAsStreamAsync(timeout.Token).ConfigureAwait(false);
                byte[] bytes = new byte[8193];
                int length = 0;
                while (length < bytes.Length)
                {
                    int count = await body.ReadAsync(bytes.AsMemory(length), timeout.Token).ConfigureAwait(false);
                    if (count == 0) break;
                    length += count;
                }
                if (length <= 8192)
                {
                    using JsonDocument document = JsonDocument.Parse(bytes.AsMemory(0, length));
                    JsonElement root = document.RootElement;
                    if (root.ValueKind == JsonValueKind.Object
                        && root.EnumerateObject().Count(property => property.Name == "code") == 1
                        && root.EnumerateObject().Count(property => property.Name == "retryable") == 1
                        && root.GetProperty("code").ValueKind == JsonValueKind.String
                        && root.GetProperty("retryable").ValueKind == JsonValueKind.True)
                    {
                        string? candidate = root.GetProperty("code").GetString();
                        retryable = response.StatusCode == HttpStatusCode.ServiceUnavailable
                            ? candidate == "realtime.capacity"
                            : candidate is "realtime.storage_failed" or "realtime.unavailable"
                                or "realtime.streaming_unavailable" or "realtime.internal_failed";
                        if (retryable) code = candidate!;
                    }
                }
            }
            catch (Exception error) when (error is JsonException or IOException
                or HttpRequestException or OperationCanceledException) { }
        }
        token.ThrowIfCancellationRequested();
        return new(code, response.StatusCode, retryable);
    }
}

internal sealed record ProductRealtimeFrame(string Cursor, string Topic, JsonElement Payload);

internal sealed class ProductRealtimeRequestException(string code, HttpStatusCode status, bool retryable)
    : HttpRequestException("Realtime stream request failed.", null, status)
{
    internal string Code { get; } = code;
    internal bool Retryable { get; } = retryable;
}
