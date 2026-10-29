using System.Net;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceFilesTests
{
    [TestMethod]
    [DataRow("{\"fresh\":true,\"value\":\"x\",\"other\":42}")]
    [DataRow("{\"fresh\":true}")]
    public async Task DatasetCsvPreservesOrdinaryJsonWithEnvelopeNamedProperties(string value)
    {
        await using var f = new Fixture();
        var content = JsonNode.Parse(PluginSurfaceApiTests.DatasetBody)!;
        var source = content["sources"]![0]!;
        source["computedFields"] = new JsonArray();
        source["items"] = new JsonArray(new JsonObject { ["id"] = "a", ["amount"] = JsonNode.Parse(value) });
        source["filteredRows"] = 1;
        source["totalRows"] = 1;
        f.Http.DatasetResultBody = content.ToJsonString();
        string id = (await f.Call("dataset.open", """{"contract":"vibetable.plugin-dataset.v1","sources":[]}""")).GetProperty("result").GetProperty("datasetId").GetString()!;
        var page = (await f.Call("dataset.page", JsonSerializer.Serialize(new { datasetId = id, collection = "orders", offset = 0, pageSize = 200 }))).GetProperty("result");
        Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(value), JsonNode.Parse(page.GetProperty("items")[0].GetProperty("amount").GetRawText())));
        var exported = await f.Call("dataset.export", JsonSerializer.Serialize(new { datasetId = id, collection = "orders", suggestedName = "synthetic.csv" }));
        Assert.IsTrue(exported.TryGetProperty("result", out var receipt), exported.GetRawText());
        Assert.IsTrue(receipt.GetProperty("committed").GetBoolean());
        Assert.AreEqual("\"id\",\"amount\"\r\n\"a\",\"" + value.Replace("\"", "\"\"", StringComparison.Ordinal) + "\"\r\n", File.ReadAllText(f.Target));
    }

    [TestMethod]
    public async Task DatasetCsvUnwrapsOnlyAnAuthoritativelyDeclaredComputedField()
    {
        await using var f = new Fixture();
        var content = JsonNode.Parse(PluginSurfaceApiTests.DatasetBody)!;
        content["sources"]![0]!["computedFields"] = new JsonArray("amount");
        content["sources"]![0]!["items"] = new JsonArray(new JsonObject { ["id"] = "a", ["amount"] = new JsonObject { ["state"] = "ready", ["fresh"] = true, ["value"] = "12.34" } });
        f.Http.DatasetResultBody = content.ToJsonString();
        string id = (await f.Call("dataset.open", """{"contract":"vibetable.plugin-dataset.v1","sources":[]}""")).GetProperty("result").GetProperty("datasetId").GetString()!;
        var exported = await f.Call("dataset.export", JsonSerializer.Serialize(new { datasetId = id, collection = "orders", suggestedName = "synthetic.csv" }));
        Assert.IsTrue(exported.GetProperty("result").GetProperty("committed").GetBoolean());
        Assert.AreEqual("\"id\",\"amount\"\r\n\"a\",\"12.34\"\r\n", File.ReadAllText(f.Target));
    }

    [TestMethod]
    public async Task DatasetCsvUsesTheDisplayedRowsAndNativeCancellationOrStalePreservesTarget()
    {
        await using var f = new Fixture();
        string id = (await f.Call("dataset.open", """{"contract":"vibetable.plugin-dataset.v1","sources":[]}""")).GetProperty("result").GetProperty("datasetId").GetString()!;
        string parameters = JsonSerializer.Serialize(new { datasetId = id, collection = "orders", suggestedName = "synthetic.csv" });
        f.CancelPick = true;
        Assert.AreEqual(JsonValueKind.Null, (await f.Call("dataset.export", parameters)).GetProperty("result").ValueKind);
        Assert.AreEqual("old", File.ReadAllText(f.Target));
        f.CancelPick = false;
        f.AfterPick = () => f.Http.DatasetStale = true;
        var stale = await f.Call("dataset.export", parameters);
        Assert.AreEqual("plugin_dataset_stale", stale.GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual("old", File.ReadAllText(f.Target));
        Assert.AreEqual(0, f.Leases);
        f.AfterPick = null; f.Http.DatasetStale = false;
        id = (await f.Call("dataset.open", """{"contract":"vibetable.plugin-dataset.v1","sources":[]}""")).GetProperty("result").GetProperty("datasetId").GetString()!;
        var receipt = (await f.Call("dataset.export", JsonSerializer.Serialize(new { datasetId = id, collection = "orders", suggestedName = "synthetic.csv" }))).GetProperty("result");
        Assert.IsTrue(receipt.GetProperty("committed").GetBoolean());
        Assert.AreEqual(id, receipt.GetProperty("datasetId").GetString());
        Assert.AreEqual(2, receipt.GetProperty("rowsWritten").GetInt32());
        Assert.AreEqual("\"id\",\"amount\"\r\n\"a\",\"9007199254740993.01\"\r\n\"b\",\"0.01\"\r\n", File.ReadAllText(f.Target));
        Assert.AreEqual(0, f.Leases);
    }
    [TestMethod]
    public async Task ImportReservationIsReleasedWhenLastPlanSlotFillsDuringFreeze()
    {
        await using var f = new Fixture();
        const string mutation = "{\"collection\":\"orders\",\"operations\":[{\"kind\":\"create\",\"values\":{\"name\":\"new\"}}]}";
        for (int i = 0; i < 7; i++) await f.Call("mutation.preview", mutation);
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        // Freeze has passed the first plan-count check. The broker's authorization
        // then admits the eighth plan before reserve returns to the handoff.
        f.Http.OnFreeze = () => f.AfterAuthorize = () =>
            Assert.IsTrue(f.Call("mutation.preview", mutation).GetAwaiter().GetResult().TryGetProperty("result", out _));
        var failed = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("plugin_plan_limit", failed.GetProperty("result").GetProperty("error").GetProperty("code").GetString(), failed.GetRawText());
        var reservations = (System.Collections.IDictionary)typeof(HostSessionFileBroker)
            .GetField("_reservations", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(f.Broker)!;
        Assert.AreEqual(1, reservations.Count, "the real broker must have reserved before the failed handoff");
        string reservationId = reservations.Keys.Cast<string>().Single();
        object reservation = reservations[reservationId]!;
        Assert.AreEqual("released", reservation.GetType().GetProperty("Outcome", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(reservation));
        // A released receipt is replayable, but cannot be changed to consumed.
        // This checks broker state rather than a mocked settle call.
        var released = await f.Broker.HandleAsync("settleImport", JsonSerializer.SerializeToElement(new { reservationId, outcome = "released" }), CancellationToken.None);
        Assert.AreEqual("released", released.GetProperty("outcome").GetString());
        await Assert.ThrowsAsync<IOException>(() => f.Broker.HandleAsync("settleImport",
            JsonSerializer.SerializeToElement(new { reservationId, outcome = "consumed" }), CancellationToken.None));
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "rejected"));
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None));
    }

    [TestMethod]
    [DataRow(128)]
    [DataRow(129)]
    [DataRow(180 * 1024)]
    public async Task WritePickerBoundsMediaTypeBeforeNativeSelection(int length)
    {
        await using var f = new Fixture();
        int selections = 0;
        f.AfterPick = () => selections++;
        string mediaType = new('<', length);
        string parameters = "{\"suggestedName\":\"out.txt\",\"mediaType\":\"" + mediaType + "\"}";
        string raw = "{\"contract\":\"" + PluginSurfaceApi.Contract + "\",\"surfaceToken\":\"" + f.Surface.SurfaceToken
            + "\",\"requestId\":\"media-boundary\",\"method\":\"file.pickWrite\",\"params\":" + parameters + "}";
        var result = JsonDocument.Parse(await f.Api.HandleAsync(f.Surface.SurfaceToken, f.Surface.DocumentUri.AbsoluteUri, raw)).RootElement.Clone();
        if (length == 128)
        {
            Assert.AreEqual(mediaType, result.GetProperty("result").GetProperty("mediaType").GetString());
            Assert.AreEqual(1, selections);
            Assert.AreEqual(1, f.GrantCount);
        }
        else
        {
            Assert.AreEqual("plugin_capability_invalid", result.GetProperty("error").GetProperty("code").GetString(), result.GetRawText());
            Assert.AreEqual(0, selections, "invalid metadata must not open the native picker");
            Assert.AreEqual(0, f.GrantCount, "invalid metadata must not mint a grant");
            Assert.AreEqual(0, f.Http.Calls.Count, "validation must precede file authorization");
        }
    }
    [TestMethod]
    public async Task UpsertImportStartRequiresApprovedKeyBeforeAnyAuthorization()
    {
        await using var f = new Fixture();
        foreach (var upsertKey in new[] { "null", "\"\"", "12" })
        {
            var denied = await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"upsert\",\"upsertKey\":" + upsertKey + ",\"columnMapping\":[]}");
            Assert.IsTrue(denied.TryGetProperty("error", out var deniedError) && deniedError.GetProperty("code").GetString() == "plugin_capability_invalid", denied.GetRawText());
        }
        var invalidMode = await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"recreate\",\"upsertKey\":null,\"columnMapping\":[]}");
        Assert.AreEqual("plugin_capability_invalid", invalidMode.GetProperty("error").GetProperty("code").GetString(), invalidMode.GetRawText());
        Assert.AreEqual(0, f.Http.Calls.Count, "validation must precede surface authorization");
        Assert.AreEqual(0, f.GrantCount, "invalid metadata must not mint a grant");
        int selections = 0; f.AfterPick = () => selections++;
        // An approved non-empty key still starts normally and keeps the picker flow.
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"upsert\",\"upsertKey\":\"key\",\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.AreEqual(1, selections);
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "data.ioProfile"
            && call.GetProperty("request").GetProperty("mode").GetString() == "upsert"));
        await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
    }

    [TestMethod]
    public async Task NativePickerFilesAreOpaqueOwnedBoundedAndRetiredBeforeFinalSave()
    {
        await using var f = new Fixture();
        string id = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
        Assert.IsFalse((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).GetRawText().Contains(f.Root, StringComparison.Ordinal));
        Assert.IsTrue((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).GetProperty("result").GetProperty("eof").GetBoolean());
        await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        Assert.IsTrue((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).TryGetProperty("error", out _));
        string write = (await f.Call("file.pickWrite", "{\"suggestedName\":\"out.txt\",\"mediaType\":\"text/plain\"}")).GetProperty("result").GetProperty("grantId").GetString()!;
        f.AfterAuthorize = () => f.Surfaces.CloseAll();
        var rejected = await f.Call("file.write", JsonSerializer.Serialize(new { grantId = write, offset = 0, base64 = Convert.ToBase64String(Encoding.UTF8.GetBytes("new")), finish = true }));
        Assert.IsTrue(rejected.TryGetProperty("error", out _)); Assert.AreEqual("old", File.ReadAllText(f.Target));
        Assert.AreEqual(0, Directory.GetFiles(f.Root, ".vibetable-*.tmp").Length);
    }

    [TestMethod]
    public async Task PickerCancellationAndInstallationRetirementCannotMintGrants()
    {
        await using var f = new Fixture(); f.CancelPick = true;
        Assert.AreEqual(JsonValueKind.Null, (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").ValueKind);
        f.CancelPick = false; f.AfterPick = () => f.Surfaces.CloseForInstallation(f.Surface.ProjectKey, f.Surface.PluginId);
        Assert.IsTrue((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).TryGetProperty("error", out _));
        Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task ImportUsesCapturedDataIoRunAndNativePreciseConfirmationWithoutLeakingTokens()
    {
        await using var f = new Fixture();
        var started = await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}");
        string taskId = started.GetProperty("result").GetProperty("taskId").GetString()!;
        var preview = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        string planId = preview.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsFalse(preview.GetRawText().Contains("internal-go-token", StringComparison.Ordinal));
        Assert.IsFalse(preview.GetRawText().Contains("grant-", StringComparison.Ordinal));
        Assert.IsNull(f.ReservationOutcomes.Single(), "a successful handoff must retain the reservation for submit");
        await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        var result = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("succeeded", result.GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
        Assert.IsTrue(f.NativeConfirmation); Assert.IsTrue(f.Http.Calls.Any(value => value.GetProperty("method").GetString() == "import.bind"));
        await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("succeeded", (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }))).GetProperty("result").GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task PrecommitExportFailureSurfacesDefiniteFailureWithoutUnknownOutcome()
    {
        await using var f = new Fixture();
        f.ExportStatus = JsonSerializer.SerializeToElement(new
        {
            kind = "data.export", state = "failed",
            progress = new { done = 0, total = 0, message = "" },
            result = (object?)null,
            error = "query includes computed cells waiting for recalculation",
            commitReceipt = "not_committed",
        });
        string taskId = (await f.Call("data.export.start",
            "{\"collection\":\"orders\",\"fields\":[\"title\"],\"query\":{},\"format\":\"csv\",\"suggestedName\":\"out.csv\"}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var failed = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        var terminal = failed.GetProperty("result");
        Assert.AreEqual("failed", terminal.GetProperty("state").GetString(), failed.GetRawText());
        Assert.AreEqual("plugin_data_io_failed", terminal.GetProperty("error").GetProperty("code").GetString());
        // The worker's positive not-committed receipt turns this into a definite
        // failure; the SDK then classifies via the stable error code instead of
        // forcing plugin_commit_unknown over a provably uncommitted export.
        Assert.IsFalse(terminal.TryGetProperty("outcome", out _), "a positively not-committed export failure must not claim an unknown commit outcome");
        Assert.IsFalse(failed.GetRawText().Contains("commitReceipt", StringComparison.Ordinal), "the internal receipt never crosses to plugins");
    }

    [TestMethod]
    public async Task LostCommitReceiptExportFailureKeepsUnknownOutcome()
    {
        await using var f = new Fixture();
        f.ExportStatus = JsonSerializer.SerializeToElement(new
        {
            kind = "data.export", state = "failed",
            progress = new { done = 1, total = 1, message = "" },
            result = (object?)null,
            error = "Export submission outcome is unknown; verify the target file.",
        });
        string taskId = (await f.Call("data.export.start",
            "{\"collection\":\"orders\",\"fields\":[\"title\"],\"query\":{},\"format\":\"csv\",\"suggestedName\":\"out.csv\"}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var failed = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        var terminal = failed.GetProperty("result");
        Assert.AreEqual("failed", terminal.GetProperty("state").GetString(), failed.GetRawText());
        // A missing receipt must stay fail-closed: the export entered its native
        // commit and lost the receipt, so only the unknown outcome is honest.
        Assert.AreEqual("unknown", terminal.GetProperty("outcome").GetString());
    }

    [TestMethod]
    public async Task AbortedExportStaysUnknownAndCancelledKeepsItsOwnOutcome()
    {
        await using var f = new Fixture();
        f.ExportStatus = JsonSerializer.SerializeToElement(new
        {
            kind = "data.export", state = "aborted",
            progress = new { done = 0, total = 0, message = "" }, result = (object?)null,
            error = "数据执行通道已中断；业务提交结果待核实。",
        });
        string abortedTask = (await f.Call("data.export.start",
            "{\"collection\":\"orders\",\"fields\":[\"title\"],\"query\":{},\"format\":\"csv\",\"suggestedName\":\"aborted.csv\"}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var aborted = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId = abortedTask }))).GetProperty("result");
        Assert.AreEqual("aborted", aborted.GetProperty("state").GetString());
        Assert.AreEqual("unknown", aborted.GetProperty("outcome").GetString());
        f.ExportStatus = JsonSerializer.SerializeToElement(new
        {
            kind = "data.export", state = "cancelled",
            progress = new { done = 1, total = 1, message = "" }, result = (object?)null, error = (string?)null,
        });
        string cancelledTask = (await f.Call("data.export.start",
            "{\"collection\":\"orders\",\"fields\":[\"title\"],\"query\":{},\"format\":\"csv\",\"suggestedName\":\"cancelled.csv\"}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var cancelled = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId = cancelledTask }))).GetProperty("result");
        Assert.AreEqual("cancelled", cancelled.GetProperty("state").GetString());
        Assert.IsFalse(cancelled.TryGetProperty("outcome", out _), "a cooperatively cancelled export is its own outcome, never an unknown commit");
    }

    [TestMethod]
    public async Task SucceededExportResultPassesThroughWithoutOutcome()
    {
        await using var f = new Fixture();
        string taskId = (await f.Call("data.export.start",
            "{\"collection\":\"orders\",\"fields\":[\"title\"],\"query\":{},\"format\":\"csv\",\"suggestedName\":\"ok.csv\"}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var done = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        var terminal = done.GetProperty("result");
        Assert.AreEqual("succeeded", terminal.GetProperty("state").GetString(), done.GetRawText());
        Assert.AreEqual(1, terminal.GetProperty("result").GetProperty("rowsWritten").GetInt32());
        Assert.IsFalse(terminal.TryGetProperty("outcome", out _));
        Assert.IsFalse(done.GetRawText().Contains("commitReceipt", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task UnknownImportCommitDoesNotReleaseTheHandedOffReservation()
    {
        await using var f = new Fixture();
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsNull(f.ReservationOutcomes.Single());
        f.Http.FailNextCommitStatus = 500;
        f.Http.FailNextCommitBody = "{\"code\":\"mutation.internal.failed\"}";
        var unknown = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_commit_unknown", unknown.GetProperty("error").GetProperty("code").GetString(), unknown.GetRawText());
        Assert.IsNull(f.ReservationOutcomes.Single(), "an uncertain business commit must not release its reservation");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"));
    }
    [TestMethod]
    public async Task PlanLimitedImportPreviewSettlesTerminalDropsItsGrantAndReclaimsItsTaskSlot()
    {
        await using var f = new Fixture();
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        var plans = new List<string>();
        for (int i = 0; i < 8; i++)
            plans.Add((await f.Call("mutation.preview", JsonSerializer.Serialize(new
            { collection = "orders", operations = new object[] { new { kind = "create", values = new { name = "new" } } } })))
                .GetProperty("result").GetProperty("planId").GetString()!);
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string grantId = f.IoGrant!; string runId = f.IoRun!;
        var limited = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("failed", limited.GetProperty("result").GetProperty("state").GetString(), limited.GetRawText());
        Assert.AreEqual("plugin_plan_limit", limited.GetProperty("result").GetProperty("error").GetProperty("code").GetString());
        Assert.IsFalse(limited.GetRawText().Contains("internal-go-token", StringComparison.Ordinal));
        // The capacity failure is a sticky terminal receipt, never a wedged retry loop.
        Assert.AreEqual(limited.GetProperty("result").GetRawText(),
            (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }))).GetProperty("result").GetRawText());
        // The minted Go import plan is dropped now instead of lingering for its TTL.
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"
            && call.GetProperty("request").GetProperty("grantId").GetString() == grantId), "plan-limit failure must drop its minted import plan");
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId, runId }), CancellationToken.None));
        for (int i = 0; i < 7; i++)
        {
            var extra = await f.Call("data.import.start", importStart);
            Assert.IsTrue(extra.TryGetProperty("result", out _), $"extra start {i}: {extra.GetRawText()}");
        }
        // A real business submit still consumes a pending plan slot.
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId = plans[0] })))
            .GetProperty("result").GetProperty("status").GetString());
        // The terminal receipt released its task slot: the eighth start evicts it, and the freed plan slot hands off again.
        string retry = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        var recovered = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId = retry }));
        Assert.IsNotNull(recovered.GetProperty("result").GetProperty("result").GetProperty("planId").GetString(), recovered.GetRawText());
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(
            new { planId = recovered.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()! })))
            .GetProperty("result").GetProperty("status").GetString());
    }

    [TestMethod]
    public async Task FailedPlanHandoffAfterSuccessfulPreviewSettlesTerminalWithCleanup()
    {
        await using var f = new Fixture(); f.Http.FailNextFreeze = true;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string grantId = f.IoGrant!; string runId = f.IoRun!;
        var failed = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("failed", failed.GetProperty("result").GetProperty("state").GetString(), failed.GetRawText());
        Assert.AreEqual("plugin_data_io_failed", failed.GetProperty("result").GetProperty("error").GetProperty("code").GetString());
        Assert.IsFalse(failed.GetRawText().Contains("import_plan_invalid", StringComparison.Ordinal));
        Assert.AreEqual(failed.GetProperty("result").GetRawText(),
            (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }))).GetProperty("result").GetRawText());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"
            && call.GetProperty("request").GetProperty("grantId").GetString() == grantId), "failed handoff must drop its minted import plan");
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId, runId }), CancellationToken.None));
    }

    [TestMethod]
    public async Task NativeTaskLimitPreStartSubmitFailureSettlesRemovedPlanResources()
    {
        await using var f = new Fixture();
        var binding = f.Tasks.CaptureNative();
        Assert.IsNotNull(binding);
        for (int i = 0; i < 64; i++)
            Assert.IsTrue(f.Tasks.AdmitTask(binding, new PluginRuntimeTaskSnapshot(
                $"block-task-{i}", $"block-run-{i}", "com.example.files", "1.0.0", "save", f.Surface.ProjectKey,
                "orders", 1, "write", "running", false, null, null, null)));
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        string grantId = f.IoGrant!; string runId = f.IoRun!;
        var rejected = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_concurrency_limit", rejected.GetProperty("error").GetProperty("code").GetString(), rejected.GetRawText());
        // The removed plan's import source is settled: reservation released, Go plan rejected, file grant revoked.
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "rejected"),
            "pre-start submit failure must settle the import plan");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.start"), "no business run may start");
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId, runId }), CancellationToken.None));
        Assert.AreEqual("plugin_plan_stale", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId })))
            .GetProperty("error").GetProperty("code").GetString());
        // The surface file slot is reusable: a fresh import still hands off a plan.
        string retry = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.IsNotNull((await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId = retry })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString());
    }

    [TestMethod]
    public async Task SessionSwitchPreStartSubmitFailureSettlesRemovedPlanResources()
    {
        await using var f = new Fixture();
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        f.Authority.Transition(null);
        var stale = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_session_stale", stale.GetProperty("error").GetProperty("code").GetString(), stale.GetRawText());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "rejected"),
            "session switch after plan removal must settle the import plan");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.start"), "no business run may start");
        Assert.AreEqual("plugin_plan_stale", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId })))
            .GetProperty("error").GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task OneShotTaskNotificationFailureConvergesNativeTaskAndSettlesPlan()
    {
        await using var f = new Fixture(); f.FailNextTaskNotification = true;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        var reply = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_capability_invalid", reply.GetProperty("error").GetProperty("code").GetString(), reply.GetRawText());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "rejected"),
            "notification failure after registration must still settle the import plan");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.start"), "no business run may start");
        // The admitted task converged to a terminal state; it cannot hold an active global task slot.
        Assert.IsNotNull(f.LastTaskId);
        Assert.IsTrue(f.Tasks.TryGetTask(f.LastTaskId!, out var snapshot));
        Assert.AreEqual("failed", snapshot.State);
        Assert.AreEqual("plugin_plan_stale", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId })))
            .GetProperty("error").GetProperty("code").GetString());
        // The freed slot keeps ordinary submits working; no business replay of the failed plan.
        string plain = (await f.Call("mutation.preview", JsonSerializer.Serialize(new
        { collection = "orders", operations = new object[] { new { kind = "create", values = new { name = "new" } } } })))
            .GetProperty("result").GetProperty("planId").GetString()!;
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId = plain })))
            .GetProperty("result").GetProperty("status").GetString());
    }

    [TestMethod]
    public async Task PersistentTaskNotificationFailureStillSettlesPlanAndConvergesTask()
    {
        await using var f = new Fixture(); f.FailTaskNotifications = true;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        var reply = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_capability_invalid", reply.GetProperty("error").GetProperty("code").GetString(), reply.GetRawText());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "rejected"),
            "persistent notification failure must not mask plan cleanup");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.start"), "no business run may start");
        // FinishNative converged the task before its own (failing) terminal notification.
        Assert.IsNotNull(f.LastTaskId);
        Assert.IsTrue(f.Tasks.TryGetTask(f.LastTaskId!, out var snapshot));
        Assert.AreEqual("failed", snapshot.State);
    }

    [TestMethod]
    public async Task SessionSwitchPreStartSubmitFailureDropsStagedAttachments()
    {
        await using var f = new Fixture();
        const string parameters = "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\",\"expectedDigest\":\"guard\",\"removeAttachmentIds\":[],\"addFile\":true}";
        var preview = await f.Call("attachments.previewChange", parameters);
        Assert.IsTrue(preview.TryGetProperty("result", out _), preview.GetRawText());
        string planId = preview.GetProperty("result").GetProperty("planId").GetString()!;
        string handle = f.Http.Calls.Single(call => call.GetProperty("method").GetString() == "attachment.stage")
            .GetProperty("request").GetProperty("handle").GetString()!;
        f.Authority.Transition(null);
        var stale = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("plugin_session_stale", stale.GetProperty("error").GetProperty("code").GetString(), stale.GetRawText());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "attachment.drop"
            && call.GetProperty("request").GetProperty("handles")[0].GetString() == handle),
            "pre-start submit failure must drop staged attachments");
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.start"), "no business run may start");
        Assert.AreEqual("plugin_plan_stale", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId })))
            .GetProperty("error").GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task RetiredBrokerStillSettlesCapturedGenerationGoImportClaim()
    {
        await using var f = new Fixture();
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        // The host broker retires (workspace switch) after the preview handed off its plan.
        f.Broker.Retire();
        var submitted = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("succeeded", submitted.GetProperty("result").GetProperty("status").GetString(), submitted.GetRawText());
        // The Go claim still settles through its captured generation even though the
        // host reservation settlement failed; verified success stays sticky.
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
            && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
            && call.GetProperty("request").GetProperty("outcome").GetString() == "committed"),
            "broker retirement must not skip the Go import settlement");
    }

    [TestMethod]
    public async Task DeletedRecordAfterConfirmationSurfacesExactRejectionNotUnknown()
    {
        await using var f = new Fixture();
        async Task<string> CreatePlan() => (await f.Call("mutation.preview", JsonSerializer.Serialize(new
        { collection = "orders", operations = new object[] { new { kind = "create", values = new { name = "new" } } } })))
            .GetProperty("result").GetProperty("planId").GetString()!;
        string raced = await CreatePlan();
        f.Http.FailNextCommitStatus = 409;
        f.Http.FailNextCommitBody = "{\"code\":\"mutation.record.not_found\",\"commitOutcome\":\"not-committed\"}";
        var rejected = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId = raced }));
        Assert.AreEqual("mutation.record.not_found", rejected.GetProperty("error").GetProperty("code").GetString(), rejected.GetRawText());
        // A failure without a definite not-committed proof keeps the true unknown boundary.
        string unknown = await CreatePlan();
        f.Http.FailNextCommitStatus = 500;
        f.Http.FailNextCommitBody = "{\"code\":\"mutation.internal.failed\"}";
        var unverifiable = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId = unknown }));
        Assert.AreEqual("plugin_commit_unknown", unverifiable.GetProperty("error").GetProperty("code").GetString(), unverifiable.GetRawText());
    }

    [TestMethod]
    public async Task BrokerSettleBudgetExhaustionStillLeavesGoClaimFreshBudget()
    {
        await using var f = new Fixture();
        // One real broker whose final native commit blocks while holding its gate; the
        // gate is released by the semantic signal of the Go settlement attempt (or a
        // bounded watchdog), never by a fixed timer window.
        var gateReleased = new ManualResetEventSlim(false);
        var settleAttempted = new ManualResetEventSlim(false);
        f.Http.OnSettleAttempt = () => { settleAttempted.Set(); gateReleased.Set(); };
        f.ReplaceBroker(new HostSessionFileBroker(
            () => new WorkspaceRequestEpochLease(new WorkspaceWireScope
            { Scope = "workspace", WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"), SessionEpoch = 7, OperationId = Guid.NewGuid(), Sequence = 1 }, CancellationToken.None, () => { }),
            action =>
            {
                action();
                if (File.ReadAllText(f.Target) == "new") gateReleased.Wait(TimeSpan.FromSeconds(30));
            }));
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        string grantId = f.IoGrant!; string runId = f.IoRun!;
        string write = (await f.Call("file.pickWrite", "{\"suggestedName\":\"out.txt\",\"mediaType\":\"text/plain\"}")).GetProperty("result").GetProperty("grantId").GetString()!;
        // The write runs on its own thread: its synchronous native commit may block
        // inside commitCurrent, which must never stall the driving test thread.
        var saving = Task.Run(() => f.Call("file.write", JsonSerializer.Serialize(new { grantId = write, offset = 0, base64 = "bmV3", finish = true })));
        bool held = false;
        for (int i = 0; i < 500 && !held; i++)
        {
            try { held = File.ReadAllText(f.Target) == "new"; } catch (IOException) { }
            if (!held) await Task.Delay(10);
        }
        Assert.IsTrue(held, "native commit must hold the broker gate");
        try
        {
            var submitted = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
            Assert.AreEqual("succeeded", submitted.GetProperty("result").GetProperty("status").GetString(), submitted.GetRawText());
            // The reservation settlement consumed its whole budget on the held gate; the
            // Go claim must still settle with a fresh, live budget. Success stays sticky.
            Assert.IsTrue(settleAttempted.Wait(TimeSpan.FromSeconds(15)), "the Go settlement attempt never happened");
            Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.settle"
                && call.GetProperty("request").GetProperty("token").GetString() == "internal-go-token"
                && call.GetProperty("request").GetProperty("outcome").GetString() == "committed"),
                "budget exhaustion at the broker must not cancel the Go settlement");
            await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
                JsonSerializer.SerializeToElement(new { grantId, runId }), CancellationToken.None));
        }
        finally
        {
            gateReleased.Set();
            await saving;
        }
    }

    [TestMethod]
    [DataRow(1024 * 1024, 0)]
    [DataRow(800 * 1024, 251)]
    [DataRow(1024 * 1024, 251)]
    [DataRow(800 * 1024, -1)]
    [DataRow(1024 * 1024, -1)]
    public async Task AttachmentStagePreservesBinaryFileWithinItsHostOnlyEncodedBudget(int byteCount, int pattern)
    {
        await using var f = new Fixture();
        byte[] bytes = new byte[byteCount];
        for (int index = 0; index < bytes.Length; index++) bytes[index] = pattern < 0 ? (byte)(index % 256) : (byte)pattern;
        File.WriteAllBytes(f.Source, bytes);
        const string parameters = "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\",\"expectedDigest\":\"guard\",\"removeAttachmentIds\":[],\"addFile\":true}";
        var preview = await f.Call("attachments.previewChange", parameters);
        Assert.IsTrue(preview.TryGetProperty("result", out _), preview.GetRawText());
        byte[] fullWire = f.Http.AttachmentStageWires.Single();
        Assert.AreEqual("application/json", f.Http.AttachmentStageMediaTypes.Single());
        using var sent = JsonDocument.Parse(fullWire);
        string base64 = sent.RootElement.GetProperty("request").GetProperty("base64").GetString()!;
        CollectionAssert.AreEqual(bytes, Convert.FromBase64String(base64));
        if (pattern != 0) Assert.IsTrue(base64.Contains('+', StringComparison.Ordinal));
        Assert.IsFalse(Encoding.UTF8.GetString(fullWire).Contains("\\u002B", StringComparison.Ordinal));
        int wireBytes = fullWire.Length;
        Console.WriteLine($"decodedBytes={bytes.Length}, pattern={pattern}, completeHttpBytes={wireBytes}");
        DirectoryInfo? directory = new(AppContext.BaseDirectory);
        while (directory is not null && !File.Exists(Path.Combine(directory.FullName, "contracts", "plugin-surface-api.v1.json"))) directory = directory.Parent;
        Assert.IsNotNull(directory);
        using var contract = JsonDocument.Parse(File.ReadAllText(Path.Combine(directory.FullName, "contracts", "plugin-surface-api.v1.json")));
        int encodedLimit = contract.RootElement.GetProperty("limits").GetProperty("attachmentStageRequestBytes").GetInt32();
        Assert.AreEqual(PluginSurfaceApi.AttachmentStageRequestBytes, encodedLimit);
        Assert.IsTrue(wireBytes > 1024 * 1024 && wireBytes <= encodedLimit);
        File.WriteAllBytes(f.Source, new byte[1024 * 1024 + 1]);
        Assert.IsTrue((await f.Call("attachments.previewChange", parameters)).TryGetProperty("error", out _));
        Assert.AreEqual(1, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "attachment.stage"));
    }

    [TestMethod]
    public async Task OrdinarySurfaceHttpKeepsItsExistingJsonEncoder()
    {
        await using var f = new Fixture();
        const string value = "+<>&";
        var response = await f.Call("storage.set", JsonSerializer.Serialize(new { key = "ordinary", value }));
        Assert.IsTrue(response.TryGetProperty("result", out _));
        var call = f.Http.Calls.Single();
        Assert.AreEqual(value, call.GetProperty("request").GetProperty("value").GetString());
        Assert.IsTrue(call.GetRawText().Contains("\\u002B", StringComparison.Ordinal));
    }

    [TestMethod]
    [DataRow("file.read", 251)]
    [DataRow("file.read", -1)]
    [DataRow("file.read", -2)]
    [DataRow("attachments.read", 251)]
    [DataRow("attachments.read", -1)]
    [DataRow("attachments.read", -2)]
    public async Task BinaryReadsPreserveMaximumBlocksThroughCompleteHandleReply(string method, int pattern)
    {
        await using var f = new Fixture();
        byte[] content = new byte[256 * 1024];
        for (int index = 0; index < content.Length; index++)
            content[index] = pattern switch { -1 => (byte)(index % 256), -2 => (index % 3) switch { 0 => 251, 1 => 239, _ => 190 }, _ => (byte)pattern };
        string parameters;
        string handle;
        if (method == "file.read")
        {
            File.WriteAllBytes(f.Source, content);
            handle = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
            parameters = JsonSerializer.Serialize(new { grantId = handle });
        }
        else
        {
            f.Http.AttachmentReadContent = content;
            handle = (await f.Call("attachments.list", "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\"}")).GetProperty("result").GetProperty("items")[0].GetProperty("attachmentId").GetString()!;
            parameters = JsonSerializer.Serialize(new { attachmentId = handle, offset = 0 });
        }
        string wire = await f.CallRaw(method, parameters);
        using var reply = JsonDocument.Parse(wire);
        Assert.IsTrue(reply.RootElement.TryGetProperty("result", out var result), wire);
        CollectionAssert.AreEqual(content, Convert.FromBase64String(result.GetProperty("base64").GetString()!));
        int bytes = Encoding.UTF8.GetByteCount(wire);
        Assert.IsTrue(bytes <= 1024 * 1024);
        Console.WriteLine($"method={method}, pattern={pattern}, decodedBytes={content.Length}, completeFrameBytes={bytes}");
        if (method == "file.read")
        {
            Assert.IsTrue((await f.Call(method, parameters)).GetProperty("result").GetProperty("eof").GetBoolean());
            await f.Call("file.release", JsonSerializer.Serialize(new { grantId = handle }));
            Assert.IsTrue((await f.Call(method, parameters)).TryGetProperty("error", out _));
        }
        else
        {
            Assert.IsTrue(result.GetProperty("eof").GetBoolean());
            Assert.IsTrue((await f.Call(method, JsonSerializer.Serialize(new { attachmentId = handle, offset = -1 }))).TryGetProperty("error", out _));
            f.Surfaces.CloseAll();
            Assert.IsTrue((await f.Call(method, parameters)).TryGetProperty("error", out _));
        }
    }

    [TestMethod]
    public async Task BinaryReplyKeepsDefaultEscapingForEnvelopeStrings()
    {
        await using var f = new Fixture();
        string grant = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
        const string requestId = "<script>+&";
        string request = JsonSerializer.Serialize(new { contract = PluginSurfaceApi.Contract, surfaceToken = f.Surface.SurfaceToken, requestId, method = "file.read", @params = new { grantId = grant } });
        string wire = await f.Api.HandleAsync(f.Surface.SurfaceToken, f.Surface.DocumentUri.AbsoluteUri, request);
        using var reply = JsonDocument.Parse(wire);
        Assert.IsTrue(reply.RootElement.TryGetProperty("result", out _), wire);
        Assert.AreEqual(requestId, reply.RootElement.GetProperty("requestId").GetString());
        Assert.IsFalse(wire.Contains("<script>", StringComparison.Ordinal));
        Assert.IsTrue(wire.Contains("\\u003C", StringComparison.Ordinal));
        Assert.IsTrue(wire.Contains("\\u002B", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task AttachmentsReplaceInternalNamesAndTicketsWithSurfaceHandles()
    {
        await using var f = new Fixture();
        var listed = await f.Call("attachments.list", "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\"}");
        Assert.IsFalse(listed.GetRawText().Contains("internal-stored-name", StringComparison.Ordinal));
        string id = listed.GetProperty("result").GetProperty("items")[0].GetProperty("attachmentId").GetString()!;
        var read = await f.Call("attachments.read", JsonSerializer.Serialize(new { attachmentId = id, offset = 0 }));
        Assert.AreEqual("YWJj", read.GetProperty("result").GetProperty("base64").GetString());
        Assert.IsTrue((await f.Call("attachments.read", "{\"attachmentId\":\"foreign-handle\",\"offset\":0}")).TryGetProperty("error", out _));
        Assert.IsTrue((await f.Call("attachments.previewChange", JsonSerializer.Serialize(new { collection = "orders", recordId = "other", fieldId = "files", expectedDigest = "guard", removeAttachmentIds = new[] { id }, addFile = false }))).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task VerifiedNativeSaveRemainsSuccessfulWhenTheSurfaceClosesImmediatelyAfterReplace()
    {
        await using var f = new Fixture();
        string id = (await f.Call("file.pickWrite", "{\"suggestedName\":\"out.txt\",\"mediaType\":\"text/plain\"}")).GetProperty("result").GetProperty("grantId").GetString()!;
        f.AfterNativeCommit = () => f.Surfaces.CloseAll();
        var reply = await f.Call("file.write", JsonSerializer.Serialize(new { grantId = id, offset = 0, base64 = "bmV3", finish = true }));
        Assert.IsTrue(reply.GetProperty("result").GetProperty("committed").GetBoolean());
        Assert.AreEqual("new", File.ReadAllText(f.Target)); Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task FileCapacityReleasesAcrossRepeatedMountsAndAttachmentListsReuseOwnedHandles()
    {
        await using var f = new Fixture();
        var ids = new List<string>();
        for (int i = 0; i < 8; i++) ids.Add((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!);
        Assert.IsTrue((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).TryGetProperty("error", out _));
        foreach (string id in ids) await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        for (int i = 0; i < 16; i++)
        {
            string id = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
            await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        }
        string? prior = null;
        for (int i = 0; i < 70; i++)
        {
            var listed = await f.Call("attachments.list", "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\"}");
            string current = listed.GetProperty("result").GetProperty("items")[0].GetProperty("attachmentId").GetString()!;
            if (prior is not null) Assert.AreEqual(prior, current); prior = current;
        }
    }

    [TestMethod]
    public async Task RepeatedOwnedIoCancelCoalescesAndRevokesItsCapturedFileGrant()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}")).GetProperty("result").GetProperty("taskId").GetString()!;
        await Task.WhenAll(f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })), f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })));
        Assert.AreEqual(1, f.IoCancels);
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None));
        Assert.IsTrue((await f.Call("data.io.cancel", "{\"taskId\":\"foreign-task\"}")).TryGetProperty("error", out _));
        Assert.AreEqual(1, f.IoCancels);
    }

    [TestMethod]
    public async Task TimedOutIoCancelAllowsAnExplicitRetryWhileItsCleanupIsStillPending()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        f.IoCancelHold = new(TaskCreationOptions.RunContinuationsAsynchronously);
        var cleanupEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var cleanupHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoWaitTerminal = async token =>
        {
            cleanupEntered.TrySetResult();
            await cleanupHold.Task.WaitAsync(token);
        };
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        string parameters = JsonSerializer.Serialize(new { taskId });
        var first = f.CallWithId("data.io.cancel", parameters, "timeout-first");
        var sharedFirst = f.CallWithId("data.io.cancel", parameters, "timeout-shared");
        Assert.AreEqual(1, f.IoCancels);
        // Use the actual 5s Port.Cancel budget: no worker terminal is fabricated on timeout.
        foreach (var reply in await Task.WhenAll(first, sharedFirst).WaitAsync(TimeSpan.FromSeconds(10)))
            Assert.AreEqual("plugin_timeout", reply.GetProperty("error").GetProperty("code").GetString(), reply.GetRawText());
        await cleanupEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Task oldCleanup = ((System.Collections.IDictionary)typeof(PluginSurfaceApi)
            .GetField("_cleanups", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(f.Api)!).Values.Cast<Task>().Single();
        Assert.AreEqual("running", (await f.Call("data.io.status", parameters)).GetProperty("result").GetProperty("state").GetString());
        Assert.AreEqual(1, f.IoCancels, "a failed attempt must not retry itself");

        var retryHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = retryHold; f.IoWaitTerminal = null;
        var retry = f.CallWithId("data.io.cancel", parameters, "retry-first");
        var sharedRetry = f.CallWithId("data.io.cancel", parameters, "retry-shared");
        try
        {
            Assert.AreEqual(2, f.IoCancels, "a new explicit cancel must reach Port.Cancel after the failed attempt");
            Assert.IsFalse(retry.IsCompleted || sharedRetry.IsCompleted, "the new attempt must await its own acknowledgement");
            // A late failure from the old attempt must neither finish nor replace the new ack.
            cleanupHold.TrySetException(new InvalidOperationException("old cleanup failed"));
            await oldCleanup.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.IsFalse(retry.IsCompleted || sharedRetry.IsCompleted);
            retryHold.SetResult();
            foreach (var reply in await Task.WhenAll(retry, sharedRetry))
                Assert.IsTrue(reply.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), reply.GetRawText());
            Assert.IsTrue((await f.Call("data.io.cancel", parameters)).GetProperty("result").GetProperty("cancelRequested").GetBoolean());
            Assert.AreEqual(2, f.IoCancels, "the successful acknowledgement must stay sticky");
            Assert.AreEqual("running", (await f.Call("data.io.status", parameters)).GetProperty("result").GetProperty("state").GetString());
        }
        finally { cleanupHold.TrySetResult(); retryHold.TrySetResult(); }
    }

    [TestMethod]
    public async Task RetirementDoesNotRetryAFailedIoCancellation()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        f.IoCancelHold = new(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold.SetException(new InvalidOperationException("port cancellation failed"));
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.IsTrue((await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }))).TryGetProperty("error", out _));
        Assert.AreEqual(1, f.IoCancels);
        f.Surfaces.CloseAll();
        Assert.AreEqual(1, f.IoCancels, "retirement must not automatically retry a failed acknowledgement");
    }

    [TestMethod]
    public async Task RawFrameIoCancelAdmissionIsBoundedIndependentAndReleasesSlots()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var hold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = hold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        string secondTaskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        // A client bypassing the SDK parks eight raw-frame cancels with distinct requestIds
        // on the same task's CancelAck; each synchronous call is admitted before it parks.
        var parked = Enumerable.Range(0, 8).Select(index =>
            f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), $"raw-cancel-{index}")).ToArray();
        // A duplicate in-flight requestId and a ninth distinct id (even for a different task)
        // are both rejected fast and stably instead of parking another 15s handler.
        var duplicate = await f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), "raw-cancel-0");
        Assert.AreEqual("plugin_request_duplicate", duplicate.GetProperty("error").GetProperty("code").GetString(), duplicate.GetRawText());
        var ninth = await f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId = secondTaskId }), "raw-cancel-8");
        Assert.AreEqual("plugin_concurrency_limit", ninth.GetProperty("error").GetProperty("code").GetString(), ninth.GetRawText());
        Assert.AreEqual(1, f.IoCancels, "same-task cancels must coalesce into one port cancellation");
        // Ordinary pending admission is unaffected while eight cancel handlers are
        // parked; the full-ordinary-budget converse is proven by FullOrdinaryPendingBudgetStillAdmitsCancels.
        for (int i = 0; i < 8; i++)
        {
            var ordinary = await f.Call("storage.set", JsonSerializer.Serialize(new { key = $"k{i}", value = "v" }));
            Assert.IsTrue(ordinary.TryGetProperty("result", out _), ordinary.GetRawText());
        }
        // Settling the ack returns every slot; all parked cancels complete with a receipt.
        hold.SetResult();
        foreach (var reply in await Task.WhenAll(parked))
            Assert.AreEqual(true, reply.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), reply.GetRawText());
        // The freed admission accepts a fresh cancel immediately (coalesced terminal ack).
        var readmitted = await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual(true, readmitted.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), readmitted.GetRawText());
    }

    [TestMethod]
    public async Task FullOrdinaryPendingBudgetStillAdmitsCancels()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var hold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.Http.HoldStorageSet = hold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        // Settle the task's CancelAck first so the cancel below cannot race port cleanup.
        Assert.IsTrue((await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        // Park eight ordinary requests. HTTP delivery may start asynchronously, so judge the
        // ordinary budget as full only on the peer's own entry signal, not on task creation.
        var parked = Enumerable.Range(0, 8).Select(index =>
            f.CallWithId("storage.set", JsonSerializer.Serialize(new { key = $"k{index}", value = "v" }), $"ordinary-{index}")).ToArray();
        bool full = false;
        for (int i = 0; i < 500 && !(full = f.Http.StorageSetHeld == 8); i++) await Task.Delay(10);
        Assert.IsTrue(full, "eight ordinary requests must be genuinely in flight");
        var overflow = await f.CallWithId("storage.set", JsonSerializer.Serialize(new { key = "overflow", value = "v" }), "ordinary-overflow");
        Assert.AreEqual("plugin_concurrency_limit", overflow.GetProperty("error").GetProperty("code").GetString(), overflow.GetRawText());
        // The completely full ordinary budget must not block cancellation.
        var cancel = await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        Assert.IsTrue(cancel.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), cancel.GetRawText());
        hold.SetResult();
        foreach (var reply in await Task.WhenAll(parked))
            Assert.IsTrue(reply.TryGetProperty("result", out _), reply.GetRawText());
    }

    [TestMethod]
    public async Task FailedIoCancelHandlerReturnsItsAdmissionSlot()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var hold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = hold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        // An unknown task fails inside the handler after admission consumed a slot.
        var foreign = await f.Call("data.io.cancel", "{\"taskId\":\"foreign-task\"}");
        Assert.AreEqual("plugin_source_denied", foreign.GetProperty("error").GetProperty("code").GetString(), foreign.GetRawText());
        // A slot leaked by that failure would leave room for at most seven of these eight;
        // any rejection surfaces as an error reply instead of a cancelRequested receipt.
        var parked = Enumerable.Range(0, 8).Select(index =>
            f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), $"after-failure-{index}")).ToArray();
        hold.SetResult();
        foreach (var reply in await Task.WhenAll(parked))
            Assert.IsTrue(reply.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), reply.GetRawText());
    }

    [TestMethod]
    public async Task RetiredSurfacePromptlyUnwindsParkedCancels()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var hold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = hold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        var parked = Enumerable.Range(0, 8).Select(index =>
            f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), $"retire-{index}")).ToArray();
        Assert.AreEqual(1, f.IoCancels, "the eight parked cancels must have coalesced onto one port cancellation");
        // Retirement fires the surface lifetime the cancel deadline is linked to, so the
        // whole parked budget unwinds through its finally instead of waiting out 15s.
        f.Surfaces.CloseAll();
        var replies = await Task.WhenAll(parked).WaitAsync(TimeSpan.FromSeconds(5));
        foreach (var reply in replies)
            Assert.AreEqual("plugin_timeout", reply.GetProperty("error").GetProperty("code").GetString(), reply.GetRawText());
        hold.SetResult();
    }

    [TestMethod]
    public async Task CancelOnFinishedWorkerSettlesPreviewHandoffWithoutStatus()
    {
        await using var f = new Fixture();
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.AreEqual(true, (await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.freeze"),
            "cancel on a finished worker must run the Gate-protected settlement now");
        var settled = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("succeeded", settled.GetProperty("result").GetProperty("state").GetString());
        string planId = settled.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsNull(f.ReservationOutcomes.Single(), "a cancel-driven handoff must retain the reservation for submit");
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }))).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"),
            "ownership moved to the plan; the handed-off grant must not be dropped");
    }

    [TestMethod]
    public async Task ConcurrentStatusAndCancelOnFinishedWorkerShareOneHandoff()
    {
        await using var f = new Fixture();
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var replies = await Task.WhenAll(
            f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })),
            f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })));
        Assert.AreEqual("succeeded", replies[0].GetProperty("result").GetProperty("state").GetString());
        Assert.AreEqual(true, replies[1].GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.AreEqual(1, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "import.freeze"),
            "status and cancel must share one Gate-protected handoff, never a duplicate");
        string planId = replies[0].GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsNull(f.ReservationOutcomes.Single());
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }))).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"));
    }

    [TestMethod]
    public async Task CancelOnlyFinishedIoTasksReleaseTheirConcurrencySlots()
    {
        await using var f = new Fixture(); f.IoPreviewErrors = true;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        for (int i = 0; i < 9; i++)
        {
            string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
            var cancel = await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
            Assert.AreEqual(true, cancel.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), $"iteration {i}");
        }
        Assert.AreEqual(9, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "import.drop"),
            "each cancel-only finished preview must settle and drop its minted grant");
    }

    [TestMethod]
    public async Task LateCancelCleanupKeepsTheHandedOffImportPlanAlive()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var hold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = hold;
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var parked = f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), "cancel-first");
        // The worker finishes while the first cancel is still parked inside the port call.
        f.SucceedIo();
        Assert.AreEqual(true, (await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.AreEqual(1, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "import.freeze"));
        hold.SetResult();
        Assert.AreEqual(true, (await parked).GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"),
            "late cancellation cleanup must not revoke or drop a handed-off plan");
        Assert.IsNull(f.ReservationOutcomes.Single());
        string planId = (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }))).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
    }

    [TestMethod]
    public async Task CancelSettlementFailureReleasesGrantAndKeepsStableTerminal()
    {
        await using var f = new Fixture(); f.Http.FailNextFreeze = true;
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.AreEqual(true, (await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None),
            "the failed settlement must revoke the captured file grant");
        var settled = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("failed", settled.GetProperty("result").GetProperty("state").GetString());
        Assert.AreEqual("plugin_data_io_failed", settled.GetProperty("result").GetProperty("error").GetProperty("code").GetString());
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"));
        string next = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.IsFalse(string.IsNullOrEmpty(next), "the settled task must not wedge the concurrency budget");
    }

    [TestMethod]
    public async Task SurfaceRetirementDuringCancelSettlementStillReleasesTheFileGrant()
    {
        await using var f = new Fixture();
        f.Http.OnFreeze = () => f.Surfaces.CloseAll();
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}"))
            .GetProperty("result").GetProperty("taskId").GetString()!;
        var cancel = await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        Assert.IsTrue(cancel.TryGetProperty("result", out _), cancel.GetRawText());
        bool denied = false;
        try { await f.Broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None); }
        catch { denied = true; }
        Assert.IsTrue(denied, "retirement during the settlement must still revoke or deny the captured file grant");
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"));
    }

    [TestMethod]
    public async Task CancelReturningDuringHandoffKeepsTheHandedOffPlanAlive()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var portHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var freezeHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = portHold; f.Http.HoldFreeze = freezeHold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        var parkedCancel = f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), "cancel-first");
        // The worker finishes while the first cancel is still parked inside the port call.
        f.SucceedIo();
        var settling = f.CallWithId("data.io.status", JsonSerializer.Serialize(new { taskId }), "status-during");
        bool freezeParked = false;
        for (int i = 0; i < 500 && !(freezeParked = f.Http.FreezeHeld == 1); i++) await Task.Delay(10);
        Assert.IsTrue(freezeParked, "the settlement must be parked inside the handoff");
        // The port cancel now returns while the handoff is still in flight; its cleanup must
        // wait for the Gate instead of revoking the grant the handoff is about to own.
        portHold.SetResult();
        bool cancelFinishedFirst = await Task.WhenAny(parkedCancel, Task.Delay(TimeSpan.FromSeconds(3))) == parkedCancel;
        Assert.IsFalse(cancelFinishedFirst,
            "cancellation cleanup must not settle ownership while a handoff holds the Gate");
        freezeHold.SetResult();
        var status = await settling;
        Assert.AreEqual("succeeded", status.GetProperty("result").GetProperty("state").GetString(), status.GetRawText());
        string planId = status.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        var firstReply = await parkedCancel;
        Assert.AreEqual(true, firstReply.GetProperty("result").GetProperty("cancelRequested").GetBoolean(), firstReply.GetRawText());
        Assert.AreEqual(1, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "import.freeze"));
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"),
            "the late cancellation cleanup must not drop the handed-off plan");
        Assert.IsNull(f.ReservationOutcomes.Single());
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }))).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
    }

    [TestMethod]
    public async Task CancelRevokedGrantCannotBeHandedOffByALaterSettlement()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var freezeHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.Http.HoldFreeze = freezeHold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        // The ack is ordered after the Gate-protected revoke, so this reply proves the grant is gone.
        Assert.AreEqual(true, (await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })))
            .GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        f.SucceedIo();
        var settling = f.CallWithId("data.io.status", JsonSerializer.Serialize(new { taskId }), "status-after");
        bool freezeParked = false;
        for (int i = 0; i < 500 && !(freezeParked = f.Http.FreezeHeld == 1); i++) await Task.Delay(10);
        Assert.IsTrue(freezeParked, "the later settlement must still attempt the handoff under the Gate");
        freezeHold.SetResult();
        var status = await settling;
        Assert.AreEqual("failed", status.GetProperty("result").GetProperty("state").GetString(), status.GetRawText());
        Assert.AreEqual("plugin_data_io_failed", status.GetProperty("result").GetProperty("error").GetProperty("code").GetString());
        Assert.IsEmpty(f.ReservationOutcomes, "no reservation may be minted from a revoked grant");
        Assert.IsTrue(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"),
            "the dead grant's Go claim must be dropped");
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead",
            JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None));
    }

    [TestMethod]
    public async Task CancelAckFailsPromptlyWhenSettlementHoldsTheGatePastItsBudget()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        var portHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var freezeHold = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.IoCancelHold = portHold; f.Http.HoldFreeze = freezeHold;
        const string importStart = "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}";
        string taskId = (await f.Call("data.import.start", importStart)).GetProperty("result").GetProperty("taskId").GetString()!;
        var parkedCancel = f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), "cancel-budget");
        f.SucceedIo();
        var settling = f.CallWithId("data.io.status", JsonSerializer.Serialize(new { taskId }), "status-budget");
        bool freezeParked = false;
        for (int i = 0; i < 500 && !(freezeParked = f.Http.FreezeHeld == 1); i++) await Task.Delay(10);
        Assert.IsTrue(freezeParked, "the settlement must legitimately hold the Gate at freeze");
        // The port cancel returns with most of its 5s budget left; the Gate wait then
        // consumes the rest. The acknowledgement must fail promptly (well before the
        // 15s request deadline an unbudgeted Gate wait would run into).
        portHold.SetResult();
        var cancelReply = await parkedCancel.WaitAsync(TimeSpan.FromSeconds(10));
        Assert.AreEqual("plugin_timeout", cancelReply.GetProperty("error").GetProperty("code").GetString(), cancelReply.GetRawText());
        // A new explicit cancel now observes the finished worker and joins settlement,
        // while the first attempt still owes its Gate-protected cleanup decision.
        var repeatedCancel = f.CallWithId("data.io.cancel", JsonSerializer.Serialize(new { taskId }), "cancel-after-budget");
        Assert.IsFalse(repeatedCancel.IsCompleted, "the repeated cancel must wait for the handoff");
        // The owed Gate-protected decision still completes afterwards: the settlement's
        // handoff wins and the late cancellation must neither revoke nor drop it.
        freezeHold.SetResult();
        var status = await settling;
        Assert.AreEqual("succeeded", status.GetProperty("result").GetProperty("state").GetString(), status.GetRawText());
        string planId = status.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsTrue((await repeatedCancel).GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.AreEqual(1, f.IoCancels, "a finished worker must settle rather than start another port cancellation");
        Assert.AreEqual(1, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "import.freeze"));
        Assert.IsNull(f.ReservationOutcomes.Single());
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "import.drop"),
            "the completed handoff must survive the timed-out cancellation");
        Assert.AreEqual("succeeded", (await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }))).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual("consumed", f.ReservationOutcomes.Single());
    }

    private sealed class Fixture : IAsyncDisposable, IWebReplySink, INativeProductFileHost
    {
        internal string Root { get; } = Path.Combine(Path.GetTempPath(), "vibetable-plugin-files-" + Guid.NewGuid().ToString("N"));
        internal string Source => Path.Combine(Root, "source.csv");
        internal string Target => Path.Combine(Root, "out.txt");
        internal PluginSurfaceSessionManager Surfaces { get; } = new();
        internal PluginSurfaceSession Surface { get; }
        internal HostSessionFileBroker Broker { get; private set; }
        internal void ReplaceBroker(HostSessionFileBroker broker) => Broker = broker;
        internal PluginSurfaceApi Api { get; }
        internal HostPluginTaskRegistry Tasks => _tasks;
        internal ProductAuthorityEpoch Authority => _authority;
        internal Peer Http { get; } = new();
        internal bool CancelPick;
        internal Action? AfterPick;
        internal Action? AfterAuthorize { set => Http.AfterAuthorize = value; }
        internal int Leases;
        internal string?[] ReservationOutcomes => ((System.Collections.IDictionary)typeof(HostSessionFileBroker)
            .GetField("_reservations", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(Broker)!).Values.Cast<object>()
            .Select(value => (string?)value.GetType().GetProperty("Outcome", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(value)).ToArray();
        internal int GrantCount => ((System.Collections.IDictionary)typeof(HostSessionFileBroker)
            .GetField("_grants", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(Broker)!).Count;
        internal Action? AfterNativeCommit;
        internal bool NativeConfirmation;
        internal bool IoRunning;
        internal bool IoPreviewErrors;
        internal int IoCancels;
        internal TaskCompletionSource? IoCancelHold;
        internal Func<CancellationToken, Task>? IoWaitTerminal;
        internal JsonElement? ExportStatus;
        internal bool FailTaskNotifications;
        internal bool FailNextTaskNotification;
        internal string? LastTaskId;
        private int IoStarts;
        internal string? IoGrant;
        internal string? IoRun;
        private readonly ProductAuthorityEpoch _authority = new();
        private readonly HostPluginTaskRegistry _tasks;
        private readonly PluginProjectContext _context = new("local:11111111111141118111111111111111", "workspace:7", 7);
        private readonly ProductSidecarGenerationSnapshot _generation;
        private JsonElement _status;
        internal Fixture()
        {
            Directory.CreateDirectory(Root); File.WriteAllText(Source, "name\nnew\n"); File.WriteAllText(Target, "old");
            _authority.Transition(_context); _tasks = new(_authority); _tasks.SetWorkspaceContext(_context);
            var surface = Surfaces.Open(_context.ProjectKey, "com.example.files", PluginPackageRevision.Create("C:/synthetic", new string('a', 64)), "ui/index.html");
            Surfaces.BindIdentity(surface.SurfaceToken, _context, 1, "3.x", "1.0.0", "save", "write", "ui/index.html", installationId: "installation");
            Surfaces.TryGet(surface.SurfaceToken, out var bound); Surface = bound!;
            _generation = new ProductSidecarGenerationSnapshot(this, 1, new PocketBaseAdminContext(new Uri("http://127.0.0.1:1/bootstrap"), new Uri("http://127.0.0.1:1/"), "X-VibeTable-Session", "synthetic"), new ProductSidecarIdentity("11111111-1111-4111-8111-111111111111", 7, 1, "claim"), [], action => action());
            Broker = new HostSessionFileBroker(() => { Interlocked.Increment(ref Leases); return new WorkspaceRequestEpochLease(new WorkspaceWireScope { Scope = "workspace", WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"), SessionEpoch = 7, OperationId = Guid.NewGuid(), Sequence = 1 }, CancellationToken.None, () => Interlocked.Decrement(ref Leases)); }, action => { action(); if (File.ReadAllText(Target) == "new") { var after = AfterNativeCommit; AfterNativeCommit = null; after?.Invoke(); } });
            Api = new(Surfaces, _tasks, () => _generation, this, Http, files: () => Broker, fileHost: this, pickFile: pick => Task.FromResult(pick()), dataIo: () => new(Broker, StartIo, _ => _status, CancelPort, (_, token) => IoWaitTerminal?.Invoke(token) ?? Task.CompletedTask));
        }
        private async Task<JsonElement> CancelPort(string id, CancellationToken token)
        {
            IoCancels++;
            if (IoCancelHold is { } hold) await hold.Task.WaitAsync(token);
            return _status;
        }
        private async Task<JsonElement> StartIo(string kind, JsonElement parameters, CancellationToken token)
        {
            string grantId = parameters.GetProperty("grantId").GetString()!; string runId = parameters.GetProperty("pluginScope").GetProperty("runId").GetString()!;
            IoGrant = grantId; IoRun = runId;
            if (kind == "data.export")
            {
                // The fake port owns the terminal the tests need to observe; a
                // template without a taskId gets a fresh host identity stamped.
                var template = ExportStatus is { } custom
                    ? JsonNode.Parse(custom.GetRawText())!.AsObject()
                    : new JsonObject
                    {
                        ["kind"] = kind,
                        ["state"] = IoRunning ? "running" : "succeeded",
                        ["progress"] = JsonSerializer.SerializeToNode(new { done = 1, total = 1, message = "written" }),
                        ["result"] = JsonSerializer.SerializeToNode(new
                        {
                            collection = "orders", format = "csv", rowsWritten = 1,
                            schemaRevision = "schema_1", capabilityHash = "schema_1", outputDisplayName = "out.txt"
                        }),
                        ["error"] = null,
                    };
                template["taskId"] = "owned-task-" + Interlocked.Increment(ref IoStarts);
                _status = JsonSerializer.SerializeToElement(template);
                return _status;
            }
            var opened = await Broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId, runId }), token);
            string transferId = opened.GetProperty("transferId").GetString()!;
            for (; ; ) { var block = await Broker.HandleAsync("read", JsonSerializer.SerializeToElement(new { transferId, maxBytes = 262144 }), token); if (block.GetProperty("eof").GetBoolean()) break; }
            await Broker.HandleAsync("closeRead", JsonSerializer.SerializeToElement(new { transferId }), token);
            _status = JsonSerializer.SerializeToElement(new { taskId = "owned-task-" + Interlocked.Increment(ref IoStarts), kind, state = IoRunning ? "running" : "succeeded", progress = new { done = 1, total = 1, message = "parsed" }, result = new { token = new { token = "internal-go-token" }, summary = new { totalRows = 1, validRows = IoPreviewErrors ? 0 : 1, errorRows = IoPreviewErrors ? 1 : 0 }, rows = Array.Empty<object>() }, error = (string?)null }); return _status;
        }
        internal void SucceedIo() => _status = JsonSerializer.SerializeToElement(new
        {
            taskId = _status.GetProperty("taskId").GetString()!, kind = "data.previewImport", state = "succeeded",
            progress = new { done = 1, total = 1, message = "parsed" },
            result = new { token = new { token = "internal-go-token" }, summary = new { totalRows = 1, validRows = 1, errorRows = 0 }, rows = Array.Empty<object>() },
            error = (string?)null
        });
        internal async Task<JsonElement> Call(string method, string parameters)
            => JsonDocument.Parse(await CallRaw(method, parameters)).RootElement.Clone();
        internal async Task<JsonElement> CallWithId(string method, string parameters, string requestId)
            => JsonDocument.Parse(await CallRawWithId(method, parameters, requestId)).RootElement.Clone();
        internal Task<string> CallRaw(string method, string parameters)
            => CallRawWithId(method, parameters, Guid.NewGuid().ToString("N"));
        internal async Task<string> CallRawWithId(string method, string parameters, string requestId)
        { var raw = JsonSerializer.Serialize(new { contract = PluginSurfaceApi.Contract, surfaceToken = Surface.SurfaceToken, requestId, method, @params = JsonDocument.Parse(parameters).RootElement }); return await Api.HandleAsync(Surface.SurfaceToken, Surface.DocumentUri.AbsoluteUri, raw); }
        public string? SelectImportSource() { AfterPick?.Invoke(); return CancelPick ? null : Source; }
        public string? SelectExportTarget(string format, string defaultName) => SelectAttachmentTarget(defaultName);
        public NativeAttachmentSelection SelectAttachmentSources(bool replacement) { AfterPick?.Invoke(); return new(CancelPick ? [] : [Source], true); }
        public string? SelectAttachmentTarget(string suggestedName) { AfterPick?.Invoke(); return CancelPick ? null : Target; }
        public string CreateAttachmentPreviewPath(string suggestedName) => throw new NotSupportedException();
        public Task PreviewAttachmentAsync(string fullPath) => throw new NotSupportedException();
        public void PostNotification(string type, object? payload)
        {
            if (type == "plugin.task.changed" && payload is PluginEventEnvelope taskEnvelope)
            {
                LastTaskId = taskEnvelope.EntityId;
                if (FailTaskNotifications || FailNextTaskNotification) { FailNextTaskNotification = false; throw new InvalidOperationException("notification sink failed"); }
            }
            if (type != "plugin.interaction.requested" || payload is not PluginEventEnvelope envelope) return;
            var interaction = envelope.Snapshot.Deserialize<PluginRuntimeInteractionSnapshot>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
            NativeConfirmation = _tasks.RequiresNativeConfirmation(interaction.RunId);
            _tasks.BeginInteractionResolve(new(interaction.RunId, interaction.PendingConfirmation!.InteractionId, "approved"), fromNativeHost: true);
        }
        public void PostResponse(string type, string? id, object? payload) { }
        public void PostOperationFailed(string? id, string message, string? code = null, string? operation = null, string? operationId = null) { }
        public async ValueTask DisposeAsync() { Api.Dispose(); await Api.DrainAsync(); Broker.Retire(); await Broker.DrainCompletion; _authority.Dispose(); Directory.Delete(Root, true); }
    }
    private sealed class Peer : HttpMessageHandler
    {
        internal List<JsonElement> Calls { get; } = [];
        internal bool DatasetStale;
        internal string DatasetResultBody = PluginSurfaceApiTests.DatasetBody;
        internal Action? AfterAuthorize;
        internal Action? OnSettleAttempt;
        internal bool FailNextFreeze;
        internal Action? OnFreeze;
        internal TaskCompletionSource? HoldFreeze;
        private int _freezeHeld;
        internal int FreezeHeld => _freezeHeld;
        internal TaskCompletionSource? HoldStorageSet;
        private int _storageSetHeld;
        internal int StorageSetHeld => _storageSetHeld;
        internal int FailNextCommitStatus;
        internal string? FailNextCommitBody;
        internal byte[]? AttachmentReadContent;
        internal List<byte[]> AttachmentStageWires { get; } = [];
        internal List<string?> AttachmentStageMediaTypes { get; } = [];
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            byte[] bodyBytes = await request.Content!.ReadAsByteArrayAsync(token);
            // A settle request that arrives with an already-cancelled token never
            // performs its wire round trip; only a live-budget settle is recorded.
            // The attempt itself is observable regardless of the token state.
            using (var peek = JsonDocument.Parse(bodyBytes))
            {
                if (peek.RootElement.TryGetProperty("method", out var requested) && requested.GetString() == "import.settle")
                {
                    OnSettleAttempt?.Invoke();
                    token.ThrowIfCancellationRequested();
                }
            }
            var call = JsonDocument.Parse(bodyBytes).RootElement.Clone(); Calls.Add(call); string method = call.GetProperty("method").GetString()!;
            if (method == "dataset.validate" && DatasetStale) return new(HttpStatusCode.Conflict) { Content = new StringContent("""{"code":"plugin_dataset_stale"}""") };
            if (method == "storage.set" && HoldStorageSet is { } parked) { Interlocked.Increment(ref _storageSetHeld); await parked.Task.WaitAsync(token); }
            if (method == "attachment.stage") { AttachmentStageWires.Add(bodyBytes); AttachmentStageMediaTypes.Add(request.Content.Headers.ContentType?.MediaType); }
            if (method == "file.authorize") { var after = AfterAuthorize; AfterAuthorize = null; after?.Invoke(); }
            if (method == "import.freeze" && HoldFreeze is { } heldFreeze) { Interlocked.Increment(ref _freezeHeld); await heldFreeze.Task.WaitAsync(token); }
            if (method == "import.freeze") { var freeze = OnFreeze; OnFreeze = null; freeze?.Invoke(); }
            if (method == "import.freeze" && FailNextFreeze) { FailNextFreeze = false; return new(HttpStatusCode.Conflict) { Content = new StringContent("{\"code\":\"import_plan_invalid\"}") }; }
            if (method == "mutation.commit" && FailNextCommitBody is { } commitBody)
            { int status = FailNextCommitStatus; FailNextCommitStatus = 0; FailNextCommitBody = null; return new((HttpStatusCode)status) { Content = new StringContent(commitBody) }; }
            if (method == "attachment.read" && AttachmentReadContent is { } content)
            {
                long offset = call.GetProperty("request").GetProperty("offset").GetInt64();
                if (offset < 0) return new(HttpStatusCode.Conflict) { Content = new StringContent("{\"code\":\"plugin_capability_invalid\"}") };
                int start = (int)Math.Min(offset, content.Length);
                string encoded = Convert.ToBase64String(content.AsSpan(start, Math.Min(256 * 1024, content.Length - start)));
                return new(HttpStatusCode.OK) { Content = new StringContent("{\"base64\":\"" + encoded + "\",\"eof\":true}") };
            }
            string body = method switch
            {
                "dataset.open" => DatasetResultBody,
                "dataset.validate" => """{"valid":true}""",
                "data.describe" => "{\"schemaRevision\":\"schema_1\"}",
                "mutation.preview" => "{\"affectedCount\":1}",
                "import.freeze" => "{\"attempt\":1,\"plan\":{\"contract\":\"vibetable.surface-mutation-plan.v1\",\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"operations\":[{\"kind\":\"create\",\"values\":{\"name\":\"new\"}}]}}",
                "mutation.commit" => "{\"contractVersion\":\"2.0\",\"status\":\"applied\",\"affectedRows\":[{\"recordId\":\"r1\",\"operation\":\"insert\"}]}",
                "attachment.list" => "{\"items\":[{\"storedName\":\"internal-stored-name\",\"displayName\":\"safe.txt\",\"mediaType\":\"text/plain\",\"sizeBytes\":3}]}",
                "attachment.read" => "{\"base64\":\"YWJj\",\"eof\":true}",
                _ => "{}"
            };
            return new(HttpStatusCode.OK) { Content = new StringContent(body) };
        }
    }
}
