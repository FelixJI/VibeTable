using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

public sealed partial class HostProductRpcInvokerTests
{
    [TestMethod]
    [DataRow("paste_token_expired", "paste_error", MutationErrorKind.Validation, true)]
    [DataRow("paste_token_invalid", "paste_error", MutationErrorKind.Validation, true)]
    [DataRow("paste_token_unknown", "paste_error", MutationErrorKind.Validation, true)]
    [DataRow("paste_plan_invalid", "paste_error", MutationErrorKind.Validation, true)]
    [DataRow("schema_mismatch", "paste_error", MutationErrorKind.SchemaMismatch, true)]
    [DataRow("schema_unknown", "paste_error", MutationErrorKind.SchemaMismatch, true)]
    [DataRow("mutation.field.invalid_value", "paste_error", MutationErrorKind.Validation, false)]
    [DataRow("mutation.relation.restricted", "paste_error", MutationErrorKind.Validation, false)]
    [DataRow("paste_token_consumed", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("paste_idempotency_mismatch", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("mutation.idempotency_conflict", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("mutation.storage.failed", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("mutation.internal.failed", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("mutation_invalid_response", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("paste_future_code", "paste_error", MutationErrorKind.Unknown, false)]
    [DataRow("paste_token_expired", "wrong_kind", MutationErrorKind.Unknown, false)]
    public async Task Review17PasteRejectionOwnsItsTerminalAndReadBarrier(string code, string wireKind, MutationErrorKind expectedKind, bool notSubmitted)
    {
        const string reason = "The public paste rejection reason.";
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        await using var fixture = await HostFixture.OpenAsync();
        fixture.Http.Error = JsonSerializer.SerializeToElement(new { code = -32040, message = "Paste error", data = new { kind = wireKind, code, message = reason } });
        fixture.Http.BeforeReply = (_, _) => release.Task;
        using var gateway = fixture.TableGateway();
        var service = new TableWorkspaceService(gateway); var sink = new FakeWebReplySink();
        using var barrier = new TableMutationReadBarrier();
        var controller = new WorkspaceTableRequestController(service, new FakeDatabasePicker(null), sink, () => null) { MutationReads = barrier };
        var request = new RoutedWebRequest("table.applyPasteRequested", "owned-paste", JsonSerializer.SerializeToElement(new { collection = "orders", token = "preview-token", idempotencyKey = "apply-key" }), "");
        Task write = controller.DispatchAsync(request);
        await fixture.Http.RpcEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Task recovery = barrier.WaitAsync(request, CancellationToken.None); Assert.IsFalse(recovery.IsCompleted);
        release.SetResult(); await write.WaitAsync(TimeSpan.FromSeconds(2));
        var reply = sink.Replies.Single(); Assert.AreEqual(request.RequestId, reply.RequestId);
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        if (expectedKind == MutationErrorKind.Unknown)
        {
            await Assert.ThrowsExactlyAsync<TableMutationUnconfirmedException>(() => recovery);
            Assert.AreEqual("operation.failed", reply.Type);
        }
        else
        {
            await recovery.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.AreEqual("table.editRejected", reply.Type);
            Assert.AreEqual(MutationErrorMapper.ToWireKind(expectedKind), payload.GetProperty("kind").GetString());
            Assert.AreEqual(code, payload.GetProperty("code").GetString()); Assert.AreEqual(reason, payload.GetProperty("message").GetString());
            Assert.AreEqual("applyPaste", payload.GetProperty("operation").GetString());
            Assert.AreEqual(notSubmitted, payload.TryGetProperty("notSubmitted", out var fact) && fact.GetBoolean());
            // A later known rejection cannot clear uncertainty left by another logical write.
            await barrier.TrackAsync(request, settled => { settled(false); return Task.CompletedTask; });
            await controller.DispatchAsync(request with { RequestId = "another-paste" });
            await Assert.ThrowsExactlyAsync<TableMutationUnconfirmedException>(() => barrier.WaitAsync(request, CancellationToken.None));
        }
        Assert.AreEqual(expectedKind == MutationErrorKind.Unknown ? 1 : 2, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "table.applyPaste"));
    }

}
