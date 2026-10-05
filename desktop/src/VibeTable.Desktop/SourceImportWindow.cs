using System.ComponentModel;
using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop;

internal sealed record NativeSourceField(string Id, string Name, string Kind, string ValueKind);
internal sealed record NativeSourceTable(string Id, string Name, NativeSourceField[] Fields);
internal sealed record NativeSourceConnection(string Name, NativeSourceTable[] Tables,
    Func<string[], IHostSourceImportProvider> CreateProvider, Action Release) : IDisposable
{
    public void Dispose() => Release();
}

/// <summary>Native credential, selection and confirmation boundary.</summary>
internal sealed class SourceImportWindow : Window
{
    private readonly Func<string, string, string, string, CancellationToken, Task<NativeSourceConnection>> _connect;
    private readonly IHostSourceImportWizardSession _session;
    private readonly Func<Exception, string> _describeError;
    private readonly CancellationTokenSource _lifetime;
    private readonly CancellationTokenRegistration _retirement;
    private readonly TextBox _source = new();
    private readonly PasswordBox _token = new();
    private readonly TextBox _accessKey = new();
    private readonly PasswordBox _secret = new();
    private readonly StackPanel _credentials = new();
    private readonly StackPanel _tables = new();
    private readonly TextBlock _status = new() { TextWrapping = TextWrapping.Wrap };
    private readonly TextBlock _report = new() { TextWrapping = TextWrapping.Wrap };
    private readonly CheckBox _reverse = new() { Content = "允许为单向关联创建迁移反向字段" };
    private readonly Button _previewButton = new() { Content = "预检", IsEnabled = false };
    private readonly Button _startButton = new() { Content = "确认并导入", IsEnabled = false };
    private readonly List<TableChoice> _choices = [];
    private NativeSourceConnection? _connection;
    private HostSourceImportPreview? _preview;
    private bool _busy;
    private bool _closing;
    private bool _closed;

    internal HostSourceImportOpenResult Result { get; private set; } = new(true, null);

    internal SourceImportWindow(string provider,
        Func<string, string, string, string, CancellationToken, Task<NativeSourceConnection>> connect,
        IHostSourceImportWizardSession session, Func<Exception, string> describeError,
        CancellationToken cancellation)
    {
        _connect = connect;
        _session = session;
        _describeError = describeError;
        _lifetime = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        Title = provider == "feishu" ? "从飞书多维表格导入" : "从 WPS 多维表格导入";
        Width = 840;
        Height = 760;
        MinWidth = 680;
        MinHeight = 520;
        FontFamily = new System.Windows.Media.FontFamily("Microsoft YaHei UI");
        FontSize = 14;
        Background = System.Windows.Media.Brushes.White;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        AutomationProperties.SetAutomationId(this, "source-import-window");
        AutomationProperties.SetAutomationId(_previewButton, "preview-button");
        AutomationProperties.SetAutomationId(_startButton, "start-button");
        AutomationProperties.SetAutomationId(_status, "status-text");
        AutomationProperties.SetAutomationId(_report, "report-text");
        AutomationProperties.SetAutomationId(_reverse, "reverse-checkbox");
        AutomationProperties.SetAutomationId(_tables, "tables-panel");

        var root = new DockPanel { Margin = new Thickness(24) };
        var footer = new StackPanel { Orientation = Orientation.Horizontal,
            HorizontalAlignment = HorizontalAlignment.Right, Margin = new Thickness(0, 16, 0, 0) };
        var cancel = new Button { Content = "取消", MinWidth = 80, Padding = new Thickness(12, 6, 12, 6) };
        AutomationProperties.SetAutomationId(cancel, "cancel-button");
        cancel.Click += (_, _) => Close();
        foreach (Button button in new[] { _previewButton, _startButton, cancel })
        { button.Margin = new Thickness(8, 0, 0, 0); button.Padding = new Thickness(12, 6, 12, 6); footer.Children.Add(button); }
        DockPanel.SetDock(footer, Dock.Bottom);
        root.Children.Add(footer);
        var content = new StackPanel();
        root.Children.Add(new ScrollViewer { Content = content, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        content.Children.Add(new TextBlock { Text = Title, FontSize = 24, FontWeight = FontWeights.SemiBold });
        content.Children.Add(new TextBlock { Text = "仅导入当前授权范围内的数据；分页读取不保证时点一致。凭据只保留在本次 Host 会话。",
            TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 8, 0, 16) });
        if (provider == "wps")
            content.Children.Add(new TextBlock { Text = "个人与企业账号尚待真实联调。请使用明确的多维表格 file_id；分享链接、文件浏览及附件下载暂不支持。",
                TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 0, 0, 16) });
        AddInput(_credentials, provider == "feishu" ? "Base / Wiki 链接或 app_token" : "多维表格 file_id", _source, "source-input");
        AddInput(_credentials, "只读授权的访问令牌（access token）", _token, "token-input");
        if (provider == "wps")
        {
            AddInput(_credentials, "签名 access key（仅应用开启 KSO-1 时填写）", _accessKey, "access-key-input");
            AddInput(_credentials, "签名 secret key（未开启签名请留空）", _secret, "secret-input");
        }
        var connectButton = new Button { Content = "连接并读取表目录", HorizontalAlignment = HorizontalAlignment.Left,
            Padding = new Thickness(12, 6, 12, 6), Margin = new Thickness(0, 8, 0, 12) };
        AutomationProperties.SetAutomationId(connectButton, "connect-button");
        connectButton.Click += async (_, _) => await ConnectAsync();
        _credentials.Children.Add(connectButton);
        content.Children.Add(_credentials);
        content.Children.Add(_status);
        content.Children.Add(_tables);
        _reverse.Margin = new Thickness(0, 16, 0, 12);
        _reverse.Checked += (_, _) => InvalidatePreview();
        _reverse.Unchecked += (_, _) => InvalidatePreview();
        content.Children.Add(_reverse);
        content.Children.Add(_report);
        Content = root;
        _previewButton.Click += async (_, _) => await PreviewAsync();
        _startButton.Click += (_, _) => Start();
        _source.TextChanged += (_, _) => ConnectionChanged();
        _accessKey.TextChanged += (_, _) => ConnectionChanged();
        _token.PasswordChanged += (_, _) => ConnectionChanged();
        _secret.PasswordChanged += (_, _) => ConnectionChanged();
        Closing += OnClosing;
        Closed += (_, _) => DisposeResources();
        _retirement = cancellation.Register(() => Dispatcher.BeginInvoke(new Action(() =>
        { if (!_closed) Close(); })));
    }

    private static void AddInput(Panel panel, string label, Control input, string automationId)
    {
        AutomationProperties.SetAutomationId(input, automationId);
        panel.Children.Add(new TextBlock { Text = label, Margin = new Thickness(0, 8, 0, 4) });
        input.Padding = new Thickness(8, 6, 8, 6);
        panel.Children.Add(input);
    }

    private async Task ConnectAsync()
    {
        if (_busy || _closing) return;
        SetBusy(true);
        InvalidatePreview();
        _connection?.Dispose();
        _connection = null;
        _choices.Clear();
        _tables.Children.Clear();
        _status.Text = "正在连接并读取当前授权范围内的表目录…";
        string secret = _secret.Password;
        string accessToken = _token.Password;
        _secret.Clear();
        _token.Clear();
        try
        {
            _connection = await _connect(_source.Text.Trim(), accessToken,
                _accessKey.Text.Trim(), secret, _lifetime.Token);
            _lifetime.Token.ThrowIfCancellationRequested();
            foreach (NativeSourceTable table in _connection.Tables) AddTable(table);
            _status.Text = _connection.Tables.Length == 0
                ? "连接成功，当前授权范围内没有数据表。请核对授权后重新连接。"
                : $"{_connection.Name} · 当前授权范围内 {_connection.Tables.Length} 张表。请选择表及必要的关联目标。";
        }
        catch (OperationCanceledException) { _status.Text = "读取已取消。"; }
        catch (Exception error) { _status.Text = SafeError(error); }
        finally { SetBusy(false); }
    }

    private void AddTable(NativeSourceTable table)
    {
        var selected = new CheckBox { Content = $"{table.Name}（{table.Fields.Length} 个字段）", FontWeight = FontWeights.SemiBold };
        AutomationProperties.SetAutomationId(selected, $"table-select-{table.Id}");
        var target = new TextBox { Text = table.Name, Padding = new Thickness(6), Margin = new Thickness(0, 6, 0, 6) };
        AutomationProperties.SetAutomationId(target, $"table-target-{table.Id}");
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
            AutomationProperties.SetAutomationId(policy, $"field-policy-{table.Id}-{field.Id}");
            DockPanel.SetDock(policy, Dock.Right);
            row.Children.Add(policy);
            row.Children.Add(new TextBlock { Text = $"{field.Name} · {field.Kind}", TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 4, 12, 0) });
            fields.Children.Add(row);
            choices.Add(new(field, policy));
            policy.SelectionChanged += (_, _) => InvalidatePreview();
        }
        panel.Children.Add(new Expander { Header = "字段与保真策略（选择快照将失去动态计算）", Content = fields });
        _tables.Children.Add(panel);
        _choices.Add(new(table, selected, target, choices));
        selected.Checked += (_, _) => InvalidatePreview();
        selected.Unchecked += (_, _) => InvalidatePreview();
        target.TextChanged += (_, _) => InvalidatePreview();
    }

    private async Task PreviewAsync()
    {
        if (_busy || _closing || _connection is null) return;
        TableChoice[] selected = _choices.Where(c => c.Selected.IsChecked == true).ToArray();
        if (selected.Length == 0) { _report.Text = "请至少选择一张来源表。"; return; }
        SetBusy(true);
        InvalidatePreview();
        _report.Text = "正在读取所选表并预检；此步骤不会创建目标表或写入记录…";
        try
        {
            string[] ids = selected.Select(c => c.Table.Id).ToArray();
            HostSourceImportDecision[] decisions = selected.SelectMany(table => table.Fields
                .Where(field => field.Policy.SelectedIndex != 0)
                .Select(field => new HostSourceImportDecision(table.Table.Id, field.Field.Id,
                    field.Policy.SelectedIndex == 1 ? "snapshot" : "skip",
                    field.Policy.SelectedIndex == 1 ? SnapshotTargetKind(field.Field) : "", "", true))).ToArray();
            var options = new HostSourceImportOptions(ids, selected.Select(c =>
                new HostSourceImportTargetName(c.Table.Id, c.Target.Text.Trim())).ToArray(), decisions,
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
        NonWritableSnapshotKinds.Contains(field.ValueKind) ? "json" : field.ValueKind;

    private void InvalidatePreview()
    {
        _preview = null;
        _startButton.IsEnabled = false;
        if (!_busy) _report.Text = "配置已变化，请重新预检。";
    }

    private void ConnectionChanged()
    {
        if (_busy || _closed) return;
        _session.DiscardPreview();
        _connection?.Dispose();
        _connection = null;
        _choices.Clear();
        _tables.Children.Clear();
        InvalidatePreview();
        _previewButton.IsEnabled = false;
        _status.Text = "连接配置已变化，请重新连接。";
    }

    private void SetBusy(bool value)
    {
        _busy = value;
        _credentials.IsEnabled = !value;
        _tables.IsEnabled = !value;
        _reverse.IsEnabled = !value;
        _previewButton.IsEnabled = !value && _connection is { Tables.Length: > 0 };
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
        _token.Clear();
        _secret.Clear();
        _session.Dispose();
        _connection?.Dispose();
        _lifetime.Dispose();
    }

    private sealed record FieldChoice(NativeSourceField Field, ComboBox Policy);
    private sealed record TableChoice(NativeSourceTable Table, CheckBox Selected, TextBox Target, List<FieldChoice> Fields);
}
