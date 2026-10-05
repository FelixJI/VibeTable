using System.Net;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class SourceImportHostTests
{
    [TestMethod]
    public async Task CatalogRefreshFailureDoesNotReclassifyOrReexecuteCommittedMigration()
    {
        using var fixture = new Fixture();
        fixture.Registry.BindSourceImportCatalogRefresh(fixture.Snapshot,
            (_, _) => throw new IOException("synthetic catalog failure"));
        JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
        Assert.AreEqual("succeeded", result.GetProperty("state").GetString());
        Assert.AreEqual(1, result.GetProperty("result").GetProperty("created").GetInt32());
        Assert.AreEqual(1, fixture.Http.Executions);
        Assert.AreEqual(0, fixture.Http.ResultReads);
        Assert.AreEqual(0, fixture.Http.Discards);
    }

    [TestMethod]
    public async Task PreparationFailureAndForeignCatalogBindingNeverRefresh()
    {
        foreach (bool noTargets in new[] { true, false })
        {
            using var fixture = new Fixture();
            using var foreign = new Fixture();
            int refreshes = 0;
            fixture.Provider.ObserveMode = noTargets ? "failure" : "";
            fixture.Registry.BindSourceImportCatalogRefresh(noTargets ? fixture.Snapshot : foreign.Snapshot,
                (_, _) => { refreshes++; return Task.CompletedTask; });
            JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
            Assert.AreEqual(noTargets ? "failed" : "succeeded", result.GetProperty("state").GetString());
            Assert.AreEqual(0, refreshes);
        }
    }

    [TestMethod]
    [DataRow("succeeded")]
    [DataRow("failed")]
    [DataRow("cancelled")]
    [DataRow("unknown")]
    public async Task CommittedTargetsRefreshCatalogBeforeTerminalNotification(string outcome)
    {
        using var fixture = new Fixture();
        fixture.Http.Outcome = outcome;
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        bool projected = false;
        bool terminalBeforeCatalog = false;
        fixture.Registry.BindSourceImportCatalogRefresh(fixture.Snapshot, async (snapshot, token) =>
        {
            Assert.IsTrue(snapshot.Matches(fixture.Snapshot));
            entered.TrySetResult();
            await release.Task.WaitAsync(token);
            projected = true;
        });
        fixture.Registry.TaskChanged += change =>
        {
            if (change.GetProperty("state").GetString() is "succeeded" or "failed" or "cancelled")
                terminalBeforeCatalog = !projected;
        };
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual("running", fixture.Registry.Status(initial.GetProperty("taskId").GetString()!).GetProperty("state").GetString());
        release.TrySetResult();
        JsonElement terminal = await fixture.Terminal(initial);
        Assert.IsTrue(projected);
        Assert.IsFalse(terminalBeforeCatalog);
        Assert.AreEqual(outcome, terminal.GetProperty("result").GetProperty("state").GetString());
        Assert.AreEqual(1, terminal.GetProperty("result").GetProperty("created").GetInt32());
        Assert.AreEqual(1, fixture.Http.Executions);
    }

    [TestMethod]
    public async Task UploadingSourceIsVisibleWithRealPlannedCountsAndCanBeCancelled()
    {
        using var fixture = new Fixture(attachment: true);
        fixture.Http.HoldUpload = true;
        fixture.Http.RecordCount = 7;
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Http.Uploading.Task.WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement history = await fixture.History();
        JsonElement live = history.GetProperty("migrations")[0];
        Assert.AreEqual(initial.GetProperty("taskId").GetString(), live.GetProperty("jobId").GetString());
        Assert.AreEqual("running", live.GetProperty("state").GetString());
        Assert.AreEqual("attachments", live.GetProperty("stage").GetString());
        Assert.AreEqual(7, live.GetProperty("total").GetInt32());
        Assert.AreEqual(7, live.GetProperty("notSubmitted").GetInt32());
        Assert.AreEqual(0, live.GetProperty("created").GetInt32());
        Assert.AreEqual(0, live.GetProperty("unknownRecords").GetInt32());
        Assert.AreEqual("合成来源", live.GetProperty("sourceName").GetString());
        JsonElement field = live.GetProperty("fields")[0];
        Assert.AreEqual("snapshot", field.GetProperty("policy").GetString());
        Assert.AreEqual("", field.GetProperty("definition").GetString());
        Assert.IsFalse(live.GetRawText().Contains("PRIVATE_SOURCE_EXPRESSION", StringComparison.Ordinal));
        Assert.AreEqual(0, fixture.Http.Executions);
        await fixture.Invoker.InvokeAsync("task.cancel", JsonSerializer.SerializeToElement(new
            { taskId = live.GetProperty("jobId").GetString() }), CancellationToken.None);
        Assert.AreEqual("cancelled", (await fixture.Terminal(initial)).GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task ConcurrentPreparationsRemainSeparateAndOtherSnapshotsCannotSeeThem()
    {
        using var fixture = new Fixture();
        using var other = new Fixture(registry: fixture.Registry);
        fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        other.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        using var secondProvider = new Provider(false)
            { ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously) };
        string secondSession = fixture.Invoker.RegisterSourceImportProvider(secondProvider);
        fixture.Http.RecordCount = 7;
        JsonElement first = await fixture.Start(await fixture.Prepare());
        fixture.Http.RecordCount = 11;
        HostSourceImportPreview secondPreview = await fixture.Invoker.PrepareSourceImportAsync(secondSession,
            new HostSourceImportOptions(["table-1"], [], [], false), CancellationToken.None);
        JsonElement second = await fixture.Start(secondPreview);
        JsonElement unrelated = await other.Start(await other.Prepare());
        await Task.WhenAll(fixture.Provider.Observing.Task, secondProvider.Observing.Task,
            other.Provider.Observing.Task).WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement[] entries = (await fixture.History()).GetProperty("migrations").EnumerateArray().ToArray();
        Assert.AreEqual(2, entries.Length);
        Assert.AreEqual(7, entries.Single(e => e.GetProperty("jobId").GetString() == first.GetProperty("taskId").GetString())
            .GetProperty("notSubmitted").GetInt32());
        Assert.AreEqual(11, entries.Single(e => e.GetProperty("jobId").GetString() == second.GetProperty("taskId").GetString())
            .GetProperty("notSubmitted").GetInt32());
        Assert.AreEqual(1, (await other.History()).GetProperty("migrations").GetArrayLength());
        fixture.Registry.RequestCancel(first.GetProperty("taskId").GetString()!);
        await fixture.Terminal(first);
        await fixture.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement remaining = (await fixture.History()).GetProperty("migrations");
        Assert.AreEqual(2, remaining.GetArrayLength());
        Assert.AreEqual("cancelled", remaining.EnumerateArray().Single(e =>
            e.GetProperty("jobId").GetString() == first.GetProperty("taskId").GetString()).GetProperty("state").GetString());
        Assert.AreEqual("running", remaining.EnumerateArray().Single(e =>
            e.GetProperty("jobId").GetString() == second.GetProperty("taskId").GetString()).GetProperty("state").GetString());
        fixture.Registry.RequestCancel(second.GetProperty("taskId").GetString()!);
        await fixture.Terminal(second);
        await secondProvider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        other.Registry.RequestCancel(unrelated.GetProperty("taskId").GetString()!);
        await other.Terminal(unrelated);
        await other.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(0, fixture.Http.Executions);
        Assert.AreEqual(0, other.Http.Executions);
    }

    [TestMethod]
    public async Task DurableInterruptedReceiptGetsLiveStateButKeepsAuthoritativeCounts()
    {
        using var fixture = new Fixture();
        fixture.Http.HoldExecute = true;
        fixture.Http.Outcome = "interrupted";
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Http.Executing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement entry = (await fixture.History()).GetProperty("migrations")[0];
        Assert.AreEqual("running", entry.GetProperty("state").GetString());
        Assert.AreEqual(1, entry.GetProperty("created").GetInt32());
        Assert.AreEqual(2, entry.GetProperty("notSubmitted").GetInt32());
        Assert.AreEqual("local-table", entry.GetProperty("targets")[0].GetProperty("tableId").GetString());
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        await fixture.Terminal(initial);
        Assert.AreEqual("interrupted", (await fixture.History()).GetProperty("migrations")[0].GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task SubmittedJobWithoutDurableEntryFailsClosedInsteadOfInventingCounts()
    {
        using var fixture = new Fixture();
        fixture.Http.HoldExecute = true;
        fixture.Http.HideHistory = true;
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Http.Executing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => fixture.History());
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        await fixture.Terminal(initial);
    }

    [TestMethod]
    public async Task DurableTerminalStateWinsOverStillRunningHost()
    {
        using var fixture = new Fixture();
        fixture.Http.HoldExecute = true;
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Http.Executing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual("running", fixture.Registry.Status(initial.GetProperty("taskId").GetString()!).GetProperty("state").GetString());
        Assert.AreEqual("succeeded", (await fixture.History()).GetProperty("migrations")[0].GetProperty("state").GetString());
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        await fixture.Terminal(initial);
    }

    [TestMethod]
    public async Task UnclaimedUploadsAreDiscardedAfterDriftExceptionFailureAndCancellation()
    {
        foreach (string mode in new[] { "version", "structure", "failure", "upload", "cancel" })
        {
            using var fixture = new Fixture(attachment: true);
            fixture.Provider.ObserveMode = mode;
            fixture.Http.FailUpload = mode == "upload";
            if (mode == "cancel") fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
            HostSourceImportPreview preview = await fixture.Prepare();
            JsonElement initial = await fixture.Start(preview);
            if (mode == "cancel")
            {
                await fixture.Provider.Observing.Task.WaitAsync(TimeSpan.FromSeconds(5));
                fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
            }
            await fixture.Terminal(initial);
            await fixture.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
            JsonElement durable = (await fixture.History()).GetProperty("migrations")[0];
            Assert.AreEqual(mode == "cancel" ? "cancelled" : "failed", durable.GetProperty("state").GetString(), mode);
            Assert.AreEqual(0, durable.GetProperty("created").GetInt32(), mode);
            Assert.AreEqual(durable.GetProperty("total").GetInt32(), durable.GetProperty("notSubmitted").GetInt32(), mode);
            Assert.AreEqual(1, fixture.Http.Starts, mode);
            Assert.AreEqual(1, fixture.Http.Finishes, mode);
            Assert.AreEqual(1, fixture.Http.Discards, mode);
            Assert.AreEqual(preview.Token, fixture.Http.DiscardMetadata.GetProperty("token").GetString());
            Assert.AreEqual(7UL, fixture.Http.DiscardMetadata.GetProperty("sessionEpoch").GetUInt64());
            Assert.IsFalse(fixture.Http.Staged.Contains(preview.Token), mode);
            Assert.IsTrue(fixture.Http.Staged.Contains("other-plan"), mode);
            Assert.AreEqual(0, fixture.Http.Executions, mode);
        }
    }

    [TestMethod]
    public async Task AttachmentPermissionAndMissingPathFailuresSettleDurableAdmission()
    {
        foreach (string failure in new[] { "permission", "disk" })
        {
            using var fixture = new Fixture(attachment: true);
            fixture.Provider.AttachmentFailure = failure;
            JsonElement terminal = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
            Assert.AreEqual("failed", terminal.GetProperty("state").GetString());
            Assert.IsNotNull(fixture.Provider.AttachmentError);
            if (failure == "permission") Assert.IsInstanceOfType<UnauthorizedAccessException>(fixture.Provider.AttachmentError);
            else Assert.IsInstanceOfType<IOException>(fixture.Provider.AttachmentError);
            JsonElement durable = (await fixture.History()).GetProperty("migrations")[0];
            Assert.AreEqual("failed", durable.GetProperty("state").GetString());
            Assert.AreEqual(0, durable.GetProperty("created").GetInt32());
            Assert.AreEqual(1, durable.GetProperty("notSubmitted").GetInt32());
            Assert.AreEqual(1, fixture.Http.Starts);
            Assert.AreEqual(1, fixture.Http.Finishes);
            Assert.AreEqual(0, fixture.Http.Uploads);
            Assert.AreEqual(0, fixture.Http.Executions);
        }
    }

    [TestMethod]
    public async Task AdmissionAndSettlementAckFailuresNeverFabricateOrReplayExecution()
    {
        foreach (string failure in new[] { "start-before", "start-ack", "finish-before", "finish-ack" })
        {
            using var fixture = new Fixture();
            fixture.Http.LifecycleFailure = failure;
            fixture.Provider.ObserveMode = "failure";
            JsonElement terminal = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
            Assert.AreEqual(failure == "start-ack" ? "failed" : "aborted", terminal.GetProperty("state").GetString(), failure);
            JsonElement history = (await fixture.History()).GetProperty("migrations");
            if (failure == "start-before") Assert.AreEqual(0, history.GetArrayLength());
            else Assert.AreEqual(failure == "finish-before" ? "interrupted" : "failed", history[0].GetProperty("state").GetString(), failure);
            Assert.AreEqual(1, fixture.Http.Starts, failure);
            Assert.AreEqual(1, fixture.Http.Finishes, failure);
            Assert.AreEqual(0, fixture.Http.Executions, failure);
        }
    }

    [TestMethod]
    public async Task ClaimRejectionDiscardsButAmbiguousClaimedExecutionCannotLoseItsUploads()
    {
        foreach (bool claimed in new[] { false, true })
        {
            using var fixture = new Fixture(attachment: true);
            fixture.Http.RejectClaim = !claimed;
            fixture.Http.LoseAck = claimed;
            fixture.Http.FailResultRead = true;
            HostSourceImportPreview preview = await fixture.Prepare();
            await fixture.Terminal(await fixture.Start(preview));
            await fixture.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
            Assert.AreEqual(1, fixture.Http.Discards);
            Assert.AreEqual(claimed, fixture.Http.Staged.Contains(preview.Token));
            Assert.AreEqual(1, fixture.Http.Executions, "Never replay an ambiguous execute.");
        }
    }

    [TestMethod]
    public async Task RetiredGoSessionNeverDiscardsAnOldTokenThroughAnotherEpoch()
    {
        using var fixture = new Fixture(attachment: true);
        fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Provider.Observing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        fixture.Authority.Retire();
        await fixture.Terminal(initial);
        await fixture.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(0, fixture.Http.Discards);
    }

    [TestMethod]
    public async Task GoRunSurvivesInvokerDisposalAndPythonRetirement()
    {
        using var fixture = new Fixture();
        fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        HostSourceImportPreview preview = await fixture.Prepare();
        JsonElement initial = await fixture.Start(preview);
        await fixture.Provider.Observing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        fixture.Invoker.Dispose();
        fixture.Registry.RetireClient(null);
        fixture.Provider.ObserveGate.SetResult();
        JsonElement result = await fixture.Terminal(initial);
        Assert.AreEqual("succeeded", result.GetProperty("state").GetString());
        Assert.AreEqual(0, fixture.PythonStarts);
        Assert.AreEqual(1, fixture.Http.Executions);
        Assert.AreEqual(initial.GetProperty("taskId").GetString(), fixture.Http.JobId);
        Assert.IsTrue(fixture.Http.SessionHeaderSeen);
    }

    [TestMethod]
    public async Task SourceVersionOrStructureDriftAndObservationFailureNeverExecute()
    {
        foreach (string mode in new[] { "version", "structure", "failure" })
        {
            using var fixture = new Fixture();
            HostSourceImportPreview preview = await fixture.Prepare();
            fixture.Provider.ObserveMode = mode;
            JsonElement result = await fixture.Terminal(await fixture.Start(preview));
            Assert.AreEqual("failed", result.GetProperty("state").GetString(), mode);
            Assert.AreEqual(0, fixture.Http.Executions, mode);
        }
    }

    [TestMethod]
    public async Task ActualAttachmentBytesPrecedeExecutionAndFailureBlocksIt()
    {
        foreach (bool fail in new[] { false, true })
        {
            using var fixture = new Fixture(attachment: true);
            fixture.Http.FailUpload = fail;
            HostSourceImportPreview preview = await fixture.Prepare();
            JsonElement result = await fixture.Terminal(await fixture.Start(preview));
            Assert.AreEqual(fail ? "failed" : "succeeded", result.GetProperty("state").GetString());
            Assert.AreEqual(fail ? 0 : 1, fixture.Http.Executions);
            Assert.AreEqual(1, fixture.Http.Uploads);
            CollectionAssert.AreEqual(new byte[] { 0, 1, 2, 255 }, fixture.Http.UploadBytes);
            Assert.AreEqual("object-1", fixture.Http.UploadMetadata.GetProperty("key").GetProperty("objectId").GetString());
            Assert.AreEqual(preview.Token, fixture.Http.UploadMetadata.GetProperty("token").GetString());
        }
    }

    [TestMethod]
    public async Task CommitAckLossReadsOriginalJobOnceAndNeverResubmits()
    {
        using var fixture = new Fixture();
        fixture.Http.LoseAck = true;
        JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
        Assert.AreEqual("succeeded", result.GetProperty("state").GetString());
        Assert.AreEqual(1, fixture.Http.Executions);
        Assert.AreEqual(1, fixture.Http.ResultReads);
        Assert.AreEqual(1, result.GetProperty("result").GetProperty("created").GetInt32());
    }

    [TestMethod]
    public async Task UnavailableReceiptNeverClaimsSuccessOrRetries()
    {
        using var fixture = new Fixture();
        fixture.Http.LoseAck = true;
        fixture.Http.FailResultRead = true;
        JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
        Assert.AreEqual("aborted", result.GetProperty("state").GetString());
        Assert.AreEqual(JsonValueKind.Null, result.GetProperty("result").ValueKind);
        Assert.AreEqual(1, fixture.Http.Executions);
        Assert.AreEqual(1, fixture.Http.ResultReads);
    }

    [TestMethod]
    public async Task PartialGoOutcomePreservesCountsAndTargets()
    {
        foreach (string outcome in new[] { "failed", "cancelled", "unknown", "interrupted" })
        {
            using var fixture = new Fixture();
            fixture.Http.Outcome = outcome;
            JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
            Assert.AreEqual(outcome is "unknown" or "interrupted" ? "aborted" : outcome,
                result.GetProperty("state").GetString());
            JsonElement receipt = result.GetProperty("result");
            Assert.AreEqual(outcome, receipt.GetProperty("state").GetString());
            Assert.AreEqual(1, receipt.GetProperty("created").GetInt32());
            Assert.AreEqual(2, receipt.GetProperty("notSubmitted").GetInt32());
            Assert.AreEqual("local-table", receipt.GetProperty("targets")[0].GetProperty("tableId").GetString());
        }
    }

    [TestMethod]
    public async Task HistoryCombinesSeparateGoMigrationsWithoutStartingPython()
    {
        using var fixture = new Fixture();
        JsonElement result = await fixture.Terminal(await fixture.Start(await fixture.Prepare()));
        JsonElement history = await fixture.Invoker.InvokeAsync("data.importHistory",
            JsonSerializer.SerializeToElement(new { }), CancellationToken.None);
        Assert.AreEqual(0, history.GetProperty("items").GetArrayLength());
        Assert.AreEqual(1, history.GetProperty("migrations").GetArrayLength());
        Assert.AreEqual(result.GetProperty("taskId").GetString(),
            history.GetProperty("migrations")[0].GetProperty("jobId").GetString());
        Assert.AreEqual(0, fixture.PythonStarts);
    }

    [TestMethod]
    public async Task CancelImmediatelyAfterAdmissionDoesNotExecute()
    {
        using var fixture = new Fixture();
        fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        Assert.AreEqual("cancelled", (await fixture.Terminal(initial)).GetProperty("state").GetString());
        Assert.AreEqual("cancelled", (await fixture.History()).GetProperty("migrations")[0].GetProperty("state").GetString());
        Assert.AreEqual(1, fixture.Http.Starts);
        Assert.AreEqual(1, fixture.Http.Finishes);
        Assert.AreEqual(0, fixture.Http.Executions);
    }

    [TestMethod]
    public async Task RunningCancelReconcilesDurablePartialOutcome()
    {
        using var fixture = new Fixture();
        fixture.Http.HoldExecute = true;
        fixture.Http.Outcome = "cancelled";
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Http.Executing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        JsonElement result = await fixture.Terminal(initial);
        Assert.AreEqual("cancelled", result.GetProperty("state").GetString());
        Assert.AreEqual(1, result.GetProperty("result").GetProperty("created").GetInt32());
        Assert.AreEqual(1, fixture.Http.Executions);
        Assert.AreEqual(1, fixture.Http.ResultReads);
    }

    [TestMethod]
    public async Task CancelBeforeSubmissionStopsWithoutBusinessWrite()
    {
        using var fixture = new Fixture();
        fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
        JsonElement initial = await fixture.Start(await fixture.Prepare());
        await fixture.Provider.Observing.Task.WaitAsync(TimeSpan.FromSeconds(5));
        fixture.Registry.RequestCancel(initial.GetProperty("taskId").GetString()!);
        JsonElement result = await fixture.Terminal(initial);
        Assert.AreEqual("cancelled", result.GetProperty("state").GetString());
        Assert.AreEqual(0, fixture.Http.Executions);
    }

    [TestMethod]
    public async Task GoGenerationAndEpochRetirementAbortAndCancelProviderWork()
    {
        foreach (bool epoch in new[] { false, true })
        {
            using var fixture = new Fixture();
            fixture.Provider.ObserveGate = new(TaskCreationOptions.RunContinuationsAsynchronously);
            JsonElement initial = await fixture.Start(await fixture.Prepare());
            await fixture.Provider.Observing.Task.WaitAsync(TimeSpan.FromSeconds(5));
            if (epoch) fixture.Leases.Retire(); else fixture.Authority.Retire();
            JsonElement result = await fixture.Terminal(initial);
            Assert.AreEqual("aborted", result.GetProperty("state").GetString());
            await fixture.Provider.Disposed.Task.WaitAsync(TimeSpan.FromSeconds(5));
            Assert.AreEqual(0, fixture.Http.Executions);
        }
    }

    [TestMethod]
    public async Task RendererCannotSupplySourceObservationOrUseMissingProvider()
    {
        using var fixture = new Fixture();
        HostSourceImportPreview preview = await fixture.Prepare();
        await Assert.ThrowsExactlyAsync<JsonException>(() => fixture.Invoker.InvokeAsync("task.create",
            JsonSerializer.SerializeToElement(new { kind = "data.sourceImport", @params = new
            { providerSessionId = preview.ProviderSessionId, token = preview.Token, confirmed = true, observation = new { version = "forged" } } }),
            CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() => fixture.Invoker.InvokeAsync("task.create",
            JsonSerializer.SerializeToElement(new { kind = "data.sourceImport", @params = new
            { providerSessionId = "missing", token = preview.Token, confirmed = true } }), CancellationToken.None));
        Assert.AreEqual(0, fixture.PythonStarts);
        Assert.AreEqual(0, fixture.Http.Executions);
    }

    [TestMethod]
    public async Task FixedPackagedSourceHasCompositeIdsCycleAndRealAttachmentBytes()
    {
        using var provider = new TestModeSourceImport("success");
        HostSourceImportSnapshot source = await provider.ReadAsync(CancellationToken.None);
        Assert.AreEqual(3, source.Tables.Length);
        Assert.AreEqual(6, source.Tables.Sum(table => table.Records.Length));
        CollectionAssert.AreEqual(new[] { "r1", "r2" }, source.Tables[0].Records.Select(row => row.Id).ToArray());
        CollectionAssert.AreEqual(new[] { "r1", "r2" }, source.Tables[1].Records.Select(row => row.Id).ToArray());
        Assert.AreEqual("r2", source.Tables[2].Records[0].Values["ca"].GetString());
        Assert.AreEqual(2, source.Tables[0].Records[0].Values["ab"].GetArrayLength());
        Assert.AreEqual("ba", source.Tables[0].Fields.Single(field => field.Id == "ab").Relation!.TargetFieldId);
        HostSourceImportAttachment attachment = source.Attachments.Single();
        await using Stream stream = await provider.OpenAttachmentAsync(attachment, CancellationToken.None);
        using var content = new MemoryStream();
        await stream.CopyToAsync(content);
        Assert.AreEqual(attachment.Size, content.Length);
        CollectionAssert.AreEqual(new byte[] { 137, 80, 78, 71, 13, 10, 26, 10 }, content.ToArray()[..8]);
        Assert.AreEqual("image/png", attachment.Mime);
        Assert.AreEqual(source.Version, (await provider.ObserveAsync(CancellationToken.None)).Version);
        using var drift = new TestModeSourceImport("drift");
        Assert.AreNotEqual((await drift.ReadAsync(CancellationToken.None)).Version,
            (await drift.ObserveAsync(CancellationToken.None)).Version);
    }

    private sealed class Fixture : IDisposable
    {
        internal readonly FakeAuthority Authority = new();
        internal readonly FakeLeases Leases = new();
        internal readonly HostDataIoTaskRegistry Registry;
        internal readonly Provider Provider;
        internal readonly Peer Http;
        internal readonly HostProductRpcInvoker Invoker;
        internal readonly ProductSidecarGenerationSnapshot Snapshot;
        internal int PythonStarts;
        private readonly string _session;

        internal Fixture(bool attachment = false, HostDataIoTaskRegistry? registry = null)
        {
            Registry = registry ?? new HostDataIoTaskRegistry();
            Provider = new Provider(attachment);
            var identity = new ProductSidecarIdentity(Guid.NewGuid().ToString("D"), 7, 3, Guid.NewGuid().ToString("D"));
            var snapshot = new ProductSidecarGenerationSnapshot(Authority, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:12345/_/"), new Uri("http://127.0.0.1:12345/"),
                    "X-VibeTable-Session", "synthetic-source-session"), identity, [],
                action => Authority.Current && action());
            Snapshot = snapshot;
            Http = new Peer(Provider);
            Invoker = new HostProductRpcInvoker(null, snapshot, Leases, action => action(), handler: Http,
                tryUseGoCurrent: action => Authority.Current && action(), taskOwner: Registry,
                ensurePython: _ => { PythonStarts++; throw new InvalidOperationException("Python must not start."); });
            _session = Invoker.RegisterSourceImportProvider(Provider);
        }

        internal Task<HostSourceImportPreview> Prepare() => Invoker.PrepareSourceImportAsync(_session,
            new HostSourceImportOptions(["table-1"], [], [], false), CancellationToken.None);

        internal Task<JsonElement> Start(HostSourceImportPreview preview) => Invoker.InvokeAsync("task.create",
            JsonSerializer.SerializeToElement(new { kind = "data.sourceImport", @params = new
            { providerSessionId = preview.ProviderSessionId, token = preview.Token, confirmed = true } }), CancellationToken.None);

        internal Task<JsonElement> History() => Invoker.InvokeAsync("data.importHistory",
            JsonSerializer.SerializeToElement(new { }), CancellationToken.None);

        internal async Task<JsonElement> Terminal(JsonElement initial)
        {
            string id = initial.GetProperty("taskId").GetString()!;
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            while (true)
            {
                JsonElement status = Registry.Status(id);
                if (status.GetProperty("state").GetString() is "succeeded" or "failed" or "cancelled" or "aborted") return status;
                await Task.Delay(10, timeout.Token);
            }
        }

        public void Dispose() { Invoker.Dispose(); Registry.Dispose(); Http.Dispose(); Leases.Dispose(); }
    }

    private sealed class Provider(bool attachment) : IHostSourceImportProvider
    {
        internal TaskCompletionSource? ObserveGate;
        internal readonly TaskCompletionSource Observing = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal readonly TaskCompletionSource Disposed = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal string ObserveMode = "";
        internal string AttachmentFailure = "";
        internal Exception? AttachmentError;
        internal HostSourceImportAttachment[] Attachments => attachment
            ? [new("object-1", "table-1", "record-1", "file-1", "test.bin", "application/octet-stream", 4)] : [];
        public Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token) => Task.FromResult(new HostSourceImportSnapshot(
            "synthetic", "container-1", "合成来源", "v1", new("2026-10-01T00:00:00Z", "2026-10-01T00:00:01Z", "window"),
            [new("table-1", "源表", "t1", "field-1", [], [])], Attachments));
        public async Task<HostSourceImportObservation> ObserveAsync(CancellationToken token)
        {
            Observing.TrySetResult();
            if (ObserveGate is not null) await ObserveGate.Task.WaitAsync(token);
            if (ObserveMode == "failure") throw new IOException("Synthetic observation failure.");
            return new(ObserveMode == "version" ? "v2" : "v1",
                new Dictionary<string, string> { ["table-1"] = ObserveMode == "structure" ? "t2" : "t1" });
        }
        public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment item, CancellationToken token)
        {
            try
            {
                if (AttachmentFailure == "permission") return Task.FromResult<Stream>(
                    File.OpenRead(Path.TrimEndingDirectorySeparator(Path.GetTempPath())));
                if (AttachmentFailure == "disk") return Task.FromResult<Stream>(File.OpenRead(
                    Path.Combine(Path.GetTempPath(), "vibetable-source-missing-" + Guid.NewGuid().ToString("N"), "attachment.bin")));
                return Task.FromResult<Stream>(new MemoryStream([0, 1, 2, 255], writable: false));
            }
            catch (Exception error) { AttachmentError = error; throw; }
        }
        public void Dispose() => Disposed.TrySetResult();
    }

    private sealed class Peer(Provider provider) : HttpMessageHandler
    {
        internal int Executions, Uploads, ResultReads, Discards, Starts, Finishes;
        private readonly System.Collections.Concurrent.ConcurrentDictionary<string, int> _plans = new();
        private readonly System.Collections.Concurrent.ConcurrentDictionary<string, JsonElement> _receipts = new();
        private int _previews;
        internal int RecordCount = 1;
        internal bool LoseAck, FailUpload, FailResultRead, SessionHeaderSeen, HoldExecute, HoldUpload, HideHistory, RejectClaim;
        internal readonly TaskCompletionSource Executing = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal readonly TaskCompletionSource Uploading = new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal readonly HashSet<string> Staged = ["other-plan"];
        private readonly HashSet<string> _claimed = [];
        internal string Outcome = "succeeded";
        internal string LifecycleFailure = "";
        internal string? JobId;
        internal byte[]? UploadBytes;
        internal JsonElement UploadMetadata;
        internal JsonElement DiscardMetadata;

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            SessionHeaderSeen |= request.Headers.TryGetValues("X-VibeTable-Session", out var values)
                && values.Single() == "synthetic-source-session";
            string path = request.RequestUri!.AbsolutePath;
            if (path.EndsWith("/source-import/history", StringComparison.Ordinal))
                return Reply(new { contract = HostSourceImportResult.ContractName,
                    entries = HideHistory ? [] : _receipts.Select(pair =>
                        pair.Key == JobId && Executions > 0 && !RejectClaim ? ResultElement() : pair.Value).ToArray() });
            if (path.EndsWith("/import-history", StringComparison.Ordinal))
                return Reply(new { items = Array.Empty<object>() });
            if (path.EndsWith("/preview", StringComparison.Ordinal))
            {
                string planToken = "mip1.synthetic-" + Interlocked.Increment(ref _previews);
                _plans[planToken] = RecordCount;
                return Reply(new
            {
                contract = HostSourceImportResult.ContractName, token = planToken,
                expiresAt = DateTimeOffset.UtcNow.AddMinutes(5).ToUnixTimeSeconds(),
                plan = new { provider = "synthetic", containerId = "container-1", displayName = "合成来源", version = "v1", canApply = true,
                    readWindow = new { startedAt = "2026-10-01T00:00:00Z", finishedAt = "2026-10-01T00:00:01Z", consistency = "window" },
                    fields = new[] { new { source = new { provider = "synthetic", containerId = "container-1", tableId = "table-1", fieldId = "field-1" },
                        kind = "formula", policy = "snapshot", definition = "PRIVATE_SOURCE_EXPRESSION" } },
                    diagnostics = Array.Empty<object>(),
                    tables = new[] { new { sourceId = "table-1", version = "t1", recordCount = RecordCount } }, attachments = provider.Attachments },
            });
            }
            if (path.EndsWith("/start", StringComparison.Ordinal) || path.EndsWith("/finish", StringComparison.Ordinal))
            {
                token.ThrowIfCancellationRequested();
                using JsonDocument doc = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
                string plan = doc.RootElement.GetProperty("token").GetString()!;
                string job = doc.RootElement.GetProperty("jobId").GetString()!;
                Assert.AreEqual(7UL, doc.RootElement.GetProperty("sessionEpoch").GetUInt64());
                string state = "interrupted";
                if (path.EndsWith("/start", StringComparison.Ordinal))
                {
                    Interlocked.Increment(ref Starts);
                    if (LifecycleFailure == "start-before") return new(HttpStatusCode.ServiceUnavailable);
                }
                else
                {
                    Interlocked.Increment(ref Finishes);
                    if (LifecycleFailure == "finish-before") return new(HttpStatusCode.ServiceUnavailable);
                    if (_claimed.Contains(plan)) return new(HttpStatusCode.Conflict);
                    if (!_receipts.ContainsKey(job)) return new(HttpStatusCode.NotFound);
                    state = doc.RootElement.GetProperty("state").GetString()!;
                }
                JsonElement receipt = JsonSerializer.SerializeToElement(new
                {
                    contract = HostSourceImportResult.ContractName, jobId = job, state, stage = "preparing",
                    provider = "synthetic", containerId = "container-1", sourceName = "合成来源",
                    created = 0, total = _plans[plan], notSubmitted = _plans[plan], unknownRecords = 0, sessionEpoch = 7,
                    startedAt = "2026-10-05T00:00:00Z", finishedAt = state == "interrupted" ? null : "2026-10-05T00:00:01Z",
                    readWindow = new { startedAt = "2026-10-01T00:00:00Z", finishedAt = "2026-10-01T00:00:01Z", consistency = "window" },
                    fields = new[] { new { source = new { provider = "synthetic", containerId = "container-1", tableId = "table-1", fieldId = "field-1" },
                        kind = "formula", policy = "snapshot", definition = "" } },
                    targets = Array.Empty<object>(), batches = Array.Empty<object>(), diagnostics = Array.Empty<object>(),
                });
                _receipts[job] = receipt;
                if ((state == "interrupted" && LifecycleFailure == "start-ack")
                    || (state != "interrupted" && LifecycleFailure == "finish-ack"))
                    throw new HttpRequestException("Synthetic lifecycle ACK loss.");
                return Reply(receipt);
            }
            if (path.EndsWith("/upload", StringComparison.Ordinal))
            {
                Assert.IsTrue(Starts > 0, "Attachment work requires durable admission first.");
                Uploads++;
                var form = (MultipartFormDataContent)request.Content!;
                foreach (HttpContent part in form)
                {
                    string? name = part.Headers.ContentDisposition?.Name?.Trim('"');
                    if (name == "file") UploadBytes = await part.ReadAsByteArrayAsync(token);
                    if (name == "metadata")
                    {
                        using JsonDocument doc = JsonDocument.Parse(await part.ReadAsStringAsync(token));
                        UploadMetadata = doc.RootElement.Clone();
                    }
                }
                Staged.Add(UploadMetadata.GetProperty("token").GetString()!);
                Uploading.TrySetResult();
                if (HoldUpload) await Task.Delay(Timeout.InfiniteTimeSpan, token);
                return FailUpload ? new(HttpStatusCode.InternalServerError) : Reply(new { uploaded = true });
            }
            if (path.EndsWith("/discard", StringComparison.Ordinal))
            {
                token.ThrowIfCancellationRequested();
                Discards++;
                using JsonDocument doc = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
                DiscardMetadata = doc.RootElement.Clone();
                string plan = DiscardMetadata.GetProperty("token").GetString()!;
                if (_claimed.Contains(plan)) return new(HttpStatusCode.Conflict);
                Staged.Remove(plan);
                return Reply(new { discarded = true });
            }
            if (path.EndsWith("/execute", StringComparison.Ordinal))
            {
                Executions++;
                using JsonDocument doc = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
                JobId = doc.RootElement.GetProperty("jobId").GetString();
                if (RejectClaim) return new(HttpStatusCode.BadRequest);
                _claimed.Add(doc.RootElement.GetProperty("token").GetString()!);
                Executing.TrySetResult();
                if (HoldExecute) await Task.Delay(Timeout.InfiniteTimeSpan, token);
                if (provider.Attachments.Length != Uploads) throw new InvalidOperationException("Executed before all attachment uploads.");
                if (LoseAck) throw new HttpRequestException("Synthetic lost ACK after commit.");
                return Result();
            }
            if (path.Contains("/result/", StringComparison.Ordinal))
            {
                ResultReads++;
                Assert.IsTrue(path.EndsWith("/" + JobId, StringComparison.Ordinal));
                return FailResultRead ? new(HttpStatusCode.InternalServerError)
                    : RejectClaim ? Reply(_receipts[JobId!]) : Result();
            }
            throw new InvalidOperationException("Unexpected private route: " + path);
        }

        private HttpResponseMessage Result() => Reply(ResultWire());
        private JsonElement ResultElement() => JsonSerializer.SerializeToElement(ResultWire());
        private object ResultWire() => new
        {
            contract = HostSourceImportResult.ContractName, jobId = JobId, state = Outcome,
            stage = Outcome == "succeeded" ? "settled" : "records",
            finishedAt = HoldExecute && Outcome == "interrupted" ? null : "2026-10-05T00:00:00Z",
            created = 1, total = Outcome == "succeeded" ? 1 : 3, notSubmitted = Outcome == "succeeded" ? 0 : 2,
            unknownRecords = 0, sessionEpoch = 7,
            targets = new[] { new { sourceTableId = "table-1", tableId = "local-table", name = "目标表" } },
            batches = Array.Empty<object>(), diagnostics = Array.Empty<object>(),
        };
        private static HttpResponseMessage Reply(object value) => new(HttpStatusCode.OK)
        { Content = new StringContent(JsonSerializer.Serialize(value, new JsonSerializerOptions(JsonSerializerDefaults.Web))) };
    }

    private sealed class FakeAuthority : IProductSidecarGenerationAuthority
    {
        internal bool Current = true;
        public event Action? CurrentChanged;
        public bool TryUseCurrent(ProductSidecarGenerationSnapshot snapshot, Func<bool> action) => Current && action();
        internal void Retire() { Current = false; CurrentChanged?.Invoke(); }
    }

    private sealed class FakeLeases : IWorkspaceHostEpochLeaseSource, IDisposable
    {
        private readonly CancellationTokenSource _epoch = new();
        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => !_epoch.IsCancellationRequested;
        public bool TryCaptureHost(Guid workspaceId, ulong sessionEpoch, Guid operationId, out WorkspaceRequestEpochLease? lease)
        {
            lease = new(new WorkspaceWireScope { Scope = "workspace", WorkspaceId = workspaceId,
                SessionEpoch = sessionEpoch, OperationId = operationId, Sequence = 1 }, _epoch.Token, () => { });
            return !_epoch.IsCancellationRequested;
        }
        internal void Retire() => _epoch.Cancel();
        public void Dispose() => _epoch.Dispose();
    }
}
