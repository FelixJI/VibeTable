using System.Collections.Generic;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>
/// G3 document workspace contracts. Mirrors
/// <c>backend.contracts.document_workspace</c>.
/// </summary>

// --- readFolder ---

public sealed record ReadFolderParams(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId
);

public sealed record DocumentSummary(
    [property: JsonPropertyName("linkId")] string? LinkId,
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("workspaceId")] string WorkspaceId,
    [property: JsonPropertyName("fileName")] string FileName,
    [property: JsonPropertyName("mimeType")] string? MimeType,
    [property: JsonPropertyName("mainHead")] string? MainHead,
    [property: JsonPropertyName("mainHash")] string? MainHash,
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("linkType")] string? LinkType,
    [property: JsonPropertyName("folderRelativePath")] string? FolderRelativePath,
    [property: JsonPropertyName("isMissing")] bool IsMissing
);

public sealed record FolderResult(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("folderId")] string? FolderId,
    [property: JsonPropertyName("documents")] List<DocumentSummary> Documents
);

public sealed record ReadDocumentsParams(
    [property: JsonPropertyName("limit")] int Limit,
    [property: JsonPropertyName("offset")] int Offset
);

public sealed record DocumentListResult(
    [property: JsonPropertyName("documents")] List<DocumentSummary> Documents,
    [property: JsonPropertyName("total")] int Total
);

// --- registerDocument ---

public sealed record RegisterDocumentParams(
    [property: JsonPropertyName("workspaceId")] string WorkspaceId,
    [property: JsonPropertyName("workspaceName")] string WorkspaceName,
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("fileName")] string FileName,
    [property: JsonPropertyName("mimeType")] string MimeType,
    [property: JsonPropertyName("schemeId")] string SchemeId,
    [property: JsonPropertyName("revisionId")] string RevisionId,
    [property: JsonPropertyName("hash")] string Hash,
    [property: JsonPropertyName("size")] long Size,
    [property: JsonPropertyName("createdAt")] string CreatedAt,
    [property: JsonPropertyName("itemCollection")] string? ItemCollection,
    [property: JsonPropertyName("itemId")] string? ItemId,
    [property: JsonPropertyName("linkType")] string LinkType
);

public sealed record RegisterDocumentResult(
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("linkId")] string? LinkId
);

// --- publishIndexBatch ---

public sealed record RevisionIndexEntry(
    [property: JsonPropertyName("revisionId")] string RevisionId,
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("schemeId")] string SchemeId,
    [property: JsonPropertyName("parentRevisionId")] string? ParentRevisionId,
    [property: JsonPropertyName("sequence")] int Sequence,
    [property: JsonPropertyName("versionLabel")] string VersionLabel,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("hash")] string Hash,
    [property: JsonPropertyName("size")] long Size,
    [property: JsonPropertyName("mimeType")] string MimeType,
    [property: JsonPropertyName("createdAt")] string CreatedAt,
    [property: JsonPropertyName("createdBy")] string? CreatedBy,
    [property: JsonPropertyName("deviceId")] string? DeviceId,
    [property: JsonPropertyName("comment")] string? Comment
);

public sealed record PublishHeadAdvance(
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("schemeId")] string SchemeId,
    [property: JsonPropertyName("expectedHeadRevisionId")] string? ExpectedHeadRevisionId,
    [property: JsonPropertyName("newHeadRevisionId")] string NewHeadRevisionId
);

public sealed record PublishIndexBatchParams(
    [property: JsonPropertyName("revisions")] List<RevisionIndexEntry> Revisions,
    [property: JsonPropertyName("headAdvance")] PublishHeadAdvance? HeadAdvance,
    [property: JsonPropertyName("idempotencyKey")] string IdempotencyKey
);

public sealed record PublishResult(
    [property: JsonPropertyName("revisionId")] string RevisionId,
    [property: JsonPropertyName("status")] string Status
);

public sealed record PublishIndexBatchResult(
    [property: JsonPropertyName("results")] List<PublishResult> Results,
    [property: JsonPropertyName("conflicts")] List<string> Conflicts
);

// --- link / unlink ---

public sealed record LinkDocumentParams(
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("itemCollection")] string ItemCollection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("linkType")] string LinkType
);

public sealed record LinkResult(
    [property: JsonPropertyName("linkId")] string LinkId,
    [property: JsonPropertyName("status")] string Status
);

public sealed record UnlinkDocumentParams(
    [property: JsonPropertyName("linkId")] string LinkId
);

// --- readDocumentHistory ---

public sealed record ReadDocumentHistoryParams(
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("limit")] int Limit,
    [property: JsonPropertyName("offset")] int Offset
);

public sealed record DocumentRevisionEntry(
    [property: JsonPropertyName("revisionId")] string RevisionId,
    [property: JsonPropertyName("schemeName")] string? SchemeName,
    [property: JsonPropertyName("sequence")] int Sequence,
    [property: JsonPropertyName("versionLabel")] string VersionLabel,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("hash")] string Hash,
    [property: JsonPropertyName("size")] long Size,
    [property: JsonPropertyName("createdAt")] string CreatedAt,
    [property: JsonPropertyName("createdBy")] string? CreatedBy
);

public sealed record DocumentHistoryResult(
    [property: JsonPropertyName("documentId")] string DocumentId,
    [property: JsonPropertyName("revisions")] List<DocumentRevisionEntry> Revisions,
    [property: JsonPropertyName("total")] int Total
);

// --- desktop host notifications ---

/// <summary>
/// Typed payload for the host -&gt; web <c>document.operationFailed</c>
/// notification. File-operation failures intentionally use a domain-specific
/// channel instead of the table-oriented generic operation failure route.
/// </summary>
public sealed record DocumentOperationFailedPayload(
    [property: JsonPropertyName("message")] string Message,
    [property: JsonPropertyName("code")] string? Code
);

// --- desktop file-version requests ---

public sealed record DocumentEntryHandleRequest(
    [property: JsonPropertyName("entryHandle")] string EntryHandle
);

public sealed record DocumentCommitRevisionRequestedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("note")] string? Note,
    [property: JsonPropertyName("schemeHandle")] string? SchemeHandle
);

public sealed record DocumentPromoteVersionRequestedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("versionLabel")] string VersionLabel,
    [property: JsonPropertyName("note")] string? Note,
    [property: JsonPropertyName("schemeHandle")] string? SchemeHandle
);

public sealed record DocumentRevisionHandleRequest(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("revisionHandle")] string RevisionHandle
);

public sealed record DocumentSchemeCreateRequestedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("baseRevisionHandle")] string? BaseRevisionHandle
);

public sealed record DocumentSchemeHandleRequest(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("schemeHandle")] string SchemeHandle
);

public sealed record DocumentSchemeRenameRequestedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("schemeHandle")] string SchemeHandle,
    [property: JsonPropertyName("name")] string Name
);

// --- desktop file-version responses ---

public sealed record DocumentVersionCommittedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("revisionHandle")] string RevisionHandle,
    [property: JsonPropertyName("currentRevision")] string CurrentRevision,
    [property: JsonPropertyName("schemeHandle")] string? SchemeHandle
);

public sealed record DocumentRevisionPreviewCompletedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("revisionHandle")] string RevisionHandle,
    [property: JsonPropertyName("action")] string Action
);

public sealed record DocumentSchemeBridgeEntry(
    [property: JsonPropertyName("schemeHandle")] string SchemeHandle,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("currentRevisionHandle")] string? CurrentRevisionHandle,
    [property: JsonPropertyName("currentRevisionLabel")] string? CurrentRevisionLabel,
    [property: JsonPropertyName("archived")] bool Archived,
    [property: JsonPropertyName("active")] bool Active
);

public sealed record DocumentSchemeListLoadedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("schemes")] IReadOnlyList<DocumentSchemeBridgeEntry> Schemes
);

public sealed record DocumentSchemeMutationCompletedPayload(
    [property: JsonPropertyName("entryHandle")] string EntryHandle,
    [property: JsonPropertyName("scheme")] DocumentSchemeBridgeEntry Scheme
);
