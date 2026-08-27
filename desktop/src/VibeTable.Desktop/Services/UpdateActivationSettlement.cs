namespace VibeTable.Desktop.Services;

internal interface IUpdateActivationSettlement
{
    Task CompleteHealthCheckAsync(
        UpdateActivationHealth health,
        CancellationToken cancellationToken);
}

internal abstract record UpdateActivationHealth
{
    internal sealed record Healthy(
        UpdateWorkspaceHealthProbeReceipt Receipt) : UpdateActivationHealth;

    internal sealed record Failed(
        UpdateActivationFailureCode Code) : UpdateActivationHealth;
}

internal enum UpdateActivationFailureCode
{
    WorkspaceHealthProbeFailed,
    UpdatedProcessExited,
    HealthTimeout,
}

internal interface IUpdateHostLifetimePort
{
    void RequestExit(int exitCode);
}

internal enum UpdateActivationStartupDisposition
{
    Proceed,
    Blocked,
}

internal sealed record UpdateActivationStartupResolution(
    UpdateActivationStartupDisposition Disposition,
    IUpdateActivationSettlement? Settlement,
    string? ErrorCode = null);
