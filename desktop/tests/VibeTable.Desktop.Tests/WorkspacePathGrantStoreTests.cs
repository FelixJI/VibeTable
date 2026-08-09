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
        Assert.IsTrue(
            VibeTable.Desktop.MainWindow.IsWorkspaceMutation(
                "history.applyRestore"),
            "History restore mutates the authoritative workspace database.");
        Assert.IsFalse(
            VibeTable.Desktop.MainWindow.IsWorkspaceMutation(
                "history.previewRestore"),
            "History restore preview is read-only.");
    }

    [TestMethod]
    public void TestModePickerReadsEveryFixedControlWithoutOpeningNativeDialogs()
    {
        string controls = CreateControlsDirectory();
        try
        {
            string root = Path.Combine(controls, "workspace-root");
            string exports = Path.Combine(controls, "exports");
            string extracts = Path.Combine(controls, "extracts");
            string imported = Path.Combine(controls, "source.vtsnapshot");
            string upgrade = Path.Combine(controls, "upgrade.bin");
            Directory.CreateDirectory(root);
            Directory.CreateDirectory(exports);
            Directory.CreateDirectory(extracts);
            File.WriteAllText(imported, "package");
            File.WriteAllText(upgrade, "upgrade");
            WriteControl(controls, "workspace-root.txt", root);
            WriteControl(controls, "snapshot-export-target.txt", Path.Combine(exports, "out.vtsnapshot"));
            WriteControl(controls, "snapshot-import-source.txt", imported);
            WriteControl(controls, "snapshot-extract-target.txt", Path.Combine(extracts, "document.docx"));
            WriteControl(controls, "file-upgrade-source.txt", upgrade);

            var picker = new TestModeWorkspacePathPicker(controls);

            Assert.AreEqual(Path.GetFullPath(root), picker.PickWorkspaceRoot());
            Assert.AreEqual(
                Path.GetFullPath(Path.Combine(exports, "out.vtsnapshot")),
                picker.PickSnapshotExportTarget());
            Assert.AreEqual(Path.GetFullPath(imported), picker.PickSnapshotImportSource());
            Assert.AreEqual(
                Path.GetFullPath(Path.Combine(extracts, "document.docx")),
                picker.PickSnapshotExtractTarget());
            Assert.AreEqual(Path.GetFullPath(upgrade), picker.PickFileUpgradeSource());
        }
        finally
        {
            Directory.Delete(controls, recursive: true);
        }
    }

    [TestMethod]
    public void TestModePickerFailsClosedForMissingOrEmptyControl()
    {
        string controls = CreateControlsDirectory();
        try
        {
            var picker = new TestModeWorkspacePathPicker(controls);

            WorkspacePathGrantException missing = Assert.ThrowsExactly<WorkspacePathGrantException>(
                picker.PickWorkspaceRoot);
            Assert.AreEqual("workspace.test_control_missing", missing.Code);

            WriteControl(controls, "workspace-root.txt", "   ");
            WorkspacePathGrantException empty = Assert.ThrowsExactly<WorkspacePathGrantException>(
                picker.PickWorkspaceRoot);
            Assert.AreEqual("workspace.test_control_invalid", empty.Code);
        }
        finally
        {
            Directory.Delete(controls, recursive: true);
        }
    }

    [TestMethod]
    public void TestModePickerRejectsMissingSourceFilesAndOutputParents()
    {
        string controls = CreateControlsDirectory();
        try
        {
            WriteControl(controls, "snapshot-import-source.txt", Path.Combine(controls, "missing.vtsnapshot"));
            WriteControl(controls, "file-upgrade-source.txt", Path.Combine(controls, "missing.bin"));
            WriteControl(controls, "snapshot-export-target.txt", Path.Combine(controls, "missing", "out.vtsnapshot"));
            var picker = new TestModeWorkspacePathPicker(controls);

            Assert.AreEqual(
                "workspace.test_control_source_missing",
                Assert.ThrowsExactly<WorkspacePathGrantException>(
                    picker.PickSnapshotImportSource).Code);
            Assert.AreEqual(
                "workspace.test_control_source_missing",
                Assert.ThrowsExactly<WorkspacePathGrantException>(
                    picker.PickFileUpgradeSource).Code);
            Assert.AreEqual(
                "workspace.test_control_parent_missing",
                Assert.ThrowsExactly<WorkspacePathGrantException>(
                    picker.PickSnapshotExportTarget).Code);
        }
        finally
        {
            Directory.Delete(controls, recursive: true);
        }
    }

    private static string CreateControlsDirectory()
    {
        string controls = Path.Combine(Path.GetTempPath(), "vibetable-picker-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(controls);
        return controls;
    }

    private static void WriteControl(string controls, string name, string value) =>
        File.WriteAllText(Path.Combine(controls, name), value + Environment.NewLine);

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
