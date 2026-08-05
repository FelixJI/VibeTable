using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWorkspaceOpenPolicyTests
{
    [TestMethod]
    public void AllowsProjectionOnlyAfterTheWorkspaceSessionIsOpenedAndIdle()
    {
        WorkspaceSessionV2 verifying = Session(
            WorkspaceSessionState.Switching,
            WorkspaceSessionPhase.Verifying);

        Assert.IsFalse(ProductWorkspaceOpenPolicy.CanProject(verifying));
        Assert.IsTrue(ProductWorkspaceOpenPolicy.CanProject(Session(
            WorkspaceSessionState.OpenedReadOnly,
            WorkspaceSessionPhase.Idle)));
        Assert.IsTrue(ProductWorkspaceOpenPolicy.CanProject(Session(
            WorkspaceSessionState.OpenedWritable,
            WorkspaceSessionPhase.Idle)));
        Assert.IsTrue(ProductWorkspaceOpenPolicy.CanProject(Session(
            WorkspaceSessionState.OpenedProvisional,
            WorkspaceSessionPhase.Idle)));
    }

    private static WorkspaceSessionV2 Session(
        WorkspaceSessionState state,
        WorkspaceSessionPhase phase) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"),
        SessionEpoch = 7,
        State = state,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = state == WorkspaceSessionState.OpenedWritable,
        Provisional = state == WorkspaceSessionState.OpenedProvisional,
        Phase = phase,
        ErrorCode = null,
    };
}
