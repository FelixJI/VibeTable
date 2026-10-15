using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Threading;
using System.Windows.Controls;
using System.Windows.Media;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Parsed pending-confirmation request extracted from a
/// <c>plugin.interaction.requested</c> envelope. Pure data — no WPF state — so
/// the #482 parser is unit-testable without STA.
/// </summary>
internal sealed record NativeConfirmationRequest(
    string ProjectKey,
    string PluginId,
    string ActionId,
    string RunId,
    string InteractionId,
    string Title,
    string Summary,
    int AffectedCount,
    string Risk,
    string Details = "")
{
    /// <summary>
    /// Parses the envelope payload. Accepts the typed
    /// <see cref="PluginEventEnvelope"/> produced by the host registry and a
    /// raw camelCase <see cref="JsonElement"/> (the same wire shape the web
    /// consumes). Returns null when the payload carries no pending
    /// confirmation.
    /// </summary>
    public static NativeConfirmationRequest? TryParse(object? envelopePayload)
    {
        JsonElement snapshot;
        string projectKey;
        if (envelopePayload is PluginEventEnvelope envelope)
        {
            snapshot = envelope.Snapshot;
            projectKey = envelope.ProjectKey;
        }
        else
        {
            try
            {
                JsonElement element = envelopePayload is JsonElement raw
                    ? raw
                    : JsonSerializer.SerializeToElement(envelopePayload, WebJson);
                if (element.ValueKind is not JsonValueKind.Object) return null;
                if (!element.TryGetProperty("snapshot", out snapshot)
                    || snapshot.ValueKind is not JsonValueKind.Object) return null;
                if (!element.TryGetProperty("projectKey", out JsonElement project)
                    || project.ValueKind is not JsonValueKind.String) return null;
                projectKey = project.GetString() ?? "";
            }
        catch (Exception ex) when (ex is JsonException or InvalidOperationException or NotSupportedException)
        {
            return null;
        }
        }

        try
        {
            string runId = ReadString(snapshot, "runId");
            string pluginId = ReadString(snapshot, "pluginId");
            string actionId = ReadString(snapshot, "actionId");
            if (!snapshot.TryGetProperty("pendingConfirmation", out JsonElement pending)
                || pending.ValueKind is not JsonValueKind.Object)
            {
                return null;
            }
            string interactionId = ReadString(pending, "interactionId");
            string title = ReadString(pending, "title");
            string risk = ReadString(pending, "risk");
            if (string.IsNullOrWhiteSpace(runId) || string.IsNullOrWhiteSpace(interactionId)
                || string.IsNullOrWhiteSpace(pluginId) || string.IsNullOrWhiteSpace(actionId))
            {
                return null;
            }
            int affectedCount = 0;
            if (pending.TryGetProperty("preview", out JsonElement preview)
                && preview.ValueKind is JsonValueKind.Object
                && preview.TryGetProperty("affectedCount", out JsonElement count)
                && count.ValueKind is JsonValueKind.Number
                && count.TryGetInt32(out affectedCount))
            {
            }
            string summary = "";
            if (pending.TryGetProperty("preview", out JsonElement previewRows)
                && previewRows.ValueKind is JsonValueKind.Object
                && previewRows.TryGetProperty("summary", out JsonElement summaryElement)
                && summaryElement.ValueKind is JsonValueKind.Array)
            {
                summary = string.Join(
                    " · ", summaryElement.EnumerateArray().Select(item => item.GetRawText()));
            }
            string details = previewRows.ValueKind == JsonValueKind.Object
                && previewRows.TryGetProperty("sampleRows", out JsonElement samples)
                && samples.ValueKind == JsonValueKind.Array && samples.GetArrayLength() > 0
                ? JsonSerializer.Serialize(samples, new JsonSerializerOptions { WriteIndented = true }) : "";
            return new NativeConfirmationRequest(
                projectKey, pluginId, actionId, runId, interactionId,
                string.IsNullOrWhiteSpace(title) ? "插件请求确认" : title,
                summary, affectedCount,
                string.IsNullOrWhiteSpace(risk) ? "write" : risk, details);
        }
        catch (Exception ex) when (ex is KeyNotFoundException or InvalidOperationException or JsonException or FormatException)
        {
            return null;
        }
    }

    private static string ReadString(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind is JsonValueKind.String
            ? value.GetString() ?? ""
            : "";

    private static readonly JsonSerializerOptions WebJson = new(JsonSerializerDefaults.Web);
}

/// <summary>
/// #482 native confirmation for plugin surface mutations. This window shows
/// the exact operation summary and resolves through the SAME one-shot
/// registry decision the trusted web footer uses for legacy surfaces (the
/// resolve callback is the dispatcher's ResolveInteraction path) — it never
/// owns a second approval state and can never be approved by plugin DOM.
/// For application/headless surfaces it is the ONLY approval entry. Closing
/// the window without a decision rejects the pending interaction through
/// the same registry; an expired or retired interaction cannot be approved.
/// </summary>
public sealed class NativePluginConfirmationWindow : Window
{
    private readonly Func<PluginResolveInteractionParams, Task> _resolve;
    private readonly string _runId;
    private readonly string _interactionId;
    private bool _decided;
    private readonly DispatcherTimer _expiry;

    internal string ProjectKey { get; }
    internal string PluginId { get; }
    internal string ActionId { get; }

    private NativePluginConfirmationWindow(
        Window owner,
        Func<PluginResolveInteractionParams, Task> resolve,
        NativeConfirmationRequest request,
        Func<PluginResolveInteractionParams, bool> isPending)
    {
        _resolve = resolve;
        _runId = request.RunId;
        _interactionId = request.InteractionId;
        ProjectKey = request.ProjectKey;
        PluginId = request.PluginId;
        ActionId = request.ActionId;
        Title = "VibeTable 插件确认";
        Owner = owner;
        ShowInTaskbar = false;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        SizeToContent = SizeToContent.Height;
        ResizeMode = ResizeMode.NoResize;
        Width = 460;
        MaxWidth = 520;
        SetResourceReference(BackgroundProperty, "VtSurfaceBackgroundBrush");
        SetResourceReference(ForegroundProperty, "VtWindowTextBrush");

        var panel = new StackPanel { Margin = new Thickness(18) };
        var badge = new TextBlock
        {
            Text = $"HOST FINAL {request.Risk.ToUpperInvariant()} CONFIRMATION",
            FontSize = 10,
            FontWeight = FontWeights.Bold,
            Margin = new Thickness(0, 0, 0, 6),
        };
        badge.SetResourceReference(TextBlock.ForegroundProperty, "VtMutedTextBrush");
        panel.Children.Add(badge);
        // Host-trusted PluginId/ActionId (not the plugin-authored title) tell the user which plugin is asking to write.
        var identity = new TextBlock
        {
            Text = $"插件 {request.PluginId} · 动作 {request.ActionId}",
            FontSize = 12,
            FontWeight = FontWeights.SemiBold,
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 8),
        };
        AutomationProperties.SetAutomationId(identity, "plugin-confirm-identity");
        AutomationProperties.SetName(identity, $"来源插件 {request.PluginId}，动作 {request.ActionId}");
        panel.Children.Add(identity);
        panel.Children.Add(new TextBlock
        {
            Text = request.Title,
            FontSize = 15,
            FontWeight = FontWeights.SemiBold,
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 6),
        });
        panel.Children.Add(new TextBlock
        {
            Text = string.IsNullOrWhiteSpace(request.Summary)
                ? $"将影响 {request.AffectedCount} 条记录"
                : $"{request.Summary}（共 {request.AffectedCount} 条）",
            FontSize = 12,
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 14),
        });
        if (!string.IsNullOrWhiteSpace(request.Details))
        {
            panel.Children.Add(new TextBlock
            {
                Text = "当前计划的操作样本（最多 10 条）", Margin = new Thickness(0, 0, 0, 4),
            });
            panel.Children.Add(new TextBox
            {
                Text = request.Details, IsReadOnly = true, TextWrapping = TextWrapping.Wrap,
                VerticalScrollBarVisibility = ScrollBarVisibility.Auto, MaxHeight = 240,
                Margin = new Thickness(0, 0, 0, 14),
            });
        }
        var actions = new StackPanel { Orientation = Orientation.Horizontal, HorizontalAlignment = HorizontalAlignment.Right };
        var reject = new Button { Content = "拒绝", Padding = new Thickness(14, 5, 14, 5), Margin = new Thickness(0, 0, 8, 0) };
        AutomationProperties.SetAutomationId(reject, "plugin-confirm-reject");
        reject.IsCancel = true;
        reject.Click += (_, _) => Decide("rejected");
        var approve = new Button { Content = "由宿主确认并继续", Padding = new Thickness(14, 5, 14, 5), FontWeight = FontWeights.SemiBold };
        AutomationProperties.SetAutomationId(approve, "plugin-confirm-approve");
        approve.Click += (_, _) => Decide("approved");
        actions.Children.Add(reject);
        actions.Children.Add(approve);
        panel.Children.Add(actions);
        Content = panel;
        _expiry = new DispatcherTimer(TimeSpan.FromMilliseconds(250), DispatcherPriority.Normal,
            (_, _) =>
            {
                if (!isPending(new(_runId, _interactionId, "approved")))
                {
                    _decided = true;
                    approve.IsEnabled = false;
                    Close();
                }
            }, Dispatcher);
        Loaded += (_, _) => reject.Focus();
        Closing += (_, _) =>
        {
            _expiry.Stop();
            if (!_decided)
            {
                _decided = true;
                _ = _resolve(new(_runId, _interactionId, "rejected"));
            }
        };
    }

    private async void Decide(string decision)
    {
        if (_decided)
        {
            return;
        }
        _decided = true;
        try
        {
            await _resolve(new PluginResolveInteractionParams(_runId, _interactionId, decision));
        }
        catch
        {
            // The one-shot registry decision answers already-resolved/expired
            // interactions with a status; transport-level failures surface
            // through the task state instead of a second dialog.
        }
        Close();
    }

    /// <summary>
    /// Builds the window from a plugin.interaction.requested envelope payload.
    /// Returns null when the payload does not carry a pending confirmation.
    /// </summary>
    public static NativePluginConfirmationWindow? TryCreate(
        Window owner,
        object? envelopePayload,
        Func<PluginResolveInteractionParams, Task> resolve,
        Func<PluginResolveInteractionParams, bool> isPending)
    {
        NativeConfirmationRequest? request = NativeConfirmationRequest.TryParse(envelopePayload);
        return request is null || !isPending(new(request.RunId, request.InteractionId, "approved"))
            ? null : new NativePluginConfirmationWindow(owner, resolve, request, isPending);
    }
}
