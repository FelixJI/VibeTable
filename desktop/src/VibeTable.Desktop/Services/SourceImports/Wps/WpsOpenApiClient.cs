using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// WPS 365 OpenAPI 只读客户端，仅访问官方 dbsheet 端点：
/// - GET  /v7/coop/dbsheet/{file_id}/schema
/// - POST /v7/coop/dbsheet/{file_id}/sheets/{sheet_id}/records
/// 约束：禁止自动重定向；HTTP 200 但业务 code != 0 视为失败；只有 429/官方
/// 限频码做有界退避重试（默认 4 次尝试），认证/权限错误立即失败；凭据不进入
/// 异常文本。
///
/// 每次尝试的完整请求（发送 + 响应体读取 + JSON 解析）都受三个取消源约束：
/// 调用方 token、客户端会话 lifetime（Dispose 立即中止在途请求体）、有限
/// 请求超时；响应体大小受 MaxResponseBytes 上限约束，超限 fail-closed。
/// </summary>
internal sealed class WpsOpenApiClient : IDisposable
{
    // 限频策略官方示例（parent/list-child）：应用 10 次/秒，超限返回 429000001。
    private static readonly long[] s_rateLimitCodes = [429_000_001, 400_000_001];
    internal const int MaxAttempts = 4;
    internal const int MaxResponseBytes = 32 << 20;
    private static readonly TimeSpan[] s_backoff =
        [TimeSpan.FromMilliseconds(500), TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2)];
    private static readonly TimeSpan s_defaultTimeout = TimeSpan.FromSeconds(30);

    private readonly HttpClient _http;
    private readonly WpsSourceImportConnection _connection;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly TimeSpan _requestTimeout;
    private readonly CancellationTokenSource _lifetime = new();
    private int _disposed;

    internal WpsOpenApiClient(WpsSourceImportConnection connection, HttpMessageHandler? handler = null,
        Func<TimeSpan, CancellationToken, Task>? delay = null, TimeSpan? requestTimeout = null)
    {
        _connection = connection;
        _delay = delay ?? ((span, token) => Task.Delay(span, token));
        _requestTimeout = requestTimeout ?? s_defaultTimeout;
        if (_requestTimeout <= TimeSpan.Zero)
            throw new ArgumentException("请求超时必须为正时间跨度。", nameof(requestTimeout));
        _http = new HttpClient(handler ?? CreateDefaultHandler()) { BaseAddress = connection.ApiBase };
        _http.DefaultRequestHeaders.Accept.ParseAdd("application/json");
    }

    private static HttpClientHandler CreateDefaultHandler() => new()
    {
        // 认证头绝不能跟随重定向外泄；客户端层同样显式拒绝 3xx。
        AllowAutoRedirect = false,
        UseCookies = false,
    };

    internal Task<JsonElement> GetAsync(string requestUri, CancellationToken token)
        => SendAsync(HttpMethod.Get, requestUri, body: null, token);

    internal Task<JsonElement> PostAsync(string requestUri, object body, CancellationToken token)
        => SendAsync(HttpMethod.Post, requestUri, body, token);

    private async Task<JsonElement> SendAsync(HttpMethod method, string requestUri,
        object? body, CancellationToken token)
    {
        byte[]? payload = body is null ? null
            : JsonSerializer.SerializeToUtf8Bytes(body);
        for (int attempt = 1; ; attempt++)
        {
            token.ThrowIfCancellationRequested();
            // 相对 URI 在 HttpRequestMessage 上无法读取 PathAndQuery（签名需要）；
            // 统一组合为绝对地址，目标仍锁定在官方 OpenAPI 域名下。
            Uri absolute = new(_connection.ApiBase, requestUri);
            using var request = new HttpRequestMessage(method, absolute);
            request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _connection.AccessToken);
            byte[] sendPayload = payload is null ? [] : payload.ToArray();
            if (sendPayload.Length > 0)
                request.Content = new ByteArrayContent(sendPayload)
                { Headers = { ContentType = new MediaTypeHeaderValue("application/json") } };
            if (_connection.Signature is { } credentials)
            {
                // 每次尝试使用新的时间戳与载荷哈希，避免重试复用陈旧签名。
                string date = WpsKso1Signer.FormatDate(DateTimeOffset.UtcNow);
                string signature = WpsKso1Signer.Sign(method.Method, request.RequestUri!.PathAndQuery,
                    "application/json", date, credentials.SecretKey, sendPayload);
                request.Headers.TryAddWithoutValidation(WpsKso1Signer.DateHeader, date);
                request.Headers.TryAddWithoutValidation(WpsKso1Signer.AuthorizationHeader,
                    WpsKso1Signer.FormatAuthorization(credentials.AccessKey, signature));
            }
            // 作用域覆盖发送、响应体读取与解析的全过程；Dispose 会取消在途请求体。
            using var scope = CancellationTokenSource.CreateLinkedTokenSource(token, _lifetime.Token);
            scope.CancelAfter(_requestTimeout);
            HttpResponseMessage? response = null;
            try
            {
                response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead,
                    scope.Token).ConfigureAwait(false);
                if ((int)response.StatusCode is >= 300 and < 400)
                    throw new WpsImportException(WpsImportFailure.Protocol,
                        $"WPS OpenAPI 返回重定向 {(int)response.StatusCode}；官方端点不应重定向，已拒绝继续访问。");
                if (response.StatusCode == HttpStatusCode.TooManyRequests)
                {
                    if (attempt < MaxAttempts)
                    {
                        await BackoffAsync(attempt, token).ConfigureAwait(false);
                        continue;
                    }
                    throw new WpsImportException(WpsImportFailure.RateLimited,
                        "WPS OpenAPI 限频（HTTP 429）且重试次数已用尽；请稍后重试。");
                }
                if (response.StatusCode is HttpStatusCode.Unauthorized or HttpStatusCode.Forbidden)
                    throw AuthorizationFailure(response.StatusCode, null);
                byte[] bytes = await ReadBoundedAsync(response.Content, scope.Token).ConfigureAwait(false);
                JsonElement envelope;
                try
                {
                    using JsonDocument document = JsonDocument.Parse(bytes);
                    envelope = document.RootElement.Clone();
                }
                catch (JsonException)
                {
                    if (!response.IsSuccessStatusCode)
                        throw HttpFailure(response.StatusCode);
                    throw new WpsImportException(WpsImportFailure.Protocol,
                        $"WPS OpenAPI 响应不是有效 JSON（{method.Method} {requestUri}）。");
                }
                if (envelope.ValueKind != JsonValueKind.Object
                    || !envelope.TryGetProperty("code", out JsonElement codeElement)
                    || codeElement.ValueKind != JsonValueKind.Number
                    || !envelope.TryGetProperty("msg", out JsonElement msgElement)
                    || msgElement.ValueKind != JsonValueKind.String)
                    throw new WpsImportException(WpsImportFailure.Protocol,
                        $"WPS OpenAPI 响应缺少 code/msg 结构（{method.Method} {requestUri}）。");
                long code = codeElement.GetInt64();
                if (code == 0)
                {
                    if (response.IsSuccessStatusCode)
                        return envelope.TryGetProperty("data", out JsonElement data)
                            ? data.Clone() : default;
                    throw new WpsImportException(WpsImportFailure.Protocol,
                        $"WPS OpenAPI 在 HTTP {(int)response.StatusCode} 中返回 code=0；按失败处理。");
                }
                if (Array.IndexOf(s_rateLimitCodes, code) >= 0)
                {
                    if (attempt < MaxAttempts)
                    {
                        await BackoffAsync(attempt, token).ConfigureAwait(false);
                        continue;
                    }
                    throw new WpsImportException(WpsImportFailure.RateLimited,
                        $"WPS OpenAPI 限频（code={code}）且重试次数已用尽；请稍后重试。", code);
                }
                throw AuthorizationFailure(response.StatusCode, code);
            }
            catch (OperationCanceledException) when (!token.IsCancellationRequested)
            {
                throw CancelledInFlight();
            }
            catch (HttpRequestException error)
            {
                throw new WpsImportException(WpsImportFailure.Connection,
                    $"无法连接 WPS OpenAPI（{method.Method} {requestUri}）：{error.GetType().Name}");
            }
            finally
            {
                response?.Dispose();
            }
        }
    }

    private WpsImportException CancelledInFlight()
        => _lifetime.IsCancellationRequested
            ? throw new ObjectDisposedException(nameof(WpsOpenApiClient),
                "WPS OpenAPI 客户端已释放；在途请求（含响应体读取）已中止。")
            : new WpsImportException(WpsImportFailure.Connection,
                "WPS OpenAPI 请求或响应体读取超时；请稍后重试。");

    private static async Task<byte[]> ReadBoundedAsync(HttpContent content, CancellationToken token)
    {
        await using Stream stream = await content.ReadAsStreamAsync(token).ConfigureAwait(false);
        using var buffer = new MemoryStream();
        byte[] chunk = new byte[16 * 1024];
        int read;
        while ((read = await stream.ReadAsync(chunk, token).ConfigureAwait(false)) != 0)
        {
            if (buffer.Length + read > MaxResponseBytes)
                throw new WpsImportException(WpsImportFailure.Protocol,
                    $"WPS OpenAPI 响应超过 {MaxResponseBytes} 字节上限；已中止，不静默截断。");
            buffer.Write(chunk, 0, read);
        }
        return buffer.ToArray();
    }

    // 服务器 msg 可能回显 token/URL 等敏感内容，绝不进入异常、日志或报告；
    // 异常只携带封闭的官方错误码与固定文案。
    private static WpsImportException AuthorizationFailure(HttpStatusCode status, long? businessCode)
    {
        // 通用错误码约定前 3 位与 HTTP 状态一致（如 40100011、403000001）。
        string code = businessCode?.ToString(CultureInfo.InvariantCulture) ?? "";
        string codeText = businessCode is null ? $"HTTP {(int)status}" : $"code={businessCode}";
        if (code.StartsWith("401", StringComparison.Ordinal) || status == HttpStatusCode.Unauthorized)
            return new WpsImportException(WpsImportFailure.Authentication,
                $"WPS 访问凭证无效或已过期（{codeText}）；请重新授权后重试。", businessCode);
        if (code.StartsWith("403", StringComparison.Ordinal) || status == HttpStatusCode.Forbidden)
            return new WpsImportException(WpsImportFailure.Permission,
                $"无权访问该 WPS 多维表格（{codeText}）；请确认文件授权与 kso.dbsheet.read 权限。", businessCode);
        return new WpsImportException(WpsImportFailure.Protocol,
            $"WPS OpenAPI 业务失败（{codeText}）；请参照官方错误码文档排查。", businessCode);
    }

    private static WpsImportException HttpFailure(HttpStatusCode status)
        => new(WpsImportFailure.Connection, $"WPS OpenAPI 请求失败（HTTP {(int)status}）。");

    private async Task BackoffAsync(int attempt, CancellationToken token)
        => await _delay(s_backoff[Math.Min(attempt - 1, s_backoff.Length - 1)], token).ConfigureAwait(false);

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        // 先取消会话 lifetime 以中止在途请求体，再释放 HttpClient。
        _lifetime.Cancel();
        _http.Dispose();
        _lifetime.Dispose();
    }
}
