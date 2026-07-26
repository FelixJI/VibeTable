using System;
using System.IO;
using System.Windows;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop;

public partial class MainWindow
{
    private bool OpenPocketBaseAdmin()
    {
        PocketBaseAdminContext? context = _sidecar.GetAdminContext();
        if (context is null) return false;

        foreach (Window candidate in Application.Current.Windows)
        {
            if (candidate is not PocketBaseAdminWindow existing) continue;
            if (existing.Origin == context.Origin)
            {
                if (existing.WindowState == WindowState.Minimized)
                {
                    existing.WindowState = WindowState.Normal;
                }
                existing.Activate();
                return true;
            }
            existing.Close();
        }

        string profileRoot = Path.Combine(
            _productDataRoot,
            "admin-webview",
            $"p{Environment.ProcessId}-{Guid.NewGuid():N}");
        var window = new PocketBaseAdminWindow(context, profileRoot)
        {
            Owner = this,
        };
        window.Show();
        return true;
    }
}
