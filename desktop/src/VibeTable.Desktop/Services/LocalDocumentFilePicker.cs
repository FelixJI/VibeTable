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

public sealed record DocumentImportRequest(
    string WorkspaceId,
    string? FolderId,
    string? ItemCollection = null,
    string? ItemId = null,
    string LinkType = "attachment");

public sealed record DocumentImportResult(
    string DocumentId,
    string WorkspaceId,
    string? FolderId,
    string DisplayName,
    string MimeType,
    string SchemeId,
    string RevisionId,
    string? LinkId);

public sealed record DocumentRelinkResult(
    string DocumentId,
    string WorkspaceId,
    string DisplayName);

public sealed class DocumentFileOperationException : InvalidOperationException
{
    public DocumentFileOperationException(string message, string code)
        : base(message)
    {
        Code = code;
    }

    public string Code { get; }
}
