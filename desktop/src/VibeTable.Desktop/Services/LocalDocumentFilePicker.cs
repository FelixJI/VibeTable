using System.IO;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Native-only file selection boundary. Implementations may show a Windows
/// picker; renderer and data-service payloads never provide the selected path.
/// </summary>
public interface ILocalDocumentFilePicker
{
    Task<string?> PickFileAsync(
        DocumentFilePickPurpose purpose,
        string? suggestedFileName,
        CancellationToken token);
}

public enum DocumentFilePickPurpose
{
    Import,
    RelinkMissing,
}

/// <summary>
/// Test-mode-only document picker backed by one fixed control file. It never
/// opens a native dialog and never accepts a renderer-provided path.
/// </summary>
internal sealed class TestModeLocalDocumentFilePicker : ILocalDocumentFilePicker
{
    private readonly string _controlsDirectory;

    public TestModeLocalDocumentFilePicker(string controlsDirectory)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(controlsDirectory);
        _controlsDirectory = Path.GetFullPath(controlsDirectory);
    }

    public Task<string?> PickFileAsync(
        DocumentFilePickPurpose purpose,
        string? suggestedFileName,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        string control = Path.Combine(_controlsDirectory, "document-source.txt");
        try
        {
            string source = Path.GetFullPath(File.ReadAllText(control).Trim());
            if (!File.Exists(source))
            {
                throw new DocumentFileOperationException(
                    "测试文件选择器没有可用的源文件。",
                    "DOCUMENT_TEST_SOURCE_MISSING");
            }
            return Task.FromResult<string?>(source);
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception exception) when (exception is IOException
            or UnauthorizedAccessException
            or ArgumentException
            or NotSupportedException)
        {
            throw new DocumentFileOperationException(
                "测试文件选择器控制文件无效。",
                "DOCUMENT_TEST_CONTROL_INVALID");
        }
    }
}

public sealed class DocumentFileOperationException : InvalidOperationException
{
    public DocumentFileOperationException(string message, string code)
        : base(message)
    {
        Code = code;
    }

    public string Code { get; }
}
