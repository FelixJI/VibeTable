using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Threading;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;

namespace VibeTable.Desktop.Tests;

[TestClass]
public class RendererRecoveryMenuTests
{
    [STATestMethod]
    public void ClosingTheWindowDisposesTheMenuAfterItsHwndSourceWasDestroyed()
    {
        var window = new Window();
        IntPtr hwnd = new WindowInteropHelper(window).EnsureHandle();
        var source = HwndSource.FromHwnd(hwnd)!;
        var command = new RelayCommand(() => Assert.Fail("a closing window cannot start recovery"));
        var recovery = new RendererRecoveryMenu(source, command);
        bool closed = false;
        window.Closed += (_, _) =>
        {
            recovery.Dispose();
            closed = true;
        };
        window.Close();
        Assert.IsTrue(closed);
        recovery.Dispose();
        command.RaiseCanExecuteChanged();
    }

    [STATestMethod]
    public void WindowMenuUsesTheExistingRecoveryGateAndDetachesOnDispose()
    {
        var window = new Window();
        IntPtr hwnd = new WindowInteropHelper(window).EnsureHandle();
        var source = HwndSource.FromHwnd(hwnd)!;
        bool enabled = false;
        int calls = 0;
        var command = new RelayCommand(() => calls++, () => enabled);
        using var recovery = new RendererRecoveryMenu(source, command);
        try
        {
            IntPtr menu = GetSystemMenu(hwnd, false);
            var label = new StringBuilder(100);
            Assert.IsTrue(GetMenuStringW(menu, RendererRecoveryMenu.CommandId, label, label.Capacity, 0) > 0);
            Assert.AreEqual(RendererRecoveryMenu.Label, label.ToString());
            Assert.AreEqual(1u, GetMenuState(menu, RendererRecoveryMenu.CommandId, 0) & 1);
            SendMessageW(hwnd, 0x0112, (IntPtr)RendererRecoveryMenu.CommandId, IntPtr.Zero);
            PumpDispatcher();
            Assert.AreEqual(0, calls, "a disabled recovery cannot be invoked through a native message");

            enabled = true;
            command.RaiseCanExecuteChanged();
            Assert.AreEqual(0u, GetMenuState(menu, RendererRecoveryMenu.CommandId, 0) & 1);
            SendMessageW(hwnd, 0x0112, (IntPtr)RendererRecoveryMenu.CommandId, IntPtr.Zero);
            Assert.AreEqual(0, calls, "recovery must leave the menu callback stack before replacing WebView");
            PumpDispatcher();
            Assert.AreEqual(1, calls);

            recovery.Dispose();
            Assert.AreEqual(uint.MaxValue, GetMenuState(menu, RendererRecoveryMenu.CommandId, 0));
            SendMessageW(hwnd, 0x0112, (IntPtr)RendererRecoveryMenu.CommandId, IntPtr.Zero);
            PumpDispatcher();
            Assert.AreEqual(1, calls, "the window hook was removed");
        }
        finally
        {
            recovery.Dispose();
            window.Close();
        }
    }

    private static void PumpDispatcher()
    {
        var frame = new DispatcherFrame();
        Dispatcher.CurrentDispatcher.BeginInvoke(DispatcherPriority.Background, () => frame.Continue = false);
        Dispatcher.PushFrame(frame);
    }

    [DllImport("user32.dll")]
    private static extern IntPtr GetSystemMenu(IntPtr hwnd, bool revert);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetMenuStringW(IntPtr menu, uint item, StringBuilder text, int maximum, uint flags);

    [DllImport("user32.dll")]
    private static extern uint GetMenuState(IntPtr menu, uint item, uint flags);

    [DllImport("user32.dll")]
    private static extern IntPtr SendMessageW(IntPtr hwnd, uint message, IntPtr wParam, IntPtr lParam);
}
