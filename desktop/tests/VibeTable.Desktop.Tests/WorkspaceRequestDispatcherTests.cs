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
            new DatabaseOpenResult(
                new[] { "vt_t_contracts" },
                Array.Empty<string>(),
                DisplayNames: new Dictionary<string, string>
                {
                    ["vt_t_contracts"] = "合同清单",
                });
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

        var opened = await sink.WaitForAsync("database.opened");

        Assert.AreEqual(1, gateway.OpenDatabaseCalls.Count);
        Assert.AreEqual("C:/picker/chosen.db", gateway.OpenDatabaseCalls[0]);
        var namesObj = opened!.Payload!.GetType()
            .GetProperty("displayNames")?.GetValue(opened.Payload);
        var names = (IReadOnlyDictionary<string, string>)namesObj!;
        Assert.AreEqual("合同清单", names["vt_t_contracts"]);
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
            File.WriteAllText(Path.Combine(root, "local-directus", "package.json"), "{}");
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

    /// <summary>
    /// A bare dev run (no flag, no external URL) auto-starts Directus when the
    /// repo's scripts/local_directus is discoverable from the host output dir.
    /// </summary>
    [TestMethod]
    public void HostStartupOptions_AutoStartsDevLocalDirectusOnBareRun()
    {
        // Simulate a repo root: pyproject.toml + backend/ + scripts/local_directus.
        string repo = Path.Combine(
            Path.GetTempPath(), "vibetable-dev-repo-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(Path.Combine(repo, "backend"));
        Directory.CreateDirectory(Path.Combine(repo, "scripts", "local_directus"));
        // Simulate the host's output directory deep inside the repo.
        string hostOut = Path.Combine(repo, "bin", "Release", "net10.0-windows");
        Directory.CreateDirectory(hostOut);
        try
        {
            File.WriteAllText(Path.Combine(repo, "pyproject.toml"), "");
            File.WriteAllText(Path.Combine(repo, "scripts", "local_directus", "package.json"), "{}");

            // Bare run: no flags, no URL → the full stack should come up.
            Assert.IsTrue(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: false,
                configuredUrl: null,
                hostOut));
            // External URL still wins (no auto-start against a remote Directus).
            Assert.IsFalse(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: false,
                configuredUrl: "https://directus.example.com",
                hostOut));
        }
        finally
        {
            Directory.Delete(repo, recursive: true);
        }
    }

    /// <summary>
    /// --no-directus-auto disables auto-start even in a layout that would
    /// otherwise start Directus (and even with --directus-auto also set).
    /// </summary>
    [TestMethod]
    public void HostStartupOptions_NoDirectusAutoOverridesEverything()
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-noauto-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(Path.Combine(root, "local-directus"));
        Directory.CreateDirectory(Path.Combine(root, "backend"));
        try
        {
            File.WriteAllText(Path.Combine(root, "local-directus", "package.json"), "{}");
            File.WriteAllText(Path.Combine(root, "backend", "vibetable-backend.exe"), "");

            // Packaged layout that would normally auto-start, but disabled.
            Assert.IsFalse(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: false,
                configuredUrl: null,
                root,
                explicitlyDisabled: true));
            // Disabled wins even over an explicit --directus-auto.
            Assert.IsFalse(HostStartupOptions.ShouldAutoStartLocalDirectus(
                explicitlyRequested: true,
                configuredUrl: null,
                root,
                explicitlyDisabled: true));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    // -----------------------------------------------------------------------
    // Task 8: tableAdmin create/delete handlers wired to IDirectusRpcGateway.
    // -----------------------------------------------------------------------

    [TestMethod]
    public async Task CreateTableRequested_WhenGatewayNull_PostsNotAuthenticated()
    {
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        // No SetDirectusGateway call -> gateway is null.

        var payload = JsonDocument.Parse(
            """{"name":"projects","fields":[{"key":"name","type":"string"}]}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested", "req-c", payload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("NOT_AUTHENTICATED", ((dynamic)failed!.Payload!).code);
    }

    [TestMethod]
    public async Task CreateTableRequested_CallsGatewayAndPostsCollectionsChanged()
    {
        var directus = new FakeDirectusRpcGateway
        {
            // After create, list returns these (incl. system tables to prove filtering).
            ListCollectionsResult = new DirectusCollectionList(
                new[] { "projects", "directus_users", "tasks" },
                new Dictionary<string, string>()),
        };
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse(
            """{"name":"projects","fields":[{"key":"name","type":"string"}]}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested", "req-c", payload, ""));

        var notif = await sink.WaitForAsync("database.collectionsChanged");
        Assert.IsNotNull(notif);
        Assert.AreEqual(1, directus.CreateTableCalls.Count);
        Assert.AreEqual("projects", directus.CreateTableCalls[0].Name);
        // The notification payload is an anonymous type created in the
        // VibeTable.Desktop assembly (internal there), so read its 'tables'
        // member via reflection rather than dynamic binding. The list must be
        // FILTERED (directus_users removed) + SORTED (projects before tasks).
        var tablesObj = notif!.Payload!.GetType()
            .GetProperty("tables")?.GetValue(notif.Payload);
        var tables = ((IEnumerable<string>)tablesObj!).ToList();
        CollectionAssert.AreEqual(new[] { "projects", "tasks" }, tables);
    }

    [TestMethod]
    public async Task DeleteTableRequested_CallsGatewayAndPostsCollectionsChanged()
    {
        var directus = new FakeDirectusRpcGateway
        {
            DeleteTableResult = new DeleteTableResult("old", Deleted: true),
            ListCollectionsResult = new DirectusCollectionList(
                new[] { "remaining" }, new Dictionary<string, string>()),
        };
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse("""{"collection":"old"}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.deleteRequested", "req-d", payload, ""));

        var notif = await sink.WaitForAsync("database.collectionsChanged");
        Assert.IsNotNull(notif);
        Assert.AreEqual(1, directus.DeleteTableCalls.Count);
        Assert.AreEqual("old", directus.DeleteTableCalls[0]);
    }

    [TestMethod]
    public async Task DeleteTableRequested_WhenBackendDeclines_PostsDeleteDeclined()
    {
        // The backend may return Deleted:false (e.g. protected/system collection,
        // or already gone). The host must NOT silently report success via
        // collectionsChanged; it posts operation.failed code DELETE_DECLINED and
        // does not re-list collections.
        var directus = new FakeDirectusRpcGateway
        {
            DeleteTableResult = new DeleteTableResult("protected", Deleted: false),
            ListCollectionsResult = new DirectusCollectionList(
                new[] { "protected" }, new Dictionary<string, string>()),
        };
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse("""{"collection":"protected"}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.deleteRequested", "req-d", payload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("DELETE_DECLINED", (string)((dynamic)failed!.Payload!).code);
        Assert.AreEqual(1, directus.DeleteTableCalls.Count);
        // No re-list / collectionsChanged when declined.
        Assert.AreEqual(0, directus.ListCollectionsCalls.Count);
    }

    [TestMethod]
    public async Task CreateTableRequested_OnBackendError_PostsOperationFailed()
    {
        var directus = new FakeDirectusRpcGateway
        {
            CreateTableException = new InvalidOperationException("name already exists"),
        };
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse(
            """{"name":"x","fields":[]}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested", "req-c", payload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        string message = (string)((dynamic)failed!.Payload!).message;
        StringAssert.StartsWith(message, "创建表失败：");
        StringAssert.Contains(message, "name already exists");
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

    public void PostResponse(string type, string? requestId, object? payload)
    {
        lock (_gate)
        {
            _replies.Add(new Reply(type, requestId, payload));
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
