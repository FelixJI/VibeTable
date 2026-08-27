using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class UpdateActivationSettlementTests
{
    private string? _root;

    [TestCleanup]
    public void Cleanup()
    {
        if (_root is not null && Directory.Exists(_root))
        {
            Directory.Delete(_root, recursive: true);
        }
    }

    [TestMethod]
    public async Task FailedHealthIsDurableBeforeHostExitAndDoesNotMutateProductData()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("failed", 'a');
        var watchdog = new UpdateProcessIdentity(
            901,
            new DateTimeOffset(2026, 8, 27, 8, 0, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(
            902,
            new DateTimeOffset(2026, 8, 27, 8, 0, 1, TimeSpan.Zero));
        const string groupId = "owned-group-1";
        string launchNonce = new('b', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            groupId,
            launchNonce);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        string targetExecutable = Path.Combine(plan.TargetRoot, "VibeTable.Next.exe");
        string backupExecutable = Path.Combine(plan.StagingRoot, "backup", "VibeTable.Next.exe");
        string workspaceFile = Path.Combine(Root(), "workspace", "records.db");
        string unknownFile = Path.Combine(plan.TargetRoot, "user-data.db");
        string targetBefore = File.ReadAllText(targetExecutable);
        string backupBefore = File.ReadAllText(backupExecutable);
        string workspaceBefore = File.ReadAllText(workspaceFile);
        string unknownBefore = File.ReadAllText(unknownFile);
        var lifetime = new RecordingHostLifetime(() =>
        {
            using JsonDocument journal = JsonDocument.Parse(File.ReadAllText(pointerPath));
            Assert.AreEqual(
                "rollbackRequested",
                journal.RootElement.GetProperty("state").GetString());
            Assert.AreEqual(
                "workspaceHealthProbeFailed",
                journal.RootElement.GetProperty("failureCode").GetString());
            Assert.AreEqual(targetBefore, File.ReadAllText(targetExecutable));
            Assert.AreEqual(backupBefore, File.ReadAllText(backupExecutable));
            Assert.AreEqual(workspaceBefore, File.ReadAllText(workspaceFile));
            Assert.AreEqual(unknownBefore, File.ReadAllText(unknownFile));
        });

        UpdateActivationStartupResolution resolution =
            PendingUpdateActivationJournal.ResolveStartup(
                ["--claim-update", launchNonce],
                plan.TargetRoot,
                updated,
                lifetime,
                _ => Task.FromResult(true),
                _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Proceed, resolution.Disposition);
        Assert.IsNotNull(resolution.Settlement);
        await resolution.Settlement.CompleteHealthCheckAsync(
            new UpdateActivationHealth.Failed(
                UpdateActivationFailureCode.WorkspaceHealthProbeFailed),
            CancellationToken.None);

        Assert.AreEqual(1, lifetime.ExitRequests);
        Assert.AreEqual(1, lifetime.ExitCode);
    }

    [TestMethod]
    public async Task RepeatingFailedHealthIsIdempotentButHealthyConflictFailsClosed()
    {
        (UpdateApplyPlan plan, IUpdateActivationSettlement settlement, RecordingHostLifetime lifetime) =
            CreateClaimedSettlement("failure-idempotency", 'c');
        var failed = new UpdateActivationHealth.Failed(
            UpdateActivationFailureCode.WorkspaceHealthProbeFailed);

        await settlement.CompleteHealthCheckAsync(failed, CancellationToken.None);
        await settlement.CompleteHealthCheckAsync(failed, CancellationToken.None);
        ReleaseUpdateException conflict = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => settlement.CompleteHealthCheckAsync(
                HealthyReceipt(),
                CancellationToken.None));

        Assert.AreEqual("UPDATE_ACTIVATION_INVALID", conflict.Code);
        Assert.AreEqual(1, lifetime.ExitRequests);
        Assert.AreEqual(
            "rollbackRequested",
            ReadState(PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
    }

    [TestMethod]
    public async Task HealthyHealthIsDurableBeforeWatchdogExitAndCleanup()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("healthy", 'd');
        var watchdog = new UpdateProcessIdentity(
            911,
            new DateTimeOffset(2026, 8, 27, 8, 1, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(
            912,
            new DateTimeOffset(2026, 8, 27, 8, 1, 1, TimeSpan.Zero));
        string nonce = new('e', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, "owned-healthy", nonce);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        bool watchdogWaited = false;
        bool cleanupCalled = false;
        var lifetime = new RecordingHostLifetime(() => Assert.Fail("Healthy must not exit host."));
        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            plan.TargetRoot,
            updated,
            lifetime,
            actual =>
            {
                Assert.AreEqual(watchdog, actual);
                Assert.AreEqual("confirmed", ReadState(pointerPath));
                watchdogWaited = true;
                return Task.FromResult(true);
            },
            current =>
            {
                Assert.IsTrue(watchdogWaited);
                Assert.AreEqual(
                    Path.GetFullPath(plan.StagingRoot).TrimEnd(Path.DirectorySeparatorChar),
                    Path.GetFullPath(current.StagingRoot).TrimEnd(Path.DirectorySeparatorChar));
                Assert.AreEqual(plan.TargetVersion, current.TargetVersion);
                Assert.AreEqual("confirmed", ReadState(pointerPath));
                cleanupCalled = true;
                Directory.Delete(current.StagingRoot, recursive: true);
                return true;
            });

        await resolution.Settlement!.CompleteHealthCheckAsync(
            HealthyReceipt(),
            CancellationToken.None);

        Assert.IsTrue(watchdogWaited);
        Assert.IsTrue(cleanupCalled);
        Assert.AreEqual(0, lifetime.ExitRequests);
        Assert.IsFalse(File.Exists(pointerPath));
    }

    [TestMethod]
    public async Task RepeatingHealthyAfterCompletedCleanupIsIdempotent()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("healthy-idempotent", '8');
        var watchdog = new UpdateProcessIdentity(913, DateTimeOffset.UtcNow);
        var updated = new UpdateProcessIdentity(914, watchdog.StartedAtUtc.AddSeconds(1));
        string nonce = new('9', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            "owned-healthy-idempotent",
            nonce);
        int cleanupCalls = 0;
        UpdateActivationStartupResolution resolution =
            PendingUpdateActivationJournal.ResolveStartup(
                ["--claim-update", nonce],
                plan.TargetRoot,
                updated,
                new RecordingHostLifetime(() => Assert.Fail("Healthy must not exit host.")),
                _ => Task.FromResult(true),
                current =>
                {
                    cleanupCalls++;
                    Directory.Delete(current.StagingRoot, recursive: true);
                    return true;
                });

        await resolution.Settlement!.CompleteHealthCheckAsync(
            HealthyReceipt(),
            CancellationToken.None);
        await resolution.Settlement.CompleteHealthCheckAsync(
            HealthyReceipt(),
            CancellationToken.None);

        Assert.AreEqual(1, cleanupCalls);
        Assert.IsFalse(File.Exists(
            PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot)));
    }

    [TestMethod]
    [DataRow("prepared")]
    [DataRow("launchingUpdatedApp")]
    [DataRow("awaitingHealth")]
    [DataRow("rollbackRequested")]
    [DataRow("rollbackWorkerLaunching")]
    [DataRow("rollbackRestoring")]
    [DataRow("rolledBack")]
    [DataRow("rollbackFailed")]
    [DataRow("futureState")]
    public void PendingOrRecoveryStateWithoutExactClaimFailsClosedBeforeBootstrap(string state)
    {
        UpdateApplyPlan plan = CreateAppliedPlan($"blocked-{state}", '1');
        var watchdog = new UpdateProcessIdentity(
            931,
            new DateTimeOffset(2026, 8, 27, 8, 3, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        SetJournalState(pointerPath, state);

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            new UpdateProcessIdentity(932, watchdog.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => Assert.Fail("Blocked startup must not exit via settlement.")),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
        Assert.IsNull(resolution.Settlement);
        Assert.IsTrue(File.Exists(pointerPath));
    }

    [TestMethod]
    public void ClaimArgumentWithoutPendingJournalFailsClosed()
    {
        string target = Path.Combine(Root(), "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", new string('2', 64)],
            target,
            new UpdateProcessIdentity(940, DateTimeOffset.UtcNow),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
    }

    [TestMethod]
    public void VersionOnePreparedJournalRemainsReadable()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("v1", '3');
        var updater = new UpdateProcessIdentity(
            941,
            new DateTimeOffset(2026, 8, 27, 8, 4, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        ConvertJournalToVersionOne(pointerPath);

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            [
                "--cleanup-update", plan.StagingRoot,
                "--updater-pid", updater.ProcessId.ToString(),
                "--update-token", plan.Token,
            ],
            plan.TargetRoot,
            new UpdateProcessIdentity(942, updater.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Proceed, resolution.Disposition);
        Assert.IsNotNull(resolution.Settlement);
    }

    [TestMethod]
    public void VersionOneConfirmedJournalBlocksBootstrapUntilCleanupSucceeds()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("v1-confirmed-cleanup", 'd');
        var updater = new UpdateProcessIdentity(
            943,
            new DateTimeOffset(2026, 8, 27, 8, 4, 30, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, updater);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        ConvertJournalToVersionOne(pointerPath);
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        journal["state"] = "confirmed";
        journal["confirmedAt"] = "2026-08-27T08:05:00+00:00";
        File.WriteAllText(pointerPath, journal.ToJsonString());

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            new UpdateProcessIdentity(944, updater.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => false);

        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
        Assert.AreEqual("UPDATE_CLEANUP_FAILED", resolution.ErrorCode);
        Assert.IsTrue(File.Exists(pointerPath));
    }

    [TestMethod]
    [DataRow("schema")]
    [DataRow("field")]
    public void InvalidOrUnknownVersionTwoJournalFailsClosedAndIsRetained(string mutation)
    {
        UpdateApplyPlan plan = CreateAppliedPlan($"invalid-{mutation}", '4');
        var watchdog = new UpdateProcessIdentity(
            951,
            new DateTimeOffset(2026, 8, 27, 8, 5, 0, TimeSpan.Zero));
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        if (mutation == "schema")
        {
            journal["schemaVersion"] = 3;
        }
        else
        {
            journal["futureField"] = true;
        }
        File.WriteAllText(pointerPath, journal.ToJsonString());

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            new UpdateProcessIdentity(952, watchdog.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
        Assert.IsTrue(File.Exists(pointerPath));
    }

    [TestMethod]
    public async Task ConfirmedCleanupResumesBeforeOrdinaryBootstrap()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("resume-confirmed-v2", '5');
        var watchdog = new UpdateProcessIdentity(
            961,
            new DateTimeOffset(2026, 8, 27, 8, 6, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(962, watchdog.StartedAtUtc.AddSeconds(1));
        string nonce = new('6', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, "owned-resume", nonce);
        UpdateActivationStartupResolution first = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            plan.TargetRoot,
            updated,
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => false);
        await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(() =>
            first.Settlement!.CompleteHealthCheckAsync(
                HealthyReceipt(),
                CancellationToken.None));
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        Assert.AreEqual("confirmed", ReadState(pointerPath));

        bool cleanupCompleted = false;
        UpdateActivationStartupResolution resumed = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            new UpdateProcessIdentity(963, updated.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            current =>
            {
                Directory.Delete(current.StagingRoot, recursive: true);
                cleanupCompleted = true;
                return true;
            });

        Assert.AreEqual(UpdateActivationStartupDisposition.Proceed, resumed.Disposition);
        Assert.IsTrue(cleanupCompleted);
        Assert.IsNull(resumed.Settlement);
        Assert.IsFalse(File.Exists(pointerPath));
    }

    [TestMethod]
    [DataRow("watchdogProcessId")]
    [DataRow("watchdogStartedAtUtc")]
    public async Task MalformedVersionTwoConfirmedStateReturnsBlocked(string missingField)
    {
        UpdateApplyPlan plan = CreateAppliedPlan($"malformed-confirmed-{missingField}", 'e');
        var watchdog = new UpdateProcessIdentity(
            971,
            new DateTimeOffset(2026, 8, 27, 8, 7, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(972, watchdog.StartedAtUtc.AddSeconds(1));
        string nonce = new('7', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, "owned-confirmed", nonce);
        UpdateActivationStartupResolution first = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            plan.TargetRoot,
            updated,
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => false);
        await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(() =>
            first.Settlement!.CompleteHealthCheckAsync(
                HealthyReceipt(),
                CancellationToken.None));
        string pointerPath = PendingUpdateActivationJournal.GetPointerPath(plan.TargetRoot);
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        journal[missingField] = null;
        File.WriteAllText(pointerPath, journal.ToJsonString());

        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            new UpdateProcessIdentity(973, updated.StartedAtUtc.AddSeconds(1)),
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, resolution.Disposition);
        Assert.AreEqual("UPDATE_ACTIVATION_INVALID", resolution.ErrorCode);
        Assert.IsTrue(File.Exists(pointerPath));
    }

    [TestMethod]
    public void AwaitingHealthAllowsOnlyTheClaimedExactProcessIdentity()
    {
        UpdateApplyPlan plan = CreateAppliedPlan("awaiting-identity", '7');
        var watchdog = new UpdateProcessIdentity(
            971,
            new DateTimeOffset(2026, 8, 27, 8, 7, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(972, watchdog.StartedAtUtc.AddSeconds(1));
        string nonce = new('8', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, "owned-awaiting", nonce);
        _ = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            plan.TargetRoot,
            updated,
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        UpdateActivationStartupResolution exact = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            updated,
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);
        UpdateActivationStartupResolution reusedPid = PendingUpdateActivationJournal.ResolveStartup(
            [],
            plan.TargetRoot,
            updated with { StartedAtUtc = updated.StartedAtUtc.AddSeconds(1) },
            new RecordingHostLifetime(() => { }),
            _ => Task.FromResult(true),
            _ => true);

        Assert.AreEqual(UpdateActivationStartupDisposition.Proceed, exact.Disposition);
        Assert.IsNotNull(exact.Settlement);
        Assert.AreEqual(UpdateActivationStartupDisposition.Blocked, reusedPid.Disposition);
        Assert.IsNull(reusedPid.Settlement);
    }

    private (UpdateApplyPlan Plan, IUpdateActivationSettlement Settlement,
        RecordingHostLifetime Lifetime) CreateClaimedSettlement(string suffix, char token)
    {
        UpdateApplyPlan plan = CreateAppliedPlan(suffix, token);
        var watchdog = new UpdateProcessIdentity(
            921,
            new DateTimeOffset(2026, 8, 27, 8, 2, 0, TimeSpan.Zero));
        var updated = new UpdateProcessIdentity(
            922,
            new DateTimeOffset(2026, 8, 27, 8, 2, 1, TimeSpan.Zero));
        string nonce = new('f', 64);
        PendingUpdateActivationJournal.Publish(plan, watchdog);
        PendingUpdateActivationJournal.RecordUpdatedLaunch(plan, watchdog, "owned-idempotent", nonce);
        var lifetime = new RecordingHostLifetime(() => { });
        UpdateActivationStartupResolution resolution = PendingUpdateActivationJournal.ResolveStartup(
            ["--claim-update", nonce],
            plan.TargetRoot,
            updated,
            lifetime,
            _ => Task.FromResult(true),
            _ => true);
        Assert.IsNotNull(resolution.Settlement);
        return (plan, resolution.Settlement, lifetime);
    }

    private static UpdateActivationHealth.Healthy HealthyReceipt() => new(
        new UpdateWorkspaceHealthProbeReceipt(
            UpdateWorkspaceHealthProbeStatus.SkippedNoRegisteredWorkspace,
            null,
            null,
            null));

    private static string ReadState(string pointerPath)
    {
        using JsonDocument journal = JsonDocument.Parse(File.ReadAllText(pointerPath));
        return journal.RootElement.GetProperty("state").GetString()
            ?? throw new AssertFailedException("journal state missing");
    }

    private static void SetJournalState(string pointerPath, string state)
    {
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        journal["state"] = state;
        File.WriteAllText(pointerPath, journal.ToJsonString());
    }

    private static void ConvertJournalToVersionOne(string pointerPath)
    {
        JsonObject journal = JsonNode.Parse(File.ReadAllText(pointerPath))!.AsObject();
        journal["schemaVersion"] = 1;
        foreach (string name in new[]
        {
            "watchdogProcessId",
            "watchdogStartedAtUtc",
            "ownedGroupId",
            "launchNonce",
            "updatedProcessId",
            "updatedStartedAtUtc",
            "failureCode",
            "rollbackRequestedAtUtc",
            "ownedGroupQuiescedAtUtc",
            "workerLaunchNonce",
            "workerProcessId",
            "workerStartedAtUtc",
            "workerReplacementCount",
            "ownedEntryLedger",
            "rollbackAttempt",
            "rollbackErrorCode",
            "rolledBackAtUtc",
        })
        {
            journal.Remove(name);
        }
        File.WriteAllText(pointerPath, journal.ToJsonString());
    }

    private UpdateApplyPlan CreateAppliedPlan(string suffix, char tokenCharacter)
    {
        string root = Root();
        string target = Path.Combine(root, "VibeTable.Next");
        CreatePackageTree(target, "1.1.0", "new");
        File.WriteAllText(Path.Combine(target, "user-data.db"), "unknown");
        string stage = Path.Combine(root, $".VibeTable.Next.update-{suffix}");
        string source = Path.Combine(stage, "package", "VibeTable");
        CreatePackageTree(source, "1.1.0", "new");
        string backup = Path.Combine(stage, "backup");
        CreatePackageTree(backup, "1.0.0", "old");
        string workspace = Path.Combine(root, "workspace");
        Directory.CreateDirectory(workspace);
        File.WriteAllText(Path.Combine(workspace, "records.db"), "workspace");
        var plan = new UpdateApplyPlan(
            1,
            target,
            source,
            stage,
            123,
            "1.0.0",
            "1.1.0",
            new string(tokenCharacter, 64));
        File.WriteAllText(
            Path.Combine(stage, "update-plan.json"),
            JsonSerializer.Serialize(plan));
        return plan;
    }

    private static void CreatePackageTree(string root, string version, string executableContent)
    {
        Directory.CreateDirectory(Path.Combine(root, "resources"));
        File.WriteAllText(Path.Combine(root, "VibeTable.Next.exe"), executableContent);
        File.WriteAllText(
            Path.Combine(root, "release.json"),
            $$"""{"product":"VibeTable","version":"{{version}}","platform":"windows","architecture":"x64"}""");
        File.WriteAllText(Path.Combine(root, "resources", "publish-layout.json"), "{}");
    }

    private string Root()
    {
        _root ??= Path.Combine(
            Environment.CurrentDirectory,
            "build",
            "update-activation-settlement-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_root);
        return _root;
    }

    private sealed class RecordingHostLifetime(Action beforeExit) : IUpdateHostLifetimePort
    {
        public int ExitRequests { get; private set; }
        public int ExitCode { get; private set; }

        public void RequestExit(int exitCode)
        {
            beforeExit();
            ExitRequests++;
            ExitCode = exitCode;
        }
    }
}
