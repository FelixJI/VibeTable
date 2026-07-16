using System;
using System.Threading;
using System.Windows;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Desktop;

public partial class DirectusLoginWindow : Window
{
    private readonly IDirectusRpcGateway _gateway;
    private readonly DirectusLoginStore _loginStore;
    private readonly bool _managedPassword;

    public DirectusLoginWindow(IDirectusRpcGateway gateway, DirectusLoginStore loginStore)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _loginStore = loginStore ?? throw new ArgumentNullException(nameof(loginStore));
        InitializeComponent();
        var preferences = _loginStore.LoadPreferences();
        _managedPassword = preferences.ManagedPassword;
        EmailBox.Text = preferences.Email;
        RememberPasswordBox.IsChecked = preferences.RememberPassword;
        AutoLoginBox.IsChecked = preferences.AutoLogin;
        if (preferences.RememberPassword)
        {
            PasswordBox.Password = _loginStore.LoadPassword() ?? string.Empty;
        }
        UpdateRememberState();
    }

    private void OnRememberChanged(object sender, RoutedEventArgs e) => UpdateRememberState();

    private void UpdateRememberState()
    {
        if (AutoLoginBox is null)
        {
            return;
        }
        bool remember = RememberPasswordBox.IsChecked == true;
        AutoLoginBox.IsEnabled = remember;
        if (!remember)
        {
            AutoLoginBox.IsChecked = false;
        }
    }

    private async void OnLoginClick(object sender, RoutedEventArgs e)
    {
        string email = EmailBox.Text.Trim();
        string password = PasswordBox.Password;
        if (email.Length == 0 || password.Length == 0)
        {
            ErrorText.Text = "请输入邮箱和密码。";
            return;
        }

        LoginButton.IsEnabled = false;
        ErrorText.Text = string.Empty;
        try
        {
            string? otp = string.IsNullOrWhiteSpace(OtpBox.Text) ? null : OtpBox.Text.Trim();
            var status = await _gateway.LoginAsync(
                email, password, otp, CancellationToken.None);
            if (!string.Equals(status.State, "authenticated", StringComparison.Ordinal))
            {
                ErrorText.Text = "登录未建立有效会话，请重试。";
                return;
            }
            bool remember = RememberPasswordBox.IsChecked == true;
            _loginStore.Save(
                new DirectusLoginPreferences(
                    email,
                    remember,
                    remember && AutoLoginBox.IsChecked == true,
                    ManagedPassword: _managedPassword),
                remember ? password : null);
            DialogResult = true;
        }
        catch (Exception ex)
        {
            ErrorText.Text = ex.Message;
            PasswordBox.Clear();
            PasswordBox.Focus();
        }
        finally
        {
            LoginButton.IsEnabled = true;
        }
    }
}
