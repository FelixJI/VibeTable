using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Establishes a Directus admin session by calling <c>/auth/login</c> with
/// <c>mode=session</c>, returning the <c>directus_session_token</c> cookie
/// value for the host to inject into the WebView2 cookie jar. Never throws;
/// failures return null so the caller can show a host-owned error page
/// instead of leaking the Directus login page.
/// </summary>
public interface IDirectusAdminAuthenticator
{
    /// <summary>
    /// Logs in as the admin user. Returns the session cookie value, or null
    /// on any failure (non-2xx, missing cookie, network error).
    /// </summary>
    Task<string?> LoginAsync(string baseUrl, string email, string password, CancellationToken ct);
}
