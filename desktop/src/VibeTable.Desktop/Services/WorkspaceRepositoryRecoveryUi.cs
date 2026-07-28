using System.ComponentModel;
using System.Windows;
using System.Windows.Controls;

namespace VibeTable.Desktop.Services;

public interface IWorkspaceRepositoryRecoveryUi
{
    void ConfirmRecoveryKey(string workspaceName, string recoveryKey);
    string? PromptRecoveryKey(string workspaceName);
}

/// <summary>
/// Native trusted UI for protected repository recovery material. Recovery
/// keys never enter WebView messages or renderer state.
/// </summary>
public sealed class WorkspaceRepositoryRecoveryUi :
    IWorkspaceRepositoryRecoveryUi
{
    public void ConfirmRecoveryKey(
        string workspaceName,
        string recoveryKey)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceName);
        ArgumentException.ThrowIfNullOrWhiteSpace(recoveryKey);
        var confirmed = new CheckBox
        {
            Content = "我已将恢复密钥保存到安全位置",
            Margin = new Thickness(0, 16, 0, 12),
        };
        var button = new Button
        {
            Content = "确认并继续",
            IsDefault = true,
            IsEnabled = false,
            MinWidth = 120,
            HorizontalAlignment = HorizontalAlignment.Right,
        };
        confirmed.Checked += (_, _) => button.IsEnabled = true;
        confirmed.Unchecked += (_, _) => button.IsEnabled = false;
        var key = new TextBox
        {
            Text = recoveryKey,
            IsReadOnly = true,
            TextWrapping = TextWrapping.Wrap,
            FontFamily = new System.Windows.Media.FontFamily("Consolas"),
        };
        var panel = new StackPanel
        {
            Margin = new Thickness(24),
        };
        panel.Children.Add(new TextBlock
        {
            Text = $"“{workspaceName}”已启用受保护加密。请立即复制恢复密钥；遗失后无法恢复。",
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 12),
        });
        panel.Children.Add(key);
        panel.Children.Add(confirmed);
        panel.Children.Add(button);
        var window = CreateWindow("保存工作区恢复密钥", panel);
        bool accepted = false;
        button.Click += (_, _) =>
        {
            accepted = true;
            window.DialogResult = true;
        };
        window.Closing += (_, args) =>
        {
            if (!accepted)
                args.Cancel = true;
        };
        _ = window.ShowDialog();
    }

    public string? PromptRecoveryKey(string workspaceName)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceName);
        var input = new TextBox
        {
            MinWidth = 480,
            FontFamily = new System.Windows.Media.FontFamily("Consolas"),
        };
        var confirm = new Button
        {
            Content = "验证并解锁",
            IsDefault = true,
            MinWidth = 110,
            HorizontalAlignment = HorizontalAlignment.Right,
            Margin = new Thickness(0, 16, 0, 0),
        };
        var panel = new StackPanel { Margin = new Thickness(24) };
        panel.Children.Add(new TextBlock
        {
            Text = $"请输入“{workspaceName}”的恢复密钥。密钥只会发送给本机受信 Sidecar。",
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 12),
        });
        panel.Children.Add(input);
        panel.Children.Add(confirm);
        var window = CreateWindow("解锁受保护工作区", panel);
        confirm.Click += (_, _) => window.DialogResult = true;
        bool? result = window.ShowDialog();
        return result == true && !string.IsNullOrWhiteSpace(input.Text)
            ? input.Text.Trim()
            : null;
    }

    private static Window CreateWindow(string title, UIElement content)
        => new()
        {
            Title = title,
            Content = content,
            Owner = Application.Current?.MainWindow,
            WindowStartupLocation = WindowStartupLocation.CenterOwner,
            SizeToContent = SizeToContent.WidthAndHeight,
            ResizeMode = ResizeMode.NoResize,
            ShowInTaskbar = false,
        };
}
