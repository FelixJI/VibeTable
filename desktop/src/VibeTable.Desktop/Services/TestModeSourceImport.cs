using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Fixed packaged-E2E source. Only the TestMode control composition invokes
/// this adapter; no renderer data, URLs, paths or credentials are accepted.
/// </summary>
internal sealed class TestModeSourceImport(string scenario) : IHostSourceImportProvider
{
    internal const string SourceName = "QA 三表合成来源";
    internal const string AttachmentName = "qa-source-import.png";
    private static readonly byte[] AttachmentBytes = Convert.FromBase64String(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=");
    private readonly TaskCompletionSource _observing = new(TaskCreationOptions.RunContinuationsAsynchronously);

    internal static bool IsScenario(string value) => value is "success" or "drift" or "cancel";

    internal static async Task<JsonElement> RunAsync(string scenario,
        ProductSidecarGenerationSnapshot snapshot, HostDataIoTaskRegistry tasks,
        IWorkspaceHostEpochLeaseSource leases, CancellationToken token)
    {
        if (!IsScenario(scenario)) throw new ArgumentException("Unknown fixed source scenario.", nameof(scenario));
        var provider = new TestModeSourceImport(scenario);
        string session = tasks.RegisterSourceImportProvider(snapshot, leases, provider);
        string prefix = scenario switch { "drift" => "QA 来源漂移", "cancel" => "QA 来源取消", _ => "QA 来源迁移" };
        HostSourceImportPreview preview = await tasks.PrepareSourceImportAsync(session,
            new HostSourceImportOptions(["c", "a", "b"],
                [new("a", prefix + " A"), new("b", prefix + " B"), new("c", prefix + " C")], [], true), token)
            .ConfigureAwait(false);
        JsonElement initial = tasks.StartSourceImport(snapshot, JsonSerializer.SerializeToElement(new
        { providerSessionId = session, token = preview.Token, confirmed = true }));
        string taskId = initial.GetProperty("taskId").GetString()!;
        using var wait = CancellationTokenSource.CreateLinkedTokenSource(token);
        wait.CancelAfter(TimeSpan.FromMinutes(2));
        try
        {
            bool cancelled = false;
            while (true)
            {
                if (scenario == "cancel" && !cancelled && provider._observing.Task.IsCompleted)
                {
                    tasks.RequestCancel(taskId);
                    cancelled = true;
                }
                JsonElement status = tasks.Status(taskId);
                if (status.GetProperty("state").GetString() is "succeeded" or "failed" or "cancelled" or "aborted")
                    return status;
                await Task.Delay(50, wait.Token).ConfigureAwait(false);
            }
        }
        catch
        {
            tasks.RequestCancel(taskId);
            throw;
        }
    }

    public Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        string now = DateTimeOffset.UtcNow.ToString("O");
        HostSourceImportField Text(string id, string name) => new(id, name, "text", "text", false, []);
        HostSourceImportField Relation(string id, string name, string table, string reverse, string cardinality)
            => new(id, name, "relation", "relation", false, [], new(table, reverse, cardinality));
        HostSourceImportRecord Row(string id, string code, params (string Key, object Value)[] values)
        {
            var cells = new Dictionary<string, JsonElement>
            {
                ["title"] = JsonSerializer.SerializeToElement("QA 重复显示值"),
                ["code"] = JsonSerializer.SerializeToElement(code),
            };
            foreach (var value in values) cells.Add(value.Key, JsonSerializer.SerializeToElement(value.Value));
            return new(id, cells);
        }
        return Task.FromResult(new HostSourceImportSnapshot("synthetic", "qa-source-" + scenario,
            SourceName, "v1", new(now, now, "snapshot"),
            [
                new("a", "QA 来源 A", "a1", "title",
                    [Text("title", "名称"), Text("code", "编码"), Relation("ab", "关联 B", "b", "ba", "many"),
                        new("files", "附件", "file", "file", false, [])],
                    [Row("r1", "A-001", ("ab", new[] { "r1", "r2" }), ("files", new[] { "file-1" })),
                        Row("r2", "A-002", ("ab", new[] { "r2" }))]),
                new("b", "QA 来源 B", "b1", "title",
                    [Text("title", "名称"), Text("code", "编码"), Relation("ba", "反向 A", "a", "ab", "many"),
                        Relation("bc", "关联 C", "c", "", "one")],
                    [Row("r1", "B-001", ("ba", new[] { "r1" }), ("bc", "r1")),
                        Row("r2", "B-002", ("ba", new[] { "r1", "r2" }), ("bc", "r2"))]),
                new("c", "QA 来源 C", "c1", "title",
                    [Text("title", "名称"), Text("code", "编码"), Relation("ca", "循环 A", "a", "", "one")],
                    [Row("r1", "C-001", ("ca", "r2")), Row("r2", "C-002", ("ca", "r1"))]),
            ],
            [new("file-1", "a", "r1", "files", AttachmentName, "image/png", AttachmentBytes.Length)]));
    }

    public async Task<HostSourceImportObservation> ObserveAsync(CancellationToken token)
    {
        _observing.TrySetResult();
        if (scenario == "cancel") await Task.Delay(Timeout.InfiniteTimeSpan, token).ConfigureAwait(false);
        token.ThrowIfCancellationRequested();
        return new(scenario == "drift" ? "v2" : "v1",
            new Dictionary<string, string> { ["a"] = "a1", ["b"] = "b1", ["c"] = "c1" });
    }

    public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        if (attachment.Id != "file-1" || attachment.TableId != "a" || attachment.RecordId != "r1"
            || attachment.FieldId != "files" || attachment.Name != AttachmentName)
            throw new InvalidOperationException("Unknown fixed source attachment.");
        return Task.FromResult<Stream>(new MemoryStream(AttachmentBytes, writable: false));
    }

    public void Dispose() { }
}
