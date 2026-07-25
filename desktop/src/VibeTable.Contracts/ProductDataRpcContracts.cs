using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// Frozen v1 provider-neutral change notification. It intentionally contains
/// no provider token, local endpoint, filesystem path, or storage type.
/// </summary>
public sealed record DataChangedEvent(
    string ContractVersion,
    string Topic,
    string EventId,
    long Sequence,
    string OccurredAt,
    string SchemaRevision,
    string DataRevision,
    string? ChangeSetId,
    string TableId,
    IReadOnlyList<string> RecordIds,
    string Operation);
