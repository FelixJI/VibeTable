using System;
using System.IO;
using System.Security;
using System.Windows;
using System.Windows.Media;
using System.Windows.Threading;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

/// <summary>Observed system appearance; the Host is its only authority.</summary>
internal sealed record HostAppearanceSnapshot(bool SystemPrefersDark, bool HighContrast)
{
    public string EffectiveBaseTheme => SystemPrefersDark ? "dark" : "light";
}

/// <summary>Appearance projection handed to the Web layer. A null
/// Preference means "not configured" and is distinct from "system".</summary>
internal sealed record HostThemeProjection(string? Preference, string Effective, bool HighContrast);

/// <summary>Explicit light/dark wins; null or "system" follows the OS.</summary>
internal static class AppThemePolicy
{
    public static string ResolveEffectiveTheme(string? preferredTheme, HostAppearanceSnapshot system) =>
        preferredTheme is AppThemeOptions.Light or AppThemeOptions.Dark
            ? preferredTheme
            : system.EffectiveBaseTheme;
}

internal interface ISystemAppearanceSource : IDisposable
{
    HostAppearanceSnapshot Read();

    event EventHandler? Changed;
}

/// <summary>Windows source: personalization registry for dark/light,
/// <see cref="SystemParameters"/> for high contrast, WM_SETTINGCHANGE via
/// <see cref="SystemEvents"/> for change notifications.</summary>
internal sealed class WindowsSystemAppearanceSource : ISystemAppearanceSource
{
    private const string PersonalizeKeyPath =
        @"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize";
    private const string AppsUseLightThemeValue = "AppsUseLightTheme";

    public event EventHandler? Changed;

    public WindowsSystemAppearanceSource() =>
        SystemEvents.UserPreferenceChanged += OnUserPreferenceChanged;

    public HostAppearanceSnapshot Read() => new(
        SystemPrefersDark: !IsLightTheme(),
        HighContrast: SystemParameters.HighContrast);

    public void Dispose() => SystemEvents.UserPreferenceChanged -= OnUserPreferenceChanged;

    private void OnUserPreferenceChanged(object? sender, UserPreferenceChangedEventArgs e)
    {
        // Dark/light arrives as ImmersiveColorSet (General), high contrast as
        // Accessibility/Color; re-read on each keeps transitions reliable.
        if (e.Category is UserPreferenceCategory.General
            or UserPreferenceCategory.Color
            or UserPreferenceCategory.Accessibility
            or UserPreferenceCategory.VisualStyle)
        {
            Changed?.Invoke(this, EventArgs.Empty);
        }
    }

    private static bool IsLightTheme()
    {
        try
        {
            using RegistryKey? key = Registry.CurrentUser.OpenSubKey(
                PersonalizeKeyPath,
                writable: false);
            if (key?.GetValue(AppsUseLightThemeValue) is int value)
            {
                return value != 0;
            }
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or SecurityException)
        {
            // Unreadable personalization falls back to the classic light
            // scheme; appearance tracking never fails startup.
        }
        return true;
    }
}

/// <summary>
/// Single Host-side appearance authority: the persisted AppPreferences theme
/// combined with the observed system appearance, projected to the Web layer
/// and the Vt* resource dictionaries. System-change reconciliation runs on
/// the supplied dispatcher; <see cref="SetPreferredTheme"/> raises
/// synchronously on its caller.
/// </summary>
internal sealed class HostAppearanceService : IDisposable
{
    private readonly ISystemAppearanceSource _source;
    private readonly Dispatcher? _dispatcher;
    private HostAppearanceSnapshot _current;
    private string? _preferredTheme;

    public event Action<HostThemeProjection>? ProjectionChanged;

    public HostAppearanceService(
        ISystemAppearanceSource source,
        string? preferredTheme = null,
        Dispatcher? dispatcher = null)
    {
        _source = source ?? throw new ArgumentNullException(nameof(source));
        _dispatcher = dispatcher;
        _preferredTheme = AppThemeOptions.IsKnown(preferredTheme) ? preferredTheme : null;
        _current = source.Read();
        _source.Changed += OnSourceChanged;
    }

    public HostAppearanceSnapshot Current => _current;

    /// <summary>Persisted preference; null means not configured.</summary>
    public string? PreferredTheme => _preferredTheme;

    public string EffectiveTheme => AppThemePolicy.ResolveEffectiveTheme(_preferredTheme, _current);

    public HostThemeProjection Projection => new(_preferredTheme, EffectiveTheme, _current.HighContrast);

    public void SetPreferredTheme(string? theme)
    {
        string? normalized = AppThemeOptions.IsKnown(theme) ? theme : null;
        if (normalized == _preferredTheme)
        {
            return;
        }
        _preferredTheme = normalized;
        ProjectionChanged?.Invoke(Projection);
    }

    /// <summary>Re-reads the system appearance; raises the projection on change.</summary>
    public void Refresh() => OnSourceChanged(this, EventArgs.Empty);

    public void Dispose()
    {
        _source.Changed -= OnSourceChanged;
        _source.Dispose();
    }

    private void OnSourceChanged(object? sender, EventArgs args)
    {
        if (_dispatcher is { } dispatcher && !dispatcher.CheckAccess())
        {
            dispatcher.BeginInvoke(new Action(() => OnSourceChanged(sender, args)));
            return;
        }
        HostAppearanceSnapshot next = _source.Read();
        // While high contrast is active every notification re-projects (HC
        // palette edits do not move either axis, and the SystemColors
        // fallback re-reads fresh brushes on each apply); otherwise an
        // unchanged snapshot stays silent.
        if (next == _current && !next.HighContrast)
        {
            return;
        }
        _current = next;
        ProjectionChanged?.Invoke(Projection);
    }
}

/// <summary>Code-built, frozen theme dictionaries consumed through standard
/// WPF dynamic resources ({DynamicResource VtWindowBackgroundBrush} …) so
/// native surfaces match the Web effective mode. High contrast swaps in a
/// SystemColors-backed fallback so the system palette stays authoritative.
/// Values align with the Web design tokens.</summary>
internal static class HostThemeResources
{
    public const string WindowBackgroundBrushKey = "VtWindowBackgroundBrush";
    public const string WindowTextBrushKey = "VtWindowTextBrush";
    public const string SurfaceBackgroundBrushKey = "VtSurfaceBackgroundBrush";
    public const string SurfaceTextBrushKey = "VtSurfaceTextBrush";
    public const string SurfaceBorderBrushKey = "VtSurfaceBorderBrush";
    public const string MutedTextBrushKey = "VtMutedTextBrush";

    /// <summary>Marks the dictionary this class manages inside a target so
    /// repeated applies swap instead of stacking.</summary>
    internal const string ThemeMarkerKey = "__VibeTableThemeMarker";

    private const string Dark = AppThemeOptions.Dark;

    public static ResourceDictionary ForTheme(string effectiveTheme) =>
        effectiveTheme == Dark ? DarkTheme() : LightTheme();

    /// <summary>Swaps in the dictionary for the effective theme; high
    /// contrast uses the SystemColors fallback so every Vt* key keeps a
    /// value derived from the live system palette.</summary>
    public static void Apply(ResourceDictionary target, HostThemeProjection projection)
    {
        ArgumentNullException.ThrowIfNull(target);
        Remove(target);
        ResourceDictionary theme = projection.HighContrast
            ? HighContrastFallback()
            : ForTheme(projection.Effective);
        theme[ThemeMarkerKey] = projection.HighContrast ? "highContrast" : projection.Effective;
        target.MergedDictionaries.Insert(0, theme);
    }

    public static void Remove(ResourceDictionary target)
    {
        ArgumentNullException.ThrowIfNull(target);
        for (int index = target.MergedDictionaries.Count - 1; index >= 0; index--)
        {
            if (target.MergedDictionaries[index].Contains(ThemeMarkerKey))
            {
                target.MergedDictionaries.RemoveAt(index);
            }
        }
    }

    private static ResourceDictionary HighContrastFallback() => new()
    {
        [WindowBackgroundBrushKey] = System.Windows.SystemColors.WindowBrush,
        [WindowTextBrushKey] = System.Windows.SystemColors.WindowTextBrush,
        [SurfaceBackgroundBrushKey] = System.Windows.SystemColors.WindowBrush,
        [SurfaceTextBrushKey] = System.Windows.SystemColors.WindowTextBrush,
        [SurfaceBorderBrushKey] = System.Windows.SystemColors.ActiveBorderBrush,
        [MutedTextBrushKey] = System.Windows.SystemColors.GrayTextBrush,
    };

    private static ResourceDictionary LightTheme() => Build(
        windowBackground: "#FFFFFFFF",
        windowText: "#FF272E3B",
        surfaceBackground: "#FFFFFFFF",
        surfaceText: "#FF272E3B",
        surfaceBorder: "#FFE5E6EB",
        mutedText: "#FF646F7D");

    private static ResourceDictionary DarkTheme() => Build(
        windowBackground: "#FF17191F",
        windowText: "#FFE1E6ED",
        surfaceBackground: "#FF1E2128",
        surfaceText: "#FFE1E6ED",
        surfaceBorder: "#FF2A2E37",
        mutedText: "#FFA7AFBB");

    private static ResourceDictionary Build(
        string windowBackground,
        string windowText,
        string surfaceBackground,
        string surfaceText,
        string surfaceBorder,
        string mutedText) => new()
    {
        [WindowBackgroundBrushKey] = Frozen(windowBackground),
        [WindowTextBrushKey] = Frozen(windowText),
        [SurfaceBackgroundBrushKey] = Frozen(surfaceBackground),
        [SurfaceTextBrushKey] = Frozen(surfaceText),
        [SurfaceBorderBrushKey] = Frozen(surfaceBorder),
        [MutedTextBrushKey] = Frozen(mutedText),
    };

    private static SolidColorBrush Frozen(string color)
    {
        SolidColorBrush brush = new(
            (System.Windows.Media.Color)System.Windows.Media.ColorConverter.ConvertFromString(color));
        brush.Freeze();
        return brush;
    }
}
