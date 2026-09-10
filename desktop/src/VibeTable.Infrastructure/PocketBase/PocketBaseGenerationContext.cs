using System;

namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Identifies one in-process PocketBase process generation and its fixed
/// private connection material. The identifier is a correlation token, not a credential.
/// </summary>
public sealed class PocketBaseGenerationContext
{
    public PocketBaseGenerationContext(
        ulong generationId,
        PocketBaseAdminContext adminContext)
    {
        if (generationId == 0)
            throw new ArgumentOutOfRangeException(nameof(generationId));
        GenerationId = generationId;
        AdminContext = adminContext
            ?? throw new ArgumentNullException(nameof(adminContext));
    }

    public ulong GenerationId { get; }
    public PocketBaseAdminContext AdminContext { get; }

    public override string ToString()
        => $"{nameof(PocketBaseGenerationContext)} {{ GenerationId = {GenerationId} }}";
}
