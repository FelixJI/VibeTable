using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// Product history, presets, versions and insights contracts. Mirrors
/// <c>backend.contracts.presets_versions_dashboards</c>. The C# records
/// deserialize the exact camelCase bytes the Python Pydantic service emits.
/// </summary>

// --- Activity / Revisions ---

public sealed record ActivityRevisionEntry(
    string RevisionId,
    string? ActivityId,
    string Action,
    string? UserId,
    string? UserName,
    string Timestamp,
    IReadOnlyDictionary<string, object?> Delta);

public sealed record ReadActivityParams(string Collection, string ItemId, int Limit, int Offset);

public sealed record ActivityResult(
    string Collection,
    string ItemId,
    IReadOnlyList<ActivityRevisionEntry> Revisions,
    int Total,
    string CapabilityHash);

public sealed record PreviewRevertParams(string Collection, string ItemId, string TargetRevision);

public sealed record RevertDiagnostic(string Field, string Severity, string Code, string Message);

public sealed record RevertPreview(
    string Collection,
    string ItemId,
    string TargetRevision,
    string CurrentHash,
    IReadOnlyDictionary<string, IReadOnlyDictionary<string, object?>> Changes,
    IReadOnlyList<RevertDiagnostic> Diagnostics,
    string Token);

public sealed record ApplyRevertParams(string Collection, string ItemId, string Token);

public sealed record RevertResult(string Collection, string ItemId, string RevertedToRevision, IReadOnlyDictionary<string, object?> Item);

// --- Presets ---

public static class PresetScopes
{
    public const string Personal = "personal";
    public const string System = "system";
    public const string Role = "role";
}

public sealed record PresetView(
    IReadOnlyList<IReadOnlyDictionary<string, object?>> Filters,
    IReadOnlyList<IReadOnlyDictionary<string, object?>> Sorts,
    string Search,
    IReadOnlyList<string> VisibleFields,
    string Layout);

public sealed record PresetEntry(
    string Id,
    string Collection,
    string Name,
    string Scope,
    PresetView View,
    string? UserId);

public sealed record ListPresetsParams(string Collection);

public sealed record PresetsResult(string Collection, IReadOnlyList<PresetEntry> Presets);

public sealed record SavePresetParams(string Collection, string Name, PresetView View, string? PresetId);

public sealed record DeletePresetParams(string PresetId);

// --- Content Versions ---

public sealed record ContentVersionEntry(string Id, string Key, string Name, bool Outdated, string MainHash);

public sealed record ListVersionsParams(string Collection, string ItemId);

public sealed record VersionsResult(string Collection, string ItemId, IReadOnlyList<ContentVersionEntry> Versions);

public sealed record CreateVersionParams(string Collection, string ItemId, string Key, string Name);

public sealed record VersionIdParams(string Collection, string ItemId, string VersionId);

public sealed record SaveVersionParams(string Collection, string ItemId, string VersionId, IReadOnlyDictionary<string, object?> Values);

public sealed record VersionCompareResult(
    string Collection,
    string ItemId,
    string VersionId,
    bool Outdated,
    string MainHash,
    IReadOnlyDictionary<string, IReadOnlyDictionary<string, object?>> Differences);

public sealed record PromoteVersionParams(string Collection, string ItemId, string VersionId, string MainHash);

// --- Dashboards / Panels ---

public static class PanelTypes
{
    public const string Label = "label";
    public const string Metric = "metric";
    public const string MetricList = "metric-list";
    public const string List = "list";
    public const string TimeSeries = "time-series";
    public const string Bar = "bar";
    public const string Line = "line";
    public const string Donut = "donut";
    public const string Pie = "pie";
    public const string Custom = "custom";
}

public sealed record PanelPosition(int X, int Y, int Width, int Height);

public sealed record PanelEntry(
    string Id,
    string DashboardId,
    string Name,
    string Type,
    PanelPosition Position,
    IReadOnlyDictionary<string, object?> Options,
    IReadOnlyDictionary<string, object?> Query,
    string Note = "",
    string? Icon = null,
    string? Color = null,
    bool ShowHeader = true);

public sealed record DashboardEntry(
    string Id,
    string Name,
    string Note,
    IReadOnlyList<PanelEntry> Panels,
    string? Icon = null,
    string? Color = null);

public sealed record ListDashboardsParams;

public sealed record DashboardsResult(IReadOnlyList<DashboardEntry> Dashboards);

public sealed record DashboardIdParams(string DashboardId);

public sealed record SaveDashboardParams(string Name, string Note, string? DashboardId);

public sealed record SavePanelParams(
    string DashboardId,
    string Name,
    string Type,
    PanelPosition Position,
    IReadOnlyDictionary<string, object?> Options,
    IReadOnlyDictionary<string, object?> Query,
    string? PanelId);

public sealed record PanelIdParams(string DashboardId, string PanelId);

public sealed record PanelManifestEntry(
    string Type,
    PanelPosition MinSize,
    IReadOnlyDictionary<string, object?> OptionsSchema,
    string RendererVersion);

public sealed record PanelManifestResult(
    string ManifestVersion,
    string QueryContract,
    IReadOnlyList<PanelManifestEntry> Panels);

// --- Dashboard interactive filter ---

public static class DashboardFilterTypes
{
    public const string DateRange = "date-range";
    public const string Enum = "enum";
    public const string User = "user";
    public const string Relation = "relation";
    public const string NumberRange = "number-range";
}

public sealed record DashboardFilterVariable(
    string Key,
    string Label,
    string Type,
    object? DefaultValue,
    IReadOnlyList<string> AllowedFields,
    IReadOnlyList<string> TargetPanels,
    IReadOnlyDictionary<string, string>? FieldBindings = null);

public sealed record DashboardFilterState(IReadOnlyDictionary<string, object?> Values);

public sealed record PanelSelection(string PanelId, object? Value, IReadOnlyList<string> TargetPanels);
