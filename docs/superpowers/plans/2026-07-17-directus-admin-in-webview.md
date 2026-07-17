# Directus Admin in WebView — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user open Directus's own admin UI from inside the VibeTable WebView2, with the host auto-logging-in via session-cookie injection, while restricting Directus to loopback only.

**Architecture:** A single WebView2 instance navigates between two origins at runtime — `https://app.vibetable.local` (web-grid, unchanged) and `http://127.0.0.1:<port>/admin/` (Directus native). No reverse proxy. The host performs `/auth/login` itself and injects the resulting session cookie into the WebView2 cookie jar via `CoreWebView2CookieManager` before navigating. Directus is rebound to `127.0.0.1` and a high port range.

**Tech Stack:** C# (.NET 10, WPF, WebView2), Directus 12, TypeScript/Vite web-grid, MSTest.

**Spec:** `docs/superpowers/specs/2026-07-17-directus-admin-in-webview-design.md`

## Global Constraints

- **Directus env keys added:** `HOST=127.0.0.1`, `SESSION_COOKIE_TTL=7d`, `SESSION_COOKIE_SAME_SITE=lax`. (No `ADMIN_TOKEN` / static token — auto-login uses `ADMIN_PASSWORD` via session login.)
- **Port range:** default `49152`, probe range `49152..49201` (i.e. `PortProbeRangeStart=49152`, `PortProbeRangeEnd=49152+50=49202`, exclusive upper).
- **Three `.env.template` copies** must all be edited identically for the brand-neutral keys: `scripts/local_directus/.env.template`, `dist/.VibeTable.Next.staging/local-directus/.env.template`, `dist/.RCPM.Next.staging/local-directus/.env.template`.
- **Navigation allowlist:** `http://127.0.0.1:<directus-port>/*` and `http://localhost:<directus-port>/*` added to `IsAppOrigin`, alongside the existing `https://app.vibetable.local/*`.
- **Cookie auto-login target host:** the navigated admin URL must use the SAME host string the cookie's `Domain` is set to (use `127.0.0.1` consistently — that's what `DirectusSupervisor.BaseUrl` will produce once `HOST=127.0.0.1` is set; see Task 3 for the `BaseUrl` host-source consequence).
- **No Python BFF changes.** No Kestrel. No token middleware.
- **Build/test commands** (run from repo root):
  - Backend Python tests: `python -m pytest tests/backend/ -q`
  - .NET tests: `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj` and `dotnet test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj`
  - web-grid build: `cd desktop/web-grid && npm run build`

---

## File Structure

**New files:**
- `desktop/src/VibeTable.Desktop/Services/DirectusAdminAuthenticator.cs` — performs `/auth/login` and returns the session cookie value. Pure HTTP + parsing; no WebView2 dependency (testable without a real CoreWebView2).
- `desktop/src/VibeTable.Desktop/Services/IDirectusAdminAuthenticator.cs` — interface for the above (one method).
- `desktop/tests/VibeTable.Desktop.Tests/DirectusAdminAuthenticatorTests.cs` — MSTest tests using a fake `HttpMessageHandler`.

**Modified files:**
- `desktop/src/VibeTable.Infrastructure/Directus/DirectusEnvMaterializer.cs` — new env keys + new port range.
- `desktop/src/VibeTable.Infrastructure/Directus/DirectusSupervisor.cs` — `BaseUrl` now produces `http://127.0.0.1:<port>` (driven by the env key; minimal code change).
- `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs` — add `"admin.openRequested"` to `WebRequestWhitelist`.
- `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs` — new `case "admin.openRequested"` + handler method.
- `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs` — widen `IsAppOrigin`; replace blanket `NewWindowRequested` with path discrimination; inject session cookie before admin navigation.
- `desktop/web-grid/src/contracts.ts` — add `"admin.openRequested"` to `WebMessageType`.
- `desktop/web-grid/src/` — add an "Open Admin" button somewhere reasonable (toolbar); see Task 6.
- `scripts/local_directus/.env.template` + two `dist/...staging/` copies — new env keys.
- `desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusEnvMaterializerTests.cs` — assert new env keys + port range.
- `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs` — assert `admin.openRequested` is whitelisted.

---

## Task 1: Directus env — loopback bind, high port, session cookie config

**Why first:** This is the root-cause security fix (§2.4 of spec) and everything downstream reads the resolved port from it. It's a pure file-value change with deterministic tests.

**Files:**
- Modify: `desktop/src/VibeTable.Infrastructure/Directus/DirectusEnvMaterializer.cs`
- Modify: `scripts/local_directus/.env.template`
- Modify: `dist/.VibeTable.Next.staging/local-directus/.env.template`
- Modify: `dist/.RCPM.Next.staging/local-directus/.env.template`
- Test: `desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusEnvMaterializerTests.cs`

**Interfaces:**
- Consumes: nothing (root change).
- Produces: `DirectusEnvMaterializer` now writes `HOST`, `SESSION_COOKIE_TTL`, `SESSION_COOKIE_SAME_SITE` into the materialised `.env`, and the default port constants change to the 49152 range. `PickFreePort(int)` signature is unchanged.

- [ ] **Step 1: Write failing tests for the new env keys and port range**

Add these tests to `DirectusEnvMaterializerTests.cs` (inside the existing `[TestClass]`, before the `WithTemporaryDirectory` helper):

```csharp
[TestMethod]
public void Materialize_WritesLoopbackHostAndSessionCookieConfig()
{
    WithTemporaryDirectory(dir =>
    {
        File.WriteAllText(Path.Combine(dir, ".env.template"),
            "KEY=__GENERATE__\nSECRET=__GENERATE__\nPORT=49152\n"
            + "ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD=__GENERATE__\n");

        var env = DirectusEnvMaterializer.Materialize(dir);

        Assert.AreEqual("127.0.0.1", env["HOST"],
            "Directus must bind loopback to close the default 0.0.0.0 exposure");
        Assert.AreEqual("7d", env["SESSION_COOKIE_TTL"],
            "long TTL so the injected session survives a long-running app session");
        Assert.AreEqual("lax", env["SESSION_COOKIE_SAME_SITE"],
            "lax avoids cross-site cookie drop on localhost");
    });
}

[TestMethod]
public void DefaultPort_IsInHighEphemeralRange()
{
    // The constant must move off the well-known 8055 to the IANA ephemeral range.
    Assert.AreEqual(49152, DirectusEnvMaterializer.DefaultPort);
    Assert.IsTrue(DirectusEnvMaterializer.PortProbeRangeStart >= 49152,
        "probe range must start in the ephemeral range");
    Assert.IsTrue(DirectusEnvMaterializer.PortProbeRangeEnd <= 49152 + 50 + 1,
        "probe range must be within +50 of the default");
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `dotnet test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj --filter "FullyQualifiedName~Materialize_WritesLoopbackHostAndSessionCookieConfig|FullyQualifiedName~DefaultPort_IsInHighEphemeralRange"`
Expected: FAIL — `DefaultPort` is still `8055`; `HOST` key absent.

- [ ] **Step 3: Update the constants in `DirectusEnvMaterializer.cs`**

In `DirectusEnvMaterializer.cs`, find (around lines 34-40):

```csharp
    /// <summary>Default port (matches <c>.env.template</c> PORT and run.py).</summary>
    public const int DefaultPort = 8055;

    private const string GeneratePlaceholder = "__GENERATE__";
    private static readonly string[] GeneratedKeys = { "KEY", "SECRET", "ADMIN_PASSWORD" };
    private const int PortProbeRangeStart = DefaultPort;
    private const int PortProbeRangeEnd = DefaultPort + 100;
```

Replace with:

```csharp
    /// <summary>
    /// Default port in the IANA ephemeral range (49152+), off the well-known
    /// 8055. Avoids clashes with registered services.
    /// </summary>
    public const int DefaultPort = 49152;

    private const string GeneratePlaceholder = "__GENERATE__";
    private static readonly string[] GeneratedKeys = { "KEY", "SECRET", "ADMIN_PASSWORD" };

    /// <summary>
    /// Probe range for port-conflict evasion. Exposed for tests so the
    /// "high ephemeral range" invariant can be asserted.
    /// </summary>
    public const int PortProbeRangeStart = DefaultPort;
    public const int PortProbeRangeEnd = DefaultPort + 50;
```

(We make `PortProbeRangeStart`/`PortProbeRangeEnd` `public const` so the test can assert them. They were previously `private const`; widen visibility — no behavioral change.)

- [ ] **Step 4: Add the new env keys to the materialised output**

In `DirectusEnvMaterializer.cs`, find the `Materialize` method. After the existing `EnsureSqliteDatabaseDirectory(directory, values);` line (just before `WriteEnv(envFile, values);`), insert a block that sets host-binding / session-cookie defaults if they're absent from the template. We set them unconditionally here (they are host-owned security defaults; user `.env` edits to these keys are not a supported scenario):

```csharp
        EnsureSqliteDatabaseDirectory(directory, values);

        // Host-owned security defaults. These close the default 0.0.0.0
        // exposure (HOST) and make the injected session cookie usable on a
        // loopback http origin (SESSION_COOKIE_*). Applied on every
        // materialization; not user-overridable in the supported workflow.
        values["HOST"] = "127.0.0.1";
        values["SESSION_COOKIE_TTL"] = "7d";
        values["SESSION_COOKIE_SAME_SITE"] = "lax";

        WriteEnv(envFile, values);
        return values;
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `dotnet test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj --filter "FullyQualifiedName~DirectusEnvMaterializerTests"`
Expected: PASS — all env materializer tests green, including the two new ones. (If `PickFreePort_ReturnsPreferredWhenFree` breaks because it referenced the old range, it shouldn't — it passes `0`; but verify.)

- [ ] **Step 6: Update the three `.env.template` copies**

In each of these three files, make the same edits:
1. `scripts/local_directus/.env.template`
2. `dist/.VibeTable.Next.staging/local-directus/.env.template`
3. `dist/.RCPM.Next.staging/local-directus/.env.template`

Change the port line and add a HOST line. The current port block looks like (lines ~10-12):

```
# HTTP port the local Directus listens on. The VibeTable client connects via
# VIBETABLE_DIRECTUS_URL=http://localhost:${PORT}
PORT=8055
```

Replace with:

```
# HTTP port the local Directus listens on. High ephemeral range to avoid
# clashes with registered services. The VibeTable client connects via
# VIBETABLE_DIRECTUS_URL=http://127.0.0.1:${PORT}
HOST=127.0.0.1
PORT=49152
```

Note for the RCPM copy: its comment says `RCPM_DIRECTUS_URL=http://localhost:${PORT}`. Keep the `RCPM_` prefix in that copy's comment but change the host from `localhost` to `127.0.0.1` and update the port/PORT block identically. Do not change the `RCPM_` branding.

Then, in the `ADMIN_ENABLED=true` comment block at the bottom of each file, append the session-cookie keys. After the existing `ADMIN_ENABLED=true` line, add:

```
# Session cookie tuning for the host-injected admin auto-login. The host
# performs /auth/login(mode=session) and injects the resulting cookie into
# the WebView2 cookie jar before navigating to /admin/.
SESSION_COOKIE_TTL=7d
SESSION_COOKIE_SAME_SITE=lax
```

- [ ] **Step 7: Commit**

```bash
git add desktop/src/VibeTable.Infrastructure/Directus/DirectusEnvMaterializer.cs \
        desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusEnvMaterializerTests.cs \
        scripts/local_directus/.env.template \
        dist/.VibeTable.Next.staging/local-directus/.env.template \
        dist/.RCPM.Next.staging/local-directus/.env.template
git commit -m "feat(directus): bind loopback, high port, session cookie config"
```

---

## Task 2: DirectusSupervisor BaseUrl uses the env HOST

**Why:** Once `HOST=127.0.0.1` is materialised, `DirectusSupervisor.BaseUrl` should report `http://127.0.0.1:<port>` (not `http://localhost:<port>`). Later tasks compare against this string and set cookies for `127.0.0.1`. Minimal, behaviour-preserving change.

**Files:**
- Modify: `desktop/src/VibeTable.Infrastructure/Directus/DirectusSupervisor.cs`

**Interfaces:**
- Consumes: Task 1 (`values["HOST"]` is now present in the env passed to the supervisor).
- Produces: `BaseUrl` returns `http://127.0.0.1:<port>` when `HOST=127.0.0.1`.

- [ ] **Step 1: Write a failing test**

There is no existing `DirectusSupervisorTests` that checks `BaseUrl` construction without launching a process (the supervisor is process-heavy). Rather than spin up a fake Directus, this change is covered by the integration smoke test in Task 7. So this task has no new unit test — it is a one-line behavioural change verified by reading.

Add a TODO note as a code comment to make the verification path explicit (see Step 3). Mark this task's verification as "by inspection + Task 7 smoke".

- [ ] **Step 2: (No test run — skip; covered by Task 7)**

- [ ] **Step 3: Update BaseUrl construction in DirectusSupervisor.cs**

Find `DirectusSupervisor.cs` line 178 (inside `StartAsync`):

```csharp
            _baseUrl = $"http://localhost:{port}";
```

Replace with:

```csharp
            // HOST comes from the materialised .env (DirectusEnvMaterializer sets
            // 127.0.0.1). Use it so BaseUrl matches the cookie domain / nav target.
            string host = env.TryGetValue("HOST", out string? h) && !string.IsNullOrWhiteSpace(h)
                ? h
                : "localhost";
            _baseUrl = $"http://{host}:{port}";
```

The `env` parameter is the `IDictionary<string,string>` already passed into `StartAsync`; `HOST` is present after Task 1.

Also check the readiness-probe URL (`WaitForPingAsync(_baseUrl, ...)`) — it already uses `_baseUrl`, so it will probe `127.0.0.1` automatically. No further change.

Add a short XML doc note above the `BaseUrl` property (line 94) explaining the host source, e.g.:

```csharp
    /// <summary>
    /// The resolved base URL (<c>http://{HOST}:{PORT}</c>). HOST defaults to
    /// <c>127.0.0.1</c> (set by <see cref="DirectusEnvMaterializer"/>). The
    /// WebView2 admin nav target and the injected session cookie's Domain
    /// MUST use the same host string this URL reports.
    /// </summary>
    public string? BaseUrl => _baseUrl;
```

- [ ] **Step 4: Build to verify it compiles**

Run: `dotnet build desktop/src/VibeTable.Infrastructure/VibeTable.Infrastructure.csproj`
Expected: build succeeds.

- [ ] **Step 5: Commit**

```bash
git add desktop/src/VibeTable.Infrastructure/Directus/DirectusSupervisor.cs
git commit -m "feat(directus): BaseUrl reports the env HOST (127.0.0.1)"
```

---

## Task 3: DirectusAdminAuthenticator — session login + cookie extraction

**Why:** This is the testable core of the auto-login. Isolated from WebView2 so we can unit-test the HTTP flow with a fake handler. Produces the session cookie value that Task 5 injects.

**Files:**
- Create: `desktop/src/VibeTable.Desktop/Services/IDirectusAdminAuthenticator.cs`
- Create: `desktop/src/VibeTable.Desktop/Services/DirectusAdminAuthenticator.cs`
- Test: `desktop/tests/VibeTable.Desktop.Tests/DirectusAdminAuthenticatorTests.cs`

**Interfaces:**
- Consumes: `ADMIN_EMAIL`/`ADMIN_PASSWORD` from `DirectusEnvMaterializer`'s `.env` (the caller reads them and passes them in — the authenticator stays agnostic of the filesystem).
- Produces:
  ```csharp
  namespace VibeTable.Desktop.Services;

  public interface IDirectusAdminAuthenticator
  {
      /// <summary>
      /// Performs /auth/login with mode=session against the Directus admin
      /// user. Returns the value of the directus_session_token cookie on
      /// success, or null on any failure (non-2xx, missing cookie, network
      /// error). Never throws — callers treat null as "show host error page".
      /// </summary>
      Task<string?> LoginAsync(string baseUrl, string email, string password, CancellationToken ct);
  }
  ```
  Implementation class: `DirectusAdminAuthenticator` with constructor `public DirectusAdminAuthenticator(HttpMessageHandler? handler = null)`. When `handler` is null it uses the default `HttpClientHandler`; tests pass a fake.

- [ ] **Step 1: Write the failing tests**

Create `desktop/tests/VibeTable.Desktop.Tests/DirectusAdminAuthenticatorTests.cs`:

```csharp
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

        public FakeHandler(HttpStatusCode status, string? setCookieHeader = null, bool throwOnSend = false)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~DirectusAdminAuthenticatorTests"`
Expected: FAIL — compile error (types don't exist yet).

- [ ] **Step 3: Create the interface**

Create `desktop/src/VibeTable.Desktop/Services/IDirectusAdminAuthenticator.cs`:

```csharp
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
```

- [ ] **Step 4: Create the implementation**

Create `desktop/src/VibeTable.Desktop/Services/DirectusAdminAuthenticator.cs`:

```csharp
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~DirectusAdminAuthenticatorTests"`
Expected: PASS — all 4 tests green.

- [ ] **Step 6: Commit**

```bash
git add desktop/src/VibeTable.Desktop/Services/IDirectusAdminAuthenticator.cs \
        desktop/src/VibeTable.Desktop/Services/DirectusAdminAuthenticator.cs \
        desktop/tests/VibeTable.Desktop.Tests/DirectusAdminAuthenticatorTests.cs
git commit -m "feat(desktop): DirectusAdminAuthenticator for session-cookie auto-login"
```

---

## Task 4: Whitelist + dispatch `admin.openRequested`

**Why:** Adds the new web→host message that the web-grid's "Open Admin" button will send. Two gates: router whitelist + dispatcher switch.

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs`
- Modify: `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs`
- Test: `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs`

**Interfaces:**
- Consumes: nothing from Task 3 in this task (the authenticator is wired in Task 5).
- Produces: `admin.openRequested` is an accepted inbound type, routed directly to a host-side handler in `MainWindow` (navigation is a UI concern; the workspace dispatcher is NOT involved).

- [ ] **Step 1: Write the failing router-whitelist test**

In `desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs`, add (following the existing pattern in that file — construct the router with a capture callback):

```csharp
[TestMethod]
public void Route_AcceptsAdminOpenRequested()
{
    var dispatched = new System.Collections.Generic.List<RoutedWebRequest>();
    var router = new WebMessageRouter(req => dispatched.Add(req));
    router.MarkReady(); // host is ready (existing test helper, or set via app.ready)

    string json = """{"type":"admin.openRequested","requestId":"r1","payload":{}}""";
    var reply = router.Route(json);

    Assert.IsNull(reply, "admin.openRequested should be accepted, not rejected");
    Assert.AreEqual(1, dispatched.Count);
    Assert.AreEqual("admin.openRequested", dispatched[0].Type);
}
```

If `WebMessageRouterTests.cs` uses a different readiness helper name, match it — check the existing `Route_AcceptsAppReady` or similar test for the exact call. (The router exposes `MarkReady`/`IsReady`; if the existing tests set readiness by sending `app.ready` first, do that instead.)

- [ ] **Step 2: Run test to verify it fails**

Run: `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~Route_AcceptsAdminOpenRequested"`
Expected: FAIL — `reply` is an `operation.failed` (UNKNOWN_TYPE) because the type isn't whitelisted.

- [ ] **Step 3: Add the type to the router whitelist**

In `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs`, add to `WebRequestWhitelist` (lines 52-71). Insert a new comment block + entry after the table-admin entries:

```csharp
        // Table management (web sidebar).
        "tableAdmin.createRequested",
        "tableAdmin.deleteRequested",
        // Directus admin: open the embedded Data Studio in this webview.
        "admin.openRequested",
    };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~Route_AcceptsAdminOpenRequested"`
Expected: PASS.

- [ ] **Step 5: Route `admin.openRequested` to a host-side handler in MainWindow (NOT the dispatcher)**

The workspace dispatcher runs fire-and-forget on a thread-pool task and has no access to the WebView2 UI thread or the `CoreWebView2`. Navigation is a UI concern, so `admin.openRequested` is intercepted in `MainWindow.OnRoutedWebRequest` (line ~754) BEFORE it reaches the dispatcher.

In `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`, find `OnRoutedWebRequest`. Keep its existing `app.ready` handling intact, and add an early branch for the new type:

```csharp
    private void OnRoutedWebRequest(RoutedWebRequest request)
    {
        if (request.Type == "app.ready")
        {
            _router.MarkReady();
            // ... keep the EXISTING app.ready handling here unchanged ...
        }

        if (request.Type == "admin.openRequested")
        {
            // Navigation/cookie-injection is a UI concern handled in MainWindow.
            // Do NOT forward to _dispatcher.
            _ = OpenDirectusAdminAsync(request.RequestId);
            return;
        }

        _dispatcher.Dispatch(request);
    }
```

Add a stub for `OpenDirectusAdminAsync` so this commit builds (the real body is Task 5):

```csharp
    private async Task OpenDirectusAdminAsync(string? requestId)
    {
        await Task.CompletedTask; // implemented in Task 5
    }
```

Leave `WorkspaceRequestDispatcher.cs` unchanged — it does not get a new case.

- [ ] **Step 6: Build + run the router test**

Run: `dotnet build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj && dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj --filter "FullyQualifiedName~Route_AcceptsAdminOpenRequested"`
Expected: build OK; test PASS.

- [ ] **Step 7: Commit**

```bash
git add desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs \
        desktop/src/VibeTable.Desktop/MainWindow.xaml.cs \
        desktop/tests/VibeTable.Desktop.Tests/WebMessageRouterTests.cs
git commit -m "feat(desktop): whitelist admin.openRequested; route to host nav flow"
```

> Note: Step 5 changes `OnRoutedWebRequest` to reference `OpenDirectusAdminAsync` (stub now, real body in Task 5). This keeps each task independently buildable.

---

## Task 5: Widen navigation allowlist + session-cookie injection + admin nav

**Why:** This is the integration of Tasks 1-4: the host logs in, injects the cookie, and navigates the WebView2 to `/admin/`. Also widens `IsAppOrigin` and replaces the blanket `NewWindowRequested`.

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`

**Interfaces:**
- Consumes: `IDirectusAdminAuthenticator` (Task 3), `DirectusEnvMaterializer.TryReadBootstrapCredentials` (existing, reads `.env`), `_directusSupervisor.BaseUrl` (Task 2).
- Produces: a working "Open Admin" path end-to-end.

- [ ] **Step 1: Construct the authenticator and store it**

In `MainWindow.xaml.cs` ctor (near line 112 where `_webBridge`/`_router` are built), add:

```csharp
        _adminAuth = new DirectusAdminAuthenticator();
```

Add the field near the other private fields (around line 63):

```csharp
    private readonly IDirectusAdminAuthenticator _adminAuth;
```

- [ ] **Step 2: Widen `IsAppOrigin`**

Find `IsAppOrigin` (lines 1124-1147). Currently it accepts only `https://app.vibetable.local`. Replace its body to ALSO accept the running Directus origin. Because the allowed port is runtime-resolved (from `_directusSupervisor.BaseUrl`), `IsAppOrigin` can no longer be `static`. Change it to an instance method and parse the allowed admin origin from the supervisor:

```csharp
        private bool IsAppOrigin(string? uri)
        {
            if (string.IsNullOrEmpty(uri)) return false;
            if (!Uri.TryCreate(uri, UriKind.Absolute, out var u)) return false;

            // 1. The web-grid virtual host.
            if (string.Equals(u.Scheme, "https", StringComparison.OrdinalIgnoreCase)
                && string.Equals(u.Host, WebViewAssetService.AppHostName, StringComparison.OrdinalIgnoreCase))
            {
                return true;
            }

            // 2. The Directus admin origin (http + 127.0.0.1/localhost + the
            //    running Directus port). The port is runtime-resolved from the
            //    supervisor's BaseUrl.
            if (string.Equals(u.Scheme, "http", StringComparison.OrdinalIgnoreCase)
                && (string.Equals(u.Host, "127.0.0.1", StringComparison.OrdinalIgnoreCase)
                    || string.Equals(u.Host, "localhost", StringComparison.OrdinalIgnoreCase)))
            {
                int allowedPort = ResolveDirectusPort();
                if (allowedPort > 0 && u.Port == allowedPort)
                {
                    return true;
                }
            }

            return false;
        }

        private int ResolveDirectusPort()
        {
            string? baseUrl = _owner._directusSupervisor?.BaseUrl;
            if (baseUrl is null) return 0;
            try
            {
                return new Uri(baseUrl).Port;
            }
            catch (UriFormatException)
            {
                return 0;
            }
        }
```

Because `IsAppOrigin` was `static` and is referenced from `NavigationStarting`/`FrameNavigationStarting` lambdas (which are inside the nested `WebViewBridge` class — note `_owner` access), the change from `static` to instance is fine: the lambdas already capture the `WebViewBridge` instance. Just remove the `static` keyword and update the two call sites (`if (!IsAppOrigin(args.Uri))`). Verify both call sites compile (they're calling on `this`, which is the `WebViewBridge`, so `_owner.IsAppOrigin` — actually they call `IsAppOrigin` directly inside `WebViewBridge`, so it must be reachable. Simplest: keep `IsAppOrigin` on `MainWindow` itself and have the lambdas call `_owner.IsAppOrigin(...)`. Update both lambda bodies accordingly.)

- [ ] **Step 3: Replace the blanket `NewWindowRequested` with path discrimination**

Find the `NewWindowRequested` handler (lines 1079-1082):

```csharp
            core.NewWindowRequested += (_, args) =>
            {
                args.Handled = true;
            };
```

Replace with:

```csharp
            core.NewWindowRequested += (_, args) =>
            {
                // Cancel the new window in all cases; decide where the link goes.
                args.Handled = true;
                if (args.Uri is null || !Uri.TryCreate(args.Uri, UriKind.Absolute, out var u))
                {
                    return;
                }
                if (IsAppOrigin(args.Uri))
                {
                    // Same trusted origin (web-grid or Directus admin) — navigate
                    // the current webview there instead of opening a new window.
                    _owner.Dispatcher.Invoke(() => core.Navigate(u.AbsoluteUri));
                }
                else
                {
                    // External website — hand to the system default browser.
                    try
                    {
                        System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(u.AbsoluteUri)
                        {
                            UseShellExecute = true,
                        });
                    }
                    catch (Exception ex)
                    {
                        _owner._readinessWriter?.Trace(
                            $"NewWindowRequested: failed to launch system browser for '{u.AbsoluteUri}': {ex.Message}");
                    }
                }
            };
```

(Confirm `CoreWebView2NewWindowRequestedEventArgs.Uri` exists in the WebView2 version referenced — it does in current WebView2.)

- [ ] **Step 4: Implement `OpenDirectusAdminAsync` (replace the Task-4 stub)**

Replace the stub added in Task 4 with the real implementation:

```csharp
    private async Task OpenDirectusAdminAsync(string? requestId)
    {
        try
        {
            string? baseUrl = _directusSupervisor?.BaseUrl;
            if (string.IsNullOrEmpty(baseUrl))
            {
                _webBridge.PostOperationFailed(requestId,
                    "管理后台暂不可用：Directus 未启动。", "ADMIN_NOT_READY");
                return;
            }

            // Read the admin creds from the materialised .env. The supervisor
            // owns the runtime dir; reuse the same lookup it uses.
            string runtimeDir = _directusSupervisor!.Options.LocalDirectusDirectory;
            if (!DirectusEnvMaterializer.TryReadBootstrapCredentials(
                    runtimeDir, out string email, out string password))
            {
                _webBridge.PostOperationFailed(requestId,
                    "管理后台不可用：找不到管理员凭据。", "ADMIN_CREDENTIALS_MISSING");
                return;
            }

            string? cookie = await _adminAuth.LoginAsync(baseUrl, email, password, default)
                .ConfigureAwait(true);
            if (cookie is null)
            {
                _webBridge.PostOperationFailed(requestId,
                    "管理后台登录失败，请稍后重试。", "ADMIN_LOGIN_FAILED");
                return;
            }

            // Inject the session cookie into the webview's cookie jar before nav.
            var core = WebView.CoreWebView2;
            if (core is null)
            {
                _webBridge.PostOperationFailed(requestId,
                    "管理后台不可用：WebView 未就绪。", "WEBVIEW_NOT_READY");
                return;
            }
            var cm = core.CookieManager;
            // The navigated URL uses the host from BaseUrl (127.0.0.1).
            var host = new Uri(baseUrl).Host;
            var c = cm.CreateCookie("directus_session_token", cookie, host, "/");
            cm.AddOrUpdateCookie(c);

            _readinessWriter?.Trace($"OpenDirectusAdminAsync: navigating to admin at {baseUrl}");
            core.Navigate(baseUrl.TrimEnd('/') + "/admin/");
        }
        catch (Exception ex)
        {
            _webBridge.PostOperationFailed(requestId,
                $"管理后台打开失败：{ex.Message}", "ADMIN_OPEN_ERROR");
        }
    }
```

**Verify these assumptions against the actual code before finalising** (the engineer implementing should confirm):
- `_directusSupervisor.Options.LocalDirectusDirectory` — confirm `DirectusSupervisor` exposes its options / runtime dir. If not, the runtime dir is whatever was passed to `DirectusEnvMaterializer.Materialize` at startup; expose it or capture it in a MainWindow field during `EnsureLocalDirectusAsync`. Adjust accordingly.
- `_adminAuth.LoginAsync(baseUrl, email, password, default)` — `ConfigureAwait(true)` because we then touch UI (`WebView.CoreWebView2`, navigate). Good.
- `cm.CreateCookie(name, value, Domain, Path)` — the WebView2 signature is `CreateCookie(string name, string value, string Domain, string Path)`. Domain here is the host (`127.0.0.1`), no leading dot.

- [ ] **Step 5: Build**

Run: `dotnet build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`
Expected: build succeeds. Fix any `static`→instance call-site issues in the two navigation lambdas.

- [ ] **Step 6: Commit**

```bash
git add desktop/src/VibeTable.Desktop/MainWindow.xaml.cs
git commit -m "feat(desktop): widen nav allowlist, discriminate new-window, inject admin session cookie"
```

---

## Task 6: web-grid — "Open Admin" button + contract type

**Why:** Adds the user-facing affordance and the TS contract for the new message.

**Files:**
- Modify: `desktop/web-grid/src/contracts.ts`
- Modify: the web-grid toolbar component (locate the existing toolbar/header in `desktop/web-grid/src/`)

**Interfaces:**
- Consumes: Task 4 (the host now accepts `admin.openRequested`).
- Produces: a button that posts `{ type: "admin.openRequested" }`.

- [ ] **Step 1: Add the type to `WebMessageType`**

In `desktop/web-grid/src/contracts.ts`, find `WebMessageType` (around line 543) and add a new entry (keep alphabetical-ish / group with table-admin):

```ts
  // Table-admin requests.
  | "tableAdmin.createRequested"
  | "tableAdmin.deleteRequested"
  // Open the embedded Directus admin (Data Studio) in this webview.
  | "admin.openRequested";
```

Also add to `WebPayloadMap` (around line 637) so the type is typed:

```ts
  // Table-admin requests.
  "tableAdmin.createRequested": TableAdminCreatePayload;
  "tableAdmin.deleteRequested": TableAdminDeletePayload;
  // Open Directus admin. Empty payload.
  "admin.openRequested": Record<string, never>;
```

- [ ] **Step 2: Add the button**

Locate the toolbar/header component in `desktop/web-grid/src/` (search for where `tableAdmin.createRequested` is sent — that's the table-management sidebar; an "Open Admin" button belongs in the same top-level toolbar, not inside the table admin dialog). Find the existing `postMessage`/bridge send helper (the function that builds `{ type, requestId, payload }` and calls `window.chrome.webview.postMessage`).

Add a button whose click handler posts the new message. Exact JSX/TS depends on the framework (the web-grid is plain TS + Tabulator — find the toolbar render). A minimal addition:

```ts
function openAdmin() {
  postBridgeMessage({ type: "admin.openRequested", requestId: nextRequestId(), payload: {} });
}
```

Wire `openAdmin` to a button in the toolbar HTML/TS. Use a clear label and title, e.g. label "管理后台", title "打开 Directus 管理后台". Place it where the table-admin controls live.

- [ ] **Step 3: Build the web-grid**

Run: `cd desktop/web-grid && npm run build`
Expected: build succeeds; `tsc` does not complain about the new type.

- [ ] **Step 4: Commit**

```bash
git add desktop/web-grid/src/contracts.ts desktop/web-grid/src/<toolbar-file>.ts
git commit -m "feat(web-grid): add Open Admin button + admin.openRequested contract"
```

---

## Task 7: Smoke / integration verification

**Why:** The end-to-end flow (Directus loopback + admin nav + cookie) can't be fully unit-tested. This task runs the existing smoke harness and manually confirms the spec's security invariants.

**Files:**
- No code changes (verification only). Update the smoke-test notes if a gap is found.

- [ ] **Step 1: Run all unit tests**

Run:
```
dotnet test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj
dotnet test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj
```
Expected: all green.

- [ ] **Step 2: Run the Directus smoke test (if the repo has one)**

Find the existing Directus smoke test (referenced in `MainWindow.xaml.cs` ProcessFailed comments — `--test-mode`). Run it per the repo's existing instructions. Confirm:
- Directus starts on a port in `49152..49201`.
- `BaseUrl` reports `http://127.0.0.1:<port>`.
- `GET /server/ping` answers 200.

- [ ] **Step 3: Manually verify the loopback bind**

While Directus is running, from a shell:
```
# Should succeed (loopback):
curl http://127.0.0.1:<port>/server/ping
# Confirm the listening socket is 127.0.0.1, not 0.0.0.0:
netstat -ano | findstr <port>
```
Expected: the listen line shows `127.0.0.1:<port>`, NOT `0.0.0.0:<port>`. (On Windows, `netstat -ano | findstr :<port>`.)

- [ ] **Step 4: Manually verify the admin auto-login (full flow)**

Launch the desktop app (debug build keeps devtools). Open the web-grid. Click "管理后台". Confirm:
- The webview navigates to `http://127.0.0.1:<port>/admin/` and shows the Directus admin dashboard (NOT the login page) — the session cookie injection worked.
- DevTools → Application → Cookies shows `directus_session_token` for `127.0.0.1`.
- A WS connection to `ws://127.0.0.1:<port>/websocket` is established (Network tab) — realtime works without a proxy.

- [ ] **Step 5: Manually verify navigation gating**

In the admin, click a link that would open a new window to an external site. Confirm it opens in the system default browser, not inside the webview. Confirm an in-admin same-origin link navigates the current webview.

- [ ] **Step 6: If any invariant fails, file the fix as a follow-up task (do not silently patch)**

Record results in the commit message of the next change, or open an issue.

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** §4.1 env keys → Task 1. §4.2 `IsAppOrigin` widen → Task 5 Step 2. §4.3 `NewWindowRequested` discrimination → Task 5 Step 3. §4.4 session login + cookie → Tasks 3+5. §4.5 web-grid button → Task 6. §6 error handling → Task 5's `PostOperationFailed` branches. §7 testing → Tasks 1,3,4 unit + Task 7 smoke.
- **Type consistency:** `IDirectusAdminAuthenticator.LoginAsync(baseUrl, email, password, ct)` — same signature in Task 3 interface, Task 3 impl, Task 5 call site. `admin.openRequested` string identical in Task 4 whitelist, Task 4 MainWindow branch, Task 6 contracts.
- **Open questions from spec §10:** (1) `SESSION_COOKIE_TTL` format resolved → `"7d"` (ms-style string, confirmed via Directus Security & Limits docs). (2) cookie Domain host → use `new Uri(baseUrl).Host` (`127.0.0.1`), matching the navigated URL. (3) button placement → toolbar, near table-admin controls (Task 6 Step 2).
```
