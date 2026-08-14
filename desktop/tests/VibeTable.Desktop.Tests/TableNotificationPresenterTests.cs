using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class TableNotificationPresenterTests
{
    [TestMethod]
    public void CorrelatedMutationUsesResponseAndPreservesStableErrorEnvelope()
    {
        var sink = new FakeWebReplySink();
        var error = new MutationError(
            MutationErrorKind.EditConflict,
            "记录已变化。",
            CurrentRow: new Dictionary<string, object?> { ["name"] = "new" },
            ConflictingRowKeys: ["record-1"],
            FieldErrors: null,
            Code: "ROW_DIGEST_MISMATCH");

        TableNotificationPresenter.Post(
            sink,
            new TableNotification(
                "table.editRejected",
                Page: null,
                new MutationOutcome("update", false, error, Result: null),
                RequestId: "request-1"));

        FakeWebReplySink.Reply reply = sink.Replies.Single();
        Assert.AreEqual("table.editRejected", reply.Type);
        Assert.AreEqual("request-1", reply.RequestId);
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        Assert.AreEqual("edit_conflict", payload.GetProperty("kind").GetString());
        Assert.AreEqual("update", payload.GetProperty("operation").GetString());
        Assert.AreEqual("ROW_DIGEST_MISMATCH", payload.GetProperty("code").GetString());
    }

    [TestMethod]
    public void UncorrelatedMutationAndPageStayNotifications()
    {
        var sink = new FakeWebReplySink();
        TableNotificationPresenter.Post(
            sink,
            new TableNotification(
                "table.rowsInserted",
                Page: null,
                new MutationOutcome(
                    "insert",
                    true,
                    Error: null,
                    Result: new { inserted = 1 })));
        TableNotificationPresenter.Post(
            sink,
            new TableNotification(
                "table.datasetReady",
                Page: new VibeTable.Contracts.TablePage(
                    "orders",
                    [],
                    [],
                    Offset: 0,
                    Limit: 100,
                    TotalRows: 27,
                    Mode: "remote")));

        Assert.HasCount(2, sink.Replies);
        Assert.IsTrue(sink.Replies.All(reply => reply.RequestId is null));
        Assert.AreEqual(
            1,
            JsonSerializer.SerializeToElement(sink.Replies[0].Payload)
                .GetProperty("inserted")
                .GetInt32());
        JsonElement pagePayload = JsonSerializer.SerializeToElement(sink.Replies[1].Payload);
        Assert.AreEqual(27, pagePayload.GetProperty("totalRows").GetInt32());
        Assert.AreEqual("remote", pagePayload.GetProperty("mode").GetString());
        Assert.IsFalse(pagePayload.TryGetProperty("loadedRows", out _));
    }
}
