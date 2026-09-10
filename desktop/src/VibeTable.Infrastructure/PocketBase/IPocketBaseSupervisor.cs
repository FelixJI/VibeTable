using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.PocketBase;

public interface IPocketBaseSupervisor : IAsyncDisposable
{
    event Action<object?, PocketBaseStatus>? StatusChanged;

    PocketBaseStartupTimings? LastStartupTimings { get; }
    Task StartAsync(CancellationToken cancellationToken);
    PocketBaseStatus GetStatus();
    Task StopAsync(CancellationToken cancellationToken);
    Uri? GetAdminUri();
    PocketBaseAdminContext? GetAdminContext();
    PocketBaseGenerationContext? CaptureCurrentGeneration();
    /// <summary>Terminal validation for prepared caller state; it grants no lease.</summary>
    bool IsCurrentGeneration(PocketBaseGenerationContext expected);
    void ConfigureBackendEnvironment(IDictionary<string, string> environment);
}

public sealed record PocketBaseAdminContext(
    Uri BootstrapUri,
    Uri Origin,
    string SessionHeaderName,
    string SessionSecret)
{
    public override string ToString()
        => $"{nameof(PocketBaseAdminContext)} {{ BootstrapUri = {BootstrapUri}, "
            + $"Origin = {Origin}, SessionHeaderName = {SessionHeaderName}, "
            + "SessionSecret = [REDACTED] }";
}
