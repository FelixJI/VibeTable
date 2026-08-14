using System;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Closed product RPC boundary used by the desktop host. Each operation is a
/// named VibeTable use case; there is deliberately no generic invoke method.
/// Provider credentials and physical PocketBase types never cross this seam.
/// </summary>
public interface IProductDataRpcGateway : IDisposable, IRelationLookupRpcGateway
{
    event Action<DataChangedEvent>? DataChanged;
    event Action<JsonElement>? TaskChanged;

    Task<JsonElement> DescribeFieldSettingsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PlanFieldChangeAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyFieldChangeAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> GetFieldChangeStatusAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CancelFieldChangeAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListRecycledFieldsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateTableAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteSchemaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListTablesAsync(JsonElement parameters, CancellationToken token);
    /// <summary>
    /// Returns the normalized table definition for trusted host use. This is
    /// distinct from renderer-facing <c>schema.describe</c>, which carries the
    /// relation-capability compatibility envelope.
    /// </summary>
    Task<JsonElement> GetTableSchemaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> LoadContentProfileAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CommitContentProfileAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteContentProfileAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListRecordDocumentLinksAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CommitRecordDocumentLinkAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> RepairRecordDocumentLinkAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteRecordDocumentLinkAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> QueryPageAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> OpenQueryCursorAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> FetchQueryCursorAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> QueryViewAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ReadRowsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ValidateSnapshotAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewMutationAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyMutationAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ValidateFormulaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ValidateFormulaDraftAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewFormulaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListAttachmentRefsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateFileTokenAsync(JsonElement parameters, CancellationToken token);
    /// <summary>
    /// Trusted-host-only attachment mutation. Host-selected absolute paths
    /// must never be supplied by, or returned to, the renderer.
    /// </summary>
    Task<JsonElement> ApplyHostAttachmentChangeAsync(
        JsonElement parameters,
        CancellationToken token);
    /// <summary>
    /// Trusted-host-only download to a path selected by the native save
    /// dialog. The download capability remains on the private RPC boundary.
    /// </summary>
    Task<JsonElement> SaveAttachmentToHostAsync(
        JsonElement parameters,
        CancellationToken token);
    Task<HistoryPage> ReadHistoryAsync(ReadChangeSetsParams parameters, CancellationToken token);
    Task<RestorePreview> PreviewHistoryRestoreAsync(PreviewRestoreParams parameters, CancellationToken token);
    Task<RestoreResult> ApplyHistoryRestoreAsync(ApplyRestoreParams parameters, CancellationToken token);
    Task<JsonElement> ReconcileAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListPresetsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> SavePresetAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeletePresetAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListVersionsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateVersionAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> SaveVersionAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CompareVersionAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PromoteVersionAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteVersionAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> RegisterImportSourceAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> RegisterExportTargetAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewImportAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyImportAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ExportAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateTaskAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CancelTaskAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> GetTaskStatusAsync(JsonElement parameters, CancellationToken token);

}
