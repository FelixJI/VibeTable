using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Windows.Input;
using System.Windows.Interop;

namespace VibeTable.Desktop.Services;

/// <summary>A native window-menu escape hatch that remains usable when the renderer hangs.</summary>
internal sealed class RendererRecoveryMenu : IDisposable
{
    internal const uint CommandId = 0x1000;
    internal const string Label = "恢复界面(&U)";
    private readonly HwndSource _source;
    private readonly ICommand _command;
    private readonly IntPtr _menu;
    private bool _disposed;

    public RendererRecoveryMenu(HwndSource source, ICommand command)
    {
        _source = source;
        _command = command;
        _menu = GetSystemMenu(source.Handle, false);
        if (_menu == IntPtr.Zero || !AppendMenuW(_menu, 0, CommandId, Label))
            throw new Win32Exception(Marshal.GetLastWin32Error(), "无法添加界面恢复菜单。");
        _source.AddHook(OnWindowMessage);
        _command.CanExecuteChanged += OnCanExecuteChanged;
        UpdateEnabled();
    }

    private void OnCanExecuteChanged(object? sender, EventArgs args) => UpdateEnabled();

    private void UpdateEnabled() => EnableMenuItem(_menu, CommandId, _command.CanExecute(null) ? 0u : 1u);

    private IntPtr OnWindowMessage(IntPtr hwnd, int message, IntPtr wParam, IntPtr lParam, ref bool handled)
    {
        if (message == 0x0116 && wParam == _menu) UpdateEnabled(); // WM_INITMENU
        if (message == 0x0112 && (wParam.ToInt64() & 0xFFF0) == CommandId) // WM_SYSCOMMAND
        {
            handled = true;
            // Leave the native menu message stack before disposing/rebuilding WebView2.
            _source.Dispatcher.BeginInvoke(() =>
            {
                if (!_disposed && _command.CanExecute(null)) _command.Execute(null);
            });
        }
        return IntPtr.Zero;
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _command.CanExecuteChanged -= OnCanExecuteChanged;
        if (!_source.IsDisposed) _source.RemoveHook(OnWindowMessage);
        DeleteMenu(_menu, CommandId, 0);
    }

    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr GetSystemMenu(IntPtr hwnd, bool revert);

    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool AppendMenuW(IntPtr menu, uint flags, nuint item, string text);

    [DllImport("user32.dll")]
    private static extern uint EnableMenuItem(IntPtr menu, uint item, uint flags);

    [DllImport("user32.dll")]
    private static extern bool DeleteMenu(IntPtr menu, uint item, uint flags);
}
