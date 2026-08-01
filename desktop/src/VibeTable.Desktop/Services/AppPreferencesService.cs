using System;
using System.IO;
using System.Security;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed record AppPreferences(
    bool MinimizeToTrayOnClose,
    bool StartWithWindows)
{
    public static AppPreferences Default { get; } = new(false, false);
}

internal sealed record AppPreferencesPatch(
    bool? MinimizeToTrayOnClose,
    bool? StartWithWindows);

internal interface IAppPreferencesStore
{
    bool ReadMinimizeToTrayOnClose();
    void WriteMinimizeToTrayOnClose(bool value);
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

    public bool ReadMinimizeToTrayOnClose()
    {
        try
        {
            using FileStream stream = File.OpenRead(_path);
            StoredAppPreferences? stored = JsonSerializer.Deserialize<StoredAppPreferences>(
                stream,
                SerializerOptions);
            return stored?.MinimizeToTrayOnClose ?? false;
        }
        catch (FileNotFoundException)
        {
            return false;
        }
        catch (DirectoryNotFoundException)
        {
            return false;
        }
        catch (JsonException)
        {
            return false;
        }
    }

    public void WriteMinimizeToTrayOnClose(bool value)
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
                    new StoredAppPreferences(value),
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

    private sealed record StoredAppPreferences(bool MinimizeToTrayOnClose);
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

    public AppPreferences Read() => new(
        _store.ReadMinimizeToTrayOnClose(),
        _startupRegistration.IsEnabled());

    public AppPreferences ReadForStartup()
    {
        bool minimizeToTrayOnClose = false;
        bool startWithWindows = false;
        try
        {
            minimizeToTrayOnClose = _store.ReadMinimizeToTrayOnClose();
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
        return new AppPreferences(minimizeToTrayOnClose, startWithWindows);
    }

    public AppPreferences Update(AppPreferencesPatch patch)
    {
        AppPreferences current = Read();
        AppPreferences updated = new(
            patch.MinimizeToTrayOnClose ?? current.MinimizeToTrayOnClose,
            patch.StartWithWindows ?? current.StartWithWindows);
        bool startupChanged = updated.StartWithWindows != current.StartWithWindows;

        if (startupChanged)
        {
            _startupRegistration.SetEnabled(updated.StartWithWindows);
        }

        try
        {
            if (updated.MinimizeToTrayOnClose != current.MinimizeToTrayOnClose)
            {
                _store.WriteMinimizeToTrayOnClose(updated.MinimizeToTrayOnClose);
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
