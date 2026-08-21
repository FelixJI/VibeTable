namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Secret-free timing evidence for the most recent sidecar startup attempt.
/// Durations are phase-local rather than cumulative.
/// </summary>
public sealed record PocketBaseStartupTimings(
    TimeSpan? SpawnDuration,
    TimeSpan? ReadyRecordDuration,
    TimeSpan? HealthDuration,
    string LastStage);
