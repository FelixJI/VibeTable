using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

public sealed record ColumnFilterOption(string Value, string Label);

/// <summary>
/// One column's read-side schema. Mirrors
/// <c>backend.contracts.table.ColumnSchema</c> (camelCase wire form):
/// <c>{"name","title","dataType","editable","nullable","scale","precision",</c>
/// <c>"fieldId","kind","relationId","lookupId","attachmentPolicy"}</c>.
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
    string? LookupId = null,
    IReadOnlyDictionary<string, object?>? AttachmentPolicy = null,
    IReadOnlyList<string>? FilterOperators = null,
    string FilterInput = "text",
    IReadOnlyList<ColumnFilterOption>? FilterOptions = null);

/// <summary>
/// Result of opening the configured logical source through the table gateway:
/// <c>{"tables":[...],"views":[...],"displayNames":{...}}</c>.
/// </summary>
public sealed record DatabaseOpenResult
{
    [JsonConstructor]
    public DatabaseOpenResult(
        IReadOnlyList<string> tables,
        IReadOnlyList<string> views,
        IReadOnlyDictionary<string, string> displayNames)
    {
        Tables = tables ?? throw new JsonException("Database catalog omitted tables.");
        Views = views ?? throw new JsonException("Database catalog omitted views.");
        DisplayNames = RequireCanonicalDisplayNames(tables, views, displayNames);
    }

    public IReadOnlyList<string> Tables { get; }

    public IReadOnlyList<string> Views { get; }

    public IReadOnlyDictionary<string, string> DisplayNames { get; }

    internal static IReadOnlyDictionary<string, string> RequireCanonicalDisplayNames(
        IReadOnlyList<string> tables,
        IReadOnlyList<string> views,
        IReadOnlyDictionary<string, string>? displayNames)
    {
        if (displayNames is null)
        {
            throw new JsonException("Database catalog omitted displayNames.");
        }
        foreach (string collection in tables)
        {
            RequireCanonicalDisplayName(collection, displayNames);
        }
        foreach (string collection in views)
        {
            RequireCanonicalDisplayName(collection, displayNames);
        }
        return displayNames;
    }

    private static void RequireCanonicalDisplayName(
        string collection,
        IReadOnlyDictionary<string, string> displayNames)
    {
        if (!displayNames.TryGetValue(collection, out string? displayName)
            || string.IsNullOrWhiteSpace(displayName))
        {
            throw new JsonException(
                $"Database catalog omitted the canonical display name for '{collection}'.");
        }
    }
}

/// <summary>
/// Summary of collections and views exposed by the configured source.
/// </summary>
public sealed record TableSummary
{
    public TableSummary(
        IReadOnlyList<string> tables,
        IReadOnlyList<string> views,
        IReadOnlyDictionary<string, string> displayNames)
    {
        Tables = tables ?? throw new ArgumentNullException(nameof(tables));
        Views = views ?? throw new ArgumentNullException(nameof(views));
        DisplayNames = DatabaseOpenResult.RequireCanonicalDisplayNames(
            tables,
            views,
            displayNames);
    }

    public IReadOnlyList<string> Tables { get; }

    public IReadOnlyList<string> Views { get; }

    public IReadOnlyDictionary<string, string> DisplayNames { get; }
}

/// <summary>
/// One page of rows from a product collection view:
/// <c>{"table","columns":[...],"rows":[{...,"rowKey"}],"offset","limit","totalRows","mode",</c>
/// <c>"filteredRows","querySnapshot","revision"}</c>.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="Rows"/> is a list of plain dictionaries keyed by column name plus
/// the hidden transport field <c>rowKey</c>. The web layer hides
/// <c>rowKey</c> from visible columns.
/// </para>
/// <para><see cref="Mode"/> is always <c>"remote"</c>. Every page is a bounded,
/// revision-bound server window; the renderer never owns the full dataset.</para>
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
    MutationRevision? Revision = null,
    IReadOnlyList<GroupRow>? GroupRows = null,
    int GroupOffset = 0,
    int GroupLimit = 100,
    bool HasMoreGroups = false,
    string? NextCursor = null,
    bool HasMore = false);
