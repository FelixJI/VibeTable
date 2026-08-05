using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DailyQuoteHostClientTests
{
    [TestMethod]
    public void ProductionHandler_DisablesAutomaticRedirects()
    {
        using HttpClientHandler handler =
            DailyQuoteHostClient.CreateHttpMessageHandler();
        Assert.IsFalse(handler.AllowAutoRedirect);
    }

    [TestMethod]
    public void TryParseRequest_RejectsBuiltinAndAnyUrlField()
    {
        using JsonDocument builtin = JsonDocument.Parse(
            """{"provider":"builtin","style":"mixed","locale":"zh-CN"}""");
        using JsonDocument withUrl = JsonDocument.Parse(
            """{"provider":"hitokoto","style":"mixed","locale":"zh-CN","url":"https://attacker.invalid"}""");

        Assert.IsFalse(DailyQuoteHostClient.TryParseRequest(
            builtin.RootElement,
            out _));
        Assert.IsFalse(DailyQuoteHostClient.TryParseRequest(
            withUrl.RootElement,
            out _));
    }

    [TestMethod]
    public async Task FetchAsync_UsesFixedProviderEndpointAndSanitizesOutput()
    {
        Uri? requested = null;
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            requested = request.RequestUri;
            return Task.FromResult(JsonResponse(
                """
                {"hitokoto":"  <b>Keep\n moving</b>  ","from":"\u0001Source","from_who":"Author","uuid":"safe-id-1234"}
                """));
        }));
        using var client = new DailyQuoteHostClient(http);

        DailyQuoteHostResult result = await client.FetchAsync(
            new DailyQuoteHostRequest("hitokoto", "poetry", "zh-CN"),
            CancellationToken.None);

        Assert.AreEqual("v1.hitokoto.cn", requested!.Host);
        StringAssert.Contains(requested.Query, "c=i");
        Assert.AreEqual("Keep moving", result.Text);
        Assert.AreEqual("Author · Source", result.Attribution);
        Assert.AreEqual(
            "https://hitokoto.cn/?uuid=safe-id-1234",
            result.Url);
    }

    [TestMethod]
    public async Task FetchAsync_RejectsWrongContentType()
    {
        using var http = new HttpClient(new DelegateHandler((_, _) =>
        {
            var response = new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent("{}", Encoding.UTF8, "text/html"),
            };
            return Task.FromResult(response);
        }));
        using var client = new DailyQuoteHostClient(http);

        DailyQuoteHostException exception = await Assert.ThrowsAsync<
            DailyQuoteHostException>(() => client.FetchAsync(
                new DailyQuoteHostRequest("quotable", "mixed", "en-US"),
                CancellationToken.None));

        Assert.AreEqual("DAILY_QUOTE_INVALID_CONTENT_TYPE", exception.Code);
    }

    [TestMethod]
    public async Task FetchAsync_UsesFixedQuotableEndpointAndStyleQuery()
    {
        Uri? requested = null;
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            requested = request.RequestUri;
            return Task.FromResult(JsonResponse(
                """[{"_id":"quote-1234","content":"Know thyself.","author":"Socrates"}]"""));
        }));
        using var client = new DailyQuoteHostClient(http);

        DailyQuoteHostResult result = await client.FetchAsync(
            new DailyQuoteHostRequest("quotable", "philosophy", "en-US"),
            CancellationToken.None);

        Assert.AreEqual("api.quotable.io", requested!.Host);
        Assert.AreEqual("/quotes/random", requested.AbsolutePath);
        StringAssert.Contains(requested.Query, "tags=philosophy%7Cwisdom");
        Assert.AreEqual("Know thyself.", result.Text);
    }

    [TestMethod]
    public async Task FetchAsync_UsesOnlyFixedJinrishiciEndpoints()
    {
        const string token = "abcdefghijklmnopqrstuvwx123456";
        var requested = new List<Uri>();
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            requested.Add(request.RequestUri!);
            if (request.RequestUri!.AbsolutePath == "/token")
            {
                return Task.FromResult(JsonResponse(
                    $$"""{"status":"success","data":"{{token}}"}"""));
            }
            Assert.IsTrue(request.Headers.TryGetValues(
                "X-User-Token",
                out IEnumerable<string>? values));
            Assert.AreEqual(token, values.Single());
            return Task.FromResult(JsonResponse(
                """
                {"status":"success","data":{"content":"山高水长。","origin":{"dynasty":"唐","author":"李白","title":"远行"}}}
                """));
        }));
        using var client = new DailyQuoteHostClient(http);

        DailyQuoteHostResult result = await client.FetchAsync(
            new DailyQuoteHostRequest("jinrishici", "poetry", "zh-CN"),
            CancellationToken.None);

        CollectionAssert.AreEqual(
            new[] { "/token", "/sentence" },
            requested.Select(uri => uri.AbsolutePath).ToArray());
        Assert.IsTrue(requested.All(uri => uri.Host == "v2.jinrishici.com"));
        Assert.AreEqual("山高水长。", result.Text);
    }

    [TestMethod]
    public async Task FetchAsync_BoundsStreamingResponseWithoutContentLength()
    {
        using var http = new HttpClient(new DelegateHandler((_, _) =>
        {
            var response = new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StreamContent(new MemoryStream(
                    new byte[DailyQuoteHostClient.MaximumResponseBytes + 1])),
            };
            response.Content.Headers.ContentType =
                new System.Net.Http.Headers.MediaTypeHeaderValue(
                    "application/json");
            return Task.FromResult(response);
        }));
        using var client = new DailyQuoteHostClient(http);

        DailyQuoteHostException exception = await Assert.ThrowsAsync<
            DailyQuoteHostException>(() => client.FetchAsync(
                new DailyQuoteHostRequest("quotable", "mixed", "en-US"),
                CancellationToken.None));

        Assert.AreEqual("DAILY_QUOTE_RESPONSE_TOO_LARGE", exception.Code);
    }

    [TestMethod]
    public async Task FetchAsync_EnforcesThreeSecondProductionTimeoutPolicy()
    {
        Assert.AreEqual(TimeSpan.FromSeconds(3), DailyQuoteHostClient.RequestTimeout);
        using var http = new HttpClient(new DelegateHandler(
            async (_, cancellationToken) =>
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
                return JsonResponse("{}");
            }));
        using var client = new DailyQuoteHostClient(
            http,
            timeout: TimeSpan.FromMilliseconds(20));

        DailyQuoteHostException exception = await Assert.ThrowsAsync<
            DailyQuoteHostException>(() => client.FetchAsync(
                new DailyQuoteHostRequest("quotable", "mixed", "en-US"),
                CancellationToken.None));

        Assert.AreEqual("DAILY_QUOTE_TIMEOUT", exception.Code);
    }

    private static HttpResponseMessage JsonResponse(string json)
        => new(HttpStatusCode.OK)
        {
            Content = new StringContent(json, Encoding.UTF8, "application/json"),
        };

    [TestMethod]
    [DataRow("https://attacker.invalid/collect")]
    [DataRow("http://127.0.0.1:8080/collect")]
    public async Task FetchAsync_DoesNotFollowProviderRedirect(
        string redirectTarget)
    {
        var observed = new List<Uri>();
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            observed.Add(request.RequestUri!);
            var response = new HttpResponseMessage(HttpStatusCode.Redirect);
            response.Headers.Location = new Uri(redirectTarget);
            return Task.FromResult(response);
        }));
        using var client = new DailyQuoteHostClient(http);

        await Assert.ThrowsAsync<DailyQuoteHostException>(() =>
            client.FetchAsync(
                new DailyQuoteHostRequest("hitokoto", "mixed", "zh-CN"),
                CancellationToken.None));

        Assert.HasCount(1, observed);
        Assert.AreEqual("v1.hitokoto.cn", observed[0].Host);
    }

    [TestMethod]
    [DataRow("https://attacker.invalid/token")]
    [DataRow("http://localhost:9000/token")]
    public async Task FetchAsync_DoesNotLeakTokenAcrossRedirect(
        string redirectTarget)
    {
        const string token = "abcdefghijklmnopqrstuvwx123456";
        var observed = new List<(Uri Uri, string? Token)>();
        using var http = new HttpClient(new DelegateHandler((request, _) =>
        {
            string? header = request.Headers.TryGetValues(
                "X-User-Token",
                out IEnumerable<string>? values)
                    ? values.Single()
                    : null;
            observed.Add((request.RequestUri!, header));
            if (request.RequestUri!.AbsolutePath == "/token")
            {
                return Task.FromResult(JsonResponse(
                    $$"""{"status":"success","data":"{{token}}"}"""));
            }
            var response = new HttpResponseMessage(HttpStatusCode.Redirect);
            response.Headers.Location = new Uri(redirectTarget);
            return Task.FromResult(response);
        }));
        using var client = new DailyQuoteHostClient(http);

        await Assert.ThrowsAsync<DailyQuoteHostException>(() =>
            client.FetchAsync(
                new DailyQuoteHostRequest("jinrishici", "poetry", "zh-CN"),
                CancellationToken.None));

        Assert.HasCount(2, observed);
        Assert.IsNull(observed[0].Token);
        Assert.AreEqual(token, observed[1].Token);
        Assert.IsTrue(observed.All(item =>
            item.Uri.Host == "v2.jinrishici.com"));
    }

    private sealed class DelegateHandler(
        Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> send)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
            => send(request, cancellationToken);
    }
}
