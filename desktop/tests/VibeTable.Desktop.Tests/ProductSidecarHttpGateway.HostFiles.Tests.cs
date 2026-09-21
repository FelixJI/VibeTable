using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

public sealed partial class ProductSidecarHttpGatewayTests
{
    [TestMethod]
    [DataRow(true)]
    [DataRow(false)]
    public async Task NativeAttachmentChangeSendsOnlyBytesAndExistingGuardedMutation(bool upload)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-http-file-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        string path = Path.Combine(root, "invoice.txt");
        File.WriteAllText(path, "invoice content");
        int calls = 0;
        try
        {
            var handler = new RecordingHandler(request =>
            {
                calls++;
                Assert.AreEqual(HttpMethod.Post, request.Method);
                Assert.AreEqual("/api/vibetable/v1/mutations/apply", request.RequestUri!.AbsolutePath);
                Assert.AreEqual("private-secret", request.Headers.GetValues("X-VibeTable-Session").Single());
                string wire = request.Content!.ReadAsStringAsync().GetAwaiter().GetResult();
                Assert.IsFalse(wire.Contains(root, StringComparison.Ordinal));
                string json;
                if (upload)
                {
                    Assert.AreEqual("multipart/form-data", request.Content.Headers.ContentType!.MediaType);
                    var multipart = (MultipartFormDataContent)request.Content;
                    json = multipart.First().ReadAsStringAsync().GetAwaiter().GetResult();
                    Assert.AreEqual("invoice content", multipart.Last().ReadAsStringAsync().GetAwaiter().GetResult());
                    Assert.AreEqual("upload:upload_0", multipart.Last().Headers.ContentDisposition!.Name!.Trim('"'));
                }
                else json = wire;
                using JsonDocument mutation = JsonDocument.Parse(json);
                Assert.AreEqual("orders", mutation.RootElement.GetProperty("tableId").GetString());
                Assert.AreEqual("schema_7", mutation.RootElement.GetProperty("schemaRevision").GetString());
                Assert.AreEqual("sha256:" + new string('a', 64), mutation.RootElement.GetProperty("expectedDigest").GetString());
                JsonElement operation = mutation.RootElement.GetProperty("operations")[0];
                Assert.AreEqual("setAttachments", operation.GetProperty("kind").GetString());
                Assert.AreEqual("row-1", operation.GetProperty("recordId").GetString());
                Assert.AreEqual("invoice", operation.GetProperty("fieldId").GetString());
                CollectionAssert.AreEqual(upload ? new[] { "upload_0" } : [], operation.GetProperty("uploadHandles").EnumerateArray().Select(item => item.GetString()).ToArray());
                CollectionAssert.AreEqual(upload ? Array.Empty<string>() : ["old.pdf"], operation.GetProperty("removeStoredNames").EnumerateArray().Select(item => item.GetString()).ToArray());
                Assert.AreEqual(upload ? 1 : 0, operation.GetProperty("uploadHandles").GetArrayLength());
                Assert.AreEqual(upload ? 0 : 1, operation.GetProperty("removeStoredNames").GetArrayLength());
                return Json("""{"contractVersion":"2.0","status":"applied"}""");
            });
            using var gateway = Gateway(handler);
            JsonElement result = await gateway.ApplyHostFileChangeAsync(JsonSerializer.SerializeToElement(new
            {
                tableId = "orders", recordId = "row-1", fieldId = "invoice", schemaRevision = "schema_7",
                expectedDigest = "sha256:" + new string('a', 64), hostPaths = upload ? new[] { path } : [],
                removeStoredNames = upload ? Array.Empty<string>() : ["old.pdf"],
            }), CancellationToken.None);
            Assert.AreEqual("applied", result.GetProperty("status").GetString());
            Assert.AreEqual(1, calls);
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    [DataRow("success")]
    [DataRow("variant")]
    [DataRow("truncated")]
    [DataRow("retired")]
    public async Task NativeAttachmentDownloadCommitsOnlyCompleteCurrentResult(string outcome)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-download-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        string path = Path.Combine(root, "发票.txt");
        File.WriteAllText(path, "old");
        var routes = new List<string>();
        try
        {
            var handler = new RecordingHandler(request =>
            {
                Assert.AreEqual("private-secret", request.Headers.GetValues("X-VibeTable-Session").Single());
                Assert.IsFalse(request.RequestUri!.ToString().Contains(root, StringComparison.Ordinal));
                routes.Add(request.RequestUri.AbsolutePath);
                Assert.AreEqual(HttpMethod.Get, request.Method);
                if (routes.Count == 1)
                {
                    Assert.AreEqual("?tableId=orders&recordId=r-1&fieldId=invoice&storedName=stored.txt" + (outcome == "variant" ? "&variant=thumb" : ""), request.RequestUri.Query);
                    return Json("""{"contractVersion":"2.0","downloadCapability":"opaque-capability"}""");
                }
                Assert.AreEqual("?capability=opaque-capability", request.RequestUri.Query);
                var response = new HttpResponseMessage(HttpStatusCode.OK) { Content = new ByteArrayContent(Encoding.UTF8.GetBytes("new")) };
                if (outcome == "truncated") response.Content.Headers.ContentLength = 100;
                return response;
            });
            using var gateway = Gateway(handler);
            using var caller = new CancellationTokenSource();
            var parameters = new Dictionary<string, object?>
            { ["tableId"] = "orders", ["recordId"] = "r-1", ["fieldId"] = "invoice", ["storedName"] = "stored.txt", ["outputPath"] = path };
            if (outcome == "variant") parameters["variant"] = "thumb";
            Task<JsonElement> pending = gateway.SaveHostFileAsync(JsonSerializer.SerializeToElement(parameters),
                commit => { if (outcome == "retired") { caller.Cancel(); caller.Token.ThrowIfCancellationRequested(); } commit(); }, caller.Token);
            if (outcome == "truncated") await Assert.ThrowsAsync<BackendUnavailableException>(() => pending);
            else if (outcome == "retired") await Assert.ThrowsAsync<OperationCanceledException>(() => pending);
            else
            {
                JsonElement receipt = await pending;
                Assert.IsTrue(receipt.GetProperty("saved").GetBoolean());
                Assert.AreEqual("2.0", receipt.GetProperty("contractVersion").GetString());
                Assert.AreEqual(3L, receipt.GetProperty("bytes").GetInt64());
            }
            Assert.AreEqual(outcome is "success" or "variant" ? "new" : "old", File.ReadAllText(path));
            CollectionAssert.AreEqual(new[] { "/api/vibetable/v1/files/token", "/api/vibetable/v1/attachments/download" }, routes);
            Assert.AreEqual(1, Directory.GetFiles(root).Length);
        }
        finally { Directory.Delete(root, recursive: true); }
    }
    [TestMethod]
    public async Task NativeAttachmentRestPreservesStructuredMutationConflict()
    {
        var handler = new RecordingHandler(_ => new HttpResponseMessage(HttpStatusCode.Conflict)
        {
            Content = new StringContent("""{"contractVersion":"2.0","code":"mutation.digest_conflict","message":"record changed","path":null,"details":{"recordId":"row-1"},"retryable":false}""", Encoding.UTF8, "application/json"),
        });
        using var gateway = Gateway(handler);
        RpcRemoteException error = await Assert.ThrowsAsync<RpcRemoteException>(() => gateway.ApplyHostFileChangeAsync(
            AttachmentRemoveParams(), CancellationToken.None));
        Assert.AreEqual(-32150, error.Code);
        Assert.AreEqual("Product data error", error.Message);
        JsonElement data = error.ErrorData!.Value;
        Assert.AreEqual("product_data_error", data.GetProperty("kind").GetString());
        Assert.AreEqual("mutation.digest_conflict", data.GetProperty("code").GetString());
        Assert.AreEqual("record changed", data.GetProperty("message").GetString());
        Assert.AreEqual(JsonValueKind.Null, data.GetProperty("path").ValueKind);
        Assert.AreEqual("row-1", data.GetProperty("details").GetProperty("recordId").GetString());
        Assert.IsFalse(data.GetProperty("retryable").GetBoolean());
    }

    private static JsonElement AttachmentRemoveParams() => JsonSerializer.SerializeToElement(new
    {
        tableId = "orders", recordId = "row-1", fieldId = "invoice", schemaRevision = "schema_7",
        expectedDigest = "sha256:" + new string('a', 64), hostPaths = Array.Empty<string>(), removeStoredNames = new[] { "old.pdf" },
    });

    [TestMethod]
    [DataRow("empty")]
    [DataRow("too-many")]
    [DataRow("digest")]
    [DataRow("variant")]
    public async Task NativeAttachmentInvalidInputsNeverReachHttp(string kind)
    {
        int calls = 0;
        using var gateway = Gateway(new RecordingHandler(_ => { calls++; return Json("{}"); }));
        var parameters = JsonSerializer.Deserialize<Dictionary<string, JsonElement>>(AttachmentRemoveParams().GetRawText())!;
        if (kind == "empty") parameters["removeStoredNames"] = JsonSerializer.SerializeToElement(Array.Empty<string>());
        if (kind == "too-many") parameters["hostPaths"] = JsonSerializer.SerializeToElement(Enumerable.Repeat("invalid", 33).ToArray());
        if (kind == "digest") parameters["expectedDigest"] = JsonSerializer.SerializeToElement("bad");
        if (kind == "variant")
        {
            await Assert.ThrowsAsync<JsonException>(() => gateway.SaveHostFileAsync(JsonSerializer.SerializeToElement(new
            { tableId = "orders", recordId = "r", fieldId = "f", storedName = "s", outputPath = Path.Combine(Path.GetTempPath(), "existing-parent.txt"), variant = "" }), action => action(), CancellationToken.None));
        }
        else await Assert.ThrowsAsync<JsonException>(() => gateway.ApplyHostFileChangeAsync(JsonSerializer.SerializeToElement(parameters), CancellationToken.None));
        Assert.AreEqual(0, calls);
    }

    [TestMethod]
    [DataRow("NaN")]
    [DataRow("Infinity")]
    [DataRow("-Infinity")]
    public async Task NativeAttachmentRejectsNonFiniteJsonResponses(string value)
    {
        using var gateway = Gateway(new RecordingHandler(_ => Json("{\"fields\":[" + value + "]}")));
        await Assert.ThrowsAsync<JsonException>(() => gateway.ApplyHostFileChangeAsync(AttachmentRemoveParams(), CancellationToken.None));
    }

    [TestMethod]
    public async Task NativeAttachmentTransportFailureDoesNotExposePrivateMessage()
    {
        using var gateway = Gateway(new RecordingHandler(_ => throw new HttpRequestException("secret local path")));
        BackendUnavailableException error = await Assert.ThrowsAsync<BackendUnavailableException>(() => gateway.ApplyHostFileChangeAsync(AttachmentRemoveParams(), CancellationToken.None));
        Assert.AreEqual("Product Sidecar is unavailable.", error.Message);
        Assert.IsNull(error.InnerException);
    }

}
