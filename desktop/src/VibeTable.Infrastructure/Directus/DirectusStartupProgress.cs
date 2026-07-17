namespace VibeTable.Infrastructure.Directus;

/// <summary>Fine-grained stages within the broad <see cref="DirectusState.Starting"/> state.</summary>
public enum DirectusStartupStage
{
    PreparingRuntime = 0,
    CheckingPackages = 1,
    InstallingPackages = 2,
    VerifyingPackages = 3,
    RepairingPackages = 4,
    /// <summary>
    /// A previous first-run attempt did not complete; the existing install on
    /// disk is being fully re-verified (structure + native modules + lock
    /// hash) before reuse. Distinct from <see cref="VerifyingPackages"/> so the
    /// UI can surface that a forced recheck happened after a failed init.
    /// </summary>
    RecheckingPackages = 5,
    InitializingDatabase = 6,
    StartingService = 7,
    WaitingForService = 8,
    ApplyingSchema = 9,
    Ready = 10,
}

/// <summary>Progress notification raised while the local Directus runtime starts.</summary>
public sealed record DirectusStartupProgress(
    DirectusStartupStage Stage,
    string Detail,
    bool UsedFastPath = false);
