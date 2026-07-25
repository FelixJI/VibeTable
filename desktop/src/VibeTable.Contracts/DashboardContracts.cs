using System.Collections.Generic;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>Closed aggregate operations accepted by dashboard queries.</summary>
public static class DashboardAggregates
{
    public const string Count = "count";
    public const string CountDistinct = "countDistinct";
    public const string Sum = "sum";
    public const string Average = "avg";
    public const string Minimum = "min";
    public const string Maximum = "max";
}

public sealed record DashboardMeasure(string Key, string Op, string? Field = null);

public sealed record DashboardTimeBucket(
    string Field,
    string Unit,
    string Timezone = "UTC");

/// <summary>
/// Discriminated dashboard query root. Only the two declared structured query
/// shapes can cross the desktop bridge; raw provider query JSON is not exposed.
/// </summary>
[JsonPolymorphic(TypeDiscriminatorPropertyName = "kind")]
[JsonDerivedType(typeof(DashboardRecordQuery), "records")]
[JsonDerivedType(typeof(DashboardAggregateQuery), "aggregate")]
public abstract record DashboardPanelQuery;

public sealed record DashboardRecordQuery(
    string Collection,
    IReadOnlyList<string> Fields,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<FilterCondition>? Filters = null,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<SortCondition>? Sorts = null,
    int Limit = 20) : DashboardPanelQuery;

public sealed record DashboardAggregateQuery(
    string Collection,
    IReadOnlyList<DashboardMeasure> Measures,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<string>? Dimensions = null,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<FilterCondition>? Filters = null,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    DashboardTimeBucket? TimeBucket = null,
    int Limit = 100,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    int? TopN = null) : DashboardPanelQuery;

public sealed record DashboardQueryLimits(
    int MaxConcurrentRequests = 6,
    int MaxSeriesPoints = 50_000,
    int MaxPanelPoints = 100_000,
    int MaxCategoryPoints = 5_000,
    int DefaultTopN = 100,
    int MaxPieSlices = 50,
    int MaxListRows = 100);

public sealed record DashboardInteraction(
    string SourcePanelId,
    IReadOnlyList<string> TargetPanelIds,
    string TargetField,
    string? SourceField = null);

public sealed record DashboardManagedConfig(
    int ConfigVersion = 1,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<DashboardFilterVariable>? GlobalFilters = null,
    [property: JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    IReadOnlyList<DashboardInteraction>? Interactions = null,
    int RefreshInterval = 0);

public sealed record DashboardPanelDraft(
    string ClientId,
    string Name,
    string Type,
    PanelPosition Position,
    IReadOnlyDictionary<string, object?> Options,
    DashboardPanelQuery? Query = null,
    string? PanelId = null,
    string? Note = null,
    string? Icon = null,
    string? Color = null,
    bool ShowHeader = true);

public sealed record DashboardWorkspaceResult(
    DashboardEntry Dashboard,
    DashboardManagedConfig Config,
    string Revision,
    string AtomicSaveEndpoint,
    DashboardQueryLimits QueryLimits);

public sealed record SaveDashboardDraftParams(
    string Name,
    string Note,
    IReadOnlyList<DashboardPanelDraft> Panels,
    IReadOnlyList<string> DeletedPanelIds,
    DashboardManagedConfig Config,
    string IdempotencyKey,
    string? DashboardId = null,
    string? ExpectedRevision = null,
    string? Icon = null,
    string? Color = null);

public sealed record DashboardWorkspaceParams(string DashboardId);

/// <summary>Empty parameter object for product-owned insights operations.</summary>
public sealed record InsightsEmptyParams;

public sealed record DeleteDashboardResult(string Deleted);

public sealed record SaveDashboardDraftResult(
    DashboardWorkspaceResult Workspace,
    IReadOnlyDictionary<string, string> ClientPanelIds,
    bool Atomic);

public sealed record ExecuteDashboardQueryParams(
    string PanelType,
    DashboardPanelQuery Query,
    string? RequestId = null);

public sealed record DashboardQueryResult(
    IReadOnlyList<IReadOnlyDictionary<string, JsonElement>> Rows,
    bool Truncated,
    int MaxPoints);

/// <summary>One manifest response plus the backend-enforced query limits.</summary>
public sealed record DashboardManifestBundle(
    PanelManifestResult Manifest,
    DashboardQueryLimits QueryLimits);

/// <summary>Runtime host capabilities sent with database.opened.</summary>
public sealed record HostFeatureFlags(bool Dashboards = false);
