using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// The closed set of filter <c>operator</c> values. Mirrors the 14 operators of
/// <c>backend.contracts.query.FilterOperator</c>. C# / TypeScript must reject any
/// operator not in this set.
/// </summary>
public static class FilterOperators
{
    public const string Contains = "contains";
    public const string Equal = "eq";
    public const string NotEqual = "ne";
    public const string StartsWith = "starts_with";
    public const string EndsWith = "ends_with";
    public const string Greater = "gt";
    public const string Less = "lt";
    public const string GreaterEqual = "gte";
    public const string LessEqual = "lte";
    public const string Between = "between";
    public const string In = "in";
    public const string IsNull = "is_null";
    public const string IsNotNull = "is_not_null";
    public const string Regex = "regex";
}

/// <summary>
/// One filter condition in the typed query AST. Mirrors
/// <c>backend.contracts.query.FilterCondition</c>:
/// <c>{"field","operator","value","logic"}</c>.
/// </summary>
public sealed record FilterCondition(
    string Field,
    string Operator,
    object? Value = null,
    string Logic = "AND");

/// <summary>
/// One sort condition in the typed query AST. Mirrors
/// <c>backend.contracts.query.SortCondition</c>:
/// <c>{"field","direction","nullsLast"}</c>.
/// </summary>
public sealed record SortCondition(
    string Field,
    string Direction = "asc",
    bool NullsLast = true);

/// <summary>
/// The typed query AST compiled by the Directus table gateway. Mirrors
/// <c>backend.contracts.query.TableQuery</c>:
/// <c>{"keyword","filters":[...],"sorts":[...],"offset","limit"}</c>.
/// </summary>
/// <remarks>
/// The web layer maps Tabulator sorts, filters and search onto this AST. The
/// Directus gateway validates fields against the capability schema and maps
/// supported operators to Directus query parameters; raw filter JSON and SQL
/// fragments never cross the workspace boundary.
/// </remarks>
public sealed record TableQuery(
    string? Keyword = null,
    IReadOnlyList<FilterCondition>? Filters = null,
    IReadOnlyList<SortCondition>? Sorts = null,
    int Offset = 0,
    int Limit = 100);

/// <summary>
/// A stable reference to a query view of a table. Mirrors
/// <c>backend.contracts.selection.QuerySnapshot</c>:
/// <c>{"snapshotId","digest","databaseId","table","schemaRevision","dataRevision","normalizedQuery"}</c>.
/// </summary>
public sealed record QuerySnapshot(
    string SnapshotId,
    string Digest,
    string DatabaseId,
    string Table,
    string SchemaRevision,
    int DataRevision,
    IReadOnlyDictionary<string, object?> NormalizedQuery);

/// <summary>
/// A selection bound to a query snapshot. Mirrors
/// <c>backend.contracts.selection.SelectionSnapshot</c>:
/// <c>{"querySnapshot":{...},"dataRevision","rowKeys":[...]}</c>.
/// </summary>
/// <remarks>
/// <see cref="RowKeys"/> are sorted in the current deterministic query order so
/// B2 can locate the first/last selected row even across remote pages.
/// </remarks>
public sealed record SelectionSnapshot(
    QuerySnapshot QuerySnapshot,
    int DataRevision,
    IReadOnlyList<object> RowKeys);

/// <summary>
/// Result of validating a snapshot against the current state. Mirrors
/// <c>backend.contracts.selection.SnapshotValidation</c>:
/// <c>{"valid","reason","currentDataRevision","currentSchemaRevision"}</c>.
/// </summary>
public sealed record SnapshotValidation(
    bool Valid,
    string? Reason = null,
    int? CurrentDataRevision = null,
    string? CurrentSchemaRevision = null);
