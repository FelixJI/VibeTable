using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Text;
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
