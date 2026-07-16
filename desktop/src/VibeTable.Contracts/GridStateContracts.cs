using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// One column's persisted grid state. Mirrors
/// <c>backend.contracts.grid_state.ColumnState</c>:
/// <c>{"name","width","visible","frozen","order"}</c>.
/// </summary>
public sealed record ColumnState(
    string Name,
    int? Width = null,
    bool Visible = true,
    bool Frozen = false,
    int? Order = null);

/// <summary>
/// The full persisted grid state for one table. Mirrors
/// <c>backend.contracts.grid_state.GridState</c>:
/// <c>{"columns":[...],"sorts":[...],"filters":[...],"keyword","density","forcedRemote","revision"}</c>.
/// </summary>
/// <remarks>
/// State excludes row data and pending edits — only view/layout preferences.
/// <see cref="Revision"/> is the opaque conflict token carried on save.
/// </remarks>
public sealed record GridState(
    IReadOnlyList<ColumnState>? Columns = null,
    IReadOnlyList<SortCondition>? Sorts = null,
    IReadOnlyList<FilterCondition>? Filters = null,
    string? Keyword = null,
    string Density = "comfortable",
    bool ForcedRemote = false,
    string? Revision = null);

/// <summary>
/// Result of <c>gridState.get</c> / <c>gridState.save</c>. Mirrors
/// <c>backend.contracts.grid_state.GridStateResult</c>:
/// <c>{"state":{...},"revision","conflict"}</c>.
/// </summary>
public sealed record GridStateResult(
    GridState State,
    string Revision,
    bool Conflict = false);

/// <summary>
/// B3 mutation revision returned by read/validate methods. Mirrors
/// <c>backend.contracts.mutation.MutationRevision</c>:
/// <c>{"databaseSessionId","schemaRevision","dataRevision"}</c>.
/// </summary>
public sealed record TableRevision(
    string DatabaseSessionId,
    string SchemaRevision,
    int DataRevision);
