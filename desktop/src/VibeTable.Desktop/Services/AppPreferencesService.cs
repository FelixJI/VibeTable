using System;
using System.IO;
using System.Security;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal sealed record AppPreferences(
    bool MinimizeToTrayOnClose,
    bool StartWithWindows,
    string UpdateProxy = AppSettingsUpdateProxyOption.Direct,
    string? CustomUpdateProxyUrl = null,
    string? Theme = null,
    bool PortableMode = false)
{
    public static AppPreferences Default { get; } = new(false, false);
}

internal sealed record PersistedAppPreferences(
    bool MinimizeToTrayOnClose,
    string UpdateProxy,
    string? CustomUpdateProxyUrl,
    string? Theme = null)
{
    public static PersistedAppPreferences Default { get; } = new(
        false,
        AppSettingsUpdateProxyOption.Direct,
        null);
}

internal interface IAppPreferencesStore
{
    PersistedAppPreferences Read();
    void Write(PersistedAppPreferences preferences);
}

internal sealed class JsonAppPreferencesStore : IAppPreferencesStore
{
    private static readonly JsonSerializerOptions SerializerOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true,
    };

    private readonly string _path;

    public JsonAppPreferencesStore(string path)
    {
        _path = Path.GetFullPath(path);
    }

    public PersistedAppPreferences Read()
    {
        try
        {
            using FileStream stream = File.OpenRead(_path);
            StoredAppPreferences? stored = JsonSerializer.Deserialize<StoredAppPreferences>(
                stream,
                SerializerOptions);
            if (stored is null)
            {
                return PersistedAppPreferences.Default;
            }
            string proxy = AppSettingsUpdateProxyOption.IsKnown(stored.UpdateProxy ?? "")
                ? stored.UpdateProxy!
                : AppSettingsUpdateProxyOption.Direct;
            // An unknown or corrupt theme falls back to "not configured"
            // (null) instead of blocking startup; every other preference
            // keeps its stored value under the same safe-load rule.
            string? theme = AppSettingsThemeOption.IsKnown(stored.Theme) ? stored.Theme : null;
            return new PersistedAppPreferences(
                stored.MinimizeToTrayOnClose,
                proxy,
                AppPreferencesService.NormalizeCustomUrl(stored.CustomUpdateProxyUrl),
                theme);
        }
        catch (FileNotFoundException)
        {
            return PersistedAppPreferences.Default;
        }
        catch (DirectoryNotFoundException)
        {
            return PersistedAppPreferences.Default;
        }
        catch (JsonException)
        {
            return PersistedAppPreferences.Default;
        }
    }

    public void Write(PersistedAppPreferences preferences)
    {
        string? directory = Path.GetDirectoryName(_path);
        if (string.IsNullOrWhiteSpace(directory))
        {
            throw new InvalidOperationException(
                "The application preferences path has no parent directory.");
        }

        Directory.CreateDirectory(directory);
        string temporaryPath = _path + ".tmp-" + Guid.NewGuid().ToString("N");
        try
        {
            using (FileStream stream = new(
                temporaryPath,
                FileMode.CreateNew,
                FileAccess.Write,
                FileShare.None))
            {
                JsonSerializer.Serialize(
                    stream,
                    new StoredAppPreferences(
                        preferences.MinimizeToTrayOnClose,
                        preferences.UpdateProxy,
                        preferences.CustomUpdateProxyUrl,
                        preferences.Theme),
                    SerializerOptions);
                stream.Flush(flushToDisk: true);
            }
            File.Move(temporaryPath, _path, overwrite: true);
        }
        finally
        {
            try
            {
                File.Delete(temporaryPath);
            }
            catch (IOException)
            {
                // The committed destination is authoritative. A retained temp
                // file is harmless and can be removed by normal workspace cleanup.
            }
        }
    }

    private sealed record StoredAppPreferences(
        bool MinimizeToTrayOnClose,
        string? UpdateProxy = null,
        string? CustomUpdateProxyUrl = null,
        string? Theme = null);
}

internal sealed class AppPreferencesService
{
    private readonly IAppPreferencesStore _store;
    private readonly IStartupRegistration _startupRegistration;
    private readonly IPortableModeStore _portableModeStore;

    public AppPreferencesService(
        IAppPreferencesStore store,
        IStartupRegistration startupRegistration,
        IPortableModeStore portableModeStore)
    {
        _store = store;
        _startupRegistration = startupRegistration;
        _portableModeStore = portableModeStore;
    }

    public AppPreferences Read()
    {
        PersistedAppPreferences stored = _store.Read();
        return new AppPreferences(
            stored.MinimizeToTrayOnClose,
            _startupRegistration.IsEnabled(),
            stored.UpdateProxy,
            stored.CustomUpdateProxyUrl,
            stored.Theme,
            _portableModeStore.IsEnabled());
    }

    public AppPreferences ReadForStartup()
    {
        bool minimizeToTrayOnClose = false;
        bool startWithWindows = false;
        string updateProxy = AppSettingsUpdateProxyOption.Direct;
        string? customUpdateProxyUrl = null;
        string? theme = null;
        try
        {
            PersistedAppPreferences stored = _store.Read();
            minimizeToTrayOnClose = stored.MinimizeToTrayOnClose;
            updateProxy = stored.UpdateProxy;
            customUpdateProxyUrl = stored.CustomUpdateProxyUrl;
            theme = stored.Theme;
        }
        catch (Exception exception) when (IsNonCriticalReadFailure(exception))
        {
            // Preferences are optional; an ACL or transient I/O problem must
            // not prevent the local-first workspace from starting.
        }
        try
        {
            startWithWindows = _startupRegistration.IsEnabled();
        }
        catch (Exception exception) when (IsNonCriticalReadFailure(exception))
        {
            // The settings page still uses strict Read/Update and will surface
            // a permission failure. Startup itself remains available.
        }
        return new AppPreferences(
            minimizeToTrayOnClose,
            startWithWindows,
            updateProxy,
            customUpdateProxyUrl,
            theme,
            _portableModeStore.IsEnabled());
    }

    public AppPreferences Update(AppSettingsPreferencesPatch patch)
    {
        AppPreferences current = Read();
        bool portableMode = patch.PortableMode ?? current.PortableMode;
        AppPreferences updated = new(
            patch.MinimizeToTrayOnClose ?? current.MinimizeToTrayOnClose,
            patch.StartWithWindows ?? current.StartWithWindows,
            patch.UpdateProxy ?? current.UpdateProxy,
            patch.HasCustomUpdateProxyUrl
                ? NormalizeCustomUrl(patch.CustomUpdateProxyUrl)
                : current.CustomUpdateProxyUrl,
            patch.HasTheme && AppSettingsThemeOption.IsKnown(patch.Theme)
                ? patch.Theme
                : current.Theme,
            portableMode);
        if (!AppSettingsUpdateProxyOption.IsKnown(updated.UpdateProxy))
        {
            throw new ArgumentException("Unknown update proxy.", nameof(patch));
        }
        if (patch.HasTheme && !AppSettingsThemeOption.IsKnown(patch.Theme))
        {
            throw new ArgumentException("Unknown theme.", nameof(patch));
        }
        bool startupChanged = updated.StartWithWindows != current.StartWithWindows;
        bool portableModeChanged = portableMode != current.PortableMode;
        bool storedPreferencesChanged =
            updated.MinimizeToTrayOnClose != current.MinimizeToTrayOnClose
            || updated.UpdateProxy != current.UpdateProxy
            || updated.CustomUpdateProxyUrl != current.CustomUpdateProxyUrl
            || updated.Theme != current.Theme;

        if (portableModeChanged)
        {
            // Arms the migration and flips the marker. The switch itself only
            // takes effect after restart; a later failure in this update
            // cannot un-arm it, which is safe (a pending switch that never
            // applies is discarded on next launch).
            _portableModeStore.SetEnabled(portableMode);
        }

        if (startupChanged)
        {
            _startupRegistration.SetEnabled(updated.StartWithWindows);
        }

        try
        {
            if (storedPreferencesChanged)
            {
                _store.Write(new PersistedAppPreferences(
                    updated.MinimizeToTrayOnClose,
                    updated.UpdateProxy,
                    updated.CustomUpdateProxyUrl,
                    updated.Theme));
            }
        }
        catch
        {
            if (startupChanged)
            {
                _startupRegistration.SetEnabled(current.StartWithWindows);
            }
            throw;
        }

        return updated;
    }

    /// <summary>
    /// One-time legacy migration: persists <paramref name="theme"/> only when
    /// the Host never persisted a theme (compare-unset). An already configured
    /// Host value — including an explicit "system" — always wins and is
    /// returned unchanged without writing. Dispatch is serialized on the UI
    /// thread, so the read-modify-write is atomic against other updates.
    /// </summary>
    public AppPreferences AdoptLegacyTheme(string theme)
    {
        if (!AppSettingsThemeOption.IsKnown(theme))
        {
            throw new ArgumentException("Unknown theme.", nameof(theme));
        }
        AppPreferences current = Read();
        if (current.Theme is not null)
        {
            return current;
        }
        AppPreferences updated = current with { Theme = theme };
        _store.Write(new PersistedAppPreferences(
            updated.MinimizeToTrayOnClose,
            updated.UpdateProxy,
            updated.CustomUpdateProxyUrl,
            updated.Theme));
        return updated;
    }

    /// <summary>Trims a custom proxy URL; an empty value normalizes to null
    /// (business rule kept beside the service that owns persistence).</summary>
    internal static string? NormalizeCustomUrl(string? value)
    {
        string? trimmed = value?.Trim();
        return string.IsNullOrEmpty(trimmed) ? null : trimmed;
    }

    private static bool IsNonCriticalReadFailure(Exception exception) =>
        exception is IOException
            or UnauthorizedAccessException
            or SecurityException
            or InvalidOperationException;
}

internal static class WindowClosePolicy
{
    public static bool ShouldMinimizeToTray(
        AppPreferences preferences,
        bool explicitExitRequested) =>
        preferences.MinimizeToTrayOnClose && !explicitExitRequested;
}

/// <summary>
/// Decides whether the main window should be hidden into the tray on launch.
/// Only an auto-started launch (<c>--autostart</c>) of a user who has opted
/// into tray behavior hides the window; the tray icon then becomes the sole
/// entry point until the user restores the window. Manual launches and users
/// without the tray preference always see the window as before.
/// </summary>
internal static class StartupVisibilityPolicy
{
    public static bool ShouldStartHidden(
        bool autoStart,
        bool minimizeToTrayOnClose) =>
        autoStart && minimizeToTrayOnClose;
}
