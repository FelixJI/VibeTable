namespace VibeTable.Infrastructure.PocketBase;

internal sealed class PocketBaseGenerationContext
{
    private readonly object _admissionGate = new();
    private readonly TaskCompletionSource _retirementRequested = new(
        TaskCreationOptions.RunContinuationsAsynchronously);
    private bool _retired;

    internal PocketBaseGenerationContext(
        long generationId,
        PocketBaseAdminContext adminContext)
    {
        if (generationId <= 0)
            throw new ArgumentOutOfRangeException(nameof(generationId));
        GenerationId = generationId;
        AdminContext = adminContext
            ?? throw new ArgumentNullException(nameof(adminContext));
    }

    internal long GenerationId { get; }
    internal PocketBaseAdminContext AdminContext { get; }
    internal Task RetirementRequestedForTests => _retirementRequested.Task;

    /// <summary>
    /// Runs a short synchronous action only while this generation remains
    /// current. The action must not call back into its owning supervisor.
    /// </summary>
    internal bool TryUseCurrent(Func<bool> action)
    {
        ArgumentNullException.ThrowIfNull(action);
        lock (_admissionGate)
        {
            return !_retired && action();
        }
    }

    internal void Retire()
    {
        _retirementRequested.TrySetResult();
        lock (_admissionGate)
            _retired = true;
    }

    public override string ToString()
        => $"{nameof(PocketBaseGenerationContext)} " +
            $"{{ GenerationId = {GenerationId} }}";
}
