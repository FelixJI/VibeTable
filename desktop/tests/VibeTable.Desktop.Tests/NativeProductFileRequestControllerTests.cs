using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class NativeProductFileRequestControllerTests
{
    [TestMethod]
    public async Task UploadMaterializesTrustedPathsAndCorrelatesRpcResult()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            AttachmentSelection = new NativeAttachmentSelection(
                [@"C:\trusted\one.pdf", @"C:\trusted\two.png"],
                PickerWasShown: false),
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.uploadRequested",
            AttachmentPayload(includeDigest: true)));

        Assert.AreEqual(1, gateway.AttachmentChanges.Count);
        JsonElement parameters = gateway.AttachmentChanges.Single();
        CollectionAssert.AreEqual(
            new[] { @"C:\trusted\one.pdf", @"C:\trusted\two.png" },
            parameters.GetProperty("hostPaths")
                .EnumerateArray()
                .Select(value => value.GetString())
                .ToArray());
        Assert.IsEmpty(parameters.GetProperty("removeStoredNames").EnumerateArray());
        FakeWebReplySink.Reply response = sink.Replies.Single();
        Assert.AreEqual("file.uploadRequested", response.Type);
        Assert.AreEqual("request-file.uploadRequested", response.RequestId);
    }

    [TestMethod]
    public async Task InvalidMutationContextFailsBeforeTrustedPathRpc()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            AttachmentSelection = new NativeAttachmentSelection(
                [@"C:\trusted\one.pdf"],
                PickerWasShown: false),
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.uploadRequested",
            AttachmentPayload(includeDigest: false)));

        Assert.IsEmpty(gateway.AttachmentChanges);
        AssertFailure(sink.Replies.Single(), "ATTACHMENT_CONTEXT_INVALID");
    }

    [TestMethod]
    public async Task ReplacementPickerCancellationIsDistinctFromInvalidPayload()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            AttachmentSelection = new NativeAttachmentSelection(
                [],
                PickerWasShown: true),
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.replaceRequested",
            AttachmentPayload(includeDigest: true, storedName: "stored.bin")));

        Assert.IsEmpty(gateway.AttachmentChanges);
        AssertFailure(sink.Replies.Single(), "CANCELLED");
    }

    [TestMethod]
    public async Task PreviewUnavailableIsACorrelatedCapabilityOutcomeAfterMaterialization()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            PreviewPath = @"C:\preview\safe-report.pdf",
            PreviewFailure = new DocumentPreviewException(
                "没有安全预览器。",
                "PREVIEW_HANDLER_UNAVAILABLE"),
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.previewRequested",
            AttachmentPayload(
                includeDigest: false,
                storedName: "stored.pdf",
                originalName: @"..\safe-report.pdf")));

        Assert.AreEqual(1, gateway.AttachmentSaves.Count);
        Assert.AreEqual(
            host.PreviewPath,
            gateway.AttachmentSaves.Single().GetProperty("outputPath").GetString());
        CollectionAssert.AreEqual(new[] { host.PreviewPath }, host.PreviewedPaths);
        FakeWebReplySink.Reply response = sink.Replies.Single();
        Assert.AreEqual("file.previewRequested", response.Type);
        Assert.AreEqual("request-file.previewRequested", response.RequestId);
        JsonElement outcome = JsonSerializer.SerializeToElement(response.Payload);
        Assert.AreEqual("unavailable", outcome.GetProperty("outcome").GetString());
        Assert.AreEqual(
            "PREVIEW_HANDLER_UNAVAILABLE",
            outcome.GetProperty("reason").GetString());
        Assert.IsFalse(outcome.TryGetProperty("path", out _));
    }

    [TestMethod]
    public async Task PreviewOpenedProducesExactlyOneCorrelatedTerminalOutcome()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            PreviewPath = @"C:\preview\safe-report.pdf",
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.previewRequested",
            AttachmentPayload(
                includeDigest: false,
                storedName: "stored.pdf",
                originalName: "report.pdf")));

        FakeWebReplySink.Reply response = sink.Replies.Single();
        Assert.AreEqual("file.previewRequested", response.Type);
        Assert.AreEqual("request-file.previewRequested", response.RequestId);
        JsonElement outcome = JsonSerializer.SerializeToElement(response.Payload);
        Assert.AreEqual("opened", outcome.GetProperty("outcome").GetString());
        Assert.AreEqual(JsonValueKind.Null, outcome.GetProperty("reason").ValueKind);
        Assert.IsFalse(outcome.TryGetProperty("path", out _));
    }

    [TestMethod]
    public async Task SessionCancellationProducesNoAttachmentReply()
    {
        using var session = new CancellationTokenSource();
        session.Cancel();
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway
        {
            AttachmentChangeFailure = new OperationCanceledException(session.Token),
        };
        var host = new FakeNativeProductFileHost
        {
            AttachmentSelection = new NativeAttachmentSelection(
                [@"C:\trusted\cancelled.pdf"],
                PickerWasShown: false),
        };
        var controller = Controller(sink, gateway, host, () => session.Token);

        await controller.DispatchAsync(Request(
            "file.uploadRequested",
            AttachmentPayload(includeDigest: true)));

        Assert.IsEmpty(sink.Replies);
    }

    [TestMethod]
    public async Task ImportAndExportRegisterOnlyNativeSelectedPaths()
    {
        string importPath = Path.GetTempFileName();
        try
        {
            await File.WriteAllTextAsync(importPath, "one,two");
            var sink = new FakeWebReplySink();
            var gateway = new FakeProductFileGateway();
            var host = new FakeNativeProductFileHost
            {
                ImportSource = importPath,
                ExportTarget = @"C:\exports\report.csv",
            };
            var controller = Controller(sink, gateway, host);

            await controller.DispatchAsync(Request("data.importSourceRequested", "{}"));
            await controller.DispatchAsync(Request(
                "data.exportTargetRequested",
                """{"format":"csv","defaultName":"report.csv"}"""));

            Assert.AreEqual(
                Path.GetFullPath(importPath),
                gateway.ImportRegistrations.Single().GetProperty("path").GetString());
            Assert.AreEqual(
                new FileInfo(importPath).Length,
                gateway.ImportRegistrations.Single().GetProperty("sizeBytes").GetInt64());
            Assert.AreEqual(
                host.ExportTarget,
                gateway.ExportRegistrations.Single().GetProperty("path").GetString());
            Assert.HasCount(2, sink.Replies);
        }
        finally
        {
            File.Delete(importPath);
        }
    }

    [TestMethod]
    public async Task RemoveAndDownloadShareValidatedAttachmentIdentity()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            AttachmentTarget = @"C:\exports\downloaded.pdf",
        };
        var controller = Controller(sink, gateway, host);

        await controller.DispatchAsync(Request(
            "file.removeRequested",
            AttachmentPayload(includeDigest: true, storedName: "stored.pdf")));
        await controller.DispatchAsync(Request(
            "file.downloadRequested",
            AttachmentPayload(
                includeDigest: false,
                storedName: "stored.pdf",
                originalName: @"..\report.pdf")));

        JsonElement change = gateway.AttachmentChanges.Single();
        Assert.IsEmpty(change.GetProperty("hostPaths").EnumerateArray());
        CollectionAssert.AreEqual(
            new[] { "stored.pdf" },
            change.GetProperty("removeStoredNames")
                .EnumerateArray()
                .Select(value => value.GetString())
                .ToArray());
        Assert.AreEqual("report.pdf", host.AttachmentTargetSuggestions.Single());
        JsonElement save = gateway.AttachmentSaves.Single();
        Assert.AreEqual("stored.pdf", save.GetProperty("storedName").GetString());
        Assert.AreEqual(host.AttachmentTarget, save.GetProperty("outputPath").GetString());
        FakeWebReplySink.Reply download = sink.Replies.Single(
            reply => reply.Type == "file.downloadRequested");
        JsonElement outcome = JsonSerializer.SerializeToElement(download.Payload);
        Assert.AreEqual("saved", outcome.GetProperty("outcome").GetString());
        Assert.IsFalse(outcome.TryGetProperty("path", out _));
    }

    [TestMethod]
    public async Task DownloadPickerCancellationProducesExactlyOneCorrelatedTerminalOutcome()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var controller = Controller(sink, gateway, new FakeNativeProductFileHost());

        await controller.DispatchAsync(Request(
            "file.downloadRequested",
            AttachmentPayload(
                includeDigest: false,
                storedName: "stored.pdf",
                originalName: "report.pdf")));

        Assert.IsEmpty(gateway.AttachmentSaves);
        FakeWebReplySink.Reply response = sink.Replies.Single();
        Assert.AreEqual("file.downloadRequested", response.Type);
        Assert.AreEqual("request-file.downloadRequested", response.RequestId);
        JsonElement outcome = JsonSerializer.SerializeToElement(response.Payload);
        Assert.AreEqual("cancelled", outcome.GetProperty("outcome").GetString());
        Assert.IsFalse(outcome.TryGetProperty("path", out _));
    }

    [TestMethod]
    public async Task LegacyAttachmentNotificationsKeepSideEffectsWithoutPostingAnyReply()
    {
        foreach (Exception? previewFailure in new Exception?[]
        {
            null,
            new DocumentPreviewException(
                "No preview handler is installed.",
                "PREVIEW_HANDLER_UNAVAILABLE"),
            new IOException("preview failed"),
        })
        {
            var sink = new FakeWebReplySink();
            var gateway = new FakeProductFileGateway();
            var host = new FakeNativeProductFileHost { PreviewFailure = previewFailure };

            await Controller(sink, gateway, host).DispatchAsync(Notification(
                "file.previewRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

            Assert.HasCount(1, gateway.AttachmentSaves);
            CollectionAssert.AreEqual(new[] { host.PreviewPath }, host.PreviewedPaths);
            Assert.IsEmpty(sink.Replies);
        }

        var cancelSink = new FakeWebReplySink();
        var cancelHost = new FakeNativeProductFileHost();
        await Controller(cancelSink, new FakeProductFileGateway(), cancelHost)
            .DispatchAsync(Notification(
                "file.downloadRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        Assert.HasCount(1, cancelHost.AttachmentTargetSuggestions);
        Assert.IsEmpty(cancelSink.Replies);
    }

    [TestMethod]
    public async Task NativeAttachmentSessionCancellationProducesOneCorrelatedFailure()
    {
        using var session = new CancellationTokenSource();
        session.Cancel();
        var gateway = new FakeProductFileGateway
        {
            AttachmentSaveFailure = new OperationCanceledException(session.Token),
        };

        foreach (string type in new[] { "file.previewRequested", "file.downloadRequested" })
        {
            var sink = new FakeWebReplySink();
            var host = new FakeNativeProductFileHost
            {
                AttachmentTarget = @"C:\exports\report.pdf",
            };

            await Controller(sink, gateway, host, () => session.Token)
                .DispatchAsync(Request(
                    type,
                    AttachmentPayload(false, "stored.pdf", "report.pdf")));

            AssertFailure(sink.Replies.Single(), "CANCELLED");
        }
    }

    [TestMethod]
    public async Task PreviewCallbackCancellationWinsOverAHostSuccess()
    {
        using var session = new CancellationTokenSource();
        var sink = new FakeWebReplySink();
        var host = new FakeNativeProductFileHost
        {
            PreviewCallback = session.Cancel,
        };

        await Controller(sink, new FakeProductFileGateway(), host, () => session.Token)
            .DispatchAsync(Request(
                "file.previewRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        AssertFailure(sink.Replies.Single(), "CANCELLED");
    }

    [TestMethod]
    public async Task DownloadTargetCallbackCancellationWinsBeforeSaving()
    {
        using var session = new CancellationTokenSource();
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway();
        var host = new FakeNativeProductFileHost
        {
            AttachmentTarget = @"C:\exports\report.pdf",
            AttachmentTargetCallback = session.Cancel,
        };

        await Controller(sink, gateway, host, () => session.Token)
            .DispatchAsync(Request(
                "file.downloadRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        Assert.IsEmpty(gateway.AttachmentSaves);
        AssertFailure(sink.Replies.Single(), "CANCELLED");
    }

    [TestMethod]
    public async Task GatewayCompletionCancellationWinsBeforePreviewing()
    {
        using var session = new CancellationTokenSource();
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway
        {
            AttachmentSaveCallback = session.Cancel,
        };
        var host = new FakeNativeProductFileHost();

        await Controller(sink, gateway, host, () => session.Token)
            .DispatchAsync(Request(
                "file.previewRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        Assert.HasCount(1, gateway.AttachmentSaves);
        Assert.IsEmpty(host.PreviewedPaths);
        AssertFailure(sink.Replies.Single(), "CANCELLED");
    }

    [TestMethod]
    public async Task NativeAttachmentActionErrorsProduceOneCorrelatedFailure()
    {
        var previewSink = new FakeWebReplySink();
        var previewHost = new FakeNativeProductFileHost
        {
            PreviewFailure = new DocumentPreviewException(
                "预览器启动失败。",
                "PREVIEW_LAUNCH_FAILED"),
        };
        await Controller(previewSink, new FakeProductFileGateway(), previewHost)
            .DispatchAsync(Request(
                "file.previewRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        FakeWebReplySink.Reply previewFailure = previewSink.Replies.Single();
        Assert.AreEqual("request-file.previewRequested", previewFailure.RequestId);
        AssertFailure(previewFailure, "PREVIEW_LAUNCH_FAILED");

        var downloadSink = new FakeWebReplySink();
        var downloadGateway = new FakeProductFileGateway
        {
            AttachmentSaveFailure = new IOException("disk unavailable"),
        };
        var downloadHost = new FakeNativeProductFileHost
        {
            AttachmentTarget = @"C:\exports\report.pdf",
        };
        await Controller(downloadSink, downloadGateway, downloadHost)
            .DispatchAsync(Request(
                "file.downloadRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        FakeWebReplySink.Reply downloadFailure = downloadSink.Replies.Single();
        Assert.AreEqual("request-file.downloadRequested", downloadFailure.RequestId);
        AssertFailure(downloadFailure, "ATTACHMENT_DOWNLOAD_FAILED");
    }

    [TestMethod]
    public async Task NativeAttachmentHostErrorsProduceOneCorrelatedFailure()
    {
        var previewSink = new FakeWebReplySink();
        var previewHost = new FakeNativeProductFileHost
        {
            PreviewPathFailure = new IOException("preview directory unavailable"),
        };

        await Controller(previewSink, new FakeProductFileGateway(), previewHost)
            .DispatchAsync(Request(
                "file.previewRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        AssertFailure(previewSink.Replies.Single(), "ATTACHMENT_PREVIEW_FAILED");

        var downloadSink = new FakeWebReplySink();
        var downloadHost = new FakeNativeProductFileHost
        {
            AttachmentTargetFailure = new InvalidOperationException("dialog unavailable"),
        };

        await Controller(downloadSink, new FakeProductFileGateway(), downloadHost)
            .DispatchAsync(Request(
                "file.downloadRequested",
                AttachmentPayload(false, "stored.pdf", "report.pdf")));

        AssertFailure(downloadSink.Replies.Single(), "ATTACHMENT_DOWNLOAD_FAILED");
    }

    [TestMethod]
    public async Task UnavailableGatewayAndUnknownTypeFailClosed()
    {
        var sink = new FakeWebReplySink();
        var gateway = new FakeProductFileGateway { IsAvailable = false };
        var controller = Controller(sink, gateway, new FakeNativeProductFileHost());

        await controller.DispatchAsync(Request("data.importSourceRequested", "{}"));
        await controller.DispatchAsync(Request("file.rawRequested", "{}"));

        AssertFailure(sink.Replies[0], "BACKEND_UNAVAILABLE");
        AssertFailure(sink.Replies[1], "UNKNOWN_TYPE");
    }

    [TestMethod]
    public void HandlesOnlyTheClosedNativeProductFileUnion()
    {
        foreach (string type in new[]
        {
            "data.importSourceRequested",
            "data.exportTargetRequested",
            "file.uploadRequested",
            "file.replaceRequested",
            "file.removeRequested",
            "file.previewRequested",
            "file.downloadRequested",
        })
        {
            Assert.IsTrue(NativeProductFileRequestController.Handles(type), type);
        }
        Assert.IsFalse(NativeProductFileRequestController.Handles("file.rawRequested"));
        Assert.IsFalse(NativeProductFileRequestController.Handles("document.importRequested"));
    }

    private static NativeProductFileRequestController Controller(
        FakeWebReplySink sink,
        IProductFileRpcGateway gateway,
        INativeProductFileHost host,
        Func<CancellationToken>? sessionToken = null)
        => new(sink, gateway, host, sessionToken);

    private static RoutedWebRequest Request(string type, string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return new RoutedWebRequest(
            type,
            $"request-{type}",
            document.RootElement.Clone(),
            string.Empty);
    }

    private static RoutedWebRequest Notification(string type, string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return new RoutedWebRequest(
            type,
            RequestId: null,
            document.RootElement.Clone(),
            string.Empty);
    }

    private static string AttachmentPayload(
        bool includeDigest,
        string? storedName = null,
        string? originalName = null)
    {
        var payload = new Dictionary<string, object?>
        {
            ["tableId"] = "table-1",
            ["recordId"] = "record-1",
            ["fieldId"] = "field-1",
        };
        if (includeDigest)
        {
            payload["schemaRevision"] = "revision-1";
            payload["expectedDigest"] = "sha256:" + new string('a', 64);
        }
        if (storedName is not null)
            payload["storedName"] = storedName;
        if (originalName is not null)
            payload["originalName"] = originalName;
        return JsonSerializer.Serialize(payload);
    }

    private static void AssertFailure(FakeWebReplySink.Reply reply, string code)
    {
        Assert.AreEqual("operation.failed", reply.Type);
        Assert.IsNotNull(reply.RequestId);
        string expectedOperation = reply.RequestId["request-".Length..];
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        Assert.AreEqual(
            code,
            payload.GetProperty("code").GetString());
        Assert.AreEqual(
            expectedOperation,
            payload.GetProperty("operation").GetString());
    }

    private sealed class FakeNativeProductFileHost : INativeProductFileHost
    {
        public NativeAttachmentSelection AttachmentSelection { get; init; } = new([], false);
        public string? ImportSource { get; init; }
        public string? ExportTarget { get; init; }
        public string? AttachmentTarget { get; init; }
        public string PreviewPath { get; init; } = @"C:\preview\attachment.bin";
        public Exception? PreviewFailure { get; init; }
        public Exception? PreviewPathFailure { get; init; }
        public Exception? AttachmentTargetFailure { get; init; }
        public Action? PreviewCallback { get; init; }
        public Action? AttachmentTargetCallback { get; init; }
        public List<string> PreviewedPaths { get; } = [];
        public List<string> AttachmentTargetSuggestions { get; } = [];

        public string? SelectImportSource() => ImportSource;

        public string? SelectExportTarget(string format, string defaultName) => ExportTarget;

        public NativeAttachmentSelection SelectAttachmentSources(bool replacement)
            => AttachmentSelection;

        public string? SelectAttachmentTarget(string suggestedName)
        {
            AttachmentTargetSuggestions.Add(suggestedName);
            if (AttachmentTargetFailure is not null)
                throw AttachmentTargetFailure;
            AttachmentTargetCallback?.Invoke();
            return AttachmentTarget;
        }

        public string CreateAttachmentPreviewPath(string suggestedName)
            => PreviewPathFailure is null
                ? PreviewPath
                : throw PreviewPathFailure;

        public Task PreviewAttachmentAsync(string fullPath)
        {
            PreviewedPaths.Add(fullPath);
            PreviewCallback?.Invoke();
            return PreviewFailure is null
                ? Task.CompletedTask
                : Task.FromException(PreviewFailure);
        }
    }

    private sealed class FakeProductFileGateway : IProductFileRpcGateway
    {
        public bool IsAvailable { get; init; } = true;
        public Exception? AttachmentChangeFailure { get; init; }
        public Exception? AttachmentSaveFailure { get; init; }
        public Action? AttachmentSaveCallback { get; init; }
        public List<JsonElement> ImportRegistrations { get; } = [];
        public List<JsonElement> ExportRegistrations { get; } = [];
        public List<JsonElement> AttachmentChanges { get; } = [];
        public List<JsonElement> AttachmentSaves { get; } = [];

        public Task<JsonElement> RegisterImportSourceAsync(
            JsonElement parameters,
            CancellationToken cancellationToken)
        {
            ImportRegistrations.Add(parameters.Clone());
            return Success();
        }

        public Task<JsonElement> RegisterExportTargetAsync(
            JsonElement parameters,
            CancellationToken cancellationToken)
        {
            ExportRegistrations.Add(parameters.Clone());
            return Success();
        }

        public Task<JsonElement> ApplyAttachmentChangeAsync(
            JsonElement parameters,
            CancellationToken cancellationToken)
        {
            AttachmentChanges.Add(parameters.Clone());
            return AttachmentChangeFailure is null
                ? Success()
                : Task.FromException<JsonElement>(AttachmentChangeFailure);
        }

        public Task<JsonElement> SaveAttachmentAsync(
            JsonElement parameters,
            CancellationToken cancellationToken)
        {
            AttachmentSaves.Add(parameters.Clone());
            AttachmentSaveCallback?.Invoke();
            return AttachmentSaveFailure is null
                ? Success()
                : Task.FromException<JsonElement>(AttachmentSaveFailure);
        }

        private static Task<JsonElement> Success()
            => Task.FromResult(JsonSerializer.SerializeToElement(new { ok = true }));
    }
}
