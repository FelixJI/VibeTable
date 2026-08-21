namespace VibeTable.Desktop.Services;

/// <summary>
/// Explicit composition marker for hosts/tests that intentionally omit the
/// database-open route. It prevents a partial route whose workspace admission
/// succeeds without the matching grid persistence binding.
/// </summary>
public sealed class NoDatabaseOpenRoute
{
    private NoDatabaseOpenRoute()
    {
    }

    public static NoDatabaseOpenRoute Instance { get; } = new();
}
