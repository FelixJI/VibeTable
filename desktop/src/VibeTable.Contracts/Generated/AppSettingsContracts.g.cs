// Generated from contracts/v2/app-settings-contract.json; do not edit.
#nullable enable

using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>Closed app-settings bridge message names shared by the router and
/// controllers. WebRequestTypes are admitted web->host requests;
/// HostNotificationTypes lists correlated responses plus the host->web push.</summary>
public static class AppSettingsWireMethods
{
    public const string AppPreferencesGet = "appPreferences.get";
    public const string AppPreferencesUpdate = "appPreferences.update";
    public const string AppearanceChanged = "appearance.changed";

    public static readonly string[] WebRequestTypes = [AppPreferencesGet, AppPreferencesUpdate];
    public static readonly string[] HostNotificationTypes = [AppPreferencesGet, AppPreferencesUpdate, AppearanceChanged];
}

/// <summary>Wire values of the themePreference enum family.</summary>
public static class AppSettingsThemeOption
{
    public const string Light = "light";
    public const string Dark = "dark";
    public const string System = "system";

    public static readonly string[] Values = [Light, Dark, System];

    public static bool IsKnown(string? value) => value is Light or Dark or System;
}

/// <summary>Wire values of the updateProxy enum family.</summary>
public static class AppSettingsUpdateProxyOption
{
    public const string Direct = "direct";
    public const string GhproxyNet = "ghproxyNet";
    public const string GhProxyCom = "ghProxyCom";
    public const string Custom = "custom";

    public static readonly string[] Values = [Direct, GhproxyNet, GhProxyCom, Custom];

    public static bool IsKnown(string? value) => value is Direct or GhproxyNet or GhProxyCom or Custom;
}

/// <summary>Host -> web preferences projection shared by get/update replies.</summary>
public sealed record AppSettingsPreferences(
    [property: JsonPropertyName("minimizeToTrayOnClose")]
    bool MinimizeToTrayOnClose,
    [property: JsonPropertyName("startWithWindows")]
    bool StartWithWindows,
    [property: JsonPropertyName("portableMode")]
    bool PortableMode,
    [property: JsonPropertyName("updateProxy")]
    string UpdateProxy,
    [property: JsonPropertyName("customUpdateProxyUrl")]
    string CustomUpdateProxyUrl,
    [property: JsonPropertyName("theme")]
    string? Theme,
    [property: JsonPropertyName("effectiveTheme")]
    string EffectiveTheme,
    [property: JsonPropertyName("highContrast")]
    bool HighContrast);

/// <summary>Host -> web appearance.changed payload.</summary>
public sealed record AppSettingsAppearanceChanged(
    [property: JsonPropertyName("preference")]
    string? Preference,
    [property: JsonPropertyName("effective")]
    string Effective,
    [property: JsonPropertyName("highContrast")]
    bool HighContrast);

/// <summary>Settings-family operation.failed payload; mirrors the host's
/// <summary>OperationFailedPayload wire shape (nulls serialize as null).</summary>
public sealed record AppSettingsOperationFailed(
    [property: JsonPropertyName("message")]
    string Message,
    [property: JsonPropertyName("code")]
    string? Code,
    [property: JsonPropertyName("operation")]
    string? Operation,
    [property: JsonPropertyName("operationId")]
    string? OperationId);

/// <summary>Parsed appPreferences.update patch. Has* flags distinguish an
/// explicitly provided string field from an absent one.</summary>
public sealed record AppSettingsPreferencesPatch(
    bool? MinimizeToTrayOnClose = null,
    bool? StartWithWindows = null,
    bool? PortableMode = null,
    string? UpdateProxy = null,
    string? CustomUpdateProxyUrl = null,
    bool HasCustomUpdateProxyUrl = false,
    string? Theme = null,
    bool HasTheme = false,
    string? AdoptLegacyTheme = null,
    bool HasAdoptLegacyTheme = false
);

public static class AppSettingsPatchReader
{
    public static bool TryRead(JsonElement payload, out AppSettingsPreferencesPatch? patch)
    {
        patch = null;
        if (payload.ValueKind != JsonValueKind.Object)
            return false;
        bool? minimizeToTrayOnClose = null;
        bool sawMinimizeToTrayOnClose = false;
        bool? startWithWindows = null;
        bool sawStartWithWindows = false;
        bool? portableMode = null;
        bool sawPortableMode = false;
        string? updateProxy = null;
        bool sawUpdateProxy = false;
        string? customUpdateProxyUrl = null;
        bool sawCustomUpdateProxyUrl = false;
        string? theme = null;
        bool sawTheme = false;
        string? adoptLegacyTheme = null;
        bool sawAdoptLegacyTheme = false;
        foreach (JsonProperty property in payload.EnumerateObject())
        {
            if (property.NameEquals("minimizeToTrayOnClose"))
            {
                if (sawMinimizeToTrayOnClose || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                    return false;
                sawMinimizeToTrayOnClose = true;
                minimizeToTrayOnClose = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("startWithWindows"))
            {
                if (sawStartWithWindows || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                    return false;
                sawStartWithWindows = true;
                startWithWindows = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("portableMode"))
            {
                if (sawPortableMode || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                    return false;
                sawPortableMode = true;
                portableMode = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("updateProxy"))
            {
                if (sawUpdateProxy || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawUpdateProxy = true;
                updateProxy = property.Value.GetString();
                if (updateProxy is null || !AppSettingsUpdateProxyOption.IsKnown(updateProxy))
                    return false;
                continue;
            }
            if (property.NameEquals("customUpdateProxyUrl"))
            {
                if (sawCustomUpdateProxyUrl || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawCustomUpdateProxyUrl = true;
                customUpdateProxyUrl = property.Value.GetString();
                if (customUpdateProxyUrl is null || customUpdateProxyUrl.Length > 2048)
                    return false;
                continue;
            }
            if (property.NameEquals("theme"))
            {
                if (sawTheme || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawTheme = true;
                theme = property.Value.GetString();
                if (theme is null || !AppSettingsThemeOption.IsKnown(theme))
                    return false;
                continue;
            }
            if (property.NameEquals("adoptLegacyTheme"))
            {
                if (sawAdoptLegacyTheme || property.Value.ValueKind != JsonValueKind.String)
                    return false;
                sawAdoptLegacyTheme = true;
                adoptLegacyTheme = property.Value.GetString();
                if (adoptLegacyTheme is null || !AppSettingsThemeOption.IsKnown(adoptLegacyTheme))
                    return false;
                continue;
            }
            return false;
        }
        if (!sawMinimizeToTrayOnClose && !sawStartWithWindows && !sawPortableMode && !sawUpdateProxy && !sawCustomUpdateProxyUrl && !sawTheme && !sawAdoptLegacyTheme)
            return false;
        if (sawTheme && sawAdoptLegacyTheme)
            return false;
        if (sawAdoptLegacyTheme && (sawMinimizeToTrayOnClose || sawStartWithWindows || sawPortableMode || sawUpdateProxy || sawCustomUpdateProxyUrl || sawTheme))
            return false;
        patch = new AppSettingsPreferencesPatch(
            minimizeToTrayOnClose, startWithWindows, portableMode, updateProxy, customUpdateProxyUrl, sawCustomUpdateProxyUrl, theme, sawTheme, adoptLegacyTheme, sawAdoptLegacyTheme);
        return true;
    }
}

/// <summary>Stable operation.failed codes for the app-settings family.</summary>
public static class AppSettingsErrorCodes
{
    public const string BadPayload = "APP_PREFERENCES_BAD_PAYLOAD";
    public const string ReadFailed = "APP_PREFERENCES_READ_FAILED";
    public const string WriteFailed = "APP_PREFERENCES_WRITE_FAILED";
    public const string PortableModeWriteFailed = "PORTABLE_MODE_WRITE_FAILED";
}
