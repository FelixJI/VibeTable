using System;
using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.PocketBase;

public interface IPocketBaseHealthProbe
{
    Task<PocketBaseHealthStatus?> GetHealthAsync(
        Uri endpoint,
        string sessionSecret,
        CancellationToken cancellationToken);

    Task<bool> RequestShutdownAsync(
        Uri endpoint,
        string sessionSecret,
        CancellationToken cancellationToken);
}

public sealed record PocketBaseBuildIdentity(
    string? ContractVersion,
    string? PocketBaseVersion,
    string? SchemaVersion,
    string? MigrationHash);

public sealed record PocketBaseHealthStatus(
    string? Status,
    string? PocketBase,
    bool SchemaReady,
    bool StorageWritable,
    PocketBaseBuildIdentity? Build);

internal sealed class HttpPocketBaseHealthProbe : IPocketBaseHealthProbe, IDisposable
{
    private const string SessionHeader = "X-VibeTable-Session";
    private readonly HttpClient _httpClient;

    public HttpPocketBaseHealthProbe()
        : this(new HttpClient { Timeout = TimeSpan.FromSeconds(2) })
    {
    }

    internal HttpPocketBaseHealthProbe(HttpClient httpClient)
    {
        _httpClient = httpClient ?? throw new ArgumentNullException(nameof(httpClient));
    }

    public async Task<PocketBaseHealthStatus?> GetHealthAsync(
        Uri endpoint,
        string sessionSecret,
        CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, endpoint);
        request.Headers.TryAddWithoutValidation(SessionHeader, sessionSecret);
        using HttpResponseMessage response = await _httpClient
            .SendAsync(request, HttpCompletionOption.ResponseContentRead, cancellationToken)
            .ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            return null;
        }
        try
        {
            await using var content = await response.Content
                .ReadAsStreamAsync(cancellationToken)
                .ConfigureAwait(false);
            return await JsonSerializer.DeserializeAsync<PocketBaseHealthStatus>(
                content,
                new JsonSerializerOptions { PropertyNameCaseInsensitive = true },
                cancellationToken).ConfigureAwait(false)
                ?? throw new InvalidDataException(
                    "PocketBase health endpoint returned an empty response.");
        }
        catch (JsonException exception)
        {
            throw new InvalidDataException(
                "PocketBase health endpoint returned invalid JSON.",
                exception);
        }
    }

    public async Task<bool> RequestShutdownAsync(
        Uri endpoint,
        string sessionSecret,
        CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(HttpMethod.Post, endpoint);
        request.Headers.TryAddWithoutValidation(SessionHeader, sessionSecret);
        using HttpResponseMessage response = await _httpClient
            .SendAsync(request, HttpCompletionOption.ResponseHeadersRead, cancellationToken)
            .ConfigureAwait(false);
        return response.StatusCode == System.Net.HttpStatusCode.Accepted;
    }

    public void Dispose() => _httpClient.Dispose();
}
