using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceCreationLocationTests
{
    [TestMethod]
    public async Task ManagedDefaultUsesProductDataAndNeverOpensThePicker()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-managed-");

        await fixture.DispatchAsync("workspace.create", CreateParameters(
            "Managed",
            "managedDefault",
            selectedRootGrant: null,
            storageMode: "direct"));

        WorkspaceRegistryEntryV2 created = fixture.Registry.List().Single();
        Assert.AreEqual(
            Path.Combine(
                Path.GetFullPath(fixture.ProductDataRoot),
                "workspaces",
                created.WorkspaceId.ToString("D")),
            created.SelectedRoot);
        Assert.AreEqual(0, fixture.Picker.WorkspacePickCount);
    }

    [TestMethod]
    public async Task OtherLocationMaterializesAndConsumesTheNativeGrant()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-other-");
        string chosen = Path.Combine(fixture.Root, "selected");
        fixture.Picker.SelectedRoot = chosen;

        await fixture.DispatchAsync("workspace.create", CreateParameters(
            "Other",
            "other",
            "host-picker://workspace-root",
            "direct"));

        Assert.AreEqual(
            Path.GetFullPath(chosen),
            fixture.Registry.List().Single().SelectedRoot);
        Assert.AreEqual(1, fixture.Picker.WorkspacePickCount);
    }

    [TestMethod]
    [DataRow(
        "managedDefault",
        "host-picker://workspace-root",
        "The managed default location must not include a path grant.")]
    [DataRow(
        "other",
        null,
        "The other location requires a selectedRootGrant.")]
    [DataRow(
        "remote",
        null,
        "Workspace locationPolicy is invalid.")]
    public async Task InvalidPolicyAndGrantCombinationsFailClosed(
        string policy,
        string? grant,
        string expectedMessage)
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-invalid-");

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.DispatchAsync("workspace.create", CreateParameters(
                    "Invalid",
                    policy,
                    grant,
                    "direct")));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(expectedMessage, error.Message);
    }

    [TestMethod]
    public async Task ManagedDefaultRejectsMirroredStorageMode()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-mirror-");

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.DispatchAsync("workspace.create", CreateParameters(
                    "Invalid mirror",
                    "managedDefault",
                    null,
                    "mirrored")));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(
            "The managed default location requires direct storage mode.",
            error.Message);
    }

    [TestMethod]
    public async Task CreateLocationParamsRejectUnknownFields()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-unknown-");

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.DispatchAsync("workspace.create", new
                {
                    displayName = "Unknown",
                    locationPolicy = "managedDefault",
                    selectedRootGrant = (string?)null,
                    storageMode = "direct",
                    encryptionMode = "convenient",
                    userMarkedSync = false,
                    unexpected = true,
                }));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(
            "Workspace create params contain missing or unknown fields.",
            error.Message);
    }

    [TestMethod]
    public async Task ManagedDefaultRejectsManualSyncClassification()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-create-sync-");

        WorkspaceRegistryException error =
            await Assert.ThrowsExactlyAsync<WorkspaceRegistryException>(() =>
                fixture.DispatchAsync("workspace.create", new
                {
                    displayName = "Managed",
                    locationPolicy = "managedDefault",
                    selectedRootGrant = (string?)null,
                    storageMode = "direct",
                    encryptionMode = "convenient",
                    userMarkedSync = true,
                }));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(
            "The managed default location cannot be marked as sync-managed.",
            error.Message);
    }

    private static object CreateParameters(
        string displayName,
        string locationPolicy,
        string? selectedRootGrant,
        string storageMode) => new
        {
            displayName,
            locationPolicy,
            selectedRootGrant,
            storageMode,
            encryptionMode = "convenient",
            userMarkedSync = false,
        };
}
