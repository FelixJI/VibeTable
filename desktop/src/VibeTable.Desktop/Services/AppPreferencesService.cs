using System;
using System.IO;
using System.Security;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed record AppPreferences(
    bool MinimizeToTrayOnClose,
    bool StartWithWindows,
    string UpdateProxy = UpdateProxyOptions.Direct,
    string? CustomUpdateProxyUrl = null)
{
    public static AppPreferences Default { get; } = new(false, false);
}

internal sealed record AppPreferencesPatch(
    bool? MinimizeToTrayOnClose,
    bool? StartWithWindows,
    string? UpdateProxy = null,
    string? CustomUpdateProxyUrl = null,
    bool HasCustomUpdateProxyUrl = false);

internal sealed record PersistedAppPreferences(
    bool MinimizeToTrayOnClose,
    string UpdateProxy,
    string? CustomUpdateProxyUrl)
{
    public static PersistedAppPreferences Default { get; } = new(
        false,
        UpdateProxyOptions.Direct,
        null);
}

internal static class UpdateProxyOptions
{
    public const string Direct = "direct";
    public const string GhProxyNet = "ghproxyNet";
    public const string GhProxyCom = "ghProxyCom";
    public const string Custom = "custom";

    public static bool IsKnown(string value) => value is
        Direct or GhProxyNet or GhProxyCom or Custom;

    public static string? NormalizeCustomUrl(string? value)
    {
        string? trimmed = value?.Trim();
        return string.IsNullOrEmpty(trimmed) ? null : trimmed;
    }
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
            string proxy = UpdateProxyOptions.IsKnown(stored.UpdateProxy ?? "")
                ? stored.UpdateProxy!
                : UpdateProxyOptions.Direct;
            return new PersistedAppPreferences(
                stored.MinimizeToTrayOnClose,
                proxy,
                UpdateProxyOptions.NormalizeCustomUrl(stored.CustomUpdateProxyUrl));
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
                        preferences.CustomUpdateProxyUrl),
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
        string? CustomUpdateProxyUrl = null);
}

internal sealed class AppPreferencesService
{
    private readonly IAppPreferencesStore _store;
    private readonly IStartupRegistration _startupRegistration;

    public AppPreferencesService(
        IAppPreferencesStore store,
        IStartupRegistration startupRegistration)
    {
        _store = store;
        _startupRegistration = startupRegistration;
    }

    public AppPreferences Read()
    {
        PersistedAppPreferences stored = _store.Read();
        return new AppPreferences(
            stored.MinimizeToTrayOnClose,
            _startupRegistration.IsEnabled(),
            stored.UpdateProxy,
            stored.CustomUpdateProxyUrl);
    }

    public AppPreferences ReadForStartup()
    {
        bool minimizeToTrayOnClose = false;
        bool startWithWindows = false;
        string updateProxy = UpdateProxyOptions.Direct;
        string? customUpdateProxyUrl = null;
        try
        {
            PersistedAppPreferences stored = _store.Read();
            minimizeToTrayOnClose = stored.MinimizeToTrayOnClose;
            updateProxy = stored.UpdateProxy;
            customUpdateProxyUrl = stored.CustomUpdateProxyUrl;
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
            customUpdateProxyUrl);
    }

    public AppPreferences Update(AppPreferencesPatch patch)
    {
        AppPreferences current = Read();
        AppPreferences updated = new(
            patch.MinimizeToTrayOnClose ?? current.MinimizeToTrayOnClose,
            patch.StartWithWindows ?? current.StartWithWindows,
            patch.UpdateProxy ?? current.UpdateProxy,
            patch.HasCustomUpdateProxyUrl
                ? UpdateProxyOptions.NormalizeCustomUrl(patch.CustomUpdateProxyUrl)
                : current.CustomUpdateProxyUrl);
        if (!UpdateProxyOptions.IsKnown(updated.UpdateProxy))
        {
            throw new ArgumentException("Unknown update proxy.", nameof(patch));
        }
        bool startupChanged = updated.StartWithWindows != current.StartWithWindows;
        bool storedPreferencesChanged =
            updated.MinimizeToTrayOnClose != current.MinimizeToTrayOnClose
            || updated.UpdateProxy != current.UpdateProxy
            || updated.CustomUpdateProxyUrl != current.CustomUpdateProxyUrl;

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
                    updated.CustomUpdateProxyUrl));
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
