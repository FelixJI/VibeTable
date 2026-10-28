using System.Text.Json;

namespace VibeTable.Desktop.Services;
internal interface IApplicationRequestHost
{
    void RequestExit();

    void Trace(string message);
}

/// <summary>
/// Owns renderer-driven release update and daily quote
/// requests. WPF supplies only native lifecycle effects; validation, state,
/// cancellation, result projection, and stable errors stay inside this seam.
/// </summary>
internal sealed class ApplicationRequestController : IDisposable
{
    private readonly IWebReplySink _reply;
    private readonly IApplicationRequestHost _host;
    private readonly AppPreferencesUseCases _preferences;
    private readonly ReleaseUpdateCoordinator _updates;
    private readonly DailyQuoteHostClient _dailyQuotes;
    private readonly Func<CancellationToken> _sessionToken;

    public ApplicationRequestController(
        IWebReplySink reply,
        IApplicationRequestHost host,
        AppPreferencesUseCases preferences,
        ReleaseUpdateCoordinator updates,
        DailyQuoteHostClient dailyQuotes,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _preferences = preferences ?? throw new ArgumentNullException(nameof(preferences));
        _updates = updates ?? throw new ArgumentNullException(nameof(updates));
        _dailyQuotes = dailyQuotes ?? throw new ArgumentNullException(nameof(dailyQuotes));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
    }

    public AppPreferences CurrentPreferences => _preferences.CurrentPreferences;

    public static bool Handles(string requestType)
        => requestType is
            "update.check" or
            "update.install" or
            "dailyQuote.fetch";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "update.check" => CheckForReleaseUpdateAsync(request),
            "update.install" => InstallReleaseUpdateAsync(request),
            "dailyQuote.fetch" => FetchDailyQuoteAsync(request),
            _ => RejectUnknownAsync(request),
        };

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
            _preferences.RefreshCurrent();
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
    Action requestExit,
    Action<string> trace) : IApplicationRequestHost
{
    public void RequestExit() => requestExit();

    public void Trace(string message) => trace(message);
}
