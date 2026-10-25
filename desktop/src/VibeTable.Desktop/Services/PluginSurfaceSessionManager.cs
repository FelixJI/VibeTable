using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Linq;
using System.Security.Cryptography;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public sealed record PluginSurfaceSession(
    string SurfaceToken,
    string ProjectKey,
    string PluginId,
    PluginPackageRevision Revision,
    Uri DocumentUri,
    string IframeSandbox,
    string ContentSecurityPolicy)
{
    public PluginProjectContext? Context { get; init; }
    public int InstallationRevision { get; init; }
    public string? InstallationId { get; init; }
    public string ApiVersion { get; init; } = "";
    public string PluginVersion { get; init; } = "";
    public string ActionId { get; init; } = "";
    public string Risk { get; init; } = "";
    public string Entry { get; init; } = "";
    public string Presentation { get; init; } = "surface";
    public string ActiveCollection { get; init; } = "";
    public PluginRuntimeCommandContext? InvocationContext { get; init; }
}

public sealed class PluginSurfacePolicyException : Exception
{
    public PluginSurfacePolicyException(string code, string message) : base(message)
        => Code = code;

    public string Code { get; }
}

/// <summary>
/// Owns short-lived custom-surface capabilities. Tokens are renderer-local
/// capabilities and are removed immediately when their surface closes.
/// </summary>
public sealed class PluginSurfaceSessionManager
{
    private static readonly HashSet<string> AllowedEvents = new(StringComparer.Ordinal)
    {
        PluginSurfaceEvents.Ready,
        PluginSurfaceEvents.Close,
        PluginSurfaceEvents.Action,
    };

    private static readonly HashSet<string> ThemeVariables = new(StringComparer.Ordinal)
    {
        "--vt-plugin-bg",
        "--vt-plugin-surface",
        "--vt-plugin-text",
        "--vt-plugin-text-muted",
        "--vt-plugin-border",
        "--vt-plugin-primary",
        "--vt-plugin-danger",
        "--vt-plugin-radius",
        "--vt-plugin-space-unit",
    };

    internal const int RetiredCloseLimit = 256;
    private readonly object _gate = new();
    private readonly HashSet<string> _mountedTokens = new(StringComparer.Ordinal);
    private readonly HashSet<string> _retiredTokens = new(StringComparer.Ordinal);
    private readonly Queue<string> _retiredOrder = new();
    private readonly ConcurrentDictionary<string, PluginSurfaceSession> _sessions =
        new(StringComparer.Ordinal);

    public PluginSurfaceSession Open(PluginPackageRevision revision, string entryPath)
        => Open(string.Empty, string.Empty, revision, entryPath);

    public PluginSurfaceSession Open(
        string projectKey,
        string pluginId,
        PluginPackageRevision revision,
        string entryPath,
        Uri? documentUri = null)
    {
        ArgumentNullException.ThrowIfNull(revision);
        string normalized = PluginResourceHost.NormalizePackagePath(entryPath);
        string token = Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant();
        var session = new PluginSurfaceSession(
            token,
            projectKey,
            pluginId,
            revision,
            documentUri ?? new Uri(revision.Origin, normalized),
            "allow-scripts allow-same-origin",
            PluginResourceHost.ContentSecurityPolicy);
        if (!_sessions.TryAdd(token, session))
        {
            throw new InvalidOperationException("Could not allocate a unique surface token.");
        }
        Changed?.Invoke();
        return session;
    }

    internal event Action<string>? Closed;

    /// <summary>
    /// Raised after a surface is allocated, mounted or closed. Callbacks run
    /// outside the gate; mounted frames are distinct from catalog capabilities.
    /// </summary>
    public event Action? Changed;

    /// <summary>Number of currently open plugin surfaces.</summary>
    public int ActiveSurfaceCount => _sessions.Count;

    /// <summary>Number of plugin frames mounted by the current renderer.</summary>
    internal int MountedSurfaceCount
    {
        get { lock (_gate) return _mountedTokens.Count; }
    }

    internal bool MarkMounted(string surfaceToken)
    {
        bool changed;
        lock (_gate)
        {
            if (!_sessions.ContainsKey(surfaceToken)) return false;
            changed = _mountedTokens.Add(surfaceToken);
        }
        if (changed) Changed?.Invoke();
        return true;
    }

    public bool TryAccept(PluginSurfaceEvent message, out PluginSurfaceSession? session)
    {
        session = null;
        if (message is null
            || !string.Equals(message.Contract, PluginContractVersions.Surface, StringComparison.Ordinal)
            || string.IsNullOrWhiteSpace(message.SurfaceToken)
            || !AllowedEvents.Contains(message.Event))
        {
            return false;
        }

        if (!string.Equals(message.Event, PluginSurfaceEvents.Close, StringComparison.Ordinal))
            return _sessions.TryGetValue(message.SurfaceToken, out session);
        if (!TryRetire(message.SurfaceToken, out session)) return false;
        Closed?.Invoke(message.SurfaceToken);
        Changed?.Invoke();
        return true;
    }

    // Native frame destruction/session drain can precede the trusted renderer's
    // unmount notification. A known retired close only acknowledges that cleanup.
    internal bool TryAcknowledgeRetiredClose(PluginSurfaceEvent message)
    {
        if (message is null || message.Contract != PluginContractVersions.Surface
            || message.Event != PluginSurfaceEvents.Close
            || string.IsNullOrWhiteSpace(message.SurfaceToken)) return false;
        lock (_gate) return _retiredTokens.Contains(message.SurfaceToken);
    }

    public PluginSurfaceHostMessage UpdateTheme(
        string surfaceToken,
        PluginSurfaceThemeSnapshot theme)
    {
        if (!_sessions.ContainsKey(surfaceToken))
        {
            throw Policy("PLUGIN_SURFACE_TOKEN_INVALID", "Surface token is not active.");
        }
        ValidateTheme(theme);
        return new PluginSurfaceHostMessage(
            PluginContractVersions.Surface,
            surfaceToken,
            PluginSurfaceMessages.ThemeChanged,
            theme);
    }

    public bool Close(string surfaceToken)
    {
        if (!TryRetire(surfaceToken, out _)) return false;
        Closed?.Invoke(surfaceToken);
        Changed?.Invoke();
        return true;
    }

    private bool TryRetire(string surfaceToken, out PluginSurfaceSession? session)
    {
        session = null;
        lock (_gate)
        {
            if (string.IsNullOrWhiteSpace(surfaceToken) || !_sessions.TryRemove(surfaceToken, out session)) return false;
            _mountedTokens.Remove(surfaceToken);
            _retiredTokens.Add(surfaceToken);
            _retiredOrder.Enqueue(surfaceToken);
            while (_retiredOrder.Count > RetiredCloseLimit)
                _retiredTokens.Remove(_retiredOrder.Dequeue());
            return true;
        }
    }

    // Synchronous post only. Close/Bind serialize here; callbacks run after Close releases this lock.
    internal bool TryUseCurrent(PluginSurfaceSession expected, Func<bool> action)
    {
        lock (_gate)
            return _sessions.TryGetValue(expected.SurfaceToken, out var current) && current == expected && action();
    }

    public bool IsActive(string surfaceToken)
        => !string.IsNullOrWhiteSpace(surfaceToken)
            && _sessions.ContainsKey(surfaceToken);

    public bool TryGet(string surfaceToken, out PluginSurfaceSession? session)
    {
        session = null;
        return !string.IsNullOrWhiteSpace(surfaceToken)
            && _sessions.TryGetValue(surfaceToken, out session);
    }

    public int CloseForInstallation(string projectKey, string pluginId)
    {
        int removed = 0;
        foreach (var pair in _sessions)
        {
            if (string.Equals(pair.Value.ProjectKey, projectKey, StringComparison.Ordinal)
                && string.Equals(pair.Value.PluginId, pluginId, StringComparison.Ordinal)
                && Close(pair.Key))
            {
                removed++;
            }
        }
        return removed;
    }

    public void CloseAll()
    {
        foreach (string token in _sessions.Keys) Close(token);
    }

    internal void BindIdentity(string token, PluginProjectContext context, int revision, string apiVersion, string pluginVersion = "", string actionId = "", string risk = "", string entry = "", string presentation = "surface", string? installationId = null)
    {
        lock (_gate)
            if (_sessions.TryGetValue(token, out var session))
                _sessions[token] = session with { Context = context, InstallationRevision = revision, InstallationId = installationId, ApiVersion = apiVersion, PluginVersion = pluginVersion, ActionId = actionId, Risk = risk, Entry = entry, Presentation = presentation };
    }

    internal void BindActiveCollection(string projectKey, string pluginId, string actionId, PluginRuntimeCommandContext context)
    {
        lock (_gate)
            foreach (var pair in _sessions)
                if (pair.Value.ProjectKey == projectKey && pair.Value.PluginId == pluginId && pair.Value.ActionId == actionId)
                    _sessions[pair.Key] = pair.Value with { ActiveCollection = context.Collection ?? "", InvocationContext = context };
    }

    private static void ValidateTheme(PluginSurfaceThemeSnapshot theme)
    {
        if (theme is null
            || !string.Equals(theme.Contract, PluginContractVersions.Theme, StringComparison.Ordinal)
            || theme.Mode is not (PluginThemeModes.Light or PluginThemeModes.Dark)
            || theme.Locale is not ("zh-CN" or "en-US")
            || theme.Density is not (PluginDensityModes.Comfortable or PluginDensityModes.Compact)
            || theme.Variables is null
            || theme.Variables.Count != ThemeVariables.Count
            || theme.Variables.Keys.Any(key => !ThemeVariables.Contains(key))
            || ThemeVariables.Any(key => !theme.Variables.TryGetValue(key, out string? value)
                || string.IsNullOrWhiteSpace(value)))
        {
            throw Policy("PLUGIN_THEME_INVALID", "Plugin theme contract is invalid.");
        }
    }

    private static PluginSurfacePolicyException Policy(string code, string message)
        => new(code, message);
}
