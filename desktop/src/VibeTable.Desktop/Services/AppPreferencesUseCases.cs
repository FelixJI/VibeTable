using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

internal interface IAppPreferencesEffects
{
    void EnsureTrayIcon();
    void SetPreferredTheme(string? theme);
    void ApplyPreferences(AppPreferences preferences);
    HostThemeProjection Projection { get; }
}

/// <summary>Typed preference operations and their single current-state owner.</summary>
internal sealed class AppPreferencesUseCases
{
    private readonly AppPreferencesService _service;
    private readonly IAppPreferencesEffects _effects;

    public AppPreferencesUseCases(
        AppPreferencesService service,
        IAppPreferencesEffects effects,
        AppPreferences initialPreferences)
    {
        _service = service ?? throw new ArgumentNullException(nameof(service));
        _effects = effects ?? throw new ArgumentNullException(nameof(effects));
        CurrentPreferences = initialPreferences
            ?? throw new ArgumentNullException(nameof(initialPreferences));
    }

    public AppPreferences CurrentPreferences { get; private set; }

    public AppPreferences RefreshCurrent() => CurrentPreferences = _service.Read();

    public AppSettingsPreferences Get()
    {
        RefreshCurrent();
        _effects.ApplyPreferences(CurrentPreferences);
        return Project();
    }

    public AppSettingsPreferences Update(AppSettingsPreferencesPatch patch)
    {
        ArgumentNullException.ThrowIfNull(patch);
        if (patch.MinimizeToTrayOnClose is true)
            _effects.EnsureTrayIcon();
        CurrentPreferences = patch.HasAdoptLegacyTheme
            ? _service.AdoptLegacyTheme(patch.AdoptLegacyTheme!)
            : _service.Update(patch);
        _effects.SetPreferredTheme(CurrentPreferences.Theme);
        _effects.ApplyPreferences(CurrentPreferences);
        return Project();
    }

    private AppSettingsPreferences Project()
    {
        HostThemeProjection appearance = _effects.Projection;
        return new AppSettingsPreferences(
            CurrentPreferences.MinimizeToTrayOnClose,
            CurrentPreferences.StartWithWindows,
            CurrentPreferences.UpdateProxy,
            CurrentPreferences.CustomUpdateProxyUrl ?? "",
            CurrentPreferences.Theme,
            appearance.Effective,
            appearance.HighContrast);
    }
}

internal sealed class AppPreferencesEffects(
    HostAppearanceService appearance,
    Action<AppPreferences> applyPreferences,
    Action ensureTrayIcon) : IAppPreferencesEffects
{
    public void EnsureTrayIcon() => ensureTrayIcon();
    public void SetPreferredTheme(string? theme) => appearance.SetPreferredTheme(theme);
    public void ApplyPreferences(AppPreferences preferences) => applyPreferences(preferences);
    public HostThemeProjection Projection => appearance.Projection;
}
