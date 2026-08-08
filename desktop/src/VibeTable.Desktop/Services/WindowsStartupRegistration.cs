using System;
using System.IO;
using System.Security;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

internal interface IStartupRegistration
{
    bool IsEnabled();
    void SetEnabled(bool enabled);
}

internal sealed class WindowsStartupRegistration : IStartupRegistration
{
    internal const string RunKeyPath = @"Software\Microsoft\Windows\CurrentVersion\Run";
    internal const string ValueName = "VibeTable.Next";

    private readonly string _command;

    public WindowsStartupRegistration(string executablePath)
    {
        _command = BuildCommand(executablePath);
    }

    public static WindowsStartupRegistration ForCurrentProcess() => new(
        Environment.ProcessPath
        ?? throw new InvalidOperationException("The current executable path is unavailable."));

    public bool IsEnabled()
    {
        string? value = ReadRawRunValue();
        return string.Equals(
            value?.Trim(),
            _command,
            StringComparison.OrdinalIgnoreCase);
    }

    public void SetEnabled(bool enabled)
    {
        if (enabled)
        {
            using RegistryKey key = Registry.CurrentUser.CreateSubKey(
                RunKeyPath,
                writable: true)
                ?? throw new InvalidOperationException(
                    "The current-user startup registry key is unavailable.");
            key.SetValue(ValueName, _command, RegistryValueKind.String);
            return;
        }

        using RegistryKey? existing = Registry.CurrentUser.OpenSubKey(
            RunKeyPath,
            writable: true);
        existing?.DeleteValue(ValueName, throwOnMissingValue: false);
    }

    /// <summary>
    /// Removes the startup value when it no longer points at the running
    /// process (for example after the executable was moved or the product was
    /// reinstalled elsewhere). Best-effort: registry permission or transient
    /// I/O failures are swallowed so a dead value can never block startup.
    /// </summary>
    public void ReconcileForCurrentProcess()
    {
        try
        {
            if (!IsStaleRunValue(_command, ReadRawRunValue()))
            {
                return;
            }
            using RegistryKey? existing = Registry.CurrentUser.OpenSubKey(
                RunKeyPath,
                writable: true);
            existing?.DeleteValue(ValueName, throwOnMissingValue: false);
        }
        catch (Exception exception) when (IsNonCriticalRegistryFailure(exception))
        {
            // A retained stale value is harmless to Windows and does not affect
            // the running process; the next successful reconcile will clear it.
        }
    }

    /// <summary>
    /// True when a stored startup value exists but does not match the current
    /// process command. Null/empty/whitespace values are treated as "not
    /// stale" (they represent "no startup configured", not a dead pointer).
    /// Case-insensitive to match <see cref="IsEnabled"/> semantics.
    /// </summary>
    internal static bool IsStaleRunValue(string currentCommand, string? storedValue)
    {
        if (string.IsNullOrWhiteSpace(storedValue))
        {
            return false;
        }
        return !string.Equals(
            storedValue.Trim(),
            currentCommand,
            StringComparison.OrdinalIgnoreCase);
    }

    private string? ReadRawRunValue()
    {
        using RegistryKey? key = Registry.CurrentUser.OpenSubKey(
            RunKeyPath,
            writable: false);
        return key?.GetValue(
            ValueName,
            defaultValue: null,
            RegistryValueOptions.DoNotExpandEnvironmentNames) as string;
    }

    private static bool IsNonCriticalRegistryFailure(Exception exception) =>
        exception is IOException
            or UnauthorizedAccessException
            or SecurityException
            or InvalidOperationException;

    internal static string BuildCommand(string executablePath)
    {
        string fullPath = Path.GetFullPath(executablePath);
        if (fullPath.Contains('"'))
        {
            throw new ArgumentException(
                "The executable path cannot contain a quotation mark.",
                nameof(executablePath));
        }
        return $"\"{fullPath}\" --autostart";
    }
}

internal sealed class InMemoryStartupRegistration : IStartupRegistration
{
    private bool _enabled;

    public bool IsEnabled() => _enabled;

    public void SetEnabled(bool enabled)
    {
        _enabled = enabled;
    }
}
