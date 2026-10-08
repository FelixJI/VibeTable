using System.IO;
using System.Text.Json;
using System.Threading;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Threading;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Composition-level regression for the trusted native source import window.
/// Every test drives the real <see cref="SourceImportWindow"/> on a dedicated
/// STA dispatcher (mirroring ProductWebViewBridgeNativeObjectTests); the
/// connect delegate and wizard session are gated fakes completing through
/// TaskCompletionSource, so every await boundary stays real while the test
/// stays deterministic without sleeps. The parked window is off-screen with
/// no taskbar entry and never activates, so local runs stay undisturbed.
/// </summary>
[TestClass]
public sealed class SourceImportWindowTests
{
    private const string SecretToken = "secret-token-DO-NOT-ECHO";
    private const string SecretAccessKey = "AKID-DO-NOT-ECHO";
    private const string SecretSigningKey = "SK-DO-NOT-ECHO";

    private static string DescribeSafe(Exception error) => "SAFE-" + error.GetType().Name;

    private static void AssertNoEcho(string? text)
    {
        Assert.IsNotNull(text);
        Assert.IsFalse(text.Contains(SecretToken, StringComparison.Ordinal), text);
        Assert.IsFalse(text.Contains(SecretAccessKey, StringComparison.Ordinal), text);
        Assert.IsFalse(text.Contains(SecretSigningKey, StringComparison.Ordinal), text);
        Assert.IsFalse(text.Contains("DO-NOT-ECHO", StringComparison.Ordinal), text);
    }

    [TestMethod]
    public async Task ConnectSelectPreviewConfirmReturnsTaskIdAndSubmittedProviderSurvivesClose()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();

        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "  https://feishu.test/base/app-native-test  ";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        // The connect is genuinely suspended at its await boundary.
        Assert.AreEqual("正在连接并读取当前授权范围内的表目录…",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.AreEqual(1, connect.SnapshotCalls().Count);
        FakeSourceProvider provider = new(log, "prov-main");
        connect.CompleteWith(provider);
        await fixture.SettleAsync();

        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text), "2 张表");
        ConnectCall call = connect.SnapshotCalls().Single();
        Assert.AreEqual("https://feishu.test/base/app-native-test", call.Source);
        Assert.AreEqual(SecretToken, call.AccessToken);
        Assert.AreEqual("", call.AccessKey);
        Assert.AreEqual("", call.Secret);
        Assert.AreEqual("", await fixture.ReadAsync(w => Require<PasswordBox>(w, "token-input").Password));
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        Assert.AreEqual("正在读取所选表并预检；此步骤不会创建目标表或写入记录…",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        PrepareCall prepare = session.SnapshotCalls().Single();
        Assert.AreSame(provider, prepare.Provider);
        AssertNoEcho(JsonSerializer.Serialize(prepare.Options));
        CollectionAssert.AreEqual(new[] { "tbl-1" }, prepare.Options.SelectedTableIds);
        Assert.AreEqual("tbl-1", prepare.Options.TargetNames.Single().TableId);
        Assert.AreEqual("订单", prepare.Options.TargetNames.Single().Name);
        Assert.AreEqual(0, prepare.Options.Decisions.Length);
        Assert.IsFalse(prepare.Options.ConfirmReverse);
        HostSourceImportPreview preview = session.CompletePending();
        await fixture.SettleAsync();

        string report = await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text);
        StringAssert.Contains(report, "订单：12 条记录");
        StringAssert.Contains(report, "提示 · tbl-1 字段映射已自动生成");
        StringAssert.Contains(report, "预检通过");
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        HostSourceImportOpenResult result = await fixture.ReadAsync(w => w.Result);
        Assert.IsFalse(result.Cancelled);
        Assert.AreEqual("task-native-1", result.TaskId);
        Assert.AreSame(preview, session.StartedPreview);
        Assert.AreEqual(1, session.Disposals);
        Assert.AreEqual(1, connect.Releases);
        // The registry owns the submitted provider; closing the window must
        // neither discard nor dispose it.
        Assert.AreEqual(0, provider.DisposeCount);
        Assert.IsFalse(log.Contains("session:pending-discarded"),
            string.Join(" -> ", log.Snapshot()));
        var entries = log.Snapshot();
        Assert.IsTrue(entries.IndexOf("session:started") >= 0
            && entries.IndexOf("session:disposed") > entries.IndexOf("session:started"),
            string.Join(" -> ", entries));
    }

    [TestMethod]
    public async Task EditingSelectionNamePolicyOrReverseInvalidatesConfirmationUntilRechecked()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await ConnectAndSelectFirstTableAsync(fixture, connect, new FakeSourceProvider(log, "prov-edit"));

        // A plan with blocking diagnostics never opens the confirm gate.
        session.CanApply = false;
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text), "预检存在阻断项");
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        session.CanApply = true;
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await fixture.DoAsync(w => Require<TextBox>(w, "table-target-tbl-1").Text = "订单_import");
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.AreEqual("订单_import", session.SnapshotCalls()[^1].Options.TargetNames.Single().Name);

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = false);
        await AssertConfirmationInvalidatedAsync(fixture);
        // Re-selecting the table must not resurrect the stale preview.
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await fixture.DoAsync(w => Require<ComboBox>(w, "field-policy-tbl-1-f-amount").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        HostSourceImportDecision decision = session.SnapshotCalls()[^1].Options.Decisions.Single();
        Assert.AreEqual("tbl-1", decision.TableId);
        Assert.AreEqual("f-amount", decision.FieldId);
        Assert.AreEqual("snapshot", decision.Policy);
        Assert.AreEqual("number", decision.TargetKind);
        Assert.IsTrue(decision.Confirmed);

        await fixture.DoAsync(w => Require<CheckBox>(w, "reverse-checkbox").IsChecked = true);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.IsTrue(session.SnapshotCalls()[^1].Options.ConfirmReverse);

        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        Assert.AreEqual("task-native-1", (await fixture.ReadAsync(w => w.Result)).TaskId);
        Assert.AreEqual(6, session.SnapshotCalls().Count);
    }

    [TestMethod]
    public async Task ClosingWhileConnectingCancelsReadThenDisposesSession()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button"); // Gate stays open: connect in flight.
        await fixture.DoAsync(w => w.Close());

        await connect.Cancelled.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        // The cancelled read never yielded a connection to release.
        Assert.AreEqual(0, connect.Releases);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.AreEqual(1, session.Disposals);
        HostSourceImportOpenResult result = await fixture.ReadAsync(w => w.Result);
        Assert.IsTrue(result.Cancelled);
        Assert.IsNull(result.TaskId);
        var entries = log.Snapshot();
        Assert.IsTrue(entries.IndexOf("connect:cancel-observed") >= 0
            && entries.IndexOf("session:disposed") > entries.IndexOf("connect:cancel-observed"),
            string.Join(" -> ", entries));
    }

    [TestMethod]
    public async Task ClosingWhilePreparingCancelsPrecheckThenDisposesProviderAndSession()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        FakeSourceProvider provider = new(log, "prov-inflight");
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await ConnectAndSelectFirstTableAsync(fixture, connect, provider);

        await ClickAsync(fixture, "preview-button"); // Gate stays open: precheck in flight.
        Assert.AreEqual("正在读取所选表并预检；此步骤不会创建目标表或写入记录…",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        await fixture.DoAsync(w => w.Close());

        await session.Cancelled.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text), "预检已取消");
        Assert.AreEqual(1, provider.DisposeCount);
        Assert.AreEqual(1, connect.Releases);
        Assert.AreEqual(1, session.Disposals);
        Assert.IsNull(session.StartedPreview);
        var entries = log.Snapshot();
        Assert.IsTrue(entries.IndexOf("session:prepare-cancel-observed") >= 0
            && entries.IndexOf("session:provider-discarded") > entries.IndexOf("session:prepare-cancel-observed")
            && entries.IndexOf("session:disposed") > entries.IndexOf("session:provider-discarded"),
            string.Join(" -> ", entries));
    }

    [TestMethod]
    public async Task CredentialInputsNeverEchoAndOnlyReachTheConnectDelegate()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("wps", connect, session);
        await fixture.ShowAsync();

        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "  file-wps-native-test  ";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
            Require<TextBox>(w, "access-key-input").Text = $"  {SecretAccessKey}  ";
            Require<PasswordBox>(w, "secret-input").Password = SecretSigningKey;
        });
        connect.Failure = new HttpRequestException(
            $"401 access_token={SecretToken} kso_key={SecretAccessKey} kso_secret={SecretSigningKey}");
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWith(new FakeSourceProvider(log, "prov-wps-unused")); // Fake throws after the gate.
        await fixture.SettleAsync();

        Assert.AreEqual("SAFE-HttpRequestException",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        AssertNoEcho(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        AssertNoEcho(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        ConnectCall call = connect.SnapshotCalls().Single();
        Assert.AreEqual("file-wps-native-test", call.Source);
        Assert.AreEqual(SecretToken, call.AccessToken);
        Assert.AreEqual(SecretAccessKey, call.AccessKey);
        Assert.AreEqual(SecretSigningKey, call.Secret);
        // Secrets leave the inputs as soon as they are handed to the delegate.
        Assert.AreEqual("", await fixture.ReadAsync(w => Require<PasswordBox>(w, "token-input").Password));
        Assert.AreEqual("", await fixture.ReadAsync(w => Require<PasswordBox>(w, "secret-input").Password));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));

        connect.Failure = null;
        await fixture.DoAsync(w => Require<PasswordBox>(w, "token-input").Password = SecretToken);
        await ClickAsync(fixture, "connect-button");
        FakeSourceProvider provider = new(log, "prov-wps");
        connect.CompleteWith(provider);
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text), "张表");
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();

        session.PrepareFailure = new InvalidOperationException(
            $"preview failed token={SecretToken} secret={SecretSigningKey}");
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual("SAFE-InvalidOperationException",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        AssertNoEcho(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.AreEqual(1, provider.DisposeCount);

        session.PrepareFailure = null;
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        session.StartFailure = new InvalidOperationException($"start failed token={SecretToken}");
        await ClickAsync(fixture, "start-button");
        await fixture.SettleAsync();
        Assert.AreEqual("SAFE-InvalidOperationException",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        AssertNoEcho(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);

        session.StartFailure = null;
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        Assert.AreEqual("task-native-1", (await fixture.ReadAsync(w => w.Result)).TaskId);
        foreach (PrepareCall prepare in session.SnapshotCalls())
            AssertNoEcho(JsonSerializer.Serialize(prepare.Options));
    }

    [TestMethod]
    [DataRow("relation", "json", DisplayName = "relation 快照目标转 json")]
    [DataRow("file", "json", DisplayName = "附件 file 快照目标转 json")]
    [DataRow("formula", "json", DisplayName = "formula 快照目标转 json")]
    [DataRow("lookup", "json", DisplayName = "lookup 快照目标转 json")]
    [DataRow("autoDate", "json", DisplayName = "autoDate 快照目标转 json")]
    [DataRow("autoNumber", "text", DisplayName = "autoNumber 原编号保留为 text")]
    [DataRow("number", "number", DisplayName = "number 快照目标保持 number")]
    [DataRow("dateTime", "dateTime", DisplayName = "dateTime 快照目标保持 dateTime")]
    [DataRow("", "", DisplayName = "空 ValueKind 留空由引擎默认 json")]
    public async Task SnapshotDecisionsMapNonWritableKindsToExplicitJsonTargets(
        string valueKind, string expectedTargetKind)
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWithSingleFieldCatalog(new FakeSourceProvider(log, "prov-snapshot"), valueKind);
        await fixture.SettleAsync();
        await fixture.DoAsync(w =>
        {
            Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true;
            Require<ComboBox>(w, "field-policy-tbl-1-f-target").SelectedIndex = 1;
        });
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();

        // The untouched native field must not turn into a decision at all.
        HostSourceImportOptions options = session.SnapshotCalls().Single().Options;
        HostSourceImportDecision decision = options.Decisions.Single();
        Assert.AreEqual("tbl-1", decision.TableId);
        Assert.AreEqual("f-target", decision.FieldId);
        Assert.AreEqual("snapshot", decision.Policy);
        Assert.AreEqual(expectedTargetKind, decision.TargetKind);
        Assert.AreEqual("", decision.TargetName);
        Assert.IsTrue(decision.Confirmed);
        // A json snapshot carries the source value payload only; this asserts
        // the decision contract the Go planField accepts, never that any
        // attachment reference was actually downloaded.
    }

    [TestMethod]
    public async Task ReverseNamesForTwoRelationsLandInSeparateNativeDecisions()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWithRelationCatalog(new FakeSourceProvider(log, "prov-rel"));
        await fixture.SettleAsync();
        // Two one-sided relations (tbl-a→tbl-b and tbl-c→tbl-b) would collide
        // on the shared "迁移反向关联" default in Go checkFieldNames; each gets
        // its own explicit reciprocal name decision.
        await fixture.DoAsync(w =>
        {
            Require<CheckBox>(w, "table-select-tbl-a").IsChecked = true;
            Require<CheckBox>(w, "table-select-tbl-b").IsChecked = true;
            Require<CheckBox>(w, "table-select-tbl-c").IsChecked = true;
            Require<CheckBox>(w, "reverse-checkbox").IsChecked = true;
            Require<TextBox>(w, "field-reverse-name-tbl-a-f-rel-a").Text = "订单A的反向";
            Require<TextBox>(w, "field-reverse-name-tbl-c-f-rel-c").Text = "订单C的反向";
        });
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();

        HostSourceImportOptions options = session.SnapshotCalls().Single().Options;
        Assert.IsTrue(options.ConfirmReverse);
        HostSourceImportDecision[] decisions = options.Decisions
            .OrderBy(decision => decision.FieldId, StringComparer.Ordinal).ToArray();
        Assert.AreEqual(2, decisions.Length, string.Join(";", options.Decisions));
        Assert.AreEqual("tbl-a", decisions[0].TableId);
        Assert.AreEqual("f-rel-a", decisions[0].FieldId);
        Assert.AreEqual("native", decisions[0].Policy);
        Assert.AreEqual("", decisions[0].TargetKind);
        Assert.AreEqual("订单A的反向", decisions[0].TargetName);
        Assert.IsTrue(decisions[0].Confirmed);
        Assert.AreEqual("tbl-c", decisions[1].TableId);
        Assert.AreEqual("f-rel-c", decisions[1].FieldId);
        Assert.AreEqual("native", decisions[1].Policy);
        Assert.AreEqual("订单C的反向", decisions[1].TargetName);
        Assert.AreNotEqual(decisions[0].TargetName, decisions[1].TargetName);

        // Snapshot on a reverse-naming relation must not carry the reverse
        // name: it renames no target column and creates no reciprocal field.
        await fixture.DoAsync(w => Require<ComboBox>(w, "field-policy-tbl-a-f-rel-a").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        decisions = session.SnapshotCalls()[^1].Options.Decisions
            .OrderBy(decision => decision.FieldId, StringComparer.Ordinal).ToArray();
        Assert.AreEqual(2, decisions.Length, string.Join(";", decisions));
        HostSourceImportDecision snapshot = decisions.Single(decision => decision.FieldId == "f-rel-a");
        Assert.AreEqual("snapshot", snapshot.Policy);
        Assert.AreEqual("json", snapshot.TargetKind);
        Assert.AreEqual("", snapshot.TargetName);
        HostSourceImportDecision native = decisions.Single(decision => decision.FieldId == "f-rel-c");
        Assert.AreEqual("native", native.Policy);
        Assert.AreEqual("订单C的反向", native.TargetName);
    }

    [TestMethod]
    public async Task SnapshotTargetChoiceAppliesExactTextAndJsonAndInvalidatesPreview()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWithSingleFieldCatalog(new FakeSourceProvider(log, "prov-kind"), "number");
        await fixture.SettleAsync();
        await fixture.DoAsync(w =>
        {
            Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true;
            Require<ComboBox>(w, "field-policy-tbl-1-f-target").SelectedIndex = 1;
        });
        await fixture.SettleAsync();
        // The explicit snapshot target selector only exists for snapshot rows.
        await fixture.DoAsync(w => Assert.AreEqual(Visibility.Visible,
            Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").Visibility));
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        // Default keeps the source kind (old locked number mapping).
        Assert.AreEqual("number", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);

        // Exact text for float64-precision-losing numbers (9007199254740993).
        await fixture.DoAsync(w => Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual("text", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);

        // JSON for values without an option identity mapping.
        await fixture.DoAsync(w => Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").SelectedIndex = 2);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual("json", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);

        // Leaving snapshot hides the selector again; native emits no decision
        // for a field that does not require a reverse name.
        await fixture.DoAsync(w =>
        {
            Require<ComboBox>(w, "field-policy-tbl-1-f-target").SelectedIndex = 0;
            Assert.AreEqual(Visibility.Collapsed,
                Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").Visibility);
        });
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual(0, session.SnapshotCalls()[^1].Options.Decisions.Length);
    }

    [TestMethod]
    public async Task UnsupportedSheetsStayVisibleWhileDataTablesRemainSelectable()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("wps", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "file-wps-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWithUnsupportedSheets(new FakeSourceProvider(log, "prov-wps-unsupported"),
        [
            new NativeSourceTable("tbl-1", "订单",
                [new NativeSourceField("f-native", "标题", "text", "text")]),
        ],
        [
            new NativeSourceUnsupportedSheet("sheet-dash", "销售看板", "xlDbDashBoardSheet",
                "仪表盘不是数据表，不参与模型迁移"),
            new NativeSourceUnsupportedSheet("sheet-doc", "项目文档", "xlEtFlexPaperSheet",
                "智能文档关联表不是数据表，不参与模型迁移"),
        ]);
        await fixture.SettleAsync();

        string status = await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text);
        StringAssert.Contains(status, "1 张表");
        StringAssert.Contains(status, "另有 2 个来源项不支持迁移，已在下方逐项列出原因。");
        string dashboard = await fixture.ReadAsync(w => Require<TextBlock>(w, "unsupported-sheet-sheet-dash").Text);
        StringAssert.Contains(dashboard, "销售看板");
        StringAssert.Contains(dashboard, "xlDbDashBoardSheet");
        StringAssert.Contains(dashboard, "仪表盘不是数据表，不参与模型迁移");
        string document = await fixture.ReadAsync(w => Require<TextBlock>(w, "unsupported-sheet-sheet-doc").Text);
        StringAssert.Contains(document, "项目文档");
        StringAssert.Contains(document, "智能文档关联表不是数据表，不参与模型迁移");

        // The migratable data table stays fully usable next to the disclosure.
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        // The disclosure survives the precheck pass.
        StringAssert.Contains(
            await fixture.ReadAsync(w => Require<TextBlock>(w, "unsupported-sheet-sheet-dash").Text), "销售看板");
    }

    [TestMethod]
    public async Task UnsupportedSheetsStayVisibleWhenNoDataTablesExist()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        using var fixture = new NativeWizardStaFixture("wps", connect, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "file-wps-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWithUnsupportedSheets(new FakeSourceProvider(log, "prov-wps-empty"),
        [],
        [
            new NativeSourceUnsupportedSheet("sheet-only", "全部是看板的文件", "xlDbDashBoardSheet",
                "仪表盘不是数据表，不参与模型迁移"),
        ]);
        await fixture.SettleAsync();

        string status = await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text);
        StringAssert.Contains(status, "连接成功，当前授权范围内没有数据表。");
        StringAssert.Contains(status, "另有 1 个来源项不支持迁移，已在下方逐项列出原因。");
        string only = await fixture.ReadAsync(w => Require<TextBlock>(w, "unsupported-sheet-sheet-only").Text);
        StringAssert.Contains(only, "全部是看板的文件");
        StringAssert.Contains(only, "仪表盘不是数据表，不参与模型迁移");
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
    }

    [TestMethod]
    public async Task LocalFileCatalogLoadsThroughInjectedOpenerWithoutCloudDelegateOrToken()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session, opener);
        await fixture.ShowAsync();

        // The local path is reachable with no source link and no token; the
        // opener is the only entry and never needs a credential.
        Assert.AreEqual("", await fixture.ReadAsync(w => Require<PasswordBox>(w, "token-input").Password));
        await ClickAsync(fixture, "source-file-button");
        Assert.AreEqual("正在读取本地 .base 文件…",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        Assert.AreEqual(1, opener.Calls);
        FakeSourceProvider provider = new(log, "prov-local");
        opener.CompleteWith(LocalBaseConnection(provider, () => log.Add("local-connection:released")));
        await fixture.SettleAsync();

        // The cloud HTTP delegate was never invoked and no token or signature
        // was requested: the catalog arrives only through the opener.
        Assert.AreEqual(0, connect.SnapshotCalls().Count);
        Assert.IsFalse(log.Contains("connect:enter"));
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text), "1 张表");
        // A notice-free local file opens no acknowledgement gate at all.
        Assert.AreEqual(Visibility.Collapsed,
            await fixture.ReadAsync(w => Require<CheckBox>(w, "notice-acknowledge-checkbox").Visibility));

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        PrepareCall prepare = session.SnapshotCalls().Single();
        Assert.AreSame(provider, prepare.Provider);
        AssertNoEcho(JsonSerializer.Serialize(prepare.Options));
        CollectionAssert.AreEqual(new[] { "tbl-1" }, prepare.Options.SelectedTableIds);
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
    }

    [TestMethod]
    public async Task CancelledLocalFileSelectionIsSilentAndNeverPrepares()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session, opener);
        await fixture.ShowAsync();
        await ClickAsync(fixture, "source-file-button");
        opener.CompleteWithCancel(); // null: the user dismissed the picker.
        await fixture.SettleAsync();

        // Cancellation is neither an error nor a write: no precheck, no cloud
        // delegate, no credential consumption and the window stays open.
        Assert.AreEqual("已取消选择本地 .base 文件，未导入任何内容。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        Assert.AreEqual(1, opener.Calls);
        Assert.AreEqual(0, connect.SnapshotCalls().Count);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.IsFalse(log.Contains("session:prepare-enter"));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);
        Assert.AreEqual("", await fixture.ReadAsync(w => Require<PasswordBox>(w, "token-input").Password));

        // WPS never exposes the local entry, even with an opener injected.
        using var wpsFixture = new NativeWizardStaFixture("wps",
            new FakeConnect(log), new FakeWizardSession(log), new FakeLocalFileOpener(log));
        await wpsFixture.ShowAsync();
        Assert.IsNull(await wpsFixture.ReadAsync(w => FindByAutomationId<Button>(w, "source-file-button")));
    }

    [TestMethod]
    public async Task LocalFileNoticesRequireAcknowledgementBeforePreview()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session, opener);
        await fixture.ShowAsync();
        await ClickAsync(fixture, "source-file-button");
        opener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-notices"),
            () => log.Add("local-connection:released"),
        [
            "文件包含 2 个仪表盘：仪表盘不会随导入迁移",
            "文件包含 1 个工作流：工作流（自动化）不会随导入迁移",
            "表「订单」字段「总额」是公式：源文件未导出计算值，不会迁入计算值",
        ]));
        await fixture.SettleAsync();

        // Every notice stays visible next to the catalog with an explicit
        // acknowledgement gate; nothing is silently dropped or over-claimed.
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "notice-0").Text), "仪表盘");
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "notice-1").Text), "工作流");
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "notice-2").Text), "不会迁入计算值");
        Assert.AreEqual(Visibility.Visible,
            await fixture.ReadAsync(w => Require<CheckBox>(w, "notice-acknowledge-checkbox").Visibility));

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        await ClickAsync(fixture, "preview-button"); // RaiseEvent bypasses IsEnabled.
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text), "告知");

        await fixture.DoAsync(w => Require<CheckBox>(w, "notice-acknowledge-checkbox").IsChecked = true);
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        // Retracting the acknowledgement invalidates the confirmed preview.
        await fixture.DoAsync(w => Require<CheckBox>(w, "notice-acknowledge-checkbox").IsChecked = false);
        await AssertConfirmationInvalidatedAsync(fixture);
    }

    [TestMethod]
    public async Task SwitchingToLocalFileInvalidatesCloudPreviewAndPreparesWithNewProvider()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session, opener);
        await fixture.ShowAsync();
        FakeSourceProvider cloudProvider = new(log, "prov-cloud");
        await ConnectAndSelectFirstTableAsync(fixture, connect, cloudProvider);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await ClickAsync(fixture, "source-file-button");
        FakeSourceProvider localProvider = new(log, "prov-local-switch");
        opener.CompleteWith(LocalBaseConnection(localProvider, () => log.Add("local-connection:released")));
        await fixture.SettleAsync();

        // The cloud preview cannot survive the source switch: the cloud
        // connection is released and the stale preview neither confirms nor
        // starts anything.
        Assert.AreEqual(1, opener.Calls);
        Assert.AreEqual(1, connect.SnapshotCalls().Count);
        Assert.AreEqual(1, connect.Releases);
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        await ClickAsync(fixture, "start-button");
        await fixture.SettleAsync();
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);
        Assert.IsTrue((await fixture.ReadAsync(w => w.Result)).Cancelled);

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        // The new precheck runs against the local file's provider only; the
        // cloud token/provider is not reused for the new source.
        Assert.AreSame(localProvider, session.SnapshotCalls()[^1].Provider);
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
    }

    [TestMethod]
    public async Task ClosingWhileOpeningLocalFileReleasesLateConnectionWithoutAdopting()
    {
        var log = new EventLog();
        var connect = new FakeConnect(log);
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture("feishu", connect, session, opener);
        await fixture.ShowAsync();
        await ClickAsync(fixture, "source-file-button"); // Gate stays open: file read in flight.
        await fixture.DoAsync(w => w.Close()); // Busy: close deferred, lifetime cancelled.

        // The opener ignores the token and completes with a real connection.
        int releases = 0;
        opener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-late"),
            () => releases++));
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        // The late connection is released exactly once, never adopted into
        // the catalog, and the precheck never starts.
        Assert.AreEqual(1, releases);
        Assert.AreEqual(0, connect.SnapshotCalls().Count);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.AreEqual(1, session.Disposals);
        Assert.AreEqual(0, await fixture.ReadAsync(w => Require<StackPanel>(w, "tables-panel").Children.Count));
        Assert.AreEqual("读取本地 .base 文件已取消。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
    }

    [TestMethod]
    public async Task WindowCompositionRendersRealContentAndOptInScreenshotsStayInsideBuildTree()
    {
        // Feishu scenario only: synthetic Feishu catalog/plan data — never a
        // WPS sheet type mixed in. Expanded strategy area with the snapshot
        // target selector and reverse-name row, then the precheck report.
        var feishuLog = new EventLog();
        var feishuConnect = new FakeConnect(feishuLog);
        var feishuSession = new FakeWizardSession(feishuLog);
        Capture catalog;
        Capture precheck;
        using (var fixture = new NativeWizardStaFixture("feishu", feishuConnect, feishuSession))
        {
            // Test-only presentation size so controls and report fit the
            // capture; the product default stays 840x760.
            await fixture.DoAsync(w => { w.Width = 880; w.Height = 1080; });
            await fixture.ShowAsync();
            await fixture.DoAsync(w =>
            {
                Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
                Require<PasswordBox>(w, "token-input").Password = SecretToken;
            });
            await ClickAsync(fixture, "connect-button");
            feishuConnect.CompleteWithRelationCatalog(new FakeSourceProvider(feishuLog, "prov-shot"));
            await fixture.SettleAsync();
            await fixture.DoAsync(w =>
            {
                Require<CheckBox>(w, "table-select-tbl-a").IsChecked = true;
                Require<CheckBox>(w, "table-select-tbl-b").IsChecked = true;
                Require<ComboBox>(w, "field-policy-tbl-a-f-title-a").SelectedIndex = 1;
                Require<TextBox>(w, "field-reverse-name-tbl-a-f-rel-a").Text = "客户B的订单A";
            });
            // Expand the first table's strategy expander and scroll the
            // snapshot/reverse controls into the viewport.
            await fixture.DoAsync(w => RequireType<Expander>(w).IsExpanded = true);
            await fixture.SettleAsync();
            await fixture.DoAsync(w => ScrollAnchorIntoView(w,
                Require<TextBox>(w, "field-reverse-name-tbl-a-f-rel-a"), 140));
            await fixture.SettleAsync();
            // Catalog capture: strategy rows with the snapshot target selector
            // and the reverse-name row visible.
            catalog = await fixture.ReadAsync(CaptureWindow);

            await ClickAsync(fixture, "preview-button");
            feishuSession.CompletePending();
            await fixture.SettleAsync();
            Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
            await fixture.DoAsync(w => ScrollAnchorIntoView(w, Require<TextBlock>(w, "report-text"), 24));
            await fixture.SettleAsync();
            // Precheck capture: the report itself; the footer buttons are
            // docked outside the ScrollViewer and stay visible with it.
            precheck = await fixture.ReadAsync(CaptureWindow);
            await ClickAsync(fixture, "start-button");
            await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        }

        // Separate WPS scenario: the typed unsupported-sheet disclosure with
        // WPS-native sheet types next to a still-selectable data table.
        var wpsLog = new EventLog();
        var wpsConnect = new FakeConnect(wpsLog);
        Capture unsupported;
        using (var fixture = new NativeWizardStaFixture("wps", wpsConnect, new FakeWizardSession(wpsLog)))
        {
            await fixture.DoAsync(w => { w.Width = 880; w.Height = 1080; });
            await fixture.ShowAsync();
            await fixture.DoAsync(w =>
            {
                Require<TextBox>(w, "source-input").Text = "file-wps-native-test";
                Require<PasswordBox>(w, "token-input").Password = SecretToken;
            });
            await ClickAsync(fixture, "connect-button");
            wpsConnect.CompleteWithUnsupportedSheets(new FakeSourceProvider(wpsLog, "prov-shot-wps"),
                [new NativeSourceTable("tbl-1", "订单",
                    [new NativeSourceField("f-native", "标题", "text", "text")])],
            [
                new NativeSourceUnsupportedSheet("sheet-dash", "销售看板", "xlDbDashBoardSheet",
                    "仪表盘不是数据表，不参与模型迁移"),
            ]);
            await fixture.SettleAsync();
            await fixture.DoAsync(w =>
            {
                Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true;
                ScrollAnchorIntoView(w, Require<StackPanel>(w, "unsupported-panel"), 96);
            });
            await fixture.SettleAsync();
            unsupported = await fixture.ReadAsync(CaptureWindow);
        }

        // Synthetic local .base scenario: a fabricated .local catalog — never
        // the user's real sample file — with the full non-table disclosure
        // set (workflows/dashboards/views, attachment reference snapshots,
        // unexported formula values) next to a still-selectable data table.
        var localLog = new EventLog();
        var localConnect = new FakeConnect(localLog);
        var localOpener = new FakeLocalFileOpener(localLog);
        Capture localNotices;
        using (var fixture = new NativeWizardStaFixture("feishu", localConnect,
            new FakeWizardSession(localLog), localOpener))
        {
            await fixture.DoAsync(w => { w.Width = 880; w.Height = 1080; });
            await fixture.ShowAsync();
            await ClickAsync(fixture, "source-file-button");
            localOpener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(localLog, "prov-shot-local"),
                () => localLog.Add("local-connection:released"),
            [
                "文件包含 1 个工作流：工作流（自动化）不会随导入迁移",
                "文件包含 2 个仪表盘：仪表盘不会随导入迁移",
                "文件包含 3 个视图：视图配置不会随导入迁移",
                "附件字段仅保留引用快照：附件字节不会下载或迁入",
                "表「订单」字段「总额」是公式：源文件未导出计算值，不会迁入计算值",
            ], name: "合成示例.local.base"));
            await fixture.SettleAsync();
            await fixture.DoAsync(w =>
            {
                Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true;
                ScrollAnchorIntoView(w, Require<StackPanel>(w, "notices-panel"), 96);
            });
            await fixture.SettleAsync();
            // Gated state capture: notices disclosed, acknowledgement still
            // unchecked, preflight therefore unavailable.
            localNotices = await fixture.ReadAsync(CaptureWindow);
        }

        foreach ((string name, Capture capture) in new[]
        {
            ("catalog", catalog), ("precheck", precheck), ("wps-unsupported", unsupported),
            ("local-notices", localNotices),
        })
        {
            Assert.IsTrue(capture.Width > 600 && capture.Height > 500,
                $"{name} capture {capture.Width}x{capture.Height} is not the composed wizard");
            Assert.IsTrue(capture.ContentPixels > 100,
                $"{name} capture only painted {capture.ContentPixels} content pixels");
        }

        // Disk artifacts are strictly opt-in and only ever target this tree's
        // ignored build/automation/native-wizard directory.
        string? directory = ResolveOptInScreenshotDirectory();
        if (directory is null) return;
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-catalog.png"), catalog.Png);
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-precheck.png"), precheck.Png);
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-wps-unsupported.png"), unsupported.Png);
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-local-notices.png"), localNotices.Png);
    }

    private static async Task AssertConfirmationInvalidatedAsync(NativeWizardStaFixture fixture)
    {
        await fixture.SettleAsync();
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.AreEqual("配置已变化，请重新预检。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
    }

    private static async Task ConnectAndSelectFirstTableAsync(
        NativeWizardStaFixture fixture, FakeConnect connect, FakeSourceProvider provider)
    {
        await fixture.DoAsync(w =>
        {
            Require<TextBox>(w, "source-input").Text = "https://feishu.test/base/app-native-test";
            Require<PasswordBox>(w, "token-input").Password = SecretToken;
        });
        await ClickAsync(fixture, "connect-button");
        connect.CompleteWith(provider);
        await fixture.SettleAsync();
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
    }

    /// <summary>Synthetic local .base catalog exactly as the MainWindow
    /// opener hands it over: no token, no signature, no file path in the
    /// window, optional non-table notices.</summary>
    private static NativeSourceConnection LocalBaseConnection(
        FakeSourceProvider provider, Action release, string[]? notices = null, string name = "本地 base 文件") => new(
        name,
        [new NativeSourceTable("tbl-1", "订单", [new NativeSourceField("f-title", "标题", "text", "text")])],
        _ => provider, release, Notices: notices);

    private static Task ClickAsync(NativeWizardStaFixture fixture, string automationId) =>
        fixture.DoAsync(w => Require<Button>(w, automationId)
            .RaiseEvent(new RoutedEventArgs(Button.ClickEvent)));

    private static T RequireType<T>(SourceImportWindow window) where T : DependencyObject
        => FindByType<T>(window) ?? throw new AssertFailedException(
            $"No {typeof(T).Name} in the native wizard tree.");

    private static T? FindByType<T>(DependencyObject node) where T : DependencyObject
    {
        if (node is T typed) return typed;
        foreach (object child in LogicalTreeHelper.GetChildren(node))
            if (child is DependencyObject childNode && FindByType<T>(childNode) is { } match)
                return match;
        return null;
    }

    /// <summary>Scrolls the wizard's ScrollViewer so the anchor (with a lead
    /// margin above it) sits inside the viewport for composition captures.</summary>
    private static void ScrollAnchorIntoView(SourceImportWindow window, FrameworkElement anchor, double lead)
    {
        ScrollViewer viewer = RequireType<ScrollViewer>(window);
        if (viewer.Content is not FrameworkElement content)
            throw new AssertFailedException("Native wizard ScrollViewer has no scrollable content.");
        double offset = anchor.TransformToAncestor(content).Transform(new Point(0, 0)).Y;
        viewer.ScrollToVerticalOffset(Math.Max(0, offset - lead));
        window.UpdateLayout();
    }

    private static T Require<T>(SourceImportWindow window, string automationId) where T : FrameworkElement
        => FindByAutomationId<T>(window, automationId) ?? throw new AssertFailedException(
            $"No {typeof(T).Name} with AutomationId '{automationId}' in the native wizard tree.");

    private static T? FindByAutomationId<T>(DependencyObject node, string automationId) where T : FrameworkElement
    {
        if (node is FrameworkElement element && element is T typed
            && string.Equals(element.GetValue(AutomationProperties.AutomationIdProperty) as string,
                automationId, StringComparison.Ordinal))
            return typed;
        foreach (object child in LogicalTreeHelper.GetChildren(node))
            if (child is DependencyObject childNode
                && FindByAutomationId<T>(childNode, automationId) is { } match)
                return match;
        return null;
    }

    private sealed record Capture(int Width, int Height, int ContentPixels, byte[] Png);

    private static Capture CaptureWindow(SourceImportWindow window)
    {
        window.UpdateLayout();
        DpiScale dpi = VisualTreeHelper.GetDpi(window);
        int width = Math.Max(1, (int)Math.Ceiling(window.ActualWidth * dpi.DpiScaleX));
        int height = Math.Max(1, (int)Math.Ceiling(window.ActualHeight * dpi.DpiScaleY));
        var bitmap = new RenderTargetBitmap(width, height,
            dpi.PixelsPerInchX, dpi.PixelsPerInchY, PixelFormats.Pbgra32);
        bitmap.Render(window);
        bitmap.Freeze();
        int stride = width * 4;
        var pixels = new byte[checked(stride * height)];
        bitmap.CopyPixels(pixels, stride, 0);
        int contentPixels = 0;
        for (int offset = 0; offset < pixels.Length; offset += 4)
            if (pixels[offset] != byte.MaxValue || pixels[offset + 1] != byte.MaxValue
                || pixels[offset + 2] != byte.MaxValue)
                contentPixels++;
        var encoder = new PngBitmapEncoder();
        encoder.Frames.Add(BitmapFrame.Create(bitmap));
        using var stream = new MemoryStream();
        encoder.Save(stream);
        return new(width, height, contentPixels, stream.ToArray());
    }

    /// <summary>
    /// Resolves the opt-in screenshot directory from
    /// VIBETABLE_NATIVE_WIZARD_SCREENSHOTS. Unset means "keep the tree clean";
    /// any other value must resolve inside this repository tree's ignored
    /// build/automation/native-wizard directory or the test fails closed.
    /// </summary>
    private static string? ResolveOptInScreenshotDirectory()
    {
        string? requested = Environment.GetEnvironmentVariable("VIBETABLE_NATIVE_WIZARD_SCREENSHOTS");
        if (string.IsNullOrWhiteSpace(requested)) return null;
        DirectoryInfo? candidate = new(AppContext.BaseDirectory);
        while (candidate is not null && !File.Exists(Path.Combine(candidate.FullName, "AGENTS.md")))
            candidate = candidate.Parent;
        Assert.IsNotNull(candidate,
            "Repository root with AGENTS.md was not found above the test output directory.");
        string allowedRoot = Path.GetFullPath(
            Path.Combine(candidate.FullName, "build", "automation", "native-wizard"))
            + Path.DirectorySeparatorChar;
        string requestedRoot = Path.GetFullPath(requested.Trim());
        if (!requestedRoot.EndsWith(Path.DirectorySeparatorChar))
            requestedRoot += Path.DirectorySeparatorChar;
        Assert.IsTrue(requestedRoot.StartsWith(allowedRoot, StringComparison.OrdinalIgnoreCase),
            $"Native wizard screenshots may only target {allowedRoot}; refusing {requestedRoot}.");
        string directory = requestedRoot.TrimEnd(Path.DirectorySeparatorChar);
        Directory.CreateDirectory(directory);
        return directory;
    }

    private sealed class NativeWizardStaFixture : IDisposable
    {
        internal static readonly TimeSpan WaitBudget = TimeSpan.FromSeconds(15);

        private readonly Thread _thread;
        private readonly TaskCompletionSource _constructed =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private Exception? _constructionFailure;
        private Dispatcher? _dispatcher;
        private SourceImportWindow? _window;

        internal TaskCompletionSource Closed { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        internal SourceImportWindow Window =>
            _window ?? throw new AssertFailedException("Native wizard window was not constructed.");

        internal Dispatcher Dispatcher =>
            _dispatcher ?? throw new AssertFailedException("Native wizard dispatcher was not constructed.");

        internal NativeWizardStaFixture(string provider, FakeConnect connect, FakeWizardSession session,
            FakeLocalFileOpener? openLocalFile = null)
        {
            _thread = new Thread(() =>
            {
                SourceImportWindow? window = null;
                Exception? failure = null;
                try
                {
                    _dispatcher = Dispatcher.CurrentDispatcher;
                    window = new SourceImportWindow(provider, connect.Invoke, session,
                        DescribeSafe, CancellationToken.None, openLocalFile is null ? null : openLocalFile.Invoke);
                    // Composition-only presentation: no taskbar entry, no
                    // focus stealing and parked far off-screen.
                    window.WindowStartupLocation = WindowStartupLocation.Manual;
                    window.Left = -32000;
                    window.Top = -32000;
                    window.ShowInTaskbar = false;
                    window.ShowActivated = false;
                    window.Closed += (_, _) => Closed.TrySetResult();
                    _window = window;
                }
                catch (Exception error) { failure = error; }
                finally
                {
                    _constructionFailure = failure;
                    _constructed.TrySetResult();
                }
                if (failure is not null) return;
                try { Dispatcher.Run(); }
                finally
                {
                    try { window?.Close(); }
                    catch (Exception) { /* Best effort unwind after shutdown. */ }
                }
            })
            { IsBackground = true, Name = "vibetable-native-wizard-tests" };
            _thread.SetApartmentState(ApartmentState.STA);
            _thread.Start();
            if (!_constructed.Task.Wait(TimeSpan.FromSeconds(30)))
                Assert.Fail("Native wizard window construction timed out.");
            if (_constructionFailure is not null)
                System.Runtime.ExceptionServices.ExceptionDispatchInfo
                    .Capture(_constructionFailure).Throw();
        }

        internal async Task ShowAsync()
        {
            await DoAsync(w => w.Show());
            await SettleAsync();
        }

        internal async Task DoAsync(Action<SourceImportWindow> action)
        {
            DispatcherOperation operation = Dispatcher.InvokeAsync(() => action(Window));
            await operation.Task.WaitAsync(WaitBudget);
        }

        internal async Task<T> ReadAsync<T>(Func<SourceImportWindow, T> read)
        {
            DispatcherOperation<T> operation = Dispatcher.InvokeAsync(() => read(Window));
            return await operation.Task.WaitAsync(WaitBudget);
        }

        /// <summary>Drains the dispatcher until every queued continuation
        /// above idle priority has run, without any timing sleeps.</summary>
        internal async Task SettleAsync()
        {
            DispatcherOperation idle =
                Dispatcher.InvokeAsync(() => { }, DispatcherPriority.ApplicationIdle);
            await idle.Task.WaitAsync(WaitBudget);
        }

        public void Dispose()
        {
            // Close before shutdown so the window's own disposal contract runs
            // on its dispatcher when a test forgot to close it.
            try
            {
                Dispatcher.BeginInvoke(new Action(() =>
                {
                    try { if (!Closed.Task.IsCompleted) Window.Close(); }
                    catch (Exception) { /* Keep shutdown going. */ }
                }), DispatcherPriority.Normal);
            }
            catch (Exception) { /* Dispatcher already down. */ }
            try { Dispatcher.BeginInvokeShutdown(DispatcherPriority.Background); }
            catch (Exception) { /* Dispatcher already down. */ }
            if (!_thread.Join(TimeSpan.FromSeconds(10)))
                Assert.Fail("Native wizard STA thread did not shut down within the budget.");
        }
    }

    private sealed class EventLog
    {
        private readonly object _gate = new();
        private readonly List<string> _entries = [];

        internal void Add(string entry) { lock (_gate) _entries.Add(entry); }
        internal IReadOnlyList<string> Snapshot() { lock (_gate) return _entries.ToArray(); }
        internal int IndexOf(string entry) { lock (_gate) return _entries.IndexOf(entry); }
        internal bool Contains(string entry) => IndexOf(entry) >= 0;
    }

    internal sealed record ConnectCall(string Source, string AccessToken, string AccessKey, string Secret);

    private sealed class FakeConnect(EventLog log)
    {
        private readonly object _gate = new();
        internal TaskCompletionSource<ConnectCall> Started { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource Cancelled { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource<NativeSourceConnection>? Pending { get; private set; }
        private readonly List<ConnectCall> _calls = [];
        internal int Releases;
        internal Exception? Failure;
        internal FakeSourceProvider? Provider { get; private set; }

        internal IReadOnlyList<ConnectCall> SnapshotCalls() { lock (_gate) return _calls.ToArray(); }

        internal async Task<NativeSourceConnection> Invoke(string source, string accessToken,
            string accessKey, string secret, CancellationToken token)
        {
            var completion = new TaskCompletionSource<NativeSourceConnection>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            var call = new ConnectCall(source, accessToken, accessKey, secret);
            lock (_gate)
            {
                _calls.Add(call);
                Pending = completion;
            }
            log.Add("connect:enter");
            Started.TrySetResult(call);
            using CancellationTokenRegistration registration = token.Register(() =>
            {
                log.Add("connect:cancel-observed");
                Cancelled.TrySetResult();
                completion.TrySetCanceled(token);
            });
            NativeSourceConnection connection = await completion.Task;
            if (Failure is not null) throw Failure;
            return connection;
        }

        internal void CompleteWith(FakeSourceProvider provider)
        {
            Provider = provider;
            Pending!.TrySetResult(new NativeSourceConnection(
                "飞书测试空间",
                [
                    new NativeSourceTable("tbl-1", "订单",
                    [
                        new NativeSourceField("f-title", "标题", "text", "string"),
                        new NativeSourceField("f-amount", "金额", "number", "number"),
                    ]),
                    new NativeSourceTable("tbl-2", "客户",
                        [new NativeSourceField("f-name", "名称", "text", "string")]),
                ],
                _ => provider,
                () => { Releases++; log.Add("connection:released"); }));
        }

        internal void CompleteWithSingleFieldCatalog(FakeSourceProvider provider, string valueKind)
        {
            Provider = provider;
            Pending!.TrySetResult(new NativeSourceConnection(
                "飞书测试空间",
                [
                    new NativeSourceTable("tbl-1", "订单",
                    [
                        new NativeSourceField("f-native", "标题", "text", "text"),
                        new NativeSourceField("f-target", "策略字段", "text", valueKind),
                    ]),
                ],
                _ => provider,
                () => { Releases++; log.Add("connection:released"); }));
        }

        internal void CompleteWithRelationCatalog(FakeSourceProvider provider)
        {
            Provider = provider;
            Pending!.TrySetResult(new NativeSourceConnection(
                "飞书测试空间",
                [
                    new NativeSourceTable("tbl-a", "订单A",
                    [
                        new NativeSourceField("f-title-a", "标题A", "text", "text"),
                        new NativeSourceField("f-rel-a", "关联B", "relation", "relation", RequiresReverseName: true),
                    ]),
                    new NativeSourceTable("tbl-c", "订单C",
                    [
                        new NativeSourceField("f-title-c", "标题C", "text", "text"),
                        new NativeSourceField("f-rel-c", "关联B", "relation", "relation", RequiresReverseName: true),
                    ]),
                    new NativeSourceTable("tbl-b", "客户B", [new NativeSourceField("f-name-b", "名称", "text", "text")]),
                ],
                _ => provider,
                () => { Releases++; log.Add("connection:released"); }));
        }

        internal void CompleteWithUnsupportedSheets(FakeSourceProvider provider,
            NativeSourceTable[] tables, NativeSourceUnsupportedSheet[] unsupportedSheets)
        {
            Provider = provider;
            Pending!.TrySetResult(new NativeSourceConnection("WPS 测试文件", tables,
                _ => provider,
                () => { Releases++; log.Add("connection:released"); }, unsupportedSheets));
        }
    }

    /// <summary>Fake .base opener mirroring the MainWindow-injected delegate:
    /// gated at its await boundary, fully offline — no file dialog, no file
    /// path, no HTTP, so tests never touch the user's real sample.</summary>
    private sealed class FakeLocalFileOpener(EventLog log)
    {
        private readonly object _gate = new();
        internal TaskCompletionSource<NativeSourceConnection?>? Pending { get; private set; }
        internal int Calls { get; private set; }

        internal Task<NativeSourceConnection?> Invoke(CancellationToken token)
        {
            var completion = new TaskCompletionSource<NativeSourceConnection?>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            lock (_gate) { Calls++; Pending = completion; }
            log.Add("local-file:enter");
            return completion.Task;
        }

        internal void CompleteWith(NativeSourceConnection connection) => Pending!.TrySetResult(connection);
        internal void CompleteWithCancel() => Pending!.TrySetResult(null);
    }

    internal sealed record PrepareCall(IHostSourceImportProvider Provider, HostSourceImportOptions Options);

    private sealed class FakeWizardSession(EventLog log) : IHostSourceImportWizardSession
    {
        private readonly object _gate = new();
        private int _sequence;
        private readonly List<PrepareCall> _calls = [];
        internal TaskCompletionSource Cancelled { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource<HostSourceImportPreview>? Pending { get; private set; }
        internal int Disposals;
        internal bool CanApply = true;
        internal string TaskId = "task-native-1";
        internal Exception? PrepareFailure;
        internal Exception? StartFailure;
        internal HostSourceImportPreview? StartedPreview { get; private set; }
        private HostSourceImportPreview? _current;
        private IHostSourceImportProvider? _pendingProvider;

        internal IReadOnlyList<PrepareCall> SnapshotCalls() { lock (_gate) return _calls.ToArray(); }

        public async Task<HostSourceImportPreview> PrepareAsync(IHostSourceImportProvider provider,
            HostSourceImportOptions options, CancellationToken token)
        {
            var completion = new TaskCompletionSource<HostSourceImportPreview>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            lock (_gate)
            {
                _sequence = _calls.Count + 1;
                _calls.Add(new PrepareCall(provider, options));
                Pending = completion;
            }
            log.Add("session:prepare-enter");
            token.ThrowIfCancellationRequested();
            try
            {
                using CancellationTokenRegistration registration = token.Register(() =>
                {
                    log.Add("session:prepare-cancel-observed");
                    Cancelled.TrySetResult();
                    completion.TrySetCanceled(token);
                });
                HostSourceImportPreview preview = await completion.Task;
                if (PrepareFailure is not null) throw PrepareFailure;
                lock (_gate)
                {
                    _current = preview;
                    _pendingProvider = provider;
                }
                log.Add("session:prepare-succeeded");
                return preview;
            }
            catch
            {
                // Mirrors the trusted HostSourceImportWizardSession contract:
                // a cancelled or failed prepare releases the handed-off
                // provider; a started one stays owned by the registry.
                provider.Dispose();
                log.Add("session:provider-discarded");
                throw;
            }
        }

        internal HostSourceImportPreview CompletePending()
        {
            HostSourceImportPreview preview = new(
                $"provider-session-{_sequence}", $"plan-token-{_sequence}",
                DateTimeOffset.UtcNow.AddMinutes(5), BuildPlan(CanApply));
            Pending!.TrySetResult(preview);
            return preview;
        }

        internal static JsonElement BuildPlan(bool canApply) => JsonSerializer.SerializeToElement(new
        {
            canApply,
            provider = "feishu",
            containerId = "app-native-test",
            displayName = "飞书测试空间",
            version = "v3",
            tables = new object[]
            {
                new { name = "订单", recordCount = 12, sourceId = "tbl-1", version = "v3" },
            },
            diagnostics = new object[]
            {
                new
                {
                    blocking = !canApply,
                    tableId = "tbl-1",
                    message = canApply ? "字段映射已自动生成" : "目标名称与现有表冲突",
                },
            },
        });

        public string Start(HostSourceImportPreview preview)
        {
            lock (_gate)
            {
                if (StartFailure is not null) throw StartFailure;
                if (!ReferenceEquals(preview, _current))
                    throw new InvalidOperationException("Source preview is no longer current.");
                StartedPreview = preview;
                _current = null;
                _pendingProvider = null;
            }
            log.Add("session:started");
            return TaskId;
        }

        public void DiscardPreview()
        {
            lock (_gate)
            {
                if (_pendingProvider is { } pending)
                {
                    pending.Dispose();
                    log.Add("session:pending-discarded");
                }
                _current = null;
                _pendingProvider = null;
            }
            log.Add("session:discard-preview");
        }

        public void Dispose()
        {
            lock (_gate)
            {
                if (_pendingProvider is { } pending)
                {
                    pending.Dispose();
                    log.Add("session:pending-discarded");
                }
                _pendingProvider = null;
                _current = null;
            }
            Disposals++;
            log.Add("session:disposed");
        }
    }

    private sealed class FakeSourceProvider(EventLog log, string id) : IHostSourceImportProvider
    {
        private int _disposeCount;
        internal int DisposeCount => Volatile.Read(ref _disposeCount);

        public void Dispose()
        {
            Interlocked.Increment(ref _disposeCount);
            log.Add($"provider:{id}:disposed");
        }

        public Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<HostSourceImportObservation> ObserveAsync(CancellationToken token) => throw new NotSupportedException();
        public Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token)
            => throw new NotSupportedException();
    }
}
