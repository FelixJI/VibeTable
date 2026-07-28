using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspacePathGrantStoreTests
{
    [TestMethod]
    public void WorkspaceRootGrantIsOpaqueOperationBoundAndSingleUse()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-grant-" + Guid.NewGuid().ToString("N"));
        var picker = new FakePicker { WorkspaceRoot = root };
        var grants = new WorkspacePathGrantStore(picker);
        Guid operationId = Guid.NewGuid();
        using JsonDocument source = JsonDocument.Parse(
            """{"selectedRootGrant":"host-picker://workspace-root"}""");

        JsonElement materialized = grants.MaterializeSentinels(
            "workspace.create",
            operationId,
            source.RootElement);
        string grantId = materialized
            .GetProperty("selectedRootGrant")
            .GetString()!;

        StringAssert.StartsWith(grantId, "host-path-grant://");
        Assert.IsFalse(grantId.Contains(root, StringComparison.Ordinal));
        Assert.ThrowsExactly<WorkspacePathGrantException>(() =>
            grants.Consume(
                grantId,
                "workspace.create",
                Guid.NewGuid(),
                "workspace-root"));
        Assert.AreEqual(
            Path.GetFullPath(root),
            grants.Consume(
                grantId,
                "workspace.create",
                operationId,
                "workspace-root"));
        Assert.ThrowsExactly<WorkspacePathGrantException>(() =>
            grants.Consume(
                grantId,
                "workspace.create",
                operationId,
                "workspace-root"));
    }

    [TestMethod]
    public void SidecarGrantBindsPurposeMethodAndOperation()
    {
        string package = Path.Combine(Path.GetTempPath(), "source.vtsnapshot");
        var grants = new WorkspacePathGrantStore(new FakePicker
        {
            SnapshotImport = package,
        });
        Guid operationId = Guid.NewGuid();
        using JsonDocument source = JsonDocument.Parse(
            """{"pathGrant":"host-picker://snapshot-import"}""");
        JsonElement materialized = grants.MaterializeSentinels(
            "snapshot.inspectPackage",
            operationId,
            source.RootElement);

        WorkspaceSidecarPathGrant? binding = grants.ConsumeForSidecar(
            materialized,
            "snapshot.inspectPackage",
            operationId);

        Assert.IsNotNull(binding);
        Assert.AreEqual("snapshot-import", binding.Purpose);
        Assert.AreEqual(operationId, binding.OperationId);
        Assert.AreEqual(Path.GetFullPath(package), binding.Path);
        Assert.ThrowsExactly<WorkspacePathGrantException>(() =>
            grants.ConsumeForSidecar(
                materialized,
                "snapshot.inspectPackage",
                operationId));
    }

    [TestMethod]
    public void SnapshotExtractGrantUsesDedicatedPurposeAndIsSingleUse()
    {
        string target = Path.Combine(Path.GetTempPath(), "季度规划.docx");
        var grants = new WorkspacePathGrantStore(new FakePicker
        {
            SnapshotExport = target,
        });
        Guid operationId = Guid.NewGuid();
        using JsonDocument source = JsonDocument.Parse(
            """{"planId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","pathGrant":"host-picker://snapshot-extract"}""");
        JsonElement materialized = grants.MaterializeSentinels(
            "snapshot.applyExtract",
            operationId,
            source.RootElement);

        WorkspaceSidecarPathGrant? binding = grants.ConsumeForSidecar(
            materialized,
            "snapshot.applyExtract",
            operationId);

        Assert.IsNotNull(binding);
        Assert.AreEqual("snapshot-extract", binding.Purpose);
        Assert.AreEqual(Path.GetFullPath(target), binding.Path);
        Assert.ThrowsExactly<WorkspacePathGrantException>(() =>
            grants.ConsumeForSidecar(
                materialized,
                "snapshot.applyExtract",
                operationId));
        Assert.IsFalse(
            VibeTable.Desktop.MainWindow.IsWorkspaceMutation(
                "snapshot.applyExtract"),
            "Extract writes only to an operation-bound external path grant.");
    }

    private sealed class FakePicker : IWorkspacePathPicker
    {
        public string? WorkspaceRoot { get; init; }
        public string? SnapshotExport { get; init; }
        public string? SnapshotImport { get; init; }
        public string? FileUpgrade { get; init; }
        public string? PickWorkspaceRoot() => WorkspaceRoot;
        public string? PickSnapshotExportTarget() => SnapshotExport;
        public string? PickSnapshotExtractTarget() => SnapshotExport;
        public string? PickSnapshotImportSource() => SnapshotImport;
        public string? PickFileUpgradeSource() => FileUpgrade;
    }
}
