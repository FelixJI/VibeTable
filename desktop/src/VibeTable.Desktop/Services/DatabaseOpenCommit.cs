using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the state-first database-open commit shared by renderer and host
/// producers. The workspace discovery admission is committed before the
/// terminal is enqueued and rolled back when the terminal fails.
/// </summary>
internal sealed class DatabaseOpenCommit : IDisposable
{
    private readonly TableWorkspaceService.DatabaseOpenAdmission _workspace;
    private int _completed;

    private DatabaseOpenCommit(
        TableWorkspaceService.DatabaseOpenAdmission workspace)
    {
        _workspace = workspace;
    }

    public static DatabaseOpenCommit Begin(
        TableWorkspaceService workspace,
        string source,
        DatabaseOpenResult result)
    {
        return new DatabaseOpenCommit(
            workspace.BeginDatabaseOpenAdmission(source, result));
    }

    public void Enqueue(Action terminal)
    {
        ArgumentNullException.ThrowIfNull(terminal);
        terminal();
        _workspace.Complete();
        Interlocked.Exchange(ref _completed, 1);
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _completed, 1) != 0) return;
        _workspace.Dispose();
    }
}
