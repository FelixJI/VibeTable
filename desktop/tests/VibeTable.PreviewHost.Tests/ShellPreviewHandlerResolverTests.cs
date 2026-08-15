using VibeTable.Desktop.Services;
using VibeTable.PreviewHost;
using System.Windows;

namespace VibeTable.PreviewHost.Tests;

[TestClass]
public sealed class ShellPreviewHandlerResolverTests
{
    private static readonly Guid PreviewClsid =
        Guid.Parse("84F66100-FF7C-4FB4-B0C0-02CD7FB668FE");

    [TestMethod]
    public void Resolve_UsesDirectExtensionRegistration()
    {
        var values = new Dictionary<string, string?>(StringComparer.OrdinalIgnoreCase)
        {
            [$@".docx\shellex\{ShellPreviewHandlerResolver.PreviewHandlerAssociation}"] =
                PreviewClsid.ToString("B"),
        };
        var resolver = new ShellPreviewHandlerResolver(
            key => values.GetValueOrDefault(key));

        Assert.AreEqual(PreviewClsid, resolver.Resolve("proposal.docx"));
    }

    [TestMethod]
    public void Resolve_FallsBackToProgIdRegistration()
    {
        var values = new Dictionary<string, string?>(StringComparer.OrdinalIgnoreCase)
        {
            [".xlsx"] = "Excel.Sheet.12",
            [$@"Excel.Sheet.12\shellex\{ShellPreviewHandlerResolver.PreviewHandlerAssociation}"] =
                PreviewClsid.ToString("B"),
        };
        var resolver = new ShellPreviewHandlerResolver(
            key => values.GetValueOrDefault(key));

        Assert.AreEqual(PreviewClsid, resolver.Resolve("budget.xlsx"));
    }

    [TestMethod]
    public void Resolve_UnknownExtensionReturnsNull()
    {
        var resolver = new ShellPreviewHandlerResolver(_ => null);

        Assert.IsNull(resolver.Resolve("archive.unknown"));
    }

    [TestMethod]
    public void LaunchSpec_UsesStructuredArgumentsWithoutShellOrElevation()
    {
        string appDirectory = Path.Combine("C:\\", "Program Files", "VibeTable");
        string documentPath = Path.Combine("C:\\", "Private Files", "quarterly report.docx");

        var spec = PreviewHostLaunchSpec.Create(
            appDirectory,
            documentPath,
            PreviewClsid);
        var startInfo = spec.CreateStartInfo();

        Assert.AreEqual(
            Path.Combine(
                appDirectory,
                PreviewHostLaunchSpec.ExecutableName),
            startInfo.FileName);
        Assert.IsFalse(startInfo.UseShellExecute);
        Assert.AreEqual(string.Empty, startInfo.Verb);
        CollectionAssert.AreEqual(
            new[]
            {
                "--preview-host",
                "--file",
                documentPath,
                "--handler",
                PreviewClsid.ToString("D"),
            },
            startInfo.ArgumentList.ToArray());
    }

    [TestMethod]
    public void LaunchSpec_RejectsEmptyHandlerClsid()
    {
        Assert.Throws<ArgumentException>(() => PreviewHostLaunchSpec.Create(
            "C:\\VibeTable",
            "C:\\Documents\\report.docx",
            Guid.Empty));
    }

    [TestMethod]
    public void CanPreview_RequiresExistingFileAndRegisteredHandler()
    {
        string temp = Path.Combine(
            Path.GetTempPath(), "vibetable-preview-probe-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(temp);
        try
        {
            string documentPath = Path.Combine(temp, "report.docx");
            File.WriteAllText(documentPath, "test");
            var resolver = new ShellPreviewHandlerResolver(
                key => key.EndsWith(
                    $@".docx\shellex\{ShellPreviewHandlerResolver.PreviewHandlerAssociation}",
                    StringComparison.OrdinalIgnoreCase)
                    ? PreviewClsid.ToString("B")
                    : null);
            using var preview = new ShellDocumentPreview(resolver, temp);

            Assert.IsTrue(preview.CanPreview(documentPath));
            Assert.IsFalse(preview.CanPreview(Path.Combine(temp, "missing.docx")));
        }
        finally
        {
            try { Directory.Delete(temp, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public void PreviewHostArguments_ParseAbsolutePathAndHandler()
    {
        string documentPath = Path.Combine("C:\\", "Private Files", "quarterly report.docx");

        bool parsed = PreviewHostArguments.TryParse(
            ["--handler", PreviewClsid.ToString("D"), "--file", documentPath],
            out var arguments);

        Assert.IsTrue(parsed);
        Assert.AreEqual(documentPath, arguments.FilePath);
        Assert.AreEqual(PreviewClsid, arguments.HandlerClsid);
    }

    [TestMethod]
    public void PreviewHostArguments_RejectRelativeOrDuplicateInput()
    {
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--file", "relative.docx", "--handler", PreviewClsid.ToString("D")],
            out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--file", "C:\\Documents\\one.docx", "--file", "C:\\Documents\\two.docx"],
            out _));
    }

    [TestMethod]
    public void PreviewHostArguments_RejectMalformedClosedArgumentSet()
    {
        string absolute = Path.Combine("C:\\", "Documents", "report.docx");
        string handler = PreviewClsid.ToString("D");

        Assert.IsFalse(PreviewHostArguments.TryParse([], out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--file", absolute, "--unknown", handler],
            out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--handler", Guid.Empty.ToString("D"), "--file", absolute],
            out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--handler", "not-a-guid", "--file", absolute],
            out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--handler", handler, "--handler", handler],
            out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            ["--file", " ", "--handler", handler],
            out _));
    }

    [TestMethod]
    public void ShellPreviewSession_FailsClosedForUnregisteredComHandlerAndDisposesIdempotently()
    {
        using var session = new ShellPreviewSession(
            Path.Combine("C:\\", "Documents", "report.docx"),
            Guid.Parse("11111111-1111-4111-8111-111111111111"));

        session.Resize(100, 80);
        Assert.Throws<PreviewHostLoadException>(() => session.Start(IntPtr.Zero, 100, 80));
        session.Dispose();
        session.Dispose();
    }

    [TestMethod]
    public void NativeRect_PreservesPixelBounds()
    {
        var rect = new NativeRect(1, 2, 300, 400);

        Assert.AreEqual(1, rect.Left);
        Assert.AreEqual(2, rect.Top);
        Assert.AreEqual(300, rect.Right);
        Assert.AreEqual(400, rect.Bottom);
    }

    [STATestMethod]
    public void PreviewHostEntry_RejectsInvalidArgumentsBeforeCreatingAWindow()
    {
        var application = new Application();

        int exitCode = PreviewHostEntry.Start(application, ["--file", "relative.docx"]);

        Assert.AreEqual(2, exitCode);
        Assert.AreEqual(ShutdownMode.OnExplicitShutdown, application.ShutdownMode);
        Assert.IsNull(application.MainWindow);
        application.Shutdown();
    }

    [STATestMethod]
    public void ShellPreviewWindow_ProjectsAConstrainedHostSurface()
    {
        string path = Path.Combine("C:\\", "Documents", "quarterly report.docx");

        var window = new ShellPreviewWindow(path, PreviewClsid);

        Assert.AreEqual("预览 · quarterly report.docx", window.Title);
        Assert.AreEqual(880, window.Width);
        Assert.AreEqual(640, window.Height);
        Assert.IsInstanceOfType<ShellPreviewHost>(window.Content);
        window.Close();
    }
}
