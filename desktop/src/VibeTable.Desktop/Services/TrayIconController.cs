using System;
using System.Drawing;
using System.Windows;
using System.Windows.Forms;
using System.Windows.Resources;
using WpfApplication = System.Windows.Application;

namespace VibeTable.Desktop.Services;

internal sealed class TrayIconController : IDisposable
{
    private readonly Icon _icon;
    private readonly ContextMenuStrip _menu;
    private readonly NotifyIcon _notifyIcon;
    private bool _disposed;

    public TrayIconController(Action restore, Action exit)
    {
        ArgumentNullException.ThrowIfNull(restore);
        ArgumentNullException.ThrowIfNull(exit);

        StreamResourceInfo resource = WpfApplication.GetResourceStream(
            new Uri("Assets/Brand/VibeTable.ico", UriKind.Relative))
            ?? throw new InvalidOperationException("The VibeTable tray icon is unavailable.");
        using (resource.Stream)
        using (var sourceIcon = new Icon(resource.Stream))
        {
            _icon = (Icon)sourceIcon.Clone();
        }

        _menu = new ContextMenuStrip();
        var openItem = new ToolStripMenuItem("打开 VibeTable");
        openItem.Click += (_, _) => restore();
        var exitItem = new ToolStripMenuItem("退出");
        exitItem.Click += (_, _) => exit();
        _menu.Items.Add(openItem);
        _menu.Items.Add(new ToolStripSeparator());
        _menu.Items.Add(exitItem);

        _notifyIcon = new NotifyIcon
        {
            ContextMenuStrip = _menu,
            Icon = _icon,
            Text = "VibeTable",
            Visible = false,
        };
        _notifyIcon.DoubleClick += (_, _) => restore();
    }

    public bool Visible
    {
        get => !_disposed && _notifyIcon.Visible;
        set
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            _notifyIcon.Visible = value;
        }
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _notifyIcon.Visible = false;
        _notifyIcon.Dispose();
        _menu.Dispose();
        _icon.Dispose();
    }
}
