using System;
using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Whitelist / validation tests for <see cref="WebMessageRouter"/>.
/// </summary>
/// <remarks>
/// <para>
/// The router is the only path from the untrusted WebView2 boundary into the
/// .NET host. Every Phase A inbound web type is whitelisted explicitly; an
/// out-of-whitelist type, a payload over 4 MiB, an invalid JSON document, or
/// any message received before the host reaches the Ready state MUST be
/// rejected with an <c>operation.failed</c> reply and MUST NOT reach the
/// JSON-RPC client.
/// </para>
/// <para>
/// Host -&gt; web notifications use a SEPARATE whitelist
/// (<see cref="WebMessageRouter.IsHostNotificationAllowed"/>) so future
/// business notifications each require an explicit DTO + mapping; there is no
/// generic forwarding of RPC notifications to the WebView in Phase A.
/// </para>
/// </remarks>
[TestClass]
public sealed class WebMessageRouterTests
{
    private static readonly string AppReadyJson =
        """{"type":"app.ready","requestId":"r1","payload":{}}""";

    [TestMethod]
    public void Route_AppReadyBeforeReady_DispatchesBootstrapHandshake()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = false
        };

        var reply = router.Route(AppReadyJson);

        Assert.IsNull(reply);
        Assert.AreEqual(1, dispatched.Count);
        Assert.AreEqual("app.ready", dispatched[0].Type);
    }

    [TestMethod]
    public void Route_BusinessRequestBeforeReady_ReturnsOperationFailed_AndDoesNotDispatch()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = false
        };

        var reply = router.Route(
            """{"type":"database.openRequested","requestId":"r1","payload":{}}""");

        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply!.Type);
        Assert.AreEqual("r1", reply.RequestId);
        Assert.AreEqual(0, dispatched.Count);
    }

    [TestMethod]
    public void Route_ValidAppReady_Dispatches_AndReturnsNoReply()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        var reply = router.Route(AppReadyJson);

        Assert.IsNull(reply);
        Assert.AreEqual(1, dispatched.Count);
        Assert.AreEqual("app.ready", dispatched[0].Type);
        Assert.AreEqual("r1", dispatched[0].RequestId);
    }

    [TestMethod]
    public void Route_StartupSubmissionAfterAppReady_IsWhitelisted()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req)) { IsReady = true };

        var reply = router.Route(
            """{"type":"host.loginSubmitted","payload":{"email":"admin@example.com","password":"secret123"}}""");

        Assert.IsNull(reply);
        Assert.AreEqual("host.loginSubmitted", dispatched.Single().Type);
        Assert.AreEqual(string.Empty, dispatched.Single().Raw);
        Assert.IsTrue(router.IsHostNotificationAllowed("host.startupStateChanged"));
    }

    [TestMethod]
    public void Route_UnknownType_ReturnsOperationFailed_AndDoesNotDispatch()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        var reply = router.Route(
            """{"type":"system.byebye","requestId":"r2","payload":{}}""");

        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply!.Type);
        Assert.AreEqual("r2", reply.RequestId);
        Assert.AreEqual(0, dispatched.Count);
    }

    [TestMethod]
    public void Route_AllFourWhitelistedTypes_AreAccepted()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        foreach (var (type, payload) in new[]
        {
            ("app.ready", """{}"""),
            ("database.openRequested", """{"path":"C:\\x.db"}"""),
            ("table.selected", """{"table":"t"}"""),
            ("table.pageRequested", """{"table":"t","offset":0,"limit":50}""")
        })
        {
            var json = $"{{\"type\":\"{type}\",\"requestId\":\"id-{type}\",\"payload\":{payload}}}";
            var reply = router.Route(json);
            Assert.IsNull(reply, $"{type} should be accepted");
        }

        Assert.AreEqual(4, dispatched.Count);
    }

    [TestMethod]
    public void Route_InvalidJson_ReturnsOperationFailed()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        var reply = router.Route("not json");

        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply!.Type);
        Assert.IsNull(reply.RequestId);
        Assert.AreEqual(0, dispatched.Count);
    }

    [TestMethod]
    public void Route_MissingType_ReturnsOperationFailed()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        var reply = router.Route("""{"requestId":"r3","payload":{}}""");

        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply!.Type);
        Assert.AreEqual("r3", reply.RequestId);
        Assert.AreEqual(0, dispatched.Count);
    }

    [TestMethod]
    public void Route_PayloadOver4MiB_ReturnsOperationFailed()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        // Build a payload whose UTF-8 byte length exceeds the 4 MiB cap.
        var big = new string('A', (4 * 1024 * 1024) + 32);
        var json = $"{{\"type\":\"app.ready\",\"requestId\":\"r4\",\"payload\":{{\"x\":\"{big}\"}}}}";

        var reply = router.Route(json);

        Assert.IsNotNull(reply);
        Assert.AreEqual("operation.failed", reply!.Type);
        Assert.AreEqual("r4", reply.RequestId);
        Assert.AreEqual(0, dispatched.Count);
    }

    [TestMethod]
    public void Route_PayloadExactly4MiB_IsAccepted()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        // Aim for a total message size just under 4 MiB.
        var big = new string('A', (4 * 1024 * 1024) - 256);
        var json = $"{{\"type\":\"app.ready\",\"requestId\":\"r5\",\"payload\":{{\"x\":\"{big}\"}}}}";

        var reply = router.Route(json);

        Assert.IsNull(reply);
        Assert.AreEqual(1, dispatched.Count);
    }

    [TestMethod]
    public void IsHostNotificationAllowed_ReturnsTrue_OnlyForKnownNotifications()
    {
        var router = new WebMessageRouter(_ => { });

        Assert.IsTrue(router.IsHostNotificationAllowed("database.opened"));
        Assert.IsTrue(router.IsHostNotificationAllowed("table.pageLoaded"));
        // table.datasetReady MUST be whitelisted: TableWorkspaceService emits it
        // as the client-mode "full grid loaded" signal and the TS hostBridge
        // consumes it; the grid stays in its loading state forever if it is
        // dropped. Regression guard for the silent-trap bug where wiring the
        // outbound gate (without this entry) stranded every client-mode load.
        Assert.IsTrue(router.IsHostNotificationAllowed("table.datasetReady"));
        Assert.IsTrue(router.IsHostNotificationAllowed("operation.failed"));
        Assert.IsTrue(router.IsHostNotificationAllowed("directus.changed"));
        Assert.IsFalse(router.IsHostNotificationAllowed("system.warn"));
        Assert.IsFalse(router.IsHostNotificationAllowed(""));
    }

    [TestMethod]
    public void IsHostNotificationAllowed_RejectsUnknownNotificationType()
    {
        var router = new WebMessageRouter(_ => { });

        // Any future notification type MUST be added explicitly to the outbound
        // whitelist before PostNotification will forward it; this guard makes
        // the outbound boundary symmetric with the inbound router whitelist.
        Assert.IsFalse(router.IsHostNotificationAllowed("database.someFutureEvent"));
        Assert.IsFalse(router.IsHostNotificationAllowed("table.somethingNew"));
        Assert.IsFalse(router.IsHostNotificationAllowed("backend.notify"));
        // Null is rejected (defensive: the gate is callable with a null type).
        Assert.IsFalse(router.IsHostNotificationAllowed(null!));
    }

    [TestMethod]
    public void Whitelists_AcceptTableAdminRequestsAndCollectionsChangedNotification()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        // Inbound: the two new tableAdmin requests MUST be whitelisted (a null
        // reply means the message was accepted and dispatched, not rejected as
        // out-of-whitelist).
        foreach (var (type, payload) in new[]
        {
            ("tableAdmin.createRequested", """{"name":"t","fields":[]}"""),
            ("tableAdmin.deleteRequested", """{"collection":"t"}""")
        })
        {
            var json = $"{{\"type\":\"{type}\",\"requestId\":\"id-{type}\",\"payload\":{payload}}}";
            var reply = router.Route(json);
            Assert.IsNull(reply, $"{type} should be whitelisted inbound");
        }

        Assert.AreEqual(2, dispatched.Count);
        Assert.AreEqual("tableAdmin.createRequested", dispatched[0].Type);
        Assert.AreEqual("tableAdmin.deleteRequested", dispatched[1].Type);

        // Outbound: database.collectionsChanged MUST be allowed so the host can
        // push a refreshed collection list to the web sidebar after a
        // create/delete.
        Assert.IsTrue(router.IsHostNotificationAllowed("database.collectionsChanged"));
    }

    [TestMethod]
    public void BuildOperationFailed_ProducesValidEnvelope()
    {
        var envelope = WebMessageRouter.BuildOperationFailed(
            requestId: "abc",
            message: "bad",
            code: "BAD");

        Assert.AreEqual("operation.failed", envelope.Type);
        Assert.AreEqual("abc", envelope.RequestId);
        Assert.IsNotNull(envelope.Payload);
        Assert.AreEqual("bad", envelope.Payload!.Message);
        Assert.AreEqual("BAD", envelope.Payload.Code);
    }

    [TestMethod]
    public void Whitelists_AcceptOpaqueDocumentRequestsAndResponses()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(dispatched.Add) { IsReady = true };
        foreach (string type in new[]
        {
            "document.listRequested",
            "document.importRequested",
            "document.externalDropRequested",
            "document.dragOutRequested",
            "document.openRequested",
            "document.previewRequested",
            "document.revealRequested",
            "document.historyRequested",
            "document.relinkRequested",
        })
        {
            var reply = router.Route(JsonSerializer.Serialize(new
            {
                type,
                requestId = "r",
                payload = new { },
            }));
            Assert.IsNull(reply, type);
        }

        Assert.AreEqual(9, dispatched.Count);
        Assert.IsTrue(router.IsHostNotificationAllowed("document.listLoaded"));
        Assert.IsTrue(router.IsHostNotificationAllowed("document.historyLoaded"));
        Assert.IsTrue(router.IsHostNotificationAllowed("document.actionCompleted"));
        Assert.IsTrue(router.IsHostNotificationAllowed("document.workspaceChanged"));
        Assert.IsTrue(router.IsHostNotificationAllowed("document.operationFailed"));
    }

    [TestMethod]
    public void DocumentOperationFailedPayload_SerializesTypedWebShape()
    {
        var payload = new DocumentOperationFailedPayload("drop failed", "DROP_CODE");
        using var document = JsonDocument.Parse(JsonSerializer.Serialize(payload));

        Assert.AreEqual("drop failed", document.RootElement.GetProperty("message").GetString());
        Assert.AreEqual("DROP_CODE", document.RootElement.GetProperty("code").GetString());
    }

    [TestMethod]
    public void ExternalDropWithoutNativePaths_ProducesMissingObjectsFailure()
    {
        var failure = MainWindow.ValidateExternalDropPaths([]);

        Assert.IsNotNull(failure);
        Assert.AreEqual("DOCUMENT_DROP_OBJECTS_MISSING", failure!.Code);
        Assert.IsNull(MainWindow.ValidateExternalDropPaths([@"C:\safe\file.txt"]));
    }

    [TestMethod]
    public void Route_AcceptsAdminOpenRequested()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(req => dispatched.Add(req))
        {
            IsReady = true
        };

        string json = """{"type":"admin.openRequested","requestId":"r1","payload":{}}""";
        var reply = router.Route(json);

        Assert.IsNull(reply, "admin.openRequested should be accepted, not rejected");
        Assert.AreEqual(1, dispatched.Count);
        Assert.AreEqual("admin.openRequested", dispatched[0].Type);
    }
}
