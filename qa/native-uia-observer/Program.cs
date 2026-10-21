using System.IO;
using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Text;
using System.Text.Json;
using System.Runtime.InteropServices;
using System.Windows.Interop;
using System.Windows.Media.Imaging;
using System.Windows.Automation;

internal static class Program
{
    private static readonly Stopwatch Clock = new();

    [MTAThread]
    public static int Main(string[] args)
    {
        Console.Error.WriteLine("UIA_STAGE managed entered");
        Clock.Start();
        Console.Error.WriteLine($"UIA_STAGE observer begin elapsedMs=0 apartment={Thread.CurrentThread.GetApartmentState()} utc={DateTime.UtcNow:o}");
        try
        {
            if (args.Length == 4 && args[0] == "--file-dialog") return ObserveFileDialog(args);
            if (args.Length is 2 or 3 && args[0] is "--buttons" or "--screenshot")
                return ObservePluginChrome(args);
            if (args.Length is 2 or 3 && args[0] == "--source-import")
                return ObserveSourceImportWindow(args);
            if (args.Length != 1 || !long.TryParse(args[0], NumberStyles.None, CultureInfo.InvariantCulture, out long hwnd) || hwnd <= 0)
                return Reject("UIA_ARGUMENT_INVALID");
            Console.OutputEncoding = new UTF8Encoding(false, true);
            return ReadDocument(new IntPtr(hwnd));
        }
        catch (Exception exception)
        {
            // Exception messages and document/path contents never enter stderr.
            Console.Error.WriteLine("UIA_OBSERVER_ERROR " + exception.GetType().Name);
            return 1;
        }
    }

    // Read-only QA: actual native bounds/focus and an onscreen WPF screenshot.
    // No UIA InvokePattern, product messages or mutation of product state.
    private static int ObservePluginChrome(string[] args)
    {
        if (!long.TryParse(args[1], out long value) || value <= 0) return Reject("UIA_ARGUMENT_INVALID");
        SetProcessDpiAwarenessContext(new IntPtr(-4));
        Console.OutputEncoding = new UTF8Encoding(false, true);
        var hwnd = new IntPtr(value);
        var root = AutomationElement.FromHandle(hwnd);
        var bounds = root.Current.BoundingRectangle;
        var buttons = new List<object>();
        var nativeElements = new Stack<AutomationElement>();
        nativeElements.Push(root);
        int visited = 0;
        while (nativeElements.TryPop(out var element))
        {
            if (++visited > 512) return Reject("UIA_NATIVE_TREE_LIMIT");
            // Never descend into Chrome accessibility: a frozen renderer must
            // not block observation of the independent WPF security chrome.
            if (element.Current.FrameworkId != "WPF") continue;
            var child = TreeWalker.ControlViewWalker.GetFirstChild(element);
            while (child is not null)
            {
                nativeElements.Push(child);
                child = TreeWalker.ControlViewWalker.GetNextSibling(child);
            }
            if (element.Current.ControlType != ControlType.Button) continue;
            string id = element.Current.AutomationId;
            if (!id.StartsWith("plugin-", StringComparison.Ordinal)) continue;
            var box = element.Current.BoundingRectangle;
            buttons.Add(new { id, name = element.Current.Name, pid = element.Current.ProcessId,
                enabled = element.Current.IsEnabled, focused = element.Current.HasKeyboardFocus,
                x = box.X, y = box.Y, width = box.Width, height = box.Height });
        }
        if (args[0] == "--screenshot")
        {
            if (args.Length != 3 || !Path.IsPathFullyQualified(args[2])) return Reject("UIA_ARGUMENT_INVALID");
            int width = checked((int)bounds.Width), height = checked((int)bounds.Height);
            if (width <= 0 || height <= 0 || width > 8192 || height > 8192) return Reject("UIA_BOUNDS_INVALID");
            IntPtr source = GetDC(IntPtr.Zero), destination = CreateCompatibleDC(source);
            IntPtr bitmap = CreateCompatibleBitmap(source, width, height);
            if (source == IntPtr.Zero || destination == IntPtr.Zero || bitmap == IntPtr.Zero)
                return Reject("UIA_CAPTURE_FAILED");
            IntPtr prior = SelectObject(destination, bitmap);
            try
            {
                if (!BitBlt(destination, 0, 0, width, height, source, (int)bounds.X, (int)bounds.Y, 0x00CC0020))
                    return Reject("UIA_CAPTURE_FAILED");
                var image = Imaging.CreateBitmapSourceFromHBitmap(bitmap, IntPtr.Zero,
                    System.Windows.Int32Rect.Empty, BitmapSizeOptions.FromEmptyOptions());
                var encoder = new PngBitmapEncoder();
                encoder.Frames.Add(BitmapFrame.Create(image));
                using var output = File.Create(args[2]);
                encoder.Save(output);
            }
            finally
            {
                SelectObject(destination, prior); DeleteObject(bitmap); DeleteDC(destination);
                ReleaseDC(IntPtr.Zero, source);
            }
        }
        Console.Out.Write(JsonSerializer.Serialize(new { hwnd = value, pid = root.Current.ProcessId,
            title = root.Current.Name, x = bounds.X, y = bounds.Y, width = bounds.Width,
            height = bounds.Height, dpi = GetDpiForWindow(hwnd), systemHighContrast = System.Windows.SystemParameters.HighContrast, buttons }));
        return 0;
    }

    // Read-only QA for the .base source import wizard: actual native
    // bounds/state of the fixed WPF control surface and an onscreen
    // screenshot. No UIA InvokePattern, product messages or mutation.
    private static readonly string[] SourceImportExactIds =
    [
        "source-file-button", "notice-acknowledge-checkbox", "preview-button",
        "start-button", "cancel-button", "status-text", "report-text", "reverse-checkbox",
    ];
    private static readonly string[] SourceImportPrefixes =
    [
        "table-select-", "table-target-", "field-policy-", "field-snapshot-kind-",
        "field-reverse-name-", "notice-",
    ];

    private static int ObserveSourceImportWindow(string[] args)
    {
        if (!long.TryParse(args[1], out long value) || value <= 0) return Reject("UIA_ARGUMENT_INVALID");
        if (args.Length == 3 && !Path.IsPathFullyQualified(args[2])) return Reject("UIA_ARGUMENT_INVALID");
        SetProcessDpiAwarenessContext(new IntPtr(-4));
        Console.OutputEncoding = new UTF8Encoding(false, true);
        var hwnd = new IntPtr(value);
        var root = AutomationElement.FromHandle(hwnd);
        if (root.Current.AutomationId != "source-import-window") return Reject("UIA_SOURCE_IMPORT_WINDOW_INVALID");
        var controls = new List<object>();
        var nativeElements = new Stack<AutomationElement>();
        nativeElements.Push(root);
        int visited = 0;
        while (nativeElements.TryPop(out var element))
        {
            if (++visited > 512) return Reject("UIA_NATIVE_TREE_LIMIT");
            // The wizard is pure WPF; never descend into other accessibility.
            if (element.Current.FrameworkId != "WPF") continue;
            var child = TreeWalker.ControlViewWalker.GetFirstChild(element);
            while (child is not null)
            {
                nativeElements.Push(child);
                child = TreeWalker.ControlViewWalker.GetNextSibling(child);
            }
            string id = element.Current.AutomationId;
            string className = element.Current.ClassName;
            bool listed = SourceImportExactIds.Contains(id)
                || SourceImportPrefixes.Any(prefix => id.StartsWith(prefix, StringComparison.Ordinal))
                || className == "Expander";
            if (!listed) continue;
            var box = element.Current.BoundingRectangle;
            string state = ReadState(element);
            controls.Add(new { id, name = element.Current.Name,
                type = element.Current.ControlType.ProgrammaticName,
                className, value = state, pid = element.Current.ProcessId,
                enabled = element.Current.IsEnabled, focused = element.Current.HasKeyboardFocus,
                x = box.X, y = box.Y, width = box.Width, height = box.Height });
        }
        var bounds = root.Current.BoundingRectangle;
        if (args.Length == 3)
        {
            int width = checked((int)bounds.Width), height = checked((int)bounds.Height);
            if (width <= 0 || height <= 0 || width > 8192 || height > 8192) return Reject("UIA_BOUNDS_INVALID");
            IntPtr source = GetDC(IntPtr.Zero), destination = CreateCompatibleDC(source);
            IntPtr bitmap = CreateCompatibleBitmap(source, width, height);
            if (source == IntPtr.Zero || destination == IntPtr.Zero || bitmap == IntPtr.Zero)
                return Reject("UIA_CAPTURE_FAILED");
            IntPtr prior = SelectObject(destination, bitmap);
            try
            {
                if (!BitBlt(destination, 0, 0, width, height, source, (int)bounds.X, (int)bounds.Y, 0x00CC0020))
                    return Reject("UIA_CAPTURE_FAILED");
                var image = Imaging.CreateBitmapSourceFromHBitmap(bitmap, IntPtr.Zero,
                    System.Windows.Int32Rect.Empty, BitmapSizeOptions.FromEmptyOptions());
                var encoder = new PngBitmapEncoder();
                encoder.Frames.Add(BitmapFrame.Create(image));
                using var output = File.Create(args[2]);
                encoder.Save(output);
            }
            finally
            {
                SelectObject(destination, prior); DeleteObject(bitmap); DeleteDC(destination);
                ReleaseDC(IntPtr.Zero, source);
            }
        }
        Console.Out.Write(JsonSerializer.Serialize(new
        {
            hwnd = value, pid = root.Current.ProcessId, title = root.Current.Name,
            automationId = root.Current.AutomationId,
            x = bounds.X, y = bounds.Y, width = bounds.Width, height = bounds.Height,
            dpi = GetDpiForWindow(hwnd), systemHighContrast = System.Windows.SystemParameters.HighContrast,
            controls,
        }));
        return 0;
    }

    // Fixed state projections for the closed wizard surface: toggle state for
    // checkboxes, the selected entry label for the policy comboboxes, the
    // expand/collapse state for expanders and the text projection for
    // status/notices. Read-only patterns only.
    private static string ReadState(AutomationElement element)
    {
        try
        {
            if (element.TryGetCurrentPattern(TogglePattern.Pattern, out object toggle))
                return ((TogglePattern)toggle).Current.ToggleState.ToString();
            if (element.TryGetCurrentPattern(SelectionPattern.Pattern, out object selection))
            {
                var chosen = ((SelectionPattern)selection).Current.GetSelection();
                return chosen.Length == 0 ? "" : chosen[0].Current.Name ?? "";
            }
            if (element.TryGetCurrentPattern(ExpandCollapsePattern.Pattern, out object expand))
                return ((ExpandCollapsePattern)expand).Current.ExpandCollapseState.ToString();
            if (element.TryGetCurrentPattern(ValuePattern.Pattern, out object value))
                return ((ValuePattern)value).Current.Value ?? "";
        }
        catch (InvalidOperationException) { }
        return element.Current.Name ?? "";
    }

    private static int ObserveFileDialog(string[] args)
    {
        if (Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true" || Environment.GetEnvironmentVariable("CI") != "true"
            || !long.TryParse(args[1], out long value) || value <= 0 || !uint.TryParse(args[2], out uint pid)
            || !Path.IsPathFullyQualified(args[3])) return Reject("UIA_FILE_DIALOG_SCOPE_INVALID");
        SetProcessDpiAwarenessContext(new IntPtr(-4));
        var hwnd = new IntPtr(value);
        if (!OwnedFileDialog(hwnd, pid)) return Reject("UIA_FILE_DIALOG_OWNER_INVALID");
        var root = AutomationElement.FromHandle(hwnd);
        var controls = new List<object>();
        var queue = new Queue<AutomationElement>(); queue.Enqueue(root);
        int count = 0;
        while (queue.TryDequeue(out var item))
        {
            if (++count > 512) return Reject("UIA_FILE_DIALOG_TREE_LIMIT");
            if (item.Current.ProcessId != pid) return Reject("UIA_FILE_DIALOG_PROCESS_INVALID");
            var child = TreeWalker.ControlViewWalker.GetFirstChild(item);
            while (child is not null)
            {
                if (queue.Count + count >= 512) return Reject("UIA_FILE_DIALOG_TREE_LIMIT");
                queue.Enqueue(child); child = TreeWalker.ControlViewWalker.GetNextSibling(child);
            }
            string id = item.Current.AutomationId;
            string? role = item.Current.ControlType == ControlType.Edit && id is "1148" or "1001" ? "filename"
                : item.Current.ControlType == ControlType.Button && id == "1" ? "accept"
                : item.Current.ControlType == ControlType.Button && id == "2" ? "cancel" : null;
            if (role is null) continue;
            var b = item.Current.BoundingRectangle;
            controls.Add(new { role, id, enabled = item.Current.IsEnabled, focused = item.Current.HasKeyboardFocus, pid = item.Current.ProcessId,
                x = b.X, y = b.Y, width = b.Width, height = b.Height });
        }
        if (!OwnedFileDialog(hwnd, pid)) return Reject("UIA_FILE_DIALOG_OWNER_CHANGED");
        var bounds = root.Current.BoundingRectangle;
        int width = checked((int)bounds.Width), height = checked((int)bounds.Height);
        if (width <= 0 || height <= 0 || width > 8192 || height > 8192) return Reject("UIA_BOUNDS_INVALID");
        IntPtr source = GetDC(IntPtr.Zero), destination = CreateCompatibleDC(source);
        IntPtr bitmap = CreateCompatibleBitmap(source, width, height);
        if (source == IntPtr.Zero || destination == IntPtr.Zero || bitmap == IntPtr.Zero) return Reject("UIA_CAPTURE_FAILED");
        IntPtr prior = SelectObject(destination, bitmap);
        try
        {
            if (!BitBlt(destination, 0, 0, width, height, source, (int)bounds.X, (int)bounds.Y, 0x00CC0020)) return Reject("UIA_CAPTURE_FAILED");
            var image = Imaging.CreateBitmapSourceFromHBitmap(bitmap, IntPtr.Zero, System.Windows.Int32Rect.Empty, BitmapSizeOptions.FromEmptyOptions());
            var encoder = new PngBitmapEncoder(); encoder.Frames.Add(BitmapFrame.Create(image));
            using var output = File.Create(args[3]); encoder.Save(output);
        }
        finally { SelectObject(destination, prior); DeleteObject(bitmap); DeleteDC(destination); ReleaseDC(IntPtr.Zero, source); }
        Console.Out.Write(JsonSerializer.Serialize(new { hwnd = value, pid, title = root.Current.Name, controls }));
        return 0;
    }

    private static bool OwnedFileDialog(IntPtr hwnd, uint pid)
    {
        GetWindowThreadProcessId(hwnd, out uint actual);
        var name = new StringBuilder(128); GetClassName(hwnd, name, name.Capacity);
        var title = new StringBuilder(256); GetWindowText(hwnd, title, title.Capacity);
        if (actual != pid || name.ToString() != "#32770" || title.ToString() is not
            ("Replace attachment" or "Add attachments" or "保存托管附件" or "Import table data" or "Export table data"
            or "导入飞书 .base 文件")) return false;
        var owner = GetWindow(hwnd, 4);
        for (int depth = 0; owner != IntPtr.Zero && depth < 8; depth++, owner = GetWindow(owner, 4))
        {
            GetWindowThreadProcessId(owner, out actual);
            if (actual != pid) return false;
            title.Clear(); GetWindowText(owner, title, title.Capacity);
            if (title.ToString() == "VibeTable") return true;
        }
        return false;
    }
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
    [DllImport("user32.dll")] private static extern IntPtr GetWindow(IntPtr hwnd, uint command);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetClassName(IntPtr hwnd, StringBuilder text, int count);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetWindowText(IntPtr hwnd, StringBuilder text, int count);

    [DllImport("user32.dll")] private static extern uint GetDpiForWindow(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern bool SetProcessDpiAwarenessContext(IntPtr value);
    [DllImport("user32.dll")] private static extern IntPtr GetDC(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern int ReleaseDC(IntPtr hwnd, IntPtr dc);
    [DllImport("gdi32.dll")] private static extern IntPtr CreateCompatibleDC(IntPtr dc);
    [DllImport("gdi32.dll")] private static extern IntPtr CreateCompatibleBitmap(IntPtr dc, int width, int height);
    [DllImport("gdi32.dll")] private static extern IntPtr SelectObject(IntPtr dc, IntPtr value);
    [DllImport("gdi32.dll")] private static extern bool DeleteObject(IntPtr value);
    [DllImport("gdi32.dll")] private static extern bool DeleteDC(IntPtr dc);
    [DllImport("gdi32.dll")] private static extern bool BitBlt(IntPtr dest, int x, int y, int width,
        int height, IntPtr source, int sx, int sy, int mode);

    private static int ReadDocument(IntPtr hwnd)
    {
        Stage("assemblies begin");
        // The application is an ordinary named CLR caller, without PowerShell frames.
        AssemblyName providers = typeof(AutomationElement).Assembly.GetName();
        providers.Name = "UIAutomationClientsideProviders";
        Stage("provider-registration begin");
        ClientSettings.RegisterClientSideProviderAssembly(providers);
        Stage("provider-registration end");
        Stage("assemblies end");
        Stage("root begin");
        AutomationElement root = AutomationElement.FromHandle(hwnd);
        Stage("root end");
        var condition = new OrCondition(
            new PropertyCondition(AutomationElement.ControlTypeProperty, ControlType.Document),
            new PropertyCondition(AutomationElement.ControlTypeProperty, ControlType.Edit));
        Stage("document-query begin");
        List<AutomationElement> documents;
        do
        {
            documents = new List<AutomationElement>();
            foreach (AutomationElement document in root.FindAll(TreeScope.Descendants, condition))
            {
                AutomationElement? parent = TreeWalker.RawViewWalker.GetParent(document);
                if (document.Current.ControlType == ControlType.Edit && parent?.Current.ControlType == ControlType.StatusBar)
                    continue;
                documents.Add(document);
            }
            if (documents.Count == 0)
                Thread.Sleep(25);
        } while (documents.Count == 0);
        Stage("document-query end", $"count={documents.Count}");
        if (documents.Count != 1)
            return Reject("UIA_DOCUMENT_COUNT", $"expected=1 actual={documents.Count}");
        var tabCondition = new PropertyCondition(AutomationElement.ControlTypeProperty, ControlType.TabItem);
        Stage("tab-query begin");
        AutomationElementCollection tabs = root.FindAll(TreeScope.Descendants, tabCondition);
        Stage("tab-query end", $"count={tabs.Count}");
        if (tabs.Count > 1)
            return Reject("UIA_TAB_COUNT", $"maximum=1 actual={tabs.Count}");
        AutomationElement sole = documents[0];
        string text;
        Stage("pattern begin");
        if (sole.TryGetCurrentPattern(TextPattern.Pattern, out object textObject))
        {
            Stage("pattern end", "kind=text");
            Stage("read-text begin");
            text = ((TextPattern)textObject).DocumentRange.GetText(4096);
        }
        else if (sole.TryGetCurrentPattern(ValuePattern.Pattern, out object valueObject))
        {
            Stage("pattern end", "kind=value");
            Stage("read-text begin");
            text = ((ValuePattern)valueObject).Current.Value;
            if (text.Length > 4096)
                return Reject("UIA_TEXT_LIMIT");
        }
        else
            return Reject("UIA_TEXT_PATTERN");
        string name = sole.Current.Name;
        Stage("read-text end");
        Stage("encode begin");
        var utf8 = new UTF8Encoding(false, true);
        string frame = "VIBETABLE_UIA1\t" + Convert.ToBase64String(utf8.GetBytes(name)) + "\t" + Convert.ToBase64String(utf8.GetBytes(text));
        if (frame.Length > 65536)
            return Reject("UIA_FRAME_LIMIT");
        Stage("encode end", $"length={frame.Length}");
        Console.Out.Write(frame);
        Stage("output end");
        return 0;
    }

    private static int Reject(string code, string detail = "")
    {
        Console.Error.WriteLine($"UIA_OBSERVER_ERROR {code}{(detail.Length == 0 ? "" : " " + detail)}");
        return 1;
    }

    private static void Stage(string name, string detail = "")
    {
        Console.Error.WriteLine($"UIA_STAGE {name} elapsedMs={Clock.ElapsedMilliseconds}{(detail.Length == 0 ? "" : " " + detail)}");
    }
}
