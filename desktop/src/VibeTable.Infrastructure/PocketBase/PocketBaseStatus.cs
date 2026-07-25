using System;

namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Secret-free lifecycle snapshot safe for host UI consumption.
/// </summary>
public sealed record PocketBaseStatus(
    PocketBaseState State,
    Uri? BaseAddress,
    bool AdminAvailable,
    int? ExitCode,
    string? Error);
