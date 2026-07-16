using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Tests for <see cref="WorkspaceRequestDispatcher"/> — the glue between the
/// whitelisted router and the table workspace.
/// </summary>
/// <remarks>
/// These pin the Task-10 invariants at the routing boundary:
/// <list type="bullet">
/// <item><c>database.openRequested</c> uses ONLY the picker's path (the web
/// payload path is ignored).</item>
/// <item><c>database.opened</c> is posted with the tables/views on success.</item>
/// <item><c>table.selected</c> forwards a known table name and surfaces
/// rejection of an unknown name as <c>operation.failed</c>.</item>
/// <item><c>HostStartupOptions</c> parses the shell-smoke CLI flags verbatim.
/// </item>
/// </list>
/// </remarks>
[TestClass]
public sealed class WorkspaceRequestDispatcherTests
{
    [TestMethod]
    public async Task DatabaseOpenRequested_UsesPickerPath_NotWebPayloadPath()
    {
        // The web payload carries a "path" field, but the host MUST ignore it
        // and use the picker's path instead (paths come only from the picker).
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["C:/picker/chosen.db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        var workspace = new TableWorkspaceService(gateway);
        var picker = new FakeDatabasePicker("C:/picker/chosen.db");
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(workspace, picker, sink);

        // Web payload supplies a DIFFERENT, attacker-controlled path. It must be
        // ignored: only the picker's path reaches the gateway.
        var payload = JsonDocument.Parse(
            """{"path":"C:/evil/attacker.db"}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "database.openRequested", "req-1", payload, Raw: ""));

        await sink.WaitForAsync("database.opened");

        Assert.AreEqual(1, gateway.OpenDatabaseCalls.Count);
        Assert.AreEqual("C:/picker/chosen.db", gateway.OpenDatabaseCalls[0]);
    }

    [TestMethod]
    public async Task TableSelected_UnknownName_PostsOperationFailed()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        var workspace = new TableWorkspaceService(gateway);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);

        // Open first so the known-tables list is populated.
        dispatcher.Dispatch(new RoutedWebRequest(
            "database.openRequested", "req-open",
            JsonDocument.Parse("""{}""").RootElement.Clone(), ""));
        await sink.WaitForAsync("database.opened");

        // Now select a table name that was NOT advertised.
        var payload = JsonDocument.Parse("""{"table":"not-a-real-table"}""")
            .RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected", "req-sel", payload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.AreEqual("req-sel", failed!.RequestId);
    }

    [TestMethod]
    public void HostStartupOptions_Parses_TestModeFlags_Verbatim()
    {
        var options = HostStartupOptions.Parse(new[]
        {
            "--test-mode",
            "--readiness-dir", "C:/tmp/out",
        });

        Assert.IsTrue(options.TestMode);
        Assert.AreEqual("C:/tmp/out", options.ReadinessDir);
    }

    [TestMethod]
    public void HostStartupOptions_AutoStartsPackagedLocalDirectusWithoutEnvironmentUrl()
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-packaged-auto-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(Path.Combine(root, "local-directus"));
        Directory.CreateDirectory(Path.Combine(root, "backend"));
        try
        {
            File.WriteAllText(Path.Combine(root, "local-directus", "run.py"), "");
            File.WriteAllText(Path.Combine(root, "backend", "vibetable-backend.exe"), "");

            Assert.IsTrue(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: false,
                configuredUrl: null,
                root));
            Assert.IsFalse(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: false,
                configuredUrl: "https://directus.example.com",
                root));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

internal sealed class FakeDatabasePicker : IDatabasePicker
{
    private readonly string _path;
    public FakeDatabasePicker(string path) => _path = path;
    public Task<string?> PickDatabaseAsync() => Task.FromResult<string?>(_path);
}

internal sealed class FakeWebReplySink : IWebReplySink
{
    public sealed record Reply(string Type, string? RequestId, object? Payload);

    private readonly List<Reply> _replies = new();
    private readonly object _gate = new();

    public void PostNotification(string type, object? payload)
    {
        lock (_gate)
        {
            _replies.Add(new Reply(type, null, payload));
            Monitor.PulseAll(_gate);
        }
    }

    public void PostOperationFailed(string? requestId, string message, string? code = null)
    {
        lock (_gate)
        {
            _replies.Add(new Reply("operation.failed", requestId,
                new { message, code }));
            Monitor.PulseAll(_gate);
        }
    }

    public List<Reply> Replies
    {
        get { lock (_gate) { return _replies.ToList(); } }
    }

    public async Task<Reply?> WaitForAsync(string type, int timeoutMs = 2000)
    {
        var deadline = DateTime.UtcNow.AddMilliseconds(timeoutMs);
        while (true)
        {
            lock (_gate)
            {
                var match = _replies.FirstOrDefault(r => r.Type == type);
                if (match is not null) return match;
                var remaining = deadline - DateTime.UtcNow;
                if (remaining <= TimeSpan.Zero) return null;
                Monitor.Wait(_gate, remaining);
            }
        }
    }

    public async Task<Reply?> WaitForFailedAsync(int timeoutMs = 2000)
        => await WaitForAsync("operation.failed", timeoutMs).ConfigureAwait(false);
}
