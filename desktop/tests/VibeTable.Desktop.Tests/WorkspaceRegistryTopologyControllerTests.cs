using VibeTable.Contracts;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRegistryTopologyControllerTests
{
    [TestMethod]
    public async Task OpenPreservesSuccessFailureAndCancellationAtModuleInterface()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-topology-open-");
        Guid workspaceId = Guid.NewGuid();
        WorkspaceSessionV2 opened = OpenSession(workspaceId, 19);
        fixture.Session.Open = (_, _, _, _) => Task.FromResult(opened);

        WorkspaceSessionV2 result = await fixture.Controller.OpenAsync(
            workspaceId,
            WorkspaceOpenMode.Writable,
            switching: true,
            CancellationToken.None);

        Assert.AreSame(opened, result);
        Assert.AreEqual((workspaceId, true), fixture.Session.LastOpen);

        fixture.Session.Open = (_, _, _, _) =>
            Task.FromException<WorkspaceSessionV2>(
                new InvalidOperationException("failed"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            fixture.Controller.OpenAsync(
                workspaceId,
                WorkspaceOpenMode.Writable,
                switching: false,
                CancellationToken.None));

        fixture.Session.Open = (_, _, _, token) =>
            Task.FromCanceled<WorkspaceSessionV2>(token);
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();
        await Assert.ThrowsExactlyAsync<TaskCanceledException>(() =>
            fixture.Controller.OpenAsync(
                workspaceId,
                WorkspaceOpenMode.Writable,
                switching: false,
                cancelled.Token));
    }

    private static WorkspaceSessionV2 OpenSession(Guid workspaceId, ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.OpenedWritable,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = true,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };
}
