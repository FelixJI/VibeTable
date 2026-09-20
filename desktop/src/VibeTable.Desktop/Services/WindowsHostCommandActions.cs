using System.Diagnostics;
using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Automation;

namespace VibeTable.Desktop.Services;

internal sealed class WindowsHostCommandActions(
    Window owner, Func<IHostCommandExportGateway?> gateway,
    INativeProductFileHost files, string? controlsDirectory) : IHostCommandActions
{
    public async Task<JsonElement> ExportAsync(JsonElement parameters, Action ensureCurrent, CancellationToken token)
    {
        ensureCurrent();
        IHostCommandExportGateway captured = gateway() ?? throw new InvalidOperationException("Backend unavailable.");
        void Current()
        {
            ensureCurrent();
            if (!ReferenceEquals(captured, gateway())) throw new OperationCanceledException("Backend binding changed.");
        }
        string format = parameters.GetProperty("format").GetString()!;
        string? path = files.SelectExportTarget(format, $"vibetable-export.{format}");
        Current();
        if (path is null) throw new OperationCanceledException("Export cancelled.");
        JsonElement grant = await captured.RegisterExportTargetAsync(JsonSerializer.SerializeToElement(new { path }), token);
        Current();
        var taskParameters = JsonNode.Parse(parameters.GetRawText())!.AsObject();
        taskParameters["grantId"] = grant.GetProperty("grantId").GetString();
        taskParameters["includeRelations"] = true;
        taskParameters["lookupIds"] = new JsonArray();
        return await captured.ExecuteExportAsync(JsonSerializer.SerializeToElement(taskParameters), token);
    }

    public async Task<bool> OpenHttpsAsync(Uri uri, Action ensureCurrent, CancellationToken token)
    {
        ensureCurrent();
        bool confirmed = await ConfirmAsync(uri, token);
        ensureCurrent();
        if (!confirmed) return false;
        using Process? process = Process.Start(new ProcessStartInfo(uri.AbsoluteUri) { UseShellExecute = true });
        return true;
    }

    private Task<bool> ConfirmAsync(Uri uri, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        var completion = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var dialog = new Window
        {
            Owner = owner, Title = "打开外部链接 / Open external link", Width = 480,
            SizeToContent = SizeToContent.Height, ResizeMode = ResizeMode.NoResize,
            WindowStartupLocation = WindowStartupLocation.CenterOwner,
            ShowInTaskbar = false,
        };
        AutomationProperties.SetAutomationId(dialog, "command-https-confirmation");
        var panel = new StackPanel { Margin = new Thickness(20) };
        panel.Children.Add(new TextBlock { Text = "将在默认浏览器中打开 / Open in your default browser:", TextWrapping = TextWrapping.Wrap });
        panel.Children.Add(new TextBlock { Text = uri.AbsoluteUri, Margin = new Thickness(0, 12, 0, 16), TextWrapping = TextWrapping.Wrap });
        var buttons = new StackPanel { Orientation = Orientation.Horizontal, HorizontalAlignment = HorizontalAlignment.Right };
        var cancel = new Button { Content = "取消 / Cancel", MinWidth = 90, Margin = new Thickness(0, 0, 8, 0), IsCancel = true };
        var open = new Button { Content = "打开 / Open", MinWidth = 90 };
        AutomationProperties.SetAutomationId(cancel, "command-https-cancel");
        AutomationProperties.SetAutomationId(open, "command-https-open");
        bool confirmed = false;
        cancel.Click += (_, _) => dialog.Close();
        open.Click += (_, _) => { confirmed = true; dialog.Close(); };
        buttons.Children.Add(cancel); buttons.Children.Add(open); panel.Children.Add(buttons);
        dialog.Content = panel;
        var registration = token.Register(() => dialog.Dispatcher.BeginInvoke(new Action(dialog.Close)));
        dialog.Closed += (_, _) =>
        {
            registration.Dispose();
            if (token.IsCancellationRequested) completion.TrySetCanceled(token);
            else completion.TrySetResult(confirmed);
        };
        dialog.Show();
        cancel.Focus();
        if (controlsDirectory is not null)
        {
            // Product E2E uses the real confirmation window and exercises refusal without opening a browser.
            string decision = Path.Combine(controlsDirectory, "command-https-cancel.txt");
            if (File.Exists(decision))
                dialog.Dispatcher.BeginInvoke(new Action(() => cancel.RaiseEvent(new RoutedEventArgs(Button.ClickEvent))));
        }
        return completion.Task;
    }
}
