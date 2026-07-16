using System;
using System.Security.Cryptography;
using System.Windows;

namespace VibeTable.Desktop;

public partial class DirectusFirstRunWindow : Window
{
    public DirectusFirstRunWindow()
    {
        InitializeComponent();
        UpdateMode();
    }

    public string Email { get; private set; } = string.Empty;
    public string Password { get; private set; } = string.Empty;
    public bool ManagedLogin { get; private set; }
    public bool RememberPassword { get; private set; }
    public bool AutoLogin { get; private set; }

    private void OnModeChanged(object sender, RoutedEventArgs e) => UpdateMode();

    private void UpdateMode()
    {
        if (PasswordPanel is null)
        {
            return;
        }
        bool managed = ManagedLoginBox.IsChecked == true;
        PasswordPanel.IsEnabled = !managed;
        RememberPasswordBox.IsChecked = true;
        RememberPasswordBox.IsEnabled = !managed;
        AutoLoginBox.IsChecked = true;
        AutoLoginBox.IsEnabled = !managed;
    }

    private void OnConfirmClick(object sender, RoutedEventArgs e)
    {
        string email = EmailBox.Text.Trim();
        if (!LooksLikeEmail(email))
        {
            ErrorText.Text = "请输入有效邮箱地址，例如 admin@company.com。";
            EmailBox.Focus();
            return;
        }

        bool managed = ManagedLoginBox.IsChecked == true;
        string password;
        if (managed)
        {
            password = Convert.ToBase64String(RandomNumberGenerator.GetBytes(32));
        }
        else
        {
            password = PasswordBox.Password;
            if (password.Length < 8)
            {
                ErrorText.Text = "密码至少需要 8 位。";
                PasswordBox.Focus();
                return;
            }
            if (!string.Equals(password, ConfirmPasswordBox.Password, StringComparison.Ordinal))
            {
                ErrorText.Text = "两次输入的密码不一致。";
                ConfirmPasswordBox.Focus();
                return;
            }
        }

        bool remember = managed || RememberPasswordBox.IsChecked == true;
        bool autoLogin = managed || AutoLoginBox.IsChecked == true;
        if (autoLogin && !remember)
        {
            ErrorText.Text = "启用自动登录时必须同时保存密码。";
            return;
        }

        Email = email;
        Password = password;
        ManagedLogin = managed;
        RememberPassword = remember;
        AutoLogin = autoLogin;
        DialogResult = true;
    }

    private static bool LooksLikeEmail(string value)
    {
        int at = value.IndexOf('@');
        int dot = value.LastIndexOf('.');
        return at > 0 && dot > at + 1 && dot < value.Length - 1;
    }
}
