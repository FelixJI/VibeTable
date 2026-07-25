using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>Closed provider-neutral relation and Lookup capability surface.</summary>
public interface IRelationLookupRpcGateway
{
    Task<JsonElement> DescribeSchemaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> SearchRelationTargetsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> UpdateSingleRelationAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewRelationDeltaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyRelationDeltaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListLookupsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ValidateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> UpdateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> QueryLookupsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewRelationChangeAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyRelationChangeAsync(JsonElement parameters, CancellationToken token);
}
