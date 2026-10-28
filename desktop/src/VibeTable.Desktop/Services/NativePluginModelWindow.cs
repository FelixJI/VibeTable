using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;

namespace VibeTable.Desktop.Services;

/// <summary>Trusted native picker. The plugin never receives the workspace catalog.</summary>
internal sealed class NativePluginModelWindow : Window
{
    private sealed record Choice(string Id, string Label) { public override string ToString() => Label; }
    private readonly StackPanel _body = new() { Margin = new Thickness(20) };
    private readonly StackPanel _buttons = new() { Margin = new Thickness(20, 0, 20, 12) };
    private readonly Dictionary<string, ComboBox> _tables = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Dictionary<string, ComboBox>> _fields = new(StringComparer.Ordinal);
    internal JsonElement? Mappings { get; private set; }

    private NativePluginModelWindow(string title)
    {
        Title = title; Width = 680; Height = 620; MinWidth = 520; MinHeight = 380;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        AutomationProperties.SetAutomationId(this, "PluginModelWindow");
        var layout = new DockPanel(); DockPanel.SetDock(_buttons, Dock.Bottom); layout.Children.Add(_buttons);
        layout.Children.Add(new ScrollViewer { Content = _body, VerticalScrollBarVisibility = ScrollBarVisibility.Auto }); Content = layout;
    }

    internal static async Task<JsonElement?> ChooseMappingAsync(JsonElement catalog,
        Func<string, string, CancellationToken, Task<JsonElement>> readFields, CancellationToken token)
    {
        using var lifetime = CancellationTokenSource.CreateLinkedTokenSource(token);
        return await ShowAsync(() =>
        {
            var window = new NativePluginModelWindow("映射插件数据模型");
            window.Closed += (_, _) => lifetime.Cancel();
            window.Line("请选择现有表和字段。只接受类型、精度及关系兼容的精确映射；不会改写现有值。");
            JsonElement[] tables = catalog.GetProperty("tables").EnumerateArray().Select(t => t.Clone()).ToArray();
            foreach (var entity in catalog.GetProperty("model").GetProperty("entities").EnumerateArray())
            {
                string key = entity.GetProperty("key").GetString()!;
                window.Line(entity.GetProperty("label").GetString()!);
                var table = new ComboBox { Margin = new Thickness(0, 4, 0, 8), MinHeight = 30 };
                AutomationProperties.SetAutomationId(table, $"PluginModelTable_{key}");
                foreach (var candidate in tables) table.Items.Add(new Choice(candidate.GetProperty("tableId").GetString()!, candidate.GetProperty("displayName").GetString()!));
                window._tables[key] = table; window._body.Children.Add(table);
                var fields = new Dictionary<string, ComboBox>(StringComparer.Ordinal);
                foreach (var field in entity.GetProperty("fields").EnumerateArray())
                {
                    string fieldKey = field.GetProperty("key").GetString()!;
                    window.Line($"{field.GetProperty("label").GetString()} · {TypeLabel(field)}");
                    var choice = new ComboBox { Margin = new Thickness(12, 2, 0, 8), MinHeight = 28 };
                    AutomationProperties.SetAutomationId(choice, $"PluginModelField_{key}_{fieldKey}");
                    fields[fieldKey] = choice; window._body.Children.Add(choice);
                }
                window._fields[key] = fields;
                var error = new TextBlock { TextWrapping = TextWrapping.Wrap };
                window._body.Children.Add(error);
                int selectionVersion = 0;
                table.SelectionChanged += async (_, _) =>
                {
                    int version = ++selectionVersion;
                    foreach (var field in fields.Values) field.Items.Clear();
                    error.Text = "";
                    if (table.SelectedItem is not Choice selected) return;
                    table.IsEnabled = false;
                    try
                    {
                        var summary = tables.First(t => t.GetProperty("tableId").GetString() == selected.Id);
                        var schema = await readFields(selected.Id, summary.GetProperty("schemaRevision").GetString()!, lifetime.Token);
                        if (lifetime.IsCancellationRequested || version != selectionVersion) return;
                        foreach (var actual in schema.GetProperty("fields").EnumerateArray())
                        {
                            var item = new Choice(actual.GetProperty("fieldId").GetString()!, $"{actual.GetProperty("displayName").GetString()} · {actual.GetProperty("logicalType").GetString()}");
                            foreach (var field in fields.Values) field.Items.Add(item);
                        }
                    }
                    catch (OperationCanceledException) when (lifetime.IsCancellationRequested) { }
                    catch (Exception) { if (version == selectionVersion) error.Text = "字段目录读取失败或结构已变化，请关闭后重新配置。"; }
                    finally { if (version == selectionVersion) table.IsEnabled = true; }
                };
            }
            window.Loaded += (_, _) => window._tables.Values.FirstOrDefault()?.Focus();
            window.Button("检查映射并预览", "plugin-model-preview", () =>
            {
                var mappings = new Dictionary<string, object>(StringComparer.Ordinal);
                foreach (var (entity, table) in window._tables)
                {
                    if (table.SelectedItem is not Choice selected || window._fields[entity].Values.Any(field => field.SelectedItem is not Choice)) return;
                    mappings[entity] = new { tableId = selected.Id, fields = window._fields[entity].ToDictionary(pair => pair.Key, pair => ((Choice)pair.Value.SelectedItem).Id) };
                }
                window.Mappings = JsonSerializer.SerializeToElement(mappings); window.DialogResult = true;
            });
            return window;
        }, token);
    }

    internal static async Task<bool> ConfirmAsync(JsonElement preview, CancellationToken token)
    {
        JsonElement? confirmed = await ShowAsync(() =>
        {
            var window = new NativePluginModelWindow("确认插件数据模型精确计划");
            bool blocked = preview.GetProperty("diagnostics").GetArrayLength() > 0;
            window.Line("仅在整个模型完成后授予下列字段权限。已存在的业务值及用户扩展字段会保留。中断后可查询已完成范围。");
            var bindings = preview.GetProperty("bindings");
            foreach (var entity in preview.GetProperty("model").GetProperty("entities").EnumerateArray())
            {
                string key = entity.GetProperty("key").GetString()!;
                string target = bindings.TryGetProperty(key, out var binding) ? binding.GetProperty("tableId").GetString()! : "尚未选择";
                window.Line($"{entity.GetProperty("label").GetString()} → {target}");
                window.Line("允许操作：" + string.Join("、", entity.GetProperty("operations").EnumerateArray().Select(op => OperationLabel(op.GetString()!))));
                foreach (var field in entity.GetProperty("fields").EnumerateArray())
                {
                    string fieldKey = field.GetProperty("key").GetString()!;
                    string mapped = binding.ValueKind == JsonValueKind.Object && binding.GetProperty("fields").TryGetProperty(fieldKey, out var id) ? id.GetString()! : "新建";
                    window.Line($"  {field.GetProperty("label").GetString()} · {TypeLabel(field)} → {mapped}");
                }
            }
            foreach (var diagnostic in preview.GetProperty("diagnostics").EnumerateArray())
                window.Line($"拒绝：{diagnostic.GetProperty("path").GetString()} — {DiagnosticLabel(diagnostic.GetProperty("code").GetString()!)}");
            if (!blocked) window.Button("确认此精确计划", "plugin-model-confirm", () => { window.Mappings = JsonSerializer.SerializeToElement(true); window.DialogResult = true; });
            window.Button(blocked ? "关闭诊断" : "取消", "plugin-model-cancel", () => window.DialogResult = false);
            return window;
        }, token);
        return confirmed is not null;
    }

    private void Line(string text) => _body.Children.Add(new TextBlock { Text = text, TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 4, 0, 4) });
    private void Button(string text, string id, Action action)
    {
        var button = new Button { Content = text, MinHeight = 34, Margin = new Thickness(0, 12, 0, 0) };
        AutomationProperties.SetAutomationId(button, id); button.Click += (_, _) => action(); _buttons.Children.Add(button);
    }
    private static string TypeLabel(JsonElement field)
    {
        string type = field.GetProperty("type").GetString()!;
        if (field.TryGetProperty("precision", out var precision)) type += precision.GetString() == "integer" ? "（整数）" : "（64 位浮点）";
        if (field.TryGetProperty("relation", out var relation)) type += $" → {relation.GetProperty("entity").GetString()}.{relation.GetProperty("field").GetString()}（{relation.GetProperty("cardinality").GetString()}）";
        return type;
    }
    private static string OperationLabel(string value) => value switch { "read" => "读取", "query" => "查询", "create" => "新增记录", "update" => "修改记录", "delete" => "删除记录", _ => value };
    private static string DiagnosticLabel(string code) => code switch
    {
        "plugin_model_precision_loss" => "数字精度不兼容，禁止自动转换或舍入",
        "plugin_model_type_mismatch" => "字段类型不兼容",
        "plugin_model_relation_mismatch" => "关联目标、方向或基数不兼容",
        "plugin_model_name_conflict" => "名称与现有对象冲突，请手动映射",
        "plugin_model_mapping_required" => "必须选择已有对象",
        "plugin_model_duplicate_mapping" => "多个逻辑对象不能映射到同一对象",
        "plugin_plan_stale" => "表结构在确认后发生变化，旧计划已失效；已完成部分仍保留",
        _ => code,
    };
    private static async Task<JsonElement?> ShowAsync(Func<NativePluginModelWindow> create, CancellationToken token)
    {
        var application = Application.Current ?? throw new PluginSurfacePolicyException("plugin_api_unsupported", "原生模型窗口不可用。");
        return await application.Dispatcher.InvokeAsync(() =>
        {
            token.ThrowIfCancellationRequested();
            var window = create(); if (application.MainWindow is { IsVisible: true } owner) window.Owner = owner;
            using var registration = token.Register(() => window.Dispatcher.BeginInvoke(() => { if (window.IsVisible) window.Close(); }));
            bool? result = window.ShowDialog(); token.ThrowIfCancellationRequested();
            return result == true ? window.Mappings : null;
        });
    }
}
