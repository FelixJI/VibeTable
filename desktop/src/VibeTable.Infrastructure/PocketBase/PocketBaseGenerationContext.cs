namespace VibeTable.Infrastructure.PocketBase;

internal sealed class PocketBaseGenerationContext
{
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

    public override string ToString()
        => $"{nameof(PocketBaseGenerationContext)} " +
            $"{{ GenerationId = {GenerationId} }}";
}
