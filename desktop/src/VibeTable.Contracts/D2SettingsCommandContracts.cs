using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// D2 settings/commands/shortcuts contracts. Mirrors
/// <c>backend.contracts.settings_commands</c>.
/// </summary>

// --- Settings ---

public static class ThemeModes
{
    public const string Light = "light";
    public const string Dark = "dark";
    public const string System = "system";
}

public sealed record ThemeTokens(string Mode, string Accent, string Background, string Foreground);

public sealed record DeviceSettings(
    int SchemaVersion,
    ThemeTokens Theme,
    IReadOnlyDictionary<string, int> WindowPosition,
    IReadOnlyList<string> RecentCollections);

public sealed record SharedSettingsEntry(string Key, object? Value, string UpdatedOn);

public sealed record ReadSharedSettingsParams(string Collection, IReadOnlyList<string> Keys);

public sealed record SharedSettingsResult(IReadOnlyList<SharedSettingsEntry> Settings, string CachedOn, bool Fresh);

public sealed record SaveDeviceSettingsParams(DeviceSettings Settings);

// --- Commands ---

public static class CommandRisks
{
    public const string None = "none";
    public const string Elevated = "elevated";
    public const string Destructive = "destructive";
}

public sealed record LocalCommandCatalogEntry(
    string CommandId, string Version, IReadOnlyDictionary<string, object?> ParamSchema,
    bool RequiresGrant, bool Cancellable, string Risk, string Description);

public sealed record CommandsResult(IReadOnlyList<LocalCommandCatalogEntry> Commands);

public sealed record RunCommandParams(string CommandId, IReadOnlyDictionary<string, object?> Params, string? GrantId);

public sealed record CommandResult(string CommandId, bool Success, IReadOnlyDictionary<string, object?> Output, string? Error);

// --- Shortcuts ---

public static class ShortcutTargets
{
    public const string BuiltInCommand = "built-in-command";
    public const string Url = "url";
    public const string FileAction = "file-action";
}

public sealed record ShortcutEntry(
    string ShortcutId, string Target, string? CommandId, string? Url, string Label, string Accelerator);

public sealed record ShortcutsResult(IReadOnlyList<ShortcutEntry> Shortcuts);

public sealed record SaveShortcutParams(ShortcutEntry Shortcut);

public sealed record DeleteShortcutParams(string ShortcutId);

public sealed record LaunchActionParams(string ShortcutId);

public sealed record LaunchActionResult(string ShortcutId, bool Launched, string? BlockedReason);
