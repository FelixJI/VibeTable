using System.Diagnostics;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.PreviewHost;
using VibeTable.Infrastructure.Diagnostics;
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
    public void PreviewEvidence_RequiresTheClosedTestModeArgumentPair()
    {
        string controls = @"C:\VibeTable\build\qa\controls";
        var launch = PreviewHostLaunchSpec.Create(
            @"C:\VibeTable", @"C:\VibeTable\files\native.txt", PreviewClsid, controls);
        string[] arguments = launch.CreateStartInfo().ArgumentList.Skip(1).ToArray();
        Assert.IsTrue(PreviewHostArguments.TryParse(arguments, out var parsed));
        Assert.AreEqual(controls, parsed.TestEvidenceDirectory);
        Assert.IsFalse(PreviewHostArguments.TryParse(
            arguments.Take(4).Concat(["--e2e-controls-dir", controls]).ToArray(), out _));
        Assert.IsFalse(PreviewHostArguments.TryParse(
            arguments.Take(4).Concat(["--test-mode", "--e2e-controls-dir", "relative"]).ToArray(), out _));
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
    public void Show_MissingHelperKeepsPublicCodeAndEmitsClosedFixedDiagnostic()
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-preview-missing-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            string documentPath = Path.Combine(root, "report.docx");
            File.WriteAllText(documentPath, "test");
            var resolver = new ShellPreviewHandlerResolver(
                key => key.EndsWith(
                    $@".docx\shellex\{ShellPreviewHandlerResolver.PreviewHandlerAssociation}",
                    StringComparison.OrdinalIgnoreCase)
                    ? PreviewClsid.ToString("B")
                    : null);
            using var preview = new ShellDocumentPreview(resolver, root);
            using var listener = new PreviewTraceCaptureListener();
            Trace.Listeners.Add(listener);
            try
            {
                var error = Assert.Throws<DocumentPreviewException>(
                    () => preview.Show(documentPath));

                Assert.AreEqual("PREVIEW_HOST_CREATE_FAILED", error.Code);
            }
            finally
            {
                Trace.Listeners.Remove(listener);
            }

            Assert.HasCount(1, listener.Lines);
            string line = listener.Lines[0];
            Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
            using JsonDocument document = JsonDocument.Parse(line);
            Assert.AreEqual(
                "document-preview",
                document.RootElement.GetProperty("module").GetString());
            Assert.AreEqual(
                "preview.host.spawn.failed",
                document.RootElement.GetProperty("event").GetString());
            Assert.AreEqual(
                "PREVIEW_HOST_EXECUTABLE_MISSING",
                document.RootElement.GetProperty("errorCode").GetString());
            Assert.IsFalse(line.Contains(root, StringComparison.Ordinal));
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public void Show_ResolverFailureEmitsClosedEventWithoutSensitiveContent()
    {
        const string SensitiveMessage = @"C:\Users\customer\private-report.docx";
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-preview-resolve-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            string documentPath = Path.Combine(root, "secret quarter.docx");
            File.WriteAllText(documentPath, "test");
            var resolver = new ShellPreviewHandlerResolver(
                _ => throw new InvalidOperationException(SensitiveMessage));
            using var preview = new ShellDocumentPreview(resolver, root);
            using var listener = new PreviewTraceCaptureListener();
            Trace.Listeners.Add(listener);
            try
            {
                var error = Assert.Throws<DocumentPreviewException>(
                    () => preview.Show(documentPath));

                Assert.AreEqual("PREVIEW_HANDLER_UNAVAILABLE", error.Code);
            }
            finally
            {
                Trace.Listeners.Remove(listener);
            }

            Assert.HasCount(1, listener.Lines);
            string line = listener.Lines[0];
            Assert.IsTrue(DiagnosticLogLine.IsSafe(line), line);
            using JsonDocument document = JsonDocument.Parse(line);
            Assert.AreEqual(
                "document-preview",
                document.RootElement.GetProperty("module").GetString());
            Assert.AreEqual(
                "preview.host.resolve.failed",
                document.RootElement.GetProperty("event").GetString());
            Assert.AreEqual(
                $"InvalidOperationException(0x{new InvalidOperationException().HResult:X8})",
                document.RootElement.GetProperty("errorCode").GetString());
            Assert.IsFalse(line.Contains(SensitiveMessage, StringComparison.Ordinal));
            Assert.IsFalse(line.Contains(documentPath, StringComparison.Ordinal));
            Assert.IsFalse(line.Contains("secret quarter", StringComparison.Ordinal));
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public void PreviewStartDiagnostics_LegacyPlainTextIsDroppedWhileClosedEventsPersist()
    {
        DirectoryInfo? repository = new(AppContext.BaseDirectory);
        while (repository is not null && !File.Exists(Path.Combine(repository.FullName, "qa", "next.py")))
            repository = repository.Parent;
        Assert.IsNotNull(
            repository, "Preview start diagnostics must stay inside the repository build tree.");
        string root = Path.Combine(
            repository.FullName,
            "build", "qa", "preview-start-diagnostics",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        string logPath = Path.Combine(root, "desktop.log");

        using (var listener = new RotatingDiagnosticTraceListener(logPath))
        {
            listener.WriteLine(
                $"Preview host start failed (TimeoutException, 0x{new TimeoutException().HResult:X8}).");
            listener.WriteLine(DiagnosticEvent.Failure(
                "document-preview",
                "preview.host.input-idle.failed",
                $"TimeoutException(0x{new TimeoutException().HResult:X8})"));
        }

        string[] persisted = File.ReadAllLines(logPath);
        Assert.HasCount(1, persisted);
        Assert.IsTrue(DiagnosticLogLine.IsSafe(persisted[0]));
        Assert.IsFalse(persisted[0].Contains("Preview host", StringComparison.Ordinal));
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

    [STATestMethod]
    public void ShellPreviewSession_RejectsRegisteredComClassWithoutPreviewInterface()
    {
        // FileOpenDialog is registered on supported Windows versions but is not an
        // IPreviewHandler. Constructing it does not show a dialog.
        using var session = new ShellPreviewSession(
            Path.Combine("C:\\", "Documents", "report.docx"),
            Guid.Parse("DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7"));

        Assert.Throws<PreviewHostLoadException>(() => session.Start(IntPtr.Zero, 100, 80));
        session.Resize(100, 80);
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

    [TestMethod]
    public void PreviewEvidence_NormalLaunchDoesNotWriteAControlRecord()
    {
        var fixture = CreatePreviewEvidenceFixture();
        var arguments = new PreviewHostArguments(fixture.Source, PreviewClsid);

        PreviewHostEntry.WriteEvidence(arguments, "do-preview-returned", new IntPtr(123));

        Assert.HasCount(0, Directory.GetFiles(fixture.Controls));
        Assert.AreEqual("synthetic preview evidence source", File.ReadAllText(fixture.Source));
    }

    [TestMethod]
    public void PreviewEvidence_ReplacesACompleteBoundRecordWithoutLeavingTemporaryFile()
    {
        var fixture = CreatePreviewEvidenceFixture();
        var arguments = new PreviewHostArguments(fixture.Source, PreviewClsid, fixture.Controls);
        string result = Path.Combine(fixture.Controls, "document-native-preview-result.json");
        PreviewHostEntry.WriteEvidence(arguments, "failed");
        using (var failure = System.Text.Json.JsonDocument.Parse(File.ReadAllText(result)))
        {
            Assert.AreEqual("failed", failure.RootElement.GetProperty("outcome").GetString());
            Assert.AreEqual(0L, failure.RootElement.GetProperty("hwnd").GetInt64());
        }

        PreviewHostEntry.WriteEvidence(arguments, "do-preview-returned", new IntPtr(123));

        using var success = System.Text.Json.JsonDocument.Parse(File.ReadAllText(result));
        var record = success.RootElement;
        CollectionAssert.AreEqual(
            new[] { "outcome", "source", "handlerClsid", "processId", "hwnd" },
            record.EnumerateObject().Select(property => property.Name).ToArray());
        Assert.AreEqual("do-preview-returned", record.GetProperty("outcome").GetString());
        Assert.AreEqual(fixture.Source, record.GetProperty("source").GetString());
        Assert.AreEqual(PreviewClsid, record.GetProperty("handlerClsid").GetGuid());
        Assert.AreEqual(Environment.ProcessId, record.GetProperty("processId").GetInt32());
        Assert.AreEqual(123L, record.GetProperty("hwnd").GetInt64());
        CollectionAssert.AreEqual(new[] { result }, Directory.GetFiles(fixture.Controls));
        Assert.AreEqual("synthetic preview evidence source", File.ReadAllText(fixture.Source));
    }

    [TestMethod]
    public void PreviewEvidence_MissingControlDirectoryCannotPublishSuccess()
    {
        var fixture = CreatePreviewEvidenceFixture();
        string missing = Path.Combine(fixture.Controls, "missing");
        var arguments = new PreviewHostArguments(fixture.Source, PreviewClsid, missing);

        Assert.Throws<DirectoryNotFoundException>(() =>
            PreviewHostEntry.WriteEvidence(arguments, "do-preview-returned", new IntPtr(123)));

        Assert.IsFalse(Directory.Exists(missing));
        Assert.HasCount(0, Directory.GetFiles(fixture.Controls));
        Assert.AreEqual("synthetic preview evidence source", File.ReadAllText(fixture.Source));
    }

    [TestMethod]
    public void PreviewEvidence_RejectsAReparseAncestorWithoutTouchingItsSyntheticTarget()
    {
        var fixture = CreatePreviewEvidenceFixture();
        string outside = Path.Combine(Path.GetDirectoryName(fixture.Controls)!, "outside");
        string actual = Path.Combine(outside, "nested");
        Directory.CreateDirectory(actual);
        string sentinel = Path.Combine(actual, "document-native-preview-result.json");
        File.WriteAllText(sentinel, "synthetic outside sentinel");
        string junction = Path.Combine(fixture.Controls, "redirected");
        CreatePreviewEvidenceJunction(junction, outside);
        try
        {
            var arguments = new PreviewHostArguments(
                fixture.Source, PreviewClsid, Path.Combine(junction, "nested"));

            Assert.Throws<IOException>(() =>
                PreviewHostEntry.WriteEvidence(arguments, "do-preview-returned", new IntPtr(123)));

            Assert.AreEqual("synthetic outside sentinel", File.ReadAllText(sentinel));
            CollectionAssert.AreEqual(new[] { sentinel }, Directory.GetFiles(actual));
        }
        finally
        {
            Directory.Delete(junction);
        }
    }

    [TestMethod]
    [DataRow("document-native-preview-result.json")]
    [DataRow("document-native-preview-result.json.tmp")]
    public void PreviewEvidence_RejectsAReparseResultOrTemporaryPath(string name)
    {
        var fixture = CreatePreviewEvidenceFixture();
        string outside = Path.Combine(Path.GetDirectoryName(fixture.Controls)!, "outside");
        Directory.CreateDirectory(outside);
        string sentinel = Path.Combine(outside, "sentinel.json");
        File.WriteAllText(sentinel, "synthetic outside sentinel");
        string link = Path.Combine(fixture.Controls, name);
        CreatePreviewEvidenceJunction(link, outside);
        try
        {
            var arguments = new PreviewHostArguments(fixture.Source, PreviewClsid, fixture.Controls);

            var error = Assert.Throws<IOException>(() =>
                PreviewHostEntry.WriteEvidence(arguments, "do-preview-returned", new IntPtr(123)));
            Assert.AreEqual(name.EndsWith(".tmp", StringComparison.Ordinal)
                ? "Preview evidence temporary file is a reparse point."
                : "Preview evidence file is a reparse point.", error.Message);

            Assert.AreEqual("synthetic outside sentinel", File.ReadAllText(sentinel));
            CollectionAssert.AreEqual(new[] { sentinel }, Directory.GetFiles(outside));
            CollectionAssert.AreEqual(new[] { link }, Directory.GetDirectories(fixture.Controls));
            Assert.HasCount(0, Directory.GetFiles(fixture.Controls));
        }
        finally
        {
            Directory.Delete(link);
        }
    }

    [TestMethod]
    public void PreviewDiagnosticsFailureNeverReplacesTheProductError()
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-preview-io-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            string documentPath = Path.Combine(root, "report.docx");
            File.WriteAllText(documentPath, "test");
            var missingHelperResolver = new ShellPreviewHandlerResolver(
                key => key.EndsWith(
                    $@".docx\shellex\{ShellPreviewHandlerResolver.PreviewHandlerAssociation}",
                    StringComparison.OrdinalIgnoreCase)
                    ? PreviewClsid.ToString("B")
                    : null);
            var failingResolver = new ShellPreviewHandlerResolver(
                _ => throw new InvalidOperationException(@"C:\Users\customer\机密-report.docx"));
            using (var missingHelperPreview = new ShellDocumentPreview(missingHelperResolver, root))
            using (var resolvePreview = new ShellDocumentPreview(failingResolver, root))
            using (var listener = new ThrowingPreviewTraceListener())
            {
                Trace.Listeners.Add(listener);
                try
                {
                    var missing = Assert.Throws<DocumentPreviewException>(
                        () => missingHelperPreview.Show(documentPath));
                    Assert.AreEqual("PREVIEW_HOST_CREATE_FAILED", missing.Code);

                    var resolve = Assert.Throws<DocumentPreviewException>(
                        () => resolvePreview.Show(documentPath));
                    Assert.AreEqual("PREVIEW_HANDLER_UNAVAILABLE", resolve.Code);
                }
                finally
                {
                    Trace.Listeners.Remove(listener);
                }
            }
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { }
        }
    }

    private sealed class ThrowingPreviewTraceListener : TraceListener
    {
        public override void Write(string? message) => ThrowForPreview(message);

        public override void WriteLine(string? message) => ThrowForPreview(message);

        // Only the document-preview diagnostics may throw; unrelated parallel
        // trace traffic from other test classes must stay untouched.
        private static void ThrowForPreview(string? message)
        {
            if (message is not null &&
                message.Contains("\"module\":\"document-preview\"", StringComparison.Ordinal))
                throw new IOException("diagnostic sink failure");
        }
    }

    private sealed class PreviewTraceCaptureListener : TraceListener
    {
        private readonly object _gate = new();
        private readonly List<string> _lines = [];

        public IReadOnlyList<string> Lines
        {
            get { lock (_gate) return [.. _lines]; }
        }

        public override void Write(string? message) => WriteLine(message);

        public override void WriteLine(string? message)
        {
            if (message is null ||
                (!message.Contains("document-preview", StringComparison.Ordinal) &&
                 !message.Contains("Preview host ", StringComparison.Ordinal)))
                return;
            lock (_gate) _lines.Add(message);
        }
    }

    private static (string Controls, string Source) CreatePreviewEvidenceFixture()
    {
        DirectoryInfo? repository = new(AppContext.BaseDirectory);
        while (repository is not null && !File.Exists(Path.Combine(repository.FullName, "qa", "next.py")))
            repository = repository.Parent;
        Assert.IsNotNull(repository, "Preview evidence tests must stay inside the repository build tree.");
        string root = Path.Combine(repository.FullName, "build", "qa", "415-preview-evidence-fix", Guid.NewGuid().ToString("N"));
        string controls = Path.Combine(root, "controls");
        Directory.CreateDirectory(controls);
        string source = Path.Combine(root, "synthetic.txt");
        File.WriteAllText(source, "synthetic preview evidence source");
        return (controls, source);
    }

    private static void CreatePreviewEvidenceJunction(string junction, string target)
    {
        // Reuse the existing Windows test junction pattern; both paths belong
        // to this test's synthetic fixture, and only the junction is unlinked.
        var start = new System.Diagnostics.ProcessStartInfo
        {
            FileName = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe",
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        foreach (string argument in new[] { "/d", "/c", "mklink", "/J", junction, target })
            start.ArgumentList.Add(argument);
        using var process = System.Diagnostics.Process.Start(start);
        Assert.IsNotNull(process);
        if (!process.WaitForExit(5000))
        {
            process.Kill();
            process.WaitForExit();
            Assert.Fail("The synthetic fixture junction command did not finish.");
        }
        Assert.AreEqual(0, process.ExitCode, process.StandardError.ReadToEnd());
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
