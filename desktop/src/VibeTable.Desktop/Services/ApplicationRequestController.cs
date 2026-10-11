using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal interface IApplicationRequestHost
{
    void ApplyPreferences(AppPreferences preferences);

    void EnsureTrayIcon();

    void RequestExit();

    void Trace(string message);
}

/// <summary>
/// Owns renderer-driven desktop preference, release update, and daily quote
/// requests. WPF supplies only native lifecycle effects; validation, state,
/// cancellation, result projection, and stable errors stay inside this seam.
/// </summary>
internal sealed class ApplicationRequestController : IDisposable
{
    private readonly IWebReplySink _reply;
    private readonly IApplicationRequestHost _host;
    private readonly AppPreferencesService _preferences;
    private readonly ReleaseUpdateCoordinator _updates;
    private readonly DailyQuoteHostClient _dailyQuotes;
    private readonly HostAppearanceService _appearance;
    private readonly Func<CancellationToken> _sessionToken;

    public ApplicationRequestController(
        IWebReplySink reply,
        IApplicationRequestHost host,
        AppPreferencesService preferences,
        ReleaseUpdateCoordinator updates,
        DailyQuoteHostClient dailyQuotes,
        HostAppearanceService appearance,
        AppPreferences initialPreferences,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _preferences = preferences ?? throw new ArgumentNullException(nameof(preferences));
        _updates = updates ?? throw new ArgumentNullException(nameof(updates));
        _dailyQuotes = dailyQuotes ?? throw new ArgumentNullException(nameof(dailyQuotes));
        _appearance = appearance ?? throw new ArgumentNullException(nameof(appearance));
        CurrentPreferences = initialPreferences
            ?? throw new ArgumentNullException(nameof(initialPreferences));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
    }

    public AppPreferences CurrentPreferences { get; private set; }

    public static bool Handles(string requestType)
        => requestType is
            "appPreferences.get" or
            "appPreferences.update" or
            "update.check" or
            "update.install" or
            "dailyQuote.fetch";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "appPreferences.get" => GetPreferencesAsync(request),
            "appPreferences.update" => UpdatePreferencesAsync(request),
            "update.check" => CheckForReleaseUpdateAsync(request),
            "update.install" => InstallReleaseUpdateAsync(request),
            "dailyQuote.fetch" => FetchDailyQuoteAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private Task GetPreferencesAsync(RoutedWebRequest request)
    {
        if (!HasEmptyObjectPayload(request))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The application preferences request is invalid.",
                "APP_PREFERENCES_BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        try
        {
            CurrentPreferences = _preferences.Read();
            _host.ApplyPreferences(CurrentPreferences);
            PostPreferences(request);
        }
        catch (Exception exception)
        {
            TraceUnexpectedFailure("Application preferences read", exception);
            _reply.PostOperationFailed(
                request.RequestId,
                "无法读取桌面应用设置。",
                "APP_PREFERENCES_READ_FAILED");
        }
        return Task.CompletedTask;
    }

    private Task UpdatePreferencesAsync(RoutedWebRequest request)
    {
        if (!TryReadAppPreferencesPatch(request.Payload, out AppPreferencesPatch? patch)
            || patch is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The application preferences update is invalid.",
                "APP_PREFERENCES_BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        try
        {
            if (patch.MinimizeToTrayOnClose is true)
                _host.EnsureTrayIcon();
            CurrentPreferences = patch.HasAdoptLegacyTheme
                ? _preferences.AdoptLegacyTheme(patch.AdoptLegacyTheme!)
                : _preferences.Update(patch);
            _appearance.SetPreferredTheme(CurrentPreferences.Theme);
            _host.ApplyPreferences(CurrentPreferences);
            PostPreferences(request);
        }
        catch (Exception exception)
        {
            TraceUnexpectedFailure("Application preferences update", exception);
            _reply.PostOperationFailed(
                request.RequestId,
                "无法保存桌面应用设置，请检查当前用户权限后重试。",
                "APP_PREFERENCES_WRITE_FAILED");
        }
        return Task.CompletedTask;
    }

    private async Task FetchDailyQuoteAsync(RoutedWebRequest request)
    {
        if (!DailyQuoteHostClient.TryParseRequest(
                request.Payload,
                out DailyQuoteHostRequest? quoteRequest)
            || quoteRequest is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The daily quote request is invalid.",
                "DAILY_QUOTE_BAD_PAYLOAD");
            return;
        }
        CancellationToken token = _sessionToken();
        try
        {
            DailyQuoteHostResult result = await _dailyQuotes.FetchAsync(
                quoteRequest,
                token).ConfigureAwait(false);
            _reply.PostResponse("dailyQuote.fetch", request.RequestId, result);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (DailyQuoteHostException exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception exception)
        {
            _host.Trace(
                $"Daily quote request failed; exception={exception.GetType().Name}");
            _reply.PostOperationFailed(
                request.RequestId,
                "The daily quote provider is unavailable.",
                "DAILY_QUOTE_UNAVAILABLE");
        }
    }

    private async Task CheckForReleaseUpdateAsync(RoutedWebRequest request)
    {
        if (!HasEmptyObjectPayload(request))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The update check request is invalid.",
                "UPDATE_BAD_PAYLOAD");
            return;
        }
        CancellationToken token = _sessionToken();
        try
        {
            CurrentPreferences = _preferences.Read();
            ReleaseUpdateCheckResult result = await _updates.CheckAsync(
                CurrentPreferences,
                token);
            _reply.PostResponse(
                request.Type,
                request.RequestId,
                new
                {
                    currentVersion = result.CurrentVersion,
                    latestVersion = result.LatestVersion,
                    updateAvailable = result.UpdateAvailable,
                    canInstall = result.CanInstall,
                    installUnavailableReason = result.InstallUnavailableReason,
                    downloadBytes = result.DownloadBytes,
                    releaseUrl = result.ReleaseUrl,
                    notesTruncated = result.NotesTruncated,
                    releases = result.Releases.Select(note => new
                    {
                        version = note.Version,
                        title = note.Title,
                        body = note.Body,
                        publishedAt = note.PublishedAt,
                        releaseUrl = note.ReleaseUrl,
                    }),
                });
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (ReleaseUpdateException exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception exception)
        {
            TraceUnexpectedFailure("Release update check", exception);
            _reply.PostOperationFailed(
                request.RequestId,
                "无法连接 GitHub 检查更新，请稍后重试。",
                "UPDATE_CHECK_FAILED");
        }
    }

    private async Task InstallReleaseUpdateAsync(RoutedWebRequest request)
    {
        if (!HasEmptyObjectPayload(request))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The update install request is invalid.",
                "UPDATE_BAD_PAYLOAD");
            return;
        }
        CancellationToken token = _sessionToken();
        try
        {
            await _updates.LaunchUpdateAsync(token);
            _reply.PostResponse(
                request.Type,
                request.RequestId,
                new { status = "restarting" });
            _host.RequestExit();
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (ReleaseUpdateException exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception exception)
        {
            TraceUnexpectedFailure("Release update install", exception);
            _reply.PostOperationFailed(
                request.RequestId,
                "更新包下载或暂存失败，现有程序与用户数据均未更改。",
                "UPDATE_INSTALL_FAILED");
        }
    }

    internal static bool TryReadAppPreferencesPatch(
        JsonElement payload,
        out AppPreferencesPatch? patch)
    {
        patch = null;
        if (payload.ValueKind != JsonValueKind.Object)
            return false;
        bool? minimizeToTrayOnClose = null;
        bool? startWithWindows = null;
        string? updateProxy = null;
        string? customUpdateProxyUrl = null;
        string? theme = null;
        string? adoptLegacyTheme = null;
        bool sawMinimize = false;
        bool sawStartup = false;
        bool sawUpdateProxy = false;
        bool sawCustomUpdateProxyUrl = false;
        bool sawTheme = false;
        bool sawAdoptLegacyTheme = false;
        foreach (JsonProperty property in payload.EnumerateObject())
        {
            if (property.NameEquals("minimizeToTrayOnClose"))
            {
                if (sawMinimize || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                    return false;
                sawMinimize = true;
                minimizeToTrayOnClose = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("startWithWindows"))
            {
                if (sawStartup || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                    return false;
                sawStartup = true;
                startWithWindows = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("updateProxy"))
            {
                if (sawUpdateProxy || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawUpdateProxy = true;
                updateProxy = property.Value.GetString();
                if (updateProxy is null || !UpdateProxyOptions.IsKnown(updateProxy))
                    return false;
                continue;
            }
            if (property.NameEquals("customUpdateProxyUrl"))
            {
                if (sawCustomUpdateProxyUrl
                    || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawCustomUpdateProxyUrl = true;
                customUpdateProxyUrl = property.Value.GetString();
                if (customUpdateProxyUrl is null || customUpdateProxyUrl.Length > 2048)
                    return false;
                continue;
            }
            if (property.NameEquals("theme"))
            {
                if (sawTheme || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawTheme = true;
                theme = property.Value.GetString();
                if (theme is null || !AppThemeOptions.IsKnown(theme))
                    return false;
                // A direct choice and the one-time legacy adoption are
                // mutually exclusive: mixing them could overwrite an
                // explicit Host value through the compare-unset path.
                if (sawAdoptLegacyTheme)
                    return false;
                continue;
            }
            if (property.NameEquals("adoptLegacyTheme"))
            {
                if (sawAdoptLegacyTheme || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawAdoptLegacyTheme = true;
                adoptLegacyTheme = property.Value.GetString();
                if (adoptLegacyTheme is null || !AppThemeOptions.IsKnown(adoptLegacyTheme))
                    return false;
                if (sawTheme)
                    return false;
                continue;
            }
            return false;
        }
        if (!sawMinimize && !sawStartup && !sawUpdateProxy && !sawCustomUpdateProxyUrl
            && !sawTheme && !sawAdoptLegacyTheme)
            return false;
        patch = new AppPreferencesPatch(
            minimizeToTrayOnClose,
            startWithWindows,
            updateProxy,
            customUpdateProxyUrl,
            sawCustomUpdateProxyUrl,
            theme,
            sawTheme,
            adoptLegacyTheme,
            sawAdoptLegacyTheme);
        return true;
    }

    private void PostPreferences(RoutedWebRequest request)
        => _reply.PostResponse(
            request.Type,
            request.RequestId,
            new
            {
                minimizeToTrayOnClose = CurrentPreferences.MinimizeToTrayOnClose,
                startWithWindows = CurrentPreferences.StartWithWindows,
                updateProxy = CurrentPreferences.UpdateProxy,
                customUpdateProxyUrl = CurrentPreferences.CustomUpdateProxyUrl ?? "",
                theme = CurrentPreferences.Theme,
                effectiveTheme = _appearance.Projection.Effective,
                highContrast = _appearance.Projection.HighContrast,
            });

    private void TraceUnexpectedFailure(string operation, Exception exception)
        => _host.Trace($"{operation} failed; exception={exception.GetType().Name}");

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(
            request.RequestId,
            "应用请求类型无效。",
            "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private static bool HasEmptyObjectPayload(RoutedWebRequest request)
        => request.Payload.ValueKind == JsonValueKind.Object
            && !request.Payload.EnumerateObject().Any();

    public void Dispose() => _dailyQuotes.Dispose();
}

internal sealed class ApplicationRequestHost(
    Action<AppPreferences> applyPreferences,
    Action ensureTrayIcon,
    Action requestExit,
    Action<string> trace) : IApplicationRequestHost
{
    public void ApplyPreferences(AppPreferences preferences)
        => applyPreferences(preferences);

    public void EnsureTrayIcon() => ensureTrayIcon();

    public void RequestExit() => requestExit();

    public void Trace(string message) => trace(message);
}
