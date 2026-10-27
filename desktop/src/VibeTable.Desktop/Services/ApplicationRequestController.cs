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
                AppSettingsErrorCodes.BadPayload);
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
                AppSettingsErrorCodes.ReadFailed);
        }
        return Task.CompletedTask;
    }

    private Task UpdatePreferencesAsync(RoutedWebRequest request)
    {
        // The generated reader rejects unknown/duplicate keys, present nulls,
        // bad enums, oversized URLs, empty patches and any mixing of
        // theme/adoptLegacyTheme before business state is touched.
        if (!AppSettingsPatchReader.TryRead(request.Payload, out AppSettingsPreferencesPatch? patch)
            || patch is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "The application preferences update is invalid.",
                AppSettingsErrorCodes.BadPayload);
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
            // The store write may already have committed and appearance/native
            // effects may have run; this failure never means "not committed".
            _reply.PostOperationFailed(
                request.RequestId,
                "无法保存桌面应用设置，请检查当前用户权限后重试。",
                AppSettingsErrorCodes.WriteFailed);
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

    /// <summary>Result projection shared by get/update replies. The DTO and
    /// its wire names are generated from the app-settings contract.</summary>
    private void PostPreferences(RoutedWebRequest request)
        => _reply.PostResponse(
            request.Type,
            request.RequestId,
            new AppSettingsPreferences(
                CurrentPreferences.MinimizeToTrayOnClose,
                CurrentPreferences.StartWithWindows,
                CurrentPreferences.UpdateProxy,
                CurrentPreferences.CustomUpdateProxyUrl ?? "",
                CurrentPreferences.Theme,
                _appearance.Projection.Effective,
                _appearance.Projection.HighContrast));

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
