using System;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Default <see cref="IDirectusAdminAuthenticator"/>. Uses a long-lived
/// <see cref="HttpClient"/>; the optional <see cref="HttpMessageHandler"/>
/// constructor argument exists for tests.
/// </summary>
public sealed class DirectusAdminAuthenticator : IDirectusAdminAuthenticator
{
    private static readonly string SessionCookieName = "directus_session_token";

    private readonly HttpClient _client;

    public DirectusAdminAuthenticator(HttpMessageHandler? handler = null)
    {
        _client = handler is null ? new HttpClient() : new HttpClient(handler);
    }

    public async Task<string?> LoginAsync(
        string baseUrl, string email, string password, CancellationToken ct)
    {
        try
        {
            using var req = new HttpRequestMessage(HttpMethod.Post, baseUrl.TrimEnd('/') + "/auth/login")
            {
                Content = new StringContent(
                    JsonSerializer.Serialize(new
                    {
                        email,
                        password,
                        mode = "session",
                    }),
                    Encoding.UTF8,
                    "application/json"),
            };

            using var resp = await _client.SendAsync(req, ct).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
            {
                return null;
            }

            // Set-Cookie headers can appear multiple times; pick the session one.
            if (!resp.Headers.TryGetValues("Set-Cookie", out var cookies))
            {
                return null;
            }

            foreach (string raw in cookies)
            {
                // Parse just the name=value prefix (cookie attributes follow ';').
                int semi = raw.IndexOf(';');
                string pair = semi < 0 ? raw : raw[..semi];
                int eq = pair.IndexOf('=');
                if (eq <= 0) continue;
                string name = pair[..eq].Trim();
                if (string.Equals(name, SessionCookieName, StringComparison.Ordinal))
                {
                    return pair[(eq + 1)..].Trim();
                }
            }
            return null;
        }
        catch (HttpRequestException)
        {
            return null;
        }
    }
}
