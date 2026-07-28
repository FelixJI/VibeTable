using System.Text.Json;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceCreationLocationTests
{
    [TestMethod]
    public void ManagedDefaultUsesProductDataAndNeverOpensThePicker()
    {
        Guid operationId = Guid.NewGuid();
        Guid workspaceId = Guid.NewGuid();
        var picker = new RecordingPicker(@"D:\chosen");
        var grants = new WorkspacePathGrantStore(picker);
        string productData = Path.Combine(
            Path.GetTempPath(),
            "vibetable-tests",
            Guid.NewGuid().ToString("N"));
        using JsonDocument document = JsonDocument.Parse(
            """
            {
              "displayName": "Managed",
              "locationPolicy": "managedDefault",
              "selectedRootGrant": null,
              "storageMode": "direct",
              "encryptionMode": "convenient"
            }
            """);

        string resolved = MainWindow.ResolveWorkspaceCreateRoot(
            document.RootElement,
            operationId,
            grants,
            productData,
            workspaceId);

        Assert.AreEqual(
            Path.Combine(
                Path.GetFullPath(productData),
                "workspaces",
                workspaceId.ToString("D")),
            resolved);
        Assert.AreEqual(0, picker.WorkspacePickCount);
    }

    [TestMethod]
    public void OtherLocationMaterializesAndConsumesTheNativeGrant()
    {
        Guid operationId = Guid.NewGuid();
        Guid workspaceId = Guid.NewGuid();
        string chosen = Path.Combine(
            Path.GetTempPath(),
            "vibetable-selected",
            Guid.NewGuid().ToString("N"));
        var picker = new RecordingPicker(chosen);
        var grants = new WorkspacePathGrantStore(picker);
        using JsonDocument document = JsonDocument.Parse(
            """
            {
              "displayName": "Other",
              "locationPolicy": "other",
              "selectedRootGrant": "host-picker://workspace-root",
              "storageMode": "direct",
              "encryptionMode": "convenient"
            }
            """);

        string resolved = MainWindow.ResolveWorkspaceCreateRoot(
            document.RootElement,
            operationId,
            grants,
            Path.GetTempPath(),
            workspaceId);

        Assert.AreEqual(chosen, resolved);
        Assert.AreEqual(1, picker.WorkspacePickCount);
    }

    [TestMethod]
    [DataRow(
        "managedDefault",
        "\"host-picker://workspace-root\"",
        "The managed default location must not include a path grant.")]
    [DataRow(
        "other",
        "null",
        "The other location requires a selectedRootGrant.")]
    [DataRow(
        "remote",
        "null",
        "Workspace locationPolicy is invalid.")]
    public void InvalidPolicyAndGrantCombinationsFailClosed(
        string policy,
        string grantJson,
        string expectedMessage)
    {
        var grants = new WorkspacePathGrantStore(
            new RecordingPicker(Path.GetTempPath()));
        using JsonDocument document = JsonDocument.Parse(
            $$"""
            {
              "displayName": "Invalid",
              "locationPolicy": "{{policy}}",
              "selectedRootGrant": {{grantJson}},
              "storageMode": "direct",
              "encryptionMode": "convenient"
            }
            """);

        WorkspaceRegistryException error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => MainWindow.ResolveWorkspaceCreateRoot(
                document.RootElement,
                Guid.NewGuid(),
                grants,
                Path.GetTempPath(),
                Guid.NewGuid()));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(expectedMessage, error.Message);
    }

    [TestMethod]
    public void CreateLocationParamsRejectUnknownFields()
    {
        using JsonDocument document = JsonDocument.Parse(
            """
            {
              "displayName": "Unknown",
              "locationPolicy": "managedDefault",
              "selectedRootGrant": null,
              "storageMode": "direct",
              "encryptionMode": "convenient",
              "unexpected": true
            }
            """);

        WorkspaceRegistryException error = Assert.ThrowsExactly<WorkspaceRegistryException>(
            () => MainWindow.ResolveWorkspaceCreateRoot(
                document.RootElement,
                Guid.NewGuid(),
                new WorkspacePathGrantStore(
                    new RecordingPicker(Path.GetTempPath())),
                Path.GetTempPath(),
                Guid.NewGuid()));

        Assert.AreEqual("workspace.request_invalid", error.Code);
        Assert.AreEqual(
            "Workspace create params contain missing or unknown fields.",
            error.Message);
    }

    private sealed class RecordingPicker(string workspaceRoot) : IWorkspacePathPicker
    {
        public int WorkspacePickCount { get; private set; }

        public string? PickWorkspaceRoot()
        {
            WorkspacePickCount++;
            return workspaceRoot;
        }

        public string? PickSnapshotExportTarget() => null;
        public string? PickSnapshotImportSource() => null;
        public string? PickSnapshotExtractTarget() => null;
        public string? PickFileUpgradeSource() => null;
    }
}
