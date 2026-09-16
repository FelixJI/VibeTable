using System.ComponentModel;
using System.Diagnostics;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
[DoNotParallelize]
public sealed class UpdateRecoveryProcessAdapterTests
{
    [TestMethod]
    public void RetainedUnassignedGroupKeepsBothOwnershipHandlesUntilDisposed()
    {
        var job = new Microsoft.Win32.SafeHandles.SafeFileHandle(
            new IntPtr(-1), ownsHandle: false);
        var exact = new Microsoft.Win32.SafeHandles.SafeFileHandle(
            new IntPtr(-1), ownsHandle: false);
        var group = new UpdateOwnedProcessGroup(
            "retained-unassigned",
            new UpdateProcessIdentity(1234, DateTimeOffset.MinValue),
            job,
            exact);

        Assert.IsFalse(group.Ownership.IsClosed);
        Assert.IsFalse(group.ExactProcessOwnership!.IsClosed);

        group.Dispose();

        Assert.IsTrue(job.IsClosed);
        Assert.IsTrue(exact.IsClosed);
    }

    [TestMethod]
    public async Task InvalidExactOwnershipWaitFailsClosed()
    {
        using var job = new Microsoft.Win32.SafeHandles.SafeFileHandle(
            new IntPtr(-1), ownsHandle: false);
        using var exact = new Microsoft.Win32.SafeHandles.SafeFileHandle(
            IntPtr.Zero, ownsHandle: false);
        using var group = new UpdateOwnedProcessGroup(
            "invalid-exact",
            new UpdateProcessIdentity(1234, DateTimeOffset.MinValue),
            job,
            exact);
        var adapter = new WindowsUpdateRecoveryProcessAdapter();

        ReleaseUpdateException error = await Assert.ThrowsExactlyAsync<ReleaseUpdateException>(
            () => adapter.WaitForOwnedProcessGroupExitAsync(
                group,
                TimeSpan.FromSeconds(1),
                CancellationToken.None));

        Assert.AreEqual("UPDATE_PROCESS_CLEANUP_WAIT_FAILED", error.Code);
    }

    [TestMethod]
    public void BlankOwnedGroupIdentityIsRejectedBeforeCreateProcess()
    {
        var adapter = new WindowsUpdateRecoveryProcessAdapter();
        string missingExecutable = Path.Combine(
            Environment.CurrentDirectory,
            $"missing-update-process-{Guid.NewGuid():N}.exe");

        ArgumentException error = Assert.ThrowsExactly<ArgumentException>(() =>
            adapter.StartUpdatedPackage(new UpdateUpdatedPackageLaunch(
                missingExecutable,
                Environment.CurrentDirectory,
                [],
                " ")));

        Assert.AreEqual("ownedGroupId", error.ParamName);
    }

    [TestMethod]
    public async Task ClosingOwnedGroupHandleDoesNotKillRootProcess()
    {
        var adapter = new WindowsUpdateRecoveryProcessAdapter();
        UpdateOwnedProcessGroup group = adapter.StartUpdatedPackage(LongRunningCommand());
        try
        {
            group.Dispose();
            await Task.Delay(100);

            using Process root = Process.GetProcessById(group.Root.ProcessId);
            Assert.IsFalse(root.HasExited);
            root.Kill(entireProcessTree: true);
            await root.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(5));
        }
        finally
        {
            TryKill(group.Root.ProcessId);
        }
    }

    [TestMethod]
    public async Task ExplicitTerminationWaitsUntilOwnedGroupIsEmpty()
    {
        var adapter = new WindowsUpdateRecoveryProcessAdapter();
        using UpdateOwnedProcessGroup group = adapter.StartUpdatedPackage(LongRunningCommand());

        ExactProcessTermination termination = await adapter.TerminateOwnedProcessGroupAsync(
            group,
            TimeSpan.FromSeconds(5),
            CancellationToken.None);
        OwnedProcessGroupExit empty = await adapter.WaitForOwnedProcessGroupExitAsync(
            group,
            TimeSpan.FromSeconds(1),
            CancellationToken.None);

        Assert.IsTrue(termination.Terminated);
        Assert.IsTrue(termination.GroupEmpty);
        Assert.IsTrue(empty.GroupEmpty);
    }

    [TestMethod]
    public async Task IdentityAccessFailureIsNotReportedAsMatchedExit()
    {
        var adapter = new WindowsUpdateRecoveryProcessAdapter(
            processProbe: new UnreadableProcessProbe());

        ExactProcessExit exit = await adapter.WaitForExactExitAsync(
            new UpdateProcessIdentity(
                4321,
                new DateTimeOffset(2026, 8, 27, 12, 0, 0, TimeSpan.Zero)),
            TimeSpan.FromSeconds(1),
            CancellationToken.None);

        Assert.IsFalse(exit.Exited);
        Assert.IsFalse(exit.IdentityMatched);
    }

    [TestMethod]
    [DataRow(0)]
    [DataRow(17)]
    public async Task ExitBetweenIdentityReadAndWaitPreservesExactExitCode(int exitCode)
    {
        using Process child = Process.Start(new ProcessStartInfo(
            Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe")
        {
            Arguments = $"/d /c set /p update_test_input= & exit /b {exitCode}",
            RedirectStandardInput = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        })!;
        try
        {
            var expected = new UpdateProcessIdentity(
                child.Id, new DateTimeOffset(child.StartTime.ToUniversalTime(), TimeSpan.Zero));
            var adapter = new WindowsUpdateRecoveryProcessAdapter(
                processProbe: new ExitBeforeWaitProbe(child));

            ExactProcessExit result = await adapter.WaitForExactExitAsync(
                expected, TimeSpan.FromSeconds(5), CancellationToken.None);

            Assert.IsTrue(result.Exited);
            Assert.IsTrue(result.IdentityMatched);
            Assert.AreEqual(exitCode, result.ExitCode);
        }
        finally
        {
            if (!child.HasExited)
            {
                child.Kill();
                await child.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(5));
            }
        }
    }

    private sealed class ExitBeforeWaitProbe(Process child) : IUpdateExactProcessProbe
    {
        public IUpdateExactProcess Open(int processId) => new ExitBeforeWaitProcess(
            new WindowsUpdateRecoveryProcessAdapter.WindowsExactProcessProbe().Open(processId),
            child);
    }

    private sealed class ExitBeforeWaitProcess(IUpdateExactProcess exact, Process child)
        : IUpdateExactProcess
    {
        public DateTimeOffset StartedAtUtc => exact.StartedAtUtc;
        public int ExitCode => exact.ExitCode;

        public async Task WaitForExitAsync(CancellationToken cancellationToken)
        {
            child.StandardInput.Close();
            await child.WaitForExitAsync(cancellationToken).WaitAsync(TimeSpan.FromSeconds(5));
            await exact.WaitForExitAsync(cancellationToken);
        }

        public void Dispose() => exact.Dispose();
    }

    private static UpdateUpdatedPackageLaunch LongRunningCommand()
    {
        string command = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe";
        return new UpdateUpdatedPackageLaunch(
            command,
            Environment.CurrentDirectory,
            ["/d", "/c", "ping -n 30 127.0.0.1 >nul"]);
    }

    private static void TryKill(int processId)
    {
        try
        {
            using Process process = Process.GetProcessById(processId);
            process.Kill(entireProcessTree: true);
            process.WaitForExit(5000);
        }
        catch (ArgumentException)
        {
        }
    }

    private sealed class UnreadableProcessProbe : IUpdateExactProcessProbe
    {
        public IUpdateExactProcess Open(int processId) => new UnreadableProcess();
    }

    private sealed class UnreadableProcess : IUpdateExactProcess
    {
        public DateTimeOffset StartedAtUtc => throw new Win32Exception(5);

        public int ExitCode => throw new InvalidOperationException();

        public Task WaitForExitAsync(CancellationToken cancellationToken) =>
            throw new InvalidOperationException();

        public void Dispose()
        {
        }
    }
}
