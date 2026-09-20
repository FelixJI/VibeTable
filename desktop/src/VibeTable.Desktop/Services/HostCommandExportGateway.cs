using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>A native selection plus one export; never an arbitrary retired-epoch RPC.</summary>
internal interface IHostCommandExportGateway
{
    Task<JsonElement> RegisterExportTargetAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ExecuteExportAsync(JsonElement parameters, CancellationToken token);
}
