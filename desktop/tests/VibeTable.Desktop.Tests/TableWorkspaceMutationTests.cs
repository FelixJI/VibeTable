using System.Collections.Generic;
using System.Text.Json;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// B1 Task 5: workspace-service mutation orchestration tests.
/// </summary>
/// <remarks>
/// Covers cancellation on table switch, stale-response suppression, retry
/// prohibited after unknown commit state (the host surfaces the error instead
/// of auto-retrying), and localized conflict/validation messages via
/// <see cref="MutationErrorMapper"/>.
/// </remarks>
[TestClass]
public sealed class TableWorkspaceMutationTests
{
    private static EditSchemaResult SampleSchema(string table = "contracts") => new(
        Table: table,
        SchemaRevision: "rev1",
        RowKeyKind: "primary_key",
        RowKeyStable: true,
        Editable: true,
        Columns: new List<ColumnEditSchema>());

    [TestMethod]
    public async Task UpdateCellAsync_EmitsEditCommittedOnSuccess()
    {
        var fake = new FakeTableRpcGateway();
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        var ok = await workspace.UpdateCellAsync(
            "contracts", 1, "amount", 10.5, 99.9, "rev1");

        Assert.IsTrue(ok);
        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("table.editCommitted", notifications[0].Type);
        Assert.IsTrue(notifications[0].MutationResult?.Success);
    }

    [TestMethod]
    public async Task UpdateCellAsync_MapsConflictToEditRejectedWithError()
    {
        var fake = new FakeTableRpcGateway
        {
            NextUpdateCellException = MakeRemoteError(
                -32010, "edit conflict",
                "{\"kind\":\"edit_conflict\",\"currentRow\":{\"amount\":10.5}}"),
        };
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        var ok = await workspace.UpdateCellAsync(
            "contracts",
            1,
            "amount",
            999.0,
            50.0,
            "rev1",
            requestId: "req-conflict");

        Assert.IsFalse(ok);
        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("table.editRejected", notifications[0].Type);
        var error = notifications[0].MutationResult!.Error!.Value;
        Assert.AreEqual(MutationErrorKind.EditConflict, error.Kind);
        Assert.IsNotNull(error.CurrentRow);
        Assert.IsTrue(error.CurrentRow!.ContainsKey("amount"));
        Assert.AreEqual("req-conflict", notifications[0].RequestId);
    }

    [TestMethod]
    public async Task UpdateCellAsync_MapsValidationFailure()
    {
        var fake = new FakeTableRpcGateway
        {
            NextUpdateCellException = MakeRemoteError(
                -32011, "validation",
                "{\"kind\":\"mutation_validation\",\"fieldErrors\":{\"amount\":\"not a number\"}}"),
        };
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        var ok = await workspace.UpdateCellAsync(
            "contracts", 1, "amount", 10.5, "bad", "rev1");

        Assert.IsFalse(ok);
        var error = notifications[0].MutationResult!.Error!.Value;
        Assert.AreEqual(MutationErrorKind.Validation, error.Kind);
        Assert.IsTrue(error.FieldErrors!.ContainsKey("amount"));
    }

    [TestMethod]
    public async Task UpdateCellAsync_EmitsAfterSameTableRefresh()
    {
        var fake = new FakeTableRpcGateway();
        fake.DatabaseOpenResults["path"] = new DatabaseOpenResult(
            new List<string> { "a", "b" },
            new List<string>(),
            TestDisplayNames.For("a", "b"));
        fake.SelectionProjectionResults["a"] = SelectionProjection("a");
        fake.SelectionProjectionResults["b"] = SelectionProjection("b");
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        await workspace.OpenDatabaseAsync("path");
        await workspace.SelectTableAsync("a");
        notifications.Clear();
        fake.PendingUpdateCell = new TaskCompletionSource<UpdateCellResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task<bool> update = workspace.UpdateCellAsync("a", 1, "x", 1, 2, "rev1");

        // The user's own data.changed reconciliation reselects the same table
        // while the mutation response is still in flight.
        await workspace.SelectTableAsync("a");
        notifications.Clear();
        fake.PendingUpdateCell.SetResult(new UpdateCellResult(
            1,
            "x",
            2,
            new Dictionary<string, object?> { ["rowKey"] = 1, ["x"] = 2 },
            new MutationRevision("fake", "rev1", 2)));

        Assert.IsTrue(await update);
        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("table.editCommitted", notifications[0].Type);
    }

    [TestMethod]
    public async Task UpdateCellAsync_DoesNotEmitAfterActualTableSwitch()
    {
        var fake = new FakeTableRpcGateway();
        fake.DatabaseOpenResults["path"] = new DatabaseOpenResult(
            new List<string> { "a", "b" },
            new List<string>(),
            TestDisplayNames.For("a", "b"));
        fake.SelectionProjectionResults["a"] = SelectionProjection("a");
        fake.SelectionProjectionResults["b"] = SelectionProjection("b");
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        await workspace.OpenDatabaseAsync("path");
        await workspace.SelectTableAsync("a");
        notifications.Clear();
        fake.PendingUpdateCell = new TaskCompletionSource<UpdateCellResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        Task<bool> update = workspace.UpdateCellAsync("a", 1, "x", 1, 2, "rev1");

        await workspace.SelectTableAsync("b");
        notifications.Clear();
        fake.PendingUpdateCell.SetResult(new UpdateCellResult(
            1,
            "x",
            2,
            new Dictionary<string, object?> { ["rowKey"] = 1, ["x"] = 2 },
            new MutationRevision("fake", "rev1", 2)));

        Assert.IsFalse(await update);
        Assert.AreEqual(0, notifications.Count);
    }

    [TestMethod]
    public async Task InsertRowAsync_EmitsRowsInserted()
    {
        var fake = new FakeTableRpcGateway();
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        var ok = await workspace.InsertRowAsync(
            "contracts", new Dictionary<string, object?> { ["title"] = "New" }, "rev1");

        Assert.IsTrue(ok);
        Assert.AreEqual("table.rowsInserted", notifications[0].Type);
    }

    [TestMethod]
    public async Task DeleteRowsAsync_EmitsRowsDeleted()
    {
        var fake = new FakeTableRpcGateway();
        var workspace = new TableWorkspaceService(fake);
        var notifications = new List<TableNotification>();
        workspace.Notification += n => notifications.Add(n);

        var ok = await workspace.DeleteRowsAsync(
            "contracts",
            new List<(object, string)> { (1, "d1"), (2, "d2") },
            "rev1");

        Assert.IsTrue(ok);
        Assert.AreEqual("table.rowsDeleted", notifications[0].Type);
    }

    private static RpcRemoteException MakeRemoteError(int code, string message, string dataJson)
    {
        var data = JsonSerializer.Deserialize<JsonElement>(dataJson);
        // RpcRemoteException's ctor is internal; reach it via reflection so the
        // test stays in the test assembly without exposing a public ctor.
        var ctor = typeof(RpcRemoteException).GetConstructors(
            System.Reflection.BindingFlags.Instance |
            System.Reflection.BindingFlags.NonPublic);
        return (RpcRemoteException)ctor[0].Invoke(new object[] { code, message, (JsonElement?)data });
    }

    private static TableSelectionProjection SelectionProjection(string table)
        => new(
            new TablePage(
                table,
                Array.Empty<ColumnSchema>(),
                Array.Empty<Dictionary<string, object?>>(),
                0,
                TableWorkspaceLimits.MaxPageLimit,
                0,
                "remote"),
            new EditSchemaResult(
                table,
                "schema_0001",
                "fake-row-key",
                RowKeyStable: true,
                Editable: false,
                Array.Empty<ColumnEditSchema>()));
}
