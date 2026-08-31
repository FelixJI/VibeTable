using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductSidecarHttpGatewayTests
{
    private const string WorkspaceId =
        "11111111-1111-4111-8111-111111111111";
    private const string ClaimId =
        "22222222-2222-4222-8222-222222222222";
    private static readonly ProductSidecarRegistration[] ProductCatalog =
        [new("query.page", "workspace")];

    [TestMethod]
    public void ProductionHandlerDisablesAmbientNetworkFeatures()
    {
        using HttpClientHandler handler =
            ProductSidecarHttpGateway.CreateProductionHandler();

        Assert.IsFalse(handler.UseProxy);
        Assert.IsFalse(handler.AllowAutoRedirect);
        Assert.IsFalse(handler.UseCookies);
    }

    [TestMethod]
    public async Task HandshakeUsesFixedGenerationAndAcceptsCurrentEmptyCatalog()
    {
        var firstHandler = new RecordingHandler(request =>
        {
            Assert.AreEqual(
                "/api/vibetable/v2/product/capabilities",
                request.RequestUri!.AbsolutePath);
            Assert.AreEqual(
                "generation-one-secret",
                request.Headers.GetValues("X-VibeTable-Session").Single());
            return Json(Capabilities());
        });
        using var gateway = Gateway(
            firstHandler,
            "generation-one-secret",
            expectedRegistrations: []);
        ProductSidecarCapabilities capabilities =
            await gateway.GetCapabilitiesAsync(CancellationToken.None);
        Assert.AreEqual(WorkspaceId, capabilities.WorkspaceId);
        Assert.AreEqual<ulong>(7, capabilities.SessionEpoch);
        Assert.AreEqual(0, capabilities.Registrations.Count);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            ForwardAsync(gateway));
        Assert.AreEqual(1, firstHandler.SendCount);
    }

    [TestMethod]
    public async Task ForwardIsRejectedUntilHandshakeSucceeds()
    {
        var handler = new RecordingHandler(_ =>
            throw new AssertFailedException("The request must not be sent."));
        using var gateway = Gateway(handler);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            ForwardAsync(gateway));
        Assert.AreEqual(0, handler.SendCount);
    }

    [TestMethod]
    public async Task HandshakeRequiresExactOrderedRegistrationCatalog()
    {
        ProductSidecarRegistration[] expected =
        [
            new("query.page", "workspace"),
            new("system.info", "global"),
        ];
        var handler = new RecordingHandler(_ => Json(Capabilities(
            rpcMethods: "[\"query.page\",\"system.info\"]",
            registrations:
                    "[{\"method\":\"query.page\",\"scope\":\"workspace\"}," +
                    "{\"method\":\"system.info\",\"scope\":\"global\"}]")));
        using var gateway = Gateway(handler, expectedRegistrations: expected);
        ProductSidecarCapabilities capabilities =
            await gateway.GetCapabilitiesAsync(CancellationToken.None);
        CollectionAssert.AreEqual(
            new[] { "query.page", "system.info" },
            capabilities.RpcMethods.ToArray());
        CollectionAssert.AreEqual(expected, capabilities.Registrations.ToArray());
    }

    [TestMethod]
    public async Task HandshakeRejectsClosedShapeDriftAndCatalogDrift()
    {
        string[] invalidDocuments =
        [
            Capabilities(extra: ",\"unexpected\":true"),
            Capabilities(workspaceId: ClaimId),
            Capabilities(rpcMethods: "[\"query.page\"]"),
            Capabilities(
                rpcMethods: "[\"system.info\",\"query.page\"]",
                registrations:
                    "[{\"method\":\"system.info\",\"scope\":\"global\"}," +
                    "{\"method\":\"query.page\",\"scope\":\"workspace\"}]"),
            Capabilities().Replace(
                "\"contractVersion\":\"2.0\"",
                "\"contractVersion\":\"2.0\",\"contractVersion\":\"2.0\"",
                StringComparison.Ordinal),
        ];
        foreach (string document in invalidDocuments)
        {
            var handler = new RecordingHandler(_ => Json(document));
            using var gateway = Gateway(handler, expectedRegistrations: []);
            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                gateway.GetCapabilitiesAsync(CancellationToken.None));
        }
    }

    [TestMethod]
    public async Task FailedHandshakeDoesNotEnableForwarding()
    {
        var handler = new RecordingHandler(_ => Json(Capabilities(
            fenceEpoch: 99)));
        using var gateway = Gateway(handler, expectedRegistrations: []);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.GetCapabilitiesAsync(CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            ForwardAsync(gateway));
        Assert.AreEqual(1, handler.SendCount);
    }

    [TestMethod]
    public async Task ForwardSendsOneClosedRequestAndReturnsClonedSuccess()
    {
        const string wireJson =
            "{\"scope\":\"workspace\",\"workspaceId\":\"" + WorkspaceId +
            "\",\"sessionEpoch\":7,\"sequence\":0}";
        var handler = ProductHandler(request =>
        {
            Assert.AreEqual(HttpMethod.Post, request.Method);
            Assert.AreEqual(
                "/api/vibetable/v2/product/rpc",
                request.RequestUri!.AbsolutePath);
            Assert.AreEqual(
                "application/json",
                request.Content!.Headers.ContentType!.MediaType);
            using JsonDocument sent = JsonDocument.Parse(
                request.Content.ReadAsStream());
            CollectionAssert.AreEquivalent(
                new[] { "jsonrpc", "id", "method", "wire", "params" },
                sent.RootElement.EnumerateObject()
                    .Select(property => property.Name).ToArray());
            Assert.AreEqual("request-1", sent.RootElement.GetProperty("id").GetString());
            Assert.AreEqual("query.page", sent.RootElement.GetProperty("method").GetString());
            return Json(SuccessResponse(
                wire: wireJson,
                result: "{\"rows\":[{\"id\":\"row-1\"}]}"));
        });
        using var gateway = await ReadyGatewayAsync(handler);
        using JsonDocument wire = JsonDocument.Parse(wireJson);
        using JsonDocument parameters = JsonDocument.Parse("{\"limit\":50}");
        ProductSidecarForwardResult outcome = await gateway.ForwardAsync(
            "request-1",
            "query.page",
            wire.RootElement,
            parameters.RootElement,
            CancellationToken.None);
        ProductSidecarSuccess success = (ProductSidecarSuccess)outcome;
        Assert.AreEqual(
            "row-1",
            success.Result.GetProperty("rows")[0].GetProperty("id").GetString());
        Assert.AreEqual(2, handler.SendCount);
    }

    [TestMethod]
    public async Task ForwardParsesStandardAndTypedProductErrors()
    {
        Queue<HttpResponseMessage> responses = new(
        [
            Json(ProductCapabilities()),
            Json(
                ErrorWithCode("-32600", "bad-1", "Invalid Request"),
                HttpStatusCode.BadRequest),
            Json(
                """
                {"jsonrpc":"2.0","id":"typed-1","wire":{},
                 "error":{"code":-32150,"message":"Product data error",
                  "data":{"kind":"product_data_error","message":"bad field",
                   "code":"schema.field.invalid","path":"fields[0]",
                   "details":{"precision":8},"retryable":false}}}
                """),
        ]);
        var handler = new RecordingHandler(_ => responses.Dequeue());
        using var gateway = await ReadyGatewayAsync(handler);
        var standard = (ProductSidecarFailure)await ForwardAsync(gateway, "bad-1");
        var typed = (ProductSidecarFailure)await ForwardAsync(gateway, "typed-1");

        Assert.AreEqual(-32600, standard.Error.Code);
        Assert.IsNull(standard.Error.Data);
        Assert.AreEqual(
            "schema.field.invalid",
            typed.Error.Data!.Value.GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task ForwardRejectsInvalidInputBeforeSending()
    {
        var handler = ProductHandler(_ => Json(SuccessResponse()));
        using var gateway = await ReadyGatewayAsync(handler);
        using JsonDocument scalar = JsonDocument.Parse("42");
        using JsonDocument empty = JsonDocument.Parse("{}");

        Func<Task>[] invalidRequests =
        [
            () => gateway.ForwardAsync(
                "", "query.page", empty.RootElement, empty.RootElement,
                CancellationToken.None),
            () => gateway.ForwardAsync(
                "request-1", "", empty.RootElement, empty.RootElement,
                CancellationToken.None),
            () => gateway.ForwardAsync(
                "request-1", "query.page", scalar.RootElement,
                empty.RootElement, CancellationToken.None),
            () => gateway.ForwardAsync(
                "request-1", "query.page", empty.RootElement,
                scalar.RootElement, CancellationToken.None),
        ];
        foreach (Func<Task> invalid in invalidRequests)
            await Assert.ThrowsExactlyAsync<ArgumentException>(invalid);

        Assert.AreEqual(1, handler.SendCount);
    }

    [TestMethod]
    public async Task ForwardRejectsNonClosedOrMismatchedResponses()
    {
        string[] invalidResponses =
        [
            SuccessResponse()[..^1] + ",\"extra\":1}",
            "{\"jsonrpc\":\"2.0\",\"jsonrpc\":\"2.0\",\"id\":\"request-1\",\"wire\":{},\"result\":{}}",
            SuccessResponse()[..^1] + ",\"error\":{\"code\":-32603,\"message\":\"Internal error\"}}",
            SuccessResponse(id: "other"),
            SuccessResponse(wire: "{\"scope\":\"global\"}"),
            TypedErrorWithCode("pocketbase.failed"),
            TypedErrorWithCode("schema.field.invalid\n"),
            ErrorWithCode("\"-32603\""),
            ErrorWithCode("null"),
            ErrorWithCode("{}"),
            ErrorWithCode("-32603.5"),
            ErrorWithCode("2147483648"),
        ];

        foreach (string invalid in invalidResponses)
        {
            var handler = ProductHandler(_ => Json(invalid));
            using var gateway = await ReadyGatewayAsync(handler);
            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                ForwardAsync(gateway));
        }
    }

    [TestMethod]
    public async Task RequestOverOneMiBIsRejectedWithoutPost()
    {
        var handler = ProductHandler(_ =>
            throw new AssertFailedException("Oversize request was sent."));
        using var gateway = await ReadyGatewayAsync(handler);
        using JsonDocument empty = JsonDocument.Parse("{}");
        using JsonDocument parameters = JsonDocument.Parse(
            JsonSerializer.Serialize(new { value = new string('x', 1024 * 1024) }));

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.ForwardAsync(
                "request-1",
                "query.page",
                empty.RootElement,
                parameters.RootElement,
                CancellationToken.None));

        Assert.AreEqual(1, handler.SendCount);
    }

    [TestMethod]
    public async Task ResponseContentLengthOverFourMiBIsRejectedBeforeRead()
    {
        int readCount = 0;
        var guarded = new CallbackReadStream((_, _) =>
        {
            readCount++;
            throw new AssertFailedException("Oversize body was read.");
        });
        var oversized = new StreamContent(guarded);
        oversized.Headers.ContentLength = 4 * 1024 * 1024 + 1;
        Queue<HttpResponseMessage> responses = new(
        [
            Json(ProductCapabilities()),
            new HttpResponseMessage(HttpStatusCode.OK) { Content = oversized },
        ]);
        var handler = new RecordingHandler(_ => responses.Dequeue());
        using var gateway = await ReadyGatewayAsync(handler);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            ForwardAsync(gateway));

        Assert.AreEqual(0, readCount);
    }

    [TestMethod]
    public async Task UnknownLengthResponseStopsAtFourMiBBoundary()
    {
        int remaining = 5 * 1024 * 1024;
        int served = 0;
        var oversized = new CallbackReadStream((buffer, _) =>
        {
            int count = Math.Min(buffer.Length, remaining);
            buffer.Span[..count].Clear();
            served += count;
            remaining -= count;
            return ValueTask.FromResult(count);
        });
        Queue<HttpResponseMessage> responses = new(
        [
            Json(ProductCapabilities()),
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StreamContent(oversized),
            },
        ]);
        using var gateway = await ReadyGatewayAsync(
            new RecordingHandler(_ => responses.Dequeue()));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            ForwardAsync(gateway));

        Assert.IsLessThanOrEqualTo(4 * 1024 * 1024 + 16 * 1024, served);
    }

    [TestMethod]
    public async Task UnsafeHttpStatusesNeverExposeBodyOrCredential()
    {
        HttpStatusCode[] statuses =
        [
            HttpStatusCode.Unauthorized,
            HttpStatusCode.Forbidden,
            HttpStatusCode.Redirect,
            HttpStatusCode.InternalServerError,
        ];
        foreach (HttpStatusCode status in statuses)
        {
            var handler = ProductHandler(_ => Json(
                "response-body-marker",
                status));
            using var gateway = await ReadyGatewayAsync(
                handler, "credential-marker");
            BackendUnavailableException error =
                await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
                    ForwardAsync(gateway));

            Assert.IsFalse(error.Message.Contains("response-body-marker"));
            Assert.IsFalse(error.Message.Contains("credential-marker"));
        }
    }

    [TestMethod]
    public async Task CapabilityStatusFailureDoesNotReadOrExposeBody()
    {
        int reads = 0;
        var body = new CallbackReadStream((_, _) =>
        {
            reads++;
            throw new IOException("capability-body-marker");
        });
        var handler = new RecordingHandler(_ => new HttpResponseMessage(
            HttpStatusCode.InternalServerError)
        {
            Content = new StreamContent(body),
        });
        using var gateway = Gateway(handler);

        BackendUnavailableException error =
            await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
                gateway.GetCapabilitiesAsync(CancellationToken.None));

        Assert.AreEqual(0, reads);
        Assert.IsFalse(error.Message.Contains("capability-body-marker"));
    }

    [TestMethod]
    public async Task CallerCancellationPropagatesDuringSendAndRead()
    {
        var sendHandler = new RecordingHandler(async (request, token) =>
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/capabilities"))
                return Json(ProductCapabilities());
            await Task.Delay(Timeout.InfiniteTimeSpan, token);
            throw new AssertFailedException("Canceled send resumed.");
        });
        using var sendGateway = await ReadyGatewayAsync(
            sendHandler, timeout: TimeSpan.FromSeconds(2));
        using var sendCancellation = new CancellationTokenSource(
            TimeSpan.FromMilliseconds(20));

        await Assert.ThrowsAsync<OperationCanceledException>(() =>
            ForwardAsync(sendGateway, "send-cancel", sendCancellation.Token));

        var blocking = new CallbackReadStream(async (_, token) =>
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, token);
            return 0;
        });
        Queue<HttpResponseMessage> responses = new(
        [
            Json(ProductCapabilities()),
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StreamContent(blocking),
            },
        ]);
        var readHandler = new RecordingHandler(_ => responses.Dequeue());
        using var readGateway = await ReadyGatewayAsync(
            readHandler, timeout: TimeSpan.FromSeconds(2));
        using var readCancellation = new CancellationTokenSource(
            TimeSpan.FromMilliseconds(20));

        await Assert.ThrowsAsync<OperationCanceledException>(() =>
            ForwardAsync(readGateway, "read-cancel", readCancellation.Token));
    }

    [TestMethod]
    public async Task DeadlineAndNetworkFailuresUseFixedSafeExceptions()
    {
        var timeoutHandler = new RecordingHandler(async (request, token) =>
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/capabilities"))
                return Json(ProductCapabilities());
            await Task.Delay(Timeout.InfiniteTimeSpan, token);
            throw new AssertFailedException("Deadline did not cancel send.");
        });
        using var timeoutGateway = await ReadyGatewayAsync(
            timeoutHandler,
            timeout: TimeSpan.FromMilliseconds(20));
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            ForwardAsync(timeoutGateway, "deadline"));

        var networkHandler = ProductHandler(_ =>
            throw new HttpRequestException("network-path-marker"));
        using var networkGateway = await ReadyGatewayAsync(networkHandler);
        BackendUnavailableException error =
            await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
                ForwardAsync(networkGateway, "network"));
        Assert.IsFalse(error.Message.Contains("network-path-marker"));

        var brokenStream = new CallbackReadStream((_, _) =>
            throw new IOException("response-path-marker"));
        Queue<HttpResponseMessage> responses = new(
        [
            Json(ProductCapabilities()),
            new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StreamContent(brokenStream),
            },
        ]);
        using var streamGateway = await ReadyGatewayAsync(
            new RecordingHandler(_ => responses.Dequeue()));
        BackendUnavailableException streamError =
            await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
                ForwardAsync(streamGateway, "stream"));
        Assert.IsFalse(streamError.Message.Contains("response-path-marker"));
    }

    [TestMethod]
    public async Task LatestHandshakeAttemptAloneControlsReadiness()
    {
        await VerifyInterleavingAsync(olderSucceeds: true);
        await VerifyInterleavingAsync(olderSucceeds: false);

        static async Task VerifyInterleavingAsync(bool olderSucceeds)
        {
            var firstResponse = new TaskCompletionSource<HttpResponseMessage>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            var firstStarted = new TaskCompletionSource(
                TaskCreationOptions.RunContinuationsAsynchronously);
            int capabilityCalls = 0;
            var handler = new RecordingHandler(async (request, _) =>
            {
                if (request.RequestUri!.AbsolutePath.EndsWith("/rpc"))
                    return Json(SuccessResponse(id: "probe"));
                if (Interlocked.Increment(ref capabilityCalls) == 1)
                {
                    firstStarted.TrySetResult();
                    return await firstResponse.Task;
                }
                return Json(olderSucceeds
                    ? ProductCapabilities(fenceEpoch: 99)
                    : ProductCapabilities());
            });
            using var gateway = Gateway(handler);
            Task<ProductSidecarCapabilities> older =
                gateway.GetCapabilitiesAsync(CancellationToken.None);
            await firstStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
            Task<ProductSidecarCapabilities> newer =
                gateway.GetCapabilitiesAsync(CancellationToken.None);

            if (olderSucceeds)
            {
                await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                    newer.WaitAsync(TimeSpan.FromSeconds(2)));
                firstResponse.SetResult(Json(ProductCapabilities()));
                await older.WaitAsync(TimeSpan.FromSeconds(2));
                await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                    ForwardAsync(gateway, "probe"));
                return;
            }

            await newer.WaitAsync(TimeSpan.FromSeconds(2));
            firstResponse.SetResult(Json(ProductCapabilities(fenceEpoch: 99)));
            await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
                older.WaitAsync(TimeSpan.FromSeconds(2)));
            ProductSidecarForwardResult outcome = await ForwardAsync(gateway, "probe");
            Assert.IsInstanceOfType<ProductSidecarSuccess>(outcome);
        }
    }

    [TestMethod]
    public async Task DisposeCannotBeOverwrittenByLateHandshakeSuccess()
    {
        var response = new TaskCompletionSource<HttpResponseMessage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var handler = new RecordingHandler(async (_, _) =>
        {
            started.TrySetResult();
            return await response.Task;
        });
        var gateway = Gateway(handler);
        Task<ProductSidecarCapabilities> handshake =
            gateway.GetCapabilitiesAsync(CancellationToken.None);
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        gateway.Dispose();
        response.SetResult(Json(ProductCapabilities()));

        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() =>
            handshake.WaitAsync(TimeSpan.FromSeconds(2)));
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() =>
            ForwardAsync(gateway, "probe"));
    }

    [TestMethod]
    public async Task DisposeWinsWhenSentRpcCompletesSuccessfullyLater()
    {
        var response = new TaskCompletionSource<HttpResponseMessage>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var handler = new RecordingHandler(async (request, _) =>
        {
            if (request.RequestUri!.AbsolutePath.EndsWith("/capabilities"))
                return Json(ProductCapabilities());
            started.TrySetResult();
            return await response.Task;
        });
        var gateway = await ReadyGatewayAsync(handler);
        Task<ProductSidecarForwardResult> forward = ForwardAsync(gateway, "late");
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        gateway.Dispose();
        response.SetResult(Json(SuccessResponse(id: "late")));

        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() =>
            forward.WaitAsync(TimeSpan.FromSeconds(2)));
    }

    [TestMethod]
    public async Task DisposeAndGenerationSnapshotsPreventCredentialReuse()
    {
        async Task ExerciseGeneration(string secret)
        {
            var handler = ProductHandler(request =>
            {
                Assert.AreEqual(
                    secret,
                    request.Headers.GetValues("X-VibeTable-Session").Single());
                return Json(SuccessResponse());
            });
            var gateway = await ReadyGatewayAsync(handler, secret);
            await ForwardAsync(gateway);
            gateway.Dispose();
            await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() =>
                ForwardAsync(gateway));
        }

        await ExerciseGeneration("old-generation-secret");
        await ExerciseGeneration("new-generation-secret");
    }

    [TestMethod]
    public void ConstructorRejectsNonLoopbackOrInvalidGenerationIdentity()
    {
        var remote = Context("secret", new Uri("http://example.com:8090/"));

        Assert.ThrowsExactly<ArgumentException>(() =>
            new ProductSidecarHttpGateway(remote, Identity(), []));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new ProductSidecarHttpGateway(
                Context("secret"),
                Identity(
                    workspaceId:
                        "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"),
                []));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new ProductSidecarHttpGateway(
                Context("secret"),
                Identity(sessionEpoch: 0),
                []));
        var registration = new ProductSidecarRegistration(
            "query.page", "workspace");
        Assert.ThrowsExactly<ArgumentException>(() =>
            new ProductSidecarHttpGateway(
                Context("secret"), Identity(), [registration, registration]));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new ProductSidecarHttpGateway(
                Context("secret"), Identity(), [new("query.page", "process")]));
    }

    private static ProductSidecarHttpGateway Gateway(
        HttpMessageHandler handler,
        string secret = "private-secret",
        TimeSpan? timeout = null,
        IReadOnlyCollection<ProductSidecarRegistration>? expectedRegistrations = null)
        => new(
            Context(secret),
            Identity(),
            expectedRegistrations ?? ProductCatalog,
            handler,
            timeout);

    private static async Task<ProductSidecarForwardResult> ForwardAsync(
        ProductSidecarHttpGateway gateway,
        string requestId = "request-1",
        CancellationToken cancellationToken = default)
    {
        using JsonDocument empty = JsonDocument.Parse("{}");
        return await gateway.ForwardAsync(
            requestId, "query.page", empty.RootElement,
            empty.RootElement, cancellationToken).WaitAsync(TimeSpan.FromSeconds(2));
    }

    private static async Task<ProductSidecarHttpGateway> ReadyGatewayAsync(
        HttpMessageHandler handler,
        string secret = "private-secret",
        TimeSpan? timeout = null)
    {
        ProductSidecarHttpGateway gateway = Gateway(handler, secret, timeout);
        await gateway.GetCapabilitiesAsync(CancellationToken.None);
        return gateway;
    }

    private static ProductSidecarIdentity Identity(
        string workspaceId = WorkspaceId,
        ulong sessionEpoch = 7,
        ulong fenceEpoch = 3,
        string claimId = ClaimId)
        => new(workspaceId, sessionEpoch, fenceEpoch, claimId);

    private static PocketBaseAdminContext Context(
        string secret,
        Uri? origin = null)
    {
        origin ??= new Uri("http://127.0.0.1:8090/");
        return new PocketBaseAdminContext(
            new Uri(origin, "_/"),
            origin,
            "X-VibeTable-Session",
            secret);
    }

    private static string Capabilities(
        string workspaceId = WorkspaceId,
        ulong sessionEpoch = 7,
        ulong fenceEpoch = 3,
        string claimId = ClaimId,
        string rpcMethods = "[]",
        string registrations = "[]",
        string extra = "")
        => $$"""
        {
          "contractVersion":"2.0",
          "workspaceId":"{{workspaceId}}",
          "sessionEpoch":{{sessionEpoch}},
          "fenceEpoch":{{fenceEpoch}},
          "claimId":"{{claimId}}",
          "rpcMethods":{{rpcMethods}},
          "registrations":{{registrations}}{{extra}}
        }
        """;

    private static string ProductCapabilities(ulong fenceEpoch = 3) => Capabilities(
        fenceEpoch: fenceEpoch,
        rpcMethods: "[\"query.page\"]",
        registrations: "[{\"method\":\"query.page\",\"scope\":\"workspace\"}]");

    private static string SuccessResponse(
        string id = "request-1", string wire = "{}", string result = "{}") =>
        "{\"jsonrpc\":\"2.0\",\"id\":\"" + id + "\",\"wire\":" + wire +
        ",\"result\":" + result + "}";

    private static string ErrorWithCode(
        string code, string id = "request-1", string message = "Internal error") =>
        "{\"jsonrpc\":\"2.0\",\"id\":\"" + id + "\",\"wire\":{}," +
        "\"error\":{\"code\":" + code + ",\"message\":\"" + message + "\"}}";

    private static string TypedErrorWithCode(string code) =>
        "{\"jsonrpc\":\"2.0\",\"id\":\"request-1\",\"wire\":{}," +
        "\"error\":{\"code\":-32150,\"message\":\"Product data error\"," +
        "\"data\":{\"kind\":\"product_data_error\",\"message\":\"leak\"," +
        "\"code\":" + JsonSerializer.Serialize(code) +
        ",\"path\":null,\"details\":{},\"retryable\":false}}}";

    private static HttpResponseMessage Json(
        string body,
        HttpStatusCode status = HttpStatusCode.OK)
        => new(status)
        {
            Content = new StringContent(body, Encoding.UTF8, "application/json"),
        };

    private static RecordingHandler ProductHandler(
        Func<HttpRequestMessage, HttpResponseMessage> rpc)
        => new(request => request.RequestUri!.AbsolutePath.EndsWith(
                "/capabilities",
                StringComparison.Ordinal)
            ? Json(ProductCapabilities())
            : rpc(request));

    private sealed class RecordingHandler : HttpMessageHandler
    {
        private readonly Func<
            HttpRequestMessage,
            CancellationToken,
            Task<HttpResponseMessage>> _response;

        public RecordingHandler(Func<HttpRequestMessage, HttpResponseMessage> response)
            : this((request, _) => Task.FromResult(response(request)))
        {
        }

        public RecordingHandler(Func<
            HttpRequestMessage,
            CancellationToken,
            Task<HttpResponseMessage>> response)
        {
            _response = response;
        }

        public int SendCount { get; private set; }

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
        {
            SendCount++;
            return _response(request, cancellationToken);
        }
    }

    private sealed class CallbackReadStream(
        Func<Memory<byte>, CancellationToken, ValueTask<int>> read)
        : Stream
    {
        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position
        {
            get => throw new NotSupportedException();
            set => throw new NotSupportedException();
        }

        public override int Read(byte[] buffer, int offset, int count)
            => read(buffer.AsMemory(offset, count), CancellationToken.None)
                .AsTask().GetAwaiter().GetResult();

        public override ValueTask<int> ReadAsync(
            Memory<byte> buffer,
            CancellationToken cancellationToken = default)
            => read(buffer, cancellationToken);

        public override void Flush() => throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin)
            => throw new NotSupportedException();
        public override void SetLength(long value)
            => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count)
            => throw new NotSupportedException();
    }
}
