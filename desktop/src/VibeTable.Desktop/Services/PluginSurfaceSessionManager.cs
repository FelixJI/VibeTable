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
    string ContentSecurityPolicy);

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

    private readonly ConcurrentDictionary<string, PluginSurfaceSession> _sessions =
        new(StringComparer.Ordinal);

    public PluginSurfaceSession Open(PluginPackageRevision revision, string entryPath)
        => Open(string.Empty, string.Empty, revision, entryPath);

    public PluginSurfaceSession Open(
        string projectKey,
        string pluginId,
        PluginPackageRevision revision,
        string entryPath)
    {
        ArgumentNullException.ThrowIfNull(revision);
        string normalized = PluginResourceHost.NormalizePackagePath(entryPath);
        string token = Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant();
        var session = new PluginSurfaceSession(
            token,
            projectKey,
            pluginId,
            revision,
            new Uri(revision.Origin, normalized),
            "allow-scripts allow-same-origin",
            PluginResourceHost.ContentSecurityPolicy);
        if (!_sessions.TryAdd(token, session))
        {
            throw new InvalidOperationException("Could not allocate a unique surface token.");
        }
        return session;
    }

    public bool TryAccept(PluginSurfaceEvent message, out PluginSurfaceSession? session)
    {
        session = null;
        if (message is null
            || !string.Equals(message.Contract, PluginContractVersions.Surface, StringComparison.Ordinal)
            || string.IsNullOrWhiteSpace(message.SurfaceToken)
            || !AllowedEvents.Contains(message.Event)
            || !_sessions.TryGetValue(message.SurfaceToken, out var active))
        {
            return false;
        }

        session = active;
        if (string.Equals(message.Event, PluginSurfaceEvents.Close, StringComparison.Ordinal))
        {
            _sessions.TryRemove(message.SurfaceToken, out _);
        }
        return true;
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
        => !string.IsNullOrWhiteSpace(surfaceToken)
            && _sessions.TryRemove(surfaceToken, out _);

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
                && _sessions.TryRemove(pair.Key, out _))
            {
                removed++;
            }
        }
        return removed;
    }

    public void CloseAll() => _sessions.Clear();

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
