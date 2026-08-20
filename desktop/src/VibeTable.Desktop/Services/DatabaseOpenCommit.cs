using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns the state-first database-open commit shared by renderer and host
/// producers. Both workspace discovery state and grid persistence binding are
/// admitted before the terminal is enqueued; any exception rolls both back.
/// </summary>
internal sealed class DatabaseOpenCommit : IDisposable
{
    private readonly TableWorkspaceService.DatabaseOpenAdmission _workspace;
    private readonly GridStateCoordinator.DatabaseBindingAdmission _grid;
    private int _completed;

    private DatabaseOpenCommit(
        TableWorkspaceService.DatabaseOpenAdmission workspace,
        GridStateCoordinator.DatabaseBindingAdmission grid)
    {
        _workspace = workspace;
        _grid = grid;
    }

    public static DatabaseOpenCommit Begin(
        TableWorkspaceService workspace,
        GridStateCoordinator grid,
        string source,
        DatabaseOpenResult result)
    {
        ArgumentNullException.ThrowIfNull(grid);
        TableWorkspaceService.DatabaseOpenAdmission workspaceAdmission =
            workspace.BeginDatabaseOpenAdmission(source, result);
        try
        {
            GridStateCoordinator.DatabaseBindingAdmission gridAdmission =
                grid.BeginDatabaseBinding(source);
            return new DatabaseOpenCommit(workspaceAdmission, gridAdmission);
        }
        catch
        {
            workspaceAdmission.Dispose();
            throw;
        }
    }

    public void Enqueue(Action terminal)
    {
        ArgumentNullException.ThrowIfNull(terminal);
        terminal();
        _grid.Complete();
        _workspace.Complete();
        Interlocked.Exchange(ref _completed, 1);
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _completed, 1) != 0) return;
        _grid.Dispose();
        _workspace.Dispose();
    }
}
