using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Read-only HTTP client for the domestic Feishu open platform
/// (https://open.feishu.cn). Only fixed official endpoints are addressed;
/// the access token lives in Host memory, travels only in the Authorization
/// header and never appears in exceptions. Each instance is an independent
/// credential-bearing session: when a handler is injected (tests) it is never
/// disposed by this client; the client disposes only a handler it created
/// itself. Requests, envelopes and paging are
/// verified against larksuite/oapi-sdk-python (v2_main):
/// <list type="bullet">
/// <item>GET /open-apis/bitable/v1/apps/:app_token (code/msg/data, USER|TENANT)</item>
/// <item>GET /open-apis/bitable/v1/apps/:app_token/tables?page_size=&amp;page_token=</item>
/// <item>GET /open-apis/bitable/v1/apps/:app_token/tables/:table_id/fields?page_size=&amp;page_token=</item>
/// <item>GET /open-apis/bitable/v1/apps/:app_token/tables/:table_id/records?page_size=&amp;page_token=</item>
/// <item>GET /open-apis/wiki/v2/spaces/get_node?token=&amp;obj_type=wiki</item>
/// <item>GET /open-apis/drive/v1/medias/:file_token/download</item>
/// </list>
/// List responses use {has_more, page_token, total, items}; success is
/// HTTP 200 with envelope code == 0.
/// </summary>
internal sealed class FeishuSourceImportClient : IDisposable
{
    internal const string DefaultBaseAddress = "https://open.feishu.cn/";
    private const int MaxAttemptsPerRequest = 3;
    private static readonly TimeSpan MaxRetryWait = TimeSpan.FromSeconds(30);
    /// <summary>Hard cap for one JSON envelope body, matching the Go
    /// snapshot budget; larger bodies are protocol failures.</summary>
    internal const int MaxJsonBodyBytes = 32 * 1024 * 1024;

    private readonly HttpClient _http;
    private readonly bool _ownsHandler;
    private readonly TimeSpan _retryDelay;
    private readonly TimeSpan _jsonTimeout;
    private readonly TimeSpan _attachmentTimeout;
    private readonly string _accessToken;
    private int _disposed;

    internal FeishuSourceImportClient(string accessToken, HttpMessageHandler? handler = null,
        TimeSpan? retryDelay = null, TimeSpan? requestTimeout = null)
    {
        accessToken = accessToken.Trim();
        if (accessToken.Length == 0)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidToken,
                "未提供飞书访问令牌；请先在 Host 原生界面完成授权。");
        _accessToken = accessToken;
        _retryDelay = retryDelay ?? TimeSpan.FromMilliseconds(750);
        // HttpClient.Timeout does not constrain body streaming after
        // ResponseHeadersRead, so every attempt carries its own finite
        // deadline covering the request plus the complete body read.
        _jsonTimeout = requestTimeout ?? TimeSpan.FromSeconds(120);
        _attachmentTimeout = requestTimeout ?? TimeSpan.FromSeconds(300);
        if (handler is null)
        {
            // Redirects are refused: a controlled download must never forward
            // Authorization to another host.
            handler = new SocketsHttpHandler
            {
                AllowAutoRedirect = false,
                AutomaticDecompression = DecompressionMethods.All,
                PooledConnectionLifetime = TimeSpan.FromMinutes(10),
            };
            _ownsHandler = true;
        }
        // The base address is fixed to the verified official domestic domain;
        // no caller-supplied origin is accepted.
        _http = new HttpClient(handler, disposeHandler: _ownsHandler)
        {
            BaseAddress = new Uri(DefaultBaseAddress),
            Timeout = TimeSpan.FromSeconds(120),
        };
        _http.DefaultRequestHeaders.Accept.ParseAdd("application/json");
        _http.DefaultRequestHeaders.UserAgent.ParseAdd("VibeTable.Host.SourceImport/1.0");
    }

    internal static int MaxListPages => 1024;

    /// <summary>Verified invalid/expired access-token business code family.</summary>
    private static bool IsInvalidTokenCode(long code) =>
        code is 99991661 or 99991662 or 99991663 or 99991668;

    /// <summary>GET an official endpoint and return its successful data object.
    /// Every request targets a path under the fixed official origin only;
    /// absolute, protocol-relative or non-/open-apis paths are rejected before
    /// any request is issued. Each attempt is bounded by a finite deadline
    /// covering the request and the complete body read; user cancellation
    /// propagates untouched while deadline/network failures retry within the
    /// bounded budget.</summary>
    internal async Task<JsonElement> GetDataAsync(string pathAndQuery, CancellationToken token)
    {
        ThrowIfDisposed();
        GuardRequestPath(pathAndQuery);
        FeishuSourceImportException? failure = null;
        for (int attempt = 1; attempt <= MaxAttemptsPerRequest; attempt++)
        {
            token.ThrowIfCancellationRequested();
            using var deadline = CancellationTokenSource.CreateLinkedTokenSource(token);
            deadline.CancelAfter(_jsonTimeout);
            using var request = new HttpRequestMessage(HttpMethod.Get, pathAndQuery);
            request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _accessToken);
            try
            {
                using HttpResponseMessage response = await _http.SendAsync(
                    request, HttpCompletionOption.ResponseHeadersRead, deadline.Token).ConfigureAwait(false);
                if ((int)response.StatusCode is >= 300 and < 400)
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        $"飞书接口返回重定向（HTTP {(int)response.StatusCode}）；已拒绝跟随以避免凭据外发。");
                switch (response.StatusCode)
                {
                    case HttpStatusCode.OK:
                        return await ReadEnvelopeAsync(response, deadline.Token).ConfigureAwait(false);
                    case HttpStatusCode.Unauthorized:
                        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidToken,
                            "飞书访问令牌无效或已过期；请在 Host 原生界面重新授权。");
                    case HttpStatusCode.Forbidden:
                        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Forbidden,
                            "当前飞书授权无权访问该资源；请核对应用权限范围与文档授权。");
                    case HttpStatusCode.NotFound:
                        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.NotFound,
                            "飞书资源不存在或已删除；请核对链接或 app_token。");
                    case HttpStatusCode.TooManyRequests:
                        failure = new FeishuSourceImportException(FeishuSourceImportErrorKind.RateLimited,
                            "飞书接口限流；读取是只读且有界的，请稍后重新预检。");
                        if (attempt < MaxAttemptsPerRequest
                            && await WaitAsync(RetryAfter(response), token).ConfigureAwait(false))
                            continue;
                        throw failure;
                    case HttpStatusCode.RequestTimeout:
                    case HttpStatusCode.InternalServerError:
                    case HttpStatusCode.BadGateway:
                    case HttpStatusCode.ServiceUnavailable:
                    case HttpStatusCode.GatewayTimeout:
                        failure = TransientFailure(attempt,
                            $"飞书服务暂时不可用（HTTP {(int)response.StatusCode}）；请稍后重试。");
                        if (attempt < MaxAttemptsPerRequest && await WaitAsync(_retryDelay, token).ConfigureAwait(false))
                            continue;
                        throw failure;
                    default:
                        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                            $"飞书接口返回未预期的 HTTP {(int)response.StatusCode}。");
                }
            }
            catch (Exception error) when (!token.IsCancellationRequested
                && error is HttpRequestException or OperationCanceledException)
            {
                // Deadline expiry or a broken network path. The user's own
                // cancellation never enters this branch and propagates raw.
                failure = TransientFailure(attempt,
                    $"网络中断或响应读取超时（单次上限 {_jsonTimeout.TotalSeconds:0} 秒）；请稍后重试。");
                if (attempt < MaxAttemptsPerRequest && await WaitAsync(_retryDelay, token).ConfigureAwait(false))
                    continue;
                throw failure;
            }
        }
        throw failure ?? new FeishuSourceImportException(FeishuSourceImportErrorKind.Transient, "未知网络失败。");
    }

    /// <summary>Read the {code, msg, data} envelope of a 200 response. The
    /// complete body is read under the attempt deadline and bounded by
    /// MaxJsonBodyBytes; server-controlled text never enters errors.</summary>
    private static async Task<JsonElement> ReadEnvelopeAsync(
        HttpResponseMessage response, CancellationToken token)
    {
        await using Stream source = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false);
        var body = new MemoryStream();
        byte[] buffer = new byte[16 * 1024];
        int read;
        while ((read = await source.ReadAsync(buffer, token).ConfigureAwait(false)) != 0)
        {
            if (body.Length + read > MaxJsonBodyBytes)
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                    $"飞书响应体超过单次读取容量（{MaxJsonBodyBytes} 字节）；已停止读取。");
            body.Write(buffer, 0, read);
        }
        using JsonDocument document = JsonDocument.Parse(body.ToArray());
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object || !root.TryGetProperty("code", out JsonElement codeElement)
            || codeElement.ValueKind != JsonValueKind.Number || !codeElement.TryGetInt64(out long code))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "飞书响应缺少业务状态码；本次读取不完整，已停止。");
        if (code == 0)
        {
            if (root.TryGetProperty("data", out JsonElement data) && data.ValueKind == JsonValueKind.Object)
                return data.Clone();
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "飞书成功响应缺少数据对象；本次读取不完整，已停止。");
        }
        // Fixed wording plus the numeric code only: raw server-controlled
        // text (msg, URLs, tokens) never crosses into exceptions or reports.
        string apiCode = code.ToString(CultureInfo.InvariantCulture);
        throw new FeishuSourceImportException(
            IsInvalidTokenCode(code) ? FeishuSourceImportErrorKind.InvalidToken : FeishuSourceImportErrorKind.Business,
            IsInvalidTokenCode(code)
                ? $"飞书访问令牌无效或已过期（code {apiCode}）；请在 Host 原生界面重新授权。"
                : $"飞书接口返回业务失败（code {apiCode}）；本次读取不完整，已停止。",
            apiCode);
    }

    /// <summary>
    /// Walk a {has_more, page_token, items} list endpoint completely. Duplicate
    /// cursors, duplicate item identities, has_more without a next token or a
    /// runaway page count are explicit protocol failures: a read is never
    /// truncated into success.
    /// </summary>
    internal async Task<List<JsonElement>> ListAllAsync(
        string basePath, string idProperty, int pageSize, int maxItems, CancellationToken token)
    {
        var items = new List<JsonElement>();
        var seenCursors = new HashSet<string>(StringComparer.Ordinal);
        var seenIds = new HashSet<string>(StringComparer.Ordinal);
        string? cursor = null;
        for (int page = 0; ; page++)
        {
            ThrowIfDisposed();
            token.ThrowIfCancellationRequested();
            if (page >= MaxListPages || items.Count > maxItems)
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Capacity,
                    "来源分页超过本次读取上限；请缩小迁移范围后重新预检。");
            string separator = basePath.Contains('?', StringComparison.Ordinal) ? "&" : "?";
            string path = basePath + separator + "page_size=" + pageSize.ToString(CultureInfo.InvariantCulture);
            if (cursor is not null)
            {
                if (!seenCursors.Add(cursor))
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        $"来源分页游标重复（{idProperty} 列表）；读取不完整，已停止。");
                path += "&page_token=" + Uri.EscapeDataString(cursor);
            }
            JsonElement data = await GetDataAsync(path, token).ConfigureAwait(false);
            if (!data.TryGetProperty("items", out JsonElement list) || list.ValueKind != JsonValueKind.Array)
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                    $"来源分页响应缺少 items 数组（{idProperty} 列表）；读取不完整，已停止。");
            foreach (JsonElement item in list.EnumerateArray())
            {
                if (item.ValueKind != JsonValueKind.Object
                    || !item.TryGetProperty(idProperty, out JsonElement idElement)
                    || idElement.ValueKind != JsonValueKind.String)
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        $"来源列表项缺少稳定的 {idProperty}；读取不完整，已停止。");
                string id = idElement.GetString()!;
                if (!seenIds.Add(id))
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        $"来源 {idProperty} 重复：{id}；读取不完整，已停止。");
                items.Add(item.Clone());
                if (items.Count > maxItems)
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Capacity,
                        $"来源条目超过本次读取上限（{maxItems}）；请缩小迁移范围后重新预检。");
            }
            bool hasMore = data.TryGetProperty("has_more", out JsonElement more) && more.ValueKind is JsonValueKind.True;
            string? next = data.TryGetProperty("page_token", out JsonElement tokenElement)
                && tokenElement.ValueKind == JsonValueKind.String ? tokenElement.GetString() : null;
            if (!hasMore) break;
            if (string.IsNullOrEmpty(next))
                throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                    $"来源分页标记 has_more 但缺少下一页游标（{idProperty} 列表）；读取不完整，已停止。");
            cursor = next;
        }
        return items;
    }

    /// <summary>
    /// Download attachment bytes through the official media endpoint. The
    /// stream length must equal the reviewed metadata size; redirects and
    /// arbitrary URLs from record values are never followed.
    /// </summary>
    internal async Task<Stream> DownloadAttachmentAsync(string fileToken, long declaredSize, CancellationToken token)
    {
        ThrowIfDisposed();
        if (!IsValidToken(fileToken))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.InvalidInput,
                "附件令牌无效；已拒绝下载。");
        if (declaredSize <= 0 || declaredSize > 32 * 1024 * 1024)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Capacity,
                "附件大小超出单文件迁移容量；请缩小迁移范围后重新预检。");
        const string mediaBase = "/open-apis/drive/v1/medias/";
        GuardRequestPath(mediaBase);
        FeishuSourceImportException? failure = null;
        for (int attempt = 1; attempt <= MaxAttemptsPerRequest; attempt++)
        {
            token.ThrowIfCancellationRequested();
            // The deadline covers the request and the complete body stream;
            // HttpClient.Timeout alone would not bound the latter.
            using var deadline = CancellationTokenSource.CreateLinkedTokenSource(token);
            deadline.CancelAfter(_attachmentTimeout);
            using var request = new HttpRequestMessage(HttpMethod.Get,
                mediaBase + Uri.EscapeDataString(fileToken) + "/download");
            request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _accessToken);
            try
            {
                using HttpResponseMessage response = await _http.SendAsync(
                    request, HttpCompletionOption.ResponseHeadersRead, deadline.Token).ConfigureAwait(false);
                if ((int)response.StatusCode is >= 300 and < 400)
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        $"附件下载被重定向（HTTP {(int)response.StatusCode}）；已拒绝跟随以避免凭据外发。");
                if (response.StatusCode != HttpStatusCode.OK)
                    throw new FeishuSourceImportException(response.StatusCode switch
                    {
                        HttpStatusCode.Unauthorized => FeishuSourceImportErrorKind.InvalidToken,
                        HttpStatusCode.Forbidden => FeishuSourceImportErrorKind.Forbidden,
                        HttpStatusCode.NotFound => FeishuSourceImportErrorKind.NotFound,
                        HttpStatusCode.TooManyRequests => FeishuSourceImportErrorKind.RateLimited,
                        _ => FeishuSourceImportErrorKind.Transient,
                    }, $"附件下载失败（HTTP {(int)response.StatusCode}）；该附件未迁移，请核对权限后重试。");
                await using Stream source = await response.Content.ReadAsStreamAsync(deadline.Token)
                    .ConfigureAwait(false);
                var content = new MemoryStream();
                byte[] buffer = new byte[16 * 1024];
                int read;
                while ((read = await source.ReadAsync(buffer, deadline.Token).ConfigureAwait(false)) != 0)
                {
                    if (content.Length + read > declaredSize)
                        throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                            "附件实际大小超过预检元数据；已停止下载，该附件未迁移。");
                    content.Write(buffer, 0, read);
                }
                if (content.Length != declaredSize)
                    throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                        "附件实际大小小于预检元数据；已停止下载，该附件未迁移。");
                return new MemoryStream(content.ToArray(), writable: false);
            }
            catch (FeishuSourceImportException)
            {
                throw;
            }
            catch (Exception error) when (!token.IsCancellationRequested
                && error is HttpRequestException or OperationCanceledException)
            {
                failure = TransientFailure(attempt,
                    $"附件下载网络中断或读取超时（单次上限 {_attachmentTimeout.TotalSeconds:0} 秒）；请稍后重试。");
                if (attempt < MaxAttemptsPerRequest && await WaitAsync(_retryDelay, token).ConfigureAwait(false))
                    continue;
                throw failure;
            }
        }
        throw failure ?? new FeishuSourceImportException(FeishuSourceImportErrorKind.Transient, "附件下载失败。");
    }

    private static FeishuSourceImportException TransientFailure(int attempt, string message) =>
        new(FeishuSourceImportErrorKind.Transient,
            message + $"（第 {attempt} 次尝试；底层异常已隔离，不进入报告）");

    private async Task<bool> WaitAsync(TimeSpan delay, CancellationToken token)
    {
        ThrowIfDisposed();
        if (delay > TimeSpan.Zero) await Task.Delay(delay, token).ConfigureAwait(false);
        else token.ThrowIfCancellationRequested();
        return true;
    }

    private static TimeSpan RetryAfter(HttpResponseMessage response)
    {
        if (response.Headers.TryGetValues("Retry-After", out IEnumerable<string>? values))
        {
            string value = values.FirstOrDefault() ?? "";
            if (int.TryParse(value, NumberStyles.Integer, CultureInfo.InvariantCulture, out int seconds))
                return TimeSpan.FromSeconds(Math.Clamp(seconds, 0, (int)MaxRetryWait.TotalSeconds));
            if (DateTimeOffset.TryParse(value, CultureInfo.InvariantCulture,
                DateTimeStyles.AssumeUniversal, out DateTimeOffset at))
            {
                TimeSpan wait = at - DateTimeOffset.UtcNow;
                if (wait < TimeSpan.Zero) wait = TimeSpan.Zero;
                if (wait > MaxRetryWait) wait = MaxRetryWait;
                return wait;
            }
        }
        return TimeSpan.Zero;
    }

    /// <summary>
    /// Requests may only address paths under the verified official origin.
    /// Absolute URLs, protocol-relative references, backslashes and paths
    /// outside /open-apis/ are rejected before any request is issued, so the
    /// fixed base address cannot be escaped.
    /// </summary>
    private static void GuardRequestPath(string path)
    {
        if (path.Length == 0
            || !path.StartsWith("/open-apis/", StringComparison.Ordinal)
            || path.StartsWith("//", StringComparison.Ordinal)
            || path.Contains("://", StringComparison.Ordinal)
            || path.Contains('\\'))
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Protocol,
                "已拒绝非官方飞书目标的请求；凭据不会发送到官方端点之外。");
    }

    internal static bool IsValidToken(string value) =>
        value.Length is >= 8 and <= 128
        && value.All(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-');

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
            throw new FeishuSourceImportException(FeishuSourceImportErrorKind.Disposed,
                "飞书来源会话已释放；请重新连接来源。");
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _http.Dispose();
    }
}
