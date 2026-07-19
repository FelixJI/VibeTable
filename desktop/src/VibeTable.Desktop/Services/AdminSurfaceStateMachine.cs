using System;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Small host-owned state machine for the lazy Directus surface. Closing the
/// surface hides it without destroying the initialized WebView2 instance.
/// </summary>
public sealed class AdminSurfaceStateMachine
{
    public AdminSurfaceState State { get; private set; } = AdminSurfaceState.Hidden;

    public bool IsInitialized { get; private set; }

    /// <summary>
    /// True only after the Directus page has completed navigation successfully.
    /// An initialized WebView is not necessarily reusable (for example, login or
    /// navigation may have failed after CoreWebView2 was created).
    /// </summary>
    public bool HasReadyPage { get; private set; }

    public bool IsVisible => State != AdminSurfaceState.Hidden;

    public string? LastError { get; private set; }

    /// <returns>True when the caller must initialize WebView2.</returns>
    public bool BeginOpen()
    {
        LastError = null;
        State = HasReadyPage ? AdminSurfaceState.Ready : AdminSurfaceState.Initializing;
        return !IsInitialized;
    }

    public void MarkReady()
    {
        MarkInitialized();
        HasReadyPage = true;
        LastError = null;
        State = AdminSurfaceState.Ready;
    }

    public void MarkInitialized()
        => IsInitialized = true;

    public void MarkFailed(string message)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(message);
        HasReadyPage = false;
        LastError = message;
        State = AdminSurfaceState.Failed;
    }

    public void Close()
    {
        LastError = null;
        State = AdminSurfaceState.Hidden;
    }

    public void Release()
    {
        LastError = null;
        IsInitialized = false;
        HasReadyPage = false;
        State = AdminSurfaceState.Hidden;
    }
}

public enum AdminSurfaceState
{
    Hidden,
    Initializing,
    Ready,
    Failed,
}
