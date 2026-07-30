using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

public interface IWorkspacePathPicker
{
    string? PickWorkspaceRoot();
    string? PickSnapshotExportTarget();
    string? PickSnapshotImportSource();
    string? PickSnapshotExtractTarget();
    string? PickFileUpgradeSource();
}

/// <summary>
/// Converts renderer sentinels into opaque, operation-bound, expiring,
/// single-use grants. Absolute paths remain inside the native host.
/// </summary>
public sealed class WorkspacePathGrantStore
{
    public const string WorkspaceRootSentinel = "host-picker://workspace-root";
    public const string SnapshotExportSentinel = "host-picker://snapshot-export";
    public const string SnapshotImportSentinel = "host-picker://snapshot-import";
    public const string SnapshotExtractSentinel = "host-picker://snapshot-extract";
    public const string FileUpgradeSentinel = "host-picker://file-upgrade";

    private readonly object _gate = new();
    private readonly IWorkspacePathPicker _picker;
    private readonly TimeProvider _timeProvider;
    private readonly Dictionary<string, Grant> _grants =
        new(StringComparer.Ordinal);
    private string? _recentSnapshotImport;

    public WorkspacePathGrantStore(
        IWorkspacePathPicker picker,
        TimeProvider? timeProvider = null)
    {
        _picker = picker ?? throw new ArgumentNullException(nameof(picker));
        _timeProvider = timeProvider ?? TimeProvider.System;
    }

    public JsonElement MaterializeSentinels(
        string method,
        Guid operationId,
        JsonElement parameters)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(method);
        if (operationId == Guid.Empty)
            throw new ArgumentException(
                "Operation id must be non-empty.",
                nameof(operationId));
        JsonObject root = JsonNode.Parse(
            parameters.ValueKind == JsonValueKind.Undefined
                ? "{}"
                : parameters.GetRawText()) as JsonObject
            ?? throw new WorkspacePathGrantException(
                "workspace.request_invalid",
                "Workspace v2 params must be an object.");
        Replace(
            root,
            "selectedRootGrant",
            WorkspaceRootSentinel,
            method,
            operationId,
            "workspace-root",
            _picker.PickWorkspaceRoot);
        Replace(
            root,
            "pathGrant",
            SnapshotExportSentinel,
            method,
            operationId,
            "snapshot-export",
            _picker.PickSnapshotExportTarget);
        Replace(
            root,
            "pathGrant",
            SnapshotImportSentinel,
            method,
            operationId,
            "snapshot-import",
            PickSnapshotImport);
        Replace(
            root,
            "pathGrant",
            SnapshotExtractSentinel,
            method,
            operationId,
            "snapshot-extract",
            _picker.PickSnapshotExtractTarget);
        Replace(
            root,
            "pathGrant",
            FileUpgradeSentinel,
            method,
            operationId,
            "file-upgrade",
            _picker.PickFileUpgradeSource);
        using JsonDocument result = JsonDocument.Parse(root.ToJsonString());
        return result.RootElement.Clone();
    }

    public string Consume(
        string grantId,
        string method,
        Guid operationId,
        string purpose)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(grantId);
        lock (_gate)
        {
            PurgeExpired();
            if (!_grants.TryGetValue(grantId, out Grant? grant)
                || grant.OperationId != operationId
                || !string.Equals(grant.Method, method, StringComparison.Ordinal)
                || !string.Equals(grant.Purpose, purpose, StringComparison.Ordinal))
            {
                throw new WorkspacePathGrantException(
                    "workspace.path_grant_invalid",
                    "The path grant is missing, expired, consumed, or belongs to another operation.");
            }
            _grants.Remove(grantId);
            return grant.Path;
        }
    }

    public WorkspaceSidecarPathGrant? ConsumeForSidecar(
        JsonElement parameters,
        string method,
        Guid operationId)
    {
        string? grantId = parameters.ValueKind == JsonValueKind.Object
            && parameters.TryGetProperty(
                "pathGrant",
                out JsonElement grantElement)
            && grantElement.ValueKind == JsonValueKind.String
                ? grantElement.GetString()
                : null;
        if (grantId is null
            || !grantId.StartsWith(
                "host-path-grant://",
                StringComparison.Ordinal))
        {
            return null;
        }
        string purpose = method switch
        {
            "snapshot.export" => "snapshot-export",
            "snapshot.inspectPackage" or "snapshot.import" => "snapshot-import",
            "snapshot.applyExtract" => "snapshot-extract",
            "fileHistory.upgrade" => "file-upgrade",
            _ => throw new WorkspacePathGrantException(
                "workspace.path_grant_purpose_invalid",
                "This method cannot consume a host path grant."),
        };
        string path = Consume(grantId, method, operationId, purpose);
        return new WorkspaceSidecarPathGrant(
            grantId,
            method,
            operationId,
            purpose,
            path);
    }

    private void Replace(
        JsonObject root,
        string propertyName,
        string sentinel,
        string method,
        Guid operationId,
        string purpose,
        Func<string?> pick)
    {
        if (root[propertyName]?.GetValue<string>() is not string value
            || !string.Equals(value, sentinel, StringComparison.Ordinal))
        {
            return;
        }
        string? selected = pick();
        if (string.IsNullOrWhiteSpace(selected))
        {
            throw new WorkspacePathGrantException(
                "workspace.path_selection_cancelled",
                "The native path selection was cancelled.");
        }
        string path = Path.GetFullPath(selected);
        string grantId = $"host-path-grant://{Guid.NewGuid():D}";
        lock (_gate)
        {
            PurgeExpired();
            _grants.Add(
                grantId,
                new Grant(
                    method,
                    operationId,
                    purpose,
                    path,
                    _timeProvider.GetUtcNow().AddMinutes(5)));
        }
        root[propertyName] = grantId;
    }

    private string? PickSnapshotImport()
    {
        lock (_gate)
        {
            if (_recentSnapshotImport is not null)
            {
                string selected = _recentSnapshotImport;
                _recentSnapshotImport = null;
                return selected;
            }
        }
        string? path = _picker.PickSnapshotImportSource();
        if (!string.IsNullOrWhiteSpace(path))
        {
            lock (_gate)
                _recentSnapshotImport = Path.GetFullPath(path);
        }
        return path;
    }

    private void PurgeExpired()
    {
        DateTimeOffset now = _timeProvider.GetUtcNow();
        foreach (string key in _grants
                     .Where(pair => pair.Value.ExpiresAt <= now)
                     .Select(pair => pair.Key)
                     .ToArray())
        {
            _grants.Remove(key);
        }
    }

    private sealed record Grant(
        string Method,
        Guid OperationId,
        string Purpose,
        string Path,
        DateTimeOffset ExpiresAt);
}

public sealed class WindowsWorkspacePathPicker : IWorkspacePathPicker
{
    public string? PickWorkspaceRoot()
    {
        var dialog = new OpenFolderDialog
        {
            Title = "选择 VibeTable 工作区位置",
            Multiselect = false,
        };
        return dialog.ShowDialog() == true ? dialog.FolderName : null;
    }

    public string? PickSnapshotExportTarget()
    {
        var dialog = new SaveFileDialog
        {
            Title = "导出 VibeTable 快照包",
            AddExtension = true,
            DefaultExt = ".vtsnapshot",
            Filter = "VibeTable 快照包 (*.vtsnapshot)|*.vtsnapshot",
            FileName = $"vibetable-snapshot-{DateTime.Now:yyyyMMdd-HHmmss}.vtsnapshot",
        };
        return dialog.ShowDialog() == true ? dialog.FileName : null;
    }

    public string? PickSnapshotImportSource()
    {
        var dialog = new OpenFileDialog
        {
            Title = "选择 VibeTable 快照包",
            CheckFileExists = true,
            Multiselect = false,
            Filter = "VibeTable 快照包 (*.vtsnapshot)|*.vtsnapshot",
        };
        return dialog.ShowDialog() == true ? dialog.FileName : null;
    }

    public string? PickSnapshotExtractTarget()
    {
        var dialog = new SaveFileDialog
        {
            Title = "从 VibeTable 快照提取文件副本",
            AddExtension = false,
            Filter = "所有文件 (*.*)|*.*",
            FileName = $"vibetable-extract-{DateTime.Now:yyyyMMdd-HHmmss}",
        };
        return dialog.ShowDialog() == true ? dialog.FileName : null;
    }

    public string? PickFileUpgradeSource()
    {
        var dialog = new OpenFileDialog
        {
            Title = "选择升级为正式版本的文件",
            CheckFileExists = true,
            Multiselect = false,
            Filter = "所有文件 (*.*)|*.*",
        };
        return dialog.ShowDialog() == true ? dialog.FileName : null;
    }
}

public sealed class WorkspacePathGrantException(
    string code,
    string message) : Exception(message)
{
    public string Code { get; } = code;
}

public sealed record WorkspaceSidecarPathGrant(
    string GrantId,
    string Method,
    Guid OperationId,
    string Purpose,
    string Path);
