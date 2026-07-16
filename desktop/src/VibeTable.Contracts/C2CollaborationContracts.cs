using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// C2 collaboration + insights contracts. Mirrors
/// <c>backend.contracts.collaboration</c> and
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

// --- Comments ---

public sealed record CommentEntry(
    string Id,
    string Collection,
    string ItemId,
    string Comment,
    string? UserId,
    string? UserName,
    string CreatedOn,
    string? EditedOn);

public sealed record ReadCommentsParams(string Collection, string ItemId, int Limit, int Offset);

public sealed record CommentsResult(string Collection, string ItemId, IReadOnlyList<CommentEntry> Comments, int Total);

public sealed record CreateCommentParams(string Collection, string ItemId, string Comment, string RequestId);

public sealed record UpdateCommentParams(string CommentId, string Comment);

public sealed record DeleteCommentParams(string CommentId);

public sealed record CommentMention(string Name, string UserId);

public sealed record SearchMentionsParams(string Prefix, int Limit);

public sealed record MentionsResult(IReadOnlyList<CommentMention> Mentions);

// --- Notifications ---

public static class NotificationFolders
{
    public const string Inbox = "inbox";
    public const string Archive = "archive";
}

public sealed record NotificationEntry(
    string Id,
    string Subject,
    string Message,
    string? Collection,
    string? ItemId,
    string Timestamp,
    bool Read);

public sealed record ReadNotificationsParams(string Folder, int Limit, int Offset);

public sealed record NotificationsResult(IReadOnlyList<NotificationEntry> Notifications, int Total, int UnreadCount);

public sealed record NotificationIdParams(string NotificationId);

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
    IReadOnlyDictionary<string, object?> Query);

public sealed record DashboardEntry(string Id, string Name, string Note, IReadOnlyList<PanelEntry> Panels);

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
    string DirectusCompatibility,
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
    IReadOnlyList<string> TargetPanels);

public sealed record DashboardFilterState(IReadOnlyDictionary<string, object?> Values);

public sealed record PanelSelection(string PanelId, object? Value, IReadOnlyList<string> TargetPanels);
