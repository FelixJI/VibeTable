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

    // -----------------------------------------------------------------------
    // Regression: after a create/delete, the C# workspace's known-tables cache
    // MUST be refreshed so a subsequent table.selected for the new (or
    // just-removed) name is accepted (or rejected) against the FRESH list.
    // Previously the cache was only populated by OpenDatabaseAsync, so creating
    // a table and clicking it in the sidebar threw ArgumentException ("Table
    // '...' is not one of the names advertised by source discovery").
    // -----------------------------------------------------------------------

    [TestMethod]
    public async Task CreateTableRequested_ThenTableSelected_AllowsSelectWithoutFailure()
    {
        var directus = new FakeDirectusRpcGateway
        {
            // After create, list returns the new table alongside an existing one.
            ListCollectionsResult = new DirectusCollectionList(
                new[] { "existing", "vt_t_new8HQS4XPG7DKRR7S9" },
                new Dictionary<string, string>()),
        };
        // The workspace gateway must serve pages for the new table once selected.
        var tableGateway = new FakeTableRpcGateway();
        tableGateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "existing" }, Array.Empty<string>());
        tableGateway.TablePages["vt_t_new8HQS4XPG7DKRR7S9"] = new Dictionary<int, TablePage>
        {
            [0] = new TablePage(
                Table: "vt_t_new8HQS4XPG7DKRR7S9",
                Columns: Array.Empty<ColumnSchema>(),
                Rows: Array.Empty<Dictionary<string, object?>>(),
                Offset: 0, Limit: 500, TotalRows: 0, Mode: "client"),
        };
        var workspace = new TableWorkspaceService(tableGateway);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        // Initial open seeds the cache with only "existing".
        dispatcher.Dispatch(new RoutedWebRequest(
            "database.openRequested", "req-open",
            JsonDocument.Parse("""{}""").RootElement.Clone(), ""));
        await sink.WaitForAsync("database.opened");

        // Create the new table. The handler re-lists and posts collectionsChanged.
        var createPayload = JsonDocument.Parse(
            """{"name":"newtable","fields":[{"key":"name","type":"string"}]}""")
            .RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.createRequested", "req-c", createPayload, ""));
        await sink.WaitForAsync("database.collectionsChanged");

        // Now select the freshly-created table. Before the fix this threw
        // ArgumentException inside SelectTableAsync and surfaced as
        // operation.failed on "req-sel".
        var selectPayload = JsonDocument.Parse(
            """{"table":"vt_t_new8HQS4XPG7DKRR7S9"}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected", "req-sel", selectPayload, ""));

        // Give the fire-and-forget dispatch a brief window to produce any
        // operation.failed. Absence of failure (within the window) is success.
        var failed = await sink.WaitForFailedAsync(timeoutMs: 400);
        Assert.IsNull(failed, $"table.selected should not fail after create; got: {failed?.Payload}");
        // And the workspace gateway should have been asked to read the new table.
        Assert.IsTrue(tableGateway.ReadTablePageCalls.Any(
            c => c.Table == "vt_t_new8HQS4XPG7DKRR7S9"));
    }

    [TestMethod]
    public async Task DeleteTableRequested_RemovesTableFromKnownTables()
    {
        var directus = new FakeDirectusRpcGateway
        {
            DeleteTableResult = new DeleteTableResult("old", Deleted: true),
            // After delete, list returns only the remaining table.
            ListCollectionsResult = new DirectusCollectionList(
                new[] { "remaining" }, new Dictionary<string, string>()),
        };
        var tableGateway = new FakeTableRpcGateway();
        tableGateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "remaining", "old" }, Array.Empty<string>());
        var workspace = new TableWorkspaceService(tableGateway);
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        // Open seeds the cache with BOTH "remaining" and "old".
        dispatcher.Dispatch(new RoutedWebRequest(
            "database.openRequested", "req-open",
            JsonDocument.Parse("""{}""").RootElement.Clone(), ""));
        await sink.WaitForAsync("database.opened");

        // Delete "old"; the handler re-lists and posts collectionsChanged.
        var deletePayload = JsonDocument.Parse("""{"collection":"old"}""")
            .RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "tableAdmin.deleteRequested", "req-d", deletePayload, ""));
        await sink.WaitForAsync("database.collectionsChanged");

        // Selecting the deleted name must now fail — proves the cache actually
        // refreshed (not just grew).
        var selectPayload = JsonDocument.Parse("""{"table":"old"}""")
            .RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "table.selected", "req-sel", selectPayload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("req-sel", failed!.RequestId);
    }

    // -----------------------------------------------------------------------
    // identifierMappings.delete/purge routing into IDirectusRpcGateway.
    // -----------------------------------------------------------------------

    [TestMethod]
    public async Task DeleteIdentifierMappingRequested_CallsGatewayAndPostsResult()
    {
        var directus = new FakeDirectusRpcGateway();
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse("""{"mappingId":"m-1"}""")
            .RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "identifierMappings.deleteRequested", "req-del", payload, ""));

        var reply = await sink.WaitForAsync("identifierMappings.result");
        Assert.IsNotNull(reply);
        Assert.AreEqual("req-del", reply!.RequestId);
        Assert.AreEqual(1, directus.DeleteIdentifierMappingCalls.Count);
        Assert.AreEqual("m-1", directus.DeleteIdentifierMappingCalls[0]);
    }

    [TestMethod]
    public async Task DeleteIdentifierMappingRequested_MissingMappingId_PostsBadPayload()
    {
        var directus = new FakeDirectusRpcGateway();
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse("""{}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "identifierMappings.deleteRequested", "req-del", payload, ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)failed!.Payload!).code);
        Assert.AreEqual(0, directus.DeleteIdentifierMappingCalls.Count);
    }

    [TestMethod]
    public async Task PurgeIdentifierMappingsRequested_CallsGatewayAndPostsResult()
    {
        var directus = new FakeDirectusRpcGateway();
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var payload = JsonDocument.Parse("""{}""").RootElement.Clone();
        dispatcher.Dispatch(new RoutedWebRequest(
            "identifierMappings.purgeRequested", "req-purge", payload, ""));

        var reply = await sink.WaitForAsync("identifierMappings.result");
        Assert.IsNotNull(reply);
        Assert.AreEqual("req-purge", reply!.RequestId);
        Assert.AreEqual(1, directus.PurgeIdentifierMappingsCalls);
    }

    [TestMethod]
    public async Task RelationLookupRequests_UseClosedGatewayMethods_AndEchoCorrelation()
    {
        var directus = new FakeDirectusRpcGateway();
        var workspace = new TableWorkspaceService(new FakeTableRpcGateway());
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            workspace, new FakeDatabasePicker("db"), sink);
        dispatcher.SetDirectusGateway(directus);

        var cases = new (string Type, string Payload)[]
        {
            ("schema.describe", """{"collection":"contracts","requestGeneration":1,"accepts":["vibetable.relation-capabilities.v1","vibetable.lookup-query.v1"]}"""),
            ("relation.searchTargets", """{"relationId":"rel-1"}"""),
            ("relation.updateSingle", """{"relationId":"rel-1","sourceItemId":"1","target":null,"expectedSchemaRevision":"s1","idempotencyKey":"i1"}"""),
            ("relation.previewDelta", """{"relationId":"rel-1","sourceItemId":"1","expectedSchemaRevision":"s1","adds":[],"updates":[],"removes":[],"idempotencyKey":"i2"}"""),
            ("relation.applyDelta", """{"relationId":"rel-1","sourceItemId":"1","expectedSchemaRevision":"s1","adds":[],"updates":[],"removes":[],"idempotencyKey":"i3"}"""),
            ("lookup.list", """{"collection":"contracts"}"""),
            ("lookup.validate", """{"definition":{},"existing":[]}"""),
            ("lookup.create", """{"definition":{},"requestId":"i4"}"""),
            ("lookup.update", """{"definition":{},"expectedRevision":1,"requestId":"i5"}"""),
            ("lookup.delete", """{"collection":"contracts","lookupId":"l1","expectedRevision":1,"requestId":"i6"}"""),
            ("lookup.preview", LookupQueryPayload(includeDefinitions: true)),
            ("lookup.query", LookupQueryPayload(includeDefinitions: false)),
            ("table_admin.previewRelationChange", """{"collection":"contracts","action":"create","config":{},"expectedSchemaRevision":"s1"}"""),
            ("table_admin.applyRelationChange", """{"planId":"plan-1","operationId":"operation-1","expectedSchemaRevision":"s1","cascadeLookupIds":[]}"""),
        };

        CollectionAssert.AreEquivalent(
            RelationLookupRpcRegistry.RequestTypes.ToArray(),
            cases.Select(item => item.Type).ToArray(),
            "Every registered endpoint must have an explicit dispatcher/gateway test case.");

        for (var index = 0; index < cases.Length; index++)
        {
            var item = cases[index];
            string requestId = $"request-{index}";
            dispatcher.Dispatch(new RoutedWebRequest(
                item.Type,
                requestId,
                JsonDocument.Parse(item.Payload).RootElement.Clone(),
                ""));

            var reply = await sink.WaitForAsync(item.Type);
            Assert.IsNotNull(reply, item.Type);
            Assert.AreEqual(requestId, reply!.RequestId, item.Type);
        }

        CollectionAssert.AreEqual(
            cases.Select(item => item.Type).ToArray(),
            directus.RelationLookupCalls.Select(call => call.Method).ToArray());
    }

    [TestMethod]
    public async Task RelationLookupRequest_WithoutAuthenticatedGateway_IsStableFailure()
    {
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);

        dispatcher.Dispatch(new RoutedWebRequest(
            "lookup.list",
            "request-auth",
            JsonDocument.Parse("""{"collection":"contracts"}""").RootElement.Clone(),
            ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("request-auth", failed!.RequestId);
        Assert.AreEqual("NOT_AUTHENTICATED", ((dynamic)failed.Payload!).code);
    }

    [TestMethod]
    public async Task RelationLookupRequest_InvalidPayload_IsRejectedBeforeRpc()
    {
        var directus = new FakeDirectusRpcGateway();
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);
        dispatcher.SetDirectusGateway(directus);

        dispatcher.Dispatch(new RoutedWebRequest(
            "relation.updateSingle",
            "request-bad",
            JsonDocument.Parse("""{"relationId":42}""").RootElement.Clone(),
            ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("request-bad", failed!.RequestId);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)failed.Payload!).code);
        Assert.AreEqual(0, directus.RelationLookupCalls.Count);
    }

    [TestMethod]
    public async Task RelationLookupRequest_JsonDeserializationFailure_IsStableBadPayload()
    {
        var directus = new FakeDirectusRpcGateway
        {
            RelationLookupException = new JsonException("invalid relation payload"),
        };
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);
        dispatcher.SetDirectusGateway(directus);

        dispatcher.Dispatch(new RoutedWebRequest(
            "lookup.list",
            "request-json",
            JsonDocument.Parse("""{"collection":"contracts"}""").RootElement.Clone(),
            ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("request-json", failed!.RequestId);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)failed.Payload!).code);
        Assert.AreEqual(1, directus.RelationLookupCalls.Count);
    }

    [TestMethod]
    public async Task PreviewRelationChange_InvalidAction_IsRejectedBeforeRpc()
    {
        var directus = new FakeDirectusRpcGateway();
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);
        dispatcher.SetDirectusGateway(directus);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table_admin.previewRelationChange",
            "request-action",
            JsonDocument.Parse(
                """{"collection":"contracts","action":"replace","expectedSchemaRevision":"s1"}""")
                .RootElement.Clone(),
            ""));

        var failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("request-action", failed!.RequestId);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)failed.Payload!).code);
        Assert.AreEqual(0, directus.RelationLookupCalls.Count);
    }

    private static string LookupQueryPayload(bool includeDefinitions)
    {
        string definitions = includeDefinitions ? ",\"definitions\":[]" : string.Empty;
        return "{\"contract\":\"vibetable.lookup-query.v1\",\"collection\":\"contracts\","
            + "\"fieldRefs\":[],\"query\":{},\"requestGeneration\":1,"
            + "\"schemaRevision\":\"s1\",\"permissionRevision\":\"p1\","
            + "\"lookupRevision\":\"l1\"" + definitions + "}";
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
