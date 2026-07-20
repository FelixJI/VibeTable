using System.IO;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;
using System.Windows;
using System.Windows.Interop;

namespace VibeTable.PreviewHost;

internal sealed class ShellPreviewWindow : Window
{
    public ShellPreviewWindow(string fullPath, Guid handlerClsid)
    {
        Title = $"预览 · {Path.GetFileName(fullPath)}";
        Width = 880;
        Height = 640;
        MinWidth = 520;
        MinHeight = 360;
        WindowStartupLocation = WindowStartupLocation.CenterScreen;
        Content = new ShellPreviewHost(fullPath, handlerClsid);
    }
}

internal sealed class ShellPreviewHost : HwndHost
{
    private const int WsChild = 0x40000000;
    private const int WsVisible = 0x10000000;
    private const int WsClipChildren = 0x02000000;
    private const int WsClipSiblings = 0x04000000;

    private readonly string _fullPath;
    private readonly Guid _handlerClsid;
    private IntPtr _hostHandle;
    private ShellPreviewSession? _session;

    public ShellPreviewHost(string fullPath, Guid handlerClsid)
    {
        _fullPath = fullPath;
        _handlerClsid = handlerClsid;
    }

    protected override HandleRef BuildWindowCore(HandleRef hwndParent)
    {
        _hostHandle = CreateWindowEx(
            0,
            "STATIC",
            string.Empty,
            WsChild | WsVisible | WsClipChildren | WsClipSiblings,
            0,
            0,
            Math.Max(1, (int)ActualWidth),
            Math.Max(1, (int)ActualHeight),
            hwndParent.Handle,
            IntPtr.Zero,
            IntPtr.Zero,
            IntPtr.Zero);
        if (_hostHandle == IntPtr.Zero)
            throw new PreviewHostLoadException();

        _session = new ShellPreviewSession(_fullPath, _handlerClsid);
        _session.Start(_hostHandle, WidthPixels, HeightPixels);
        return new HandleRef(this, _hostHandle);
    }

    protected override void DestroyWindowCore(HandleRef hwnd)
    {
        _session?.Dispose();
        _session = null;
        if (hwnd.Handle != IntPtr.Zero) DestroyWindow(hwnd.Handle);
        _hostHandle = IntPtr.Zero;
    }

    protected override void OnWindowPositionChanged(Rect rcBoundingBox)
    {
        base.OnWindowPositionChanged(rcBoundingBox);
        _session?.Resize(WidthPixels, HeightPixels);
    }

    private int WidthPixels => Math.Max(1, (int)Math.Ceiling(ActualWidth));
    private int HeightPixels => Math.Max(1, (int)Math.Ceiling(ActualHeight));

    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateWindowEx(
        int extendedStyle,
        string className,
        string windowName,
        int style,
        int x,
        int y,
        int width,
        int height,
        IntPtr parent,
        IntPtr menu,
        IntPtr instance,
        IntPtr parameter);

    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool DestroyWindow(IntPtr window);
}

internal sealed class ShellPreviewSession : IDisposable
{
    private const uint StgmRead = 0x00000000;
    private const uint StgmShareDenyNone = 0x00000040;

    private readonly string _fullPath;
    private readonly Guid _handlerClsid;
    private object? _handlerObject;
    private IPreviewHandler? _handler;
    private IStream? _stream;

    public ShellPreviewSession(string fullPath, Guid handlerClsid)
    {
        _fullPath = fullPath;
        _handlerClsid = handlerClsid;
    }

    public void Start(IntPtr parent, int width, int height)
    {
        try
        {
            Type type = Type.GetTypeFromCLSID(_handlerClsid, throwOnError: true)!;
            _handlerObject = Activator.CreateInstance(type)
                ?? throw new PreviewHostLoadException();
            _handler = (IPreviewHandler)_handlerObject;
            InitializeHandler();
            var rect = new NativeRect(0, 0, width, height);
            _handler.SetWindow(parent, ref rect);
            _handler.DoPreview();
        }
        catch
        {
            Dispose();
            throw new PreviewHostLoadException();
        }
    }

    public void Resize(int width, int height)
    {
        if (_handler is null) return;
        var rect = new NativeRect(0, 0, width, height);
        _handler.SetRect(ref rect);
    }

    public void Dispose()
    {
        try { _handler?.Unload(); } catch { }
        if (_stream is not null && Marshal.IsComObject(_stream))
            Marshal.FinalReleaseComObject(_stream);
        if (_handlerObject is not null && Marshal.IsComObject(_handlerObject))
            Marshal.FinalReleaseComObject(_handlerObject);
        _stream = null;
        _handler = null;
        _handlerObject = null;
    }

    private void InitializeHandler()
    {
        if (_handlerObject is IInitializeWithStream streamInitializer)
        {
            int result = SHCreateStreamOnFileEx(
                _fullPath,
                StgmRead | StgmShareDenyNone,
                0,
                false,
                null,
                out var stream);
            Marshal.ThrowExceptionForHR(result);
            _stream = stream;
            streamInitializer.Initialize(stream, 0);
            return;
        }
        if (_handlerObject is IInitializeWithFile fileInitializer)
        {
            fileInitializer.Initialize(_fullPath, 0);
            return;
        }
        throw new PreviewHostLoadException();
    }

    [DllImport("shlwapi.dll", CharSet = CharSet.Unicode)]
    private static extern int SHCreateStreamOnFileEx(
        string fileName,
        uint mode,
        uint attributes,
        [MarshalAs(UnmanagedType.Bool)] bool create,
        IStream? template,
        out IStream stream);
}

internal sealed class PreviewHostLoadException : Exception;

[StructLayout(LayoutKind.Sequential)]
internal struct NativeRect
{
    public NativeRect(int left, int top, int right, int bottom)
    {
        Left = left;
        Top = top;
        Right = right;
        Bottom = bottom;
    }

    public int Left;
    public int Top;
    public int Right;
    public int Bottom;
}

[ComImport]
[Guid("8895B1C6-B41F-4C1C-A562-0D564250836F")]
[InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
internal interface IPreviewHandler
{
    void SetWindow(IntPtr parent, ref NativeRect rect);
    void SetRect(ref NativeRect rect);
    void DoPreview();
    void Unload();
    void SetFocus();
    void QueryFocus(out IntPtr window);
    [PreserveSig]
    uint TranslateAccelerator(ref NativeMessage message);
}

[ComImport]
[Guid("B7D14566-0509-4CCE-A71F-0A554233BD9B")]
[InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
internal interface IInitializeWithFile
{
    void Initialize([MarshalAs(UnmanagedType.LPWStr)] string filePath, uint mode);
}

[ComImport]
[Guid("B824B49D-22AC-4161-AC8A-9916E8FA3F7F")]
[InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
internal interface IInitializeWithStream
{
    void Initialize(IStream stream, uint mode);
}

[StructLayout(LayoutKind.Sequential)]
internal struct NativeMessage
{
    public IntPtr Window;
    public uint Message;
    public IntPtr WParam;
    public IntPtr LParam;
    public uint Time;
    public NativePoint Point;
}

[StructLayout(LayoutKind.Sequential)]
internal struct NativePoint
{
    public int X;
    public int Y;
}
