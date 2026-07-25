> 历史设计归档；不属于当前产品实现。

# Directus Admin in WebView — Loopback + Session-Cookie Auto-Login

**Date:** 2026-07-17
**Status:** Design (pending implementation plan)
**Owner:** Felix Ji-13980K

---

## 1. Goal & Non-Goals

### Goal

Let the user open **Directus's own admin UI** ("Data Studio") from inside the VibeTable
WebView2, with the host logging them in automatically (no password typing), while
restricting Directus to **loopback-only** so no other machine on the LAN can reach it.

### Non-Goals (explicitly out of scope)

- **No hard denial of the Directus admin UI / API to local processes.** The user
  accepted that any loopback-bound service is callable by other processes on the same
  machine. "Restrict to local" means `127.0.0.1` binding — not a token gate, not
  `SERVE_APP=false`, not a reverse-proxy auth layer.
- **No reverse proxy.** Earlier iterations explored a Kestrel/YARP proxy to embed both
  the web-grid and Directus admin under one origin. That was rejected as over-engineering
  once the user decided cross-origin navigation switching in a single WebView2 is
  acceptable (see §3).
- **No Python BFF changes.** The BFF keeps talking to Directus over
  `VIBETABLE_DIRECTUS_URL` exactly as today.

---

## 2. Background — Why the obvious ideas don't work

Captured here so we don't re-litigate them. Each was investigated against Directus 12
source / WebView2 docs.

### 2.1 WebView2 interception cannot proxy a virtual-host origin

- `SetVirtualHostNameToFolderMapping` serves **static files from a folder only** — no
  dynamic generation, no proxy hook.
- `AddWebResourceRequestedFilter` **does not fire** for URLs served by the virtual-host
  mapping (official MS how-to). So "keep the vhost for the grid + intercept `/admin/*`"
  is impossible.
- **No WebView2 API can intercept/proxy WebSocket traffic.** The WS upgrade is handled
  internally by Chromium. Directus realtime needs WS. Therefore any design that relies
  on intercepting at the WebView2 layer is dead on arrival for the admin app.

**Conclusion:** the webview must reach Directus via a **real HTTP server** that serves
WS natively. Directus itself is that server — no proxy needed if the webview navigates
to it directly.

### 2.2 Directus 12 admin SPA does NOT auto-login from a URL token

Investigated against `app/src/routes/login/login.vue`, `app/src/auth.ts`,
`app/src/sdk.ts`, `app/src/router.ts`:

- The SPA is hard-wired to `authentication('session', { credentials: 'include' })`.
  It never calls `staticToken()` or reads an `access_token` query param.
- `?access_token=<token>` only authenticates a single REST/GraphQL request
  (`api/src/middleware/extract-token.ts`); it does **not** establish a session and the
  SPA ignores it.
- There is no `ADMIN_STATIC_TOKEN` env var. The real one is `ADMIN_TOKEN` (singular),
  set only at bootstrap on the first admin user's `directus_users.token`. It does not
  log the SPA in.

**Conclusion:** auto-login must be done by **injecting the session cookie** into the
webview, not by a URL parameter. This is feasible because WebView2 exposes a
programmatic `CoreWebView2CookieManager` — the one capability the embedded webview has
that an external browser does not.

### 2.3 CORS / CSP / cookie-secure flags do NOT block a local browser from loading admin

- `CORS_ORIGIN` only governs cross-origin API calls, not top-level navigation. Opening
  `localhost:8055/admin` in Chrome is same-origin; CORS never applies.
- `CONTENT_SECURITY_POLICY` restricts what a loaded document may do; it cannot prevent
  the document from loading and cannot distinguish "external Chrome" from "WebView2".
- `SESSION_COOKIE_SECURE=true` would break HTTP login for everyone (including our
  webview), not selectively.

**Conclusion:** none of these are a "block local browser" lever. The only real lever
is `HOST=127.0.0.1` (bind) — and even that only stops other machines, not local
processes. We accept that.

### 2.4 Current Directus binding is `0.0.0.0` (a latent exposure)

Neither `.env.template` nor `DirectusEnvMaterializer` sets `HOST`, and Directus 12
defaults to `HOST=0.0.0.0`. So Directus today listens on **all network interfaces**,
reachable from the LAN if the firewall allows. This spec closes that hole.

---

## 3. Architecture

A single WebView2 instance navigates between two origins at runtime. No proxy.

```
WebView2 (single CoreWebView2 instance)
  ├─ default:  https://app.vibetable.local/index.html   (web-grid, vhost mapping, UNCHANGED)
  └─ "Open Admin" button:
       host injects session cookie  →  core.Navigate("http://127.0.0.1:<port>/admin/")
       (Directus serves admin UI + REST + WS natively on its own port)
  └─ "Back":   core.Navigate("https://app.vibetable.local/index.html")

Directus process: HOST=127.0.0.1, PORT=<49152..49201>, SERVE_APP=true (default)
```

### Why this is fine

- A WebView2 is Chromium; `core.Navigate(url)` to any origin works.
- The vhost mapping registration persists across navigations to other origins
  (MS-confirmed); navigating back to `app.vibetable.local` keeps working without
  re-registration.
- WS from the admin app goes directly to Directus on its real port — no interception,
  no proxying, no WebView2 limitation bites.
- The cost is that web-grid state (localStorage/JS heap) is lost when navigating to
  admin and back. Accepted for an "occasionally peek at admin" workflow.

### Why we rejected the alternatives

| Alternative | Why rejected |
|---|---|
| Kestrel reverse proxy (both apps same origin) | Adds a whole proxy layer + WS forwarding to avoid a page reload. Not worth it given the admin is a secondary view. |
| Dedicated second WebView2 window for admin | Works, but the user explicitly wanted admin reachable from inside the main webview, not a popup. |
| `SERVE_APP=false` + Kestrel serves admin static | Belongs to the "hard denial" design that the user deprioritised. |
| External browser launch | Cookie can't be injected cross-process → user would have to type the random `ADMIN_PASSWORD`. Rejected. |

---

## 4. Component Changes

### 4.1 `DirectusEnvMaterializer.cs` (+ `.env.template`)

Add three env values to the materialised `.env` and the template:

| Key | Value | Reason |
|---|---|---|
| `HOST` | `127.0.0.1` | Bind loopback. Closes the current `0.0.0.0` exposure (§2.4). |
| `PORT` (default) | `49152` | Move off the well-known `8055`. IANA ephemeral range, unlikely to clash with registered services. |
| `SESSION_COOKIE_TTL` | long (e.g. `"30d"` — exact string per Directus docs) | So the injected session survives a long-running app session without forcing re-login mid-use. |
| `SESSION_COOKIE_SAME_SITE` | `lax` | Default is `strict`; `lax` is safer against cross-site edge cases while still working on `localhost`. Revisit if cookie is dropped. |

Port probe range becomes `49152..49201` (`PortProbeRangeStart = 49152`,
`PortProbeRangeEnd = 49152 + 50`). Logic otherwise unchanged.

`GeneratedKeys` stays `{ KEY, SECRET, ADMIN_PASSWORD }` — **no** static token is
generated. Auto-login uses `ADMIN_PASSWORD` via session login (§5).

### 4.2 `MainWindow.xaml.cs` — navigation gating

`IsAppOrigin` currently accepts only `https://app.vibetable.local`. Widen it to **also**
accept the Directus admin origin: scheme `http`, host `127.0.0.1` (and `localhost`),
port == the running Directus port (resolved at runtime from the supervisor, not
hardcoded). Keep rejecting everything else (`data:`, `blob:`, `file:`, other hosts).

Apply the same widening to **both** `NavigationStarting` and `FrameNavigationStarting`
handlers (existing pattern).

### 4.3 `MainWindow.xaml.cs` — `NewWindowRequested` path discrimination

Currently every `NewWindowRequested` is set `Handled = true` (silent swallow). Under
Directus admin this would break any in-admin link that targets a new window. Replace
the blanket handler with discrimination on `args.Uri`:

- **Same-host link** (host is `127.0.0.1` / `localhost`, or `app.vibetable.local`):
  cancel the new window (`Handled = true`) **and** navigate the current webview to that
  URI (`core.Navigate(args.Uri)`). Keeps navigation in-app.
- **Different-host link** (anything else — an external website the admin links to):
  cancel the new window (`Handled = true`) **and** launch the system default browser
  via `Process.Start(new ProcessStartInfo(uri) { UseShellExecute = true })`.
- Unparseable / null URI: `Handled = true` (keep current safe default).

### 4.4 Session-cookie auto-login (new helper)

Before navigating to admin, the host establishes a Directus session and injects the
cookie. New method (placement: alongside the existing webview bridge code):

```
async Task<bool> EnsureDirectusAdminSessionAsync(CoreWebView2 core, CancellationToken ct)
{
    1. Read ADMIN_EMAIL / ADMIN_PASSWORD from the materialised .env
       (DirectusEnvMaterializer already owns this file; reuse its parse).
    2. POST http://127.0.0.1:<port>/auth/login
       body JSON: { "email": ..., "password": ..., "mode": "session" }
       (Use HttpClient; trust the loopback endpoint — no TLS needed.)
    3. On 200, capture Set-Cookie: directus_session_token=<value>
       (also directus_refresh_token if present).
    4. Via core.CookieManager: CreateCookie(name, value, Domain="127.0.0.1", Path="/")
       for each captured cookie, set Secure/SameSite to match SESSION_COOKIE_* values,
       then AddOrUpdateCookie(...).
    5. Return true. On non-200 / exception, return false (caller shows host error page).
}
```

The webview then navigates to `http://127.0.0.1:<port>/admin/`. The SPA's first-load
`/auth/refresh` call reads the session cookie via `extract-token.ts` and hydrates as
authenticated — the login form is skipped.

### 4.5 web-grid — "Open Admin" button

Add a UI affordance in the web-grid that posts a host message (existing
`window.chrome.webview` bridge) asking the host to open the Directus admin. New message
type in `desktop/web-grid/src/contracts.ts`, e.g. `admin.openRequested` (web → host).

The host handler:
1. Ensures Directus is running and ready (existing readiness path).
2. Calls `EnsureDirectusAdminSessionAsync`.
3. On success, `core.Navigate(adminUrl)`.
4. On failure, posts an `operation.failed`-style message back so the grid shows a host
   error (not the Directus login page — that would leak the admin surface on failure).

"Back" navigation from admin to the grid is a normal webview navigation to
`https://app.vibetable.local/index.html` — it does not need a new message; it can be a
link/button the admin renders, or a host-owned chrome button. (Open question for
implementation: where exactly the "back" affordance lives. Not load-bearing for the
spec.)

---

## 5. Data Flow — Opening Admin

```
user clicks "Open Admin" in web-grid
  → postMessage { type: "admin.openRequested" }
host receives
  → reads ADMIN_EMAIL / ADMIN_PASSWORD from .env
  → POST 127.0.0.1:<port>/auth/login  (mode: session)
  → Set-Cookie: directus_session_token=<v>
  → WebView2 CookieManager.AddOrUpdateCookie(...)
  → core.Navigate("http://127.0.0.1:<port>/admin/")
webview
  → admin SPA boots, calls /auth/refresh
  → reads session cookie → authenticated
  → renders admin UI, opens WS to /websocket (direct to Directus, no proxy)
```

Failure modes (§6).

---

## 6. Error Handling

| Scenario | Behaviour |
|---|---|
| Directus not running / not ready when "Open Admin" clicked | Host waits on existing readiness probe; on timeout, posts `operation.failed` to grid with "管理后台暂不可用，Directus 启动中". |
| `/auth/login` returns non-200 (wrong creds, DB locked, etc.) | Post `operation.failed`; do NOT navigate (avoids showing the Directus login page). |
| Login succeeds but SPA's `/auth/refresh` 401s (cookie mis-set) | Fallback: navigate anyway; if the SPA shows its login page, treat as a known degraded mode. Mitigation: verify cookie domain/path match `SESSION_COOKIE_*`; set `SESSION_COOKIE_SAME_SITE=lax`. |
| Session expires mid-use (long-running admin session) | SPA itself will redirect to its login route. Out of scope for v1: user closes admin, re-opens via "Open Admin" → host re-logins. (Long `SESSION_COOKIE_TTL` makes this rare.) |
| Kestrel — N/A | No proxy in this design. |
| Directus port bind fails (whole 49152..49201 occupied) | Existing `PickFreePort` throws → ViewModel moves to `Faulted` (unchanged behaviour). |
| webview navigates to an unapproved origin | `IsAppOrigin` gate cancels (unchanged behaviour, widened allowlist). |

---

## 7. Testing Strategy

### Unit (existing `VibeTable.Infrastructure.Tests`)

- `DirectusEnvMaterializerTests` — assert the materialised `.env` now contains
  `HOST=127.0.0.1`, `PORT=49152` (or a free port in range), `SESSION_COOKIE_TTL=<long>`,
  `SESSION_COOKIE_SAME_SITE=lax`. Assert `GeneratedKeys` is unchanged (no static token).
- Port probe range: existing port-conflict test updated for the new range
  `49152..49201`.

### Unit (new, desktop side)

- `EnsureDirectusAdminSessionAsync` — mock `HttpClient` (return a 200 with a
  `Set-Cookie` header) and a fake `CoreWebView2CookieManager`; assert the right cookie
  is created with the right domain/path. Cover the non-200 → returns-false path.
- `IsAppOrigin` widening — assert `http://127.0.0.1:<port>/admin/` accepted,
  `http://127.0.0.1:<other-port>/` rejected, `https://app.vibetable.local/...`
  still accepted, `http://example.com` rejected.
- `NewWindowRequested` discrimination — assert same-host URI → current-webview
  navigate; different-host URI → system browser; null/unparseable → swallowed.

### Integration / smoke (existing Directus smoke harness)

- Extend the smoke test: after Directus is up, assert `GET /admin/` returns 200
  (admin UI served, `SERVE_APP=true` still works).
- Assert a `curl http://<lan-ip>:<port>/server/ping` from a second interface is
  refused (loopback binding holds). If the smoke env has no second interface, at
  least assert the listening endpoint reports `127.0.0.1`, not `0.0.0.0`.

---

## 8. Security Posture — explicit

This design is **deliberately not a hard denial** of the Directus admin/API to local
processes. The user accepted that loopback services are callable by any process on the
machine. What this design **does** guarantee:

- **Other machines on the LAN cannot reach Directus** (`HOST=127.0.0.1`). Closes the
  current `0.0.0.0` exposure.
- **The user does not see or type the admin password** in the common case (cookie
  auto-login). The random `ADMIN_PASSWORD` stays in the gitignored `.env`.
- **The webview's navigation surface stays bounded** — only `app.vibetable.local` and
  `http://127.0.0.1:<directus-port>` are reachable; everything else is cancelled.

What it **does not** guarantee (accepted trade-offs):

- A local process that knows the port can call `/auth/login` with the admin password
  if it can read `.env` (file permissions are the boundary, not this design).
- A local browser that opens `http://127.0.0.1:<port>/admin/` directly will see the
  Directus login page. This is accepted; hard-blocking it was rejected as
  disproportionate (§1, §2.3).

---

## 9. Out of Scope / Future

- **Hard denial of admin UI to local browsers** (would require `SERVE_APP=false` +
  proxy/static-file takeover). Documented as a future option if the threat model
  changes.
- **Session refresh on expiry** without reopening admin (host-side cookie refresh
  loop). Defer until the long-TTL path proves insufficient.
- **A "Back to web-grid" affordance rendered inside admin** (host-owned chrome button
  vs SPA-injected link). Decide at implementation time.
- **Admin deep-linking** (open admin straight to a specific collection/table). The
  navigation target can be any `/admin/<path>`; the cookie injection is path-agnostic.

---

## 10. Open Questions for Implementation

1. Exact `SESSION_COOKIE_TTL` string format accepted by Directus 12 (e.g. `"30d"` vs
   ms). Verify against `packages/env/src/constants/defaults.ts` during implementation.
2. Whether `127.0.0.1` vs `localhost` as the cookie `Domain` matters for the SPA's
   `fetch` credentials. Decide based on which host the navigated URL uses; keep them
   consistent (navigate to the same host the cookie is set on).
3. Where the "Open Admin" button lives in the web-grid UI (toolbar? menu?). UI choice;
   not load-bearing for the spec.
