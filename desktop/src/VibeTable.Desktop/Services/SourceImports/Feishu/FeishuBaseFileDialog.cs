using System.Runtime.InteropServices;
using System.Text;
using System.Windows;
using System.Windows.Interop;

namespace VibeTable.Desktop.Services;

/// <summary>Cancellation-support failure of the owned .base picker. The
/// dialog is never auto-closed on this path; the user closes it manually.</summary>
internal sealed class FeishuBaseFileDialogException : Exception
{
    internal FeishuBaseFileDialogException(string message) : base(message) { }
}

/// <summary>
/// The Feishu .base picker as a native modal owned by the wizard window.
/// WPF's OpenFileDialog is sealed and its Vista IFileDialog path never calls
/// CommonDialog.HookProc, so a thread-local WH_CBT hook delivers
/// cancellation: the dialog HWND is accepted only when it is a system
/// "#32770" dialog directly owned by the explicit owner window on the arming
/// UI thread and process, and WM_CLOSE is posted to that same re-verified
/// handle. PostMessage success only proves queuing — actual dismissal is
/// observed when ShowDialog returns; a failed delivery never auto-closes the
/// dialog.
/// </summary>
internal static class FeishuBaseFileDialog
{
    internal const string Title = "导入飞书 .base 文件";
    internal const string Filter = "飞书多维表格 (*.base)|*.base";

    internal static FeishuBaseFileDialogException Fault(string message) => new(message);

    /// <summary>Returns the chosen path (or null when dismissed). Throws
    /// OperationCanceledException for a cancelled token and
    /// FeishuBaseFileDialogException when cancellable observation could not be
    /// established or a requested close was undeliverable.</summary>
    internal static Task<string?> PickFileAsync(Window owner, CancellationToken token,
        Action<IntPtr>? onDialogShown = null)
    {
        ArgumentNullException.ThrowIfNull(owner);
        token.ThrowIfCancellationRequested();
        using CbtOwnedDialogCloser closer = CbtOwnedDialogCloser.Arm(owner, onDialogShown);
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            Title = Title,
            Filter = Filter,
            CheckFileExists = true,
            CheckPathExists = true,
            Multiselect = false,
            RestoreDirectory = true,
        };
        bool? picked;
        using (CancellationTokenRegistration registration = token.Register(closer.RequestClose))
        {
            picked = dialog.ShowDialog(owner);
            registration.Dispose();
        }
        // Undelivered cancellations must not fall back to a silent manual
        // close; without a close request the picker itself worked, so its
        // user result stands. A cancellation racing this window re-verifies
        // the dead dialog handle and resolves as plain cancellation.
        if (closer.CloseRequested && closer.Fault is { } fault) throw fault;
        if (closer.CloseRequested && !closer.EverCaptured)
            throw Fault("取消请求未能送达文件选择对话框；请手动关闭选择窗口后重试。");
        token.ThrowIfCancellationRequested();
        return Task.FromResult(picked == true ? dialog.FileName : null);
    }
}

/// <summary>Thread-local CBT observation of the one dialog this owner shows.
/// Bookkeeping is lock-protected because the token may be cancelled from any
/// thread; the hook only runs on the arming UI thread and its delegate stays
/// strongly referenced for this instance's lifetime.</summary>
internal sealed class CbtOwnedDialogCloser : IDisposable
{
    private const int WhCbt = 5;
    private const int HcbtActivate = 5;
    private const int WmClose = 0x0010;
    private const uint GwOwner = 4;
    private const uint GaRoot = 2;
    private const string DialogClassName = "#32770";

    private delegate IntPtr HookProc(int nCode, IntPtr wParam, IntPtr lParam);

    private readonly object _gate = new();
    private readonly IntPtr _ownerHandle;
    private readonly uint _ownerThreadId;
    private readonly uint _processId;
    private readonly HookProc _proc;
    private readonly Action<IntPtr>? _onShown;
    private IntPtr _hook;
    private IntPtr _dialog;
    private bool _everCaptured;
    private bool _closeRequested;
    private bool _disposed;
    private Exception? _fault;

    private CbtOwnedDialogCloser(IntPtr ownerHandle, uint ownerThreadId, uint processId, Action<IntPtr>? onShown)
    {
        _ownerHandle = ownerHandle;
        _ownerThreadId = ownerThreadId;
        _processId = processId;
        _onShown = onShown;
        _proc = HookCallback;
    }

    /// <summary>Installs the thread-local hook; throws before any dialog is
    /// shown when cancellable observation cannot be established.</summary>
    internal static CbtOwnedDialogCloser Arm(Window owner, Action<IntPtr>? onShown = null)
    {
        IntPtr ownerHandle = new WindowInteropHelper(owner).EnsureHandle();
        uint threadId = GetCurrentThreadId();
        _ = GetWindowThreadProcessId(ownerHandle, out uint processId);
        var closer = new CbtOwnedDialogCloser(ownerHandle, threadId, processId, onShown);
        closer._hook = SetWindowsHookExW(WhCbt, closer._proc, IntPtr.Zero, threadId);
        if (closer._hook == IntPtr.Zero)
            throw FeishuBaseFileDialog.Fault("无法建立文件选择对话框的取消监听，已停止打开。");
        return closer;
    }

    internal bool CloseRequested { get { lock (_gate) return _closeRequested; } }
    internal bool EverCaptured { get { lock (_gate) return _everCaptured; } }
    internal Exception? Fault { get { lock (_gate) return _fault; } }

    /// <summary>Thread-safe close request: WM_CLOSE to the captured dialog
    /// after re-verifying the exact ownership; before capture the hook
    /// delivers on the first verified activation instead.</summary>
    internal void RequestClose()
    {
        IntPtr target;
        lock (_gate)
        {
            if (_disposed) return;
            _closeRequested = true;
            target = _dialog;
        }
        if (target != IntPtr.Zero) TryPostClose();
    }

    private void TryPostClose()
    {
        lock (_gate)
        {
            if (_disposed || _dialog == IntPtr.Zero) return;
            if (!IsWindow(_dialog) || !OwnedDialog(_dialog))
            {
                _dialog = IntPtr.Zero; // already going away; nothing to close
                return;
            }
            if (!PostMessageW(_dialog, WmClose, IntPtr.Zero, IntPtr.Zero))
                _fault ??= FeishuBaseFileDialog.Fault("取消请求未能送达文件选择对话框；请手动关闭选择窗口后重试。");
        }
    }

    private IntPtr HookCallback(int nCode, IntPtr wParam, IntPtr lParam)
    {
        if (nCode == HcbtActivate)
        {
            try { OnActivated(wParam); }
            catch (Exception error)
            {
                // The hook must never throw into the message pump; observation
                // faults are always recorded and decided after Show returns.
                lock (_gate) _fault ??= FeishuBaseFileDialog.Fault(
                    $"文件选择对话框观察失败（{error.GetType().Name}），取消请求可能未送达；请手动关闭选择窗口后重试。");
            }
        }
        return CallNextHookEx(_hook, nCode, wParam, lParam);
    }

    private void OnActivated(IntPtr hwnd)
    {
        lock (_gate)
        {
            if (_disposed || _dialog != IntPtr.Zero || !OwnedDialog(hwnd)) return;
            _dialog = hwnd;
            _everCaptured = true;
        }
        _onShown?.Invoke(hwnd);
        bool deliver;
        lock (_gate) deliver = _closeRequested;
        if (deliver) TryPostClose();
    }

    /// <summary>Strict gate: system dialog class, directly owned by the
    /// explicit owner window, on the arming thread and process.</summary>
    private bool OwnedDialog(IntPtr hwnd) =>
        hwnd != IntPtr.Zero
        && hwnd != _ownerHandle
        && GetWindowThreadProcessId(hwnd, out uint pid) == _ownerThreadId
        && pid == _processId
        && GetClassName(hwnd) == DialogClassName
        && GetWindow(hwnd, GwOwner) == _ownerHandle
        && GetAncestor(hwnd, GaRoot) == hwnd;

    private static string GetClassName(IntPtr hwnd)
    {
        var name = new StringBuilder(64);
        int length = GetClassNameW(hwnd, name, name.Capacity);
        return length > 0 ? name.ToString(0, length) : "";
    }

    /// <summary>Whether a previously captured dialog handle is still a live
    /// window (it must be gone after dismissal).</summary>
    internal static bool IsWindowAlive(IntPtr hwnd) => hwnd != IntPtr.Zero && IsWindow(hwnd);

    /// <summary>Recovery close that re-verifies the dialog's full identity
    /// (system dialog class, direct ownership by the given owner handle,
    /// same thread and process as that owner) before posting WM_CLOSE.
    /// Primitive handles only — no WPF object access — so it is safe from
    /// any thread; never a title/class/process sweep.</summary>
    internal static void CloseVerifiedDialog(IntPtr hwnd, IntPtr ownerHandle)
    {
        if (hwnd == IntPtr.Zero || ownerHandle == IntPtr.Zero) return;
        uint ownerThread = GetWindowThreadProcessId(ownerHandle, out uint ownerProcess);
        if (GetWindowThreadProcessId(hwnd, out uint dialogProcess) == ownerThread
            && dialogProcess == ownerProcess
            && GetClassName(hwnd) == DialogClassName
            && GetWindow(hwnd, GwOwner) == ownerHandle)
            _ = PostMessageW(hwnd, WmClose, IntPtr.Zero, IntPtr.Zero);
    }

    public void Dispose()
    {
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
        }
        if (_hook != IntPtr.Zero)
        {
            UnhookWindowsHookEx(_hook);
            _hook = IntPtr.Zero;
        }
        lock (_gate) _dialog = IntPtr.Zero;
    }

    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr SetWindowsHookExW(int idHook, HookProc lpfn, IntPtr hMod, uint dwThreadId);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool UnhookWindowsHookEx(IntPtr hhk);

    [DllImport("user32.dll")]
    private static extern IntPtr CallNextHookEx(IntPtr hhk, int nCode, IntPtr wParam, IntPtr lParam);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool PostMessageW(IntPtr hWnd, int msg, IntPtr wParam, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool IsWindow(IntPtr hWnd);

    [DllImport("user32.dll")]
    private static extern IntPtr GetWindow(IntPtr hWnd, uint uCmd);

    [DllImport("user32.dll")]
    private static extern IntPtr GetAncestor(IntPtr hWnd, uint gaFlags);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassNameW(IntPtr hWnd, StringBuilder lpClassName, int nMaxCount);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint lpdwProcessId);

    [DllImport("kernel32.dll")]
    private static extern uint GetCurrentThreadId();
}
