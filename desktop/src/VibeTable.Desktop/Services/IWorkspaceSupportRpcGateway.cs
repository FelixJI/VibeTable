using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Backend-owned support operations used by the product workspace. Business
/// records and paste plans stay in Go; only per-user grid state crosses this adapter.
/// </summary>
public interface IWorkspaceSupportRpcGateway
{
    Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token);

    Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token);

}
