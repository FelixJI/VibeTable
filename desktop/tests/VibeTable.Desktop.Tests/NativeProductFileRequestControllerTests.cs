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
    public async Task PreviewSavesBeforeOpeningAndPreservesPreviewFailureCode()
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
        AssertFailure(sink.Replies.Single(), "PREVIEW_HANDLER_UNAVAILABLE");
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
        Assert.AreEqual(
            code,
            JsonSerializer.SerializeToElement(reply.Payload)
                .GetProperty("code")
                .GetString());
    }

    private sealed class FakeNativeProductFileHost : INativeProductFileHost
    {
        public NativeAttachmentSelection AttachmentSelection { get; init; } = new([], false);
        public string? ImportSource { get; init; }
        public string? ExportTarget { get; init; }
        public string? AttachmentTarget { get; init; }
        public string PreviewPath { get; init; } = @"C:\preview\attachment.bin";
        public Exception? PreviewFailure { get; init; }
        public List<string> PreviewedPaths { get; } = [];
        public List<string> AttachmentTargetSuggestions { get; } = [];

        public string? SelectImportSource() => ImportSource;

        public string? SelectExportTarget(string format, string defaultName) => ExportTarget;

        public NativeAttachmentSelection SelectAttachmentSources(bool replacement)
            => AttachmentSelection;

        public string? SelectAttachmentTarget(string suggestedName)
        {
            AttachmentTargetSuggestions.Add(suggestedName);
            return AttachmentTarget;
        }

        public string CreateAttachmentPreviewPath(string suggestedName) => PreviewPath;

        public Task PreviewAttachmentAsync(string fullPath)
        {
            PreviewedPaths.Add(fullPath);
            return PreviewFailure is null
                ? Task.CompletedTask
                : Task.FromException(PreviewFailure);
        }
    }

    private sealed class FakeProductFileGateway : IProductFileRpcGateway
    {
        public bool IsAvailable { get; init; } = true;
        public Exception? AttachmentChangeFailure { get; init; }
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
            return Success();
        }

        private static Task<JsonElement> Success()
            => Task.FromResult(JsonSerializer.SerializeToElement(new { ok = true }));
    }
}
