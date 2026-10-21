using System.IO;
using System.Text.Json;
using System.Threading;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Threading;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Composition-level regression for the trusted native .base import window.
/// Every test drives the real <see cref="SourceImportWindow"/> on a dedicated
/// STA dispatcher; the file opener and wizard session are gated fakes
/// completing through TaskCompletionSource, so every await boundary stays
/// real while the test stays deterministic without sleeps. The parked window
/// is off-screen with no taskbar entry and never activates, so local runs
/// stay undisturbed.
/// </summary>
[TestClass]
public sealed class SourceImportWindowTests
{
    private static string DescribeSafe(Exception error) => "SAFE-" + error.GetType().Name;

    [TestMethod]
    public async Task LoadedOpensPickerDirectlyThenPreviewConfirmReturnsTaskId()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();

        // The web click opens this window for one purpose: the system .base
        // picker opens itself on load; no credential input exists at all. The
        // opener receives this window as the picker's explicit owner.
        Assert.AreEqual(1, opener.Calls);
        CollectionAssert.AreEqual(new[] { fixture.Window }, opener.Owners,
            "窗口必须作为 picker 的显式 owner 传入");
        Assert.IsNull(await fixture.ReadAsync(w => FindByAutomationId<TextBox>(w, "token-input")));
        Assert.IsNull(await fixture.ReadAsync(w => FindByAutomationId<Button>(w, "connect-button")));
        FakeSourceProvider provider = new(log, "prov-local");
        opener.CompleteWith(LocalBaseConnection(provider, () => log.Add("local-connection:released")));
        await fixture.SettleAsync();

        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text), "2 张表");
        Assert.IsNotNull(await fixture.ReadAsync(w => Require<ScrollViewer>(w, "source-import-scroll")));
        Assert.IsNotNull(await fixture.ReadAsync(w => Require<Expander>(w, "table-policy-tbl-1")));
        // A relation-free local file keeps the reciprocal-field gate hidden.
        Assert.AreEqual(Visibility.Collapsed,
            await fixture.ReadAsync(w => Require<CheckBox>(w, "reverse-checkbox").Visibility));
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        PrepareCall prepare = session.SnapshotCalls().Single();
        Assert.AreSame(provider, prepare.Provider);
        CollectionAssert.AreEqual(new[] { "tbl-1" }, prepare.Options.SelectedTableIds);
        Assert.AreEqual("订单", prepare.Options.TargetNames.Single().Name);
        Assert.AreEqual(0, prepare.Options.Decisions.Length);
        Assert.IsFalse(prepare.Options.ConfirmReverse);
        HostSourceImportPreview preview = session.CompletePending();
        await fixture.SettleAsync();

        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text), "订单：12 条记录");
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        HostSourceImportOpenResult result = await fixture.ReadAsync(w => w.Result);
        Assert.IsFalse(result.Cancelled);
        Assert.AreEqual("task-native-1", result.TaskId);
        Assert.AreSame(preview, session.StartedPreview);
        Assert.AreEqual(1, session.Disposals);
        // The registry owns the submitted provider; closing the window must
        // neither discard nor dispose it.
        Assert.AreEqual(0, provider.DisposeCount);
    }

    [TestMethod]
    public async Task CancelledPickerKeepsWindowOpenWithZeroWrites()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();

        opener.CompleteWithCancel(); // The user dismissed the system picker.
        await fixture.SettleAsync();

        // Cancellation is neither an error nor a write: no precheck ran and
        // the window stays open for another attempt.
        Assert.AreEqual("已取消选择本地 .base 文件，未导入任何内容。可重新选择文件或关闭窗口。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
        Assert.AreEqual(1, opener.Calls);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);

        // Re-selecting through the button works after a cancelled picker.
        await ClickAsync(fixture, "source-file-button");
        opener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-second"),
            () => log.Add("local-connection:released")));
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text), "2 张表");

        // Cancelling a re-selection keeps the already loaded catalog.
        await ClickAsync(fixture, "source-file-button");
        opener.CompleteWithCancel();
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text),
            "仍使用当前已读取的内容");
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));
    }

    [TestMethod]
    public async Task SwitchingFilesInvalidatesPreviewAndPreparesWithNewProvider()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        FakeSourceProvider firstProvider = new(log, "prov-first");
        int firstReleases = 0;
        opener.CompleteWith(LocalBaseConnection(firstProvider, () => firstReleases++));
        await fixture.SettleAsync();
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        await ClickAsync(fixture, "source-file-button");
        FakeSourceProvider secondProvider = new(log, "prov-second");
        opener.CompleteWith(LocalBaseConnection(secondProvider, () => { }));
        await fixture.SettleAsync();

        // The old preview cannot survive the file switch: the old provider is
        // released and the stale preview neither confirms nor starts anything.
        Assert.AreEqual(1, firstReleases);
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
        Assert.AreSame(secondProvider, session.SnapshotCalls()[^1].Provider);
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
    }

    [TestMethod]
    public async Task EditingSelectionNamePolicyOrReverseInvalidatesConfirmationUntilRechecked()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-edit"), [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-title", "标题", "text", "text"),
                new NativeSourceField("f-amount", "金额", "number", "number"),
            ]),
        ]));
        await fixture.SettleAsync();
        await SelectFirstTableAsync(fixture);

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
        Assert.AreEqual("订单_import", session.SnapshotCalls()[^1].Options.TargetNames.Single().Name);

        await fixture.DoAsync(w => Require<ComboBox>(w, "field-policy-tbl-1-f-amount").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        HostSourceImportDecision decision = session.SnapshotCalls()[^1].Options.Decisions.Single();
        Assert.AreEqual("snapshot", decision.Policy);
        Assert.AreEqual("number", decision.TargetKind);
        Assert.IsTrue(decision.Confirmed);

        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        Assert.AreEqual("task-native-1", (await fixture.ReadAsync(w => w.Result)).TaskId);
    }

    [TestMethod]
    public async Task ClosingWhileOpeningReleasesLateConnectionWithoutAdopting()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        await fixture.DoAsync(w => w.Close()); // Busy: close deferred, lifetime cancelled.

        // The opener ignores the token and completes with a real connection.
        int releases = 0;
        opener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-late"), () => releases++));
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        // The late connection is released exactly once, never adopted into
        // the catalog, and the precheck never starts.
        Assert.AreEqual(1, releases);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.AreEqual(1, session.Disposals);
        Assert.AreEqual(0, await fixture.ReadAsync(w => Require<StackPanel>(w, "tables-panel").Children.Count));
        Assert.AreEqual("读取本地 .base 文件已取消。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text));
    }

    [TestMethod]
    public async Task ClosingWhilePreparingCancelsPrecheckThenDisposesProviderAndSession()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        FakeSourceProvider provider = new(log, "prov-inflight");
        opener.CompleteWith(LocalBaseConnection(provider, () => log.Add("local-connection:released")));
        await fixture.SettleAsync();
        await SelectFirstTableAsync(fixture);

        await ClickAsync(fixture, "preview-button"); // Gate stays open: precheck in flight.
        await fixture.DoAsync(w => w.Close());

        await session.Cancelled.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text), "预检已取消");
        Assert.AreEqual(1, provider.DisposeCount);
        Assert.AreEqual(1, session.Disposals);
        Assert.IsNull(session.StartedPreview);
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
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-snapshot"),
        [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-native", "标题", "text", "text"),
                new NativeSourceField("f-target", "策略字段", "text", valueKind),
            ]),
        ]));
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

        HostSourceImportDecision decision = session.SnapshotCalls().Single().Options.Decisions.Single();
        Assert.AreEqual("tbl-1", decision.TableId);
        Assert.AreEqual("f-target", decision.FieldId);
        Assert.AreEqual("snapshot", decision.Policy);
        Assert.AreEqual(expectedTargetKind, decision.TargetKind);
        Assert.AreEqual("", decision.TargetName);
        Assert.IsTrue(decision.Confirmed);
    }

    [TestMethod]
    public async Task ReverseNamesForTwoRelationsLandInSeparateNativeDecisions()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-rel"),
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
            new NativeSourceTable("tbl-b", "客户B", [new NativeSourceField("f-name-b", "名称B", "text", "text")]),
        ]));
        await fixture.SettleAsync();

        // One-sided relations reveal the reciprocal gate only when they exist.
        Assert.AreEqual(Visibility.Visible,
            await fixture.ReadAsync(w => Require<CheckBox>(w, "reverse-checkbox").Visibility));

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
        Assert.AreEqual("订单A的反向", decisions[0].TargetName);
        Assert.AreEqual("订单C的反向", decisions[1].TargetName);

        await fixture.DoAsync(w => Require<ComboBox>(w, "field-policy-tbl-a-f-rel-a").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        decisions = session.SnapshotCalls()[^1].Options.Decisions
            .OrderBy(decision => decision.FieldId, StringComparer.Ordinal).ToArray();
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
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-kind"),
        [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-native", "标题", "text", "text"),
                new NativeSourceField("f-target", "策略字段", "text", "number"),
            ]),
        ]));
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
        Assert.AreEqual("number", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);

        await fixture.DoAsync(w => Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").SelectedIndex = 1);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual("text", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);

        await fixture.DoAsync(w => Require<ComboBox>(w, "field-snapshot-kind-tbl-1-f-target").SelectedIndex = 2);
        await AssertConfirmationInvalidatedAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreEqual("json", session.SnapshotCalls()[^1].Options.Decisions.Single().TargetKind);
    }

    [TestMethod]
    public async Task LocalFileNoticesRequireAcknowledgementBeforePreview()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-notices"),
            () => log.Add("local-connection:released"),
        [
            "文件包含 2 个仪表盘：仪表盘不会随导入迁移",
            "文件包含 1 个工作流：工作流（自动化）不会随导入迁移",
            "表「订单」字段「总额」是公式：源文件未导出计算值，不会迁入计算值",
        ]));
        await fixture.SettleAsync();

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

        await fixture.DoAsync(w => Require<CheckBox>(w, "notice-acknowledge-checkbox").IsChecked = false);
        await AssertConfirmationInvalidatedAsync(fixture);
    }

    [TestMethod]
    public async Task LoadedPutsInitialKeyboardFocusOnTheFilePickerEntry()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWithCancel();
        await fixture.SettleAsync();

        Button selectFile = await fixture.ReadAsync(w => Require<Button>(w, "source-file-button"));
        Assert.AreSame(selectFile, await fixture.ReadAsync(w => FocusManager.GetFocusedElement(w)));
    }

    [TestMethod]
    public async Task EscapeKeyCancelsAnIdleWindowWithoutStartingWork()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWithCancel();
        await fixture.SettleAsync();

        await PressEscapeAsync(fixture);
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);

        HostSourceImportOpenResult result = await fixture.ReadAsync(w => w.Result);
        Assert.IsTrue(result.Cancelled);
        Assert.IsNull(result.TaskId);
        Assert.AreEqual(0, session.SnapshotCalls().Count);
        Assert.AreEqual(1, session.Disposals);
    }

    // Other STA fixtures can dismiss this real popup through shared native input state.
    // Keep the open-popup Escape contract isolated from parallel windows.
    [TestMethod]
    [DoNotParallelize]
    public async Task EscapeClosesExpandedPolicyBeforeCancellingTheWindow()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-escape"),
        [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-title", "标题", "text", "text"),
                new NativeSourceField("f-amount", "金额", "number", "number"),
            ]),
        ]));
        await fixture.SettleAsync();
        await SelectFirstTableAsync(fixture);
        await fixture.DoAsync(w =>
        {
            RequireType<Expander>(w).IsExpanded = true;
            Require<ComboBox>(w, "field-policy-tbl-1-f-amount").IsDropDownOpen = true;
        });
        await fixture.SettleAsync();
        await PressEscapeAsync(fixture, "field-policy-tbl-1-f-amount");
        Assert.IsFalse(await fixture.ReadAsync(w => Require<ComboBox>(w, "field-policy-tbl-1-f-amount").IsDropDownOpen));
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);
        Assert.AreEqual(0, session.Disposals);

        await PressEscapeAsync(fixture, "field-policy-tbl-1-f-amount");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        Assert.IsTrue((await fixture.ReadAsync(w => w.Result)).Cancelled);
        Assert.AreEqual(1, session.Disposals);
    }

    [TestMethod]
    public async Task WindowSurfacesThemedChromeAndFollowsEffectiveThemeChanges()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var source = new FakeSystemAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false),
        };
        using var appearance = new HostAppearanceService(source);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session, appearance);
        await fixture.ShowAsync();
        opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-theme"),
        [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-title", "标题", "text", "text"),
                new NativeSourceField("f-amount", "金额", "number", "number"),
            ]),
        ]));
        await fixture.SettleAsync();

        Assert.AreEqual(
            ThemedColor("dark", HostThemeResources.WindowBackgroundBrushKey),
            await fixture.ReadAsync(w => ((SolidColorBrush)w.Background!).Color));
        Assert.AreEqual(
            ThemedColor("dark", HostThemeResources.WindowTextBrushKey),
            await fixture.ReadAsync(w => ((SolidColorBrush)w.Foreground!).Color));
        await fixture.DoAsync(w => AssertFluentControls(w));
        foreach (string id in new[] { "field-policy-tbl-1-f-amount", "field-snapshot-kind-tbl-1-f-amount" })
        {
            Assert.AreEqual(ThemedColor("dark", HostThemeResources.SurfaceBackgroundBrushKey),
                await fixture.ReadAsync(w => ((SolidColorBrush)Require<ComboBox>(w, id).Background).Color));
        }
        await fixture.DoAsync(w =>
        {
            RequireType<Expander>(w).IsExpanded = true;
            Require<ComboBox>(w, "field-policy-tbl-1-f-amount").SelectedIndex = 1;
        });
        await fixture.SettleAsync();
        await fixture.DoAsync(w => AssertFluentControls(w));
        string? screenshotDirectory = ResolveOptInScreenshotDirectory();
        if (screenshotDirectory is not null)
        {
            Capture dark = await fixture.ReadAsync(CaptureWindow);
            File.WriteAllBytes(Path.Combine(screenshotDirectory, "native-wizard-dark.png"), dark.Png);
        }

        await fixture.DoAsync(w => appearance.SetPreferredTheme(AppThemeOptions.Light));
        await fixture.SettleAsync();
        Assert.AreEqual(
            ThemedColor("light", HostThemeResources.WindowBackgroundBrushKey),
            await fixture.ReadAsync(w => ((SolidColorBrush)w.Background!).Color));

        await fixture.DoAsync(w => source.Simulate(
            new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: true)));
        await fixture.SettleAsync();
        Assert.AreEqual(
            System.Windows.SystemColors.WindowBrush.Color,
            await fixture.ReadAsync(w => ((SolidColorBrush)w.Background!).Color));
    }

    [TestMethod]
    public async Task WindowCompositionRendersRealContentAndOptInScreenshotsStayInsideBuildTree()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        Capture catalog;
        Capture precheck;
        Capture localNotices;
        using (var fixture = new NativeWizardStaFixture(opener, session))
        {
            // Test-only presentation size so controls and report fit the
            // capture; the product default stays 840x760.
            await fixture.DoAsync(w => { w.Width = 880; w.Height = 1080; });
            await fixture.ShowAsync();
            opener.CompleteWith(CatalogConnection(new FakeSourceProvider(log, "prov-shot"),
            [
                new NativeSourceTable("tbl-a", "订单A",
                [
                    new NativeSourceField("f-title-a", "标题A", "text", "text"),
                    new NativeSourceField("f-rel-a", "关联B", "relation", "relation", RequiresReverseName: true),
                ]),
                new NativeSourceTable("tbl-b", "客户B", [new NativeSourceField("f-name-b", "名称B", "text", "text")]),
            ]));
            await fixture.SettleAsync();
            await fixture.DoAsync(w =>
            {
                Require<CheckBox>(w, "table-select-tbl-a").IsChecked = true;
                Require<CheckBox>(w, "table-select-tbl-b").IsChecked = true;
                Require<ComboBox>(w, "field-policy-tbl-a-f-title-a").SelectedIndex = 1;
                Require<TextBox>(w, "field-reverse-name-tbl-a-f-rel-a").Text = "客户B的订单A";
            });
            await fixture.DoAsync(w => RequireType<Expander>(w).IsExpanded = true);
            await fixture.SettleAsync();
            await fixture.DoAsync(w => ScrollAnchorIntoView(w,
                Require<TextBox>(w, "field-reverse-name-tbl-a-f-rel-a"), 140));
            await fixture.SettleAsync();
            catalog = await fixture.ReadAsync(CaptureWindow);

            await ClickAsync(fixture, "preview-button");
            session.CompletePending();
            await fixture.SettleAsync();
            Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
            await fixture.DoAsync(w => ScrollAnchorIntoView(w, Require<TextBlock>(w, "report-text"), 24));
            await fixture.SettleAsync();
            precheck = await fixture.ReadAsync(CaptureWindow);
            await ClickAsync(fixture, "start-button");
            await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        }

        // Synthetic local .base scenario: a fabricated .local catalog — never
        // the user's real sample file — with the full non-table disclosure
        // set (workflows/dashboards/views, attachment reference snapshots,
        // unexported formula values) next to a still-selectable data table.
        var noticeOpener = new FakeLocalFileOpener(log);
        using (var fixture = new NativeWizardStaFixture(noticeOpener, new FakeWizardSession(log)))
        {
            await fixture.DoAsync(w => { w.Width = 880; w.Height = 1080; });
            await fixture.ShowAsync();
            noticeOpener.CompleteWith(LocalBaseConnection(new FakeSourceProvider(log, "prov-shot-local"),
                    () => log.Add("local-connection:released"),
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
            localNotices = await fixture.ReadAsync(CaptureWindow);
        }

        foreach ((string name, Capture capture) in new[]
        {
            ("catalog", catalog), ("precheck", precheck), ("local-notices", localNotices),
        })
        {
            Assert.IsTrue(capture.Width > 600 && capture.Height > 500,
                $"{name} capture {capture.Width}x{capture.Height} is not the composed wizard");
            Assert.IsTrue(capture.ContentPixels > 100,
                $"{name} capture only painted {capture.ContentPixels} content pixels");
        }

        string? directory = ResolveOptInScreenshotDirectory();
        if (directory is null) return;
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-catalog.png"), catalog.Png);
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-precheck.png"), precheck.Png);
        File.WriteAllBytes(Path.Combine(directory, "native-wizard-local-notices.png"), localNotices.Png);
    }

    [TestMethod]
    public async Task RetiringTheWindowDismissesTheRealOwnedPickerWithZeroWrites()
    {
        // A REAL native OpenFileDialog modal, dismissed programmatically via
        // the window's own retirement path — no synthetic key/mouse input.
        // Gated to real CI runners only (GITHUB_ACTIONS=true && CI=true);
        // anything else reports Inconclusive instead of faking the modal.
        if (Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true"
            || Environment.GetEnvironmentVariable("CI") != "true")
        {
            Assert.Inconclusive("真实模态 picker 退役回归仅在受控 CI runner 上执行。");
        }
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var shown = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        IntPtr pickerHandle = IntPtr.Zero;
        async Task<NativeSourceConnection?> RealOpener(Window owner, CancellationToken token)
        {
            // Plain await: cancellation/faults propagate into the window's own
            // OCE/error paths instead of being remapped into a connection.
            string? path = await FeishuBaseFileDialog.PickFileAsync(owner, token,
                handle => { pickerHandle = handle; shown.TrySetResult(); });
            return path is null ? null : LocalBaseConnection(new FakeSourceProvider(log, "prov-real"), () => { });
        }
        using var fixture = new NativeWizardStaFixture(RealOpener, session);
        // Owner handle captured on the window's own STA thread; cleanup uses
        // primitives only, never cross-thread WPF access.
        IntPtr ownerHandle = await fixture.ReadAsync(
            w => new System.Windows.Interop.WindowInteropHelper(w).EnsureHandle());
        try
        {
            // Show/Loaded synchronously enters the real ShowDialog modal, so
            // ShowAsync cannot be awaited before the cancellation: fire it,
            // drive shown→Close→Closed, then reclaim it (and any failure).
            Task showing = fixture.ShowAsync();
            await shown.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
            await fixture.DoAsync(w => w.Close());
            await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
            await showing.WaitAsync(NativeWizardStaFixture.WaitBudget);

            HostSourceImportOpenResult result = await fixture.ReadAsync(w => w.Result);
            Assert.IsTrue(result.Cancelled);
            Assert.IsNull(result.TaskId);
            Assert.AreEqual(0, session.SnapshotCalls().Count, "退役必须零预检/写入");
            Assert.AreEqual(1, session.Disposals);
            Assert.IsFalse(CbtOwnedDialogCloser.IsWindowAlive(pickerHandle), "picker 句柄必须已销毁");
        }
        finally
        {
            // Recovery close with full identity re-verification so a failed
            // cancel cannot leave a live modal blocking the runner.
            if (pickerHandle != IntPtr.Zero)
                CbtOwnedDialogCloser.CloseVerifiedDialog(pickerHandle, ownerHandle);
        }
    }

    [TestMethod]
    public async Task FailedReSelectionRevokesOldPreviewUntilNewFilePreflight()
    {
        var log = new EventLog();
        var session = new FakeWizardSession(log);
        var opener = new FakeLocalFileOpener(log);
        using var fixture = new NativeWizardStaFixture(opener, session);
        await fixture.ShowAsync();
        FakeSourceProvider providerA = new(log, "prov-a");
        int releases = 0;
        opener.CompleteWith(LocalBaseConnection(providerA, () => releases++));
        await fixture.SettleAsync();
        await SelectFirstTableAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        // Cancelling the picker preserves the current file and its reviewed preview.
        await ClickAsync(fixture, "source-file-button");
        opener.CompleteWithCancel();
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text),
            "仍使用当前已读取的内容");
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));

        // A failed replacement read must require a fresh preflight before the old file can be submitted.
        await ClickAsync(fixture, "source-file-button");
        opener.FailWith(new IOException("合成损坏文件"));
        await fixture.SettleAsync();
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "status-text").Text),
            "SAFE-IOException");
        StringAssert.Contains(await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text),
            "不能确认导入");
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "preview-button").IsEnabled));

        // The revoked preview cannot be submitted even by a raised click.
        await ClickAsync(fixture, "start-button");
        await fixture.SettleAsync();
        Assert.IsFalse(fixture.Closed.Task.IsCompleted);
        Assert.IsNull(session.StartedPreview);

        // Re-selecting a readable file recovers the full flow.
        await ClickAsync(fixture, "source-file-button");
        FakeSourceProvider providerB = new(log, "prov-b");
        opener.CompleteWith(LocalBaseConnection(providerB, () => { }));
        await fixture.SettleAsync();
        Assert.AreEqual(1, releases); // A's connection is released on adopt.
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        await SelectFirstTableAsync(fixture);
        await ClickAsync(fixture, "preview-button");
        session.CompletePending();
        await fixture.SettleAsync();
        Assert.AreSame(providerB, session.SnapshotCalls()[^1].Provider);
        Assert.IsTrue(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        await ClickAsync(fixture, "start-button");
        await fixture.Closed.Task.WaitAsync(NativeWizardStaFixture.WaitBudget);
        Assert.AreEqual("task-native-1", (await fixture.ReadAsync(w => w.Result)).TaskId);
    }

    private static async Task AssertConfirmationInvalidatedAsync(NativeWizardStaFixture fixture)
    {
        await fixture.SettleAsync();
        Assert.IsFalse(await fixture.ReadAsync(w => Require<Button>(w, "start-button").IsEnabled));
        Assert.AreEqual("配置已变化，请重新预检。",
            await fixture.ReadAsync(w => Require<TextBlock>(w, "report-text").Text));
    }

    private static async Task SelectFirstTableAsync(NativeWizardStaFixture fixture)
    {
        await fixture.DoAsync(w => Require<CheckBox>(w, "table-select-tbl-1").IsChecked = true);
        await fixture.SettleAsync();
    }

    /// <summary>Synthetic local .base catalog exactly as the MainWindow
    /// opener hands it over: no token, no signature, no file path in the
    /// window, optional non-table notices.</summary>
    private static NativeSourceConnection LocalBaseConnection(
        FakeSourceProvider provider, Action release, string[]? notices = null, string name = "本地 base 文件") => new(
        name,
        [
            new NativeSourceTable("tbl-1", "订单",
            [
                new NativeSourceField("f-title", "标题", "text", "text"),
                new NativeSourceField("f-amount", "金额", "number", "number"),
            ]),
            new NativeSourceTable("tbl-2", "客户", [new NativeSourceField("f-name", "名称", "text", "text")]),
        ],
        _ => provider, release, Notices: notices);

    private static NativeSourceConnection CatalogConnection(
        FakeSourceProvider provider, NativeSourceTable[] tables, string name = "本地 base 文件") =>
        new(name, tables, _ => provider, () => { });

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

    private static Task PressEscapeAsync(NativeWizardStaFixture fixture, string? targetId = null) =>
        fixture.DoAsync(w =>
        {
            PresentationSource? presentation = PresentationSource.FromVisual(w);
            Assert.IsNotNull(presentation);
            UIElement target = targetId is null ? w : Require<ComboBox>(w, targetId);
            target.RaiseEvent(new KeyEventArgs(
                Keyboard.PrimaryDevice,
                presentation,
                timestamp: 0,
                Key.Escape)
            { RoutedEvent = UIElement.KeyDownEvent });
        });

    private static void AssertFluentControls(SourceImportWindow window)
    {
        foreach (string id in new[] { "preview-button", "start-button", "cancel-button", "source-file-button" })
        {
            Control control = Require<Control>(window, id);
            Assert.AreSame(window.FindResource(control.GetType()), control.Style, id);
            Assert.IsNotNull(control.Template, id);
        }
        if (FindByAutomationId<CheckBox>(window, "table-select-tbl-1") is not null)
        {
            Expander expander = RequireType<Expander>(window);
            Assert.AreSame(window.FindResource(typeof(Expander)), expander.Style);
            Assert.IsNotNull(expander.Template);
        }
    }

    private static Color ThemedColor(string theme, string key) =>
        ((SolidColorBrush)HostThemeResources.ForTheme(theme)[key]).Color;

    private sealed class FakeSystemAppearanceSource : ISystemAppearanceSource
    {
        private HostAppearanceSnapshot _snapshot = new(SystemPrefersDark: false, HighContrast: false);

        public event EventHandler? Changed;

        public HostAppearanceSnapshot InitialSnapshot
        {
            init => _snapshot = value;
        }

        public HostAppearanceSnapshot Read() => _snapshot;

        public void Simulate(HostAppearanceSnapshot snapshot)
        {
            _snapshot = snapshot;
            Changed?.Invoke(this, EventArgs.Empty);
        }

        public void Dispose() { }
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

        internal NativeWizardStaFixture(Func<Window, CancellationToken, Task<NativeSourceConnection?>> opener,
            FakeWizardSession session, HostAppearanceService? appearance = null)
        {
            _thread = new Thread(() =>
            {
                SourceImportWindow? window = null;
                Exception? failure = null;
                try
                {
                    _dispatcher = Dispatcher.CurrentDispatcher;
                    window = new SourceImportWindow(opener, session,
                        DescribeSafe, CancellationToken.None, appearance);
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

        internal NativeWizardStaFixture(FakeLocalFileOpener openLocalFile, FakeWizardSession session,
            HostAppearanceService? appearance = null)
            : this(openLocalFile.Invoke, session, appearance)
        {
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

    /// <summary>Fake .base opener mirroring the MainWindow-injected delegate:
    /// gated at its await boundary, fully offline — no file dialog, no file
    /// path, no HTTP, so tests never touch the user's real sample. Records
    /// that the window passed itself as the picker's explicit owner.</summary>
    private sealed class FakeLocalFileOpener(EventLog log)
    {
        private readonly object _gate = new();
        internal TaskCompletionSource<NativeSourceConnection?>? Pending { get; private set; }
        internal int Calls { get; private set; }
        internal List<Window> Owners { get; } = [];

        internal Task<NativeSourceConnection?> Invoke(Window owner, CancellationToken token)
        {
            var completion = new TaskCompletionSource<NativeSourceConnection?>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            lock (_gate) { Calls++; Owners.Add(owner); Pending = completion; }
            log.Add("local-file:enter");
            return completion.Task;
        }

        internal void CompleteWith(NativeSourceConnection connection) => Pending!.TrySetResult(connection);
        internal void CompleteWithCancel() => Pending!.TrySetResult(null);
        internal void FailWith(Exception error) => Pending!.TrySetException(error);
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
                lock (_gate)
                {
                    _current = preview;
                    _pendingProvider = provider;
                }
                return preview;
            }
            catch
            {
                provider.Dispose();
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
            displayName = "本地 base 文件",
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
                if (!ReferenceEquals(preview, _current))
                    throw new InvalidOperationException("Source preview is no longer current.");
                StartedPreview = preview;
                _current = null;
                _pendingProvider = null;
            }
            return TaskId;
        }

        public void DiscardPreview()
        {
            lock (_gate)
            {
                if (_pendingProvider is { } pending) pending.Dispose();
                _current = null;
                _pendingProvider = null;
            }
        }

        public void Dispose()
        {
            lock (_gate)
            {
                if (_pendingProvider is { } pending) pending.Dispose();
                _pendingProvider = null;
                _current = null;
            }
            Disposals++;
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
