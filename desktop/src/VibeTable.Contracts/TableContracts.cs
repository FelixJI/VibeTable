using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// One column's read-side schema. Mirrors
/// <c>backend.contracts.table.ColumnSchema</c> (camelCase wire form):
/// <c>{"name","title","dataType","editable","nullable","scale","precision",</c>
/// <c>"fieldId","kind","relationId","lookupId"}</c>.
/// </summary>
public sealed record ColumnSchema(
    string Name,
    string Title,
    string DataType,
    bool Editable,
    bool Nullable,
    int? Scale = null,
    int? Precision = null,
    string? FieldId = null,
    string Kind = "scalar",
    string? RelationId = null,
    string? LookupId = null);

/// <summary>
/// Result of opening the configured logical source through the table gateway:
/// <c>{"tables":[...],"views":[...],"openMode","leaseHolder"}</c>.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="OpenMode"/> is retained for wire compatibility. The Directus
/// gateway currently returns <c>"remote"</c>; it does not negotiate a local
/// database lease.
/// </para>
/// </remarks>
public sealed record DatabaseOpenResult(
    IReadOnlyList<string> Tables,
    IReadOnlyList<string> Views,
    string OpenMode = "read_write",
    string? LeaseHolder = null,
    IReadOnlyDictionary<string, string>? DisplayNames = null);

/// <summary>
/// Summary of collections and views exposed by the configured source.
/// </summary>
public sealed record TableSummary(
    IReadOnlyList<string> Tables,
    IReadOnlyList<string> Views,
    IReadOnlyDictionary<string, string>? DisplayNames = null);

/// <summary>
/// One page of rows from a Directus-backed collection view:
/// <c>{"table","columns":[...],"rows":[{...,"rowKey"}],"offset","limit","totalRows","mode",</c>
/// <c>"filteredRows","querySnapshot","revision"}</c>.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="Rows"/> is a list of plain dictionaries keyed by column name plus
/// the hidden transport field <c>rowKey</c>. The web layer hides
/// <c>rowKey</c> from visible columns.
/// </para>
/// <para>
/// <see cref="Mode"/> is <c>"client"</c> when
/// <see cref="TotalRows"/> &lt;= 25_000 (the host client-row budget) and
/// <c>"remote"</c> otherwise — a hint to the web layer to page over RPC instead
/// of loading the whole dataset.
/// </para>
/// </remarks>
public sealed record TablePage(
    string Table,
    IReadOnlyList<ColumnSchema> Columns,
    IReadOnlyList<Dictionary<string, object?>> Rows,
    int Offset,
    int Limit,
    int TotalRows,
    string Mode,
    int? FilteredRows = null,
    QuerySnapshot? QuerySnapshot = null,
    MutationRevision? Revision = null);
