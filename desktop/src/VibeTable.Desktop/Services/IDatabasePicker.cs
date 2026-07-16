using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Resolves the logical data-source identifier used by the workspace.
/// </summary>
/// <remarks>
/// <para>
/// The renderer cannot supply a local path or endpoint. Production binds this
/// interface to the configured Directus source identifier.
/// </para>
/// <para>
/// Keeping resolution behind an interface makes the WebView boundary
/// deterministic and independently testable.
/// </para>
/// </remarks>
public interface IDatabasePicker
{
    /// <summary>
    /// Returns the configured source identifier, or null when unavailable.
    /// </summary>
    Task<string?> PickDatabaseAsync();
}
