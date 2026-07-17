using System;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DirectusAdminAuthenticatorTests
{
    [TestMethod]
    public async Task LoginAsync_ReturnsSessionCookieValue_OnSuccess()
    {
        var handler = new FakeHandler(
            HttpStatusCode.OK,
            setCookieHeader: "directus_session_token=abc123; Path=/; HttpOnly");
        var auth = new DirectusAdminAuthenticator(handler);

        string? token = await auth.LoginAsync(
            "http://127.0.0.1:49152", "admin@example.com", "pw", CancellationToken.None);

        Assert.AreEqual("abc123", token);
        // Verify the request body used mode=session and carried the creds.
        Assert.AreEqual("session", handler.LastRequestBodyMode);
        Assert.AreEqual("admin@example.com", handler.LastRequestEmail);
    }

    [TestMethod]
    public async Task LoginAsync_ReturnsNull_OnNon2xx()
    {
        var handler = new FakeHandler(HttpStatusCode.Unauthorized);
        var auth = new DirectusAdminAuthenticator(handler);

        string? token = await auth.LoginAsync(
            "http://127.0.0.1:49152", "admin@example.com", "wrong", CancellationToken.None);

        Assert.IsNull(token);
    }

    [TestMethod]
    public async Task LoginAsync_ReturnsNull_WhenCookieMissing()
    {
        // 200 but no Set-Cookie — treat as failure (don't navigate).
        var handler = new FakeHandler(HttpStatusCode.OK, setCookieHeader: null);
        var auth = new DirectusAdminAuthenticator(handler);

        string? token = await auth.LoginAsync(
            "http://127.0.0.1:49152", "admin@example.com", "pw", CancellationToken.None);

        Assert.IsNull(token);
    }

    [TestMethod]
    public async Task LoginAsync_ReturnsNull_OnNetworkError()
    {
        var handler = new FakeHandler(throwOnSend: true);
        var auth = new DirectusAdminAuthenticator(handler);

        string? token = await auth.LoginAsync(
            "http://127.0.0.1:49152", "admin@example.com", "pw", CancellationToken.None);

        Assert.IsNull(token);
    }

    private sealed class FakeHandler : HttpMessageHandler
    {
        private readonly HttpStatusCode _status;
        private readonly string? _setCookie;
        private readonly bool _throw;

        public string? LastRequestBodyMode { get; private set; }
        public string? LastRequestEmail { get; private set; }

        public FakeHandler(HttpStatusCode status = HttpStatusCode.OK, string? setCookieHeader = null, bool throwOnSend = false)
        {
            _status = status; _setCookie = setCookieHeader; _throw = throwOnSend;
        }

        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken ct)
        {
            if (_throw) throw new HttpRequestException("simulated network failure");
            // Parse the JSON body to capture mode/email for assertions.
            if (request.Content is StringContent sc)
            {
                string body = sc.ReadAsStringAsync(ct).GetAwaiter().GetResult();
                LastRequestBodyMode = body.Contains("\"mode\":\"session\"") ? "session" : null;
                LastRequestEmail = body.Contains("admin@example.com") ? "admin@example.com" : null;
            }
            var resp = new HttpResponseMessage(_status);
            if (_setCookie is not null) resp.Headers.Add("Set-Cookie", _setCookie);
            return Task.FromResult(resp);
        }
    }
}
