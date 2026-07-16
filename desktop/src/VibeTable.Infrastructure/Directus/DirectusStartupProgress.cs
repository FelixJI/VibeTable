namespace VibeTable.Infrastructure.Directus;

/// <summary>Fine-grained stages within the broad <see cref="DirectusState.Starting"/> state.</summary>
public enum DirectusStartupStage
{
    PreparingRuntime = 0,
    CheckingPackages = 1,
    InstallingPackages = 2,
    VerifyingPackages = 3,
    RepairingPackages = 4,
    InitializingDatabase = 5,
    StartingService = 6,
    WaitingForService = 7,
    ApplyingSchema = 8,
    Ready = 9,
}

/// <summary>Progress notification raised while the local Directus runtime starts.</summary>
public sealed record DirectusStartupProgress(
    DirectusStartupStage Stage,
    string Detail,
    bool UsedFastPath = false);
