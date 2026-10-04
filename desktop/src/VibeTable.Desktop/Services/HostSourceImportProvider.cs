using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

// This port is internal to trusted Host adapters. Credentials, URLs and paths
// stay in the provider; the renderer receives only a session ID and plan token.
internal interface IHostSourceImportProvider : IDisposable
{
    Task<HostSourceImportSnapshot> ReadAsync(CancellationToken token);
    Task<HostSourceImportObservation> ObserveAsync(CancellationToken token);
    Task<Stream> OpenAttachmentAsync(HostSourceImportAttachment attachment, CancellationToken token);
}

internal sealed record HostSourceImportSnapshot(
    string Provider, string ContainerId, string DisplayName, string Version,
    HostSourceImportReadWindow ReadWindow, HostSourceImportTable[] Tables,
    HostSourceImportAttachment[] Attachments)
{
    public string Contract => HostSourceImportResult.ContractName;
}
internal sealed record HostSourceImportReadWindow(string StartedAt, string FinishedAt, string Consistency);
internal sealed record HostSourceImportTable(string Id, string Name, string Version,
    string PrimaryFieldId, HostSourceImportField[] Fields, HostSourceImportRecord[] Records);
internal sealed record HostSourceImportField(string Id, string Name, string Kind,
    string ValueKind, bool Required, HostSourceImportOption[] Options,
    HostSourceImportRelation? Relation = null, HostSourceImportNumberFormat? NumberFormat = null,
    string Timezone = "", string Definition = "");
internal sealed record HostSourceImportOption(string Id, string Label, string Color);
internal sealed record HostSourceImportRelation(string TargetTableId, string TargetFieldId, string Cardinality);
internal sealed record HostSourceImportNumberFormat(bool OnlyInt, int DisplayScale, string ScaleMode,
    bool TrimTrailingZeros, bool UseGrouping, string Currency, string PercentStorage, string? Unit);
internal sealed record HostSourceImportRecord(string Id, IReadOnlyDictionary<string, JsonElement> Values);
internal sealed record HostSourceImportAttachment(string Id, string TableId, string RecordId,
    string FieldId, string Name, string Mime, long Size);
internal sealed record HostSourceImportOptions(string[] SelectedTableIds,
    HostSourceImportTargetName[] TargetNames, HostSourceImportDecision[] Decisions, bool ConfirmReverse);
internal sealed record HostSourceImportTargetName(string TableId, string Name);
internal sealed record HostSourceImportDecision(string TableId, string FieldId, string Policy,
    string TargetKind, string TargetName, bool Confirmed);
internal sealed record HostSourceImportObservation(string Version, IReadOnlyDictionary<string, string> TableVersions);
internal sealed record HostSourceImportPreview(string ProviderSessionId, string Token,
    DateTimeOffset ExpiresAt, JsonElement Plan);

/// <summary>Validated Go outcome, retaining all mapping and provenance details.</summary>
internal sealed record HostSourceImportResult(JsonElement Wire, string JobId, string State,
    int Created, int Total, int NotSubmitted, int UnknownRecords, ulong SessionEpoch)
{
    internal const string ContractName = "vibetable.source-import.v1";

    internal static HostSourceImportResult Parse(JsonElement wire)
    {
        if (wire.ValueKind != JsonValueKind.Object
            || wire.GetProperty("contract").GetString() != ContractName)
            throw new JsonException("Invalid source import result contract.");
        string job = wire.GetProperty("jobId").GetString() ?? "";
        string state = wire.GetProperty("state").GetString() ?? "";
        int created = wire.GetProperty("created").GetInt32();
        int total = wire.GetProperty("total").GetInt32();
        int pending = wire.GetProperty("notSubmitted").GetInt32();
        int unknown = wire.GetProperty("unknownRecords").GetInt32();
        ulong epoch = wire.GetProperty("sessionEpoch").GetUInt64();
        string? unresolved = wire.TryGetProperty("unknownBatch", out JsonElement batch)
            ? batch.GetString() : null;
        bool settled = wire.GetProperty("stage").GetString() == "settled"
            && wire.TryGetProperty("finishedAt", out JsonElement finished)
            && !string.IsNullOrWhiteSpace(finished.GetString());
        if (string.IsNullOrWhiteSpace(job) || epoch == 0
            || state is not ("running" or "succeeded" or "failed" or "cancelled" or "unknown" or "interrupted")
            || created < 0 || pending < 0 || unknown < 0 || total < 0
            || (long)created + pending + unknown != total
            || (unknown > 0 && string.IsNullOrWhiteSpace(unresolved))
            || (state == "succeeded" && (pending != 0 || unknown != 0
                || !string.IsNullOrEmpty(unresolved) || !settled))
            || wire.GetProperty("targets").ValueKind is not (JsonValueKind.Array or JsonValueKind.Null)
            || wire.GetProperty("batches").ValueKind is not (JsonValueKind.Array or JsonValueKind.Null)
            || wire.GetProperty("diagnostics").ValueKind is not (JsonValueKind.Array or JsonValueKind.Null))
            throw new JsonException("Invalid source import result facts.");
        return new(wire.Clone(), job, state, created, total, pending, unknown, epoch);
    }
}
