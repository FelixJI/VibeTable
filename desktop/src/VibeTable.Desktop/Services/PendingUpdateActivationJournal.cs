using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record UpdateProcessIdentity(
    int ProcessId,
    DateTimeOffset StartedAtUtc);

internal sealed record UpdateOwnedEntryLedger(string Name, string Phase);

internal enum UpdateRecoveryState
{
    LaunchingUpdatedApp,
    AwaitingHealth,
    Confirmed,
    RollbackRequested,
}

internal static class PendingUpdateActivationJournal
{
    private const int SchemaVersion = 2;
    private const int MaximumPointerBytes = 16 * 1024;
    private const string PreparedState = "prepared";
    private const string ConfirmedState = "confirmed";
    private const string PointerFileName = ".VibeTable.Next.update-pending.json";
    private const string LockFileName = ".VibeTable.Next.update-pending.lock";
    private static readonly Encoding Utf8NoBom =
        new UTF8Encoding(encoderShouldEmitUTF8Identifier: false);
    private static readonly JsonSerializerOptions WebJson =
        new(JsonSerializerDefaults.Web);
    private static readonly HashSet<string> VersionOnePointerProperties =
        new(StringComparer.Ordinal)
        {
            "schemaVersion",
            "state",
            "targetRoot",
            "stagingRoot",
            "currentVersion",
            "targetVersion",
            "token",
            "smokeTest",
            "updaterProcessId",
            "updaterStartedAtUtc",
            "createdAtUtc",
            "confirmedAt",
        };
    private static readonly HashSet<string> VersionTwoPointerProperties =
        new(VersionOnePointerProperties, StringComparer.Ordinal)
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
        };

    internal static string GetPointerPath(string runningRoot) =>
        Path.Combine(GetTargetParent(runningRoot), PointerFileName);

    internal static UpdateProcessIdentity CurrentProcessIdentity()
    {
        using Process current = Process.GetCurrentProcess();
        return new UpdateProcessIdentity(
            current.Id,
            new DateTimeOffset(current.StartTime.ToUniversalTime(), TimeSpan.Zero));
    }

    internal static void Publish(UpdateApplyPlan plan, UpdateProcessIdentity updater)
    {
        if (updater.ProcessId <= 0)
        {
            throw InvalidPointer("更新激活进程身份无效。");
        }
        UpdateApplyPlan normalized = NormalizePlanPaths(plan);
        ValidatePlanShape(normalized);
        string pointerPath = GetPointerPath(normalized.TargetRoot);
        string lockPath = GetLockPath(normalized.TargetRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            if (File.Exists(pointerPath) || Directory.Exists(pointerPath))
            {
                UpdateProcessCommand.RejectReparsePoint(pointerPath);
                throw new ReleaseUpdateException(
                    "已有尚未完成的更新激活记录，已拒绝覆盖。",
                    "UPDATE_ACTIVATION_PENDING");
            }
            var pending = new PendingUpdateActivation(
                SchemaVersion,
                PreparedState,
                normalized.TargetRoot,
                normalized.StagingRoot,
                normalized.CurrentVersion,
                normalized.TargetVersion,
                normalized.Token,
                normalized.SmokeTest,
                updater.ProcessId,
                updater.StartedAtUtc,
                DateTimeOffset.UtcNow,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                null,
                0,
                [],
                null,
                null,
                null);
            WriteNew(pointerPath, pending);
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    internal static void RecordUpdatedLaunch(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string ownedGroupId,
        string launchNonce)
    {
        if (string.IsNullOrWhiteSpace(ownedGroupId) || !IsLowerHexNonce(launchNonce))
        {
            throw InvalidPointer("更新启动身份无效。");
        }
        UpdateApplyPlan normalized = NormalizePlanPaths(plan);
        string pointerPath = GetPointerPath(normalized.TargetRoot);
        string lockPath = GetLockPath(normalized.TargetRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            PendingUpdateActivation pending = Read(pointerPath);
            _ = ValidatePointerShape(
                pending,
                normalized.TargetRoot,
                null,
                CleanupArgumentsStatus.None);
            if (pending.SchemaVersion != SchemaVersion
                || pending.State != PreparedState
                || !MatchesPlan(pending, normalized)
                || !MatchesUpdater(pending, watchdog))
            {
                throw InvalidPointer("更新启动记录与当前 attempt 不一致。");
            }
            Replace(pointerPath, pending with
            {
                State = "launchingUpdatedApp",
                WatchdogProcessId = watchdog.ProcessId,
                WatchdogStartedAtUtc = watchdog.StartedAtUtc,
                OwnedGroupId = ownedGroupId,
                LaunchNonce = launchNonce,
            });
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    internal static UpdateActivationStartupResolution ResolveStartup(
        IReadOnlyList<string> arguments,
        string runningRoot,
        UpdateProcessIdentity currentProcess,
        IUpdateHostLifetimePort hostLifetime,
        Func<UpdateProcessIdentity, Task<bool>> waitForWatchdogExit,
        Func<UpdateApplyPlan, bool> cleanupStage)
    {
        ArgumentNullException.ThrowIfNull(hostLifetime);
        string normalizedRoot;
        try
        {
            normalizedRoot = ReleasePackageStager.NormalizeDirectory(runningRoot);
            UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(normalizedRoot);
        }
        catch (Exception)
        {
            return new UpdateActivationStartupResolution(
                UpdateActivationStartupDisposition.Blocked,
                null,
                "UPDATE_ACTIVATION_INVALID");
        }
        string pointerPath = GetPointerPath(normalizedRoot);
        if (!File.Exists(pointerPath))
        {
            return new UpdateActivationStartupResolution(
                HasUpdateProtocolArgument(arguments)
                    ? UpdateActivationStartupDisposition.Blocked
                    : UpdateActivationStartupDisposition.Proceed,
                null,
                HasUpdateProtocolArgument(arguments)
                    ? "UPDATE_ACTIVATION_MISSING"
                    : null);
        }

        UpdateActivationStartupResolution? confirmedResume =
            TryResumeConfirmedCleanup(
                normalizedRoot,
                waitForWatchdogExit,
                cleanupStage);
        if (confirmedResume is not null)
        {
            return confirmedResume;
        }

        string? launchNonce = TryReadSingleArgument(arguments, "--claim-update");
        string lockPath = GetLockPath(normalizedRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            PendingUpdateActivation pending = Read(pointerPath);
            if (pending.SchemaVersion == 1)
            {
                CleanupArgumentsStatus argumentStatus = TryParseCleanupArguments(
                    arguments,
                    out CleanupArguments? cleanupArguments);
                if (argumentStatus == CleanupArgumentsStatus.Invalid)
                {
                    return new UpdateActivationStartupResolution(
                        UpdateActivationStartupDisposition.Blocked,
                        null,
                        "UPDATE_ACTIVATION_INVALID");
                }
                ValidatedPending validated = ValidatePending(
                    pending,
                    normalizedRoot,
                    cleanupArguments,
                    argumentStatus);
                var legacy = new JournalActivationGate(
                    pending,
                    validated.Plan,
                    validated.FailedApply,
                    pointerPath,
                    waitForWatchdogExit,
                    cleanupStage);
                if (pending.State == ConfirmedState)
                {
                    legacy.ResumeConfirmedCleanup();
                    bool completed = legacy.Completion.GetAwaiter().GetResult();
                    return new UpdateActivationStartupResolution(
                        completed
                            ? UpdateActivationStartupDisposition.Proceed
                            : UpdateActivationStartupDisposition.Blocked,
                        null,
                        completed ? null : "UPDATE_CLEANUP_FAILED");
                }
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Proceed,
                    new LegacyUpdateActivationSettlement(legacy));
            }
            CleanupArgumentsStatus versionTwoArgumentStatus = TryParseCleanupArguments(
                arguments,
                out CleanupArguments? versionTwoCleanupArguments);
            if (versionTwoArgumentStatus == CleanupArgumentsStatus.Invalid)
            {
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Blocked,
                    null,
                    "UPDATE_ACTIVATION_INVALID");
            }
            if (pending.SchemaVersion == SchemaVersion
                && pending.State == "awaitingHealth"
                && pending.UpdatedProcessId == currentProcess.ProcessId
                && pending.UpdatedStartedAtUtc == currentProcess.StartedAtUtc)
            {
                PointerShape awaitingShape = ValidatePointerShape(
                    pending with { State = PreparedState },
                    normalizedRoot,
                    versionTwoCleanupArguments,
                    versionTwoArgumentStatus);
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Proceed,
                    new JournalUpdateActivationSettlement(
                        pending,
                        awaitingShape.Plan,
                        pointerPath,
                        hostLifetime,
                        waitForWatchdogExit,
                        cleanupStage));
            }
            if (pending.SchemaVersion != SchemaVersion
                || pending.State != "launchingUpdatedApp"
                || !IsLowerHexNonce(launchNonce)
                || !FixedTimeTokenEquals(pending.LaunchNonce ?? string.Empty, launchNonce)
                || pending.WatchdogProcessId is null or <= 0
                || pending.WatchdogStartedAtUtc is null
                || string.IsNullOrWhiteSpace(pending.OwnedGroupId))
            {
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Blocked,
                    null,
                    "UPDATE_RECOVERY_PENDING");
            }

            PointerShape shape = ValidatePointerShape(
                pending with { State = PreparedState },
                normalizedRoot,
                versionTwoCleanupArguments,
                versionTwoArgumentStatus);
            PendingUpdateActivation claimed = pending with
            {
                State = "awaitingHealth",
                LaunchNonce = null,
                UpdatedProcessId = currentProcess.ProcessId,
                UpdatedStartedAtUtc = currentProcess.StartedAtUtc,
            };
            Replace(pointerPath, claimed);
            return new UpdateActivationStartupResolution(
                UpdateActivationStartupDisposition.Proceed,
                new JournalUpdateActivationSettlement(
                    claimed,
                    shape.Plan,
                    pointerPath,
                    hostLifetime,
                    waitForWatchdogExit,
                    cleanupStage));
        }
        catch (Exception)
        {
            return new UpdateActivationStartupResolution(
                UpdateActivationStartupDisposition.Blocked,
                null,
                "UPDATE_ACTIVATION_INVALID");
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    private static UpdateActivationStartupResolution? TryResumeConfirmedCleanup(
        string runningRoot,
        Func<UpdateProcessIdentity, Task<bool>> waitForWatchdogExit,
        Func<UpdateApplyPlan, bool> cleanupStage)
    {
        string pointerPath = GetPointerPath(runningRoot);
        string lockPath = GetLockPath(runningRoot);
        PendingUpdateActivation snapshot;
        ValidatedPending validated;
        UpdateProcessIdentity watchdog;
        using (FileStream claim = AcquireLock(lockPath))
        {
            try
            {
                snapshot = Read(pointerPath);
                if (snapshot.SchemaVersion != SchemaVersion
                    || snapshot.State != ConfirmedState)
                {
                    return null;
                }
                validated = ValidatePending(
                    snapshot,
                    runningRoot,
                    null,
                    CleanupArgumentsStatus.None);
                ValidateVersionTwoConfirmedShape(snapshot);
                watchdog = new UpdateProcessIdentity(
                    snapshot.WatchdogProcessId!.Value,
                    snapshot.WatchdogStartedAtUtc!.Value);
            }
            catch (Exception)
            {
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Blocked,
                    null,
                    "UPDATE_ACTIVATION_INVALID");
            }
            finally
            {
                ReleaseLock(claim, lockPath);
            }
        }
        if (!waitForWatchdogExit(watchdog).GetAwaiter().GetResult())
        {
            return new UpdateActivationStartupResolution(
                UpdateActivationStartupDisposition.Blocked,
                null,
                "UPDATE_WATCHDOG_EXIT_TIMEOUT");
        }
        using FileStream cleanupClaim = AcquireLock(lockPath);
        try
        {
            PendingUpdateActivation current = Read(pointerPath);
            if (!SameAttempt(current, snapshot) || current.State != ConfirmedState)
            {
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Blocked,
                    null,
                    "UPDATE_ACTIVATION_INVALID");
            }
            if (!validated.StageAlreadyAbsent && !cleanupStage(validated.Plan))
            {
                return new UpdateActivationStartupResolution(
                    UpdateActivationStartupDisposition.Blocked,
                    null,
                    "UPDATE_CLEANUP_FAILED");
            }
            UpdateProcessCommand.RejectReparsePoint(pointerPath);
            File.Delete(pointerPath);
            return new UpdateActivationStartupResolution(
                UpdateActivationStartupDisposition.Proceed,
                null);
        }
        finally
        {
            ReleaseLock(cleanupClaim, lockPath);
        }
    }

    private static void ValidateVersionTwoConfirmedShape(PendingUpdateActivation pending)
    {
        if (pending.SchemaVersion != SchemaVersion
            || pending.State != ConfirmedState
            || pending.ConfirmedAt is null
            || pending.LaunchNonce is not null
            || pending.WatchdogProcessId is null or <= 0
            || pending.WatchdogStartedAtUtc is null
            || pending.UpdatedProcessId is null or <= 0
            || pending.UpdatedStartedAtUtc is null
            || string.IsNullOrWhiteSpace(pending.OwnedGroupId)
            || pending.FailureCode is not null
            || pending.RollbackRequestedAtUtc is not null
            || pending.OwnedGroupQuiescedAtUtc is not null
            || pending.WorkerLaunchNonce is not null
            || pending.WorkerProcessId is not null
            || pending.WorkerStartedAtUtc is not null
            || pending.WorkerReplacementCount != 0
            || pending.OwnedEntryLedger.Count != 0
            || pending.RollbackAttempt is not null
            || pending.RollbackErrorCode is not null
            || pending.RolledBackAtUtc is not null)
        {
            throw InvalidPointer("已确认更新状态形状无效。");
        }
    }

    internal static void RecordOwnedGroupQuiesced(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string ownedGroupId)
    {
        MutateRecovery(plan.TargetRoot, current =>
        {
            if (!MatchesPlan(current, NormalizePlanPaths(plan))
                || !MatchesUpdater(current, watchdog)
                || current.State != "rollbackRequested"
                || !string.Equals(current.OwnedGroupId, ownedGroupId, StringComparison.Ordinal))
            {
                throw InvalidPointer("无法证明失败新版 owned process group 已清空。");
            }
            return current with { OwnedGroupQuiescedAtUtc = DateTimeOffset.UtcNow };
        });
    }

    internal static UpdateRecoveryState ReadRecoveryState(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog)
    {
        PendingUpdateActivation current = Read(GetPointerPath(plan.TargetRoot));
        UpdateApplyPlan normalized = NormalizePlanPaths(plan);
        if (current.SchemaVersion != SchemaVersion
            || !MatchesPlan(current, normalized)
            || !MatchesUpdater(current, watchdog)
            || current.WatchdogProcessId != watchdog.ProcessId
            || current.WatchdogStartedAtUtc != watchdog.StartedAtUtc
            || string.IsNullOrWhiteSpace(current.OwnedGroupId)
            || current.WorkerLaunchNonce is not null
            || current.WorkerProcessId is not null
            || current.WorkerStartedAtUtc is not null
            || current.WorkerReplacementCount != 0
            || current.OwnedEntryLedger.Count != 0
            || current.RollbackAttempt is not null
            || current.RollbackErrorCode is not null
            || current.RolledBackAtUtc is not null)
        {
            throw InvalidPointer("watchdog 与当前更新 attempt 不一致。");
        }
        _ = ValidatePointerShape(
            current with { State = PreparedState, ConfirmedAt = null },
            normalized.TargetRoot,
            null,
            CleanupArgumentsStatus.None);
        bool hasUpdatedIdentity = current.UpdatedProcessId is > 0
            && current.UpdatedStartedAtUtc is not null;
        bool lacksUpdatedIdentity = current.UpdatedProcessId is null
            && current.UpdatedStartedAtUtc is null;
        bool noFailure = current.FailureCode is null
            && current.RollbackRequestedAtUtc is null
            && current.OwnedGroupQuiescedAtUtc is null;
        bool validLaunchNonce = IsLowerHexNonce(current.LaunchNonce);
        return current.State switch
        {
            "launchingUpdatedApp" when validLaunchNonce
                && lacksUpdatedIdentity
                && current.ConfirmedAt is null
                && noFailure => UpdateRecoveryState.LaunchingUpdatedApp,
            "awaitingHealth" when current.LaunchNonce is null
                && hasUpdatedIdentity
                && current.ConfirmedAt is null
                && noFailure => UpdateRecoveryState.AwaitingHealth,
            ConfirmedState when current.LaunchNonce is null
                && hasUpdatedIdentity
                && current.ConfirmedAt is not null
                && noFailure => UpdateRecoveryState.Confirmed,
            "rollbackRequested" when current.ConfirmedAt is null
                && IsStableFailureCode(current.FailureCode)
                && current.RollbackRequestedAtUtc is not null
                && current.OwnedGroupQuiescedAtUtc is null
                && ((validLaunchNonce && lacksUpdatedIdentity)
                    || (current.LaunchNonce is null && hasUpdatedIdentity))
                => UpdateRecoveryState.RollbackRequested,
            _ => throw InvalidPointer("watchdog 更新状态格式无效。"),
        };
    }

    internal static void RequestRollback(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        UpdateActivationFailureCode code)
    {
        MutateRecovery(plan.TargetRoot, current =>
        {
            if (!MatchesPlan(current, NormalizePlanPaths(plan))
                || !MatchesUpdater(current, watchdog))
            {
                throw InvalidPointer("watchdog 与当前更新 attempt 不一致。");
            }
            string stableCode = StableFailureCode(code);
            if (current.State == "rollbackRequested")
            {
                if (current.FailureCode != stableCode)
                {
                    throw InvalidPointer("更新失败结果发生冲突。");
                }
                return current;
            }
            if (current.State is not ("launchingUpdatedApp" or "awaitingHealth"))
            {
                throw InvalidPointer("当前更新状态不允许请求回退。");
            }
            return current with
            {
                State = "rollbackRequested",
                FailureCode = stableCode,
                RollbackRequestedAtUtc = DateTimeOffset.UtcNow,
            };
        });
    }

    internal static void RecordRollbackFailed(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string stableErrorCode)
    {
        MutateRecovery(plan.TargetRoot, current =>
        {
            if (!MatchesPlan(current, NormalizePlanPaths(plan))
                || !MatchesUpdater(current, watchdog))
            {
                throw InvalidPointer("watchdog 与当前更新 attempt 不一致。");
            }
            return current with
            {
                State = "rollbackFailed",
                RollbackErrorCode = stableErrorCode,
            };
        });
    }

    internal static string RecordRollbackWorkerLaunch(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string ownedGroupId,
        string workerNonce,
        bool replacement = false)
    {
        if (!IsLowerHexNonce(workerNonce))
        {
            throw InvalidPointer("回退 worker nonce 无效。");
        }
        string rollbackAttempt = string.Empty;
        bool replacementShapeInvalid = false;
        MutateRecovery(plan.TargetRoot, current =>
        {
            bool initial = current.State == "rollbackRequested" && !replacement;
            bool replacing = replacement
                && current.WorkerReplacementCount == 0
                && (current.State is
                        ("rollbackRestoring" or "rolledBack" or "restoredLaunchPending")
                    || (current.State == "rollbackWorkerLaunching"
                        && current.WorkerProcessId is null
                        && current.WorkerStartedAtUtc is null));
            if (!MatchesPlan(current, NormalizePlanPaths(plan))
                || !MatchesUpdater(current, watchdog)
                || !string.Equals(current.OwnedGroupId, ownedGroupId, StringComparison.Ordinal)
                || current.OwnedGroupQuiescedAtUtc is null
                || (!initial && !replacing))
            {
                throw InvalidPointer("回退 worker 启动身份无效。");
            }
            if (current.State is "rolledBack" or "restoredLaunchPending"
                && !IsCompletedRecoveryShape(current))
            {
                replacementShapeInvalid = true;
                return current with
                {
                    State = "rollbackFailed",
                    RollbackErrorCode = "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS",
                };
            }
            rollbackAttempt = current.RollbackAttempt ?? Guid.NewGuid().ToString("N");
            return current with
            {
                State = "rollbackWorkerLaunching",
                WorkerLaunchNonce = workerNonce,
                WorkerProcessId = null,
                WorkerStartedAtUtc = null,
                WorkerReplacementCount = replacement ? 1 : current.WorkerReplacementCount,
                RollbackAttempt = rollbackAttempt,
            };
        });
        if (replacementShapeInvalid)
        {
            throw InvalidPointer("回退 finalize 状态与磁盘形状不一致。");
        }
        return rollbackAttempt;
    }

    internal static string GetRollbackReceiptPath(string targetRoot, string rollbackAttempt) =>
        Path.Combine(
            GetTargetParent(targetRoot),
            $".VibeTable.Next.update-rollback-{rollbackAttempt}.json");

    internal static bool IsRollbackReceiptReadyForLaunch(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string rollbackAttempt)
    {
        try
        {
            PendingUpdateActivation receipt = Read(GetRollbackReceiptPath(
                plan.TargetRoot,
                rollbackAttempt));
            return receipt.SchemaVersion == SchemaVersion
                && receipt.State == "restoredLaunchPending"
                && receipt.RollbackAttempt == rollbackAttempt
                && MatchesPlan(receipt, NormalizePlanPaths(plan))
                && MatchesUpdater(receipt, watchdog)
                && receipt.WatchdogProcessId == watchdog.ProcessId
                && receipt.WatchdogStartedAtUtc == watchdog.StartedAtUtc
                && receipt.OwnedGroupQuiescedAtUtc is not null
                && receipt.WorkerProcessId is > 0
                && receipt.WorkerStartedAtUtc is not null
                && HasCompletedRecoveryShape(receipt);
        }
        catch (Exception)
        {
            return false;
        }
    }

    internal static void RunRollbackWorker(
        string targetRoot,
        string workerNonce,
        UpdateProcessIdentity worker,
        Action<string>? checkpoint = null)
    {
        string normalizedRoot = ReleasePackageStager.NormalizeDirectory(targetRoot);
        PendingUpdateActivation attempt = ClaimRollbackWorker(
            normalizedRoot,
            workerNonce,
            worker);
        string pointerPath = GetPointerPath(normalizedRoot);
        try
        {
            checkpoint?.Invoke("claim:completed");
            string backupRoot = Path.Combine(attempt.StagingRoot, "backup");
            string failedRoot = Path.Combine(attempt.StagingRoot, "failed-package");
            UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
                attempt.TargetRoot,
                attempt.StagingRoot,
                backupRoot,
                failedRoot);
            ValidateRecoveryRoot(failedRoot, attempt.StagingRoot);
            Directory.CreateDirectory(failedRoot);
            UpdateProcessCommand.RejectReparsePoint(failedRoot);
            foreach (string name in UpdatePackageOwnedEntries.InInstallOrder)
            {
                RecoverOwnedEntry(pointerPath, attempt, worker, failedRoot, name, checkpoint);
            }
            InstalledPackageIdentity restored = InstalledPackageIdentity.Read(attempt.TargetRoot);
            if (restored.Version != attempt.CurrentVersion
                || !UpdatePackageOwnedEntries.AllExistAt(attempt.TargetRoot))
            {
                throw InvalidPointer("回退后的安装身份无效。");
            }
            FinalizeRollbackReceipt(pointerPath, attempt, worker, checkpoint);
        }
        catch (Exception exception) when (exception is
            IOException or UnauthorizedAccessException or ReleaseUpdateException)
        {
            TryMarkRollbackFailed(attempt, worker, exception is ReleaseUpdateException
                ? "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS"
                : "UPDATE_ROLLBACK_IO_FAILED");
            throw;
        }
    }

    internal static void UpdateRollbackReceiptLaunch(
        string targetRoot,
        string rollbackAttempt,
        bool launched)
    {
        string receiptPath = GetRollbackReceiptPath(targetRoot, rollbackAttempt);
        PendingUpdateActivation receipt = Read(receiptPath);
        if (receipt.SchemaVersion != SchemaVersion
            || receipt.State != "restoredLaunchPending"
            || receipt.RollbackAttempt != rollbackAttempt)
        {
            throw InvalidPointer("回退 terminal receipt 无效。");
        }
        Replace(receiptPath, receipt with
        {
            State = launched ? "rollbackComplete" : "restoredLaunchFailed",
            RollbackErrorCode = launched ? null : "UPDATE_RESTORED_LAUNCH_FAILED",
        });
    }

    private static PendingUpdateActivation ClaimRollbackWorker(
        string targetRoot,
        string workerNonce,
        UpdateProcessIdentity worker)
    {
        PendingUpdateActivation? claimed = null;
        MutateRecovery(targetRoot, current =>
        {
            if (current.State != "rollbackWorkerLaunching"
                || current.OwnedGroupQuiescedAtUtc is null
                || !IsLowerHexNonce(workerNonce)
                || !FixedTimeTokenEquals(current.WorkerLaunchNonce ?? string.Empty, workerNonce)
                || worker.ProcessId <= 0)
            {
                throw InvalidPointer("回退 worker 未能认领当前 nonce。");
            }
            claimed = current with
            {
                State = "rollbackRestoring",
                WorkerLaunchNonce = null,
                WorkerProcessId = worker.ProcessId,
                WorkerStartedAtUtc = worker.StartedAtUtc,
            };
            return claimed;
        });
        return claimed!;
    }

    private static void RecoverOwnedEntry(
        string pointerPath,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        string failedRoot,
        string name,
        Action<string>? checkpoint)
    {
        string target = Path.Combine(attempt.TargetRoot, name);
        string failed = Path.Combine(failedRoot, name);
        string backup = Path.Combine(attempt.StagingRoot, "backup", name);
        string phase = CurrentLedgerPhase(pointerPath, attempt, worker, name);
        if (phase == "none")
        {
            SetLedgerPhase(pointerPath, attempt, worker, name, "isolatePlanned");
            checkpoint?.Invoke($"{name}:isolatePlanned");
            MoveExact(target, failed);
            checkpoint?.Invoke($"{name}:isolatedOnDisk");
            SetLedgerPhase(pointerPath, attempt, worker, name, "isolated");
            phase = "isolated";
        }
        else if (phase == "isolatePlanned")
        {
            ResumePlannedMove(target, failed);
            SetLedgerPhase(pointerPath, attempt, worker, name, "isolated");
            phase = "isolated";
        }
        if (phase == "isolated")
        {
            if (!ExistsExact(failed) || ExistsExact(target))
            {
                MarkRollbackFailed(
                    pointerPath,
                    attempt,
                    worker,
                    "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS");
                throw InvalidPointer("已隔离 ledger 与失败包形状不一致。");
            }
            SetLedgerPhase(pointerPath, attempt, worker, name, "restorePlanned");
            checkpoint?.Invoke($"{name}:restorePlanned");
            MoveExact(backup, target);
            checkpoint?.Invoke($"{name}:restoredOnDisk");
            SetLedgerPhase(pointerPath, attempt, worker, name, "restored");
            return;
        }
        if (phase == "restorePlanned")
        {
            if (!ExistsExact(failed))
            {
                MarkRollbackFailed(
                    pointerPath,
                    attempt,
                    worker,
                    "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS");
                throw InvalidPointer("恢复 ledger 缺少失败新版入口。");
            }
            ResumePlannedMove(backup, target);
            SetLedgerPhase(pointerPath, attempt, worker, name, "restored");
            return;
        }
        if (phase != "restored"
            || !ExistsExact(target)
            || ExistsExact(backup)
            || !ExistsExact(failed))
        {
            MarkRollbackFailed(pointerPath, attempt, worker, "UPDATE_ROLLBACK_SHAPE_AMBIGUOUS");
            throw InvalidPointer("回退 ledger 与磁盘形状不一致。");
        }
    }

    private static string CurrentLedgerPhase(
        string pointerPath,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        string name)
    {
        PendingUpdateActivation current = Read(pointerPath);
        ValidateWorker(current, attempt, worker);
        return current.OwnedEntryLedger.SingleOrDefault(entry => entry.Name == name)?.Phase
            ?? "none";
    }

    private static void SetLedgerPhase(
        string pointerPath,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        string name,
        string phase)
    {
        MutateRecovery(attempt.TargetRoot, current =>
        {
            ValidateWorker(current, attempt, worker);
            var ledger = current.OwnedEntryLedger
                .Where(entry => entry.Name != name)
                .Append(new UpdateOwnedEntryLedger(name, phase))
                .ToArray();
            return current with { OwnedEntryLedger = ledger };
        });
    }

    private static void ValidateWorker(
        PendingUpdateActivation current,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker)
    {
        if (!SameAttempt(current, attempt)
            || current.State != "rollbackRestoring"
            || current.OwnedGroupQuiescedAtUtc is null
            || current.WorkerProcessId != worker.ProcessId
            || current.WorkerStartedAtUtc != worker.StartedAtUtc)
        {
            throw InvalidPointer("回退 worker 身份已漂移。");
        }
    }

    private static void FinalizeRollbackReceipt(
        string pointerPath,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        Action<string>? checkpoint)
    {
        string lockPath = GetLockPath(attempt.TargetRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            PendingUpdateActivation current = Read(pointerPath);
            ValidateWorker(current, attempt, worker);
            if (!UpdatePackageOwnedEntries.IsFullyRestored(current.OwnedEntryLedger)
                || !HasCompletedRecoveryShape(current))
            {
                throw InvalidPointer("回退 ledger 尚未完成。");
            }
            current = current with { State = "rolledBack", RolledBackAtUtc = DateTimeOffset.UtcNow };
            Replace(pointerPath, current);
            checkpoint?.Invoke("finalize:rolledBack");
            current = current with { State = "restoredLaunchPending" };
            Replace(pointerPath, current);
            checkpoint?.Invoke("finalize:restoredLaunchPending");
            string receiptPath = GetRollbackReceiptPath(
                attempt.TargetRoot,
                attempt.RollbackAttempt!);
            if (File.Exists(receiptPath) || Directory.Exists(receiptPath))
            {
                throw InvalidPointer("回退 terminal receipt 已存在。");
            }
            File.Move(pointerPath, receiptPath, overwrite: false);
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    private static void MarkRollbackFailed(
        string pointerPath,
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        string errorCode)
    {
        _ = pointerPath;
        MutateRecovery(attempt.TargetRoot, current =>
        {
            ValidateWorker(current, attempt, worker);
            return current with { State = "rollbackFailed", RollbackErrorCode = errorCode };
        });
    }

    private static void TryMarkRollbackFailed(
        PendingUpdateActivation attempt,
        UpdateProcessIdentity worker,
        string errorCode)
    {
        try
        {
            MutateRecovery(attempt.TargetRoot, current =>
            {
                if (current.State == "rollbackFailed")
                {
                    return current;
                }
                ValidateWorker(current, attempt, worker);
                return current with
                {
                    State = "rollbackFailed",
                    RollbackErrorCode = errorCode,
                };
            });
        }
        catch (Exception)
        {
            // Preserve the original deterministic recovery failure.
        }
    }

    private static void MutateRecovery(
        string targetRoot,
        Func<PendingUpdateActivation, PendingUpdateActivation> mutation)
    {
        string pointerPath = GetPointerPath(targetRoot);
        string lockPath = GetLockPath(targetRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            PendingUpdateActivation current = Read(pointerPath);
            Replace(pointerPath, mutation(current));
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    private static void ValidateRecoveryRoot(string path, string stagingRoot)
    {
        string normalized = ReleasePackageStager.NormalizeDirectory(path);
        string expected = ReleasePackageStager.NormalizeDirectory(
            Path.Combine(stagingRoot, "failed-package"));
        if (!PathsEqual(normalized, expected))
        {
            throw InvalidPointer("回退失败包目录越界。");
        }
    }

    private static void ResumePlannedMove(string source, string destination)
    {
        bool sourceExists = ExistsExact(source);
        bool destinationExists = ExistsExact(destination);
        if (sourceExists && !destinationExists)
        {
            MoveExact(source, destination);
            return;
        }
        if (!sourceExists && destinationExists)
        {
            return;
        }
        throw InvalidPointer("回退写前 ledger 无法唯一解释磁盘形状。");
    }

    private static void MoveExact(string source, string destination)
    {
        if (ExistsExact(destination) || !ExistsExact(source))
        {
            throw InvalidPointer("回退 owned entry 磁盘形状无效。");
        }
        UpdateProcessCommand.RejectReparsePoint(source);
        if (File.Exists(source))
        {
            File.Move(source, destination);
        }
        else
        {
            UpdateProcessCommand.RejectReparsePointsRecursively(source);
            Directory.Move(source, destination);
        }
    }

    private static bool ExistsExact(string path) => File.Exists(path) || Directory.Exists(path);

    private static bool IsCompletedRecoveryShape(PendingUpdateActivation current)
    {
        if (string.IsNullOrWhiteSpace(current.RollbackAttempt)
            || ExistsExact(GetRollbackReceiptPath(
                current.TargetRoot,
                current.RollbackAttempt)))
        {
            return false;
        }
        return HasCompletedRecoveryShape(current);
    }

    private static bool HasCompletedRecoveryShape(PendingUpdateActivation current)
    {
        IReadOnlyList<string> owned = UpdatePackageOwnedEntries.InInstallOrder;
        if (!UpdatePackageOwnedEntries.IsFullyRestored(current.OwnedEntryLedger))
        {
            return false;
        }
        string backupRoot = Path.Combine(current.StagingRoot, "backup");
        string failedRoot = Path.Combine(current.StagingRoot, "failed-package");
        if (owned.Any(name =>
                !ExistsExact(Path.Combine(current.TargetRoot, name))
                || ExistsExact(Path.Combine(backupRoot, name))
                || !ExistsExact(Path.Combine(failedRoot, name))))
        {
            return false;
        }
        try
        {
            UpdateProcessCommand.RejectReparsePoint(current.TargetRoot);
            UpdateProcessCommand.RejectReparsePoint(failedRoot);
            foreach (string name in owned)
            {
                RejectOwnedEntryReparse(Path.Combine(current.TargetRoot, name));
                RejectOwnedEntryReparse(Path.Combine(failedRoot, name));
            }
            return InstalledPackageIdentity.Read(current.TargetRoot).Version
                == current.CurrentVersion;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private static void RejectOwnedEntryReparse(string path)
    {
        if (Directory.Exists(path))
        {
            UpdateProcessCommand.RejectReparsePointsRecursively(path);
            return;
        }
        UpdateProcessCommand.RejectReparsePoint(path);
    }

    internal static bool TryAbandonPrepared(
        UpdateApplyPlan plan,
        UpdateProcessIdentity updater)
    {
        UpdateApplyPlan normalized;
        string pointerPath;
        string lockPath;
        try
        {
            normalized = NormalizePlanPaths(plan);
            ValidatePlanShape(normalized);
            pointerPath = GetPointerPath(normalized.TargetRoot);
            lockPath = GetLockPath(normalized.TargetRoot);
        }
        catch (Exception)
        {
            return false;
        }
        FileStream? claim = null;
        try
        {
            claim = AcquireLock(lockPath);
            if (!File.Exists(pointerPath))
            {
                return false;
            }
            PendingUpdateActivation pending = Read(pointerPath);
            _ = ValidatePointerShape(
                pending,
                normalized.TargetRoot,
                null,
                CleanupArgumentsStatus.None);
            UpdatePackageOwnedEntries.ValidatePackageAt(
                normalized.TargetRoot,
                pending.CurrentVersion);
            if (pending.State != PreparedState
                || InstalledPackageIdentity.Read(normalized.TargetRoot).Version
                    != pending.CurrentVersion
                || !MatchesPlan(pending, normalized)
                || !MatchesUpdater(pending, updater))
            {
                return false;
            }
            UpdateProcessCommand.RejectReparsePoint(pointerPath);
            File.Delete(pointerPath);
            return true;
        }
        catch (Exception)
        {
            return false;
        }
        finally
        {
            if (claim is not null)
            {
                ReleaseLock(claim, lockPath);
            }
        }
    }

    internal static bool TryLoad(
        IReadOnlyList<string> arguments,
        string runningRoot,
        out IUpdateActivationGate? gate,
        Func<UpdateProcessIdentity, Task<bool>>? waitForUpdaterExit = null,
        Func<UpdateApplyPlan, bool>? cleanupStage = null)
    {
        gate = null;
        CleanupArgumentsStatus argumentStatus = TryParseCleanupArguments(
            arguments,
            out CleanupArguments? cleanupArguments);
        if (argumentStatus == CleanupArgumentsStatus.Invalid)
        {
            return false;
        }
        string normalizedRunningRoot;
        string pointerPath;
        try
        {
            normalizedRunningRoot = ReleasePackageStager.NormalizeDirectory(runningRoot);
            string targetParent = GetTargetParent(normalizedRunningRoot);
            UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
                normalizedRunningRoot);
            pointerPath = Path.Combine(targetParent, PointerFileName);
            if (!File.Exists(pointerPath))
            {
                return false;
            }
            PendingUpdateActivation pending = Read(pointerPath);
            ValidatedPending validated = ValidatePending(
                pending,
                normalizedRunningRoot,
                cleanupArguments,
                argumentStatus);
            var activationGate = new JournalActivationGate(
                pending,
                validated.Plan,
                validated.FailedApply,
                pointerPath,
                waitForUpdaterExit ?? WaitForUpdaterExitAsync,
                cleanupStage ?? UpdateProcessCommand.TryCleanupStage);
            gate = activationGate;
            if (pending.State == ConfirmedState)
            {
                activationGate.ResumeConfirmedCleanup();
            }
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private static ValidatedPending ValidatePending(
        PendingUpdateActivation pending,
        string runningRoot,
        CleanupArguments? arguments,
        CleanupArgumentsStatus argumentStatus)
    {
        PointerShape pointer = ValidatePointerShape(
            pending,
            runningRoot,
            arguments,
            argumentStatus);
        UpdateApplyPlan shape = pointer.Plan;
        bool stageMissing = pointer.StageMissing;
        string targetRoot = shape.TargetRoot;
        string stagingRoot = shape.StagingRoot;
        string installedVersion = InstalledPackageIdentity.Read(targetRoot).Version;
        if (pending.State == ConfirmedState)
        {
            if (installedVersion != pending.TargetVersion)
            {
                throw InvalidPointer("已确认更新的当前安装身份不一致。");
            }
            return new ValidatedPending(shape, StageAlreadyAbsent: stageMissing);
        }
        if (stageMissing)
        {
            throw InvalidPointer("未确认更新的阶段目录缺失。");
        }
        string planPath = Path.Combine(stagingRoot, "update-plan.json");
        UpdateApplyPlan plan = UpdateProcessCommand.ReadAndValidatePlan(
            planPath,
            requireCurrentSource: false,
            targetAlreadyUpdated: installedVersion == pending.TargetVersion);
        if (!MatchesPlan(pending, plan))
        {
            throw InvalidPointer("更新计划与持久化激活记录不一致。");
        }
        bool failedApply = installedVersion == pending.CurrentVersion;
        if (!failedApply && installedVersion != pending.TargetVersion)
        {
            throw InvalidPointer("当前安装身份与更新激活记录不一致。");
        }
        return new ValidatedPending(
            plan,
            StageAlreadyAbsent: false,
            FailedApply: failedApply);
    }

    private static PointerShape ValidatePointerShape(
        PendingUpdateActivation pending,
        string runningRoot,
        CleanupArguments? arguments,
        CleanupArgumentsStatus argumentStatus)
    {
        if (pending.SchemaVersion is not (1 or SchemaVersion)
            || pending.State is not (PreparedState or ConfirmedState)
            || pending.UpdaterProcessId <= 0
            || !StableReleaseVersion.TryParse(
                pending.CurrentVersion,
                out StableReleaseVersion currentVersion)
            || !StableReleaseVersion.TryParse(
                pending.TargetVersion,
                out StableReleaseVersion targetVersion)
            || targetVersion <= currentVersion
            || pending.Token.Length != 64
            || pending.Token.Any(character =>
                !Uri.IsHexDigit(character) || char.IsUpper(character))
            || (pending.State == PreparedState && pending.ConfirmedAt is not null)
            || (pending.State == ConfirmedState && pending.ConfirmedAt is null))
        {
            throw InvalidPointer("更新激活记录格式无效。");
        }
        string targetRoot = ReleasePackageStager.NormalizeDirectory(pending.TargetRoot);
        string stagingRoot = ReleasePackageStager.NormalizeDirectory(pending.StagingRoot);
        if (!PathsEqual(targetRoot, runningRoot))
        {
            throw InvalidPointer("更新激活目标与当前程序不一致。");
        }
        var shape = new UpdateApplyPlan(
            1,
            targetRoot,
            Path.Combine(stagingRoot, "package", "VibeTable"),
            stagingRoot,
            pending.UpdaterProcessId,
            pending.CurrentVersion,
            pending.TargetVersion,
            pending.Token,
            pending.SmokeTest);
        bool stageMissing = !Directory.Exists(stagingRoot) && !File.Exists(stagingRoot);
        ValidatePlanShape(shape);
        if (argumentStatus == CleanupArgumentsStatus.Valid)
        {
            if (arguments is null
                || !PathsEqual(arguments.StagingRoot, stagingRoot)
                || arguments.UpdaterProcessId != pending.UpdaterProcessId
                || !FixedTimeTokenEquals(pending.Token, arguments.Token)
                || arguments.SmokeTest != pending.SmokeTest)
            {
                throw InvalidPointer("更新激活参数与持久化记录不一致。");
            }
        }
        return new PointerShape(shape, stageMissing);
    }

    private static void ValidatePlanShape(UpdateApplyPlan plan)
    {
        string targetRoot = ReleasePackageStager.NormalizeDirectory(plan.TargetRoot);
        string stagingRoot = ReleasePackageStager.NormalizeDirectory(plan.StagingRoot);
        string sourceRoot = ReleasePackageStager.NormalizeDirectory(plan.SourceRoot);
        string targetParent = GetTargetParent(targetRoot);
        DirectoryInfo? stagingParent = Directory.GetParent(
            stagingRoot.TrimEnd(Path.DirectorySeparatorChar));
        if (stagingParent is null
            || !PathsEqual(stagingParent.FullName, targetParent)
            || !Path.GetFileName(stagingRoot.TrimEnd(Path.DirectorySeparatorChar))
                .StartsWith(".VibeTable.Next.update-", StringComparison.Ordinal)
            || !PathsEqual(
                sourceRoot,
                Path.Combine(stagingRoot, "package", "VibeTable"))
            || plan.Token.Length != 64
            || plan.Token.Any(character =>
                !Uri.IsHexDigit(character) || char.IsUpper(character)))
        {
            throw InvalidPointer("更新激活记录的目录或身份边界无效。");
        }
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
            targetRoot,
            stagingRoot);
    }

    private static PendingUpdateActivation Read(string pointerPath)
    {
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
            Path.GetDirectoryName(pointerPath)
                ?? throw InvalidPointer("无法确定更新记录目录。"));
        UpdateProcessCommand.RejectReparsePoint(pointerPath);
        var info = new FileInfo(pointerPath);
        if (info.Length <= 0 || info.Length > MaximumPointerBytes)
        {
            throw InvalidPointer("更新激活记录大小无效。");
        }
        byte[] bytes = File.ReadAllBytes(pointerPath);
        if (bytes.AsSpan().StartsWith(new byte[] { 0xef, 0xbb, 0xbf }))
        {
            throw InvalidPointer("更新激活记录编码无效。");
        }
        using JsonDocument document = JsonDocument.Parse(bytes);
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object)
        {
            throw InvalidPointer("更新激活记录必须是 JSON 对象。");
        }
        string[] names = root.EnumerateObject().Select(property => property.Name).ToArray();
        if (!root.TryGetProperty("schemaVersion", out JsonElement schemaElement)
            || schemaElement.ValueKind != JsonValueKind.Number
            || !schemaElement.TryGetInt32(out int schemaVersion))
        {
            throw InvalidPointer("更新激活记录 schema 无效。");
        }
        HashSet<string> expectedProperties = schemaVersion switch
        {
            1 => VersionOnePointerProperties,
            SchemaVersion => VersionTwoPointerProperties,
            _ => throw InvalidPointer("更新激活记录 schema 无效。"),
        };
        if (names.Length != expectedProperties.Count
            || names.Distinct(StringComparer.Ordinal).Count() != names.Length
            || names.Any(name => !expectedProperties.Contains(name)))
        {
            throw InvalidPointer("更新激活记录字段不完整或包含未知字段。");
        }
        PendingUpdateActivation? pending = JsonSerializer.Deserialize<PendingUpdateActivation>(
            bytes,
            WebJson);
        return pending ?? throw InvalidPointer("更新激活记录格式无效。");
    }

    private static void WriteNew(string pointerPath, PendingUpdateActivation pending)
    {
        string temporary = TemporaryPath(pointerPath);
        try
        {
            WriteTemporary(temporary, pending);
            File.Move(temporary, pointerPath, overwrite: false);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void Replace(string pointerPath, PendingUpdateActivation pending)
    {
        string temporary = TemporaryPath(pointerPath);
        try
        {
            WriteTemporary(temporary, pending);
            UpdateProcessCommand.RejectReparsePoint(pointerPath);
            File.Replace(temporary, pointerPath, null, ignoreMetadataErrors: true);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void WriteTemporary(string path, PendingUpdateActivation pending)
    {
        using var stream = new FileStream(
            path,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            bufferSize: 4096,
            FileOptions.WriteThrough);
        using var writer = new StreamWriter(stream, Utf8NoBom, bufferSize: 4096, leaveOpen: true);
        writer.Write(JsonSerializer.Serialize(pending, WebJson));
        writer.Flush();
        stream.Flush(flushToDisk: true);
    }

    private static string TemporaryPath(string pointerPath) =>
        pointerPath + $".tmp-{Environment.ProcessId}-{Guid.NewGuid():N}";

    private static string GetLockPath(string runningRoot) =>
        Path.Combine(GetTargetParent(runningRoot), LockFileName);

    private static string GetTargetParent(string runningRoot)
    {
        string targetRoot = ReleasePackageStager.NormalizeDirectory(runningRoot);
        DirectoryInfo? parent = Directory.GetParent(
            targetRoot.TrimEnd(Path.DirectorySeparatorChar));
        if (parent is null)
        {
            throw InvalidPointer("无法确定更新激活记录目录。");
        }
        return ReleasePackageStager.NormalizeDirectory(parent.FullName);
    }

    private static FileStream AcquireLock(string lockPath)
    {
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
            Path.GetDirectoryName(lockPath)
                ?? throw InvalidPointer("无法确定更新锁目录。"));
        if (File.Exists(lockPath) || Directory.Exists(lockPath))
        {
            UpdateProcessCommand.RejectReparsePoint(lockPath);
        }
        return new FileStream(
            lockPath,
            FileMode.OpenOrCreate,
            FileAccess.ReadWrite,
            FileShare.None,
            bufferSize: 4096,
            FileOptions.WriteThrough);
    }

    private static void ReleaseLock(FileStream claim, string lockPath)
    {
        claim.Dispose();
        TryDeleteExactFile(lockPath);
    }

    private static void TryDeleteExactFile(string path)
    {
        try
        {
            if (File.Exists(path))
            {
                UpdateProcessCommand.RejectReparsePoint(path);
                File.Delete(path);
            }
        }
        catch (IOException)
        {
            // A regular stale lock or temporary file is recoverable.
        }
        catch (UnauthorizedAccessException)
        {
            // Keep the exact path for diagnostics.
        }
    }

    private static CleanupArgumentsStatus TryParseCleanupArguments(
        IReadOnlyList<string> arguments,
        out CleanupArguments? result)
    {
        result = null;
        string? stagingRoot = null;
        string? updaterProcessIdText = null;
        string? token = null;
        bool smokeTest = false;
        bool sawUpdateArgument = false;
        for (int index = 0; index < arguments.Count; index++)
        {
            switch (arguments[index])
            {
                case "--cleanup-update":
                    sawUpdateArgument = true;
                    if (stagingRoot is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    stagingRoot = arguments[++index];
                    break;
                case "--updater-pid":
                    sawUpdateArgument = true;
                    if (updaterProcessIdText is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    updaterProcessIdText = arguments[++index];
                    break;
                case "--update-token":
                    sawUpdateArgument = true;
                    if (token is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    token = arguments[++index];
                    break;
                case "--self-update-smoke":
                    sawUpdateArgument = true;
                    if (smokeTest)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    smokeTest = true;
                    break;
            }
        }
        if (!sawUpdateArgument)
        {
            return CleanupArgumentsStatus.None;
        }
        if (stagingRoot is null
            || token is null
            || !int.TryParse(updaterProcessIdText, out int updaterProcessId)
            || updaterProcessId <= 0)
        {
            return CleanupArgumentsStatus.Invalid;
        }
        result = new CleanupArguments(stagingRoot, updaterProcessId, token, smokeTest);
        return CleanupArgumentsStatus.Valid;
    }

    internal static async Task<bool> WaitForUpdaterExitAsync(UpdateProcessIdentity expected)
    {
        if (expected.ProcessId == Environment.ProcessId)
        {
            return false;
        }
        try
        {
            using Process updater = Process.GetProcessById(expected.ProcessId);
            DateTimeOffset actualStartedAt;
            try
            {
                actualStartedAt = new DateTimeOffset(
                    updater.StartTime.ToUniversalTime(),
                    TimeSpan.Zero);
            }
            catch (Exception exception) when (exception is InvalidOperationException
                                               or System.ComponentModel.Win32Exception)
            {
                return false;
            }
            if (actualStartedAt != expected.StartedAtUtc)
            {
                // The PID was reused; the exact updater process has exited.
                return true;
            }
            using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(2));
            await updater.WaitForExitAsync(timeout.Token).ConfigureAwait(false);
            return true;
        }
        catch (ArgumentException)
        {
            return true;
        }
        catch (OperationCanceledException)
        {
            return false;
        }
    }

    private static UpdateApplyPlan NormalizePlanPaths(UpdateApplyPlan plan) => plan with
    {
        TargetRoot = ReleasePackageStager.NormalizeDirectory(plan.TargetRoot),
        SourceRoot = ReleasePackageStager.NormalizeDirectory(plan.SourceRoot),
        StagingRoot = ReleasePackageStager.NormalizeDirectory(plan.StagingRoot),
    };

    private static bool PathsEqual(string left, string right) =>
        string.Equals(
            ReleasePackageStager.NormalizeDirectory(left),
            ReleasePackageStager.NormalizeDirectory(right),
            StringComparison.OrdinalIgnoreCase);

    private static bool FixedTimeTokenEquals(string expected, string? actual)
    {
        if (actual is null || expected.Length != actual.Length)
        {
            return false;
        }
        return CryptographicOperations.FixedTimeEquals(
            Encoding.ASCII.GetBytes(expected),
            Encoding.ASCII.GetBytes(actual));
    }

    private static bool IsLowerHexNonce(string? value) =>
        value is { Length: 64 }
        && value.All(character => Uri.IsHexDigit(character) && !char.IsUpper(character));

    private static string StableFailureCode(UpdateActivationFailureCode code) => code switch
    {
        UpdateActivationFailureCode.WorkspaceHealthProbeFailed =>
            "workspaceHealthProbeFailed",
        UpdateActivationFailureCode.UpdatedProcessExited => "updatedProcessExited",
        UpdateActivationFailureCode.HealthTimeout => "healthTimeout",
        _ => throw InvalidPointer("更新健康失败码无效。"),
    };

    private static bool IsStableFailureCode(string? code) => code is
        "workspaceHealthProbeFailed" or "updatedProcessExited" or "healthTimeout";

    private static string? TryReadSingleArgument(
        IReadOnlyList<string> arguments,
        string name)
    {
        string? value = null;
        for (int index = 0; index < arguments.Count; index++)
        {
            if (!string.Equals(arguments[index], name, StringComparison.Ordinal))
            {
                continue;
            }
            if (value is not null || index + 1 >= arguments.Count)
            {
                return null;
            }
            value = arguments[++index];
        }
        return value;
    }

    private static bool HasUpdateProtocolArgument(IReadOnlyList<string> arguments) =>
        arguments.Any(argument => argument is
            "--claim-update" or "--cleanup-update" or "--rollback-update" or
            "--worker-nonce");

    private static bool MatchesUpdater(
        PendingUpdateActivation pending,
        UpdateProcessIdentity updater) =>
        pending.UpdaterProcessId == updater.ProcessId
        && pending.UpdaterStartedAtUtc == updater.StartedAtUtc;

    private static bool MatchesPlan(
        PendingUpdateActivation pending,
        UpdateApplyPlan plan) =>
        PathsEqual(pending.TargetRoot, plan.TargetRoot)
        && PathsEqual(pending.StagingRoot, plan.StagingRoot)
        && pending.CurrentVersion == plan.CurrentVersion
        && pending.TargetVersion == plan.TargetVersion
        && FixedTimeTokenEquals(pending.Token, plan.Token)
        && pending.SmokeTest == plan.SmokeTest;

    private static bool SameAttempt(
        PendingUpdateActivation left,
        PendingUpdateActivation right) =>
        PathsEqual(left.TargetRoot, right.TargetRoot)
        && PathsEqual(left.StagingRoot, right.StagingRoot)
        && left.CurrentVersion == right.CurrentVersion
        && left.TargetVersion == right.TargetVersion
        && FixedTimeTokenEquals(left.Token, right.Token)
        && left.SmokeTest == right.SmokeTest
        && left.UpdaterProcessId == right.UpdaterProcessId
        && left.UpdaterStartedAtUtc == right.UpdaterStartedAtUtc;

    private static ReleaseUpdateException InvalidPointer(string message) =>
        new(message, "UPDATE_ACTIVATION_INVALID");

    private enum CleanupArgumentsStatus
    {
        None,
        Valid,
        Invalid,
    }

    private sealed record CleanupArguments(
        string StagingRoot,
        int UpdaterProcessId,
        string Token,
        bool SmokeTest);

    private sealed record ValidatedPending(
        UpdateApplyPlan Plan,
        bool StageAlreadyAbsent,
        bool FailedApply = false);

    private sealed record PointerShape(
        UpdateApplyPlan Plan,
        bool StageMissing);

    private sealed record PendingUpdateActivation(
        int SchemaVersion,
        string State,
        string TargetRoot,
        string StagingRoot,
        string CurrentVersion,
        string TargetVersion,
        string Token,
        bool SmokeTest,
        int UpdaterProcessId,
        DateTimeOffset UpdaterStartedAtUtc,
        DateTimeOffset CreatedAtUtc,
        DateTimeOffset? ConfirmedAt,
        int? WatchdogProcessId,
        DateTimeOffset? WatchdogStartedAtUtc,
        string? OwnedGroupId,
        string? LaunchNonce,
        int? UpdatedProcessId,
        DateTimeOffset? UpdatedStartedAtUtc,
        string? FailureCode,
        DateTimeOffset? RollbackRequestedAtUtc,
        DateTimeOffset? OwnedGroupQuiescedAtUtc,
        string? WorkerLaunchNonce,
        int? WorkerProcessId,
        DateTimeOffset? WorkerStartedAtUtc,
        int WorkerReplacementCount,
        IReadOnlyList<UpdateOwnedEntryLedger> OwnedEntryLedger,
        string? RollbackAttempt,
        string? RollbackErrorCode,
        DateTimeOffset? RolledBackAtUtc);

    private sealed class JournalUpdateActivationSettlement(
        PendingUpdateActivation attempt,
        UpdateApplyPlan plan,
        string pointerPath,
        IUpdateHostLifetimePort hostLifetime,
        Func<UpdateProcessIdentity, Task<bool>> waitForWatchdogExit,
        Func<UpdateApplyPlan, bool> cleanupStage) : IUpdateActivationSettlement
    {
        private readonly SemaphoreSlim _singleWriter = new(1, 1);
        private UpdateActivationHealth? _completedHealth;
        private bool _confirmedCleanupCompleted;

        public async Task CompleteHealthCheckAsync(
            UpdateActivationHealth health,
            CancellationToken cancellationToken)
        {
            ArgumentNullException.ThrowIfNull(health);
            await _singleWriter.WaitAsync(cancellationToken).ConfigureAwait(false);
            try
            {
                if (_completedHealth is not null)
                {
                    if (_completedHealth == health)
                    {
                        if (health is UpdateActivationHealth.Healthy
                            && !_confirmedCleanupCompleted)
                        {
                            await CompleteConfirmedCleanupAsync().ConfigureAwait(false);
                            _confirmedCleanupCompleted = true;
                        }
                        return;
                    }
                    throw InvalidPointer("更新健康结果发生冲突。");
                }

                if (health is UpdateActivationHealth.Failed failed)
                {
                    PersistFailure(failed.Code);
                    _completedHealth = health;
                    hostLifetime.RequestExit(1);
                    return;
                }
                PersistHealthy();
                _completedHealth = health;
                await CompleteConfirmedCleanupAsync().ConfigureAwait(false);
                _confirmedCleanupCompleted = true;
            }
            finally
            {
                _singleWriter.Release();
            }
        }

        private void PersistHealthy()
        {
            string lockPath = GetLockPath(plan.TargetRoot);
            using FileStream claim = AcquireLock(lockPath);
            try
            {
                PendingUpdateActivation current = Read(pointerPath);
                if (!SameAttempt(current, attempt)
                    || current.State != "awaitingHealth"
                    || current.UpdatedProcessId != attempt.UpdatedProcessId
                    || current.UpdatedStartedAtUtc != attempt.UpdatedStartedAtUtc)
                {
                    throw InvalidPointer("更新健康确认与当前 attempt 不一致。");
                }
                Replace(pointerPath, current with
                {
                    State = ConfirmedState,
                    ConfirmedAt = DateTimeOffset.UtcNow,
                });
            }
            finally
            {
                ReleaseLock(claim, lockPath);
            }
        }

        private async Task CompleteConfirmedCleanupAsync()
        {
            var watchdog = new UpdateProcessIdentity(
                attempt.WatchdogProcessId
                    ?? throw InvalidPointer("更新 watchdog 身份缺失。"),
                attempt.WatchdogStartedAtUtc
                    ?? throw InvalidPointer("更新 watchdog 身份缺失。"));
            if (!await waitForWatchdogExit(watchdog).ConfigureAwait(false))
            {
                throw new ReleaseUpdateException(
                    "更新 watchdog 未能在预算内退出。",
                    "UPDATE_WATCHDOG_EXIT_TIMEOUT");
            }

            string lockPath = GetLockPath(plan.TargetRoot);
            using FileStream claim = AcquireLock(lockPath);
            try
            {
                PendingUpdateActivation current = Read(pointerPath);
                if (!SameAttempt(current, attempt) || current.State != ConfirmedState)
                {
                    throw InvalidPointer("已确认更新与当前 attempt 不一致。");
                }
                _ = ValidatePending(
                    current,
                    plan.TargetRoot,
                    null,
                    CleanupArgumentsStatus.None);
                if (Directory.Exists(plan.StagingRoot) && !cleanupStage(plan))
                {
                    throw new ReleaseUpdateException(
                        "已确认更新的阶段目录清理失败。",
                        "UPDATE_CLEANUP_FAILED");
                }
                if (plan.SmokeTest)
                {
                    WriteSmokeCompletion(plan);
                }
                UpdateProcessCommand.RejectReparsePoint(pointerPath);
                File.Delete(pointerPath);
                if (plan.SmokeTest)
                {
                    hostLifetime.RequestExit(0);
                }
            }
            finally
            {
                ReleaseLock(claim, lockPath);
            }
        }

        private void PersistFailure(UpdateActivationFailureCode code)
        {
            string lockPath = GetLockPath(plan.TargetRoot);
            using FileStream claim = AcquireLock(lockPath);
            try
            {
                PendingUpdateActivation current = Read(pointerPath);
                if (!SameAttempt(current, attempt)
                    || current.UpdatedProcessId != attempt.UpdatedProcessId
                    || current.UpdatedStartedAtUtc != attempt.UpdatedStartedAtUtc)
                {
                    throw InvalidPointer("更新健康失败与当前 attempt 不一致。");
                }
                string stableCode = StableFailureCode(code);
                if (current.State == "rollbackRequested"
                    && current.FailureCode == stableCode)
                {
                    return;
                }
                if (current.State != "awaitingHealth")
                {
                    throw InvalidPointer("更新健康失败与当前状态冲突。");
                }
                Replace(pointerPath, current with
                {
                    State = "rollbackRequested",
                    FailureCode = stableCode,
                    RollbackRequestedAtUtc = DateTimeOffset.UtcNow,
                });
            }
            finally
            {
                ReleaseLock(claim, lockPath);
            }
        }
    }

    private sealed class LegacyUpdateActivationSettlement(IUpdateActivationGate gate)
        : IUpdateActivationSettlement
    {
        private UpdateActivationHealth? _health;

        public async Task CompleteHealthCheckAsync(
            UpdateActivationHealth health,
            CancellationToken cancellationToken)
        {
            ArgumentNullException.ThrowIfNull(health);
            if (_health is not null && _health != health)
            {
                throw InvalidPointer("更新健康结果发生冲突。");
            }
            _health = health;
            if (health is UpdateActivationHealth.Healthy)
            {
                gate.ConfirmActivation();
                bool completed = await gate.Completion
                    .WaitAsync(cancellationToken)
                    .ConfigureAwait(false);
                if (!completed)
                {
                    throw new ReleaseUpdateException(
                        "旧版更新激活清理未完成。",
                        "UPDATE_CLEANUP_FAILED");
                }
                return;
            }
            gate.FailActivation();
            _ = await gate.Completion.ConfigureAwait(false);
        }
    }

    private sealed class JournalActivationGate(
        PendingUpdateActivation pending,
        UpdateApplyPlan plan,
        bool failedApply,
        string pointerPath,
        Func<UpdateProcessIdentity, Task<bool>> waitForUpdaterExit,
        Func<UpdateApplyPlan, bool> cleanupStage) : IUpdateActivationGate
    {
        private readonly TaskCompletionSource<bool> _completion =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _confirmationStarted;

        public bool ExitAfterConfirmation => plan.SmokeTest;

        public Task<bool> Completion => _completion.Task;

        public void ConfirmActivation()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _ = Task.Run(() => CompleteAsync(confirm: true));
        }

        public void FailActivation()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _completion.TrySetResult(false);
        }

        public void ResumeConfirmedCleanup()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _ = Task.Run(() => CompleteAsync(confirm: false));
        }

        private async Task CompleteAsync(bool confirm)
        {
            string lockPath = GetLockPath(plan.TargetRoot);
            FileStream? claim = null;
            try
            {
                var updater = new UpdateProcessIdentity(
                    pending.UpdaterProcessId,
                    pending.UpdaterStartedAtUtc);
                if (!await waitForUpdaterExit(updater).ConfigureAwait(false))
                {
                    _completion.TrySetResult(false);
                    return;
                }
                claim = AcquireLock(lockPath);
                PendingUpdateActivation current = Read(pointerPath);
                ValidatedPending currentValidated = ValidatePending(
                    current,
                    plan.TargetRoot,
                    null,
                    CleanupArgumentsStatus.None);
                if (!SameAttempt(current, pending)
                    || currentValidated.Plan != plan
                    || currentValidated.FailedApply != failedApply)
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (currentValidated.FailedApply)
                {
                    if (current.State != PreparedState)
                    {
                        _completion.TrySetResult(false);
                        return;
                    }
                    UpdateProcessCommand.RejectReparsePoint(pointerPath);
                    File.Delete(pointerPath);
                    _completion.TrySetResult(true);
                    return;
                }
                if (confirm)
                {
                    if (current.State != PreparedState)
                    {
                        _completion.TrySetResult(false);
                        return;
                    }
                    current = current with
                    {
                        State = ConfirmedState,
                        ConfirmedAt = DateTimeOffset.UtcNow,
                    };
                    Replace(pointerPath, current);
                }
                else if (current.State != ConfirmedState)
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (!currentValidated.StageAlreadyAbsent && !cleanupStage(plan))
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (plan.SmokeTest)
                {
                    WriteSmokeCompletion(plan);
                }
                UpdateProcessCommand.RejectReparsePoint(pointerPath);
                File.Delete(pointerPath);
                _completion.TrySetResult(true);
            }
            catch (Exception exception)
            {
                WriteActivationFailureEvidence(plan.StagingRoot, exception);
                _completion.TrySetResult(false);
            }
            finally
            {
                if (claim is not null)
                {
                    ReleaseLock(claim, lockPath);
                }
            }
        }

    }

    private static void WriteSmokeCompletion(UpdateApplyPlan plan)
    {
        string path = Path.Combine(
            plan.TargetRoot,
            UpdateProcessCommand.SmokeCompletionFileName);
        string temporary = TemporaryPath(path);
        try
        {
            using (var stream = new FileStream(
                       temporary,
                       FileMode.CreateNew,
                       FileAccess.Write,
                       FileShare.None,
                       bufferSize: 4096,
                       FileOptions.WriteThrough))
            {
                JsonSerializer.Serialize(
                    stream,
                    new SmokeCompletionEvidence(
                        plan.Token,
                        plan.TargetVersion,
                        Environment.ProcessId,
                        DateTimeOffset.UtcNow),
                    WebJson);
                stream.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void WriteActivationFailureEvidence(string stageRoot, Exception exception)
    {
        try
        {
            string path = stageRoot.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar) + ".activation-error.txt";
            File.WriteAllText(
                path,
                $"[{DateTimeOffset.UtcNow:O}] {exception}{Environment.NewLine}");
        }
        catch (Exception)
        {
            // Diagnostics must never replace the activation result.
        }
    }

    private sealed record SmokeCompletionEvidence(
        string Token,
        string TargetVersion,
        int ProcessId,
        DateTimeOffset ConfirmedAt);
}
