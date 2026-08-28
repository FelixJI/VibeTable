using System.Globalization;
using System.IO;
using System.Security.Cryptography;

namespace VibeTable.Desktop.Services;

internal sealed class UpdateRecoveryWatchdog(
    IUpdateRecoveryProcessPort processes,
    TimeProvider? timeProvider = null,
    TimeSpan? healthBudget = null,
    TimeSpan? processExitBudget = null,
    IUpdateOwnedProcessQuarantine? quarantine = null,
    Action<UpdateApplyPlan, string, bool>? receiptLaunchRecorder = null)
{
    private readonly IUpdateRecoveryProcessPort _processes = processes
        ?? throw new ArgumentNullException(nameof(processes));
    private readonly TimeProvider _timeProvider = timeProvider ?? TimeProvider.System;
    private readonly TimeSpan _healthBudget = healthBudget ?? TimeSpan.FromMinutes(2);
    private readonly TimeSpan _processExitBudget = processExitBudget ?? TimeSpan.FromSeconds(15);
    private readonly IUpdateOwnedProcessQuarantine _quarantine =
        quarantine ?? new UpdateOwnedProcessQuarantine();
    private readonly Action<UpdateApplyPlan, string, bool> _receiptLaunchRecorder =
        receiptLaunchRecorder ?? ((plan, attempt, launched) =>
            PendingUpdateActivationJournal.UpdateRollbackReceiptLaunch(
                plan.TargetRoot,
                attempt,
                launched));

    internal async Task RunUpdatedPackageAsync(
        UpdateApplyPlan plan,
        CancellationToken cancellationToken)
    {
        UpdateProcessIdentity watchdog = _processes.Current();
        string groupId = Guid.NewGuid().ToString("N");
        string launchNonce = Nonce();
        PendingUpdateActivationJournal.RecordUpdatedLaunch(
            plan,
            watchdog,
            groupId,
            launchNonce);
        UpdateOwnedProcessGroup group;
        try
        {
            group = _processes.StartUpdatedPackage(
                UpdatedLaunch(plan, groupId, launchNonce));
        }
        catch (UpdateOwnedProcessStartException exception) when (exception.GroupEmpty)
        {
            PendingUpdateActivationJournal.RequestRollback(
                plan,
                watchdog,
                UpdateActivationFailureCode.UpdatedProcessExited);
            PendingUpdateActivationJournal.RecordOwnedGroupQuiesced(
                plan,
                watchdog,
                groupId);
            await RestoreOwnedEntriesAsync(
                plan,
                watchdog,
                groupId,
                cancellationToken).ConfigureAwait(false);
            return;
        }
        catch (UpdateOwnedProcessStartException exception)
        {
            PendingUpdateActivationJournal.RecordRollbackFailed(
                plan,
                watchdog,
                "UPDATE_GROUP_NOT_QUIESCED");
            if (exception.RetainedGroup is not null)
            {
                await _quarantine.RetainUntilEmptyAsync(
                    exception.RetainedGroup,
                    _processes,
                    _processExitBudget).ConfigureAwait(false);
            }
            return;
        }
        catch (Exception)
        {
            PendingUpdateActivationJournal.RecordRollbackFailed(
                plan,
                watchdog,
                "UPDATE_GROUP_NOT_QUIESCED");
            return;
        }
        if (!string.Equals(group.GroupId, groupId, StringComparison.Ordinal))
        {
            PendingUpdateActivationJournal.RequestRollback(
                plan,
                watchdog,
                UpdateActivationFailureCode.UpdatedProcessExited);
            bool groupEmpty = await TryTerminateOwnedGroupAsync(
                group,
                cancellationToken).ConfigureAwait(false);
            PendingUpdateActivationJournal.RecordRollbackFailed(
                plan,
                watchdog,
                groupEmpty
                    ? "UPDATE_GROUP_IDENTITY_MISMATCH"
                    : "UPDATE_GROUP_NOT_QUIESCED");
            if (groupEmpty)
            {
                group.Dispose();
            }
            else
            {
                await _quarantine.RetainUntilEmptyAsync(
                    group,
                    _processes,
                    _processExitBudget).ConfigureAwait(false);
            }
            return;
        }
        bool ownershipTransferred = false;
        try
        {
            try
            {
                UpdateProcessCommand.WriteSmokeProcessEvidence(plan, group.Root.ProcessId);
            }
            catch (Exception)
            {
                ownershipTransferred = await RecoverAsync(
                    plan,
                    watchdog,
                    group,
                    groupId,
                    UpdateActivationFailureCode.UpdatedProcessExited,
                    cancellationToken).ConfigureAwait(false);
                return;
            }
            DateTimeOffset deadline = _timeProvider.GetUtcNow() + _healthBudget;
            while (_timeProvider.GetUtcNow() < deadline)
            {
                UpdateRecoveryState state;
                try
                {
                    state = PendingUpdateActivationJournal.ReadRecoveryState(plan, watchdog);
                }
                catch (Exception)
                {
                    ownershipTransferred = await TerminateAndRecordFailureAsync(
                        plan,
                        watchdog,
                        group,
                        "UPDATE_ACTIVATION_INVALID",
                        cancellationToken).ConfigureAwait(false);
                    return;
                }
                if (state == UpdateRecoveryState.Confirmed)
                {
                    return;
                }
                if (state == UpdateRecoveryState.RollbackRequested)
                {
                    ownershipTransferred = await RecoverAsync(
                        plan,
                        watchdog,
                        group,
                        groupId,
                        UpdateActivationFailureCode.WorkspaceHealthProbeFailed,
                        cancellationToken).ConfigureAwait(false);
                    return;
                }
                ExactProcessExit exited = await _processes.WaitForExactExitAsync(
                    group.Root,
                    TimeSpan.FromMilliseconds(100),
                    cancellationToken).ConfigureAwait(false);
                if (exited.Exited || !exited.IdentityMatched)
                {
                    ownershipTransferred = await RecoverAsync(
                        plan,
                        watchdog,
                        group,
                        groupId,
                        UpdateActivationFailureCode.UpdatedProcessExited,
                        cancellationToken).ConfigureAwait(false);
                    return;
                }
            }
            ownershipTransferred = await RecoverAsync(
                plan,
                watchdog,
                group,
                groupId,
                UpdateActivationFailureCode.HealthTimeout,
                cancellationToken).ConfigureAwait(false);
        }
        catch (Exception)
        {
            ownershipTransferred = await TerminateAndRecordFailureAsync(
                plan,
                watchdog,
                group,
                "UPDATE_RECOVERY_WATCHDOG_FAILED",
                cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            if (!ownershipTransferred)
            {
                group.Dispose();
            }
        }
    }

    private async Task<bool> TerminateAndRecordFailureAsync(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        UpdateOwnedProcessGroup group,
        string errorCode,
        CancellationToken cancellationToken)
    {
        bool quiesced = false;
        try
        {
            ExactProcessTermination termination =
                await _processes.TerminateOwnedProcessGroupAsync(
                    group,
                    _processExitBudget,
                    cancellationToken).ConfigureAwait(false);
            quiesced = termination.Terminated && termination.GroupEmpty;
        }
        catch (Exception)
        {
            // The stable journal error below records that quiescence was not proven.
        }
        PendingUpdateActivationJournal.RecordRollbackFailed(
            plan,
            watchdog,
            quiesced ? errorCode : "UPDATE_GROUP_NOT_QUIESCED");
        if (!quiesced)
        {
            await _quarantine.RetainUntilEmptyAsync(
                group,
                _processes,
                _processExitBudget).ConfigureAwait(false);
            return true;
        }
        return false;
    }

    internal async Task<bool> RecoverAsync(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        UpdateOwnedProcessGroup failedGroup,
        string groupId,
        UpdateActivationFailureCode failure,
        CancellationToken cancellationToken)
    {
        PendingUpdateActivationJournal.RequestRollback(plan, watchdog, failure);
        bool groupEmpty = false;
        bool ownershipTransferred = false;
        try
        {
            OwnedProcessGroupExit empty =
                await _processes.WaitForOwnedProcessGroupExitAsync(
                    failedGroup,
                    _processExitBudget,
                    cancellationToken).ConfigureAwait(false);
            groupEmpty = empty.GroupEmpty;
        }
        catch (Exception)
        {
            // An inconclusive wait must fall through to explicit termination.
        }
        if (!groupEmpty)
        {
            try
            {
                ExactProcessTermination terminated =
                    await _processes.TerminateOwnedProcessGroupAsync(
                        failedGroup,
                        _processExitBudget,
                        cancellationToken).ConfigureAwait(false);
                groupEmpty = terminated.Terminated && terminated.GroupEmpty;
            }
            catch (Exception)
            {
                groupEmpty = false;
            }
            if (!groupEmpty)
            {
                await _quarantine.RetainUntilEmptyAsync(
                    failedGroup,
                    _processes,
                    _processExitBudget).ConfigureAwait(false);
                ownershipTransferred = true;
            }
        }
        PendingUpdateActivationJournal.RecordOwnedGroupQuiesced(
            plan,
            watchdog,
            groupId);
        try
        {
            await RestoreOwnedEntriesAsync(
                plan,
                watchdog,
                groupId,
                cancellationToken).ConfigureAwait(false);
        }
        catch (Exception exception)
        {
            PendingUpdateActivationJournal.RecordRollbackFailed(
                plan,
                watchdog,
                exception is ReleaseUpdateException release
                    ? release.Code
                    : "UPDATE_ROLLBACK_PREPARATION_FAILED");
        }
        return ownershipTransferred;
    }

    private async Task RestoreOwnedEntriesAsync(
        UpdateApplyPlan plan,
        UpdateProcessIdentity watchdog,
        string groupId,
        CancellationToken cancellationToken)
    {
        string workerRoot = PrepareWorkerRoot(plan);
        string? rollbackAttempt = null;
        for (int attempt = 0; attempt < 2; attempt++)
        {
            string nonce = Nonce();
            rollbackAttempt = PendingUpdateActivationJournal.RecordRollbackWorkerLaunch(
                plan,
                watchdog,
                groupId,
                nonce,
                replacement: attempt != 0);
            string workerGroupId = Guid.NewGuid().ToString("N");
            UpdateOwnedProcessGroup workerGroup;
            try
            {
                workerGroup = _processes.StartRollbackWorker(
                    new UpdateRollbackLaunch(
                        Path.Combine(workerRoot, "VibeTable.Next.exe"),
                        workerRoot,
                        [
                            "--rollback-update",
                            plan.TargetRoot,
                            "--worker-nonce",
                            nonce,
                        ],
                        workerGroupId));
            }
            catch (UpdateOwnedProcessStartException exception) when (exception.GroupEmpty)
            {
                if (attempt == 0)
                {
                    continue;
                }
                PendingUpdateActivationJournal.RecordRollbackFailed(
                    plan,
                    watchdog,
                    "UPDATE_ROLLBACK_WORKER_START_FAILED");
                return;
            }
            catch (UpdateOwnedProcessStartException exception)
            {
                PendingUpdateActivationJournal.RecordRollbackFailed(
                    plan,
                    watchdog,
                    "UPDATE_WORKER_GROUP_NOT_QUIESCED");
                if (exception.RetainedGroup is not null)
                {
                    await _quarantine.RetainUntilEmptyAsync(
                        exception.RetainedGroup,
                        _processes,
                        _processExitBudget).ConfigureAwait(false);
                }
                return;
            }
            catch (Exception)
            {
                PendingUpdateActivationJournal.RecordRollbackFailed(
                    plan,
                    watchdog,
                    "UPDATE_WORKER_GROUP_NOT_QUIESCED");
                return;
            }
            bool workerOwnershipTransferred = false;
            try
            {
                if (!string.Equals(
                        workerGroup.GroupId,
                        workerGroupId,
                        StringComparison.Ordinal))
                {
                    bool mismatchEmpty = await TryTerminateOwnedGroupAsync(
                        workerGroup,
                        cancellationToken).ConfigureAwait(false);
                    PendingUpdateActivationJournal.RecordRollbackFailed(
                        plan,
                        watchdog,
                        mismatchEmpty
                            ? "UPDATE_WORKER_GROUP_IDENTITY_MISMATCH"
                            : "UPDATE_WORKER_GROUP_NOT_QUIESCED");
                    if (!mismatchEmpty)
                    {
                        workerOwnershipTransferred = true;
                        await _quarantine.RetainUntilEmptyAsync(
                            workerGroup,
                            _processes,
                            _processExitBudget).ConfigureAwait(false);
                    }
                    return;
                }
                ExactProcessExit? workerExit = null;
                try
                {
                    workerExit = await _processes.WaitForExactExitAsync(
                        workerGroup.Root,
                        _processExitBudget,
                        cancellationToken).ConfigureAwait(false);
                }
                catch (Exception)
                {
                    // An inconclusive exact wait requires explicit group termination.
                }
                bool workerGroupEmpty = false;
                if (workerExit is null
                    || !workerExit.Exited
                    || !workerExit.IdentityMatched)
                {
                    try
                    {
                        ExactProcessTermination termination =
                            await _processes.TerminateOwnedProcessGroupAsync(
                                workerGroup,
                                _processExitBudget,
                                cancellationToken).ConfigureAwait(false);
                        workerGroupEmpty = termination.Terminated && termination.GroupEmpty;
                    }
                    catch (Exception)
                    {
                        workerGroupEmpty = false;
                    }
                }
                else
                {
                    try
                    {
                        OwnedProcessGroupExit workerEmpty =
                            await _processes.WaitForOwnedProcessGroupExitAsync(
                                workerGroup,
                                _processExitBudget,
                                cancellationToken).ConfigureAwait(false);
                        workerGroupEmpty = workerEmpty.GroupEmpty;
                    }
                    catch (Exception)
                    {
                        workerGroupEmpty = false;
                    }
                    if (!workerGroupEmpty)
                    {
                        try
                        {
                            ExactProcessTermination termination =
                                await _processes.TerminateOwnedProcessGroupAsync(
                                    workerGroup,
                                    _processExitBudget,
                                    cancellationToken).ConfigureAwait(false);
                            workerGroupEmpty = termination.Terminated
                                && termination.GroupEmpty;
                        }
                        catch (Exception)
                        {
                            workerGroupEmpty = false;
                        }
                    }
                }
                if (!workerGroupEmpty)
                {
                    PendingUpdateActivationJournal.RecordRollbackFailed(
                        plan,
                        watchdog,
                        "UPDATE_WORKER_GROUP_NOT_QUIESCED");
                    workerOwnershipTransferred = true;
                    await _quarantine.RetainUntilEmptyAsync(
                        workerGroup,
                        _processes,
                        _processExitBudget).ConfigureAwait(false);
                    return;
                }
                if (PendingUpdateActivationJournal.IsRollbackReceiptReadyForLaunch(
                        plan,
                        watchdog,
                        rollbackAttempt))
                {
                    StartRestored(plan, rollbackAttempt);
                    return;
                }
                string receiptPath = PendingUpdateActivationJournal.GetRollbackReceiptPath(
                    plan.TargetRoot,
                    rollbackAttempt);
                if (File.Exists(receiptPath) || Directory.Exists(receiptPath))
                {
                    return;
                }
            }
            finally
            {
                if (!workerOwnershipTransferred)
                {
                    workerGroup.Dispose();
                }
            }
        }
        PendingUpdateActivationJournal.RecordRollbackFailed(
            plan,
            watchdog,
            "UPDATE_ROLLBACK_WORKER_EXITED");
    }

    private async Task<bool> TryTerminateOwnedGroupAsync(
        UpdateOwnedProcessGroup group,
        CancellationToken cancellationToken)
    {
        try
        {
            ExactProcessTermination termination =
                await _processes.TerminateOwnedProcessGroupAsync(
                    group,
                    _processExitBudget,
                    cancellationToken).ConfigureAwait(false);
            return termination.Terminated && termination.GroupEmpty;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private void StartRestored(UpdateApplyPlan plan, string rollbackAttempt)
    {
        bool launched = false;
        try
        {
            _processes.StartRestoredPackage(RestoredLaunch(plan));
            launched = true;
        }
        catch (Exception)
        {
            // The receipt below is terminal. Do not recreate a pending pointer
            // or retry process launch after the package has been restored.
        }
        finally
        {
            try
            {
                _receiptLaunchRecorder(plan, rollbackAttempt, launched);
            }
            catch (Exception)
            {
                // The pending pointer has already been atomically replaced by the
                // terminal receipt. Never re-enter pending recovery from here.
            }
        }
    }

    private static UpdateRestoredPackageLaunch RestoredLaunch(UpdateApplyPlan plan)
    {
        IReadOnlyList<string> arguments = [];
        if (plan.SmokeTest)
        {
            string stageParent = Directory.GetParent(plan.StagingRoot.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar))!.FullName;
            arguments = [
                "--test-mode",
                "--readiness-dir",
                Path.Combine(stageParent, "self-update-restored-readiness"),
                "--e2e-controls-dir",
                Path.Combine(stageParent, "self-update-restored-controls"),
            ];
        }
        return new UpdateRestoredPackageLaunch(
            Path.Combine(plan.TargetRoot, "VibeTable.Next.exe"),
            plan.TargetRoot,
            arguments);
    }

    private static UpdateUpdatedPackageLaunch UpdatedLaunch(
        UpdateApplyPlan plan,
        string groupId,
        string launchNonce)
    {
        var arguments = new List<string>
        {
            "--claim-update", launchNonce,
            "--cleanup-update", plan.StagingRoot,
            "--updater-pid", Environment.ProcessId.ToString(CultureInfo.InvariantCulture),
            "--update-token", plan.Token,
        };
        if (plan.SmokeTest)
        {
            string stageParent = Directory.GetParent(plan.StagingRoot.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar))!.FullName;
            arguments.AddRange([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir",
                Path.Combine(stageParent, "self-update-readiness"),
                "--e2e-controls-dir",
                Path.Combine(stageParent, "self-update-updated-controls"),
            ]);
        }
        return new UpdateUpdatedPackageLaunch(
            Path.Combine(plan.TargetRoot, "VibeTable.Next.exe"),
            plan.TargetRoot,
            arguments,
            groupId);
    }

    private static string PrepareWorkerRoot(UpdateApplyPlan plan)
    {
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
            plan.TargetRoot,
            plan.StagingRoot,
            plan.SourceRoot);
        UpdateProcessCommand.RejectReparsePoint(plan.SourceRoot);
        string workerRoot = Path.Combine(plan.StagingRoot, "rollback-worker", "VibeTable");
        if (Directory.Exists(workerRoot) || File.Exists(workerRoot))
        {
            ValidateWorkerRoot(workerRoot, plan.TargetVersion);
            return workerRoot;
        }
        string workerParent = Path.GetDirectoryName(workerRoot)!;
        Directory.CreateDirectory(workerParent);
        UpdateProcessCommand.RejectReparsePoint(workerParent);
        Directory.CreateDirectory(workerRoot);
        UpdateProcessCommand.RejectReparsePoint(workerRoot);
        foreach (string name in UpdatePackageOwnedEntries.InInstallOrder)
        {
            string source = Path.Combine(plan.SourceRoot, name);
            string destination = Path.Combine(workerRoot, name);
            UpdateProcessCommand.RejectReparsePoint(source);
            if (File.Exists(source))
            {
                File.Copy(source, destination, overwrite: false);
            }
            else
            {
                CopyDirectory(source, destination);
            }
        }
        ValidateWorkerRoot(workerRoot, plan.TargetVersion);
        return workerRoot;
    }

    private static void ValidateWorkerRoot(string workerRoot, string targetVersion)
    {
        UpdateProcessCommand.RejectReparsePointsRecursively(workerRoot);
        string[] actual = Directory.EnumerateFileSystemEntries(
                workerRoot,
                "*",
                SearchOption.TopDirectoryOnly)
            .Select(Path.GetFileName)
            .Order(StringComparer.Ordinal)
            .ToArray()!;
        string[] expected = UpdatePackageOwnedEntries.InInstallOrder
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (!actual.SequenceEqual(expected, StringComparer.Ordinal)
            || InstalledPackageIdentity.Read(workerRoot).Version != targetVersion)
        {
            throw new ReleaseUpdateException(
                "回退 worker root 与本次目标包身份不一致。",
                "UPDATE_ROLLBACK_WORKER_INVALID");
        }
    }

    private static void CopyDirectory(string source, string destination)
    {
        UpdateProcessCommand.RejectReparsePointsRecursively(source);
        Directory.CreateDirectory(destination);
        foreach (string directory in Directory.EnumerateDirectories(
                     source, "*", SearchOption.AllDirectories))
        {
            Directory.CreateDirectory(Path.Combine(
                destination,
                Path.GetRelativePath(source, directory)));
        }
        foreach (string file in Directory.EnumerateFiles(
                     source, "*", SearchOption.AllDirectories))
        {
            string target = Path.Combine(destination, Path.GetRelativePath(source, file));
            Directory.CreateDirectory(Path.GetDirectoryName(target)!);
            File.Copy(file, target, overwrite: false);
        }
    }

    private static string Nonce() => Convert.ToHexString(RandomNumberGenerator.GetBytes(32))
        .ToLowerInvariant();
}
