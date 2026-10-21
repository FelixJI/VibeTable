using System.ComponentModel;
using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Input;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

internal sealed record NativeSourceField(string Id, string Name, string Kind, string ValueKind,
    bool RequiresReverseName = false);
internal sealed record NativeSourceTable(string Id, string Name, NativeSourceField[] Fields);

/// <summary>Non-table disclosures of a local .base file; every entry is
/// shown and requires acknowledgement before preflight, never dropped.</summary>
internal sealed record NativeSourceConnection(string Name, NativeSourceTable[] Tables,
    Func<string[], IHostSourceImportProvider> CreateProvider, Action Release,
    string[]? Notices = null) : IDisposable
{
    public void Dispose() => Release();
}

/// <summary>Native local .base file, selection and confirmation boundary.
/// There is no credential surface and no cloud connect delegate: the only
/// entry is the Host-injected system file picker.</summary>
internal sealed class SourceImportWindow : Window
{
    private readonly Func<CancellationToken, Task<NativeSourceConnection?>> _openLocalFile;
    private readonly IHostSourceImportWizardSession _session;
    private readonly Func<Exception, string> _describeError;
    private readonly CancellationTokenSource _lifetime;
    private readonly CancellationTokenRegistration _retirement;
    private readonly Button _selectFileButton = new();
    private readonly StackPanel _tables = new();
    private readonly StackPanel _notices = new();
    private readonly CheckBox _noticeAcknowledge = new()
    {
        Content = "我已阅读并理解上述告知：相应内容不会随导入迁移，仍要继续导入数据表",
        Visibility = Visibility.Collapsed,
    };
    private readonly TextBlock _status = new() { TextWrapping = TextWrapping.Wrap };
    private readonly TextBlock _report = new() { TextWrapping = TextWrapping.Wrap };
    private readonly CheckBox _reverse = new()
    {
        Content = "允许为单向关联创建迁移反向字段",
        // Only a catalog that actually carries one-sided native relations
        // reveals the reciprocal-field gate; a plain local file must not
        // advertise a relation capability its fields never use.
        Visibility = Visibility.Collapsed,
    };
    private readonly Button _previewButton = new() { Content = "预检", IsEnabled = false };
    private readonly Button _startButton = new() { Content = "确认并导入", IsEnabled = false };
    private readonly List<TableChoice> _choices = [];
    private readonly HostAppearanceService _appearance;
    private readonly bool _ownsAppearance;
    private NativeSourceConnection? _connection;
    private HostSourceImportPreview? _preview;
    private bool _noticesAcknowledged;
    private bool _busy;
    private bool _closing;
    private bool _closed;

    internal HostSourceImportOpenResult Result { get; private set; } = new(true, null);

    internal SourceImportWindow(
        Func<CancellationToken, Task<NativeSourceConnection?>> openLocalFile,
        IHostSourceImportWizardSession session, Func<Exception, string> describeError,
        CancellationToken cancellation, HostAppearanceService? appearance = null)
    {
        _openLocalFile = openLocalFile;
        _session = session;
        _describeError = describeError;
        _lifetime = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        // Without an injected service the window still follows the system
        // appearance on its own dispatcher; an explicit preference arrives
        // once the composition root passes its shared service in.
        _appearance = appearance ?? new HostAppearanceService(
            new WindowsSystemAppearanceSource(),
            dispatcher: Dispatcher);
        _ownsAppearance = appearance is null;
        Title = "从飞书 .base 文件导入";
        Width = 840;
        Height = 760;
        MinWidth = 680;
        MinHeight = 520;
        // System font family/size and themed window chrome resolve through
        // standard WPF dynamic resources, so system changes apply live.
        SetResourceReference(FontFamilyProperty, System.Windows.SystemFonts.MessageFontFamilyKey);
        SetResourceReference(FontSizeProperty, System.Windows.SystemFonts.MessageFontSizeKey);
        SetResourceReference(BackgroundProperty, HostThemeResources.WindowBackgroundBrushKey);
        SetResourceReference(ForegroundProperty, HostThemeResources.WindowTextBrushKey);
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        AutomationProperties.SetAutomationId(this, "source-import-window");
        AutomationProperties.SetAutomationId(_previewButton, "preview-button");
        AutomationProperties.SetAutomationId(_startButton, "start-button");
        AutomationProperties.SetAutomationId(_status, "status-text");
        AutomationProperties.SetAutomationId(_report, "report-text");
        AutomationProperties.SetAutomationId(_reverse, "reverse-checkbox");
        AutomationProperties.SetAutomationId(_tables, "tables-panel");
        AutomationProperties.SetAutomationId(_notices, "notices-panel");
        AutomationProperties.SetAutomationId(_noticeAcknowledge, "notice-acknowledge-checkbox");

        var root = new Grid { Margin = new Thickness(24) };
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        var content = new StackPanel();
        // Content first, footer second: the declaration order is the Tab
        // order, so keyboard users reach the inputs before the buttons.
        root.Children.Add(new ScrollViewer
        {
            Content = content,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
        });
        var footer = new StackPanel { Orientation = Orientation.Horizontal,
            HorizontalAlignment = HorizontalAlignment.Right, Margin = new Thickness(0, 16, 0, 0) };
        var cancel = new Button { Content = "取消", MinWidth = 80, Padding = new Thickness(12, 6, 12, 6) };
        AutomationProperties.SetAutomationId(cancel, "cancel-button");
        cancel.Click += (_, _) => Close();
        foreach (Button button in new[] { _previewButton, _startButton, cancel })
        { ApplyInputTheme(button); button.Margin = new Thickness(8, 0, 0, 0); button.Padding = new Thickness(12, 6, 12, 6); footer.Children.Add(button); }
        Grid.SetRow(footer, 1);
        root.Children.Add(footer);
        content.Children.Add(new TextBlock { Text = Title, FontSize = 24, FontWeight = FontWeights.SemiBold });
        content.Children.Add(new TextBlock { Text = "在本机选择 .base 导出文件；读取全程离线完成，不访问飞书，也无需任何访问令牌。文件路径与内容不会传给 Web 界面。",
            TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 8, 0, 16) });
        _selectFileButton.Content = "选择 .base 文件";
        _selectFileButton.MinWidth = 160;
        AutomationProperties.SetAutomationId(_selectFileButton, "source-file-button");
        ApplyInputTheme(_selectFileButton);
        _selectFileButton.Click += async (_, _) => await OpenLocalFileAsync();
        content.Children.Add(_selectFileButton);
        var selectFileHint = new TextBlock
        {
            Text = "读取成功后可重新选择文件；更换文件会使既有选择与预检失效。",
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 6, 0, 12),
        };
        selectFileHint.SetResourceReference(ForegroundProperty, HostThemeResources.WindowTextBrushKey);
        content.Children.Add(selectFileHint);
        content.Children.Add(_status);
        content.Children.Add(_notices);
        _noticeAcknowledge.Margin = new Thickness(0, 8, 0, 0);
        content.Children.Add(_noticeAcknowledge);
        content.Children.Add(_tables);
        _reverse.Margin = new Thickness(0, 16, 0, 12);
        _reverse.Checked += (_, _) => InvalidatePreview();
        _reverse.Unchecked += (_, _) => InvalidatePreview();
        _noticeAcknowledge.Checked += (_, _) => AcknowledgeNotices(true);
        _noticeAcknowledge.Unchecked += (_, _) => AcknowledgeNotices(false);
        content.Children.Add(_reverse);
        content.Children.Add(_report);
        Content = root;
        OnAppearanceProjectionChanged(_appearance.Projection);
        _appearance.ProjectionChanged += OnAppearanceProjectionChanged;
        _previewButton.Click += async (_, _) => await PreviewAsync();
        _startButton.Click += (_, _) => Start();
        Closing += OnClosing;
        Closed += (_, _) => DisposeResources();
        Loaded += OnLoaded;
        KeyDown += OnKeyDown;
        _retirement = cancellation.Register(() => Dispatcher.BeginInvoke(new Action(() =>
        { if (!_closed) Close(); })));
    }

    private void OnAppearanceProjectionChanged(HostThemeProjection projection)
    {
        HostThemeResources.Apply(Resources, projection);
    }

    private async void OnLoaded(object sender, RoutedEventArgs args)
    {
        // Windows convention for an owned modal wizard: keyboard focus starts
        // on the file picker entry, not on the dialog chrome.
        if (!_closing && !_closed)
        {
            _selectFileButton.Focus();
        }
        // The web click opens this window for exactly one purpose: choosing
        // a local .base file. The system picker therefore opens directly on
        // first load; cancelling it cancels the whole import with zero
        // writes, exactly like dismissing the window.
        if (!_closing && !_closed && _connection is null)
        {
            args.Handled = true;
            await OpenLocalFileAsync();
        }
    }

    private void OnKeyDown(object sender, KeyEventArgs args)
    {
        if (args.Key != Key.Escape)
        {
            return;
        }
        // Bubbling lets an open ComboBox consume ESC before the window cancels.
        // ESC cancels exactly like the title-bar close and Cancel button:
        // the linked token drains in-flight calls before the window unloads.
        args.Handled = true;
        Close();
    }

    /// <summary>Text-entry surfaces follow the effective theme through
    /// dynamic resources; caret uses the window text color so dark surfaces
    /// keep a visible caret.</summary>
    private static void ApplyInputTheme(Control input)
    {
        if (input is Button) return; // Preserve Fluent disabled/hover states and template.
        input.SetResourceReference(BackgroundProperty, HostThemeResources.SurfaceBackgroundBrushKey);
        input.SetResourceReference(ForegroundProperty, HostThemeResources.SurfaceTextBrushKey);
        input.SetResourceReference(BorderBrushProperty, HostThemeResources.SurfaceBorderBrushKey);
        if (input is TextBox textBox)
        {
            textBox.SetResourceReference(TextBox.CaretBrushProperty, HostThemeResources.WindowTextBrushKey);
        }
        else if (input is PasswordBox passwordBox)
        {
            passwordBox.SetResourceReference(
                PasswordBox.CaretBrushProperty,
                HostThemeResources.WindowTextBrushKey);
        }
    }

    /// <summary>Loads a local .base file via the injected opener; null is
    /// plain user cancellation — no error, no write and no precheck.</summary>
    private async Task OpenLocalFileAsync()
    {
        if (_busy || _closing) return;
        SetBusy(true);
        _status.Text = "正在读取本地 .base 文件…";
        try
        {
            NativeSourceConnection? connection = await _openLocalFile(_lifetime.Token);
            ReleaseLateConnection(connection);
            if (connection is null)
            {
                _status.Text = _connection is null
                    ? "已取消选择本地 .base 文件，未导入任何内容。可重新选择文件或关闭窗口。"
                    : "已取消选择新的 .base 文件，仍使用当前已读取的内容。";
                return;
            }
            ResetConnection();
            AdoptConnection(connection);
        }
        catch (OperationCanceledException) { _status.Text = "读取本地 .base 文件已取消。"; }
        catch (Exception error) { _status.Text = SafeError(error); }
        finally
        {
            SetBusy(false);
            // After a cancelled picker with no catalog the only available
            // action is picking again; keep keyboard focus on it.
            if (!_closing && !_closed && _connection is null) _selectFileButton.Focus();
        }
    }

    // Late results after cancellation are never adopted: window disposal may
    // already have run, so the freshly returned connection is released here.
    private void ReleaseLateConnection(NativeSourceConnection? connection)
    {
        if (!_lifetime.Token.IsCancellationRequested) return;
        connection?.Dispose();
        _lifetime.Token.ThrowIfCancellationRequested();
    }

    private void ResetConnection()
    {
        // Switching files immediately releases the old pending preview and
        // its provider in the Host session, not at the next Prepare.
        _session.DiscardPreview();
        InvalidatePreview();
        _connection?.Dispose();
        _connection = null;
        _choices.Clear();
        _tables.Children.Clear();
        ResetNotices();
    }

    /// <summary>Catalog receiver for the local file source: one connection
    /// lifecycle feeding the shared preflight pipeline.</summary>
    private void AdoptConnection(NativeSourceConnection connection)
    {
        _connection = connection;
        _selectFileButton.Content = "重新选择 .base 文件";
        bool anyReverseName = false;
        foreach (NativeSourceTable table in connection.Tables)
        {
            AddTable(table);
            anyReverseName |= table.Fields.Any(field => field.RequiresReverseName);
        }
        AddNotices(connection.Notices);
        _reverse.IsChecked = anyReverseName && _reverse.IsChecked == true;
        _reverse.Visibility = anyReverseName ? Visibility.Visible : Visibility.Collapsed;
        _status.Text = connection.Tables.Length == 0
            ? $"读取成功：{connection.Name} 中没有数据表。可重新选择文件。"
            : $"{connection.Name} · 共 {connection.Tables.Length} 张表。请选择表及必要的关联目标。";
    }

    private void AddTable(NativeSourceTable table)
    {
        var selected = new CheckBox { Content = $"{table.Name}（{table.Fields.Length} 个字段）", FontWeight = FontWeights.SemiBold };
        AutomationProperties.SetAutomationId(selected, $"table-select-{table.Id}");
        var target = new TextBox { Text = table.Name, Padding = new Thickness(6), Margin = new Thickness(0, 6, 0, 6) };
        AutomationProperties.SetAutomationId(target, $"table-target-{table.Id}");
        ApplyInputTheme(target);
        var panel = new StackPanel { Margin = new Thickness(0, 14, 0, 0) };
        panel.Children.Add(selected);
        panel.Children.Add(new TextBlock { Text = "新建目标表名称", Margin = new Thickness(0, 6, 0, 0) });
        panel.Children.Add(target);
        var fields = new StackPanel();
        var choices = new List<FieldChoice>();
        foreach (NativeSourceField field in table.Fields)
        {
            var row = new DockPanel { Margin = new Thickness(0, 3, 0, 3) };
            var policy = new ComboBox { ItemsSource = new[] { "原生迁移", "保留值快照", "跳过字段" }, SelectedIndex = 0, Width = 128 };
            ApplyInputTheme(policy);
            AutomationProperties.SetAutomationId(policy, $"field-policy-{table.Id}-{field.Id}");
            var snapshotKind = new ComboBox
            {
                ItemsSource = new[] { "保持类型", "精确文本", "JSON" },
                SelectedIndex = 0,
                Width = 96,
                Margin = new Thickness(8, 0, 0, 0),
                ToolTip = "值快照的目标类型：保持来源类型、精确文本（保真大数等）或 JSON",
                Visibility = Visibility.Collapsed,
            };
            ApplyInputTheme(snapshotKind);
            AutomationProperties.SetAutomationId(snapshotKind, $"field-snapshot-kind-{table.Id}-{field.Id}");
            var strategies = new StackPanel { Orientation = Orientation.Horizontal };
            DockPanel.SetDock(strategies, Dock.Right);
            strategies.Children.Add(policy);
            strategies.Children.Add(snapshotKind);
            row.Children.Add(strategies);
            row.Children.Add(new TextBlock { Text = $"{field.Name} · {field.Kind}", TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 4, 12, 0) });
            fields.Children.Add(row);
            TextBox? reverseName = null;
            if (field.RequiresReverseName)
            {
                reverseName = new TextBox { Padding = new Thickness(6), Margin = new Thickness(0, 2, 24, 6),
                    MaxWidth = 360, HorizontalAlignment = HorizontalAlignment.Left };
                AutomationProperties.SetAutomationId(reverseName, $"field-reverse-name-{table.Id}-{field.Id}");
                ApplyInputTheme(reverseName);
                fields.Children.Add(new TextBlock
                {
                    Text = "新建反向字段名称（仅原生迁移时创建并命名；快照/跳过不创建反向字段，也不改名目标列）",
                    TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 6, 0, 2),
                });
                fields.Children.Add(reverseName);
                reverseName.TextChanged += (_, _) => InvalidatePreview();
            }
            choices.Add(new(field, policy, snapshotKind, reverseName));
            policy.SelectionChanged += (_, _) =>
            {
                snapshotKind.Visibility = policy.SelectedIndex == 1 ? Visibility.Visible : Visibility.Collapsed;
                InvalidatePreview();
            };
            snapshotKind.SelectionChanged += (_, _) => InvalidatePreview();
        }
        panel.Children.Add(new Expander { Header = "字段与保真策略（选择快照将失去动态计算）", Content = fields });
        _tables.Children.Add(panel);
        _choices.Add(new(table, selected, target, choices));
        selected.Checked += (_, _) => InvalidatePreview();
        selected.Unchecked += (_, _) => InvalidatePreview();
        target.TextChanged += (_, _) => InvalidatePreview();
    }

    private void AddNotices(string[]? notices)
    {
        ResetNotices();
        if (notices is not { Length: > 0 }) return;
        _notices.Children.Add(new TextBlock
        {
            Text = $"该来源包含 {notices.Length} 项不会随导入迁移的内容，请逐条阅读：",
            FontWeight = FontWeights.SemiBold,
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 12, 0, 4),
        });
        for (int index = 0; index < notices.Length; index++)
        {
            var line = new TextBlock { Text = notices[index], TextWrapping = TextWrapping.Wrap,
                Margin = new Thickness(0, 2, 0, 2) };
            AutomationProperties.SetAutomationId(line, $"notice-{index}");
            _notices.Children.Add(line);
        }
        _noticeAcknowledge.Visibility = Visibility.Visible;
    }

    private void ResetNotices()
    {
        _noticesAcknowledged = false;
        _noticeAcknowledge.IsChecked = false;
        _noticeAcknowledge.Visibility = Visibility.Collapsed;
        _notices.Children.Clear();
    }

    private bool NoticesPending => _connection?.Notices is { Length: > 0 } && !_noticesAcknowledged;

    private void AcknowledgeNotices(bool acknowledged)
    {
        _noticesAcknowledged = acknowledged;
        if (!acknowledged) InvalidatePreview();
        if (!_busy)
            _previewButton.IsEnabled = _connection is { Tables.Length: > 0 } && !NoticesPending;
    }

    private async Task PreviewAsync()
    {
        if (_busy || _closing || _connection is null) return;
        if (NoticesPending)
        { _report.Text = "请先阅读并勾选确认上方告知，再进行预检。"; return; }
        TableChoice[] selected = _choices.Where(c => c.Selected.IsChecked == true).ToArray();
        if (selected.Length == 0) { _report.Text = "请至少选择一张来源表。"; return; }
        SetBusy(true);
        InvalidatePreview();
        _report.Text = "正在读取所选表并预检；此步骤不会创建目标表或写入记录…";
        try
        {
            string[] ids = selected.Select(c => c.Table.Id).ToArray();
            var decisions = new List<HostSourceImportDecision>();
            foreach (TableChoice table in selected)
                foreach (FieldChoice field in table.Fields)
                {
                    if (field.Policy.SelectedIndex == 1)
                        decisions.Add(new(table.Table.Id, field.Field.Id, "snapshot",
                            SelectedSnapshotKind(field), "", true));
                    else if (field.Policy.SelectedIndex == 2)
                        decisions.Add(new(table.Table.Id, field.Field.Id, "skip", "", "", true));
                    else if (field.Field.RequiresReverseName)
                        // One-sided native relations carry an explicit reciprocal
                        // name Decision (Go checkFieldNames blocks the shared
                        // default); snapshot/skip never carry the reverse name
                        // so they cannot rename the target column.
                        decisions.Add(new(table.Table.Id, field.Field.Id, "native", "",
                            field.ReverseName?.Text.Trim() ?? "", true));
                }
            var options = new HostSourceImportOptions(ids, selected.Select(c =>
                new HostSourceImportTargetName(c.Table.Id, c.Target.Text.Trim())).ToArray(), decisions.ToArray(),
                _reverse.IsChecked == true);
            _preview = await _session.PrepareAsync(_connection.CreateProvider(ids), options, _lifetime.Token);
            _lifetime.Token.ThrowIfCancellationRequested();
            _report.Text = DescribePlan(_preview.Plan);
        }
        catch (OperationCanceledException) { _report.Text = "预检已取消，未提交导入。"; }
        catch (Exception error) { _report.Text = SafeError(error); }
        finally { SetBusy(false); }
    }

    private void Start()
    {
        if (_busy || _closing || _preview is null) return;
        try
        {
            _lifetime.Token.ThrowIfCancellationRequested();
            Result = new(false, _session.Start(_preview));
            Close();
        }
        catch (Exception error) { InvalidatePreview(); _report.Text = SafeError(error); }
    }

    private static string DescribePlan(JsonElement plan)
    {
        var lines = new List<string>();
        foreach (JsonElement table in plan.GetProperty("tables").EnumerateArray())
            lines.Add($"{table.GetProperty("name").GetString()}：{table.GetProperty("recordCount").GetInt32()} 条记录");
        foreach (JsonElement item in plan.GetProperty("diagnostics").EnumerateArray())
            lines.Add($"{(item.GetProperty("blocking").GetBoolean() ? "需处理" : "提示")} · {(item.TryGetProperty("tableId", out var tableId) ? tableId.GetString() : "")} {item.GetProperty("message").GetString()}");
        lines.Add(plan.GetProperty("canApply").GetBoolean() ? "预检通过。确认后只新建所列目标表；导入进度和结果会显示在导入管理中。" : "预检存在阻断项。请调整所选表、目标名称或字段策略后重新预检。");
        return string.Join(Environment.NewLine, lines);
    }

    private string SafeError(Exception error) => _describeError(error);

    // The Go planField contract rejects formula/lookup/autoDate for any
    // strategy and relation/file snapshot targets ("快照目标必须是普通可写值
    // 字段"), so those source values must land in an explicit json snapshot
    // target instead of being blocked. A json snapshot preserves the source
    // value payload only; it never implies attachment bytes were downloaded.
    // Empty kinds stay empty and keep the engine's own json default.
    private static readonly HashSet<string> NonWritableSnapshotKinds =
        new(StringComparer.Ordinal) { "relation", "file", "formula", "lookup", "autoDate" };

    private static string SnapshotTargetKind(NativeSourceField field) =>
        field.ValueKind == "autoNumber" ? "text"
            : NonWritableSnapshotKinds.Contains(field.ValueKind) ? "json" : field.ValueKind;

    // Explicit snapshot target choice: keep the source kind (converted where
    // the Go guards reject it as a snapshot target), exact text for values
    // that lose precision through float64 (CanonicalValue rejects numbers
    // such as 9007199254740993 with "请选择精确文本快照"), or JSON for values
    // without an option identity mapping. text and json are always writable
    // snapshot targets; every Go guard stays authoritative.
    private static string SelectedSnapshotKind(FieldChoice field) => field.SnapshotKind.SelectedIndex switch
    {
        1 => "text",
        2 => "json",
        _ => SnapshotTargetKind(field.Field),
    };

    private void InvalidatePreview()
    {
        _preview = null;
        _startButton.IsEnabled = false;
        if (!_busy) _report.Text = "配置已变化，请重新预检。";
    }

    private void SetBusy(bool value)
    {
        _busy = value;
        _selectFileButton.IsEnabled = !value;
        _tables.IsEnabled = !value;
        _reverse.IsEnabled = !value;
        _noticeAcknowledge.IsEnabled = !value;
        _previewButton.IsEnabled = !value && _connection is { Tables.Length: > 0 } && !NoticesPending;
        _startButton.IsEnabled = !value && _preview?.Plan.GetProperty("canApply").GetBoolean() == true;
        if (!value && _closing) Close();
    }

    private void OnClosing(object? sender, CancelEventArgs args)
    {
        _closing = true;
        _lifetime.Cancel();
        // Do not dispose a provider while its read is still unwinding.
        if (_busy) args.Cancel = true;
    }

    private void DisposeResources()
    {
        _closed = true;
        _retirement.Dispose();
        _session.Dispose();
        _connection?.Dispose();
        _lifetime.Dispose();
        _appearance.ProjectionChanged -= OnAppearanceProjectionChanged;
        HostThemeResources.Remove(Resources);
        if (_ownsAppearance)
        {
            _appearance.Dispose();
        }
    }

    private sealed record FieldChoice(NativeSourceField Field, ComboBox Policy, ComboBox SnapshotKind,
        TextBox? ReverseName);
    private sealed record TableChoice(NativeSourceTable Table, CheckBox Selected, TextBox Target, List<FieldChoice> Fields);
}
