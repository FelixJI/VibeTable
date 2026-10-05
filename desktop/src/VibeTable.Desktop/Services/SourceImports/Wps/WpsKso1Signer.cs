using System.Security.Cryptography;
using System.Text;

namespace VibeTable.Desktop.Services;

/// <summary>
/// KSO-1 请求签名，按官方签名说明实现：
/// X-Kso-Authorization: "KSO-1 {accessKey}:{hmac}"，其中
/// hmac = HMAC-SHA256(secretKey, "KSO-1" + Method + RequestURI + ContentType + KsoDate + sha256hex(body))。
/// RequestURI 为含 query 的路径；空请求体的 sha256 段为空串。
/// 官方来源： https://open.wps.cn/documents/app-integration-dev/wps365/server/api-description/signature-description
/// </summary>
internal static class WpsKso1Signer
{
    internal const string AuthorizationPrefix = "KSO-1";
    internal const string DateHeader = "X-Kso-Date";
    internal const string AuthorizationHeader = "X-Kso-Authorization";

    /// <summary>RFC1123（GMT）格式的 X-Kso-Date，与官方示例 "Mon, 02 Jan 2006 15:04:05 GMT" 一致。</summary>
    internal static string FormatDate(DateTimeOffset utc) => utc.ToString("R");

    internal static string FormatAuthorization(string accessKey, string signature)
        => AuthorizationPrefix + " " + accessKey + ":" + signature;

    /// <summary>对单个请求计算 KSO-1 签名；body 为空时使用空串。</summary>
    internal static string Sign(string method, string requestUri, string contentType,
        string ksoDate, string secretKey, ReadOnlySpan<byte> body)
    {
        string bodyHash = body.IsEmpty ? "" : Convert.ToHexStringLower(SHA256.HashData(body));
        string payload = AuthorizationPrefix + method + requestUri + contentType + ksoDate + bodyHash;
        using var hmac = new HMACSHA256(Encoding.UTF8.GetBytes(secretKey));
        return Convert.ToHexStringLower(hmac.ComputeHash(Encoding.UTF8.GetBytes(payload)));
    }
}
