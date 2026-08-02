using System;
using System.IO;
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
        using RegistryKey? key = Registry.CurrentUser.OpenSubKey(
            RunKeyPath,
            writable: false);
        string? value = key?.GetValue(
            ValueName,
            defaultValue: null,
            RegistryValueOptions.DoNotExpandEnvironmentNames) as string;
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
