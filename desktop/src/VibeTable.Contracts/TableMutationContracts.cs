using System.Collections.Generic;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>
/// One column's editable schema supplied by the Directus table gateway. The <c>editor</c> and
/// <c>validation</c> fields are deliberately untyped dictionaries here: the
/// discriminated-union <c>kind</c> discriminator is validated by the web layer
/// and the C# host treats them as opaque payloads it forwards to the
/// WebView.
/// </summary>
public sealed record ColumnEditSchema(
    string Name,
    string StorageName,
    string DataType,
    bool Editable,
    bool Nullable,
    bool PrimaryKey,
    IReadOnlyDictionary<string, object?> Editor,
    IReadOnlyList<IReadOnlyDictionary<string, object?>> Validation);

/// <summary>
/// Editable schema result for a collection.
/// </summary>
public sealed record EditSchemaResult(
    string Table,
    string SchemaRevision,
    string RowKeyKind,
    bool RowKeyStable,
    bool Editable,
    IReadOnlyList<ColumnEditSchema> Columns);

/// <summary>
/// Mutation revision metadata used for stale-write guards.
/// </summary>
public sealed record MutationRevision(
    string DatabaseSessionId,
    string SchemaRevision,
    int DataRevision);

/// <summary>
/// Result of updating one Directus item field.
/// </summary>
public sealed record UpdateCellResult(
    object RowKey,
    string Column,
    object? StoredValue,
    IReadOnlyDictionary<string, object?> CurrentRow,
    MutationRevision Revision);

/// <summary>
/// Result of creating one Directus item.
/// </summary>
public sealed record InsertRowResult(
    object RowKey,
    IReadOnlyDictionary<string, object?> Row,
    MutationRevision Revision);

/// <summary>
/// Result of deleting Directus items.
/// </summary>
public sealed record DeleteRowsResult(
    IReadOnlyList<object> DeletedRowKeys,
    MutationRevision Revision);

/// <summary>
/// Result of re-reading selected Directus items.
/// </summary>
public sealed record ReadRowsResult(
    IReadOnlyList<IReadOnlyDictionary<string, object?>> Rows,
    MutationRevision Revision);
