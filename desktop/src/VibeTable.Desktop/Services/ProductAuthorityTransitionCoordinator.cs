using System.Threading;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Changes the database-open authority before the plugin-plan authority. This
/// fixed ordering makes invalidation of renderer open leases the first
/// linearization point for every workspace or gateway transition.
/// </summary>
internal sealed class ProductAuthorityTransitionCoordinator(
    ProductAuthorityEpoch authority,
    Func<PluginProjectContext?, CancellationToken, IReadOnlyList<string>> retireDatabaseOpens,
    Action<PluginProjectContext?> transitionPluginPlans,
    Action<IReadOnlyList<string>> postDatabaseOpenCancellations) : IDisposable
{
    private readonly object _gate = new();

    public void Transition(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
    {
        IReadOnlyList<string> retiredOpenIds;
        lock (_gate)
        {
            authority.Transition(context, sessionToken);
            retiredOpenIds = retireDatabaseOpens(context, sessionToken);
            transitionPluginPlans(context);
        }
        try
        {
            postDatabaseOpenCancellations(retiredOpenIds);
        }
        catch
        {
            // Renderer transport is outside the authority boundary. A broken
            // sink cannot roll back either ownership transfer or fail the
            // workspace/session transition.
        }
    }

    public void Dispose() => authority.Dispose();
}
