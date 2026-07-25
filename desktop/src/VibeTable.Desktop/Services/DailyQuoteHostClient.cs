using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record DailyQuoteHostRequest(
    string Provider,
    string Style,
    string Locale);

internal sealed record DailyQuoteHostResult(
    string Text,
    string Attribution,
    string Url);

internal sealed class DailyQuoteHostException : Exception
{
    public DailyQuoteHostException(string code, string message)
        : base(message)
    {
        Code = code;
    }

    public string Code { get; }
}

/// <summary>
/// Fixed-endpoint client for the optional daily quote feature. Renderer input
/// selects an enum-like provider; it can never supply a URL.
/// </summary>
internal sealed class DailyQuoteHostClient : IDisposable
{
    internal const int MaximumResponseBytes = 32 * 1024;
    internal static readonly TimeSpan RequestTimeout = TimeSpan.FromSeconds(3);

    private static readonly Uri HitokotoEndpoint =
        new("https://v1.hitokoto.cn/");
    private static readonly Uri JinrishiciTokenEndpoint =
        new("https://v2.jinrishici.com/token");
    private static readonly Uri JinrishiciSentenceEndpoint =
        new("https://v2.jinrishici.com/sentence");
    private static readonly Uri QuotableEndpoint =
        new("https://api.quotable.io/quotes/random");

    private static readonly IReadOnlyDictionary<string, string[]> HitokotoCategories =
        new Dictionary<string, string[]>(StringComparer.Ordinal)
        {
            ["mixed"] = ["d", "e", "i", "k"],
            ["inspiring"] = ["e", "f", "k"],
            ["literary"] = ["d"],
            ["philosophy"] = ["k"],
            ["poetry"] = ["i"],
            ["lighthearted"] = ["l"],
        };

    private readonly HttpClient _httpClient;
    private readonly TimeSpan _timeout;
    private readonly bool _ownsClient;

    public DailyQuoteHostClient()
        : this(
            new HttpClient(CreateHttpMessageHandler())
            {
                Timeout = Timeout.InfiniteTimeSpan,
            },
            RequestTimeout,
            ownsClient: true)
    {
    }

    internal DailyQuoteHostClient(
        HttpClient httpClient,
        TimeSpan? timeout = null,
        bool ownsClient = false)
    {
        _httpClient = httpClient ?? throw new ArgumentNullException(nameof(httpClient));
        _timeout = timeout ?? RequestTimeout;
        _ownsClient = ownsClient;
    }

    internal static HttpClientHandler CreateHttpMessageHandler()
        => new() { AllowAutoRedirect = false };

    internal static bool TryParseRequest(
        JsonElement payload,
        out DailyQuoteHostRequest? request)
    {
        request = null;
        if (payload.ValueKind != JsonValueKind.Object)
        {
            return false;
        }
        foreach (JsonProperty property in payload.EnumerateObject())
        {
            if (property.Name is not ("provider" or "style" or "locale"))
            {
                return false;
            }
        }
        if (!TryGetString(payload, "provider", out string? provider)
            || !TryGetString(payload, "style", out string? style)
            || !TryGetString(payload, "locale", out string? locale)
            || provider is null
            || style is null
            || locale is null
            || provider is not ("hitokoto" or "jinrishici" or "quotable")
            || locale is not ("zh-CN" or "en-US")
            || !IsAllowedStyle(provider, style))
        {
            return false;
        }
        request = new DailyQuoteHostRequest(provider, style, locale);
        return true;
    }

    public async Task<DailyQuoteHostResult> FetchAsync(
        DailyQuoteHostRequest request,
        CancellationToken cancellationToken)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken);
        timeout.CancelAfter(_timeout);
        try
        {
            return request.Provider switch
            {
                "hitokoto" => await FetchHitokotoAsync(
                    request.Style,
                    timeout.Token).ConfigureAwait(false),
                "jinrishici" => await FetchJinrishiciAsync(timeout.Token)
                    .ConfigureAwait(false),
                "quotable" => await FetchQuotableAsync(
                    request.Style,
                    timeout.Token).ConfigureAwait(false),
                _ => throw new DailyQuoteHostException(
                    "DAILY_QUOTE_BAD_PROVIDER",
                    "The selected daily quote provider is not supported."),
            };
        }
        catch (OperationCanceledException)
            when (!cancellationToken.IsCancellationRequested)
        {
            throw new DailyQuoteHostException(
                "DAILY_QUOTE_TIMEOUT",
                "The daily quote provider did not respond in time.");
        }
        catch (DailyQuoteHostException)
        {
            throw;
        }
        catch (HttpRequestException)
        {
            throw new DailyQuoteHostException(
                "DAILY_QUOTE_UNAVAILABLE",
                "The daily quote provider is unavailable.");
        }
        catch (JsonException)
        {
            throw new DailyQuoteHostException(
                "DAILY_QUOTE_INVALID_RESPONSE",
                "The daily quote provider returned an invalid response.");
        }
    }

    private async Task<DailyQuoteHostResult> FetchHitokotoAsync(
        string style,
        CancellationToken cancellationToken)
    {
        using JsonDocument document = await GetJsonAsync(
            BuildHitokotoUri(style),
            headers: null,
            cancellationToken).ConfigureAwait(false);
        return ParseHitokoto(document);
    }

    private async Task<DailyQuoteHostResult> FetchQuotableAsync(
        string style,
        CancellationToken cancellationToken)
    {
        using JsonDocument document = await GetJsonAsync(
            BuildQuotableUri(style),
            headers: null,
            cancellationToken).ConfigureAwait(false);
        return ParseQuotable(document);
    }

    private async Task<DailyQuoteHostResult> FetchJinrishiciAsync(
        CancellationToken cancellationToken)
    {
        using JsonDocument tokenDocument = await GetJsonAsync(
            JinrishiciTokenEndpoint,
            headers: null,
            cancellationToken).ConfigureAwait(false);
        string token = tokenDocument.RootElement.ValueKind == JsonValueKind.Object
            && tokenDocument.RootElement.TryGetProperty("data", out JsonElement tokenNode)
            && tokenNode.ValueKind == JsonValueKind.String
                ? tokenNode.GetString() ?? string.Empty
                : string.Empty;
        if (!IsSafeToken(token))
        {
            throw InvalidResponse();
        }
        var headers = new Dictionary<string, string>(StringComparer.Ordinal)
        {
            ["X-User-Token"] = token,
        };
        using JsonDocument sentence = await GetJsonAsync(
            JinrishiciSentenceEndpoint,
            headers,
            cancellationToken).ConfigureAwait(false);
        return ParseJinrishici(sentence);
    }

    private async Task<JsonDocument> GetJsonAsync(
        Uri endpoint,
        IReadOnlyDictionary<string, string>? headers,
        CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, endpoint);
        request.Headers.Accept.ParseAdd("application/json");
        if (headers is not null)
        {
            foreach ((string name, string value) in headers)
            {
                request.Headers.TryAddWithoutValidation(name, value);
            }
        }
        using HttpResponseMessage response = await _httpClient.SendAsync(
            request,
            HttpCompletionOption.ResponseHeadersRead,
            cancellationToken).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new DailyQuoteHostException(
                "DAILY_QUOTE_UNAVAILABLE",
                "The daily quote provider is unavailable.");
        }
        string? mediaType = response.Content.Headers.ContentType?.MediaType;
        if (mediaType is null
            || !(string.Equals(mediaType, "application/json", StringComparison.OrdinalIgnoreCase)
                || mediaType.EndsWith("+json", StringComparison.OrdinalIgnoreCase)))
        {
            throw new DailyQuoteHostException(
                "DAILY_QUOTE_INVALID_CONTENT_TYPE",
                "The daily quote provider returned an unsupported content type.");
        }
        if (response.Content.Headers.ContentLength > MaximumResponseBytes)
        {
            throw ResponseTooLarge();
        }

        await using Stream input = await response.Content
            .ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
        using var output = new MemoryStream();
        byte[] buffer = new byte[4096];
        while (true)
        {
            int read = await input.ReadAsync(buffer, cancellationToken)
                .ConfigureAwait(false);
            if (read == 0)
            {
                break;
            }
            if (output.Length + read > MaximumResponseBytes)
            {
                throw ResponseTooLarge();
            }
            output.Write(buffer, 0, read);
        }
        output.Position = 0;
        return JsonDocument.Parse(
            output,
            new JsonDocumentOptions
            {
                AllowTrailingCommas = false,
                CommentHandling = JsonCommentHandling.Disallow,
                MaxDepth = 16,
            });
    }

    private static DailyQuoteHostResult ParseHitokoto(JsonDocument document)
    {
        JsonElement root = document.RootElement;
        string text = GetCleanString(root, "hitokoto", 96);
        string attribution = JoinAttribution(
            GetCleanString(root, "from_who", 40),
            GetCleanString(root, "from", 60));
        if (text.Length < 2)
        {
            throw InvalidResponse();
        }
        string uuid = GetCleanString(root, "uuid", 40);
        string url = uuid.Length >= 8 && uuid.All(IsSafeIdentifierCharacter)
            ? $"https://hitokoto.cn/?uuid={Uri.EscapeDataString(uuid)}"
            : "https://hitokoto.cn/";
        return new DailyQuoteHostResult(text, attribution, url);
    }

    private static DailyQuoteHostResult ParseJinrishici(JsonDocument document)
    {
        JsonElement root = document.RootElement;
        if (GetCleanString(root, "status", 16) != "success"
            || !root.TryGetProperty("data", out JsonElement data)
            || data.ValueKind != JsonValueKind.Object)
        {
            throw InvalidResponse();
        }
        string text = GetCleanString(data, "content", 96);
        if (text.Length < 2)
        {
            throw InvalidResponse();
        }
        string attribution = string.Empty;
        if (data.TryGetProperty("origin", out JsonElement origin)
            && origin.ValueKind == JsonValueKind.Object)
        {
            attribution = JoinAttribution(
                GetCleanString(origin, "dynasty", 16),
                GetCleanString(origin, "author", 32),
                GetCleanString(origin, "title", 48));
        }
        return new DailyQuoteHostResult(
            text,
            attribution,
            "https://www.jinrishici.com/");
    }

    private static DailyQuoteHostResult ParseQuotable(JsonDocument document)
    {
        JsonElement root = document.RootElement;
        JsonElement item = root.ValueKind == JsonValueKind.Array
            && root.GetArrayLength() > 0
                ? root[0]
                : root;
        if (item.ValueKind != JsonValueKind.Object)
        {
            throw InvalidResponse();
        }
        string text = GetCleanString(item, "content", 140);
        if (text.Length < 2)
        {
            throw InvalidResponse();
        }
        string id = GetCleanString(item, "_id", 64);
        string url = id.Length >= 4 && id.All(IsSafeIdentifierCharacter)
            ? $"https://quotable.io/quotes/{Uri.EscapeDataString(id)}"
            : "https://quotable.io/";
        return new DailyQuoteHostResult(
            text,
            GetCleanString(item, "author", 60),
            url);
    }

    private static Uri BuildHitokotoUri(string style)
    {
        IEnumerable<string> parameters =
            new[] { "encode=json", "charset=utf-8", "max_length=64" }
                .Concat(HitokotoCategories[style].Select(
                    category => $"c={Uri.EscapeDataString(category)}"));
        return new UriBuilder(HitokotoEndpoint)
        {
            Query = string.Join("&", parameters),
        }.Uri;
    }

    private static Uri BuildQuotableUri(string style)
    {
        var parameters = new List<string> { "limit=1", "maxLength=96" };
        string? tags = style switch
        {
            "inspiring" => "inspirational|success",
            "philosophy" => "philosophy|wisdom",
            _ => null,
        };
        if (tags is not null)
        {
            parameters.Add($"tags={Uri.EscapeDataString(tags)}");
        }
        return new UriBuilder(QuotableEndpoint)
        {
            Query = string.Join("&", parameters),
        }.Uri;
    }

    private static bool TryGetString(
        JsonElement payload,
        string name,
        out string? value)
    {
        value = payload.TryGetProperty(name, out JsonElement node)
            && node.ValueKind == JsonValueKind.String
                ? node.GetString()
                : null;
        return value is not null;
    }

    private static bool IsAllowedStyle(string provider, string style)
        => provider switch
        {
            "hitokoto" => HitokotoCategories.ContainsKey(style),
            "jinrishici" => style == "poetry",
            "quotable" => style is "mixed" or "inspiring" or "philosophy",
            _ => false,
        };

    private static string GetCleanString(
        JsonElement parent,
        string property,
        int maximumLength)
        => parent.ValueKind == JsonValueKind.Object
            && parent.TryGetProperty(property, out JsonElement node)
            && node.ValueKind == JsonValueKind.String
                ? Clean(node.GetString(), maximumLength)
                : string.Empty;

    private static string Clean(string? value, int maximumLength)
    {
        if (string.IsNullOrWhiteSpace(value))
        {
            return string.Empty;
        }
        var builder = new StringBuilder(Math.Min(value.Length, maximumLength));
        bool pendingSpace = false;
        bool insideTag = false;
        foreach (char character in value)
        {
            if (character == '<')
            {
                insideTag = true;
                pendingSpace = builder.Length > 0;
                continue;
            }
            if (insideTag)
            {
                if (character == '>')
                {
                    insideTag = false;
                }
                continue;
            }
            if (char.IsControl(character)
                || char.IsWhiteSpace(character)
                || character == '>')
            {
                pendingSpace = builder.Length > 0;
                continue;
            }
            if (pendingSpace && builder.Length < maximumLength)
            {
                builder.Append(' ');
            }
            pendingSpace = false;
            if (builder.Length >= maximumLength)
            {
                break;
            }
            builder.Append(character);
        }
        return builder.ToString().Trim();
    }

    private static string JoinAttribution(params string[] values)
        => Clean(string.Join(" · ", values.Where(value => value.Length > 0)), 120);

    private static bool IsSafeIdentifierCharacter(char character)
        => char.IsAsciiLetterOrDigit(character) || character is '-' or '_';

    private static bool IsSafeToken(string value)
        => value.Length is >= 20 and <= 200
            && value.All(character =>
                char.IsAsciiLetterOrDigit(character)
                || character is '/' or '+' or '-' or '_' or '=');

    private static DailyQuoteHostException InvalidResponse()
        => new(
            "DAILY_QUOTE_INVALID_RESPONSE",
            "The daily quote provider returned an invalid response.");

    private static DailyQuoteHostException ResponseTooLarge()
        => new(
            "DAILY_QUOTE_RESPONSE_TOO_LARGE",
            "The daily quote provider response exceeded the size limit.");

    public void Dispose()
    {
        if (_ownsClient)
        {
            _httpClient.Dispose();
        }
    }
}
