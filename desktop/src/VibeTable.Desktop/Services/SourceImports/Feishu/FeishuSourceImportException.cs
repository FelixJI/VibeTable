namespace VibeTable.Desktop.Services;

/// <summary>
/// Categorized Feishu connector failures. Messages are user-facing Chinese
/// text with actionable guidance; the raw Feishu business code rides along
/// for Host reports. Credentials never appear in messages.
/// </summary>
internal enum FeishuSourceImportErrorKind
{
    /// <summary>The user-supplied link/token/input is not usable.</summary>
    InvalidInput,

    /// <summary>The access token is missing, invalid or expired.</summary>
    InvalidToken,

    /// <summary>The token is valid but lacks permission for the resource.</summary>
    Forbidden,

    /// <summary>The addressed app/table/attachment does not exist.</summary>
    NotFound,

    /// <summary>Rate limited beyond the bounded retry budget.</summary>
    RateLimited,

    /// <summary>Transient network/server failure beyond the retry budget.</summary>
    Transient,

    /// <summary>HTTP 200 with a non-zero Feishu business code.</summary>
    Business,

    /// <summary>The response violates the verified protocol shape.</summary>
    Protocol,

    /// <summary>The source exceeds the read-only migration capacity.</summary>
    Capacity,

    /// <summary>The provider/connection was disposed.</summary>
    Disposed,
}

internal sealed class FeishuSourceImportException : Exception
{
    internal FeishuSourceImportErrorKind Kind { get; }
    internal string? ApiCode { get; }

    internal FeishuSourceImportException(FeishuSourceImportErrorKind kind, string message, string? apiCode = null)
        : base(message)
    {
        Kind = kind;
        ApiCode = apiCode;
    }
}
